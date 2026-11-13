package fsutil

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// makeAccessible gives its owner every permission on the directory name of
// the open directory parent, which could not be opened for reading. It does
// not follow a symbolic link, and it returns errOtherMount, without changing
// anything, if the directory is not on the mount: its permissions are then
// those of the root of another file system.
func makeAccessible(parent int, name string, mount uint64) error {
	// O_PATH opens what cannot be read; the permissions are then changed
	// through the file descriptor, so that they are those of the directory
	// whose mount was checked.
	fd, err := retry(func() (int, error) {
		return unix.Openat(parent, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if m, err := mountOf(fd); err != nil {
		return err
	} else if m != mount {
		return errOtherMount
	}
	return unix.Chmod("/proc/self/fd/"+strconv.Itoa(fd), 0o700)
}

// MountsBelow returns the mount points that are the directory dir or are
// below it, as /proc/self/mountinfo lists them: nil if there is none, or if
// the list cannot be read.
//
// It tells in advance, and for a whole tree at once, what RemoveTree would
// meet. It is not a guarantee: a mount made later, or a system without
// /proc, is for RemoveTree to refuse.
func MountsBelow(dir string) []string {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil
	}
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}
	var below []string
	for _, point := range parseMountPoints(data) {
		if point == real || strings.HasPrefix(point, strings.TrimSuffix(real, "/")+"/") {
			below = append(below, point)
		}
	}
	return below
}

// parseMountPoints returns the mount points of a /proc/PID/mountinfo file:
// the fifth field of each line, in which the kernel writes a space, a tab, a
// newline and a backslash as a backslash and three octal digits.
func parseMountPoints(data []byte) []string {
	var points []string
	for line := range bytes.Lines(data) {
		fields := bytes.Fields(line)
		if len(fields) < 5 {
			continue
		}
		points = append(points, unescapeOctal(fields[4]))
	}
	return points
}

// unescapeOctal replaces each backslash followed by three octal digits with
// the byte they give.
func unescapeOctal(field []byte) string {
	out := make([]byte, 0, len(field))
	for i := 0; i < len(field); i++ {
		if field[i] == '\\' && i+3 < len(field) {
			if n, err := strconv.ParseUint(string(field[i+1:i+4]), 8, 8); err == nil {
				out = append(out, byte(n))
				i += 3
				continue
			}
		}
		out = append(out, field[i])
	}
	return string(out)
}
