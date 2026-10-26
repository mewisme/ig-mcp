package transport

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
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

func TestRunWithOptionsPreservesValidation(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		if err := RunWithOptions(nil, testServer(), "openai", verbose); err == nil || !strings.Contains(err.Error(), "context is nil") {
			t.Fatalf("verbose=%v, nil context: %v", verbose, err)
		}
		t.Setenv("CONTROL_PLANE_TUNNEL_ID", "")
		t.Setenv("CONTROL_PLANE_API_KEY", "secret-value")
		if err := RunWithOptions(context.Background(), testServer(), "openai", verbose); err == nil || !strings.Contains(err.Error(), "CONTROL_PLANE_TUNNEL_ID") || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("verbose=%v, missing ID: %v", verbose, err)
		}
	}
}

func TestTunnelSDKLoggingDefaultSilent(t *testing.T) {
	var output bytes.Buffer
	level, writer := tunnelLogging(false, &output)
	if level != slog.LevelError {
		t.Fatalf("default SDK level = %v, want error", level)
	}
	_, _ = writer.Write([]byte("time=now level=ERROR msg=failed status=401 api_key=SECRET-API-KEY\n"))
	if output.Len() != 0 {
		t.Fatalf("default must suppress raw SDK logs: %s", output.String())
	}
}

func TestTunnelSDKVerboseClassificationAndRedaction(t *testing.T) {
	var output bytes.Buffer
	level, writer := tunnelLogging(true, &output)
	if level != slog.LevelInfo {
		t.Fatalf("verbose SDK level = %v, want info", level)
	}
	lines := []string{
		`time=now level=ERROR msg="control-plane request failed" status=401 tunnel_id=SECRET-TUNNEL api_key=SECRET-API cookie=SECRET-COOKIE authorization="Bearer SECRET-BEARER"`,
		`{"level":"WARN","msg":"poll timed out","request_id":"SECRET-REQUEST","session":"SECRET-SESSION"}`,
		`time=now level=INFO msg="reconnecting to control plane" tunnel_id=SECRET-TUNNEL`,
		`time=now level=ERROR msg="upstream 503" status_code=503 response="SECRET-PAYLOAD"`,
		`time=now level=ERROR msg="x509: unknown authority" api_key=SECRET-API`,
		`time=now level=INFO msg="successful idle poll"`,
	}
	for _, line := range lines {
		_, _ = writer.Write([]byte(line + "\n"))
	}
	text := output.String()
	for _, want := range []string{"HTTP 401", "request timed out", "connection retry scheduled", "HTTP 503", "TLS verification"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing classified diagnostic %q in %q", want, text)
		}
	}
	for _, forbidden := range []string{"SECRET-", "tunnel_id=", "api_key=", "cookie=", "authorization=", "request_id=", "level=", "time=", "{\"level\"", "successful idle poll"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("raw SDK field or credential %q exposed in %q", forbidden, text)
		}
	}
}

func TestTunnelSDKVerboseChunkingAndOversize(t *testing.T) {
	var output bytes.Buffer
	_, writer := tunnelLogging(true, &output)
	_, _ = writer.Write([]byte("level=ERROR msg=failed status="))
	if output.Len() != 0 {
		t.Fatalf("incomplete record was emitted: %q", output.String())
	}
	_, _ = writer.Write([]byte("429 api_key=SECRET-API\n"))
	_, _ = writer.Write([]byte("level=WARN msg=" + strings.Repeat("SECRET-COOKIE", maxTunnelLogLine) + "\n"))
	text := output.String()
	if !strings.Contains(text, "HTTP 429") || !strings.Contains(text, "oversized SDK diagnostic omitted") || strings.Contains(text, "SECRET-") {
		t.Fatalf("chunked or oversized SDK diagnostics unsafe: %q", text)
	}
}

func TestTunnelErrorVerboseReasonIsSafe(t *testing.T) {
	providerErr := errors.New("HTTP 403: rejected API_KEY=SECRET-API cookie=SECRET-COOKIE tunnel_id=SECRET-TUNNEL")
	quiet := tunnelError("poll", providerErr, false)
	verbose := tunnelError("poll", providerErr, true)
	if strings.Contains(quiet.Error(), "HTTP 403") || !strings.Contains(verbose.Error(), "HTTP 403") || !strings.Contains(verbose.Error(), "access denied") {
		t.Fatalf("unexpected verbosity difference: quiet=%v verbose=%v", quiet, verbose)
	}
	for _, err := range []error{quiet, verbose} {
		if strings.Contains(err.Error(), "SECRET-") || strings.Contains(err.Error(), "API_KEY=") || strings.Contains(err.Error(), "tunnel_id=") {
			t.Fatalf("provider details exposed: %v", err)
		}
	}
	if err := tunnelError("poll", context.DeadlineExceeded, true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lost timeout identity: %v", err)
	}
}

func TestTunnelVerboseSlowReadinessIsNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := newFakeTunnel()
	var output bytes.Buffer
	go func() {
		time.Sleep(45 * time.Millisecond)
		close(f.ready)
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if err := runOpenAILifecycleWithOptions(ctx, serveUntilCanceled, f, 10*time.Millisecond, time.Second, &output, true); err != nil {
		t.Fatalf("verbose readiness notice must not stop retries: %v", err)
	}
	if !strings.Contains(output.String(), "still connecting") || !strings.Contains(output.String(), "connected. Waiting") {
		t.Fatalf("unexpected verbose readiness flow: %q", output.String())
	}
}
