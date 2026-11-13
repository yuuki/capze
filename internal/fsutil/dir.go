//go:build linux || darwin

package fsutil

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"golang.org/x/sys/unix"
)

// A FileID identifies a file, whatever the paths that lead to it.
type FileID struct {
	Dev, Ino uint64
}

// A Dir is an open directory. What it does to its entries, it does by their
// names in the directory that is open, never by a path: nothing that happens
// to the paths meanwhile (a directory replaced by a symbolic link, a file
// system mounted on it) can lead it elsewhere.
type Dir struct {
	fd    int
	path  string
	id    FileID
	mount uint64
}

// OpenDir opens the directory path, through symbolic links.
func OpenDir(path string) (*Dir, error) {
	fd, err := retry(func() (int, error) {
		return unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return newDir(fd, path)
}

func newDir(fd int, path string) (*Dir, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		_ = unix.Close(fd)
		return nil, &os.PathError{Op: "stat", Path: path, Err: err}
	}
	mount, err := mountOf(fd)
	if err != nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("cannot tell the mount point of %s: %w", path, err)
	}
	return &Dir{fd: fd, path: path, id: statID(&st), mount: mount}, nil
}

// Close closes the directory, and releases its lock.
func (d *Dir) Close() {
	if d != nil {
		_ = unix.Close(d.fd)
	}
}

// Path returns the path the directory was opened with.
func (d *Dir) Path() string { return d.path }

// ID identifies the directory.
func (d *Dir) ID() FileID { return d.id }

// SameMount reports whether d and other are on the same mount of the same
// file system: rename(2) can move a file from one to the other.
func (d *Dir) SameMount(other *Dir) bool {
	return d.id.Dev == other.id.Dev && d.mount == other.mount
}

// A NotPlainDirError reports an entry that should be a directory of its own
// and is something else, such as a symbolic link.
type NotPlainDirError struct {
	Path string
	// What the entry is.
	What string
}

func (e *NotPlainDirError) Error() string {
	return e.Path + " is " + e.What + ", not a directory"
}

// Sub opens the directory name of d. The entry must be a directory of its
// own, on the mount of d: Sub returns a *NotPlainDirError for a symbolic
// link (which it does not follow) or another file, a *MountPointError for a
// mount point, and an error that is fs.ErrNotExist if there is no entry.
func (d *Dir) Sub(name string) (*Dir, error) {
	path := filepath.Join(d.path, name)
	fd, err := retry(func() (int, error) {
		return unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		// The error of open(2) for a symbolic link differs between systems.
		if st, serr := d.Lstat(name); serr == nil && !st.IsDir {
			what := "a file"
			if st.IsSymlink {
				what = "a symbolic link"
			}
			return nil, &NotPlainDirError{Path: path, What: what}
		}
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	sub, err := newDir(fd, path)
	if err != nil {
		return nil, err
	}
	if !d.SameMount(sub) {
		sub.Close()
		return nil, &MountPointError{Path: path}
	}
	return sub, nil
}

// MkSub opens the directory name of d like Sub, after creating it with the
// permissions perm if there is no entry.
func (d *Dir) MkSub(name string, perm uint32) (*Dir, error) {
	if err := unix.Mkdirat(d.fd, name, perm); err != nil && !errors.Is(err, unix.EEXIST) {
		return nil, &os.PathError{Op: "mkdir", Path: filepath.Join(d.path, name), Err: err}
	}
	return d.Sub(name)
}

// Follow opens the directory that the entry name of d leads to, through
// symbolic links and mount points.
func (d *Dir) Follow(name string) (*Dir, error) {
	path := filepath.Join(d.path, name)
	fd, err := retry(func() (int, error) {
		return unix.Openat(d.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return newDir(fd, path)
}

// maxAncestors bounds the walk of Ancestors.
const maxAncestors = 4096

// Ancestors identifies d and every directory above it, up to the root: the
// directories that really hold it, whatever path led to it.
func (d *Dir) Ancestors() ([]FileID, error) {
	ids := []FileID{d.id}
	fd, owned := d.fd, false
	defer func() {
		if owned {
			_ = unix.Close(fd)
		}
	}()
	for range maxAncestors {
		parent, err := retry(func() (int, error) {
			return unix.Openat(fd, "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		})
		if err != nil {
			return nil, &os.PathError{Op: "open", Path: filepath.Join(d.path, ".."), Err: err}
		}
		if owned {
			_ = unix.Close(fd)
		}
		fd, owned = parent, true
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return nil, &os.PathError{Op: "stat", Path: d.path, Err: err}
		}
		id := statID(&st)
		// The root is its own parent.
		if id == ids[len(ids)-1] {
			return ids, nil
		}
		ids = append(ids, id)
	}
	return nil, fmt.Errorf("%s is too deep", d.path)
}

// A Stat is what Lstat tells of an entry.
type Stat struct {
	ID        FileID
	IsDir     bool
	IsSymlink bool
}

// Lstat describes the entry name of d, without following a symbolic link.
func (d *Dir) Lstat(name string) (Stat, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(d.fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return Stat{}, &os.PathError{Op: "lstat", Path: filepath.Join(d.path, name), Err: err}
	}
	return Stat{ID: statID(&st), IsDir: statIsDir(&st), IsSymlink: statIsSymlink(&st)}, nil
}

// Names returns the names of the entries of d, sorted.
func (d *Dir) Names() ([]string, error) {
	// Another file descriptor: reading moves its position.
	fd, err := retry(func() (int, error) {
		return unix.Openat(d.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: d.path, Err: err}
	}
	f := os.NewFile(uintptr(fd), d.path)
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(names, cmp.Compare[string])
	return names, nil
}

// Rename moves the entry name of d to the entry toName of the directory to,
// with rename(2).
func (d *Dir) Rename(name string, to *Dir, toName string) error {
	if err := unix.Renameat(d.fd, name, to.fd, toName); err != nil {
		return &os.LinkError{Op: "rename", Old: filepath.Join(d.path, name), New: filepath.Join(to.path, toName), Err: err}
	}
	return nil
}

// Symlink creates the symbolic link name in d, to target.
func (d *Dir) Symlink(target, name string) error {
	if err := unix.Symlinkat(target, d.fd, name); err != nil {
		return &os.PathError{Op: "symlink", Path: filepath.Join(d.path, name), Err: err}
	}
	return nil
}

// Unlink removes the entry name of d, which is not a directory. It is not an
// error if there is no entry.
func (d *Dir) Unlink(name string) error {
	if err := unix.Unlinkat(d.fd, name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return &os.PathError{Op: "remove", Path: filepath.Join(d.path, name), Err: err}
	}
	return nil
}

// Sync flushes the entries of d to disk, so that a file created in it, or
// renamed into it, survives a crash.
func (d *Dir) Sync() error {
	err := unix.Fsync(d.fd)
	// Some file systems (network and FUSE ones) do not implement fsync(2) on
	// a directory: they have nothing to flush.
	if err == nil || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return &os.PathError{Op: "fsync", Path: d.path, Err: err}
}

// lockPollInterval is how often Lock tries again while another process holds
// the lock. flock(2) cannot wait and watch a context at the same time.
const lockPollInterval = 100 * time.Millisecond

// Lock takes the exclusive advisory lock (flock(2)) of the directory, and
// returns the function that releases it. While another process holds the
// lock, Lock waits until ctx is done; it calls waiting, if not nil, once
// before it starts to wait.
//
// The lock is on the directory itself, not on a file in it that could be
// deleted or replaced. The kernel releases it when the process exits,
// however it exits: a lock is never left behind.
func (d *Dir) Lock(ctx context.Context, waiting func()) (unlock func(), err error) {
	for first := true; ; first = false {
		err := unix.Flock(d.fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = unix.Flock(d.fd, unix.LOCK_UN) }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, &os.PathError{Op: "lock", Path: d.path, Err: err}
		}
		if first && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPollInterval):
		}
	}
}

// RemoveTree removes the directory name of d, and everything below it. See
// the function RemoveTree.
func (d *Dir) RemoveTree(ctx context.Context, name string) error {
	r := remover{ctx: ctx, mount: d.mount}
	_, err := r.removeDir(d.fd, &entry{name: filepath.Join(d.path, name)}, name, false, 0)
	return err
}
