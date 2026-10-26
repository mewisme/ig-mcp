// Package instagram owns the durable, single-account Instagram client lifecycle.
// It does not expose authentication as MCP tools.
package instagram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"go.mau.fi/mautrix-meta/pkg/instameow"
	"go.mau.fi/mautrix-meta/pkg/instameow/slidetypes"
	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/httpclient"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"
)

var (
	// ErrNoSession means no previously authenticated Instagram session exists.
	ErrNoSession = errors.New("instagram session not found; run CLI login")
	// ErrUnsupportedChallenge means Instagram requires an interactive browser step
	// that cannot safely be completed through a terminal.
	ErrUnsupportedChallenge = errors.New("instagram requires an unsupported interactive challenge; finish verification in Instagram and retry cookie login")
	ErrRateLimited          = errors.New("rate-limited by Instagram")
)

const maxSessionBytes = 8 << 20

type diskSession struct {
	Version         int                           `json:"version"`
	Revision        uint64                        `json:"revision,omitempty"`
	Generation      string                        `json:"generation,omitempty"`
	Cookies         map[string]string             `json:"cookies,omitempty"`
	Native          *types.InstagramNativeSession `json:"native,omitempty"`
	Device          *types.InstagramLoginDevice   `json:"device,omitempty"`
	DeviceIdentity  string                        `json:"device_identity,omitempty"`
	DeviceAccountID string                        `json:"device_account_id,omitempty"`
	PendingDevice   *types.InstagramLoginDevice   `json:"pending_device,omitempty"`
	PendingIdentity string                        `json:"pending_identity,omitempty"`
}

type clientSaveState struct {
	base     diskSession
	replace  bool
	identity string
}

// AccountInfo is safe to display after Instagram verifies the session.
// It excludes cookies, authentication tokens and other credentials.
type AccountInfo struct {
	ID       string
	Username string
	FullName string
}

// Service manages exactly one authenticated Instagram account. Lifecycle
// operations are serialized; Raw safely returns a snapshot pointer. Consumers
// must still respect instameow's own concurrency requirements when using Raw.
type Service struct {
	lifecycle      sync.Mutex
	mu             sync.RWMutex
	diskMu         sync.Mutex
	clients        map[*instameow.Client]*clientSaveState
	eventsMu       sync.Mutex
	events         []Event
	eventSeq       uint64
	eventEpoch     string
	path           string
	client         *instameow.Client
	cancel         context.CancelFunc
	stopped        chan struct{}
	resnapshot     bool
	realtimeStatus string
	status         string
	account        AccountInfo
}

// New creates a service using the private config directory. Override
// IG_MCP_SESSION_FILE to select a different absolute credential file path.
func New() *Service {
	configDir, err := os.UserConfigDir()
	path := ""
	if err == nil {
		path = filepath.Join(configDir, "ig-mcp", "session.json")
	}
	if override := strings.TrimSpace(os.Getenv("IG_MCP_SESSION_FILE")); override != "" {
		path = override
	}
	epoch, _ := newGeneration()
	return &Service{path: path, status: "not_logged_in", realtimeStatus: "disconnected", eventEpoch: epoch, clients: make(map[*instameow.Client]*clientSaveState)}
}

// RealtimeStatus reports socket health separately from authenticated API readiness.
func (s *Service) RealtimeStatus() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.realtimeStatus
}

// Status returns a non-secret lifecycle state. session_saved means credentials
// exist locally but have not been validated against Instagram in this process.
// "ready" means mailbox validation succeeded, not guaranteed socket health.
func (s *Service) Status() string {
	s.mu.RLock()
	current := s.status
	s.mu.RUnlock()
	if current != "not_logged_in" {
		return current
	}
	stored, err := s.readDisk()
	if errors.Is(err, ErrNoSession) {
		return current
	}
	if err != nil || validateSession(stored) != nil {
		return "unavailable"
	}
	return "session_saved"
}

// Raw returns the authenticated instameow client (or nil if not ready).
// Lifecycle replacement is lock-safe, but callers should not keep using a
// previously returned client after Close or another lifecycle operation.
func (s *Service) Raw() *instameow.Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status != "ready" {
		return nil
	}
	return s.client
}

// VerifiedAccount exposes the account returned by a successful LoadIndex.
func (s *Service) VerifiedAccount() (AccountInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.account, s.status == "ready"
}

func (s *Service) setStatus(status string) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

// Open validates cookies from a durable session and starts realtime sync.
// Missing or invalid credentials never trigger an implicit login.
func (s *Service) Open(ctx context.Context) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if ctx == nil {
		return errors.New("nil context")
	}
	s.mu.RLock()
	active := s.client != nil
	currentStatus := s.status
	s.mu.RUnlock()
	if active {
		if currentStatus != "ready" {
			return fmt.Errorf("instagram client is %s; close the client before reopening", currentStatus)
		}
		return nil
	}
	stored, err := s.readDisk()
	if err != nil {
		s.setStatus("not_logged_in")
		return err
	}
	if err := validateSession(stored); err != nil {
		s.setStatus("expired")
		return fmt.Errorf("instagram session is incomplete: %w", err)
	}
	client := s.newClient(stored, false, "")
	// Always load a fresh index: even a recently cached one can represent
	// an invalidated account, and arbitrary persisted index JSON is untrusted.
	if err := s.activate(ctx, client); err != nil {
		s.discardClient(client)
		return err
	}
	return nil
}

// Close stops realtime reconnection, persists the latest credentials, and
// detaches the client. Close is idempotent.
func (s *Service) Close() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	return s.closeLocked()
}

// ForgetSession removes only local credentials. It never calls Instagram's
// remote logout or revocation endpoints. Missing sessions are already forgotten.
func (s *Service) ForgetSession() error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if err := s.closeLocked(); err != nil {
		return err
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	err := withSessionLock(s.path, func() error {
		info, err := os.Lstat(s.path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if err := checkPrivatePath(s.path, info, false); err != nil {
			return err
		}
		return os.Remove(s.path)
	})
	if err == nil {
		s.setStatus("not_logged_in")
	}
	return err
}

func (s *Service) closeLocked() error {
	s.mu.Lock()
	c, cancel, stopped := s.client, s.cancel, s.stopped
	s.client, s.cancel = nil, nil
	s.status = "closed"
	s.realtimeStatus = "disconnected"
	s.account = AccountInfo{}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if c == nil {
		return nil
	}
	finished := make(chan struct{})
	go func() { c.Disconnect(); close(finished) }()
	deadline := time.NewTimer(7 * time.Second)
	defer deadline.Stop()
	select {
	case <-finished:
	case <-deadline.C:
		return errors.New("instagram socket cleanup exceeded deadline; credentials were not overwritten")
	}
	if stopped != nil {
		select {
		case <-stopped:
		case <-deadline.C:
			return errors.New("instagram realtime shutdown exceeded deadline; credentials were not overwritten")
		}
	}
	err := s.saveClient(c)
	s.discardClient(c)
	return err
}

func (s *Service) discardClient(c *instameow.Client) {
	s.diskMu.Lock()
	delete(s.clients, c)
	s.diskMu.Unlock()
}

func (s *Service) activate(ctx context.Context, c *instameow.Client) error {
	s.setStatus("connecting")
	viewer, _, err := c.LoadIndex(ctx)
	if err != nil {
		if isAuthError(err) {
			s.setStatus("expired")
		} else {
			s.setStatus("unavailable")
		}
		return fmt.Errorf("instagram session validation failed: %w", safeProviderError(err))
	}
	if err := s.saveClient(c); err != nil {
		s.setStatus("closed")
		return fmt.Errorf("persist instagram session: %w", err)
	}
	s.eventsMu.Lock()
	s.events = nil
	s.eventSeq = 0
	s.eventEpoch, _ = newGeneration()
	s.eventsMu.Unlock()
	realtimeCtx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.client = c
	s.cancel = cancel
	s.stopped = make(chan struct{})
	s.resnapshot = false
	s.realtimeStatus = "connecting"
	s.status = "ready"
	s.account = accountFromViewer(viewer, c.GetCookies().Get(cookies.IGCookieDSUserID))
	stopped := s.stopped
	s.mu.Unlock()
	go func() {
		defer close(stopped)
		s.superviseRealtime(realtimeCtx, c)
	}()
	return nil
}

func (s *Service) superviseRealtime(ctx context.Context, c *instameow.Client) {
	for ctx.Err() == nil {
		c.Connect(ctx)
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		if s.client != c || s.status != "ready" {
			s.mu.Unlock()
			return
		}
		refresh := s.resnapshot
		s.resnapshot = false
		if refresh {
			s.realtimeStatus = "resyncing"
		} else {
			s.realtimeStatus = "reconnecting"
		}
		s.mu.Unlock()
		if refresh {
			for ctx.Err() == nil {
				loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				_, _, err := c.LoadIndex(loadCtx)
				cancel()
				if err == nil {
					if err = s.saveClient(c); err == nil {
						break
					}
				}
				if isAuthError(err) || errors.Is(err, ErrSessionConflict) {
					if errors.Is(err, ErrSessionConflict) {
						s.invalidateClient(c, "stale_session", "stale_session")
					} else {
						s.invalidateClient(c, "expired", "authentication_expired")
					}
					return
				}
				if !waitRealtimeRetry(ctx) {
					return
				}
			}
		} else if !waitRealtimeRetry(ctx) {
			return
		}
	}
}

func waitRealtimeRetry(ctx context.Context) bool {
	t := time.NewTimer(2 * time.Second)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (s *Service) invalidateClient(c *instameow.Client, status, realtime string) {
	s.mu.Lock()
	var cancel context.CancelFunc
	if s.client == c {
		s.status = status
		s.realtimeStatus = realtime
		cancel = s.cancel
	}
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func accountFromViewer(viewer *types.PolarisViewer, fallbackID string) AccountInfo {
	account := AccountInfo{ID: safePrompt(fallbackID)}
	if viewer == nil {
		return account
	}
	for _, id := range []string{viewer.GetUserId(), viewer.Data.ID} {
		if cleanID := safePrompt(id); cleanID != "" {
			account.ID = cleanID
			break
		}
	}
	account.Username = safePrompt(viewer.GetUsername())
	account.FullName = safePrompt(viewer.GetName())
	return account
}

func isAuthError(err error) bool {
	return errors.Is(err, httpclient.ErrTokenInvalidated) ||
		errors.Is(err, httpclient.ErrChallengeRequired) ||
		errors.Is(err, httpclient.ErrCheckpointRequired) ||
		errors.Is(err, httpclient.ErrConsentRequired)
}

// Never surface raw upstream HTTP errors; they may contain cookies, bearer
// tokens, login fields or response bodies.
func safeProviderError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case isAuthError(err):
		return errors.New("credentials expired or Instagram verification required")
	case errors.Is(err, httpclient.ErrRateLimited):
		return ErrRateLimited
	default:
		return errors.New("Instagram request failed (upstream details suppressed to protect credentials)")
	}
}

func newCookies(values map[string]string) *cookies.Cookies {
	c := &cookies.Cookies{Platform: types.Instagram}
	input := make(map[cookies.MetaCookieName]string, len(values))
	for key, value := range values {
		input[cookies.MetaCookieName(key)] = value
	}
	c.UpdateValues(input)
	return c
}

func validateSession(stored diskSession) error {
	if stored.Version != 1 {
		return errors.New("unknown session file format")
	}
	if !validGeneration(stored.Generation) {
		return errors.New("invalid session generation")
	}
	c := newCookies(stored.Cookies)
	if missing := c.GetMissingCookieNames(); len(missing) > 0 {
		// Cookie names are safe to print; never include their values.
		return fmt.Errorf("missing required cookies: %v", missing)
	}
	if stored.Native != nil && (stored.Native.Authorization == "" ||
		stored.Native.UserID == "" || stored.Native.UserID != c.Get(cookies.IGCookieDSUserID)) {
		return errors.New("native authorization does not match cookie account")
	}
	if id := sessionCookieID(stored.Cookies); id == "" || (stored.DeviceAccountID != "" && stored.DeviceAccountID != id) {
		return errors.New("session account and device identity do not match")
	}
	if err := validateCookieAccount(stored.Cookies); err != nil {
		return err
	}
	return nil
}

func sessionCookieID(values map[string]string) string {
	return values[string(cookies.IGCookieDSUserID)]
}

func validateCookieAccount(values map[string]string) error {
	sessionID, err := url.PathUnescape(values[string(cookies.IGCookieSessionID)])
	if err != nil || strings.SplitN(sessionID, ":", 2)[0] != sessionCookieID(values) || !strings.Contains(sessionID, ":") {
		return errors.New("instagram sessionid does not match ds_user_id")
	}
	return nil
}

func (s *Service) newClient(stored diskSession, replace bool, identity string) *instameow.Client {
	device := stored.Device
	if !replace && stored.Native == nil && stored.DeviceAccountID != sessionCookieID(stored.Cookies) {
		device = nil // Legacy unbound devices may belong to another account.
	}
	if replace {
		device = nil
		if identity != "" {
			switch {
			case strings.EqualFold(stored.PendingIdentity, identity):
				device = stored.PendingDevice
			case stored.DeviceAccountID == sessionCookieID(stored.Cookies) && strings.EqualFold(stored.DeviceIdentity, identity):
				device = stored.Device
			}
		}
	}
	var c *instameow.Client
	log := zerolog.New(io.Discard) // upstream logs can contain identifying data
	c = instameow.NewClient(instameow.ClientParams{
		Cookies:           newCookies(stored.Cookies),
		Log:               log,
		NativeSession:     stored.Native,
		MobileLoginDevice: device,
		SaveMobileLoginDevice: func(_ context.Context, device types.InstagramLoginDevice) error {
			return s.saveDeviceForClient(c, device)
		},
		EventHandler: func(_ context.Context, event slidetypes.ClientEvent) error {
			if c == nil || s.Raw() != c {
				return nil
			}
			s.captureEvent(event)
			switch event.(type) {
			case *slidetypes.ReconnectionStateUpdate:
				if err := s.saveClient(c); errors.Is(err, ErrSessionConflict) {
					s.invalidateClient(c, "stale_session", "stale_session")
				} else if err != nil {
					s.invalidateClient(c, "unavailable", "persistence_error")
				}
			case *slidetypes.ResnapshotRequired:
				s.mu.Lock()
				s.resnapshot = true
				s.realtimeStatus = "resyncing"
				s.mu.Unlock()
			case *slidetypes.Connected:
				s.mu.Lock()
				s.realtimeStatus = "connected"
				s.mu.Unlock()
			case *slidetypes.Disconnected:
				s.mu.Lock()
				s.realtimeStatus = "reconnecting"
				s.mu.Unlock()
			case *slidetypes.AuthError:
				s.invalidateClient(c, "expired", "authentication_expired")
			}
			return nil
		},
	})
	s.diskMu.Lock()
	if s.clients == nil {
		s.clients = make(map[*instameow.Client]*clientSaveState)
	}
	s.clients[c] = &clientSaveState{base: diskSession{Generation: stored.Generation, Revision: stored.Revision}, replace: replace, identity: identity}
	s.diskMu.Unlock()
	return c
}

func (s *Service) saveDevice(device types.InstagramLoginDevice) error {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	return withSessionLock(s.path, func() error {
		stored, err := readSession(s.path)
		if errors.Is(err, ErrNoSession) {
			stored = diskSession{Version: 1}
		} else if err != nil {
			return err
		}
		if len(stored.Cookies) != 0 {
			return ErrSessionConflict // an unowned device must never replace another account's identity
		}
		stored.Device = &device
		stored.Revision++
		return writeSessionLocked(s.path, stored)
	})
}

func (s *Service) saveDeviceForClient(client *instameow.Client, device types.InstagramLoginDevice) error {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	state := s.clients[client]
	if state == nil || !state.replace || state.identity == "" {
		return ErrSessionConflict
	}
	return withSessionLock(s.path, func() error {
		stored, err := readSession(s.path)
		if errors.Is(err, ErrNoSession) {
			stored = diskSession{Version: 1}
		} else if err != nil {
			return err
		}
		if err = checkSessionRevision(stored, state.base); err != nil {
			return err
		}
		stored.PendingDevice = &device
		stored.PendingIdentity = strings.ToLower(state.identity)
		stored.Revision++
		if err = writeSessionLocked(s.path, stored); err == nil {
			state.base.Revision = stored.Revision
		}
		return err
	})
}

func (s *Service) readDisk() (diskSession, error) {
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	return readSession(s.path)
}

func (s *Service) saveClient(client *instameow.Client) error {
	if client == nil {
		return nil
	}
	s.diskMu.Lock()
	defer s.diskMu.Unlock()
	state := s.clients[client]
	if state == nil {
		return ErrSessionConflict
	}
	return withSessionLock(s.path, func() error {
		stored, err := readSession(s.path)
		if errors.Is(err, ErrNoSession) {
			stored = diskSession{Version: 1}
		} else if err != nil {
			return err
		}
		if err := checkSessionRevision(stored, state.base); err != nil {
			return err
		}
		previousID := sessionCookieID(stored.Cookies)
		values := make(map[string]string)
		for key, value := range client.GetCookies().GetAll() {
			values[string(key)] = value
		}
		if err := validateCookieAccount(values); err != nil {
			return err
		}
		newID := sessionCookieID(values)
		stored.Cookies = values
		stored.Native = client.GetInstagramNativeSession()
		if state.replace || stored.Generation == "" {
			stored.Generation, err = newGeneration()
			if err != nil {
				return err
			}
		}
		if state.replace {
			stored.PendingDevice = nil
			stored.PendingIdentity = ""
		}
		if stored.Native != nil {
			if stored.Native.UserID != newID {
				return errors.New("instagram native and cookie account mismatch")
			}
			device := stored.Native.Device
			stored.Device = &device
			stored.DeviceIdentity = strings.ToLower(state.identity)
			stored.DeviceAccountID = newID
		} else if previousID != newID || (stored.DeviceAccountID != "" && stored.DeviceAccountID != newID) {
			stored.Device = nil
			stored.DeviceIdentity = ""
			stored.DeviceAccountID = ""
		}
		if err := validateSession(stored); err != nil {
			return err
		}
		stored.Revision++
		if err = writeSessionLocked(s.path, stored); err == nil {
			state.base.Generation, state.base.Revision = stored.Generation, stored.Revision
			state.replace = false
		}
		return err
	})
}

func readSession(path string) (diskSession, error) {
	var result diskSession
	if path == "" || !filepath.IsAbs(path) {
		return result, errors.New("IG_MCP_SESSION_FILE must be absolute or a user config directory must be available")
	}
	if err := ensurePrivateDir(path); err != nil {
		return result, err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return result, ErrNoSession
	} else if err != nil {
		return result, fmt.Errorf("inspect session file: %w", err)
	}
	if err := checkPrivatePath(path, info, false); err != nil {
		return result, err
	}
	if info.Size() > maxSessionBytes {
		return result, errors.New("instagram session file is oversized")
	}
	f, err := openPrivateFile(path, os.O_RDONLY, 0)
	if err != nil {
		return result, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return result, errors.New("instagram session file changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSessionBytes+1))
	if err != nil {
		return result, err
	}
	if len(data) > maxSessionBytes || json.Unmarshal(data, &result) != nil {
		return diskSession{}, errors.New("instagram session file is invalid")
	}
	return result, nil
}

func writeSession(path string, value diskSession) error {
	return withSessionLock(path, func() error { return writeSessionLocked(path, value) })
}

func writeSessionLocked(path string, value diskSession) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("instagram session requires an absolute path")
	}
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(path); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if err := checkPrivatePath(path, info, false); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > maxSessionBytes {
		return errors.New("instagram session is too large")
	}
	temp, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if err = enforcePrivatePath(temp.Name(), false); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(temp.Name(), path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
