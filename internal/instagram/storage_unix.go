//go:build !windows

package instagram

import (
	"errors"
	"os"
	"syscall"
)

func openPrivateFile(path string, flags int, mode os.FileMode) (*os.File, error) {
	fd, err := syscall.Open(path, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func tryLockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func isBusyLock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
func unlockFile(f *os.File) error           { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
func enforcePrivateHandle(f *os.File) error { return f.Chmod(0600) }

func checkPrivatePath(_ string, info os.FileInfo, directory bool) error {
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return errors.New("instagram credential path is not a regular file or directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("instagram credential path must be private (0600 file, 0700 directory)")
	}
	return nil
}

func enforcePrivatePath(path string, directory bool) error {
	if err := os.Chmod(path, map[bool]os.FileMode{true: 0700, false: 0600}[directory]); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return checkPrivatePath(path, info, directory)
}
