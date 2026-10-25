package mcptools

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
)

func TestBoundedLimit(t *testing.T) {
	for _, tt := range []struct {
		in, want int
		valid    bool
	}{
		{0, 20, true}, {1, 1, true}, {100, 100, true},
		{-1, 0, false}, {101, 0, false},
	} {
		got, err := boundedLimit(tt.in)
		if (err == nil) != tt.valid || (tt.valid && got != tt.want) {
			t.Fatalf("boundedLimit(%d)=(%d,%v), expected (%d,valid=%v)", tt.in, got, err, tt.want, tt.valid)
		}
	}
}

func TestPageDoesNotSkipTruncatedEntries(t *testing.T) {
	src := slidetypes.Edged[slidetypes.Node[string]]{
		Edges: []slidetypes.Node[string]{
			{Node: "first", Cursor: "cursor-one"},
			{Node: "second", Cursor: "cursor-two"},
			{Node: "third", Cursor: "cursor-three"},
		},
		PageInfo: slidetypes.PageInfo{HasNextPage: true, EndCursor: "cursor-end"},
	}
	got, next, more, err := page(src, 2, "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"first", "second"}) || !more || next != "cursor-two" {
		t.Fatalf("truncated page: items=%v cursor=%q more=%v", got, next, more)
	}
	got, next, more, err = page(src, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || !more || next != "cursor-end" {
		t.Fatalf("full page: items=%v cursor=%q more=%v", got, next, more)
	}
	src.Edges[0].Cursor = ""
	got, next, more, err = page(src, 1, "")
	if err == nil || len(got) != 0 || more || next != "" {
		t.Fatalf("truncation without a usable edge cursor must fail: items=%v cursor=%q more=%v err=%v", got, next, more, err)
	}
	src.PageInfo.HasNextPage = false
	_, next, more, err = page(src, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if more || next != "" {
		t.Fatalf("terminal page: cursor=%q more=%v", next, more)
	}
	src.PageInfo.HasNextPage = true
	if _, _, _, err := page(src, 3, "cursor-end"); err == nil {
		t.Fatal("nonadvancing cursor must fail")
	}
}

func TestUniqueInboxUsersExcludesViewerAndDeduplicates(t *testing.T) {
	owner := &slidetypes.User{ID: "me"}
	first := &slidetypes.User{ID: "alice", Username: "a"}
	second := &slidetypes.User{ID: "bob", Username: "b"}
	threads := []slidetypes.WrappedThreadInfo{
		{AsIGDirectThread: &slidetypes.ThreadInfo{ID: "t1", ViewerID: "me", Users: []*slidetypes.User{owner, first, second}}},
		{AsIGDirectThread: &slidetypes.ThreadInfo{ID: "t2", Viewer: owner, Users: []*slidetypes.User{first, owner}}},
		{AsIGDirectThread: nil},
	}
	got := uniqueInboxUsers(threads)
	if len(got) != 2 || got[0].ID != "alice" || got[1].ID != "bob" ||
		!reflect.DeepEqual(got[0].ThreadIDs, []string{"t1", "t2"}) ||
		!reflect.DeepEqual(got[1].ThreadIDs, []string{"t1"}) {
		t.Fatalf("users: %+v", got)
	}
}

func TestInputValidation(t *testing.T) {
	if _, err := required("  ", "thread_id"); err == nil {
		t.Fatal("accepted empty ID")
	}
	if _, err := required(strings.Repeat("x", 513), "thread_id"); err == nil {
		t.Fatal("accepted oversize ID")
	}
	if _, err := requiredText(strings.Repeat("x", 10001), "text"); err == nil {
		t.Fatal("accepted oversize text")
	}
	if err := checkCursor(strings.Repeat("x", 4097)); err == nil {
		t.Fatal("accepted oversize cursor")
	}
	ids, err := validIDs([]string{" alice ", "alice", "bob"}, "ids", 3)
	if err != nil || !reflect.DeepEqual(ids, []string{"alice", "bob"}) {
		t.Fatalf("ids: %v, %v", ids, err)
	}
	if _, err := validIDs([]string{"x", ""}, "ids", 3); err == nil {
		t.Fatal("accepted empty member")
	}
	if _, err := pinMessageRequest(pinMessageInput{ThreadFBID: "t"}); err == nil {
		t.Fatal("accepted empty message id")
	}
}

func TestRegisterToolsAndErrorContract(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "ig-test", Version: "v1"}, nil)
	Register(server, nil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Tools) != 39 {
		t.Fatalf("registered %d tools, want 39", len(list.Tools))
	}
	seen := make(map[string]bool)
	for _, tool := range list.Tools {
		if seen[tool.Name] {
			t.Fatalf("duplicate tool %s", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Name == "login" || tool.Name == "logout" || tool.Name == "ig_login" || tool.Name == "ig_logout" {
			t.Fatalf("login/logout unexpectedly exposed: %s", tool.Name)
		}
		if tool.Annotations == nil {
			t.Fatalf("missing annotations on %s", tool.Name)
		}
		if tool.Name == "ig_delete_thread" && (tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint) {
			t.Fatal("delete thread missing destructiveHint")
		}
		if tool.Name == "ig_remove_reaction" || tool.Name == "ig_edit_message" {
			if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
				t.Fatalf("%s must advertise destructive mutation", tool.Name)
			}
		}
		if tool.Name == "ig_list_threads" || tool.Name == "ig_get_thread" || tool.Name == "ig_poll_events" {
			encoded, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			schema := string(encoded)
			if !strings.Contains(schema, "\"type\":\"object\"") {
				t.Fatalf("%s lacks object result schema: %s", tool.Name, schema)
			}
			if tool.Name == "ig_list_threads" && (!strings.Contains(schema, "participants") || strings.Contains(schema, "slide_messages")) {
				t.Fatalf("list_threads schema must be compact: %s", schema)
			}
			if tool.Name == "ig_poll_events" && !strings.Contains(schema, "generation") {
				t.Fatalf("poll_events schema missing generation: %s", schema)
			}
		}
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ig_list_threads", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatal("missing Instagram session should yield a tool error")
	}
}
