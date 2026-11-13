package layout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/yuuki/capze/internal/fsutil"
)

// expired returns how many of the oldest of n releases pruning deletes, when
// it keeps keep releases and current is the index of the current release (-1
// without one).
//
// Pruning keeps the current release and the keep-1 releases before it; with
// no current release, the keep newest ones. It never deletes the current
// release, nor a newer one: those are what a rollback can still choose.
func expired(n, current, keep int) int {
	head := n - 1
	if current >= 0 {
		head = current
	}
	return max(head+1-keep, 0)
}

// Plan returns the paths that Prune would delete, in the order Prune deletes
// them, without changing anything: what a failed deletion left in the trash,
// then the releases beyond the keep ones to keep, oldest first.
func (l *Layout) Plan(keep int) ([]string, error) {
	if keep < 1 {
		return nil, fmt.Errorf("cannot keep %d releases: at least 1", keep)
	}
	paths, err := l.trashed()
	if err != nil {
		return nil, err
	}
	h, err := l.scan()
	if err != nil {
		return nil, err
	}
	for _, r := range h.releases[:expired(len(h.releases), h.current, keep)] {
		paths = append(paths, r.Path)
	}
	return paths, nil
}

// Prune deletes the paths that Plan returns, and calls removed with each one
// once it is deleted.
//
// A release leaves releases with one rename(2), to the trash, before any of
// its files is deleted: a deletion that fails, or is interrupted, leaves the
// rest of the release in the trash, where the next Prune finds it, and never
// in releases, where a rollback could choose it. Prune goes on with the other
// paths after a failure, and returns every error.
//
// Prune never deletes through a mount point: it leaves in the trash a
// release that has one below it, with an error that names the mount point.
func (l *Layout) Prune(ctx context.Context, keep int, removed func(path string)) error {
	if keep < 1 {
		return fmt.Errorf("cannot keep %d releases: at least 1", keep)
	}
	unlock, err := l.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	// A doomed path is known to the caller as shown, and is now at path.
	type doomed struct{ shown, path string }
	var victims []doomed
	var errs []error

	trashed, err := l.trashed()
	if err != nil {
		return err
	}
	for _, path := range trashed {
		victims = append(victims, doomed{shown: path, path: path})
	}
	h, err := l.scan()
	if err != nil {
		return err
	}
	for _, r := range h.releases[:expired(len(h.releases), h.current, keep)] {
		path, err := l.discard(r)
		if err != nil {
			errs = append(errs, fmt.Errorf("discard the release %s: %w", r.ID, err))
			continue
		}
		victims = append(victims, doomed{shown: r.Path, path: path})
	}

	for _, v := range victims {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := remove(ctx, v.path); err != nil {
			errs = append(errs, fmt.Errorf("%s is left to delete: %w", v.path, err))
			continue
		}
		if removed != nil {
			removed(v.shown)
		}
	}
	return errors.Join(errs...)
}

// trashed returns the paths of the entries of the trash, sorted by name.
func (l *Layout) trashed() ([]string, error) {
	entries, err := os.ReadDir(l.trashPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		paths = append(paths, filepath.Join(l.trashPath(), e.Name()))
	}
	return paths, nil
}

// remove deletes an entry of the trash.
func remove(ctx context.Context, path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return fsutil.RemoveTree(ctx, path)
	}
	return os.Remove(path)
}
