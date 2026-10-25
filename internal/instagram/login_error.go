package instagram

import (
	"context"
	"errors"

	"go.mau.fi/mautrix-meta/pkg/instameow"
)

// LoginError distinguishes user action from transport failure without exposing
// provider HTTP responses, tokens or challenge fields.
type LoginError struct {
	Kind    string
	message string
	cause   error
}

func (e *LoginError) Error() string { return "instagram login (" + e.Kind + "): " + e.message }
func (e *LoginError) Unwrap() error { return e.cause }

func classifyLoginError(err error) error {
	if err == nil {
		return nil
	}
	var classified *LoginError
	if errors.As(err, &classified) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &LoginError{"cancelled", "login cancelled", context.Canceled}
	case errors.Is(err, context.DeadlineExceeded):
		return &LoginError{"timeout", "login timed out", context.DeadlineExceeded}
	case errors.Is(err, ErrUnsupportedChallenge):
		return &LoginError{"challenge_required", "complete verification in Instagram, then import browser cookies", ErrUnsupportedChallenge}
	case errors.Is(err, instameow.ErrInstagramWebLoginRejected):
		return &LoginError{"credentials_rejected", "Instagram rejected the login", instameow.ErrInstagramWebLoginRejected}
	case errors.Is(err, instameow.ErrInstagramWebTwoFactorCodeRejected):
		return &LoginError{"verification_rejected", "Instagram rejected the two-factor code", instameow.ErrInstagramWebTwoFactorCodeRejected}
	case errors.Is(err, ErrSessionConflict):
		return &LoginError{"session_conflict", "session changed in another process; retry from the saved session", ErrSessionConflict}
	case errors.Is(err, ErrRateLimited):
		return &LoginError{"rate_limited", "Instagram rate-limited login attempts", ErrRateLimited}
	default:
		// Upstream responses can contain credential-bearing data, including their errors.
		return &LoginError{"failed", "login was not completed; check the session and retry", nil}
	}
}
