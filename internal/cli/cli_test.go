package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mewis.me/ig-mcp/internal/instagram"
)

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return output.String(), err
}

func TestCobraValidation(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(t.TempDir(), "no-session.json"))
	cases := []struct {
		args     []string
		contains string
	}{
		{[]string{"other"}, "unknown command"},
		{[]string{"status", "extra"}, "unknown command"},
		{[]string{"serve", "--transport", "unknown"}, "unsupported transport"},
		{[]string{"serve", "unexpected"}, "unknown command"},
		{[]string{"login", "--method", "unsupported"}, "unsupported login method"},
		{[]string{"login", "--no-such-option"}, "unknown flag"},
		{[]string{"status", "--env-path", "./missing.env"}, "missing.env"},
		{[]string{"--env-path", "./missing.env", "status"}, "missing.env"},
	}
	for _, tt := range cases {
		_, err := execute(t, tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.contains) {
			t.Errorf("Execute(%q) error = %v, wanted %q", tt.args, err, tt.contains)
		}
	}
}

func TestCobraStatusAndHelp(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(t.TempDir(), "not-saved.json"))
	output, err := execute(t, "status")
	if err != nil || strings.TrimSpace(output) != "not_logged_in" {
		t.Fatalf("status returned %q, %v", output, err)
	}
	for _, args := range [][]string{{"--help"}, {"login", "--help"}, {"serve", "--help"}} {
		output, err := execute(t, args...)
		if err != nil || !strings.Contains(output, "Usage:") {
			t.Fatalf("help %q returned %q, %v", args, output, err)
		}
	}
}

func TestCobraEnvPathLoadForStatus(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	session := filepath.Join(tmp, "private", "session.json")
	if err := os.MkdirAll(filepath.Dir(session), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(session, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	// The explicit env file is loaded before instagram.New constructs the service.
	envFile := filepath.Join(tmp, "custom.env")
	if err := os.WriteFile(envFile, []byte("IG_MCP_SESSION_FILE="+session+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	previous, existed := os.LookupEnv("IG_MCP_SESSION_FILE")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("IG_MCP_SESSION_FILE", previous)
		} else {
			_ = os.Unsetenv("IG_MCP_SESSION_FILE")
		}
	})
	if err := os.Unsetenv("IG_MCP_SESSION_FILE"); err != nil {
		t.Fatal(err)
	}
	output, err := execute(t, "status", "--env-path", envFile)
	if err != nil || strings.TrimSpace(output) != "unavailable" {
		t.Fatalf("status with env path: %q, %v", output, err)
	}
}

func TestCobraAutomaticallyLoadsEnvLocalOverEnv(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	invalidSession := filepath.Join(tmp, "private", "invalid.json")
	if err := os.MkdirAll(filepath.Dir(invalidSession), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(invalidSession, []byte("invalid JSON"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".env"), []byte("IG_MCP_SESSION_FILE="+filepath.Join(tmp, "missing.json")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, ".env.local"), []byte("IG_MCP_SESSION_FILE="+invalidSession+"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	previous, existed := os.LookupEnv("IG_MCP_SESSION_FILE")
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("IG_MCP_SESSION_FILE", previous)
		} else {
			_ = os.Unsetenv("IG_MCP_SESSION_FILE")
		}
	})
	if err := os.Unsetenv("IG_MCP_SESSION_FILE"); err != nil {
		t.Fatal(err)
	}

	output, err := execute(t, "status")
	if err != nil || strings.TrimSpace(output) != "unavailable" {
		t.Fatalf("auto-loaded status = %q, %v; .env.local should win", output, err)
	}
	if got := os.Getenv("IG_MCP_SESSION_FILE"); got != invalidSession {
		t.Fatalf("loaded session path = %q, want .env.local path", got)
	}
}

func TestLoginConfirmationVerifiedAccount(t *testing.T) {
	var output bytes.Buffer
	account := instagram.AccountInfo{ID: "12345", Username: "example.user", FullName: "Example User"}
	if err := writeLoginConfirmation(&output, account); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, want := range []string{"login successful", "Session verified and saved", "@example.user", "Example User", "12345"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q from success message: %q", want, got)
		}
	}
	if strings.Contains(got, "sessionid=") {
		t.Fatal("confirmation unexpectedly included session cookie")
	}
}

func TestLoginConfirmationWithoutProfileStillSucceeds(t *testing.T) {
	var output bytes.Buffer
	if err := writeLoginConfirmation(&output, instagram.AccountInfo{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "@") || !strings.Contains(output.String(), "Session verified and saved") {
		t.Fatalf("unexpected no-profile confirmation: %q", output.String())
	}
}

func TestStatusVerboseExplainsMissingSessionWithoutRevealingPath(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "secret-account-session.json")
	t.Setenv("IG_MCP_SESSION_FILE", path)
	out, err := execute(t, "status", "--verbose")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "not_logged_in\nCause: ") || !strings.Contains(out, "ig-mcp login") || strings.Contains(out, path) {
		t.Fatalf("unsafe or unclear verbose status: %q", out)
	}
}

func TestStatusVerboseIdentifiesInsecureSessionFile(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "session-with-private-value.json")
	if err := os.WriteFile(path, []byte("SENSITIVE_COOKIE_DATA"), 0644); err != nil {
		t.Fatal(err)
	}
	// Chmod also handles filesystems with a nondefault umask.
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IG_MCP_SESSION_FILE", path)
	out, err := execute(t, "status", "-v")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "unavailable\nCause: ") {
		t.Fatalf("unexpected verbose status: %q", out)
	}
	if strings.Contains(out, path) || strings.Contains(out, "SENSITIVE_COOKIE_DATA") {
		t.Fatalf("status leaked sensitive details: %q", out)
	}
	if strings.Contains(out, "not private") == false && strings.Contains(out, "malformed") == false {
		t.Fatalf("missing useful safe diagnosis: %q", out)
	}
}
