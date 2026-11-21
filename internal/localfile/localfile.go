// Package localfile handles private bridge files in directories whose path and
// entries are controlled by the caller. Opens refuse links at the final component.
package localfile

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked reports contention on a nonblocking Lock.
var ErrLocked = errors.New("file is locked")

var (
	errNotRegular = errors.New("expected a regular file")
	errNotPrivate = errors.New("expected a private file or directory")
	errLinked     = errors.New("expected a file with one link")
)

// Lock opens or creates a private lock file and takes an exclusive process lock.
// Close releases the lock. Keep the lock path in place across acquisitions.
// When wait is false, contention returns an error matching ErrLocked.
func Lock(path string, wait bool) (*os.File, error) {
	return lock(path, wait, true)
}

// LockExisting takes the same exclusive lock without creating a file.
func LockExisting(path string, wait bool) (*os.File, error) {
	return lock(path, wait, false)
}

// OpenPrivate opens an existing private regular file for reading.
func OpenPrivate(path string) (*os.File, error) {
	return openRegular(path, true, false)
}

// OpenPrivateSingleLink opens an existing private regular file with no other
// hard links for reading.
func OpenPrivateSingleLink(path string) (*os.File, error) {
	return openRegular(path, true, true)
}

// OpenPrivateAppend opens or creates a private regular file with no other
// hard links for appending.
func OpenPrivateAppend(path string) (*os.File, error) {
	return openPrivateAppend(path)
}

// OpenRegular opens an existing regular file for reading. Use it for files
// whose privacy comes from a private containing directory, such as service logs.
func OpenRegular(path string) (*os.File, error) {
	return openRegular(path, false, false)
}

// CreatePrivate exclusively creates a private regular file for writing. If the
// path already exists, it returns an error matching os.ErrExist.
func CreatePrivate(path string) (*os.File, error) {
	return createPrivate(path)
}

// MkdirAllPrivate creates missing directories with private permissions and
// verifies the final directory. It leaves existing permissions unchanged.
func MkdirAllPrivate(path string) error {
	return mkdirAllPrivate(path)
}

// CheckPrivateDir verifies an existing directory without creating it.
func CheckPrivateDir(path string) error {
	return checkPrivateDir(path)
}

// WritePrivateAtomic syncs a private temporary file and publishes it in the
// destination directory. With createOnly it refuses to replace any existing
// entry. published reports whether publication succeeded, even if a subsequent
// directory sync fails. Keep any writer lock on a separate, stable file.
func WritePrivateAtomic(path string, data []byte, createOnly bool) (published bool, err error) {
	temp, err := createTempPrivate(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, err := temp.Write(data); err != nil {
		return false, err
	}
	if err := temp.Sync(); err != nil {
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}
	return publish(temp.Name(), path, createOnly)
}
