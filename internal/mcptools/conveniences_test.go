package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"

	"go.mewis.me/ig-mcp/internal/instagram"
)

func unreadPage(ids []string, unread []bool, cursors []string, next string) slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]] {
	edges := make([]slidetypes.Node[slidetypes.WrappedThreadInfo], 0, len(ids))
	for i, id := range ids {
		edges = append(edges, slidetypes.Node[slidetypes.WrappedThreadInfo]{Cursor: cursors[i], Node: slidetypes.WrappedThreadInfo{
			AsIGDirectThread: &slidetypes.ThreadInfo{ID: id, MarkedAsUnread: unread[i]},
		}})
	}
	return slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]]{Edges: edges,
		PageInfo: slidetypes.PageInfo{HasNextPage: next != "", EndCursor: next}}
}

func TestUnreadThreadFilterScansBeforeLimitAndPreservesCursor(t *testing.T) {
	pages := map[string]slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]]{
		"":   unreadPage([]string{"one", "two", "three"}, []bool{false, true, false}, []string{"c1", "c2", "c3"}, "c3"),
		"c3": unreadPage([]string{"four", "five", "six"}, []bool{true, true, false}, []string{"c4", "c5", "c6"}, ""),
		"c4": unreadPage([]string{"five", "six"}, []bool{true, false}, []string{"c5", "c6"}, ""),
	}
	var fetched []string
	fetch := func(_ context.Context, in inboxInput) (slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]], error) {
		fetched = append(fetched, in.Cursor)
		p, ok := pages[in.Cursor]
		if !ok {
			return p, fmt.Errorf("unexpected cursor %q", in.Cursor)
		}
		return p, nil
	}
	threads, next, more, err := listUnreadThreads(context.Background(), inboxInput{Limit: 2, UnreadOnly: true}, fetch)
	if err != nil || !more || next != "c4" || len(threads) != 2 || threads[0].ID != "two" || threads[1].ID != "four" {
		t.Fatalf("first page: threads=%+v cursor=%q more=%v err=%v", threads, next, more, err)
	}
	if !reflect.DeepEqual(fetched, []string{"", "c3"}) {
		t.Fatalf("first page must span upstream pages: %v", fetched)
	}
	threads, next, more, err = listUnreadThreads(context.Background(), inboxInput{Limit: 2, UnreadOnly: true, Cursor: next}, fetch)
	if err != nil || more || next != "" || len(threads) != 1 || threads[0].ID != "five" {
		t.Fatalf("second page: threads=%+v cursor=%q more=%v err=%v", threads, next, more, err)
	}
}

func TestUnreadThreadFilterStopsSafelyAtScanBudget(t *testing.T) {
	called := 0
	fetch := func(_ context.Context, in inboxInput) (slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]], error) {
		called++
		return unreadPage([]string{"read"}, []bool{false}, []string{fmt.Sprintf("c%d", called)}, fmt.Sprintf("c%d", called)), nil
	}
	threads, cursor, more, err := listUnreadThreads(context.Background(), inboxInput{Limit: 1}, fetch)
	if err != nil || len(threads) != 0 || !more || cursor != "c20" || called != 20 {
		t.Fatalf("scan budget: threads=%v cursor=%s more=%v calls=%d err=%v", threads, cursor, more, called, err)
	}
	called = 0
	bad := unreadPage([]string{"a", "b"}, []bool{true, true}, []string{"", "c2"}, "")
	_, _, _, err = listUnreadThreads(context.Background(), inboxInput{Limit: 1}, func(context.Context, inboxInput) (slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]], error) {
		called++
		return bad, nil
	})
	if err == nil || called != 1 {
		t.Fatalf("missing cursor for truncated page must fail, calls=%d err=%v", called, err)
	}
	called = 0
	_, _, _, err = listUnreadThreads(context.Background(), inboxInput{Cursor: "bad cursor"}, func(context.Context, inboxInput) (slidetypes.Edged[slidetypes.Node[slidetypes.WrappedThreadInfo]], error) {
		called++
		return bad, nil
	})
	if err == nil || called != 0 {
		t.Fatalf("invalid cursor must fail before fetching, calls=%d err=%v", called, err)
	}
}

func TestThreadIDResolutionNoNetworkForProvidedIDs(t *testing.T) {
	called := 0
	lookup := func(id string) (*slidetypes.ThreadInfo, error) {
		called++
		return &slidetypes.ThreadInfo{ID: id, ThreadFBID: "900", ThreadID: "800"}, nil
	}
	for _, tt := range []struct{ thread, supplied, field, want string }{
		{"", "900", "thread_fbid", "900"},
		{"t1", "000900", "thread_fbid", "900"},
		{"", "800", "thread_long_id", "800"},
		{"t1", "", "thread_fbid", "900"},
		{"t1", "", "thread_long_id", "800"},
	} {
		got, err := resolveThreadIdentifier(tt.thread, tt.supplied, tt.field, lookup)
		if err != nil || got != tt.want {
			t.Fatalf("resolve %+v => %q / %v", tt, got, err)
		}
	}
	if called != 2 {
		t.Fatalf("direct IDs must not fetch the thread; got %d lookups", called)
	}
	for _, tt := range []struct{ thread, supplied, field string }{
		{"", "", "thread_fbid"}, {"bad id", "", "thread_fbid"},
		{"t1", "invalid", "thread_fbid"}, {"t1", "-1", "thread_fbid"},
	} {
		if _, err := resolveThreadIdentifier(tt.thread, tt.supplied, tt.field, lookup); err == nil {
			t.Fatalf("accepted invalid identifiers: %+v", tt)
		}
	}
	if called != 2 {
		t.Fatalf("invalid IDs triggered lookup: %d", called)
	}
	if _, err := resolveThreadIdentifier("t1", "", "thread_fbid", func(string) (*slidetypes.ThreadInfo, error) {
		return &slidetypes.ThreadInfo{ID: "other", ThreadFBID: "900"}, nil
	}); err == nil {
		t.Fatal("lookup must reject a mismatched conversation")
	}
}

func TestMediaResolutionVerifiesOptionalFBID(t *testing.T) {
	called := 0
	lookup := func(id string) (*slidetypes.ThreadInfo, error) {
		called++
		return &slidetypes.ThreadInfo{ID: id, ThreadFBID: "900"}, nil
	}
	for _, supplied := range []string{"", "900", "000900"} {
		got, err := resolveMediaIdentifier("t1", supplied, lookup)
		if err != nil || got != "900" {
			t.Fatalf("media fbid %q => %s, %v", supplied, got, err)
		}
	}
	for _, supplied := range []string{"901", "bad"} {
		if _, err := resolveMediaIdentifier("t1", supplied, lookup); err == nil {
			t.Fatalf("accepted mismatched or invalid media fbid %q", supplied)
		}
	}
	if called != 4 {
		t.Fatalf("invalid media fbid must not trigger a GET, calls=%d", called)
	}
	if _, err := resolveMediaIdentifier("bad id", "", lookup); err == nil || called != 4 {
		t.Fatalf("invalid thread id must fail before GET: calls=%d err=%v", called, err)
	}
}

func TestWhoamiUnavailableAndPolicyOverMCP(t *testing.T) {
	t.Setenv("IG_MCP_DENY_TOOLS", "")
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "whoami-test", Version: "v1"}, nil)
	Register(server, instagram.New())
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ig_whoami", Arguments: map[string]any{}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("whoami offline failed: %+v %v", result, err)
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || !strings.Contains(string(data), `"available":false`) || strings.Contains(string(data), "cookie") || strings.Contains(string(data), "token") {
		t.Fatalf("whoami must be unavailable and safe: %s %v", data, err)
	}
	if string(data) != `{"available":false}` {
		t.Fatalf("whoami unavailable must not expose account fields: %s", data)
	}
	t.Setenv("IG_MCP_DENY_TOOLS", "ig_whoami")
	blocked, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ig_whoami", Arguments: map[string]any{}})
	if err != nil || blocked == nil || !blocked.IsError {
		t.Fatalf("whoami denylist not respected: %+v %v", blocked, err)
	}
}
