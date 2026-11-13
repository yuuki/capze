//go:build linux || darwin

package fsutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLockExcludes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	unlock, err := Lock(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}

	// A second lock waits, tells so once, and gives up with its context.
	ctx, cancel := context.WithTimeout(t.Context(), 3*lockPollInterval)
	defer cancel()
	waits := 0
	if _, err := Lock(ctx, path, func() { waits++ }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock = %v, want context.DeadlineExceeded", err)
	}
	if waits != 1 {
		t.Errorf("waiting was called %d times, want 1", waits)
	}

	// It gets the lock once the first one is released.
	got := make(chan error, 1)
	go func() {
		unlock2, err := Lock(t.Context(), path, nil)
		if err == nil {
			unlock2()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("Lock returned %v while the lock was held", err)
	case <-time.After(2 * lockPollInterval):
	}
	unlock()
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Lock did not return after the lock was released")
	}
}

func TestLockCreatesNoDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "lock")
	if _, err := Lock(t.Context(), path, nil); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lock = %v, want a not-exist error", err)
	}
}

func TestSyncDirAndTree(t *testing.T) {
	dir := t.TempDir()
	if err := SyncDir(dir); err != nil {
		t.Errorf("SyncDir: %v", err)
	}
	if err := SyncTree(dir); err != nil {
		t.Errorf("SyncTree: %v", err)
	}
	missing := filepath.Join(dir, "missing")
	if err := SyncDir(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SyncDir(missing) = %v, want a not-exist error", err)
	}
	if err := SyncTree(missing); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SyncTree(missing) = %v, want a not-exist error", err)
	}
}

func TestSameDevice(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	mustMkdir(t, sub)
	same, err := SameDevice(dir, sub)
	if err != nil || !same {
		t.Errorf("SameDevice(dir, dir/sub) = %v, %v, want true", same, err)
	}
	if _, err := SameDevice(dir, filepath.Join(dir, "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SameDevice(dir, missing) = %v, want a not-exist error", err)
	}
}

func TestRemoveTree(t *testing.T) {
	root := t.TempDir()
	// What a symbolic link of the tree points to is not part of the tree.
	outside := filepath.Join(root, "outside")
	mustMkdir(t, outside)
	mustWrite(t, filepath.Join(outside, "keep"))

	tree := filepath.Join(root, "tree")
	mustMkdir(t, filepath.Join(tree, "a", "b", "c"))
	mustWrite(t, filepath.Join(tree, "file"))
	mustWrite(t, filepath.Join(tree, "a", "b", "file"))
	if err := os.Symlink(outside, filepath.Join(tree, "a", "link-to-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(tree, "dangling")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(tree, "a", "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	// More entries than one read of the directory returns.
	many := filepath.Join(tree, "many")
	mustMkdir(t, many)
	for i := range 2*removeBatch + 10 {
		mustWrite(t, filepath.Join(many, "f"+string(rune('a'+i%26))+time.Duration(i).String()))
	}

	if err := RemoveTree(t.Context(), tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Errorf("the target of a symbolic link was removed: %v", err)
	}
	// Removing what is not there is not an error.
	if err := RemoveTree(t.Context(), tree); err != nil {
		t.Errorf("RemoveTree of a missing directory: %v", err)
	}
	if err := RemoveTree(t.Context(), filepath.Join(root, "missing", "tree")); err != nil {
		t.Errorf("RemoveTree below a missing directory: %v", err)
	}
}

func TestRemoveTreeStopsWhenCancelled(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "tree")
	mustMkdir(t, filepath.Join(tree, "sub"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := RemoveTree(ctx, tree); !errors.Is(err, context.Canceled) {
		t.Errorf("RemoveTree = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(filepath.Join(tree, "sub")); err != nil {
		t.Errorf("the tree was removed: %v", err)
	}
}

func TestRemoveTreeRefusesWhatIsNotADirectoryOfItsOwn(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	mustWrite(t, file)
	dir := filepath.Join(root, "dir")
	mustMkdir(t, dir)
	mustWrite(t, filepath.Join(dir, "keep"))
	link := filepath.Join(root, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	for _, path := range []string{file, link, ".", "..", "/", "sub/../.."} {
		if err := RemoveTree(t.Context(), path); err == nil {
			t.Errorf("RemoveTree(%q) succeeded", path)
		}
	}
	for _, path := range []string{file, filepath.Join(dir, "keep")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", path, err)
		}
	}
}

// The owner of a directory can remove it even if it lacks the write
// permission, but the directory that holds the tree is never changed.
func TestRemoveTreeReadOnlyDirectories(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root needs no permission")
	}
	root := t.TempDir()
	tree := filepath.Join(root, "tree")
	sub := filepath.Join(tree, "sub")
	mustMkdir(t, sub)
	mustWrite(t, filepath.Join(sub, "file"))
	for _, dir := range []string{sub, tree} {
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveTree(t.Context(), tree); err != nil {
		t.Fatal(err)
	}

	locked := filepath.Join(root, "locked")
	tree = filepath.Join(locked, "tree")
	mustMkdir(t, tree)
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	if err := RemoveTree(t.Context(), tree); !errors.Is(err, os.ErrPermission) {
		t.Errorf("RemoveTree in a read-only directory = %v, want a permission error", err)
	}
	if info, err := os.Stat(locked); err != nil || info.Mode().Perm() != 0o555 {
		t.Errorf("the directory that holds the tree was changed: %v, %v", info.Mode(), err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(filepath.Base(path)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
