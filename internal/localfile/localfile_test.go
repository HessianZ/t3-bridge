package localfile

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private", "nested")
	if err := CheckPrivateDir(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CheckPrivateDir on missing directory: %v", err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only check created a directory: %v", err)
	}
	if err := MkdirAllPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAllPrivate(dir); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "file")
	f, err := CreatePrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("initial"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if f, err := CreatePrivate(path); !errors.Is(err, os.ErrExist) {
		if f != nil {
			f.Close()
		}
		t.Fatalf("CreatePrivate on existing file: %v", err)
	}
	if published, err := WritePrivateAtomic(path, []byte("unexpected"), true); published || !errors.Is(err, os.ErrExist) {
		t.Fatalf("create-only on existing file: published=%v, err=%v", published, err)
	}
	requirePrivateContents(t, path, "initial")
	if published, err := WritePrivateAtomic(path, []byte("replacement"), false); !published || err != nil {
		t.Fatalf("replace: published=%v, err=%v", published, err)
	}
	requirePrivateContents(t, path, "replacement")
	newPath := filepath.Join(dir, "new")
	if published, err := WritePrivateAtomic(newPath, []byte("new"), true); !published || err != nil {
		t.Fatalf("create-only: published=%v, err=%v", published, err)
	}
	requirePrivateContents(t, newPath, "new")
}

func TestRejectLeafSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if published, err := WritePrivateAtomic(target, []byte("target"), true); !published || err != nil {
		t.Fatalf("create target: published=%v, err=%v", published, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skipf("cannot create symlink: %v", err)
		}
		t.Fatal(err)
	}
	if f, err := OpenPrivate(link); err == nil {
		f.Close()
		t.Fatal("OpenPrivate accepted a symlink")
	}
	if f, err := Lock(link, false); err == nil {
		f.Close()
		t.Fatal("Lock accepted a symlink")
	}
	requirePrivateContents(t, target, "target")
}

func TestLock(t *testing.T) {
	if path := os.Getenv("T3_BRIDGE_TEST_LOCALFILE_LOCK"); path != "" {
		if f, err := Lock(path, false); !errors.Is(err, ErrLocked) {
			if f != nil {
				f.Close()
			}
			t.Fatalf("lock held by another process: %v", err)
		}
		return
	}
	path := filepath.Join(t.TempDir(), "lock")
	lock, err := Lock(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLock$")
	child.Env = append(os.Environ(), "T3_BRIDGE_TEST_LOCALFILE_LOCK="+path)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("cross-process contention: %v\n%s", err, output)
	}
	type lockResult struct {
		file *os.File
		err  error
	}
	started, done := make(chan struct{}), make(chan lockResult, 1)
	go func() {
		close(started)
		f, err := Lock(path, true)
		done <- lockResult{f, err}
	}()
	<-started
	select {
	case result := <-done:
		if result.file != nil {
			result.file.Close()
		}
		t.Fatalf("waiting lock returned before release: %v", result.err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		result.file.Close()
	case <-ctx.Done():
		t.Fatal("waiting lock did not acquire after release")
	}
}

func requirePrivateContents(t *testing.T, path, want string) {
	t.Helper()
	f, err := OpenPrivate(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	contents, err := io.ReadAll(f)
	if err != nil || string(contents) != want {
		t.Fatalf("read private file: got %q, want %q, err=%v", contents, want, err)
	}
}
