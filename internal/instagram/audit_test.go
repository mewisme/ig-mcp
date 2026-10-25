package instagram

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

func TestSessionRevisionRejectsStaleClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.json")
	t.Setenv("IG_MCP_SESSION_FILE", path)
	if err := writeSession(path, diskSession{Version: 1, Cookies: testCookies()}); err != nil {
		t.Fatal(err)
	}
	a, b := New(), New()
	aDisk, err := a.readDisk()
	if err != nil {
		t.Fatal(err)
	}
	bDisk, err := b.readDisk()
	if err != nil {
		t.Fatal(err)
	}
	aClient := a.newClient(aDisk, false, "")
	bClient := b.newClient(bDisk, false, "")
	if err := a.saveClient(aClient); err != nil {
		t.Fatal(err)
	}
	if err := b.saveClient(bClient); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("stale client must not overwrite another process: %v", err)
	}
	fresh, err := readSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation == "" || fresh.Revision != 1 {
		t.Fatalf("session revision/generation missing: rev=%d", fresh.Revision)
	}
	newClient := a.newClient(fresh, true, "")
	if err := a.saveClient(newClient); err != nil {
		t.Fatal(err)
	}
	replaced, err := readSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Generation == fresh.Generation || replaced.Revision != fresh.Revision+1 {
		t.Fatal("explicit replacement did not rotate session generation")
	}
	if err := a.saveClient(aClient); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("previous generation must not overwrite a replacement: %v", err)
	}
}

func TestSessionLockCrossProcess(t *testing.T) {
	if os.Getenv("IG_MCP_TEST_LOCK_CHILD") == "1" {
		path := os.Getenv("IG_MCP_TEST_LOCK_PATH")
		ready := os.Getenv("IG_MCP_TEST_LOCK_READY")
		release := os.Getenv("IG_MCP_TEST_LOCK_RELEASE")
		err := withSessionLock(path, func() error {
			if err := os.WriteFile(ready, []byte("ready"), 0600); err != nil {
				return err
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(release); err == nil {
					return nil
				}
				time.Sleep(10 * time.Millisecond)
			}
			return errors.New("test lock release missing")
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	path, ready, release := filepath.Join(dir, "private", "session.json"), filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLockCrossProcess$")
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	cmd.Stdout = &diagnostics
	cmd.Env = append(os.Environ(), "IG_MCP_TEST_LOCK_CHILD=1", "IG_MCP_TEST_LOCK_PATH="+path, "IG_MCP_TEST_LOCK_READY="+ready, "IG_MCP_TEST_LOCK_RELEASE="+release)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatalf("child did not acquire lock: %v; child stderr: %s", err, diagnostics.String())
	}
	completed := make(chan error, 1)
	go func() { completed <- withSessionLock(path, func() error { return nil }) }()
	select {
	case err := <-completed:
		t.Fatalf("cross-process lock did not block: %v", err)
	case <-time.After(75 * time.Millisecond):
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second process could not acquire released lock")
	}
}

func TestDeviceIdentityIsIsolatedBetweenAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "session.json")
	t.Setenv("IG_MCP_SESSION_FILE", path)
	old := diskSession{Version: 1, Cookies: testCookies(), Device: &types.InstagramLoginDevice{PhoneID: "old-phone"}, DeviceIdentity: "old-user", DeviceAccountID: "123"}
	if err := writeSession(path, old); err != nil {
		t.Fatal(err)
	}
	s := New()
	base, err := s.readDisk()
	if err != nil {
		t.Fatal(err)
	}
	newClient := s.newClient(base, true, "other-user")
	if err := s.saveDeviceForClient(newClient, types.InstagramLoginDevice{PhoneID: "other-phone"}); err != nil {
		t.Fatal(err)
	}
	stored, err := readSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Device == nil || stored.Device.PhoneID != "old-phone" || stored.PendingDevice == nil || stored.PendingDevice.PhoneID != "other-phone" || stored.PendingIdentity != "other-user" {
		t.Fatal("unfinished login polluted the verified account's device identity")
	}
	unchanged := s.newClient(stored, false, "")
	if err := s.saveClient(unchanged); err != nil {
		t.Fatal(err)
	}
	again, err := readSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.PendingDevice == nil || again.PendingIdentity != "other-user" {
		t.Fatal("normal Open must not delete pending Android login state")
	}
	if err := s.saveDeviceForClient(newClient, types.InstagramLoginDevice{PhoneID: "changed-phone"}); !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("stale incomplete login must not overwrite active account revision: %v", err)
	}
}

func TestSessionCookieAccountAndDeviceMismatch(t *testing.T) {
	values := testCookies()
	values["ds_user_id"] = "999"
	if err := validateSession(diskSession{Version: 1, Cookies: values}); err == nil {
		t.Fatal("accepted mismatched ds_user_id/sessionid")
	}
	if _, err := parseCookies("sessionid=123%3Adata;csrftoken=csrf;ds_user_id=999;mid=browser"); err == nil {
		t.Fatal("accepted mismatched imported cookies")
	}
	if err := validateSession(diskSession{Version: 1, Cookies: testCookies(), DeviceAccountID: "999"}); err == nil {
		t.Fatal("accepted foreign device")
	}
}

func TestSessionDirectorySymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	private := filepath.Join(dir, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(private, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	path := filepath.Join(alias, "session.json")
	if _, err := readSession(path); err == nil {
		t.Fatal("credential read followed a symlink directory")
	}
	if err := writeSession(path, diskSession{Version: 1, Cookies: testCookies()}); err == nil {
		t.Fatal("credential write followed a symlink directory")
	}
}

func TestEpochChangesRequirePollingCursorReset(t *testing.T) {
	s := New()
	s.captureEvent(&slidetypes.Connected{})
	first := s.PollEventsWithEpoch(0, "", 10)
	if first.Epoch == "" || len(first.Events) != 1 {
		t.Fatal("missing initial event epoch")
	}
	s.eventsMu.Lock()
	s.eventEpoch, _ = newGeneration()
	s.events = nil
	s.eventSeq = 0
	s.eventsMu.Unlock()
	s.captureEvent(&slidetypes.Connected{}) // Sequence happens to match previous account.
	stale := s.PollEventsWithEpoch(first.NextSequence, first.Epoch, 10)
	if !stale.ResetRequired || len(stale.Events) != 0 || stale.Epoch == first.Epoch {
		t.Fatalf("did not detect same-sequence different-account cursor: %+v", stale)
	}
}

func TestSocketHealthAndAccountValidityAreIndependent(t *testing.T) {
	s := New()
	if s.RealtimeStatus() != "disconnected" {
		t.Fatal("new service must not report a connected socket")
	}
	client := s.newClient(diskSession{Version: 1, Cookies: testCookies()}, false, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.mu.Lock()
	s.client, s.cancel = client, cancel
	s.status, s.realtimeStatus = "ready", "connected"
	s.mu.Unlock()
	if s.Raw() != client || s.RealtimeStatus() != "connected" {
		t.Fatal("ready client should be available")
	}
	s.captureEvent(&slidetypes.AuthError{})
	s.invalidateClient(client, "expired", "authentication_expired")
	if s.Raw() != nil || s.Status() != "expired" || s.RealtimeStatus() != "authentication_expired" {
		t.Fatal("expired auth must not expose a ready client")
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("realtime socket not cancelled on authentication expiration")
	}
	if page := s.PollEventsWithEpoch(0, "", 20); len(page.Events) != 1 || page.Events[0].Type != "authentication_expired" {
		t.Fatal("events must remain readable on expired authentication")
	}
	if err := s.Open(context.Background()); err == nil {
		t.Fatal("inactive/expired client must require explicit Close/Open")
	}
}

func TestLoginErrorsAreTypedAndRedacted(t *testing.T) {
	var typed *LoginError
	if err := classifyLoginError(errors.New("sessionid=super-secret")); !errors.As(err, &typed) || typed.Kind != "failed" || strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("unsafe failure classification: %v", err)
	}
	if err := classifyLoginError(context.Canceled); !errors.As(err, &typed) || typed.Kind != "cancelled" || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error lost identity: %v", err)
	}
	if err := classifyLoginError(ErrSessionConflict); !errors.As(err, &typed) || typed.Kind != "session_conflict" || !errors.Is(err, ErrSessionConflict) {
		t.Fatalf("session conflict error lost identity: %v", err)
	}
}
