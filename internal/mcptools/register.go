package mcptools

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/methods"

	"go.mewis.me/ig-mcp/internal/instagram"
)

// Register installs Instagram-only tools. Session creation and login are CLI concerns.
func Register(server *mcp.Server, svc *instagram.Service) {
	registerPingTool(server, svc)
	registerReadTools(server, svc)
	registerWriteTools(server, svc)
	registerExtraTools(server, svc)
	registerEventTools(server, svc)
}

type toolOutput = map[string]any
type toolHandler[In any] func(context.Context, *instameow.Client, In) (toolOutput, error)

func addTool[In any](server *mcp.Server, svc *instagram.Service, name, description string, readOnly, destructive bool, handler toolHandler[In]) {
	openWorld := true
	annotations := &mcp.ToolAnnotations{
		ReadOnlyHint:    readOnly,
		DestructiveHint: &destructive,
		OpenWorldHint:   &openWorld,
	}
	mcp.AddTool[In, toolOutput](server, &mcp.Tool{
		Name:         name,
		Description:  description,
		Annotations:  annotations,
		OutputSchema: outputSchemaFor(name),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, toolOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, nil, safeToolError(name, err)
		}
		if err := authorizeTool(name, readOnly, destructive); err != nil {
			return nil, nil, safeToolError(name, err)
		}
		if svc == nil {
			return nil, nil, safeToolError(name, invalidf("Instagram service unavailable"))
		}
		client := svc.Raw()
		if client == nil && name != "ig_poll_events" {
			return nil, nil, safeToolError(name, invalidf("Instagram session unavailable; log in using the CLI"))
		}
		// Scope in-process replay records to the current authenticated client so
		// switching accounts cannot replay data from another session.
		sessionScope := ""
		if client != nil {
			sessionScope = fmt.Sprintf("%p/%d", client, client.GetOwnFBID())
		}
		out, err := withIdempotency(ctx, sessionScope, name, in, func() (toolOutput, error) {
			out, err := handler(ctx, client, in)
			if err != nil {
				return nil, err
			}
			if !readOnly {
				if out == nil {
					out = toolOutput{}
				}
				// A successful upstream response acknowledges submission, not delivery.
				if _, ok := out["submitted"]; !ok {
					out["submitted"] = true
				}
				if _, ok := out["acknowledged"]; !ok {
					out["acknowledged"] = mutationAcknowledged(out)
				}
			}
			return out, nil
		})
		if err != nil {
			return nil, nil, safeToolError(name, err)
		}
		return nil, out, nil
	})
}

func mutationAcknowledged(out toolOutput) bool {
	if _, partial := out["validation_error"]; partial {
		return false
	}
	if validated, ok := out["validated"].(bool); ok && !validated {
		return false
	}
	for _, key := range []string{"message", "thread", "unsent", "deleted_thread_id"} {
		if v, ok := out[key]; ok {
			if v == nil {
				return false
			}
			value := reflect.ValueOf(v)
			switch value.Kind() {
			case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
				if value.IsNil() {
					return false
				}
			case reflect.String:
				if value.Len() == 0 {
					return false
				}
			}
		}
	}
	return true
}

type validationError struct{ message string }

func (e *validationError) Error() string { return e.message }

func invalidf(format string, args ...any) error {
	return &validationError{message: fmt.Sprintf(format, args...)}
}

func safeToolError(name string, err error) error {
	var v *validationError
	var netErr net.Error
	var statusErr interface{ StatusCode() int }
	switch {
	case errors.As(err, &v):
		return fmt.Errorf("%s: %s", name, v.message)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s: request cancelled", name)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s: request timed out", name)
	case errors.As(err, &netErr):
		if netErr.Timeout() {
			return fmt.Errorf("%s: Instagram network timed out", name)
		}
		return fmt.Errorf("%s: Instagram network unavailable", name)
	case errors.As(err, &statusErr):
		switch statusErr.StatusCode() {
		case 401, 403:
			return fmt.Errorf("%s: Instagram authentication or permission rejected", name)
		case 429:
			return fmt.Errorf("%s: Instagram rate limit reached", name)
		default:
			return fmt.Errorf("%s: Instagram request rejected (details redacted)", name)
		}
	default:
		// Error strings from upstream may contain cookies, request bodies or URLs.
		return fmt.Errorf("%s: Instagram operation failed (upstream details redacted)", name)
	}
}

func required(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", invalidf("%s is required", field)
	}
	if len(value) > 512 {
		return "", invalidf("%s exceeds 512 bytes", field)
	}
	if strings.Contains(field, "id") || strings.Contains(field, "fbid") {
		for _, r := range value {
			if r < '!' || r > '~' {
				return "", invalidf("%s must be a printable ID without spaces", field)
			}
		}
	}
	return value, nil
}

func requiredID(value, field string) (string, error) { return required(value, field) }

func positiveDecimalID(value, field string) (string, error) {
	id, err := requiredID(value, field)
	if err != nil {
		return "", err
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return "", invalidf("%s must be a positive decimal ID", field)
	}
	return strconv.FormatInt(n, 10), nil
}

// A caller-supplied retry key maps to the same upstream offline threading ID
// across retries and process restarts. Never log or echo the supplied key.
func stableRequestID(clientID, operation string) (string, error) {
	if clientID == "" {
		return requestID(), nil
	}
	if len(clientID) > 128 || strings.TrimSpace(clientID) != clientID {
		return "", invalidf("client_request_id must contain 1..128 printable characters")
	}
	for _, r := range clientID {
		if r < '!' || r > '~' {
			return "", invalidf("client_request_id must contain printable ASCII without spaces")
		}
	}
	sum := sha256.Sum256([]byte("ig-mcp\x00" + operation + "\x00" + clientID))
	n := binary.BigEndian.Uint64(sum[:8]) & 0x7fffffffffffffff
	if n == 0 {
		n = 1
	}
	return strconv.FormatUint(n, 10), nil
}

func requiredText(value, field string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", invalidf("%s is required", field)
	}
	if len(value) > 10000 {
		return "", invalidf("%s exceeds 10000 bytes", field)
	}
	return value, nil
}

func boundedLimit(n int) (int, error) {
	if n < 0 || n > 100 {
		return 0, invalidf("limit must be between 1 and 100")
	}
	if n == 0 {
		return 20, nil
	}
	return n, nil
}

func checkCursor(cursor string) error {
	if len(cursor) > 4096 {
		return invalidf("cursor exceeds 4096 bytes")
	}
	for _, r := range cursor {
		if r < '!' || r > '~' {
			return invalidf("cursor must be printable ASCII without spaces")
		}
	}
	return nil
}

func requestID() string {
	return strconv.FormatInt(methods.GenerateEpochID(), 10)
}

func validIDs(ids []string, field string, max int) ([]string, error) {
	if len(ids) == 0 || len(ids) > max {
		return nil, invalidf("%s requires 1 to %d IDs", field, max)
	}
	result := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, v := range ids {
		id, err := required(v, field)
		if err != nil {
			return nil, err
		}
		if strings.Contains(field, "fbids") {
			id, err = positiveDecimalID(id, field)
			if err != nil {
				return nil, err
			}
		}
		if !seen[id] {
			seen[id] = true
			result = append(result, id)
		}
	}
	return result, nil
}

func page[T any](src slidetypes.Edged[slidetypes.Node[T]], limit int, after string) (items []T, nextCursor string, hasMore bool, err error) {
	edges := src.Edges
	hasMore = src.PageInfo.HasNextPage
	nextCursor = src.PageInfo.EndCursor
	if len(edges) > limit {
		edges = edges[:limit]
		hasMore = true
		// Do not use the end cursor of the unreturned items: doing so skips data.
		nextCursor = edges[len(edges)-1].Cursor
	}
	if hasMore && (len(edges) == 0 || nextCursor == "" || nextCursor == after) {
		return nil, "", false, fmt.Errorf("invalid upstream pagination cursor")
	}
	if hasMore && checkCursor(nextCursor) != nil {
		return nil, "", false, fmt.Errorf("invalid upstream pagination cursor")
	}
	items = make([]T, 0, len(edges))
	for _, edge := range edges {
		items = append(items, edge.Node)
	}
	if !hasMore {
		nextCursor = ""
	}
	return
}
