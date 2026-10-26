package instagram

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"golang.org/x/term"
	"maunium.net/go/mautrix/bridgev2"

	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mewis.me/ig-mcp/internal/totp"
)

// Login authenticates via the CLI using configured environment values or
// interactive prompts, and is never exposed through the MCP tool registry.
// Supported methods: auto (default), android, web, cookie. Android may still require
// a browser challenge; in that case use the cookie method after completing it
// in the official Instagram website.
func (s *Service) Login(ctx context.Context, method string) (loginErr error) {
	defer func() { loginErr = classifyLoginError(loginErr) }()
	if ctx == nil {
		return &LoginError{Kind: "invalid_input", message: "login context is required"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.RLock()
	active := s.client != nil
	s.mu.RUnlock()
	if active {
		return &LoginError{Kind: "already_active", message: "close the current client before starting a new login"}
	}
	method = resolveLoginMethod(method)
	if method != "android" && method != "web" && method != "cookie" {
		return &LoginError{Kind: "invalid_input", message: "login method must be auto, android, web or cookie"}
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) && loginNeedsTerminal(method) {
		return &LoginError{Kind: "terminal_required", message: "CLI login requires an interactive terminal; never pass credentials as command arguments"}
	}
	s.setStatus("connecting")
	defer func() {
		if s.Status() == "connecting" {
			s.setStatus("not_logged_in")
		}
	}()
	reader := bufio.NewReader(os.Stdin)
	stored, err := s.readDisk()
	if errors.Is(err, ErrNoSession) {
		stored = diskSession{Version: 1}
	} else if err != nil {
		return fmt.Errorf("load instagram session metadata: %w", err)
	}
	var client *instameow.Client
	defer func() {
		if loginErr != nil && client != nil {
			s.discardClient(client)
		}
	}()
	switch method {
	case "cookie":
		raw, readErr := loginCookies()
		if readErr != nil {
			return readErr
		}
		parsed, parseErr := parseCookies(raw)
		if parseErr != nil {
			return &LoginError{Kind: "invalid_input", message: "browser cookies are malformed, incomplete, or belong to different accounts"}
		}
		stored.Cookies = parsed
		stored.Native = nil // importing browser cookies invalidates native authorization
		if stored.DeviceAccountID != sessionCookieID(parsed) {
			stored.Device = nil
			stored.DeviceIdentity = ""
			stored.DeviceAccountID = ""
		}
		stored.Version = 1
		if err = validateSession(stored); err != nil {
			return &LoginError{Kind: "invalid_input", message: "browser session data is incomplete or inconsistent"}
		}
		client = s.newClient(stored, true, "")
	case "android", "web":
		username, password, readErr := promptCredentialsContext(ctx, reader)
		if readErr != nil {
			return readErr
		}
		stored.Cookies = nil
		stored.Native = nil
		stored.Version = 1
		client = s.newClient(stored, true, username)
		if method == "android" {
			err = androidLogin(ctx, client, reader, username, password)
		} else {
			err = webLogin(ctx, client, username, password)
		}
		password = ""
		if err != nil {
			return err
		}
	}
	if missing := client.GetCookies().GetMissingCookieNames(); len(missing) > 0 {
		return fmt.Errorf("instagram authentication did not yield required cookies: %v", missing)
	}
	if err := validateCookieAccount(cookieMap(client)); err != nil {
		return &LoginError{Kind: "invalid_input", message: "Instagram session cookies identify inconsistent accounts"}
	}
	return s.activate(ctx, client)
}

// resolveLoginMethod selects cookie import first, then credential authentication.
// Explicit methods never silently switch to another authentication mechanism.
func resolveLoginMethod(method string) string {
	method = strings.ToLower(strings.TrimSpace(method))
	if method != "" && method != "auto" {
		return method
	}
	if strings.TrimSpace(os.Getenv("IG_MCP_LOGIN_COOKIES")) != "" {
		return "cookie"
	}
	return "android"
}

func loginNeedsTerminal(method string) bool {
	switch method {
	case "cookie":
		return strings.TrimSpace(os.Getenv("IG_MCP_LOGIN_COOKIES")) == ""
	case "android", "web":
		return strings.TrimSpace(os.Getenv("IG_MCP_LOGIN_IDENTITY")) == "" || os.Getenv("IG_MCP_LOGIN_PASSWORD") == ""
	default:
		return true
	}
}

func cookieMap(client *instameow.Client) map[string]string {
	values := make(map[string]string)
	for key, value := range client.GetCookies().GetAll() {
		values[string(key)] = value
	}
	return values
}

func promptCredentials(reader *bufio.Reader) (string, string, error) {
	username, err := loginIdentity(reader)
	if err != nil {
		return "", "", err
	}
	password := os.Getenv("IG_MCP_LOGIN_PASSWORD")
	if password == "" {
		password, err = secretPrompt("Instagram password: ")
	}
	if err != nil {
		return "", "", err
	}
	if password == "" {
		return "", "", errors.New("instagram password is required")
	}
	return username, password, nil
}

func promptCredentialsContext(ctx context.Context, reader *bufio.Reader) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	username, password, err := promptCredentials(reader)
	if ctx.Err() != nil {
		return "", "", ctx.Err()
	}
	return username, password, err
}

// Terminal reads may not be interruptible at the OS level. Respect cancellation
// before and after each prompt instead of leaving an orphaned reader goroutine.
func secondFactorContext(ctx context.Context, prompt string, useTOTP bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := secondFactor(prompt, useTOTP)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return value, err
}

func secretPromptContext(ctx context.Context, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := secretPrompt(prompt)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return value, err
}

func textPromptContext(ctx context.Context, reader *bufio.Reader, prompt string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := textPrompt(reader, prompt)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return value, err
}

// loginIdentity optionally reads the account identifier from the environment.
// The CLI loads .env and .env.local before calling Login.
// An empty environment value behaves as if no identity was configured.
func loginIdentity(reader *bufio.Reader) (string, error) {
	identity := strings.TrimSpace(os.Getenv("IG_MCP_LOGIN_IDENTITY"))
	if identity == "" {
		var err error
		identity, err = textPrompt(reader, "Instagram username, email or phone: ")
		if err != nil {
			return "", err
		}
	}
	if identity == "" {
		return "", errors.New("instagram login identity is required (set IG_MCP_LOGIN_IDENTITY or enter it interactively)")
	}
	if len(identity) > 320 || strings.IndexFunc(identity, unicode.IsControl) >= 0 {
		return "", errors.New("invalid instagram login identity")
	}
	return identity, nil
}

func loginCookies() (string, error) {
	configured := os.Getenv("IG_MCP_LOGIN_COOKIES")
	if strings.TrimSpace(configured) != "" {
		return configured, nil
	}
	return secretPrompt("Instagram cookie header or one-line JSON: ")
}

// secondFactor only uses a configured authenticator secret for a TOTP method.
// The one-time code is generated at the point of use, never saved to disk.
func secondFactor(prompt string, useTOTP bool) (string, error) {
	if useTOTP {
		if secret := strings.TrimSpace(os.Getenv("IG_MCP_LOGIN_TOTP_SECRET")); secret != "" {
			code, err := totp.Generate(secret, time.Now())
			if err != nil {
				return "", fmt.Errorf("invalid IG_MCP_LOGIN_TOTP_SECRET: %w", err)
			}
			return code, nil
		}
	}
	return secretPrompt(prompt)
}

func isAuthenticatorChallenge(instructions, id, label string) bool {
	description := strings.ToLower(instructions + " " + id + " " + label)
	for _, phrase := range []string{"sms", "text message", "whatsapp", "email", "backup code", "recovery code"} {
		if strings.Contains(description, phrase) {
			return false
		}
	}
	for _, phrase := range []string{"authenticator", "auth app", "authentication app", "totp", "google authenticator", "2fa app"} {
		if strings.Contains(description, phrase) {
			return true
		}
	}
	return false // ambiguous code prompts must never guess the authenticator channel
}

func textPrompt(reader *bufio.Reader, name string) (string, error) {
	_, _ = fmt.Fprint(os.Stderr, name)
	text, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read interactive login input: %w", err)
	}
	return strings.TrimSpace(text), nil
}

func secretPrompt(name string) (string, error) {
	_, _ = fmt.Fprint(os.Stderr, name)
	data, err := term.ReadPassword(int(os.Stdin.Fd()))
	_, _ = fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("read secret from terminal: %w", err)
	}
	// Passwords can legitimately begin/end with whitespace.
	return string(data), nil
}

func androidLogin(ctx context.Context, client *instameow.Client, reader *bufio.Reader, username, password string) error {
	step, err := client.DoInstagramCAALoginSteps(ctx, map[string]string{
		"username": username, "password": password,
	})
	if err != nil {
		return fmt.Errorf("instagram Android login failed: %w", safeProviderError(err))
	}
	for i := 0; step != nil; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if i >= 20 {
			return errors.New("instagram Android login exceeded 20 interactive steps")
		}
		if step.Type != bridgev2.LoginStepTypeUserInput || step.UserInputParams == nil {
			return fmt.Errorf("%w (Android step %q)", ErrUnsupportedChallenge, step.StepID)
		}
		fields := step.UserInputParams.Fields
		if len(fields) == 0 {
			return fmt.Errorf("%w (empty Android step)", ErrUnsupportedChallenge)
		}
		if help := safePrompt(step.Instructions); help != "" {
			_, _ = fmt.Fprintln(os.Stderr, help)
		}
		values := make(map[string]string, len(fields))
		for _, field := range fields {
			if err := ctx.Err(); err != nil {
				return err
			}
			if field.ID == "" {
				return fmt.Errorf("%w (unnamed Android field)", ErrUnsupportedChallenge)
			}
			if field.Type == bridgev2.LoginInputFieldTypeCaptchaCode {
				return fmt.Errorf("%w (CAPTCHA)", ErrUnsupportedChallenge)
			}
			label := safePrompt(field.Name)
			if label == "" {
				label = safePrompt(field.ID)
			}
			if len(field.Options) > 0 {
				options := make([]string, 0, len(field.Options))
				for _, option := range field.Options {
					options = append(options, safePrompt(option))
				}
				_, _ = fmt.Fprintf(os.Stderr, "%s choices: %s\n", label, strings.Join(options, ", "))
			}
			var input string
			if field.Type == bridgev2.LoginInputFieldType2FACode {
				input, err = secondFactorContext(ctx, label+": ", isAuthenticatorChallenge(step.Instructions, field.ID, label))
			} else if field.Type == bridgev2.LoginInputFieldTypePassword ||
				field.Type == bridgev2.LoginInputFieldTypeToken {
				input, err = secretPromptContext(ctx, label+": ")
			} else {
				input, err = textPromptContext(ctx, reader, label+": ")
			}
			if err != nil {
				return err
			}
			if input == "" && field.Optional {
				continue
			}
			if input == "" {
				return fmt.Errorf("required instagram field %q is empty", field.ID)
			}
			if len(field.Options) > 0 {
				valid := false
				for _, v := range field.Options {
					if v == input {
						valid = true
						break
					}
				}
				if !valid {
					return fmt.Errorf("invalid selection for instagram field %q", field.ID)
				}
			}
			values[field.ID] = input
		}
		step, err = client.DoInstagramCAALoginSteps(ctx, values)
		if err != nil {
			return fmt.Errorf("instagram Android verification failed: %w", safeProviderError(err))
		}
	}
	if native := client.GetInstagramNativeSession(); native == nil || native.Authorization == "" {
		return errors.New("instagram Android login did not return native authorization")
	}
	return nil
}

// Upstream native web login supports direct credentials and one web 2FA code;
// auth-platform challenges/CAPTCHA need a real browser and are not simulated.
func webLogin(ctx context.Context, client *instameow.Client, username, password string) error {
	challenge, err := client.CreateInstagramWebSession(ctx, username, password)
	if err != nil {
		if errors.Is(err, instameow.ErrInstagramWebLoginRejected) {
			return err
		}
		if errors.Is(err, instameow.ErrInstagramWebCookieConsentRequired) ||
			errors.Is(err, instameow.ErrInstagramWebCheckpointUnsupported) ||
			errors.Is(err, instameow.ErrInstagramWebCheckpointCAPTCHA) {
			return ErrUnsupportedChallenge
		}
		return fmt.Errorf("instagram web login failed: %w", safeProviderError(err))
	}
	if challenge != nil {
		if challenge.AuthPlatform || (!challenge.TOTP && !challenge.SMS && !challenge.Email && !challenge.WhatsApp) {
			return fmt.Errorf("%w (web account confirmation)", ErrUnsupportedChallenge)
		}
		useTOTP := challenge.TOTP && !challenge.SMS && !challenge.Email && !challenge.WhatsApp
		code, readErr := secondFactorContext(ctx, "Instagram two-factor code: ", useTOTP)
		if readErr != nil {
			return readErr
		}
		if code == "" {
			return errors.New("instagram two-factor code is required")
		}
		if err = client.CompleteInstagramWebSessionTwoFactor(ctx, code); err != nil {
			if errors.Is(err, instameow.ErrInstagramWebTwoFactorCodeRejected) {
				return err
			}
			return fmt.Errorf("instagram web two-factor verification failed: %w", safeProviderError(err))
		}
	}
	return nil
}

// parseCookies accepts a browser Cookie header or a JSON object of cookie
// names to values. It intentionally never echoes invalid input values.
func parseCookies(input string) (map[string]string, error) {
	input = strings.TrimSpace(input)
	if strings.IndexFunc(input, unicode.IsControl) >= 0 {
		return nil, errors.New("instagram cookie input contains control characters")
	}
	if strings.HasPrefix(strings.ToLower(input), "cookie:") {
		input = strings.TrimSpace(input[len("cookie:"):])
	}
	if input == "" {
		return nil, errors.New("instagram cookies are empty")
	}
	result := map[string]string{}
	if strings.HasPrefix(input, "{") {
		if err := json.Unmarshal([]byte(input), &result); err != nil {
			return nil, errors.New("instagram cookies must be a JSON string map or HTTP Cookie header")
		}
	} else {
		segments := strings.Split(input, ";")
		for i, raw := range segments {
			item := strings.TrimSpace(raw)
			if item == "" && i == len(segments)-1 {
				// A trailing semicolon is valid in a browser Cookie header.
				continue
			}
			pair := strings.SplitN(item, "=", 2)
			if len(pair) != 2 || strings.TrimSpace(pair[0]) == "" {
				return nil, errors.New("invalid instagram cookie header format")
			}
			result[strings.TrimSpace(pair[0])] = strings.TrimSpace(pair[1])
		}
	}
	for name, value := range result {
		if name == "" || strings.IndexFunc(name, func(c rune) bool {
			return !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
				(c >= '0' && c <= '9') || c == '_' || c == '-')
		}) >= 0 || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return nil, errors.New("invalid instagram cookie name or value")
		}
	}
	if missing := newCookies(result).GetMissingCookieNames(); len(missing) > 0 {
		return nil, fmt.Errorf("instagram cookies missing required names: %v", missing)
	}
	if len(result) > 128 {
		return nil, errors.New("too many instagram cookies")
	}
	if err := validateCookieAccount(result); err != nil {
		return nil, err
	}
	return result, nil
}

// Prevent control-sequence injection through provider-supplied UI text.
func safePrompt(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.TrimSpace(text)
}

// Ensure cookie constant names remain compile-checked against upstream.
var _ = cookies.IGCookieSessionID
