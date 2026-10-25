// Package transport serves an MCP server through stdio or OpenAI Secure MCP Tunnel.
package transport

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"
)

const (
	connectionNoticeDelay = 30 * time.Second
	shutdownTimeout       = 5 * time.Second
)

var ErrTunnelStopped = errors.New("OpenAI tunnel stopped unexpectedly")

type tunnelLifecycle interface {
	Start(context.Context) error
	WaitUntilReady(context.Context) error
	Done() <-chan os.Signal
	Stop(context.Context) error
}

// Run blocks until the MCP connection ends, the tunnel shuts down, or ctx is cancelled.
// OpenAI mode uses only in-memory MCP transports; it does not open a local listener.
func Run(ctx context.Context, server *mcp.Server, mode string) error {
	if ctx == nil {
		return errors.New("transport: context is nil")
	}
	if server == nil {
		return errors.New("transport: MCP server is nil")
	}

	switch mode {
	case "stdio":
		return server.Run(ctx, &mcp.StdioTransport{})
	case "openai":
		id := strings.TrimSpace(os.Getenv("CONTROL_PLANE_TUNNEL_ID"))
		key := strings.TrimSpace(os.Getenv("CONTROL_PLANE_API_KEY"))
		if id == "" {
			return errors.New("transport: CONTROL_PLANE_TUNNEL_ID is required")
		}
		if key == "" {
			return errors.New("transport: CONTROL_PLANE_API_KEY is required")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return runOpenAI(ctx, server, id, key)
	default:
		return fmt.Errorf("transport: unsupported mode %q (want openai or stdio)", mode)
	}
}

func runOpenAI(ctx context.Context, server *mcp.Server, id, key string) (result error) {
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	// The upstream SDK emits structured key-value logs by default. The CLI
	// provides concise lifecycle messages instead, without printing credentials.
	client, err := tunnelclient.New(tunnelclient.Config{
		TunnelID: id, APIKey: key,
		LogLevel: slog.LevelError, LogWriter: io.Discard,
	}, clientTransport)
	if err != nil {
		// The upstream validation error may embed user-supplied credentials.
		return safeTunnelError("create OpenAI tunnel (check tunnel ID and API key)", err)
	}
	return runOpenAILifecycle(ctx, func(serverCtx context.Context) error {
		return server.Run(serverCtx, serverTransport)
	}, client, connectionNoticeDelay, shutdownTimeout)
}

// runOpenAILifecycle accepts an offline fake tunnel and server for deterministic
// lifecycle/health regression tests; production uses the official tunnel client.
func runOpenAILifecycle(ctx context.Context, serve func(context.Context) error, client tunnelLifecycle, noticeDelay, stopTimeout time.Duration) (result error) {
	return runOpenAILifecycleWithOutput(ctx, serve, client, noticeDelay, stopTimeout, os.Stderr)
}

func runOpenAILifecycleWithOutput(ctx context.Context, serve func(context.Context) error, client tunnelLifecycle, noticeDelay, stopTimeout time.Duration, output io.Writer) (result error) {
	notice := func(message string) {
		_, _ = fmt.Fprintln(output, "OpenAI Tunnel: "+message)
	}
	notice("connecting...")
	serverCtx, cancelServer := context.WithCancel(ctx)
	serverDone := make(chan error, 1)
	go func() { serverDone <- serve(serverCtx) }()
	serverExited := false
	defer func() {
		// Cancel MCP forwarding *before* tunnel shutdown to unblock a server
		// potentially waiting on transport close. Separate deadlines prevent
		// slow tunnel cleanup from starving the MCP server's grace period.
		cancelServer()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), stopTimeout)
		defer cancelShutdown()
		if err := client.Stop(shutdownCtx); err != nil {
			result = errors.Join(result, safeTunnelError("stop OpenAI tunnel", err))
		}
		if !serverExited {
			serverWaitCtx, cancelWait := context.WithTimeout(context.Background(), stopTimeout)
			defer cancelWait()
			select {
			case err := <-serverDone:
				if err != nil && !errors.Is(err, context.Canceled) {
					result = errors.Join(result, fmt.Errorf("transport: MCP server: %w", serverExit(err)))
				}
			case <-serverWaitCtx.Done():
				result = errors.Join(result, fmt.Errorf("transport: MCP server shutdown: %w", serverWaitCtx.Err()))
			}
		}
		if ctx.Err() != nil && result == nil {
			notice("stopped.")
		} else {
			notice("disconnected.")
		}
	}()

	if err := client.Start(serverCtx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return safeTunnelError("start OpenAI tunnel", err)
	}

	ready := make(chan error, 1)
	go func() { ready <- client.WaitUntilReady(serverCtx) }()
	// The first successful control-plane poll can take arbitrarily long during
	// retries. A slow poll is NOT a reason to tear down the working tunnel.
	// noticeDelay controls only when a one-off informational hint is printed.
	var reminder <-chan time.Time
	if noticeDelay > 0 {
		timer := time.NewTimer(noticeDelay)
		defer timer.Stop()
		reminder = timer.C
	}
	for {
		select {
		case err := <-ready:
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return safeTunnelError("wait for OpenAI tunnel readiness", err)
			}
			goto connected
		case <-reminder:
			notice("still connecting; retrying in the background.")
			reminder = nil
		case err := <-serverDone:
			serverExited = true
			return fmt.Errorf("transport: MCP server exited before tunnel was ready: %w", serverExit(err))
		case <-client.Done():
			return fmt.Errorf("transport: %w before readiness", ErrTunnelStopped)
		case <-ctx.Done():
			return nil
		}
	}
connected:
	notice("connected. Waiting for requests.")

	select {
	case <-ctx.Done():
		return nil
	case <-client.Done():
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("transport: %w after readiness", ErrTunnelStopped)
	case err := <-serverDone:
		serverExited = true
		return fmt.Errorf("transport: MCP server exited: %w", serverExit(err))
	}
}

func safeTunnelError(stage string, err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("transport: %s: %w", stage, context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("transport: %s: %w", stage, context.DeadlineExceeded)
	default:
		return fmt.Errorf("transport: %s failed (upstream error details suppressed to protect credentials)", stage)
	}
}

func serverExit(err error) error {
	if err == nil {
		return errors.New("server connection closed")
	}
	// MCP SDK errors may contain remote payloads; do not propagate arbitrary
	// transport diagnostics that might embed authentication material.
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return errors.New("MCP server transport failed (details suppressed)")
}
