// Package totp generates standard six-digit TOTP codes for Instagram login.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// Generate returns the RFC 6238 six-digit TOTP at the specified time.
// secret may be a Base32 shared secret or an otpauth://totp URI.
func Generate(secret string, at time.Time) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	if at.Unix() < 0 {
		return "", errors.New("totp: time is before the Unix epoch")
	}

	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(counter[:])
	digest := mac.Sum(nil)
	offset := int(digest[len(digest)-1] & 0x0f)
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000), nil
}

func decodeSecret(input string) ([]byte, error) {
	secret := strings.TrimSpace(input)
	if strings.HasPrefix(strings.ToLower(secret), "otpauth:") {
		u, err := url.Parse(secret)
		if err != nil || !strings.EqualFold(u.Scheme, "otpauth") || !strings.EqualFold(u.Host, "totp") || u.User != nil {
			return nil, errors.New("totp: invalid otpauth URI")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return nil, errors.New("totp: invalid otpauth URI")
		}
		params := make(map[string]string, 4)
		for name, values := range query {
			key := strings.ToLower(name)
			switch key {
			case "secret", "algorithm", "digits", "period":
				if len(values) != 1 {
					return nil, errors.New("totp: duplicate otpauth parameter")
				}
				if _, exists := params[key]; exists {
					return nil, errors.New("totp: duplicate otpauth parameter")
				}
				params[key] = values[0]
			}
		}
		if algorithm, ok := params["algorithm"]; ok && !strings.EqualFold(algorithm, "SHA1") {
			return nil, errors.New("totp: unsupported otpauth algorithm")
		}
		if digits, ok := params["digits"]; ok && digits != "6" {
			return nil, errors.New("totp: unsupported otpauth digits")
		}
		if period, ok := params["period"]; ok && period != "30" {
			return nil, errors.New("totp: unsupported otpauth period")
		}
		secret = params["secret"]
	}

	secret = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '-' {
			return -1
		}
		return unicode.ToUpper(r)
	}, secret)
	encoding := base32.StdEncoding.WithPadding(base32.NoPadding)
	if strings.ContainsRune(secret, '=') {
		encoding = base32.StdEncoding
	}
	decoded, err := encoding.DecodeString(secret)
	if err != nil || len(decoded) == 0 || encoding.EncodeToString(decoded) != secret {
		return nil, errors.New("totp: invalid Base32 secret")
	}
	return decoded, nil
}
