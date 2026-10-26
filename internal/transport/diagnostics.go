package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
)

const maxTunnelLogLine = 4096

var tunnelStatusPattern = regexp.MustCompile(`(?i)(?:\bstatus(?:_?code| code)?["']?\s*[:=]\s*["']?|\bhttp(?:/[\d.]+)?\s+)(401|403|404|408|429|500|502|503|504)\b`)

// tunnelLogging never forwards the SDK writer directly: arbitrary SDK fields
// could contain tunnel IDs, tokens, cookies, or remote MCP payloads.
func tunnelLogging(verbose bool, output io.Writer) (slog.Level, io.Writer) {
	if !verbose {
		return slog.LevelError, io.Discard
	}
	return slog.LevelInfo, &tunnelDiagnosticWriter{output: output}
}

type tunnelDiagnosticWriter struct {
	mu       sync.Mutex
	output   io.Writer
	pending  []byte
	tooLarge bool
}

func (w *tunnelDiagnosticWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		line, rest, complete := bytes.Cut(p, []byte{'\n'})
		if !w.tooLarge {
			if len(w.pending)+len(line) > maxTunnelLogLine {
				w.pending = nil
				w.tooLarge = true
			} else {
				w.pending = append(w.pending, line...)
			}
		}
		if !complete {
			break
		}
		if w.tooLarge {
			_, _ = fmt.Fprintln(w.output, "OpenAI Tunnel: verbose: oversized SDK diagnostic omitted.")
		} else if detail := classifyTunnelDiagnostic(string(w.pending)); detail != "" {
			_, _ = fmt.Fprintln(w.output, "OpenAI Tunnel: verbose: "+detail)
		}
		w.pending = nil
		w.tooLarge = false
		p = rest
	}
	return n, nil
}

func classifyTunnelDiagnostic(raw string) string {
	s := strings.ToLower(raw)
	if match := tunnelStatusPattern.FindStringSubmatch(s); len(match) == 2 {
		switch match[1] {
		case "401":
			return "HTTP 401: upstream authentication rejected; check API key."
		case "403":
			return "HTTP 403: upstream access denied; check tunnel permissions."
		case "404":
			return "HTTP 404: upstream endpoint or tunnel not found."
		case "408":
			return "HTTP 408: upstream request timed out."
		case "429":
			return "HTTP 429: upstream rate limit; retrying may help."
		default:
			return "HTTP " + match[1] + ": upstream service failure."
		}
	}
	switch {
	case strings.Contains(s, "x509"), strings.Contains(s, "certificate"), strings.Contains(s, "tls handshake"):
		return "TLS verification or handshake failure; check certificates and clock."
	case strings.Contains(s, "no such host"), strings.Contains(s, "dns"), strings.Contains(s, "lookup "):
		return "DNS resolution failed; check resolver and upstream connectivity."
	case strings.Contains(s, "connection refused"):
		return "upstream refused the connection."
	case strings.Contains(s, "connection reset"), strings.Contains(s, "broken pipe"), strings.Contains(s, "unexpected eof"):
		return "upstream connection was interrupted."
	case strings.Contains(s, "deadline exceeded"), strings.Contains(s, "timed out"), strings.Contains(s, "timeout"):
		return "upstream request timed out."
	case strings.Contains(s, "unauthorized"), strings.Contains(s, "authentication failed"):
		return "upstream authentication failed; check credentials."
	case strings.Contains(s, "reconnect"), strings.Contains(s, "retry"), strings.Contains(s, "backoff"):
		return "control-plane connection retry scheduled."
	case strings.Contains(s, "disconnect"):
		return "control-plane connection lost."
	case strings.Contains(s, "error"), strings.Contains(s, "failed"), strings.Contains(s, "level=warn"), strings.Contains(s, "level=error"), strings.Contains(s, `"level":"warn"`), strings.Contains(s, `"level":"error"`):
		return "SDK reported an upstream error or warning; details withheld."
	default:
		return ""
	}
}

func tunnelError(stage string, err error, verbose bool) error {
	safe := safeTunnelError(stage, err)
	if !verbose || err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return safe
	}
	if detail := classifyTunnelDiagnostic(err.Error()); detail != "" {
		return fmt.Errorf("%w; %s", safe, detail)
	}
	return safe
}
