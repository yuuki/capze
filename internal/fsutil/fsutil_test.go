//go:build linux || darwin

package fsutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openDir opens a directory for the time of the test.
func openDir(t *testing.T, path string) *Dir {
	t.Helper()
	d, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func TestLockExcludes(t *testing.T) {
	path := t.TempDir()
	// Two openings of the directory, as two processes have.
	first, second := openDir(t, path), openDir(t, path)
	unlock, err := first.Lock(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	// A second lock waits, tells so once, and gives up with its context.
	ctx, cancel := context.WithTimeout(t.Context(), 3*lockPollInterval)
	defer cancel()
	waits := 0
	if _, err := second.Lock(ctx, func() { waits++ }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Lock = %v, want context.DeadlineExceeded", err)
	}
	if waits != 1 {
		t.Errorf("waiting was called %d times, want 1", waits)
	}

	// It gets the lock once the first one is released.
	got := make(chan error, 1)
	go func() {
		unlock2, err := second.Lock(t.Context(), nil)
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

	// Closing the directory releases its lock too, as the end of a process
	// does.
	third, err := OpenDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := third.Lock(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	third.Close()
	if unlock, err := first.Lock(t.Context(), nil); err != nil {
		t.Errorf("Lock after the holder closed the directory: %v", err)
	} else {
		unlock()
	}
}

func TestDirSync(t *testing.T) {
	d := openDir(t, t.TempDir())
	if err := d.Sync(); err != nil {
		t.Errorf("Sync: %v", err)
	}
	if err := d.SyncFS(); err != nil {
		t.Errorf("SyncFS: %v", err)
	}
}

func TestOpenDir(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "dir"))
	mustWrite(t, filepath.Join(root, "file"))
	if err := os.Symlink("dir", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	// The directory itself may be named through a symbolic link.
	d := openDir(t, filepath.Join(root, "link"))
	if d.ID() != openDir(t, filepath.Join(root, "dir")).ID() {
		t.Error("the directory opened through a link is not the directory")
	}
	for _, path := range []string{filepath.Join(root, "file"), filepath.Join(root, "missing")} {
		if d, err := OpenDir(path); err == nil {
			d.Close()
			t.Errorf("OpenDir(%s) succeeded", path)
		}
	}
}

// Sub opens only a directory of its own: what stands in for one is refused,
// not followed.
func TestDirSub(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustMkdir(t, filepath.Join(root, "dir"))
	mustWrite(t, filepath.Join(root, "file"))
	for name, target := range map[string]string{"link": "dir", "link-out": outside, "dangling": "missing"} {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	d := openDir(t, root)

	sub, err := d.Sub("dir")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if !d.SameMount(sub) || sub.Path() != filepath.Join(root, "dir") {
		t.Errorf("Sub(dir): path %s, same mount %v", sub.Path(), d.SameMount(sub))
	}
	if st, err := d.Lstat("dir"); err != nil || st.ID != sub.ID() || !st.IsDir || st.IsSymlink {
		t.Errorf("Lstat(dir) = %+v, %v", st, err)
	}

	for _, name := range []string{"link", "link-out", "dangling", "file"} {
		sub, err := d.Sub(name)
		var notDir *NotPlainDirError
		if !errors.As(err, &notDir) {
			sub.Close()
			t.Errorf("Sub(%s) = %v, want a NotPlainDirError", name, err)
		}
		// MkSub does not replace what is there, nor follow it.
		if sub, err := d.MkSub(name, 0o755); !errors.As(err, &notDir) {
			sub.Close()
			t.Errorf("MkSub(%s) = %v, want a NotPlainDirError", name, err)
		}
	}
	if st, err := d.Lstat("link"); err != nil || st.IsDir || !st.IsSymlink {
		t.Errorf("Lstat(link) = %+v, %v", st, err)
	}
	if _, err := d.Sub("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Sub(missing) = %v, want a not-exist error", err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Errorf("something was created through a link: %v", entries)
	}

	made, err := d.MkSub("made", 0o700)
	if err != nil {
		t.Fatal(err)
	}
	made.Close()
	if info, err := os.Lstat(filepath.Join(root, "made")); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("MkSub: %v, %v, want a directory with mode 0700", info, err)
	}
	again, err := d.MkSub("made", 0o755)
	if err != nil {
		t.Errorf("MkSub of an existing directory: %v", err)
	}
	again.Close()

	want := []string{"dangling", "dir", "file", "link", "link-out", "made"}
	if names, err := d.Names(); err != nil || !slices.Equal(names, want) {
		t.Errorf("Names = %v, %v, want %v", names, err, want)
	}
}

func TestDirRenameSymlinkUnlink(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "a", "x"))
	mustMkdir(t, filepath.Join(root, "b"))
	a, b := openDir(t, filepath.Join(root, "a")), openDir(t, filepath.Join(root, "b"))

	if err := a.Rename("x", b, "y"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "b", "y")); err != nil {
		t.Errorf("the directory was not moved: %v", err)
	}
	if err := a.Rename("x", b, "z"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Rename of a missing entry = %v", err)
	}

	if err := a.Symlink("../b/y", "link"); err != nil {
		t.Fatal(err)
	}
	if target, err := os.Readlink(filepath.Join(root, "a", "link")); err != nil || target != "../b/y" {
		t.Errorf("link -> %q, %v", target, err)
	}
	if err := a.Symlink("elsewhere", "link"); !errors.Is(err, os.ErrExist) {
		t.Errorf("Symlink over an entry = %v, want an exist error", err)
	}
	// Unlink removes the link, not what it leads to, and no directory.
	if err := a.Unlink("link"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "b", "y")); err != nil {
		t.Errorf("the target of the link was removed: %v", err)
	}
	if err := a.Unlink("link"); err != nil {
		t.Errorf("Unlink of a missing entry: %v", err)
	}
	if err := b.Unlink("y"); err == nil {
		t.Error("Unlink of a directory succeeded")
	}
}

// Ancestors are the directories that hold a directory, not those of the
// path that named it.
func TestDirAncestors(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b", "c")
	mustMkdir(t, deep)
	mustMkdir(t, filepath.Join(root, "elsewhere"))
	if err := os.Symlink(deep, filepath.Join(root, "elsewhere", "link")); err != nil {
		t.Fatal(err)
	}
	d := openDir(t, filepath.Join(root, "elsewhere", "link"))
	ancestors, err := d.Ancestors()
	if err != nil {
		t.Fatal(err)
	}
	if ancestors[0] != d.ID() {
		t.Error("the first ancestor is not the directory itself")
	}
	for _, path := range []string{deep, filepath.Join(root, "a", "b"), filepath.Join(root, "a"), root, "/"} {
		if !slices.Contains(ancestors, openDir(t, path).ID()) {
			t.Errorf("%s is not among the ancestors", path)
		}
	}
	if slices.Contains(ancestors, openDir(t, filepath.Join(root, "elsewhere")).ID()) {
		t.Error("the directory of the link is among the ancestors")
	}
	if len(slices.Compact(slices.Clone(ancestors))) != len(ancestors) {
		t.Errorf("the ancestors repeat: %v", ancestors)
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

// The owner of a directory can remove it even if it may not read it.
func TestRemoveTreeUnreadableDirectories(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root needs no permission")
	}
	tree := filepath.Join(t.TempDir(), "tree")
	empty := filepath.Join(tree, "empty")
	full := filepath.Join(tree, "full")
	mustMkdir(t, empty)
	mustMkdir(t, filepath.Join(full, "sub"))
	mustWrite(t, filepath.Join(full, "sub", "file"))
	for _, dir := range []string{filepath.Join(full, "sub"), full, empty} {
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveTree(t.Context(), tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree is still there: %v", err)
	}
}

// A tree deeper than the longest path the kernel accepts: no path of the
// tree is ever given to the kernel, nor kept for each level.
func TestRemoveTreeDeep(t *testing.T) {
	const depth = 3000
	tree := filepath.Join(t.TempDir(), "tree")
	mustMkdir(t, tree)
	fd, err := unix.Open(tree, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	for range depth {
		if err := unix.Mkdirat(fd, "d", 0o755); err != nil {
			t.Fatal(err)
		}
		next, err := unix.Openat(fd, "d", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if err != nil {
			t.Fatal(err)
		}
		fd = next
	}
	_ = unix.Close(fd)

	if err := RemoveTree(t.Context(), tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree is still there: %v", err)
	}
}

// Something that keeps creating files in a directory does not keep its
// removal running: the removal ends, with an error if it lost the race.
func TestRemoveTreeEndsWhileFilesAreCreated(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "tree")
	busy := filepath.Join(tree, "busy")
	mustMkdir(t, busy)
	stop := make(chan struct{})
	var writers sync.WaitGroup
	for w := range 4 {
		writers.Go(func() {
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				// Fails once the directory is gone.
				_ = os.WriteFile(filepath.Join(busy, fmt.Sprintf("%d-%d", w, i)), nil, 0o644)
			}
		})
	}
	done := make(chan error, 1)
	go func() { done <- RemoveTree(t.Context(), tree) }()
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, unix.ENOTEMPTY) {
			t.Errorf("RemoveTree = %v, want nil or a not-empty error", err)
		}
	case <-time.After(30 * time.Second):
		t.Error("RemoveTree did not end")
	}
	close(stop)
	writers.Wait()
}

// The tree has a directory with the name that RemoveTree gives to those it
// moves to the root of a deep tree.
func TestRemoveTreeDeepWithTakenNames(t *testing.T) {
	tree := filepath.Join(t.TempDir(), "tree")
	mustMkdir(t, filepath.Join(tree, ".capze-hoisted-0", "full"))
	mustWrite(t, filepath.Join(tree, ".capze-hoisted-1"))
	deep := tree
	for range maxOpenDepth + 3 {
		deep = filepath.Join(deep, "d")
	}
	mustMkdir(t, deep)
	mustWrite(t, filepath.Join(deep, "file"))
	if err := RemoveTree(t.Context(), tree); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree is still there: %v", err)
	}
}

// With few open files allowed, a deep tree is still removed: the limit of
// the process, not only the one RemoveTree sets itself, moves directories up.
func TestRemoveTreeDeepWithFewOpenFiles(t *testing.T) {
	var limit unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_NOFILE, &limit); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(t.TempDir(), "tree")
	deep := tree
	for range maxOpenDepth - 8 {
		deep = filepath.Join(deep, "d")
	}
	mustMkdir(t, deep)
	mustWrite(t, filepath.Join(deep, "file"))

	low := limit
	low.Cur = 48
	if err := unix.Setrlimit(unix.RLIMIT_NOFILE, &low); err != nil {
		t.Skipf("cannot lower the limit of open files: %v", err)
	}
	err := RemoveTree(t.Context(), tree)
	if rerr := unix.Setrlimit(unix.RLIMIT_NOFILE, &limit); rerr != nil {
		t.Fatal(rerr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(tree); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree is still there: %v", err)
	}
}

func TestEntryPath(t *testing.T) {
	root := &entry{name: "/trash/release"}
	file := &entry{parent: &entry{parent: root, name: "etc"}, name: "passwd"}
	if got := root.path(); got != "/trash/release" {
		t.Errorf("root.path() = %q", got)
	}
	if got := file.path(); got != "/trash/release/etc/passwd" {
		t.Errorf("file.path() = %q", got)
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
