package transport

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "transport-test", Version: "1.0.0"}, nil)
}

func TestRunValidation(t *testing.T) {
	for _, tc := range []struct {
		name, mode, id, key, want string
	}{
		{"unsupported mode", "unknown", "", "", "unsupported mode"},
		{"missing tunnel ID", "openai", "", "api-key", "CONTROL_PLANE_TUNNEL_ID"},
		{"blank tunnel ID", "openai", " \t", "api-key", "CONTROL_PLANE_TUNNEL_ID"},
		{"missing API key", "openai", "tunnel_0123456789abcdef0123456789abcdef", "", "CONTROL_PLANE_API_KEY"},
		{"blank API key", "openai", "tunnel_0123456789abcdef0123456789abcdef", "  ", "CONTROL_PLANE_API_KEY"},
		{"invalid tunnel ID", "openai", "invalid-tunnel-id", "api-key", "tunnel ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CONTROL_PLANE_TUNNEL_ID", tc.id)
			t.Setenv("CONTROL_PLANE_API_KEY", tc.key)
			err := Run(context.Background(), testServer(), tc.mode)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run() error = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestRunNilInputs(t *testing.T) {
	if err := Run(nil, testServer(), "stdio"); err == nil || !strings.Contains(err.Error(), "context is nil") {
		t.Fatalf("nil context: %v", err)
	}
	if err := Run(context.Background(), nil, "stdio"); err == nil || !strings.Contains(err.Error(), "server is nil") {
		t.Fatalf("nil server: %v", err)
	}
}

func TestRunCancelledBeforeTunnelStartup(t *testing.T) {
	t.Setenv("CONTROL_PLANE_TUNNEL_ID", "tunnel_0123456789abcdef0123456789abcdef")
	t.Setenv("CONTROL_PLANE_API_KEY", "api-key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, testServer(), "openai"); err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

type fakeTunnel struct {
	ready    chan struct{}
	done     chan os.Signal
	startErr error
	startFn  func(context.Context) error
	stopErr  error
	stopFn   func(context.Context) error
	stopped  atomic.Int32
}

func newFakeTunnel() *fakeTunnel {
	return &fakeTunnel{ready: make(chan struct{}), done: make(chan os.Signal)}
}

func (f *fakeTunnel) Start(ctx context.Context) error {
	if f.startFn != nil {
		return f.startFn(ctx)
	}
	return f.startErr
}
func (f *fakeTunnel) Done() <-chan os.Signal { return f.done }
func (f *fakeTunnel) WaitUntilReady(ctx context.Context) error {
	select {
	case <-f.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeTunnel) Stop(ctx context.Context) error {
	f.stopped.Add(1)
	if f.stopFn != nil {
		return f.stopFn(ctx)
	}
	return f.stopErr
}

func serveUntilCanceled(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestTunnelLifecycleGracefulCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeTunnel()
	close(f.ready)
	f.stopFn = func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			t.Fatalf("Stop received expired context: %v", err)
		}
		return nil
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := runOpenAILifecycle(ctx, serveUntilCanceled, f, time.Second, time.Second); err != nil {
		t.Fatalf("graceful cancellation returned %v", err)
	}
	if got := f.stopped.Load(); got != 1 {
		t.Fatalf("Stop called %d times, expected once", got)
	}
}

func TestTunnelLifecycleUnexpectedDoneAfterReady(t *testing.T) {
	f := newFakeTunnel()
	close(f.ready)
	go func() {
		time.Sleep(10 * time.Millisecond)
		close(f.done)
	}()
	err := runOpenAILifecycle(context.Background(), serveUntilCanceled, f, time.Second, time.Second)
	if !errors.Is(err, ErrTunnelStopped) {
		t.Fatalf("unexpected tunnel shutdown returned %v", err)
	}
	if f.stopped.Load() != 1 {
		t.Fatal("Stop not called after spontaneous tunnel shutdown")
	}
}

func TestTunnelLifecycleDoneBeforeReady(t *testing.T) {
	f := newFakeTunnel()
	close(f.done)
	err := runOpenAILifecycle(context.Background(), serveUntilCanceled, f, time.Second, time.Second)
	if !errors.Is(err, ErrTunnelStopped) || !strings.Contains(err.Error(), "before readiness") {
		t.Fatalf("expected early tunnel shutdown, got %v", err)
	}
}

func TestTunnelLifecycleSlowControlPlaneKeepsRetrying(t *testing.T) {
	f := newFakeTunnel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	go func() {
		time.Sleep(55 * time.Millisecond) // Longer than the informational hint deadline.
		close(f.ready)
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if err := runOpenAILifecycleWithOutput(ctx, serveUntilCanceled, f, 10*time.Millisecond, time.Second, &output); err != nil {
		t.Fatalf("slow readiness should not stop tunnel: %v", err)
	}
	text := output.String()
	for _, want := range []string{"still connecting; retrying", "connected. Waiting for requests", "stopped."} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q from lifecycle output: %q", want, text)
		}
	}
}

func TestTunnelWithoutSuccessfulPollStaysRunningUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFakeTunnel() // Readiness is never signaled.
	var output bytes.Buffer
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()
	if err := runOpenAILifecycleWithOutput(ctx, serveUntilCanceled, f, 10*time.Millisecond, time.Second, &output); err != nil {
		t.Fatalf("no successful control-plane poll must not cause an error: %v", err)
	}
	text := output.String()
	if !strings.Contains(text, "still connecting; retrying") || strings.Contains(text, "connected. Waiting") || !strings.Contains(text, "stopped.") {
		t.Fatalf("incorrect waiting or shutdown state: %q", text)
	}
	if f.stopped.Load() != 1 {
		t.Fatal("Stop should run when the caller cancels the tunnel")
	}
}

func TestTunnelLifecycleServerExitBeforeReadiness(t *testing.T) {
	f := newFakeTunnel()
	err := runOpenAILifecycle(context.Background(), func(context.Context) error {
		return errors.New("upstream contains SECRET-SESSION")
	}, f, time.Second, time.Second)
	if err == nil || !strings.Contains(err.Error(), "before tunnel was ready") || strings.Contains(err.Error(), "SECRET-SESSION") {
		t.Fatalf("server shutdown error not safely reported: %v", err)
	}
}

func TestTunnelLifecycleJoinsStopAndServerFailures(t *testing.T) {
	f := newFakeTunnel()
	close(f.done)
	f.stopErr = errors.New("stop contained SECRET-COOKIE")
	err := runOpenAILifecycle(context.Background(), func(ctx context.Context) error {
		<-ctx.Done()
		return errors.New("server contained SECRET-COOKIE")
	}, f, time.Second, time.Second)
	if err == nil || !errors.Is(err, ErrTunnelStopped) || !strings.Contains(err.Error(), "stop OpenAI tunnel") || strings.Contains(err.Error(), "SECRET-COOKIE") {
		t.Fatalf("joined errors were lost or exposed secrets: %v", err)
	}
}

func TestTunnelLifecycleStopTimeoutDoesNotStarveServerWait(t *testing.T) {
	f := newFakeTunnel()
	close(f.done)
	f.stopFn = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	// The MCP server must be cancelled before Stop begins and allowed to
	// return even if Stop consumes all of its own timeout.
	err := runOpenAILifecycle(context.Background(), serveUntilCanceled, f, time.Second, 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "stop OpenAI tunnel") || strings.Contains(err.Error(), "MCP server shutdown") {
		t.Fatalf("shutdown deadline handling: %v", err)
	}
}

func TestTunnelLifecycleStartErrorsAreRedacted(t *testing.T) {
	f := newFakeTunnel()
	f.startErr = errors.New("failed with SECRET-TUNNEL-KEY")
	err := runOpenAILifecycle(context.Background(), serveUntilCanceled, f, time.Second, time.Second)
	if err == nil || !strings.Contains(err.Error(), "start OpenAI tunnel") || strings.Contains(err.Error(), "SECRET-TUNNEL-KEY") {
		t.Fatalf("start error not redacted: %v", err)
	}
}

func TestTunnelLifecycleStartUsesCallerCancellation(t *testing.T) {
	f := newFakeTunnel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.startFn = func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := runOpenAILifecycle(ctx, serveUntilCanceled, f, 10*time.Millisecond, time.Second); err != nil {
		t.Fatalf("start should exit cleanly on caller cancellation, got %v", err)
	}
	if f.stopped.Load() != 1 {
		t.Fatal("tunnel cleanup skipped after startup timeout")
	}
}

func TestTunnelConsoleOnlyPrintsUsefulLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := newFakeTunnel()
	close(f.ready)
	var output bytes.Buffer
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	if err := runOpenAILifecycleWithOutput(ctx, serveUntilCanceled, f, time.Second, time.Second, &output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	for _, want := range []string{"OpenAI Tunnel: connecting...", "OpenAI Tunnel: connected. Waiting for requests.", "OpenAI Tunnel: stopped."} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing useful message %q from %q", want, text)
		}
	}
	for _, unexpected := range []string{"level=", "time=", "tunnel_id=", "api_key=", "request_id=", "{\"level\""} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("unexpected structured logging field %q in %q", unexpected, text)
		}
	}
}

func TestTunnelConsoleErrorsDoNotShowProviderDetails(t *testing.T) {
	f := newFakeTunnel()
	f.startErr = errors.New("secret API credential from provider")
	var output bytes.Buffer
	err := runOpenAILifecycleWithOutput(context.Background(), serveUntilCanceled, f, time.Second, time.Second, &output)
	if err == nil {
		t.Fatal("expected failure")
	}
	text := output.String()
	if !strings.Contains(text, "OpenAI Tunnel: connecting...") || !strings.Contains(text, "OpenAI Tunnel: disconnected.") {
		t.Fatalf("missing startup/error lifecycle messages: %q", text)
	}
	if strings.Contains(text, "secret") || strings.Contains(err.Error(), "secret") {
		t.Fatal("provider credentials leaked into terminal output")
	}
}
