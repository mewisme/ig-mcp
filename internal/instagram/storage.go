package instagram

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var ErrSessionConflict = errors.New("instagram session changed in another process; reopen or login again")

func newGeneration() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}

func ensurePrivateDir(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("instagram session requires an absolute path")
	}
	dir := filepath.Clean(filepath.Dir(path))
	_, existingErr := os.Lstat(dir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	// No component may silently redirect the credential directory elsewhere.
	// macOS uses system-managed /var -> /private/var and /tmp -> /private/tmp
	// links. These are not user-controlled credential directory redirects.
	for part := dir; ; part = filepath.Dir(part) {
		info, err := os.Lstat(part)
		if err != nil {
			return errors.New("instagram session directory has an unsafe path component")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if runtime.GOOS != "darwin" || (part != "/var" && part != "/tmp") {
				return errors.New("instagram session directory has an unsafe path component")
			}
			target, err := filepath.EvalSymlinks(part)
			if err != nil || target != "/private"+part {
				return errors.New("instagram session directory has an unsafe path component")
			}
		} else if !info.IsDir() {
			return errors.New("instagram session directory has an unsafe path component")
		}
		if parent := filepath.Dir(part); parent == part {
			break
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if errors.Is(existingErr, os.ErrNotExist) {
		return enforcePrivatePath(dir, true)
	}
	return checkPrivatePath(dir, info, true)
}

func withSessionLock(path string, action func() error) error {
	if err := ensurePrivateDir(path); err != nil {
		return err
	}
	lockPath := path + ".lock"
	f, err := openPrivateFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open session lock: %w", err)
	}
	defer f.Close()
	info, err := os.Lstat(lockPath)
	if err != nil {
		return err
	}
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || !info.Mode().IsRegular() {
		return errors.New("instagram session lock changed while opening")
	}
	if err := enforcePrivateHandle(f); err != nil {
		return err
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		err := tryLockFile(f)
		if err == nil {
			break
		}
		if !isBusyLock(err) {
			return fmt.Errorf("lock instagram session: %w", err)
		}
		select {
		case <-deadline.C:
			return errors.New("instagram session lock acquisition timed out")
		case <-time.After(20 * time.Millisecond):
		}
	}
	defer unlockFile(f)
	// Detect a replaced lock name before treating this lock as authoritative.
	current, err := os.Lstat(lockPath)
	if err != nil || !os.SameFile(current, opened) {
		return errors.New("instagram session lock path changed")
	}
	return action()
}

func checkSessionRevision(current, base diskSession) error {
	if current.Generation != base.Generation || current.Revision != base.Revision {
		return ErrSessionConflict
	}
	return nil
}

func validGeneration(s string) bool {
	if s == "" {
		return true // legacy v1 sessions get a generation on their next write
	}
	if len(s) != 32 || strings.Trim(s, "0123456789abcdef") != "" {
		return false
	}
	return true
}
