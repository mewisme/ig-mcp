package instagram

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAutoLoginMethodPrecedence(t *testing.T) {
	t.Setenv("IG_MCP_LOGIN_COOKIES", "cookie-secret")
	t.Setenv("IG_MCP_LOGIN_IDENTITY", "account")
	t.Setenv("IG_MCP_LOGIN_PASSWORD", "password-secret")
	for _, method := range []string{"", "auto", " AUTO "} {
		if got := resolveLoginMethod(method); got != "cookie" {
			t.Fatalf("auto method %q resolved to %q", method, got)
		}
	}
	for _, method := range []string{"android", "web", "cookie"} {
		if got := resolveLoginMethod(method); got != method {
			t.Fatalf("explicit method %q resolved to %q", method, got)
		}
	}
	if loginNeedsTerminal("cookie") || loginNeedsTerminal("android") || loginNeedsTerminal("web") {
		t.Fatal("fully configured authentication should not require TTY")
	}
	t.Setenv("IG_MCP_LOGIN_COOKIES", "")
	if got := resolveLoginMethod("auto"); got != "android" {
		t.Fatalf("credential fallback resolved to %q", got)
	}
	if !loginNeedsTerminal("cookie") {
		t.Fatal("cookie prompt without configured cookies requires TTY")
	}
	t.Setenv("IG_MCP_LOGIN_PASSWORD", "")
	if !loginNeedsTerminal("android") || !loginNeedsTerminal("web") {
		t.Fatal("missing credential requires TTY")
	}
}

func TestForgetSessionLocalAndIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.json")
	s := &Service{path: path}
	if err := s.ForgetSession(); err != nil {
		t.Fatalf("forget missing session: %v", err)
	}
	if err := writeSession(path, diskSession{Version: 1, Cookies: testCookies()}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := s.ForgetSession(); err != nil {
		t.Fatalf("forget session: %v", err)
	}
	if _, err := readSession(path); !errors.Is(err, ErrNoSession) {
		t.Fatalf("session still present: %v", err)
	}
	if err := s.ForgetSession(); err != nil {
		t.Fatalf("forget again: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("cross-process lock should remain available: %v", err)
	}
}

func TestForgetSessionRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := &Service{path: path}
	if err := s.ForgetSession(); err == nil {
		t.Fatal("symlink session should be rejected")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("symlink unexpectedly removed: %v", err)
	}
}
