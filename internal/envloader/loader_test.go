package envloader

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadPrecedence(t *testing.T) {
	const (
		shared       = "IG_MCP_ENVLOADER_TEST_SHARED"
		host         = "IG_MCP_ENVLOADER_TEST_HOST"
		emptyHost    = "IG_MCP_ENVLOADER_TEST_EMPTY_HOST"
		defaultOnly  = "IG_MCP_ENVLOADER_TEST_DEFAULT_ONLY"
		localOnly    = "IG_MCP_ENVLOADER_TEST_LOCAL_ONLY"
		explicitOnly = "IG_MCP_ENVLOADER_TEST_EXPLICIT_ONLY"
	)
	clearEnv(t, shared, host, emptyHost, defaultOnly, localOnly, explicitOnly)
	dir := t.TempDir()
	writeDotenv(t, filepath.Join(dir, ".env"), shared+"=base\n"+defaultOnly+"=base-only\n"+host+"=from-base\n")
	writeDotenv(t, filepath.Join(dir, ".env.local"), shared+"=local\n"+localOnly+"=local-only\n"+emptyHost+"=from-local\n")
	writeDotenv(t, filepath.Join(dir, "custom.env"), shared+"=explicit\n"+explicitOnly+"=explicit-only\n"+host+"=from-explicit\n")
	t.Setenv(host, "inherited")
	t.Setenv(emptyHost, "")

	if err := Load(dir, "custom.env"); err != nil {
		t.Fatalf("Load: %v", err)
	}
	for key, want := range map[string]string{
		shared: "explicit", host: "inherited", emptyHost: "",
		defaultOnly: "base-only", localOnly: "local-only", explicitOnly: "explicit-only",
	} {
		if got, ok := os.LookupEnv(key); !ok || got != want {
			t.Errorf("%s = %q (present=%t), want %q", key, got, ok, want)
		}
	}
}

func TestLoadLocalOverridesDefaultWithoutExplicitPath(t *testing.T) {
	const key = "IG_MCP_ENVLOADER_TEST_LOCAL_WINS"
	clearEnv(t, key)
	dir := t.TempDir()
	writeDotenv(t, filepath.Join(dir, ".env"), key+"=base\n")
	writeDotenv(t, filepath.Join(dir, ".env.local"), key+"=local\n")
	if err := Load(dir, ""); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := os.Getenv(key); got != "local" {
		t.Fatalf("local value = %q, want local", got)
	}
}

func TestLoadMissingDefaults(t *testing.T) {
	if err := Load(t.TempDir(), ""); err != nil {
		t.Fatalf("missing optional default files: %v", err)
	}
}

func TestLoadUnquotedInstagramCookiesWithTrailingSemicolon(t *testing.T) {
	const key = "IG_MCP_LOGIN_COOKIES"
	const sample = "sessionid=123%3Asample;csrftoken=example;ds_user_id=123; wd=1024x768;"
	clearEnv(t, key)
	dir := t.TempDir()
	writeDotenv(t, filepath.Join(dir, ".env.local"), key+"="+sample+"\n")
	if err := Load(dir, ""); err != nil {
		t.Fatalf("load unquoted cookies from dotenv: %v", err)
	}
	if got := os.Getenv(key); got != sample {
		t.Fatal("cookie header was changed by dotenv parser")
	}
}

func TestLoadMissingExplicit(t *testing.T) {
	err := Load(t.TempDir(), filepath.Join("config", "missing.env"))
	if err == nil {
		t.Fatal("expected error for missing explicit file")
	}
}

func TestLoadMalformedFilesDoNotExposeContentsOrApplyPartialValues(t *testing.T) {
	const key = "IG_MCP_ENVLOADER_TEST_NO_PARTIAL"
	const secret = "sensitive-test-value"
	for _, tc := range []struct {
		name     string
		badFile  string
		explicit string
	}{
		{name: "default", badFile: ".env.local"},
		{name: "explicit", badFile: "custom.env", explicit: "custom.env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t, key)
			dir := t.TempDir()
			writeDotenv(t, filepath.Join(dir, ".env"), key+"=from-base\n")
			writeDotenv(t, filepath.Join(dir, tc.badFile), "UNTERMINATED='"+secret+"\n")
			err := Load(dir, tc.explicit)
			if err == nil {
				t.Fatal("expected syntax error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("error exposed a dotenv value: %v", err)
			}
			if _, present := os.LookupEnv(key); present {
				t.Fatal("Load applied values before validating all files")
			}
		})
	}
}

func TestLoadRelativeCwdAndExplicitPath(t *testing.T) {
	const key = "IG_MCP_ENVLOADER_TEST_RELATIVE"
	clearEnv(t, key)
	dir := t.TempDir()
	configDir := filepath.Join(dir, "config")
	if err := os.Mkdir(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeDotenv(t, filepath.Join(configDir, "other.env"), key+"=relative\n")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeDir, err := filepath.Rel(wd, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Load(relativeDir, filepath.Join("config", "other.env")); err != nil {
		t.Fatalf("Load relative paths: %v", err)
	}
	if got := os.Getenv(key); got != "relative" {
		t.Fatalf("value = %q, want relative", got)
	}
}

func TestLoadAbsoluteExplicitPath(t *testing.T) {
	const key = "IG_MCP_ENVLOADER_TEST_ABSOLUTE"
	clearEnv(t, key)
	cwd := t.TempDir()
	customPath := filepath.Join(t.TempDir(), "absolute.env")
	writeDotenv(t, filepath.Join(cwd, ".env.local"), key+"=local\n")
	writeDotenv(t, customPath, key+"=absolute\n")
	if err := Load(cwd, customPath); err != nil {
		t.Fatalf("Load absolute path: %v", err)
	}
	if got := os.Getenv(key); got != "absolute" {
		t.Fatalf("value = %q, want absolute", got)
	}
}

func TestLoadRejectsDirectoryAndSymlinks(t *testing.T) {
	t.Run("explicit directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := Load(dir, "."); err == nil {
			t.Fatal("expected error for directory")
		}
	})
	t.Run("default directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, ".env"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := Load(dir, ""); err == nil {
			t.Fatal("expected error for default directory")
		}
	})
	t.Run("explicit symlink", func(t *testing.T) {
		dir := t.TempDir()
		writeDotenv(t, filepath.Join(dir, "target.env"), "SOME_KEY=value\n")
		if err := os.Symlink("target.env", filepath.Join(dir, "alias.env")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := Load(dir, "alias.env"); err == nil {
			t.Fatal("expected error for explicit symlink")
		}
	})
	t.Run("default symlink", func(t *testing.T) {
		dir := t.TempDir()
		writeDotenv(t, filepath.Join(dir, "target.env"), "SOME_KEY=value\n")
		if err := os.Symlink("target.env", filepath.Join(dir, ".env")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := Load(dir, ""); err == nil {
			t.Fatal("expected error for default symlink")
		}
	})
}

func TestLoadAcceptsReadableDotenvModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file permissions use ACLs")
	}
	const key = "IG_MCP_ENVLOADER_READABLE_FILE_TEST"
	for _, mode := range []os.FileMode{0644, 0660, 0700} {
		t.Run(mode.String(), func(t *testing.T) {
			clearEnv(t, key)
			dir := t.TempDir()
			path := filepath.Join(dir, ".env")
			writeDotenv(t, path, key+"=value\n")
			if err := os.Chmod(path, mode); err != nil {
				t.Fatal(err)
			}
			if err := Load(dir, ""); err != nil {
				t.Fatalf("Load mode %v: %v", mode, err)
			}
			if got := os.Getenv(key); got != "value" {
				t.Fatalf("value = %q, want value", got)
			}
		})
	}
}

func TestLoadAcceptsEnvLocalMode0644(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file permissions use ACLs")
	}
	const key = "IG_MCP_ENVLOADER_LOCAL_0644_TEST"
	clearEnv(t, key)
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.local")
	writeDotenv(t, path, key+"=loaded\n")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := Load(dir, ""); err != nil {
		t.Fatalf("Load .env.local mode 0644: %v", err)
	}
	if got := os.Getenv(key); got != "loaded" {
		t.Fatalf("value = %q, want loaded", got)
	}
}

func TestLoadAllowsMoreRestrictiveReadOnlyDotenv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows file permissions use ACLs")
	}
	const key = "IG_MCP_ENVLOADER_READ_ONLY_TEST"
	clearEnv(t, key)
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	writeDotenv(t, path, key+"=readonly\n")
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	if err := Load(dir, ""); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "readonly" {
		t.Fatalf("got %q, want readonly", got)
	}
}

func TestLoadRejectsOversizedDotenvBeforeApplyingValues(t *testing.T) {
	const key = "IG_MCP_ENVLOADER_OVERSIZED_TEST"
	clearEnv(t, key)
	dir := t.TempDir()
	writeDotenv(t, filepath.Join(dir, ".env"), key+"=base\n")
	writeDotenv(t, filepath.Join(dir, ".env.local"), "LONG_VALUE="+strings.Repeat("x", maxDotenvBytes)+"\n")
	err := Load(dir, "")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized file error, got %v", err)
	}
	if _, exists := os.LookupEnv(key); exists {
		t.Fatal("Load applied a base value before rejecting the oversized override")
	}
}

func writeDotenv(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// clearEnv keeps process-environment test fixtures isolated without requiring
// a particular runner environment or invoking tests from the repository root.
func clearEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		previous, present := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if present {
				_ = os.Setenv(key, previous)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}
