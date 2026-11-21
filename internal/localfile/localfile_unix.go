//go:build darwin || linux

package localfile

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func lock(path string, wait, create bool) (*os.File, error) {
	flags := os.O_RDWR
	if create {
		flags |= os.O_CREATE
	}
	f, err := openChecked(path, flags, true, false)
	if err != nil {
		return nil, err
	}
	mode := syscall.LOCK_EX
	if !wait {
		mode |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), mode)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			err = errors.Join(ErrLocked, err)
		}
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	return f, nil
}

func openRegular(path string, private, singleLink bool) (*os.File, error) {
	return openChecked(path, os.O_RDONLY, private, singleLink)
}

func openPrivateAppend(path string) (*os.File, error) {
	return openChecked(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, true, true)
}

func openChecked(path string, flags int, private, singleLink bool) (*os.File, error) {
	// Nonblocking open lets us reject a FIFO without waiting for a peer.
	f, err := os.OpenFile(path, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		switch {
		case !info.Mode().IsRegular():
			err = errNotRegular
		case private && info.Mode().Perm()&0077 != 0:
			err = errNotPrivate
		case singleLink && info.Sys().(*syscall.Stat_t).Nlink != 1:
			err = errLinked
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, &os.PathError{Op: "check file", Path: path, Err: err}
	}
	return f, nil
}

func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
}

func mkdirAllPrivate(path string) error {
	if path != "" {
		path = filepath.Clean(path)
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return checkPrivateDir(path)
}

func checkPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return &os.PathError{Op: "check directory", Path: path, Err: errNotPrivate}
	}
	return nil
}

func createTempPrivate(dir string) (*os.File, error) {
	return os.CreateTemp(dir, ".t3-bridge-*.tmp")
}

func publish(temp, path string, createOnly bool) (bool, error) {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer dir.Close()
	if createOnly {
		err = os.Link(temp, path)
	} else {
		err = os.Rename(temp, path)
	}
	if err != nil {
		return false, err
	}
	return true, dir.Sync()
}
