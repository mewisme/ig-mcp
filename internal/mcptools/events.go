package mcptools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/mautrix-meta/pkg/instameow"

	"go.mewis.me/ig-mcp/internal/instagram"
)

type pollEventsInput struct {
	AfterSequence uint64 `json:"after_sequence,omitempty" jsonschema:"Last sequence seen in previous response; start at 0 for new service process"`
	Limit         int    `json:"limit,omitempty" jsonschema:"Maximum 100 event summaries; default 20"`
	Generation    string `json:"generation,omitempty" jsonschema:"Generation from previous response; send on every subsequent call to detect service restarts"`
}

func registerEventTools(server *mcp.Server, svc *instagram.Service) {
	addTool(server, svc, "ig_poll_events",
		"Read bounded realtime event summaries, including when Instagram authentication expires. Pass generation and next_sequence back on subsequent calls; generation changes on session activation, reset_required signals a stale cursor, dropped signals evicted events. No raw message contents.",
		true, false,
		func(_ context.Context, _ *instameow.Client, input pollEventsInput) (toolOutput, error) {
			limit, err := boundedLimit(input.Limit)
			if err != nil {
				return nil, invalidf("invalid event limit: %s", err)
			}
			if len(input.Generation) > 128 {
				return nil, invalidf("generation exceeds 128 bytes")
			}
			if input.AfterSequence != 0 && input.Generation == "" {
				return nil, invalidf("generation from previous response is required with after_sequence")
			}
			page := svc.PollEventsWithEpoch(input.AfterSequence, input.Generation, limit)
			return toolOutput{
				"events":          page.Events,
				"next_sequence":   page.NextSequence,
				"latest_sequence": page.LatestSequence,
				"dropped":         page.Dropped,
				"reset_required":  page.ResetRequired,
				"generation":      page.Epoch,
				"cursor_expired":  page.Dropped,
			}, nil
		})
}
