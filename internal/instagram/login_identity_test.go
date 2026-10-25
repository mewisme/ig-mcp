package instagram

import (
	"bufio"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestConfiguredLoginCredentials(t *testing.T) {
	t.Setenv("IG_MCP_LOGIN_IDENTITY", "account@example.com")
	t.Setenv("IG_MCP_LOGIN_PASSWORD", "test-password")
	username, password, err := promptCredentials(nil)
	if err != nil || username != "account@example.com" || password != "test-password" {
		t.Fatal("configured login credentials were not loaded")
	}
}

func TestConfiguredCookieImport(t *testing.T) {
	input := "sessionid=123%3Atest; csrftoken=test; ds_user_id=123; mid=device"
	t.Setenv("IG_MCP_LOGIN_COOKIES", input)
	got, err := loginCookies()
	if err != nil || got != input {
		t.Fatal("configured cookie header was not loaded")
	}
	parsed, err := parseCookies(got)
	if err != nil || parsed["ds_user_id"] != "123" {
		t.Fatal("configured cookie header could not be parsed")
	}
}

func TestConfiguredTOTPAndInvalidSecret(t *testing.T) {
	t.Setenv("IG_MCP_LOGIN_TOTP_SECRET", "JBSWY3DPEHPK3PXP")
	code, err := secondFactor("", true)
	if err != nil || !regexp.MustCompile(`^[0-9]{6}$`).MatchString(code) {
		t.Fatalf("did not generate a six-digit code: %v", err)
	}
	t.Setenv("IG_MCP_LOGIN_TOTP_SECRET", "NOT A VALID @ SECRET")
	_, err = secondFactor("", true)
	if err == nil || strings.Contains(err.Error(), "NOT A VALID") {
		t.Fatal("invalid TOTP secret was not rejected safely")
	}
}

func TestTOTPChallengeSelection(t *testing.T) {
	if !isAuthenticatorChallenge("Enter your authenticator app code", "code", "Security code") {
		t.Fatal("authenticator challenge should allow TOTP")
	}
	if isAuthenticatorChallenge("Enter the code we sent by SMS", "code", "Security code") {
		t.Fatal("SMS challenge must not use a TOTP secret")
	}
	if isAuthenticatorChallenge("Enter your backup code", "backup", "Recovery code") {
		t.Fatal("recovery challenge must not use a TOTP secret")
	}
	if isAuthenticatorChallenge("Enter your verification code", "code", "Security code") {
		t.Fatal("ambiguous challenge must not auto-submit an authenticator code")
	}
}

func TestLoginIdentityFromEnvironment(t *testing.T) {
	t.Setenv("IG_MCP_LOGIN_IDENTITY", "  username@example.com  ")
	// No stdin reader is needed when an identity is configured.
	got, err := loginIdentity(nil)
	if err != nil || got != "username@example.com" {
		t.Fatalf("loginIdentity() = %q, %v", got, err)
	}
}

func TestLoginIdentityFallsBackToPrompt(t *testing.T) {
	t.Setenv("IG_MCP_LOGIN_IDENTITY", "")
	got, err := loginIdentity(bufio.NewReader(strings.NewReader("fallback_username\n")))
	if err != nil || got != "fallback_username" {
		t.Fatalf("loginIdentity() fallback = %q, %v", got, err)
	}
}

func TestLoginIdentityRejectsInvalidInputWithoutEchoing(t *testing.T) {
	for _, identity := range []string{"with\nsecret", strings.Repeat("x", 321)} {
		t.Run("invalid", func(t *testing.T) {
			t.Setenv("IG_MCP_LOGIN_IDENTITY", identity)
			got, err := loginIdentity(nil)
			if err == nil || got != "" || strings.Contains(err.Error(), identity) {
				t.Fatalf("unexpected validation result: %q, %v", got, err)
			}
		})
	}
}

func TestLoginIdentityEmptyRequiresInput(t *testing.T) {
	t.Setenv("IG_MCP_LOGIN_IDENTITY", "")
	got, err := loginIdentity(bufio.NewReader(strings.NewReader("\n")))
	if err == nil || got != "" {
		t.Fatalf("empty identity result: %q, %v", got, err)
	}
}

func TestLoginIdentityInheritedEnvironment(t *testing.T) {
	// Unlike a CLI positional argument, a process environment variable is
	// available automatically to Login after Cobra's dotenv hook completes.
	t.Setenv("IG_MCP_LOGIN_IDENTITY", "test_user")
	if got := os.Getenv("IG_MCP_LOGIN_IDENTITY"); got != "test_user" {
		t.Fatalf("unexpected login identity environment value: %q", got)
	}
	if got, err := loginIdentity(nil); err != nil || got != "test_user" {
		t.Fatalf("loginIdentity inherited environment = %q, %v", got, err)
	}
}
