package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
)

func TestMCPErrorRedactionAndValidation(t *testing.T) {
	secret := "sessionid=do-not-disclose"
	for _, upstream := range []error{
		fmt.Errorf("HTTP 500 Authorization: %s", secret),
		fmt.Errorf("transport wrapped: %w", errors.New(secret)),
	} {
		safe := safeToolError("ig_send_text", upstream).Error()
		if strings.Contains(safe, secret) || !strings.Contains(safe, "redacted") {
			t.Fatalf("unsafe upstream error: %s", safe)
		}
	}
	if got := safeToolError("ig_list_threads", invalidf("limit must be between 1 and 100")).Error(); !strings.Contains(got, "limit must be") {
		t.Fatalf("lost safe validation error: %q", got)
	}
	if got := safeToolError("ig_list_threads", context.Canceled).Error(); !strings.Contains(got, "cancelled") {
		t.Fatalf("bad cancellation classification: %q", got)
	}
}

func TestStableRequestID(t *testing.T) {
	first, err := stableRequestID("my-retry-uuid-1", "ig_send_text")
	if err != nil {
		t.Fatal(err)
	}
	second, err := stableRequestID("my-retry-uuid-1", "ig_send_text")
	if err != nil || first != second {
		t.Fatalf("unstable retry ID: %q / %q: %v", first, second, err)
	}
	third, err := stableRequestID("my-retry-uuid-1", "ig_send_media")
	if err != nil || first == third {
		t.Fatalf("cross-operation retry collision: %q %q: %v", first, third, err)
	}
	for _, s := range []string{"a b", "a\nb", strings.Repeat("x", 129), "☃"} {
		if _, err := stableRequestID(s, "ig_send_text"); err == nil {
			t.Fatalf("accepted invalid retry key %q", s)
		}
	}
}

func TestMediaSignatureAndType(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x00\x00\x00\x00\x00")
	if got, err := checkedMediaType("photo.png", "image/png", png, false); err != nil || got != "image/png" {
		t.Fatalf("valid PNG rejected: %q: %v", got, err)
	}
	for _, tt := range []struct {
		filename, mime string
		data           []byte
		voice          bool
	}{
		{"photo.jpg", "image/jpeg", []byte("<script>alert(1)</script>"), false},
		{"photo.png", "image/jpeg", png, false},
		{"photo.png", "image/png", png, true},
		{"payload.svg", "image/svg+xml", []byte("<svg/>"), false},
		{"photo.png", "image/png; charset=utf-8", png, false},
	} {
		if _, err := checkedMediaType(tt.filename, tt.mime, tt.data, tt.voice); err == nil {
			t.Fatalf("invalid media accepted: %+v", tt)
		}
	}
}

func TestToolPolicies(t *testing.T) {
	t.Setenv("IG_MCP_READ_ONLY", "true")
	if err := authorizeTool("ig_send_text", false, false); err == nil {
		t.Fatal("write allowed in read-only mode")
	}
	if err := authorizeTool("ig_get_thread", true, false); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IG_MCP_READ_ONLY", "false")
	t.Setenv("IG_MCP_DENY_TOOLS", "ig_get_thread, ig_send_text")
	if err := authorizeTool("ig_get_thread", true, false); err == nil {
		t.Fatal("denylist not applied")
	}
	t.Setenv("IG_MCP_DENY_TOOLS", "")
	t.Setenv("IG_MCP_REQUIRE_DESTRUCTIVE_APPROVAL", "true")
	if err := authorizeTool("ig_delete_thread", false, true); err == nil {
		t.Fatal("destructive guard not enforced")
	}
	t.Setenv("IG_MCP_REQUIRE_DESTRUCTIVE_APPROVAL", "false")
	t.Setenv("IG_MCP_WRITE_RATE_LIMIT_PER_MINUTE", "1")
	if err := authorizeTool("ig_rate_limit_test", false, false); err != nil {
		t.Fatal(err)
	}
	if err := authorizeTool("ig_rate_limit_test", false, false); err == nil {
		t.Fatal("write rate limit not enforced")
	}
}

func TestThreadSummaryExcludesFullThreadAndUnrecognizedFields(t *testing.T) {
	owner := &slidetypes.User{ID: "me"}
	other := &slidetypes.User{ID: "person", Username: "sample"}
	msgs := &slidetypes.SlideMessages{}
	thread := &slidetypes.ThreadInfo{
		ID: "thread", ThreadFBID: "123", ThreadID: "long-id", ViewerID: "me",
		Viewer: owner, Users: []*slidetypes.User{owner, other}, SlideMessages: msgs,
		Unrecognized: map[string]any{"secret_payload": "should-not-appear"},
	}
	encoded, err := json.Marshal(summarizeThread(thread))
	if err != nil {
		t.Fatal(err)
	}
	output := string(encoded)
	if strings.Contains(output, "slide_messages") || strings.Contains(output, "secret_payload") || strings.Contains(output, "profile_pic_url") {
		t.Fatalf("summary exposed full upstream fields: %s", output)
	}
	if !strings.Contains(output, "\"thread_fbid\":\"123\"") || !strings.Contains(output, "\"username\":\"sample\"") {
		t.Fatalf("summary omitted essential routing/participant fields: %s", output)
	}
}

func TestIdentifiers(t *testing.T) {
	if value, err := positiveDecimalID(" 000123 ", "thread_fbid"); err != nil || value != "123" {
		t.Fatalf("decimal ID: %q %v", value, err)
	}
	for _, s := range []string{"0", "-1", "abc", "123\n4"} {
		if _, err := positiveDecimalID(s, "thread_fbid"); err == nil {
			t.Fatalf("accepted invalid numeric id %q", s)
		}
	}
	if _, err := requiredID("mid.a b", "message_id"); err == nil {
		t.Fatal("accepted malformed message ID")
	}
	if folder, err := normalizeInboxFolder(" general "); err != nil || folder != "GENERAL" {
		t.Fatalf("folder normalization: %q %v", folder, err)
	}
	if _, err := normalizeInboxFolder("INBOX;DROP"); err == nil {
		t.Fatal("accepted malformed folder")
	}
}

func TestIdempotencyConcurrentRetryAndConflict(t *testing.T) {
	input := sendTextInput{ThreadID: "thread123", Text: "hello", ClientRequestID: "dedupe-concurrent-123"}
	start := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := withIdempotency(context.Background(), "test-account", "ig_send_text", input, func() (toolOutput, error) {
				if calls.Add(1) == 1 {
					close(start)
					<-release
				}
				return toolOutput{"message_id": "ack"}, nil
			})
			if err != nil || out["message_id"] != "ack" {
				failures.Add(1)
			}
		}()
		if i == 0 {
			<-start
		}
	}
	close(release)
	wg.Wait()
	if failures.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("duplicate request executed %d times; failures=%d", calls.Load(), failures.Load())
	}
	changed := input
	changed.Text = "different payload"
	if _, err := withIdempotency(context.Background(), "test-account", "ig_send_text", changed, func() (toolOutput, error) {
		t.Fatal("conflicting retry must not invoke handler")
		return nil, nil
	}); err == nil {
		t.Fatal("different payload with same client_request_id was accepted")
	}
	if _, err := withIdempotency(context.Background(), "another-account", "ig_send_text", changed, func() (toolOutput, error) {
		return toolOutput{"message_id": "other-account"}, nil
	}); err != nil {
		t.Fatalf("other account incorrectly shared retry cache: %v", err)
	}
}

func TestMutationAcknowledgement(t *testing.T) {
	var missing *struct{}
	for _, out := range []toolOutput{
		{"message": missing},
		{"thread": nil},
		{"deleted_thread_id": ""},
		{"submitted": true, "validated": false},
		{"submitted": true, "validation_error": "verification unavailable"},
	} {
		if mutationAcknowledged(out) {
			t.Fatalf("ambiguous mutation was acknowledged: %#v", out)
		}
	}
	if !mutationAcknowledged(toolOutput{"user_id": "123", "blocked": true}) {
		t.Fatal("successful mutation not acknowledged")
	}
}
