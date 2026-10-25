package mcptools

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mewis.me/ig-mcp/internal/instagram"
)

func TestPollEventsWithoutAuthenticatedSession(t *testing.T) {
	t.Setenv("IG_MCP_DENY_TOOLS", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	svc := instagram.New() // No active session: expiry events must remain readable.
	server := mcp.NewServer(&mcp.Implementation{Name: "ig-test", Version: "v1"}, nil)
	Register(server, svc)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v1"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(args map[string]any) map[string]any {
		t.Helper()
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ig_poll_events", Arguments: args})
		if err != nil || result == nil || result.IsError {
			t.Fatalf("polling without session must work: result=%v, err=%v", result, err)
		}
		out, ok := result.StructuredContent.(map[string]any)
		if !ok {
			t.Fatalf("unexpected response type %T", result.StructuredContent)
		}
		return out
	}
	first := call(map[string]any{})
	gen, ok := first["generation"].(string)
	if !ok || gen == "" {
		t.Fatalf("missing service epoch: %v", first)
	}
	stale := call(map[string]any{"generation": "stale-generation"})
	if stale["reset_required"] != true || stale["generation"] != gen {
		t.Fatalf("old epoch must request reset: %v", stale)
	}
	future := call(map[string]any{"generation": gen, "after_sequence": 1})
	if future["reset_required"] != true {
		t.Fatalf("unknown sequence must request reset: %v", future)
	}
}
