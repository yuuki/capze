package layout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
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

// doomed returns what to delete: the names of the entries of the trash,
// sorted, then the releases beyond the keep ones to keep, oldest first.
//
// It fails if it cannot tell which release is in use: a current that is not
// a symbolic link, or that does not lead to a release. Counting from the
// newest release instead could delete the one in use.
func (l *Layout) doomed(h *handles, keep int) (trashed []string, releases []Release, err error) {
	if keep < 1 {
		return nil, nil, fmt.Errorf("cannot keep %d releases: at least 1", keep)
	}
	if h.trash != nil {
		if trashed, err = h.trash.Names(); err != nil {
			return nil, nil, err
		}
	}
	hist, err := l.scan(h)
	if err != nil {
		return nil, nil, err
	}
	if err := l.checkCurrentLink(hist); err != nil {
		return nil, nil, err
	}
	if hist.strayCurrent {
		return nil, nil, fmt.Errorf("%s does not lead to a release of %s, so the release in use is unknown: "+
			"point it to a release (capze rollback --to), or remove it to keep the newest releases",
			l.currentPath(), l.releasesPath())
	}
	return trashed, hist.releases[:expired(len(hist.releases), hist.current, keep)], nil
}

// Plan returns the paths that Prune would delete, in the order Prune deletes
// them, without changing anything. Like Prune, it needs to read the trash,
// which belongs to the user of capze.
func (l *Layout) Plan(keep int) ([]string, error) {
	h, err := l.open()
	if err != nil {
		return nil, err
	}
	defer h.close()
	trashed, releases, err := l.doomed(h, keep)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range trashed {
		paths = append(paths, filepath.Join(l.trashPath(), name))
	}
	for _, r := range releases {
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
// Prune never deletes through a mount point. A release that has a mount
// below it is in use (a container runs in it): Prune leaves it whole, where
// it is, with an error that names the mount point.
func (l *Layout) Prune(ctx context.Context, keep int, removed func(path string)) error {
	if keep < 1 {
		return fmt.Errorf("cannot keep %d releases: at least 1", keep)
	}
	h, err := l.open()
	if err != nil {
		return err
	}
	defer h.close()
	// Not a deploy directory, or not yet: there is nothing to delete, and
	// nothing is created in it.
	if h.releases == nil && h.state == nil {
		return nil
	}
	if err := h.ensureState(); err != nil {
		return err
	}
	unlock, err := h.state.Lock(ctx, l.Waiting)
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	trashed, releases, err := l.doomed(h, keep)
	if err != nil {
		return err
	}

	// A victim is known to the caller as shown, and is the entry name of
	// the trash.
	type victim struct{ shown, name string }
	var victims []victim
	var errs []error
	for _, name := range trashed {
		victims = append(victims, victim{shown: filepath.Join(l.trashPath(), name), name: name})
	}
	for _, r := range releases {
		if err := inUse(r.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		name, err := h.discard(r)
		if err != nil {
			errs = append(errs, fmt.Errorf("discard the release %s: %w", r.ID, err))
			continue
		}
		victims = append(victims, victim{shown: r.Path, name: name})
	}

	for _, v := range victims {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		if err := l.remove(ctx, h, v.name); err != nil {
			errs = append(errs, err)
			continue
		}
		if removed != nil {
			removed(v.shown)
		}
	}
	return errors.Join(errs...)
}

// inUse returns an error if a file system is mounted on the directory dir or
// below it, as far as the system tells.
//
// This spares a release in use as a whole. It is not what keeps capze from
// deleting through a mount point: fsutil.RemoveTree checks every directory
// it enters, also where the system does not list the mounts.
func inUse(dir string) error {
	if mounts := fsutil.MountsBelow(dir); len(mounts) > 0 {
		return fmt.Errorf("%s is left: %w", dir, &fsutil.MountPointError{Path: mounts[0]})
	}
	return nil
}

// remove deletes the entry name of the trash.
func (l *Layout) remove(ctx context.Context, h *handles, name string) error {
	st, err := h.trash.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if !st.IsDir {
		return h.trash.Unlink(name)
	}
	path := filepath.Join(l.trashPath(), name)
	if err := inUse(path); err != nil {
		return err
	}
	if err := h.trash.RemoveTree(ctx, name); err != nil {
		return fmt.Errorf("%s is left to delete: %w", path, err)
	}
	return nil
}
