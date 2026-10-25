//go:build unix

package envloader

import (
	"os"
	"syscall"
)

// O_NOFOLLOW protects the *actual opened descriptor* from a final-component
// symlink race.
func openDotenv(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
