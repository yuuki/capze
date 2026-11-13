package layout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	src, err := l.checkSource(source)
	if err != nil {
		return Release{}, err
	}
	// Before anything is created: rename(2) would fail below.
	if same, err := fsutil.SameDevice(src, l.dir); err != nil {
		return Release{}, err
	} else if !same {
		return Release{}, l.cannotMove(src)
	}
	// What a full file system refuses is made here, where nothing has
	// changed yet, the trash included: the pruning that frees space needs it.
	if err := os.MkdirAll(l.releasesPath(), 0o755); err != nil {
		return Release{}, err
	}
	if err := l.ensureTrash(); err != nil {
		return Release{}, err
	}
	if err := l.checkOneFileSystem(); err != nil {
		return Release{}, err
	}
	// Before the lock: this is the only step that can take long, and the
	// source is not yet anything to the other commands.
	if sync {
		if err := fsutil.SyncTree(src); err != nil {
			return Release{}, fmt.Errorf("flush %s to disk: %w", src, err)
		}
	}

	unlock, err := l.lock(ctx)
	if err != nil {
		return Release{}, err
	}
	defer unlock()
	h, err := l.scan()
	if err != nil {
		return Release{}, err
	}
	if err := l.checkCurrentLink(); err != nil {
		return Release{}, err
	}
	id, err := nextID(l.Now(), h.newest)
	if err != nil {
		return Release{}, err
	}
	if err := l.prepareSwitch(id); err != nil {
		return Release{}, err
	}
	// The last moment to give up without having changed anything.
	if err := ctx.Err(); err != nil {
		l.cancelSwitch()
		return Release{}, err
	}

	// The source first: this is the change that can fail for reasons that no
	// check above sees (a mount point, another mount of the same file
	// system), and nothing has to be undone then.
	release := Release{ID: id, Path: filepath.Join(l.releasesPath(), id), State: Current}
	if err := os.Rename(src, release.Path); err != nil {
		l.cancelSwitch()
		if errors.Is(err, syscall.EXDEV) {
			return Release{}, l.cannotMove(src)
		}
		return Release{}, fmt.Errorf("move %s into %s: %w", src, l.releasesPath(), underlying(err))
	}
	if err := l.discardRolledBack(h); err != nil {
		l.cancelSwitch()
		if undo := os.Rename(release.Path, src); undo != nil {
			return Release{}, fmt.Errorf("%w; %s is deployed as %s but is not the current release", err, src, release.Path)
		}
		return Release{}, err
	}
	err = fsutil.SyncDir(l.releasesPath())
	if err == nil {
		err = l.commitSwitch()
	} else {
		l.cancelSwitch()
	}
	if err != nil {
		return Release{}, fmt.Errorf("%s is deployed but is not the current release: %w", release.Path, err)
	}
	return release, nil
}

// discardRolledBack moves the releases that were rolled back to the trash:
// all of them, or none if one cannot be moved.
func (l *Layout) discardRolledBack(h *history) error {
	type moved struct{ from, to string }
	var done []moved
	for _, r := range h.releases {
		if r.State != RolledBack {
			continue
		}
		to, err := l.discard(r)
		if err != nil {
			err = fmt.Errorf("discard the rolled-back release %s: %w", r.ID, err)
			for i := len(done) - 1; i >= 0; i-- {
				if undo := os.Rename(done[i].to, done[i].from); undo != nil {
					err = fmt.Errorf("%w; %s is left in %s", err, filepath.Base(done[i].from), done[i].to)
				}
			}
			return err
		}
		done = append(done, moved{from: r.Path, to: to})
	}
	return nil
}

// checkSource returns the absolute path of the directory to deploy, after
// checking that it can become a release.
func (l *Layout) checkSource(source string) (string, error) {
	src, err := absolute(source)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("source directory %s does not exist", source)
		}
		return "", fmt.Errorf("source directory: %w", err)
	}
	info, err := os.Lstat(src)
	if err != nil {
		return "", err
	}
	// A symbolic link to a directory is refused too: the link would become
	// the release, not the directory.
	if !info.IsDir() {
		return "", fmt.Errorf("source %s is not a directory", src)
	}
	// The files are compared, not the paths: the deploy directory may be
	// reached through a symbolic link or another mount.
	if dir, err := os.Stat(l.dir); err != nil {
		return "", err
	} else if inside(l.dir, info) {
		return "", fmt.Errorf("source %s contains the deploy directory %s", src, l.dir)
	} else if !inside(src, dir) {
		return src, nil
	}
	for _, own := range []string{l.releasesPath(), l.statePath()} {
		if dir, err := os.Stat(own); err == nil && inside(src, dir) {
			return "", fmt.Errorf("source %s already belongs to the deploy directory %s", src, l.dir)
		}
	}
	return src, nil
}

// checkOneFileSystem fails if releases and the trash are not on the same
// file system: no release could then be discarded or deleted.
func (l *Layout) checkOneFileSystem() error {
	same, err := fsutil.SameDevice(l.releasesPath(), l.trashPath())
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%s and %s are not on the same file system: the deploy directory must be one file system", l.releasesPath(), l.trashPath())
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
