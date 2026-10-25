package mcptools

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.mewis.me/ig-mcp/internal/instagram"
)

func TestPingViaMCPWithoutInstagramSession(t *testing.T) {
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("IG_MCP_READ_ONLY", "true")
	t.Setenv("IG_MCP_DENY_TOOLS", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := mcp.NewServer(&mcp.Implementation{Name: "ping-test", Version: "1"}, nil)
	svc := instagram.New()
	wantInstagram := svc.Status()
	Register(server, svc)
	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "ping-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ig_ping", Arguments: map[string]any{}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("ping must work without Instagram session: %+v, %v", result, err)
	}
	fields, ok := result.StructuredContent.(map[string]any)
	if !ok || fields["mcp"] != "ok" || fields["instagram"] != wantInstagram || fields["realtime"] != "disconnected" {
		t.Fatalf("unexpected ping fields: %#v", result.StructuredContent)
	}
	if len(fields) != 3 {
		t.Fatalf("ping should stay minimal and not leak account data: %#v", fields)
	}

	// The configured denylist is still honored for health-check tools.
	t.Setenv("IG_MCP_DENY_TOOLS", "ig_ping")
	blocked, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ig_ping", Arguments: map[string]any{}})
	if err != nil || blocked == nil || !blocked.IsError {
		t.Fatalf("explicitly disabled ig_ping should return a tool error: %+v, %v", blocked, err)
	}
}

func TestPingWithoutServiceStillProvesMCPIsAlive(t *testing.T) {
	t.Setenv("IG_MCP_DENY_TOOLS", "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server := mcp.NewServer(&mcp.Implementation{Name: "ping-test", Version: "1"}, nil)
	Register(server, nil)
	st, ct := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "ping-client", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "ig_ping", Arguments: map[string]any{}})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("ping should work even without Service: %+v, %v", result, err)
	}
	fields, ok := result.StructuredContent.(map[string]any)
	if !ok || fields["mcp"] != "ok" || fields["instagram"] != "unavailable" || fields["realtime"] != "unavailable" {
		t.Fatalf("unexpected ping fields: %#v", result.StructuredContent)
	}
}
