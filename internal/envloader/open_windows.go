//go:build windows

package envloader

import (
	"errors"
	"os"
	"syscall"
)

func openDotenv(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// A reparse point must be inspected without following it. Checking the
	// handle, rather than Lstat(path) followed by Open(path), avoids TOCTOU.
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	var info syscall.ByHandleFileInformation
	if err = syscall.GetFileInformationByHandle(h, &info); err != nil || info.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = syscall.CloseHandle(h)
		if err != nil {
			return nil, err
		}
		return nil, errors.New("dotenv path is a reparse point")
	}
	return os.NewFile(uintptr(h), path), nil
}
