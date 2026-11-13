package fsutil

import (
	"bytes"
	"errors"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// SyncTree flushes to disk the file system that holds the directory dir, and
// so every file below dir (syncfs(2)).
func SyncTree(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := unix.Syncfs(int(f.Fd())); err != nil {
		return &os.PathError{Op: "syncfs", Path: dir, Err: err}
	}
	return nil
}

// mountOf returns the ID of the mount that holds the open file fd. Two
// directories of the same file system have different mount IDs when one of
// them is reached through a bind mount, which their device numbers do not
// tell.
func mountOf(fd int) (uint64, error) {
	var st unix.Statx_t
	err := unix.Statx(fd, "", unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &st)
	if err == nil && st.Mask&unix.STATX_MNT_ID != 0 {
		return st.Mnt_id, nil
	}
	// Linux before 5.8 does not report the mount ID with statx(2), but with
	// the file descriptor in /proc, since 3.15.
	return fdinfoMountOf(fd)
}

// fdinfoMountOf returns the ID of the mount that holds the open file fd, as
// /proc tells it.
func fdinfoMountOf(fd int) (uint64, error) {
	data, err := os.ReadFile("/proc/self/fdinfo/" + strconv.Itoa(fd))
	if err != nil {
		return 0, err
	}
	id, ok := parseFdinfoMountID(data)
	if !ok {
		return 0, errors.New("no mount ID in /proc/self/fdinfo")
	}
	return id, nil
}

// parseFdinfoMountID returns the mount ID of a /proc/PID/fdinfo/FD file: the
// value of its "mnt_id:" line.
func parseFdinfoMountID(data []byte) (uint64, bool) {
	for line := range bytes.Lines(data) {
		value, found := bytes.CutPrefix(line, []byte("mnt_id:"))
		if !found {
			continue
		}
		id, err := strconv.ParseUint(string(bytes.TrimSpace(value)), 10, 64)
		if err != nil {
			return 0, false
		}
		return id, true
	}
	return 0, false
}
