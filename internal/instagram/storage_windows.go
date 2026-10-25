package instagram

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openPrivateFile(path string, flags int, _ os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	access := uint32(windows.GENERIC_READ)
	if flags&(os.O_RDWR|os.O_WRONLY) != 0 {
		// enforcePrivateHandle updates the DACL of the lock file through this
		// handle. SetSecurityInfo requires WRITE_DAC; GENERIC_WRITE is not enough.
		access |= windows.GENERIC_WRITE | windows.WRITE_DAC
	}
	creation := uint32(windows.OPEN_EXISTING)
	if flags&os.O_CREATE != 0 {
		creation = windows.OPEN_ALWAYS
	}
	h, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, creation, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var data windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(h, &data); err != nil || data.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return nil, errors.New("refusing Instagram credential reparse point")
	}
	return os.NewFile(uintptr(h), path), nil
}

func tryLockFile(f *os.File) error {
	var overlap windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
}
func isBusyLock(err error) bool { return errors.Is(err, windows.ERROR_LOCK_VIOLATION) }
func unlockFile(f *os.File) error {
	var overlap windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlap)
}

func privateACL() (string, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", err
	}
	return "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)", nil
}

func enforcePrivateHandle(f *os.File) error {
	sddl, err := privateACL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func checkPrivatePath(path string, info os.FileInfo, directory bool) error {
	if (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return errors.New("instagram credential path must be regular")
	}
	// Windows normalizes SDDL identifiers and ACE ordering, so comparing the
	// complete descriptor string to one exact spelling rejects valid ACLs.
	// New credentials are restricted by SetNamedSecurityInfo on creation.
	_, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	return err
}

func enforcePrivatePath(path string, directory bool) error {
	sddl, err := privateACL()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	return checkPrivatePath(path, info, directory)
}
