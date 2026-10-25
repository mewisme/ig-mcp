package mcptools

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The operator controls these policies through the server environment, not
// tool arguments supplied by an untrusted MCP client.
// IG_MCP_READ_ONLY, IG_MCP_DENY_TOOLS and IG_MCP_REQUIRE_DESTRUCTIVE_APPROVAL
// default to off. IG_MCP_WRITE_RATE_LIMIT_PER_MINUTE defaults to 60 (0 disables).
var writeRate = struct {
	sync.Mutex
	counts map[string]struct {
		start time.Time
		n     int
	}
}{counts: make(map[string]struct {
	start time.Time
	n     int
})}

func envFlag(name string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	default:
		return false, invalidf("invalid server policy configuration")
	}
}

func authorizeTool(name string, readOnly, destructive bool) error {
	readOnlyMode, err := envFlag("IG_MCP_READ_ONLY")
	if err != nil {
		return err
	}
	approval, err := envFlag("IG_MCP_REQUIRE_DESTRUCTIVE_APPROVAL")
	if err != nil {
		return err
	}
	for _, denied := range strings.Split(os.Getenv("IG_MCP_DENY_TOOLS"), ",") {
		if strings.TrimSpace(denied) == name {
			return invalidf("tool disabled by server policy")
		}
	}
	if !readOnly && readOnlyMode {
		return invalidf("mutations disabled by server read-only policy")
	}
	if destructive && approval {
		// MCP annotations are advisory; without a trusted approval callback,
		// require the operator to disable this guard after external approval.
		return invalidf("destructive action blocked: operator approval required")
	}
	if readOnly {
		return nil
	}
	limit := 60
	if raw := strings.TrimSpace(os.Getenv("IG_MCP_WRITE_RATE_LIMIT_PER_MINUTE")); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 0 || limit > 10000 {
			return invalidf("invalid server rate limit configuration")
		}
	}
	if limit == 0 {
		return nil
	}
	writeRate.Lock()
	defer writeRate.Unlock()
	now := time.Now()
	// Global mutation budget, not a separate allowance for every tool name.
	entry := writeRate.counts["all"]
	if now.Sub(entry.start) >= time.Minute || entry.start.IsZero() {
		entry.start, entry.n = now, 0
	}
	if entry.n >= limit {
		return invalidf("write rate limit exceeded; retry later")
	}
	entry.n++
	writeRate.counts["all"] = entry
	return nil
}
