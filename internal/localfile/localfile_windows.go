package localfile

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	shareReadWrite = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE
	shareAll       = shareReadWrite | windows.FILE_SHARE_DELETE
)

func lock(path string, wait, create bool) (*os.File, error) {
	user, attrs, err := privateSecurity(false)
	if err != nil {
		return nil, err
	}
	disposition := uint32(windows.OPEN_EXISTING)
	if create {
		disposition = windows.OPEN_ALWAYS
	}
	// Omitting FILE_SHARE_DELETE keeps the lock name stable while held.
	h, err := openHandle(path, windows.GENERIC_READ|windows.GENERIC_WRITE, shareReadWrite, disposition, 0, attrs)
	if err != nil {
		return nil, err
	}
	f, err := checkedFile(path, h, user, false)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.LOCKFILE_EXCLUSIVE_LOCK)
	if !wait {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	if err := windows.LockFileEx(h, flags, 0, 1, 0, &windows.Overlapped{}); err != nil {
		_ = f.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			err = errors.Join(ErrLocked, err)
		}
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	return f, nil
}

func openRegular(path string, private, singleLink bool) (*os.File, error) {
	var user *windows.SID
	if private {
		tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return nil, err
		}
		user = tokenUser.User.Sid
	}
	h, err := openHandle(path, windows.GENERIC_READ, shareAll, windows.OPEN_EXISTING, 0, nil)
	if err != nil {
		return nil, err
	}
	return checkedFile(path, h, user, singleLink)
}

func openPrivateAppend(path string) (*os.File, error) {
	user, attrs, err := privateSecurity(false)
	if err != nil {
		return nil, err
	}
	// checkPrivate reads the owner and DACL through this handle.
	access := uint32(windows.FILE_APPEND_DATA | windows.FILE_READ_ATTRIBUTES | windows.READ_CONTROL)
	h, err := openHandle(path, access, shareAll, windows.OPEN_ALWAYS, 0, attrs)
	if err != nil {
		return nil, err
	}
	return checkedFile(path, h, user, true)
}

func createPrivate(path string) (*os.File, error) {
	user, attrs, err := privateSecurity(false)
	if err != nil {
		return nil, err
	}
	h, err := openHandle(path, windows.GENERIC_WRITE|windows.FILE_READ_ATTRIBUTES, shareAll, windows.CREATE_NEW, 0, attrs)
	if err != nil {
		return nil, err
	}
	return checkedFile(path, h, user, false)
}

func openHandle(path string, access, share, disposition, flags uint32, attrs *windows.SecurityAttributes) (windows.Handle, error) {
	name, err := windowsPath(path)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(name, access, share, attrs, disposition, flags|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return h, nil
}

func checkedFile(path string, h windows.Handle, user *windows.SID, singleLink bool) (*os.File, error) {
	if err := checkHandle(h, user, false, singleLink); err != nil {
		_ = windows.CloseHandle(h)
		return nil, &os.PathError{Op: "check file", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func checkHandle(h windows.Handle, user *windows.SID, directory, singleLink bool) error {
	typ, err := windows.GetFileType(h)
	if err != nil {
		return err
	}
	if typ != windows.FILE_TYPE_DISK {
		return errNotRegular
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 || (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory {
		if directory {
			return errNotPrivate
		}
		return errNotRegular
	}
	if singleLink && info.NumberOfLinks != 1 {
		return errLinked
	}
	if user == nil {
		return nil
	}
	return checkPrivate(h, user)
}

func checkPrivate(h windows.Handle, user *windows.SID) error {
	var fsFlags uint32
	if err := windows.GetVolumeInformationByHandle(h, nil, 0, nil, nil, &fsFlags, nil, 0); err != nil {
		return err
	}
	if fsFlags&windows.FILE_PERSISTENT_ACLS == 0 {
		return errNotPrivate
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner == nil || !owner.Equals(user) {
		return errNotPrivate
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if dacl == nil {
		return errNotPrivate
	}
	// Accept only simple ACLs. An unsupported ACE may grant additional access.
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			// Keep the file user-owned while accepting the OS and local administrator entries
			// commonly inherited by credentials in a Windows user profile.
			if !sid.Equals(user) && !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
				return errNotPrivate
			}
		default:
			return errNotPrivate
		}
	}
	return nil
}

func privateSecurity(directory bool) (*windows.SID, *windows.SecurityAttributes, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	user := tokenUser.User.Sid
	sid := user.String()
	inherit := ""
	if directory {
		inherit = "OICI"
	}
	// Set the owner explicitly, since an elevated token can default to a group.
	descriptor, err := windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;" + inherit + ";FA;;;" + sid + ")")
	if err != nil {
		return nil, nil, err
	}
	attrs := &windows.SecurityAttributes{SecurityDescriptor: descriptor}
	attrs.Length = uint32(unsafe.Sizeof(*attrs))
	return user, attrs, nil
}

func mkdirAllPrivate(path string) error {
	_, attrs, err := privateSecurity(true)
	if err != nil {
		return err
	}
	if err := mkdirAll(path, attrs); err != nil {
		return err
	}
	return checkPrivateDir(path)
}

func checkPrivateDir(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	h, err := openHandle(path, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, shareAll, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := checkHandle(h, user.User.Sid, true, false); err != nil {
		return &os.PathError{Op: "check directory", Path: path, Err: err}
	}
	return nil
}

func mkdirAll(path string, attrs *windows.SecurityAttributes) error {
	name, err := windowsPath(path)
	if err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	err = windows.CreateDirectory(name, attrs)
	if parent := filepath.Dir(path); errors.Is(err, os.ErrNotExist) && parent != path {
		if err := mkdirAll(parent, attrs); err != nil {
			return err
		}
		err = windows.CreateDirectory(name, attrs)
	}
	if err != nil && !errors.Is(err, os.ErrExist) {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

func createTempPrivate(dir string) (*os.File, error) {
	for {
		f, err := createPrivate(filepath.Join(dir, ".t3-bridge-"+rand.Text()+".tmp"))
		if !errors.Is(err, os.ErrExist) {
			return f, err
		}
	}
}

func publish(temp, path string, createOnly bool) (bool, error) {
	from, err := windowsPath(temp)
	if err != nil {
		return false, err
	}
	to, err := windowsPath(path)
	if err != nil {
		return false, err
	}
	// Keep the staging file's ACL by moving within the volume without a copy.
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if !createOnly {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	if err := windows.MoveFileEx(from, to, flags); err != nil {
		return false, &os.LinkError{Op: "move", Old: temp, New: path, Err: err}
	}
	return true, nil
}

func windowsPath(path string) (*uint16, error) {
	if path == "" {
		return nil, os.ErrInvalid
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Accept ordinary drive and UNC names, excluding devices and data streams.
	if strings.HasPrefix(path, `\\.\`) || strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\??\`) || strings.Contains(path[len(filepath.VolumeName(path)):], ":") {
		return nil, os.ErrInvalid
	}
	if len(path) >= 248 {
		if strings.HasPrefix(path, `\\`) {
			path = `\\?\UNC\` + path[2:]
		} else {
			path = `\\?\` + path
		}
	}
	return windows.UTF16PtrFromString(path)
}
