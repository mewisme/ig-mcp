package transport

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	cftunnel "go.mewis.me/cf-tunnel"
)

// CloudflareBearerTokenEnv is an independent, mandatory credential for callers
// accessing the public Cloudflare Quick Tunnel endpoint.
const CloudflareBearerTokenEnv = "IG_MCP_CF_BEARER_TOKEN"

var ErrCloudflareStopped = errors.New("Cloudflare tunnel stopped unexpectedly")

type cloudflareClient interface {
	WaitUntilReady(context.Context) error
	Wait() error
	PublicURL() string
}

type cloudflareStart func(context.Context, cftunnel.Config) (cloudflareClient, error)

func runCloudflare(ctx context.Context, server *mcp.Server) error {
	token := os.Getenv(CloudflareBearerTokenEnv)
	if err := validateCloudflareBearerToken(token); err != nil {
		return err
	}
	// Token-based tunnels consume remote ingress configuration and cannot route
	// to Config.Handler. Never silently start a public endpoint in that mode.
	if strings.TrimSpace(os.Getenv("CF_TUNNEL_TOKEN")) != "" {
		return errors.New("transport: CF_TUNNEL_TOKEN cannot be used with the in-memory Cloudflare MCP transport; unset it to use a Quick Tunnel")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		SessionTimeout: 30 * time.Minute,
	})
	mux := http.NewServeMux()
	mux.Handle("/mcp", cloudflareBearerHandler(handler, token))
	return runCloudflareLifecycle(ctx, mux, func(ctx context.Context, cfg cftunnel.Config) (cloudflareClient, error) {
		return cftunnel.Start(ctx, cfg)
	}, os.Stderr)
}

func validateCloudflareBearerToken(token string) error {
	if len(token) < 32 {
		return fmt.Errorf("transport: %s must contain at least 32 bytes of random printable ASCII", CloudflareBearerTokenEnv)
	}
	for i := 0; i < len(token); i++ {
		if token[i] < '!' || token[i] > '~' {
			return fmt.Errorf("transport: %s must contain only non-whitespace printable ASCII", CloudflareBearerTokenEnv)
		}
	}
	return nil
}

func cloudflareBearerHandler(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if len(values) != 1 || len(values[0]) < 7 || !strings.EqualFold(values[0][:7], "Bearer ") || subtle.ConstantTimeCompare([]byte(values[0][7:]), []byte(token)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Cloudflare must only receive the MCP handler; no local TCP listener is used.
// The injectable factory makes readiness and cancellation testable offline.
func runCloudflareLifecycle(ctx context.Context, handler http.Handler, start cloudflareStart, output io.Writer) (result error) {
	notice := func(message string) { _, _ = fmt.Fprintln(output, "Cloudflare Tunnel: "+message) }
	if err := ctx.Err(); err != nil {
		return err
	}
	notice("connecting...")
	tunnelCtx, cancel := context.WithCancel(ctx)
	tunnel, err := start(tunnelCtx, cftunnel.Config{Handler: handler})
	if err != nil {
		cancel()
		if ctx.Err() != nil {
			notice("stopped.")
			return nil
		}
		notice("disconnected.")
		return safeTunnelError("start Cloudflare tunnel", err)
	}

	exited := make(chan error, 1)
	go func() { exited <- tunnel.Wait() }()
	exitConsumed := false
	defer func() {
		cancel()
		if !exitConsumed {
			timer := time.NewTimer(shutdownTimeout)
			defer timer.Stop()
			select {
			case <-exited:
			case <-timer.C:
				result = errors.Join(result, errors.New("transport: Cloudflare tunnel shutdown timed out"))
			}
		}
		if ctx.Err() != nil && result == nil {
			notice("stopped.")
		} else {
			notice("disconnected.")
		}
	}()

	ready := make(chan error, 1)
	go func() { ready <- tunnel.WaitUntilReady(tunnelCtx) }()
	select {
	case <-ctx.Done():
		return nil
	case err := <-exited:
		exitConsumed = true
		if ctx.Err() != nil {
			return nil
		}
		return cloudflareExit("before readiness", err)
	case err := <-ready:
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			return safeTunnelError("wait for Cloudflare readiness", err)
		}
	}
	publicURL := tunnel.PublicURL()
	if !validCloudflarePublicURL(publicURL) {
		return errors.New("transport: Cloudflare readiness returned an invalid Quick Tunnel URL")
	}
	notice("ready: " + publicURL + "/mcp")
	select {
	case <-ctx.Done():
		return nil
	case err := <-exited:
		exitConsumed = true
		if ctx.Err() != nil {
			return nil
		}
		return cloudflareExit("after readiness", err)
	}
}

func cloudflareExit(stage string, err error) error {
	if err == nil {
		return fmt.Errorf("transport: %w %s", ErrCloudflareStopped, stage)
	}
	return errors.Join(fmt.Errorf("transport: %w %s", ErrCloudflareStopped, stage), safeTunnelError("Cloudflare tunnel runtime", err))
}

func validCloudflarePublicURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".trycloudflare.com") && len(host) > len(".trycloudflare.com")
}
