package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func testCookies() map[string]string {
	return map[string]string{
		"sessionid":  "123%3Asecret",
		"csrftoken":  "csrf-token",
		"ds_user_id": "123",
		"mid":        "device",
	}
}

func TestParseCookies(t *testing.T) {
	header, err := parseCookies("Cookie: sessionid=123%3Asecret; csrftoken=csrf-token; ds_user_id=123; mid=device")
	if err != nil {
		t.Fatalf("parse header: %v", err)
	}
	if !reflect.DeepEqual(header, testCookies()) {
		t.Fatalf("unexpected parsed cookie names or values")
	}
	raw, _ := json.Marshal(testCookies())
	fromJSON, err := parseCookies(string(raw))
	if err != nil || !reflect.DeepEqual(header, fromJSON) {
		t.Fatalf("parse JSON: %v", err)
	}
}

func TestParseCookiesWithTrailingSeparator(t *testing.T) {
	const header = "datr=sample;ig_did=dummy-id;mid=dummy-mid; wd=3440x1271;csrftoken=csrf-test;ds_user_id=123;sessionid=123%3Atest%3Avalue;rur=EAG%2C123%2C456; "
	parsed, err := parseCookies(header)
	if err != nil {
		t.Fatalf("parse cookies with trailing separator: %v", err)
	}
	if len(parsed) != 8 || parsed["sessionid"] != "123%3Atest%3Avalue" ||
		parsed["wd"] != "3440x1271" || parsed["rur"] != "EAG%2C123%2C456" {
		t.Fatalf("did not preserve cookie fields, parsed %d fields", len(parsed))
	}
	for _, bad := range []string{
		"sessionid=a;csrftoken=b;ds_user_id=1;;",
		";sessionid=a;csrftoken=b;ds_user_id=1",
		"sessionid=a;csrftoken=b;ds_user_id=1;invalid_cookie;",
	} {
		if _, err := parseCookies(bad); err == nil {
			t.Fatal("accepted malformed cookie item")
		}
	}
}

func TestRejectMalformedCookies(t *testing.T) {
	for _, raw := range []string{
		"", "sessionid=secret", "sessionid=x; csrftoken=y; ds_user_id",
		"sessionid=x; csrftoken=y; ds_user_id=1; name=with\nnewline",
		`{"sessionid":"x","csrftoken":"y","ds_user_id":"1","bad key":"z"}`,
		`{"sessionid":"x","csrftoken":"y","ds_user_id":"1","mid":"value\u0000bad"}`,
		"{incomplete json",
	} {
		if _, err := parseCookies(raw); err == nil {
			t.Errorf("expected error for malformed cookie input")
		}
	}
}

func TestSessionStorageRoundTripPrivate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "instagram", "session.json")
	expected := diskSession{Version: 1, Cookies: testCookies(), Native: &types.InstagramNativeSession{
		Authorization: "native-authorization",
		UserID:        "123",
	}, Device: &types.InstagramLoginDevice{PhoneID: "test-device"}}
	if err := writeSession(file, expected); err != nil {
		t.Fatal(err)
	}
	d, err := os.Stat(filepath.Dir(file))
	if err != nil || (runtime.GOOS != "windows" && d.Mode().Perm() != 0700) {
		t.Fatalf("insecure credential dir: %v", err)
	}
	if err := checkPrivatePath(filepath.Dir(file), d, true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("insecure credential file: %v", err)
	}
	if err := checkPrivatePath(file, info, false); err != nil {
		t.Fatal(err)
	}
	actual, err := readSession(file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expected, actual) {
		t.Fatalf("session round trip changed fields")
	}
	if err := validateSession(actual); err != nil {
		t.Fatal(err)
	}
	// Repeated atomic writes must retain private permission.
	if err := writeSession(file, expected); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(file)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("atomic rewrite broadened permissions")
	}
	if err := checkPrivatePath(file, info, false); err != nil {
		t.Fatal(err)
	}
}

func TestReadSessionSecurityAndVersion(t *testing.T) {
	file := filepath.Join(t.TempDir(), "private", "session.json")
	if _, err := readSession(file); !errors.Is(err, ErrNoSession) {
		t.Fatalf("missing session: %v", err)
	}
	if err := writeSession(file, diskSession{Version: 1, Cookies: testCookies()}); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(file, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := readSession(file); err == nil {
			t.Fatal("insecure credential file should be rejected")
		}
		if err := os.Chmod(file, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeSession(file, diskSession{Version: 2, Cookies: testCookies()}); err != nil {
		t.Fatal(err)
	}
	d, err := readSession(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateSession(d); err == nil {
		t.Fatal("newer format must be rejected")
	}
}

func TestSymlinkSessionRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	link := filepath.Join(dir, "session.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readSession(link); err == nil {
		t.Fatal("symlink session should be refused")
	}
	if err := writeSession(link, diskSession{Version: 1}); err == nil {
		t.Fatal("symlink overwrite should be refused")
	}
}

func TestStatusReportsSavedSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.json")
	t.Setenv("IG_MCP_SESSION_FILE", path)
	if err := writeSession(path, diskSession{Version: 1, Cookies: testCookies()}); err != nil {
		t.Fatal(err)
	}
	if got := New().Status(); got != "session_saved" {
		t.Fatalf("status for saved offline session = %q, want session_saved", got)
	}
}

func TestServiceNoSessionAndClose(t *testing.T) {
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(t.TempDir(), "instagram", "session.json"))
	s := New()
	if s.Raw() != nil || s.Status() != "not_logged_in" {
		t.Fatal("new client must start unauthenticated")
	}
	if err := s.Open(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("unexpected empty session result: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if s.Raw() != nil || s.Status() != "closed" {
		t.Fatal("close should detach client")
	}
}

func TestSaveDeviceAcrossUnfinishedLogin(t *testing.T) {
	t.Setenv("IG_MCP_SESSION_FILE", filepath.Join(t.TempDir(), "ig", "session.json"))
	s := New()
	device := types.InstagramLoginDevice{PhoneID: "durable-phone"}
	if err := s.saveDevice(device); err != nil {
		t.Fatal(err)
	}
	d, err := s.readDisk()
	if err != nil {
		t.Fatal(err)
	}
	if d.Device == nil || d.Device.PhoneID != device.PhoneID {
		t.Fatal("Android device identity was not persisted")
	}
	if err := validateSession(d); err == nil {
		t.Fatal("device-only persistence is not an authenticated session")
	}
}

func TestSecretValuesNotEmbeddedInErrors(t *testing.T) {
	input := "sessionid=very-secret; csrftoken=a; ds_user_id=1;\n bad=thing"
	_, err := parseCookies(input)
	if err == nil || strings.Contains(err.Error(), "very-secret") {
		t.Fatal("cookie parser leaked raw secret data")
	}
}
