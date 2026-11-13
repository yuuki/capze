//go:build linux || darwin

package fsutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// A MountPointError reports a mount point where a removal stopped.
type MountPointError struct {
	Path string
}

func (e *MountPointError) Error() string {
	return e.Path + " is a mount point: unmount it first"
}

// removeBatch is the number of directory entries read at once.
const removeBatch = 1024

// RemoveTree removes the directory dir and everything below it. Unlike
// os.RemoveAll, it never crosses a mount point: when dir, or a file or
// directory below it, is a mount point, RemoveTree removes nothing of the
// file system mounted there and returns a *MountPointError. Deleting through
// a bind mount would delete the files of the directory that is mounted, which
// are not part of the tree.
//
// RemoveTree stops at the first error, and when ctx is done: what it leaves
// can be removed later. Like os.RemoveAll, it does not follow symbolic links,
// and it returns nil if dir does not exist.
//
// However deep the tree is, RemoveTree keeps few directories open, and it
// never gives the kernel a path of the tree, which may be longer than the
// kernel accepts.
func RemoveTree(ctx context.Context, dir string) error {
	dir = filepath.Clean(dir)
	parent, name := filepath.Dir(dir), filepath.Base(dir)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return &os.PathError{Op: "remove", Path: dir, Err: unix.EINVAL}
	}
	d, err := OpenDir(parent)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return err
	}
	defer d.Close()
	return d.RemoveTree(ctx, name)
}

// A remover removes the files of one mount.
type remover struct {
	ctx context.Context
	// mount identifies the mount of the parent of the tree: every directory
	// of the tree must be on it.
	mount uint64
	// root is the open root of the tree, and rootEntry its entry.
	root      int
	rootEntry *entry
	// hoisted are the names, in the root of the tree, of the directories
	// that were too deep to enter and were moved there, to remove later.
	hoisted []string
	// hoists counts them, to name them.
	hoists int
}

// maxOpenDepth is the number of nested directories that a remover keeps
// open at once. A directory deeper than that is moved to the root of the
// tree (it is on the same mount) and removed from there: the depth of a tree
// is chosen by whoever made it, and must not exhaust the open files of the
// process.
const maxOpenDepth = 128

// maxPasses is the number of times a remover reads a directory again to
// find entries it was not given. Past that, something else is creating
// files in the directory, and removing the directory tells so (ENOTEMPTY)
// instead of running after it for ever.
const maxPasses = 4

// An entry is a file of the tree. Its path is only needed for an error: a
// path kept for each level of a deep tree would take memory in proportion to
// the square of the depth.
type entry struct {
	parent *entry
	// name is the name of the file in its directory; for the root of the
	// tree, its path.
	name string
}

func (e *entry) path() string {
	var names []string
	for ; e.parent != nil; e = e.parent {
		names = append(names, e.name)
	}
	path := e.name
	for i := len(names) - 1; i >= 0; i-- {
		path += string(filepath.Separator) + names[i]
	}
	return path
}

// removeDir removes the directory e, the entry name of the open directory
// parent, and everything below it. inTree tells whether parent is itself part
// of the tree to remove. removed tells whether the directory was there to
// remove.
func (r *remover) removeDir(parent int, e *entry, name string, inTree bool, depth int) (removed bool, err error) {
	if err := r.ctx.Err(); err != nil {
		return false, err
	}
	open := func() (int, error) {
		return retry(func() (int, error) {
			return unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		})
	}
	fd, err := open()
	if errors.Is(err, unix.EACCES) {
		// A directory that its owner may not read, which root never meets.
		// An empty one can be removed as it is; otherwise its owner gives
		// the permissions back first, unless it is a mount point.
		if removed, err := r.unlink(parent, e, name, unix.AT_REMOVEDIR, inTree); err == nil {
			return removed, nil
		}
		if err := makeAccessible(parent, name, r.mount); err != nil {
			if errors.Is(err, errOtherMount) {
				return false, &MountPointError{Path: e.path()}
			}
			return false, &os.PathError{Op: "open", Path: e.path(), Err: unix.EACCES}
		}
		fd, err = open()
	}
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return false, nil
		}
		if depth > 1 && (errors.Is(err, unix.EMFILE) || errors.Is(err, unix.ENFILE)) {
			return false, errTooDeep
		}
		return false, &os.PathError{Op: "open", Path: e.path(), Err: err}
	}
	// The mount is that of the directory that is open: a file system mounted
	// here later hides the directory but does not change what fd reads, and
	// then the directory cannot be removed (EBUSY).
	mount, err := mountOf(fd)
	if err != nil {
		_ = unix.Close(fd)
		return false, fmt.Errorf("cannot tell the mount point of %s: %w", e.path(), err)
	}
	if mount != r.mount {
		_ = unix.Close(fd)
		return false, &MountPointError{Path: e.path()}
	}
	f := os.NewFile(uintptr(fd), name) // f owns fd from here
	defer f.Close()
	if depth == 0 {
		r.root, r.rootEntry = fd, e
	}
	if err := r.removeEntries(f, fd, e, depth); err != nil {
		return false, err
	}
	// The directories that were moved here because they were too deep,
	// which may move others here in turn.
	for depth == 0 && len(r.hoisted) > 0 {
		name := r.hoisted[len(r.hoisted)-1]
		r.hoisted = r.hoisted[:len(r.hoisted)-1]
		if _, err := r.removeDir(fd, &entry{parent: e, name: name}, name, true, 1); err != nil {
			return false, err
		}
	}
	return r.unlink(parent, e, name, unix.AT_REMOVEDIR, inTree)
}

// hoist moves the directory e, the entry name of the open directory dir, to
// the root of the tree, under a name of its own.
func (r *remover) hoist(dir int, e *entry, name string) error {
	for {
		to := fmt.Sprintf(".capze-hoisted-%d", r.hoists)
		r.hoists++
		err := unix.Renameat(dir, name, r.root, to)
		if errors.Is(err, unix.EACCES) {
			// Directories of the tree without the write permission.
			if unix.Fchmod(dir, 0o700) == nil && unix.Fchmod(r.root, 0o700) == nil {
				err = unix.Renameat(dir, name, r.root, to)
			}
		}
		switch {
		case err == nil:
			r.hoisted = append(r.hoisted, to)
			return nil
		case errors.Is(err, unix.ENOENT):
			return nil
		case errors.Is(err, unix.EEXIST), errors.Is(err, unix.ENOTEMPTY), errors.Is(err, unix.ENOTDIR):
			// The name is taken, by a file of the tree: try the next one.
			if r.hoists > 1<<20 {
				return &os.PathError{Op: "rename", Path: e.path(), Err: err}
			}
		case errors.Is(err, unix.EBUSY):
			return &MountPointError{Path: e.path()}
		default:
			return &os.PathError{Op: "rename", Path: e.path(), Err: err}
		}
	}
}

// removeEntries removes every entry of the open directory f, whose file
// descriptor is fd and whose entry is dir.
func (r *remover) removeEntries(f *os.File, fd int, dir *entry, depth int) error {
	for pass := 1; ; pass++ {
		count := 0
		for {
			if err := r.ctx.Err(); err != nil {
				return err
			}
			entries, err := f.ReadDir(removeBatch)
			for _, de := range entries {
				e := &entry{parent: dir, name: de.Name()}
				var removed bool
				var err error
				switch {
				case de.IsDir() && depth >= maxOpenDepth:
					removed, err = true, r.hoist(fd, e, e.name)
				case de.IsDir():
					removed, err = r.removeDir(fd, e, e.name, true, depth+1)
					// The limit of open files is lower than expected.
					if errors.Is(err, errTooDeep) {
						removed, err = true, r.hoist(fd, e, e.name)
					}
				default:
					removed, err = r.unlink(fd, e, e.name, 0, true)
				}
				if err != nil {
					return err
				}
				if removed {
					count++
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return &os.PathError{Op: "read", Path: dir.path(), Err: err}
			}
		}
		// Reading a directory while removing its entries may skip some of
		// them on some file systems: read it again as long as something was
		// removed, a few times. When nothing was, the directory is empty, or
		// lists entries that are not there; after a few times, something
		// else fills it. Removing the directory tells both.
		if count == 0 || pass == maxPasses {
			return nil
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return &os.PathError{Op: "read", Path: dir.path(), Err: err}
		}
	}
}

// unlink removes e, the entry name of the open directory dir: a directory if
// flags is unix.AT_REMOVEDIR, anything else if it is 0. inTree tells whether
// dir is itself part of the tree to remove. removed tells whether the entry
// was there to remove.
func (r *remover) unlink(dir int, e *entry, name string, flags int, inTree bool) (removed bool, err error) {
	remove := func() error {
		_, err := retry(func() (int, error) { return 0, unix.Unlinkat(dir, name, flags) })
		return err
	}
	err = remove()
	if errors.Is(err, unix.EACCES) && inTree {
		// A directory of the tree without the write permission, such as
		// those of a Go module cache: its owner can still remove its entries
		// after giving the permission back. root never gets here.
		if unix.Fchmod(dir, 0o700) == nil {
			err = remove()
		}
	}
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.ENOENT):
		return false, nil
	case errors.Is(err, unix.EBUSY):
		// unlink(2) and rmdir(2) refuse to remove a mount point.
		return false, &MountPointError{Path: e.path()}
	default:
		return false, &os.PathError{Op: "remove", Path: e.path(), Err: err}
	}
}

// errTooDeep is the error of removeDir for a directory that cannot be opened
// because too many of its parents are: it is to be moved to the root of the
// tree.
var errTooDeep = errors.New("too many open directories")

// errOtherMount is the error of makeAccessible for a mount point.
var errOtherMount = errors.New("on another mount")

// retry makes the system call again while a signal interrupts it.
func retry(call func() (int, error)) (int, error) {
	for {
		n, err := call()
		if !errors.Is(err, unix.EINTR) {
			return n, err
		}
	}
}
