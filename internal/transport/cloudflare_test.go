package transport

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	cftunnel "go.mewis.me/cf-tunnel"
)

const testCloudflareToken = "0123456789abcdef0123456789abcdef0123456789abcdef"

func TestCloudflareRequiresIndependentBearer(t *testing.T) {
	for _, token := range []string{"", "short", strings.Repeat("x", 31), strings.Repeat("x", 32) + " ", strings.Repeat("x", 32) + "\n", strings.Repeat("x", 32) + "\x7f"} {
		if err := validateCloudflareBearerToken(token); err == nil || !strings.Contains(err.Error(), CloudflareBearerTokenEnv) || (token != "" && strings.Contains(err.Error(), token)) {
			t.Fatalf("weak bearer token accepted or exposed: %v", err)
		}
	}
	if err := validateCloudflareBearerToken(testCloudflareToken); err != nil {
		t.Fatalf("valid bearer rejected: %v", err)
	}
	t.Setenv(CloudflareBearerTokenEnv, "")
	t.Setenv("CF_TUNNEL_TOKEN", "")
	if err := Run(context.Background(), testServer(), "cloudflare"); err == nil || !strings.Contains(err.Error(), CloudflareBearerTokenEnv) {
		t.Fatalf("Cloudflare started without inbound auth: %v", err)
	}
	t.Setenv(CloudflareBearerTokenEnv, testCloudflareToken)
	t.Setenv("CF_TUNNEL_TOKEN", "managed-secret-do-not-log")
	for _, mode := range []string{"cloudflare", "cf"} {
		err := Run(context.Background(), testServer(), mode)
		if err == nil || !strings.Contains(err.Error(), "CF_TUNNEL_TOKEN") || strings.Contains(err.Error(), "managed-secret") {
			t.Fatalf("managed tunnel must fail closed in mode %q: %v", mode, err)
		}
	}
	t.Setenv("CF_TUNNEL_TOKEN", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, testServer(), "cf"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled startup should not provision a Quick Tunnel: %v", err)
	}
}

func TestCloudflareBearerHandler(t *testing.T) {
	var served atomic.Int32
	handler := cloudflareBearerHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}), testCloudflareToken)
	tests := []struct {
		name   string
		header []string
		ok     bool
	}{
		{"missing", nil, false},
		{"basic", []string{"Basic " + testCloudflareToken}, false},
		{"wrong token", []string{"Bearer " + strings.Repeat("z", len(testCloudflareToken))}, false},
		{"prefix token", []string{"Bearer " + testCloudflareToken[:40]}, false},
		{"suffix token", []string{"Bearer " + testCloudflareToken + "x"}, false},
		{"repeated authorization", []string{"Bearer " + testCloudflareToken, "Bearer " + testCloudflareToken}, false},
		{"trailing spaces", []string{"Bearer " + testCloudflareToken + " "}, false},
		{"valid", []string{"Bearer " + testCloudflareToken}, true},
		{"case-insensitive scheme", []string{"bEaReR " + testCloudflareToken}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "https://mcp.trycloudflare.com/", nil)
			for _, value := range tc.header {
				r.Header.Add("Authorization", value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if tc.ok {
				if w.Code != http.StatusNoContent {
					t.Fatalf("authorized request got %d", w.Code)
				}
			} else {
				if w.Code != http.StatusUnauthorized || w.Header().Get("WWW-Authenticate") != "Bearer" || strings.Contains(w.Body.String(), testCloudflareToken) {
					t.Fatalf("unauthorized request not rejected safely: %d", w.Code)
				}
			}
		})
	}
	if served.Load() != 2 {
		t.Fatalf("expected two authenticated requests, got %d", served.Load())
	}
}

func TestCloudflareAuthenticatedStreamableHTTP(t *testing.T) {
	mcpHandler := cloudflareBearerHandler(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return testServer()
	}, &mcp.StreamableHTTPOptions{}), testCloudflareToken)
	handler := http.NewServeMux()
	handler.Handle("/mcp", mcpHandler)
	outside := httptest.NewRecorder()
	handler.ServeHTTP(outside, httptest.NewRequest(http.MethodPost, "https://safe.trycloudflare.com/", nil))
	if outside.Code != http.StatusNotFound {
		t.Fatalf("MCP endpoint must only be available at /mcp; root status=%d", outside.Code)
	}
	const request = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"1.0"}}}`
	for _, tc := range []struct {
		token string
		want  int
	}{
		{"", http.StatusUnauthorized},
		{testCloudflareToken, http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodPost, "https://safe.trycloudflare.com/mcp", strings.NewReader(request))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json, text/event-stream")
		if tc.token != "" {
			r.Header.Set("Authorization", "Bearer "+tc.token)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("initialize status %d, want %d: %s", w.Code, tc.want, w.Body.String())
		}
		if tc.want == http.StatusOK && !strings.Contains(w.Body.String(), "result") {
			t.Fatalf("MCP initialize did not return JSON-RPC result: %s", w.Body.String())
		}
	}
}

type fakeCloudflare struct {
	ctx    context.Context
	ready  chan error
	stop   chan error
	url    string
	waited atomic.Int32
}

func newFakeCloudflare() *fakeCloudflare {
	return &fakeCloudflare{ready: make(chan error, 1), stop: make(chan error, 1), url: "https://safe.trycloudflare.com"}
}

func (f *fakeCloudflare) WaitUntilReady(ctx context.Context) error {
	select {
	case err := <-f.ready:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeCloudflare) Wait() error {
	f.waited.Add(1)
	select {
	case err := <-f.stop:
		return err
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}
func (f *fakeCloudflare) PublicURL() string { return f.url }

func fakeCloudflareFactory(f *fakeCloudflare) cloudflareStart {
	return func(ctx context.Context, cfg cftunnel.Config) (cloudflareClient, error) {
		if cfg.Handler == nil || cfg.OriginURL != "" || cfg.Token != "" || cfg.QuickService != "" {
			return nil, errors.New("invalid in-memory Quick Tunnel config")
		}
		f.ctx = ctx
		return f, nil
	}
}

type cloudflareEvents struct{ lines chan string }

func (e cloudflareEvents) Write(p []byte) (int, error) {
	e.lines <- string(p)
	return len(p), nil
}
func expectCloudflareEvent(t *testing.T, events cloudflareEvents, want string) {
	t.Helper()
	select {
	case got := <-events.lines:
		if !strings.Contains(got, want) {
			t.Fatalf("unexpected lifecycle event %q (want %q)", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("missing Cloudflare lifecycle event: %s", want)
	}
}

func TestCloudflareLifecycleWaitsForReadinessAndCancelsCleanly(t *testing.T) {
	f := newFakeCloudflare()
	events := cloudflareEvents{lines: make(chan string, 4)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runCloudflareLifecycle(ctx, http.NotFoundHandler(), fakeCloudflareFactory(f), events) }()
	expectCloudflareEvent(t, events, "connecting...")
	// A slow readiness has no hard deadline and does not stop the tunnel.
	select {
	case err := <-done:
		t.Fatalf("tunnel exited while waiting for readiness: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	f.ready <- nil
	expectCloudflareEvent(t, events, "ready: https://safe.trycloudflare.com/mcp")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("clean cancellation failed: %v", err)
	}
	expectCloudflareEvent(t, events, "stopped.")
	if f.waited.Load() != 1 {
		t.Fatalf("Wait called %d times", f.waited.Load())
	}
}

func TestCloudflareLifecycleFailureCases(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeCloudflare)
		want  string
	}{
		{"readiness error", func(f *fakeCloudflare) { f.ready <- errors.New("secret-readiness-detail") }, "wait for Cloudflare readiness"},
		{"invalid URL", func(f *fakeCloudflare) { f.url = "https://attacker.example/"; f.ready <- nil }, "invalid Quick Tunnel URL"},
		{"stopped before ready", func(f *fakeCloudflare) { f.stop <- errors.New("secret-runtime-detail") }, "before readiness"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeCloudflare()
			tc.setup(f)
			var output bytes.Buffer
			err := runCloudflareLifecycle(context.Background(), http.NotFoundHandler(), fakeCloudflareFactory(f), &output)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret-") || strings.Contains(output.String(), "secret-") {
				t.Fatalf("error not reported safely: %v output=%q", err, output.String())
			}
		})
	}
	t.Run("start error", func(t *testing.T) {
		var output bytes.Buffer
		err := runCloudflareLifecycle(context.Background(), http.NotFoundHandler(), func(context.Context, cftunnel.Config) (cloudflareClient, error) {
			return nil, errors.New("secret-start-detail")
		}, &output)
		if err == nil || !strings.Contains(err.Error(), "start Cloudflare tunnel") || strings.Contains(err.Error(), "secret-") || strings.Contains(output.String(), "secret-") {
			t.Fatalf("unsafe startup failure: %v output=%q", err, output.String())
		}
	})
	t.Run("unexpected stop after ready", func(t *testing.T) {
		f := newFakeCloudflare()
		events := cloudflareEvents{lines: make(chan string, 4)}
		done := make(chan error, 1)
		go func() {
			done <- runCloudflareLifecycle(context.Background(), http.NotFoundHandler(), fakeCloudflareFactory(f), events)
		}()
		expectCloudflareEvent(t, events, "connecting...")
		f.ready <- nil
		expectCloudflareEvent(t, events, "ready:")
		f.stop <- errors.New("secret-remote-detail")
		err := <-done
		if !errors.Is(err, ErrCloudflareStopped) || !strings.Contains(err.Error(), "after readiness") || strings.Contains(err.Error(), "secret-") {
			t.Fatalf("unexpected shutdown error not safely reported: %v", err)
		}
		expectCloudflareEvent(t, events, "disconnected.")
	})
}

func TestCloudflarePublicURLValidation(t *testing.T) {
	for _, raw := range []string{"https://valid.trycloudflare.com", "https://valid.trycloudflare.com/"} {
		if !validCloudflarePublicURL(raw) {
			t.Fatalf("valid URL rejected: %q", raw)
		}
	}
	for _, raw := range []string{"", "http://valid.trycloudflare.com", "https://trycloudflare.com", "https://valid.trycloudflare.com.evil.com", "https://user:pass@valid.trycloudflare.com", "https://valid.trycloudflare.com/?token=x", "https://valid.trycloudflare.com/path", "https://valid.trycloudflare.com:443", "https://valid.trycloudflare.com/#secret", "https://valid.trycloudflare.com\nBAD"} {
		if validCloudflarePublicURL(raw) {
			t.Fatalf("untrusted URL accepted: %q", raw)
		}
	}
}

var _ io.Writer = cloudflareEvents{}
