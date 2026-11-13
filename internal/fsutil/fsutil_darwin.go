package fsutil

import (
	"os"

	"golang.org/x/sys/unix"
)

// SyncTree flushes every file system to disk (sync(2)): macOS has no call
// that flushes only the file system that holds dir.
func SyncTree(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	if err := unix.Sync(); err != nil {
		return &os.PathError{Op: "sync", Path: dir, Err: err}
	}
	return nil
}

// mountOf identifies the mount that holds the open file fd by its device
// number: macOS has no bind mounts, so that two directories of one file
// system are on the same mount.
func mountOf(fd int) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}

// makeAccessible gives its owner every permission on the directory name of
// the open directory parent, which could not be opened for reading. It does
// not follow a symbolic link, and it returns errOtherMount, without changing
// anything, if the directory is not on the mount.
func makeAccessible(parent int, name string, mount uint64) error {
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.ENOTDIR
	}
	if uint64(st.Dev) != mount {
		return errOtherMount
	}
	return unix.Fchmodat(parent, name, 0o700, unix.AT_SYMLINK_NOFOLLOW)
}

// MountsBelow returns nil: on macOS, RemoveTree alone tells the mount points
// below a directory.
func MountsBelow(string) []string {
	return nil
}
