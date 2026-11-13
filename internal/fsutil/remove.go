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

// A MountPointError reports that RemoveTree stopped at a mount point.
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
func RemoveTree(ctx context.Context, dir string) error {
	dir = filepath.Clean(dir)
	parent, name := filepath.Dir(dir), filepath.Base(dir)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return &os.PathError{Op: "remove", Path: dir, Err: unix.EINVAL}
	}
	pfd, err := retry(func() (int, error) {
		return unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return &os.PathError{Op: "open", Path: parent, Err: err}
	}
	defer unix.Close(pfd)
	mount, err := mountOf(pfd)
	if err != nil {
		return fmt.Errorf("cannot tell the mount point of %s: %w", parent, err)
	}
	r := remover{ctx: ctx, mount: mount}
	return r.removeDir(pfd, name, dir, false)
}

// A remover removes the files of one mount.
type remover struct {
	ctx context.Context
	// mount identifies the mount of the parent of the tree: every directory
	// of the tree must be on it.
	mount uint64
}

// removeDir removes the directory name of the open directory parent, and
// everything below it. path is its path, for error messages. inTree tells
// whether parent is itself part of the tree to remove.
func (r *remover) removeDir(parent int, name, path string, inTree bool) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	fd, err := retry(func() (int, error) {
		return unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		return &os.PathError{Op: "open", Path: path, Err: err}
	}
	// The mount is that of the directory that is open: a file system mounted
	// here later hides the directory but does not change what fd reads, and
	// then the directory cannot be removed (EBUSY).
	mount, err := mountOf(fd)
	if err != nil {
		_ = unix.Close(fd)
		return fmt.Errorf("cannot tell the mount point of %s: %w", path, err)
	}
	if mount != r.mount {
		_ = unix.Close(fd)
		return &MountPointError{Path: path}
	}
	f := os.NewFile(uintptr(fd), path) // f owns fd from here
	err = r.removeEntries(f, fd, path)
	_ = f.Close()
	if err != nil {
		return err
	}
	return r.unlink(parent, name, unix.AT_REMOVEDIR, path, inTree)
}

// removeEntries removes every entry of the open directory f, whose file
// descriptor is fd.
func (r *remover) removeEntries(f *os.File, fd int, path string) error {
	for {
		removed := 0
		for {
			entries, err := f.ReadDir(removeBatch)
			for _, e := range entries {
				child := filepath.Join(path, e.Name())
				if e.IsDir() {
					if err := r.removeDir(fd, e.Name(), child, true); err != nil {
						return err
					}
				} else if err := r.unlink(fd, e.Name(), 0, child, true); err != nil {
					return err
				}
				removed++
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}
		if removed == 0 {
			return nil
		}
		// Reading a directory while removing its entries may skip some of
		// them on some file systems: read it again until it is empty.
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}
}

// unlink removes the entry name of the open directory dir: a directory if
// flags is unix.AT_REMOVEDIR, anything else if it is 0. inTree tells whether
// dir is itself part of the tree to remove.
func (r *remover) unlink(dir int, name string, flags int, path string, inTree bool) error {
	remove := func() error {
		_, err := retry(func() (int, error) { return 0, unix.Unlinkat(dir, name, flags) })
		return err
	}
	err := remove()
	if errors.Is(err, unix.EACCES) && inTree {
		// A directory of the tree without the write permission, such as
		// those of a Go module cache: its owner can still remove its entries
		// after giving the permission back. root never gets here.
		if unix.Fchmod(dir, 0o700) == nil {
			err = remove()
		}
	}
	switch {
	case err == nil, errors.Is(err, unix.ENOENT):
		return nil
	case errors.Is(err, unix.EBUSY):
		// unlink(2) and rmdir(2) refuse to remove a mount point.
		return &MountPointError{Path: path}
	default:
		return &os.PathError{Op: "remove", Path: path, Err: err}
	}
}

// retry makes the system call again while a signal interrupts it.
func retry(call func() (int, error)) (int, error) {
	for {
		n, err := call()
		if !errors.Is(err, unix.EINTR) {
			return n, err
		}
	}
}
