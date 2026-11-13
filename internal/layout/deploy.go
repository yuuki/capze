package layout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/yuuki/capze/internal/fsutil"
)

// Deploy makes the directory source the newest release, and the current one.
// It moves source into releases with rename(2), under a new name, so source
// must be on the file system of the deploy directory. With sync, it first
// flushes the files of source to disk, so that current never points, after a
// crash, to a release whose files were not all written.
//
// Deploy also discards the releases newer than the current one: they were
// rolled back, and the history must not keep them between the new release
// and the one it replaces. They move to the trash, which Prune empties.
//
// When Deploy fails, source is where it was, the releases are those that
// were there, and current is unchanged, unless the error says what else was
// done.
func (l *Layout) Deploy(ctx context.Context, source string, sync bool) (Release, error) {
	src, err := absolute(source)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Release{}, fmt.Errorf("source directory %s does not exist", source)
		}
		return Release{}, fmt.Errorf("source directory: %w", err)
	}
	// The source is opened once, and what is checked, flushed and compared
	// below is the directory that is open: if the path leads to something
	// else by the time it is moved, the move is undone.
	parent, err := fsutil.OpenDir(filepath.Dir(src))
	if err != nil {
		return Release{}, err
	}
	defer parent.Close()
	name := filepath.Base(src)
	dir, err := parent.Sub(name)
	if err != nil {
		return Release{}, sourceError(src, err)
	}
	defer dir.Close()

	h, err := l.open()
	if err != nil {
		return Release{}, err
	}
	defer h.close()
	if err := l.checkSource(h, src, dir); err != nil {
		return Release{}, err
	}
	// What a full file system refuses is made here, where nothing has
	// changed yet, the trash included: the pruning that frees space needs it.
	if h.releases == nil {
		if h.releases, err = mkSub(h.root, releasesName, 0o755); err != nil {
			return Release{}, err
		}
	}
	if err := h.ensureTrash(); err != nil {
		return Release{}, err
	}
	// Before the lock: this is the only step that can take long, and the
	// source is not yet anything to the other commands.
	if sync {
		if err := dir.SyncFS(); err != nil {
			return Release{}, fmt.Errorf("flush %s to disk: %w", src, err)
		}
	}

	unlock, err := h.state.Lock(ctx, l.Waiting)
	if err != nil {
		return Release{}, err
	}
	defer unlock()
	hist, err := l.scan(h)
	if err != nil {
		return Release{}, err
	}
	if err := l.checkCurrentLink(hist); err != nil {
		return Release{}, err
	}
	id, err := nextID(l.Now(), hist.newest)
	if err != nil {
		return Release{}, err
	}
	if err := h.prepareSwitch(id); err != nil {
		return Release{}, err
	}
	// The last moment to give up without having changed anything.
	if err := ctx.Err(); err != nil {
		h.cancelSwitch()
		return Release{}, err
	}

	// The rolled-back releases leave first: whenever the deployment stops
	// after this, even killed, the history never holds both them and the new
	// release. If the source then cannot be moved, they are moved back.
	restore, err := l.discardRolledBack(h, hist)
	if err != nil {
		h.cancelSwitch()
		return Release{}, err
	}
	release := Release{ID: id, Path: filepath.Join(l.releasesPath(), id), State: Current}
	if err := parent.Rename(name, h.releases, id); err != nil {
		h.cancelSwitch()
		if errors.Is(err, syscall.EXDEV) {
			err = l.cannotMove(src)
		} else {
			err = fmt.Errorf("move %s into %s: %w", src, l.releasesPath(), underlying(err))
		}
		return Release{}, restore(err)
	}
	// What the path led to when it was moved must be the directory that was
	// checked: not a symbolic link, nor another directory, put there
	// meanwhile.
	if st, err := h.releases.Lstat(id); err != nil || !st.IsDir || st.ID != dir.ID() {
		h.cancelSwitch()
		err = fmt.Errorf("source %s was replaced while it was deployed", src)
		if undo := h.releases.Rename(id, parent, name); undo != nil {
			err = fmt.Errorf("%w; what replaced it is left in %s", err, release.Path)
		}
		return Release{}, restore(err)
	}
	err = h.releases.Sync()
	if err == nil {
		err = l.commitSwitch(h)
	} else {
		h.cancelSwitch()
	}
	if err != nil {
		return Release{}, fmt.Errorf("%s is deployed but is not the current release: %w", release.Path, err)
	}
	return release, nil
}

// discardRolledBack moves the releases that were rolled back to the trash:
// all of them, or none if one cannot be moved. It returns the function that
// moves them back when the deployment fails with the error it is given, and
// returns that error, completed if a release could not be moved back.
func (l *Layout) discardRolledBack(h *handles, hist *history) (restore func(error) error, err error) {
	type moved struct{ id, name string }
	var done []moved
	restore = func(err error) error {
		for i := len(done) - 1; i >= 0; i-- {
			if undo := h.trash.Rename(done[i].name, h.releases, done[i].id); undo != nil {
				err = fmt.Errorf("%w; the rolled-back release %s is left in %s", err, done[i].id, filepath.Join(l.trashPath(), done[i].name))
			}
		}
		return err
	}
	for _, r := range hist.releases {
		if r.State != RolledBack {
			continue
		}
		name, err := h.discard(r)
		if err != nil {
			return nil, restore(fmt.Errorf("discard the rolled-back release %s: %w", r.ID, err))
		}
		done = append(done, moved{id: r.ID, name: name})
	}
	return restore, nil
}

// sourceError explains why the source cannot be opened as a directory of its
// own.
func sourceError(src string, err error) error {
	var notDir *fsutil.NotPlainDirError
	var mount *fsutil.MountPointError
	switch {
	case errors.As(err, &notDir):
		// A symbolic link to a directory is refused too: the link would
		// become the release, not the directory.
		return fmt.Errorf("source %s is %s, not a directory", src, notDir.What)
	case errors.As(err, &mount):
		return fmt.Errorf("source %s is a mount point, which cannot be moved", src)
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("source directory %s does not exist", src)
	}
	return err
}

// checkSource checks that the open source directory dir can become a
// release: it is neither around nor inside what the deploy directory
// already manages, and rename(2) can move it there.
//
// The directories that really hold the source are compared, not paths: the
// source may be named through a symbolic link (current, for instance) or
// another mount of the deploy directory.
func (l *Layout) checkSource(h *handles, src string, dir *fsutil.Dir) error {
	around, err := h.root.Ancestors()
	if err != nil {
		return err
	}
	if slices.Contains(around, dir.ID()) {
		return fmt.Errorf("source %s contains the deploy directory %s", src, l.dir)
	}
	above, err := dir.Ancestors()
	if err != nil {
		return err
	}
	for _, own := range []*fsutil.Dir{h.releases, h.state} {
		if own != nil && slices.Contains(above, own.ID()) {
			return fmt.Errorf("source %s already belongs to the deploy directory %s", src, l.dir)
		}
	}
	if !dir.SameMount(h.root) {
		return l.cannotMove(src)
	}
	return nil
}

// cannotMove is the error of a source that rename(2) cannot move into the
// deploy directory.
func (l *Layout) cannotMove(src string) error {
	return fmt.Errorf("source %s is on another file system, or another mount, than %s: "+
		"capze moves the directory and never copies it, so prepare the release in the deploy directory",
		src, l.dir)
}
