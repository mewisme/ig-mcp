package totp

import (
	"encoding/base32"
	"fmt"
	"strings"
	"testing"
	"time"
)

const rfcSecret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

func TestGenerateRFC6238SHA1(t *testing.T) {
	// RFC 6238 Appendix B publishes eight-digit SHA1 codes; the standard
	// six-digit result is the last six digits of the same truncated value.
	cases := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tt := range cases {
		t.Run(fmt.Sprint(tt.unix), func(t *testing.T) {
			got, err := Generate(rfcSecret, time.Unix(tt.unix, 0))
			if err != nil || got != tt.want {
				t.Fatalf("Generate() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestGenerateBase32Formats(t *testing.T) {
	at := time.Unix(59, 0)
	formats := []string{
		rfcSecret,
		"  gezd-gnbv gy3t-qojq gezd-gnbv gy3t-qojq  ",
		"GEZDGNBV-GY3TQOJQ-GEZDGNBV-GY3TQOJQ",
	}
	for _, secret := range formats {
		got, err := Generate(secret, at)
		if err != nil || got != "287082" {
			t.Fatalf("Generate(normalized Base32) = %q, %v; want 287082", got, err)
		}
	}

	padded := base32.StdEncoding.EncodeToString([]byte("1234567"))
	withPadding, err := Generate(padded, at)
	if err != nil {
		t.Fatalf("Generate(padded Base32): %v", err)
	}
	withoutPadding, err := Generate(strings.TrimRight(padded, "="), at)
	if err != nil || withoutPadding != withPadding {
		t.Fatalf("padded/unpadded mismatch: %q, %v", withoutPadding, err)
	}
}

func TestGenerateCounterBoundaries(t *testing.T) {
	for _, pair := range []struct {
		first, second int64
		same          bool
	}{
		{0, 29, true},
		{29, 30, false},
		{30, 59, true},
		{59, 60, false},
		{30*(1<<32) - 1, 30 * (1 << 32), false},
		{30 * (1 << 32), 30*(1<<32) + 29, true},
	} {
		first, err := Generate(rfcSecret, time.Unix(pair.first, 0))
		if err != nil {
			t.Fatal(err)
		}
		second, err := Generate(rfcSecret, time.Unix(pair.second, 0))
		if err != nil {
			t.Fatal(err)
		}
		if (first == second) != pair.same {
			t.Errorf("times %d, %d: got %q, %q; want same=%v", pair.first, pair.second, first, second, pair.same)
		}
	}
	// A uint32 counter would wrap to zero at this boundary.
	farFuture, err := Generate(rfcSecret, time.Unix(30*(1<<32), 0))
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := Generate(rfcSecret, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if farFuture == epoch {
		t.Error("TOTP counter appears truncated to 32 bits")
	}
	if _, err := Generate(rfcSecret, time.Unix(-1, 0)); err == nil {
		t.Error("expected an error for pre-epoch time")
	}
}

func TestGenerateOtpauthURI(t *testing.T) {
	at := time.Unix(59, 0)
	for _, uri := range []string{
		"otpauth://totp/Instagram:account?secret=" + rfcSecret,
		"otpauth://totp/Instagram:account?issuer=Instagram&algorithm=SHA1&digits=6&period=30&secret=" + rfcSecret,
		"otpauth://TOTP/Instagram%3Aaccount?Secret=" + strings.ToLower(rfcSecret) + "&Algorithm=sha1&Digits=6&Period=30",
	} {
		got, err := Generate(uri, at)
		if err != nil || got != "287082" {
			t.Fatalf("Generate(otpauth URI) = %q, %v; want 287082", got, err)
		}
	}
	withZone, err := Generate(rfcSecret, at.In(time.FixedZone("UTC+7", 7*60*60)))
	if err != nil || withZone != "287082" {
		t.Fatalf("Generate(with timezone) = %q, %v; want 287082", withZone, err)
	}
	padded := base32.StdEncoding.EncodeToString([]byte("1234567"))
	fromRaw, err := Generate(padded, at)
	if err != nil {
		t.Fatal(err)
	}
	fromURI, err := Generate("otpauth://totp/Instagram:test?secret="+padded, at)
	if err != nil || fromURI != fromRaw {
		t.Fatalf("padded otpauth URI = %q, %v; want %q", fromURI, err, fromRaw)
	}
}

func TestGenerateRejectsInvalidSecretsAndURIs(t *testing.T) {
	cases := []string{
		"", " ", "!!!", "A", "AB=C", "MZXW6===A", "MZXW6====",
		"otpauth://hotp/Instagram?secret=" + rfcSecret,
		"otpauth:/totp/Instagram?secret=" + rfcSecret,
		"otpauth://totp/Instagram", "otpauth://totp/Instagram?secret=",
		"otpauth://totp/Instagram?secret=INVALID!SECRET",
		"otpauth://totp/Instagram?secret=%ZZ",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&algorithm=SHA256",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&algorithm=",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&digits=8",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&digits=",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&period=60",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&period=",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&algorithm=SHA1&Algorithm=SHA256",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&digits=6&digits=8",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&period=30&period=60",
		"otpauth://totp/Instagram?secret=" + rfcSecret + "&secret=" + rfcSecret,
	}
	for i, secret := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			if got, err := Generate(secret, time.Unix(59, 0)); err == nil || got != "" {
				t.Fatalf("Generate(invalid secret) = %q, %v; want empty code and error", got, err)
			}
		})
	}
}

func TestGenerateErrorsDoNotExposeSecret(t *testing.T) {
	const marker = "PRIVATE!SEED_DO_NOT_EXPOSE"
	for _, secret := range []string{marker, "otpauth://totp/test?secret=" + marker} {
		_, err := Generate(secret, time.Unix(59, 0))
		if err == nil || strings.Contains(err.Error(), marker) {
			t.Fatalf("expected sanitized error for invalid secret; got %v", err)
		}
	}
}
