//go:build linux || darwin

// Package fsutil holds the file system operations of capze that the standard
// library does not offer: a lock that other capze processes honour, a
// recursive removal that never crosses a mount point, and flushing a
// directory tree to disk.
package fsutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// lockPollInterval is how often Lock tries again while another process holds
// the lock. flock(2) cannot wait and watch a context at the same time.
const lockPollInterval = 100 * time.Millisecond

// Lock takes the exclusive advisory lock (flock(2)) on the file path, which
// it creates if needed, and returns the function that releases it. While
// another process holds the lock, Lock waits until ctx is done; it calls
// waiting, if not nil, once before it starts to wait.
//
// The kernel releases the lock when the process exits, however it exits: a
// lock is never left behind.
func Lock(ctx context.Context, path string, waiting func()) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	for first := true; ; first = false {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() { _ = f.Close() }, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			_ = f.Close()
			return nil, fmt.Errorf("lock %s: %w", path, err)
		}
		if first && waiting != nil {
			waiting()
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPollInterval):
		}
	}
}

// SyncDir flushes the entries of the directory dir to disk, so that a file
// created in it, or renamed into it, survives a crash.
func SyncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	// Some file systems (network and FUSE ones) do not implement fsync(2) on
	// a directory: they have nothing to flush.
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return err
}

// SameDevice reports whether the files a and b are on the same device, which
// rename(2) requires. It follows symbolic links.
func SameDevice(a, b string) (bool, error) {
	var sa, sb unix.Stat_t
	if err := unix.Stat(a, &sa); err != nil {
		return false, &os.PathError{Op: "stat", Path: a, Err: err}
	}
	if err := unix.Stat(b, &sb); err != nil {
		return false, &os.PathError{Op: "stat", Path: b, Err: err}
	}
	return sa.Dev == sb.Dev, nil
}
