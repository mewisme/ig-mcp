package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVersionCommandAndFlagUseSameBuildMetadata(t *testing.T) {
	t.Chdir(t.TempDir())
	oldVersion, oldCommit, oldDate := Version, Commit, Date
	t.Cleanup(func() { Version, Commit, Date = oldVersion, oldCommit, oldDate })
	Version, Commit, Date = "", "", ""
	for _, args := range [][]string{{"version"}, {"--version"}} {
		output, err := execute(t, args...)
		if err != nil || !strings.Contains(output, "ig-mcp dev") || !strings.Contains(output, runtime.GOOS+"/"+runtime.GOARCH) {
			t.Fatalf("version %v: %q, %v", args, output, err)
		}
	}
	Version, Commit, Date = "1.2.3", "abc123", "2026-10-26"
	fromCommand, err := execute(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	fromFlag, err := execute(t, "--version")
	if err != nil || fromFlag != fromCommand || !strings.Contains(fromFlag, "1.2.3") || !strings.Contains(fromFlag, "abc123") || !strings.Contains(fromFlag, "2026-10-26") {
		t.Fatalf("version mismatch: command=%q, flag=%q, err=%v", fromCommand, fromFlag, err)
	}
	if releaseVersion() != "1.2.3" {
		t.Fatalf("MCP release version = %q", releaseVersion())
	}
}

func TestServeSupportsVerboseDiagnosticsFlag(t *testing.T) {
	output, err := execute(t, "serve", "--help")
	if err != nil || !strings.Contains(output, "--verbose") || !strings.Contains(output, "OpenAI Tunnel diagnostics") {
		t.Fatalf("serve missing verbose diagnostics flag: %q, %v", output, err)
	}
	if !strings.Contains(output, "cloudflare") {
		t.Fatalf("serve should advertise cloudflare transport: %q", output)
	}
}

func TestDoctorCloudflareAuthPreflight(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("IG_MCP_CF_BEARER_TOKEN", "")
	t.Setenv("CONTROL_PLANE_TUNNEL_ID", "")
	t.Setenv("CONTROL_PLANE_API_KEY", "")
	output, err := execute(t, "doctor", "--transport", "cloudflare")
	if err != nil || !strings.Contains(output, "WARN cloudflare:") || !strings.Contains(output, "IG_MCP_CF_BEARER_TOKEN") {
		t.Fatalf("cloudflare doctor must require private access token: %q, %v", output, err)
	}
	t.Setenv("IG_MCP_CF_BEARER_TOKEN", strings.Repeat("x", 32))
	output, err = execute(t, "doctor", "--transport", "cf")
	if err != nil || !strings.Contains(output, "PASS cloudflare:") || strings.Contains(output, "xxxxxxxx") {
		t.Fatalf("cloudflare doctor must not print token: %q, %v", output, err)
	}
	t.Setenv("CF_TUNNEL_TOKEN", "sensitive-dashboard-credential")
	output, err = execute(t, "doctor", "--transport", "cloudflare")
	if err != nil || !strings.Contains(output, "WARN cloudflare: CF_TUNNEL_TOKEN") || strings.Contains(output, "sensitive-dashboard-credential") {
		t.Fatalf("cloudflare doctor must reject managed token without disclosure: %q, %v", output, err)
	}
	t.Setenv("CF_TUNNEL_TOKEN", "")
	t.Setenv("IG_MCP_CF_BEARER_TOKEN", strings.Repeat("x", 32)+"\n")
	output, err = execute(t, "doctor", "--transport", "cf")
	if err != nil || !strings.Contains(output, "WARN cloudflare:") {
		t.Fatalf("cloudflare doctor must reject invalid bearer: %q, %v", output, err)
	}
}

func TestDoctorOfflineMissingConfigurationAndStdio(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("CONTROL_PLANE_TUNNEL_ID", "")
	t.Setenv("CONTROL_PLANE_API_KEY", "")
	output, err := execute(t, "doctor")
	if err != nil || !strings.Contains(output, "WARN dotenv:") || !strings.Contains(output, "WARN session: not_logged_in") || !strings.Contains(output, "WARN tunnel: missing CONTROL_PLANE_TUNNEL_ID, CONTROL_PLANE_API_KEY") {
		t.Fatalf("doctor missing configuration: %q, %v", output, err)
	}
	output, err = execute(t, "doctor", "--transport", "stdio")
	if err != nil || !strings.Contains(output, "PASS transport: stdio") || strings.Contains(output, "WARN tunnel:") {
		t.Fatalf("doctor stdio: %q, %v", output, err)
	}
	_, err = execute(t, "doctor", "--transport", "bad")
	if err == nil || !strings.Contains(err.Error(), "unsupported transport") {
		t.Fatalf("doctor invalid mode: %v", err)
	}
}

func TestDoctorAutoLoadsCustomDotenvWithoutDisclosingValues(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(root, "no-session.json"))
	// Inherited values still take precedence over any dotenv values.
	t.Setenv("CONTROL_PLANE_TUNNEL_ID", "SENSITIVE_TUNNEL_ID")
	t.Setenv("CONTROL_PLANE_API_KEY", "SENSITIVE_API_KEY")
	path := filepath.Join(root, "config", "custom.env")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("CONTROL_PLANE_TUNNEL_ID=OTHER_SECRET_ID\nCONTROL_PLANE_API_KEY=OTHER_SECRET_KEY\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := execute(t, "doctor", "--env-path", path)
	if err != nil || !strings.Contains(output, "PASS dotenv:") || !strings.Contains(output, "PASS tunnel:") {
		t.Fatalf("doctor dotenv: %q, %v", output, err)
	}
	for _, secret := range []string{"SENSITIVE_TUNNEL_ID", "SENSITIVE_API_KEY", "OTHER_SECRET_ID", "OTHER_SECRET_KEY", path} {
		if strings.Contains(output, secret) {
			t.Fatalf("doctor disclosed sensitive environment details: %q", output)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".env.local"), []byte("INVALID DOTENV SYNTAX ;\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err = execute(t, "doctor", "--transport", "stdio")
	if err != nil || !strings.Contains(output, "WARN dotenv:") {
		t.Fatalf("doctor must report invalid dotenv as warning: %q, %v", output, err)
	}
}

func TestDoctorReportsLocallySavedSessionWithoutNetwork(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "session.json")
	const saved = `{"version":1,"cookies":{"sessionid":"123%3Atest","csrftoken":"csrf","ds_user_id":"123","mid":"device"}}`
	if err := os.WriteFile(path, []byte(saved), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IG_MCP_SESSION_FILE", path)
	output, err := execute(t, "doctor", "--transport", "stdio")
	if err != nil || !strings.Contains(output, "PASS session: session_saved") || strings.Contains(output, "123%3Atest") || strings.Contains(output, path) {
		t.Fatalf("doctor saved local session: %q, %v", output, err)
	}
}

func TestLogoutIsLocalAndIdempotent(t *testing.T) {
	t.Chdir(t.TempDir())
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("IG_MCP_SESSION_FILE", path)
	if err := os.WriteFile(path, []byte("TEST_SESSION_CONTENTS_NEVER_PRINT"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		output, err := execute(t, "logout")
		if err != nil || !strings.Contains(output, "Local Instagram session cleared") || !strings.Contains(output, "sessions remain active") || strings.Contains(output, "TEST_SESSION_CONTENTS_NEVER_PRINT") {
			t.Fatalf("logout #%d: %q, %v", i, output, err)
		}
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("logout created local session: %v", err)
	}
}

func TestLoginMethodDefaultAndExplicitOverrides(t *testing.T) {
	cmd := newLoginCommand()
	method, err := cmd.Flags().GetString("method")
	if err != nil || method != "auto" {
		t.Fatalf("default login method: %q, %v", method, err)
	}
	for _, selected := range []string{"cookie", "android", "web", "auto"} {
		cmd := newLoginCommand()
		if err := cmd.Flags().Set("method", selected); err != nil {
			t.Fatal(err)
		}
		method, err := cmd.Flags().GetString("method")
		if err != nil || method != selected {
			t.Fatalf("explicit login mode %q became %q: %v", selected, method, err)
		}
	}
	output, err := execute(t, "login", "--help")
	if err != nil || !strings.Contains(output, "cookie if configured") {
		t.Fatalf("login help: %q, %v", output, err)
	}
}

func TestInitEnvPrintsUsableServeCommand(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	output, err := execute(t, "init", "env", "custom.env", "--path", "nested")
	path := filepath.Join(root, "nested", "custom.env")
	if err != nil || !strings.Contains(output, "ig-mcp --env-path "+quoteEnvPath(path)+" serve") {
		t.Fatalf("custom env command: %q, %v", output, err)
	}
	output, err = execute(t, "init", "env", "another.env", "--path", "space directory")
	withSpaces := filepath.Join(root, "space directory", "another.env")
	quoted := quoteEnvPath(withSpaces)
	if err != nil || !strings.Contains(output, "ig-mcp --env-path "+quoted+" serve") {
		t.Fatalf("env path quoting: %q, %v", output, err)
	}
}

func TestInitEnvQuoteSpecialCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		if got := quoteEnvPath(`C:\Program Files\IG & Tools\config.env`); got != `'C:\Program Files\IG & Tools\config.env'` {
			t.Fatalf("unsafe PowerShell quoting: %q", got)
		}
		return
	}
	if got := quoteEnvPath("/tmp/my $folder's/cfg.env"); got != `'/tmp/my $folder'"'"'s/cfg.env'` {
		t.Fatalf("unsafe POSIX quoting: %q", got)
	}
}
