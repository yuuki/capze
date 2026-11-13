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
