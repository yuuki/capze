package layout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
// When Deploy fails, source is where it was and current is unchanged, unless
// the error says what else was done.
func (l *Layout) Deploy(ctx context.Context, source string, sync bool) (Release, error) {
	src, err := l.checkSource(source)
	if err != nil {
		return Release{}, err
	}
	if err := os.MkdirAll(l.releasesPath(), 0o755); err != nil {
		return Release{}, err
	}
	// Before anything changes: rename(2) would fail below.
	if same, err := fsutil.SameDevice(src, l.releasesPath()); err != nil {
		return Release{}, err
	} else if !same {
		return Release{}, l.otherFileSystem(src)
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
	// The last moment to give up without having changed anything.
	if err := ctx.Err(); err != nil {
		return Release{}, err
	}

	for _, r := range h.releases {
		if r.State != RolledBack {
			continue
		}
		if _, err := l.discard(r); err != nil {
			return Release{}, fmt.Errorf("discard the rolled-back release %s: %w", r.ID, err)
		}
	}
	release := Release{ID: id, Path: filepath.Join(l.releasesPath(), id), State: Current}
	if err := os.Rename(src, release.Path); err != nil {
		if errors.Is(err, syscall.EXDEV) {
			return Release{}, l.otherFileSystem(src)
		}
		return Release{}, fmt.Errorf("move %s into %s: %w", src, l.releasesPath(), underlying(err))
	}
	err = fsutil.SyncDir(l.releasesPath())
	if err == nil {
		err = l.switchTo(id)
	}
	if err != nil {
		return Release{}, fmt.Errorf("%s is deployed but is not the current release: %w", release.Path, err)
	}
	if err := l.syncSwitch(id); err != nil {
		return Release{}, err
	}
	return release, nil
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
	// Compare the paths without their symbolic links, in case the deploy
	// directory is reached through one.
	realSrc, err := filepath.EvalSymlinks(src)
	if err != nil {
		return "", err
	}
	realDir, err := filepath.EvalSymlinks(l.dir)
	if err != nil {
		return "", err
	}
	switch {
	case within(realDir, realSrc):
		return "", fmt.Errorf("source %s contains the deploy directory %s", src, l.dir)
	case within(realSrc, filepath.Join(realDir, releasesName)), within(realSrc, filepath.Join(realDir, stateName)):
		return "", fmt.Errorf("source %s already belongs to the deploy directory %s", src, l.dir)
	}
	return src, nil
}

// within reports whether path is dir or is below it. Both are clean absolute
// paths.
func within(path, dir string) bool {
	if path == dir {
		return true
	}
	if !strings.HasSuffix(dir, string(filepath.Separator)) {
		dir += string(filepath.Separator)
	}
	return strings.HasPrefix(path, dir)
}

// otherFileSystem is the error of a source that rename(2) cannot move into
// the deploy directory.
func (l *Layout) otherFileSystem(src string) error {
	return fmt.Errorf("source %s is not on the file system of %s: "+
		"capze moves the directory and never copies it, so prepare the release on that file system",
		src, l.dir)
}

// underlying returns the cause of an error of os.Rename, whose message
// otherwise repeats both paths.
func underlying(err error) error {
	var link *os.LinkError
	if errors.As(err, &link) {
		return link.Err
	}
	return err
}
