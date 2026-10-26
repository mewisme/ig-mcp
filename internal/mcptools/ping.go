package mcptools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.mewis.me/ig-mcp/internal/instagram"
)

type pingInput struct{}

type pingOutput struct {
	MCP       string `json:"mcp" jsonschema:"MCP server status; ok confirms the tool request completed"`
	Instagram string `json:"instagram" jsonschema:"Instagram authentication or session state"`
	Realtime  string `json:"realtime" jsonschema:"Instagram realtime socket state"`
}

type whoamiInput struct{}

type whoamiOutput struct {
	Available bool   `json:"available" jsonschema:"Whether a verified Instagram account is available locally"`
	ID        string `json:"id,omitempty" jsonschema:"Verified Instagram account ID"`
	Username  string `json:"username,omitempty" jsonschema:"Verified Instagram username"`
	FullName  string `json:"full_name,omitempty" jsonschema:"Verified Instagram full name"`
}

// Whoami only reads the identity already verified during session startup.
// A missing/expired session yields a safe, empty response without network I/O.
func registerWhoamiTool(server *mcp.Server, svc *instagram.Service) {
	readOnly, destructive, openWorld := true, false, false
	mcp.AddTool[whoamiInput, whoamiOutput](server, &mcp.Tool{
		Name:        "ig_whoami",
		Description: "Return the locally verified Instagram account ID, username and full name; unavailable when not authenticated. No network request.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint: readOnly, DestructiveHint: &destructive, OpenWorldHint: &openWorld,
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ whoamiInput) (*mcp.CallToolResult, whoamiOutput, error) {
		if err := authorizeTool("ig_whoami", true, false); err != nil {
			return nil, whoamiOutput{}, safeToolError("ig_whoami", err)
		}
		if svc == nil {
			return nil, whoamiOutput{}, nil
		}
		account, ok := svc.VerifiedAccount()
		if !ok {
			return nil, whoamiOutput{}, nil
		}
		return nil, whoamiOutput{Available: true, ID: account.ID, Username: account.Username, FullName: account.FullName}, nil
	})
}

// Ping stays available even when Instagram authentication or realtime fails.
// It does not make network requests or expose account credentials.
func registerPingTool(server *mcp.Server, svc *instagram.Service) {
	readOnly, destructive, openWorld := true, false, false
	mcp.AddTool[pingInput, pingOutput](server, &mcp.Tool{
		Name:        "ig_ping",
		Description: "Check whether the Instagram MCP server responds. Also report local Instagram session and realtime states without contacting Instagram.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    readOnly,
			DestructiveHint: &destructive,
			OpenWorldHint:   &openWorld,
		},
	}, func(_ context.Context, _ *mcp.CallToolRequest, _ pingInput) (*mcp.CallToolResult, pingOutput, error) {
		if err := authorizeTool("ig_ping", true, false); err != nil {
			return nil, pingOutput{}, safeToolError("ig_ping", err)
		}
		out := pingOutput{MCP: "ok", Instagram: "unavailable", Realtime: "unavailable"}
		if svc != nil {
			out.Instagram = svc.Status()
			out.Realtime = svc.RealtimeStatus()
		}
		return nil, out, nil
	})
}
