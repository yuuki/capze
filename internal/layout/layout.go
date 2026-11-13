// Package layout manages a deploy directory, laid out like those of
// Capistrano:
//
//	DEPLOY_DIR
//	├── current -> releases/20261113101500
//	├── releases
//	│   ├── 20261112093000
//	│   └── 20261113101500
//	└── .capze
//	    ├── lock
//	    └── trash
//
// Each directory of releases is one release, named after the time it was
// deployed, and current is a symbolic link to the release in use. .capze
// belongs to capze: the lock that serializes the commands that change the
// deploy directory, and the releases that are being deleted.
//
// The package keeps these promises, which docs/design.md explains:
//
//   - A release appears in releases, and leaves it, by one rename(2): the
//     directory never holds a release that is half copied or half deleted.
//   - current changes by one rename(2): it always points to a whole release.
//   - The names of the releases sort in the order of their deployments.
//   - The releases form one history, whose head is current: a deployment
//     discards the releases newer than current, which were rolled back.
//   - Nothing is deleted through a mount point.
package layout

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yuuki/capze/internal/fsutil"
)

// The names of the files of a deploy directory.
const (
	currentName  = "current"
	releasesName = "releases"
	stateName    = ".capze"
	lockName     = "lock"
	trashName    = "trash"
	// nextLinkName is the name of the new current link, in the state
	// directory, before it replaces the old one.
	nextLinkName = "current.next"
)

// DefaultKeep is the default number of releases that pruning keeps.
const DefaultKeep = 5

// A State tells how a release relates to the current one.
type State int

// The states of a release.
const (
	// Old is the state of a release older than the current one, and of every
	// release when there is no current one.
	Old State = iota
	// Current is the state of the release that current points to.
	Current
	// RolledBack is the state of a release newer than the current one: the
	// current one was chosen again after it.
	RolledBack
)

func (s State) String() string {
	switch s {
	case Current:
		return "current"
	case RolledBack:
		return "rolled-back"
	default:
		return ""
	}
}

// A Release is one directory of releases.
type Release struct {
	// ID is the name of the directory: the time of the deployment.
	ID string
	// Path is the absolute path of the directory.
	Path string
	// State tells how the release relates to the current one.
	State State
}

// A Layout is a deploy directory.
type Layout struct {
	dir string
	// Now gives the time of a deployment. Tests replace it.
	Now func() time.Time
	// Waiting, if not nil, is called once when a command starts to wait for
	// another one that holds the lock of the deploy directory.
	Waiting func()
}

// Open returns the Layout of the deploy directory dir, which must exist: a
// mistyped path must not start a new, empty deploy directory.
func Open(dir string) (*Layout, error) {
	abs, err := absolute(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("deploy directory %s does not exist: create it first", dir)
		}
		return nil, fmt.Errorf("deploy directory: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("deploy directory %s is not a directory", abs)
	}
	return &Layout{dir: abs, Now: time.Now}, nil
}

// absolute returns the absolute path of the existing file path, without
// following a symbolic link at its end.
//
// filepath.Abs removes the ".." of a path without looking at the file system,
// while the kernel follows a symbolic link before going up: after a link, the
// two lead to different files. capze changes and deletes by absolute path, so
// it refuses a path for which they differ. It also refuses "", which
// filepath.Abs takes for the working directory: an unset variable of a script
// must not make it the deploy directory.
func absolute(path string) (string, error) {
	if path == "" {
		return "", errors.New("the path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	asGiven, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	cleaned, err := os.Lstat(abs)
	if err != nil || !os.SameFile(asGiven, cleaned) {
		return "", fmt.Errorf("%s is not %s, because of a symbolic link before a \"..\": write the path without \"..\"", path, abs)
	}
	return abs, nil
}

// Dir returns the absolute path of the deploy directory.
func (l *Layout) Dir() string { return l.dir }

func (l *Layout) currentPath() string  { return filepath.Join(l.dir, currentName) }
func (l *Layout) releasesPath() string { return filepath.Join(l.dir, releasesName) }
func (l *Layout) statePath() string    { return filepath.Join(l.dir, stateName) }
func (l *Layout) trashPath() string    { return filepath.Join(l.dir, stateName, trashName) }

// Releases returns the releases of the deploy directory, oldest first.
func (l *Layout) Releases() ([]Release, error) {
	h, err := l.scan()
	if err != nil {
		return nil, err
	}
	return h.releases, nil
}

// A history is the releases of a deploy directory at one time.
type history struct {
	// releases are sorted by ID: oldest first.
	releases []Release
	// current is the index of the current release, or -1 if current does not
	// point to a release.
	current int
	// newest is the last name of releases that is the name of a release, or
	// "" without one. It may be the name of a file that is not a release (not
	// a directory), which the next release must not take either.
	newest string
}

// find returns the index of the release id, or -1.
func (h *history) find(id string) int {
	for i, r := range h.releases {
		if r.ID == id {
			return i
		}
	}
	return -1
}

// scan reads the releases and the current link. Only the directories of
// releases named like a release are releases: capze never touches the other
// files someone may keep there.
func (l *Layout) scan() (*history, error) {
	entries, err := os.ReadDir(l.releasesPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	h := &history{current: -1}
	// os.ReadDir sorts the entries by name, and so by time of deployment.
	for _, e := range entries {
		if !validID(e.Name()) {
			continue
		}
		h.newest = e.Name()
		if e.IsDir() {
			h.releases = append(h.releases, Release{ID: e.Name(), Path: filepath.Join(l.releasesPath(), e.Name())})
		}
	}
	h.current = h.find(l.currentID())
	if h.current >= 0 {
		h.releases[h.current].State = Current
		for i := h.current + 1; i < len(h.releases); i++ {
			h.releases[i].State = RolledBack
		}
	}
	return h, nil
}

// currentID returns the name of the entry of releases that current leads
// into, or "" if it leads elsewhere or nowhere.
//
// Whatever the link is made of (relative, absolute, through another link),
// the current release is the directory it ends in. A link made by hand to a
// directory inside a release counts too: pruning must not delete that
// release.
func (l *Layout) currentID() string {
	target, err := filepath.EvalSymlinks(l.currentPath())
	if err != nil {
		return ""
	}
	releases, err := filepath.EvalSymlinks(l.releasesPath())
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(releases, target)
	if err != nil {
		return ""
	}
	id, _, _ := strings.Cut(rel, string(filepath.Separator))
	return id
}

// lock takes the lock of the deploy directory, which the commands that change
// it hold from their first look at the releases to their last change.
func (l *Layout) lock(ctx context.Context) (unlock func(), err error) {
	if err := os.MkdirAll(l.statePath(), 0o755); err != nil {
		return nil, err
	}
	return fsutil.Lock(ctx, filepath.Join(l.statePath(), lockName), l.Waiting)
}

// checkCurrentLink fails if current exists and is not a symbolic link, which
// a switch could not replace.
func (l *Layout) checkCurrentLink() error {
	info, err := os.Lstat(l.currentPath())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%s exists and is not a symbolic link", l.currentPath())
	}
	return nil
}

// switchTo points current to the release id. The link is relative, so that
// the deploy directory can be moved, or seen under another path (a mount, a
// chroot). It is created aside and renamed over the old one: at every
// instant, current points to the old release or to the new one. After an
// error, it still points to the old one.
func (l *Layout) switchTo(id string) error {
	next := filepath.Join(l.statePath(), nextLinkName)
	if err := os.Remove(next); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// The link is read where it ends up, in the deploy directory.
	if err := os.Symlink(filepath.Join(releasesName, id), next); err != nil {
		return err
	}
	if err := os.Rename(next, l.currentPath()); err != nil {
		_ = os.Remove(next)
		return err
	}
	return nil
}

// syncSwitch flushes the new current link to disk. An error tells that
// current points to the release id, which a crash may still undo.
func (l *Layout) syncSwitch(id string) error {
	if err := fsutil.SyncDir(l.dir); err != nil {
		return fmt.Errorf("%s points to %s, but a crash may undo the switch: %w", l.currentPath(), id, err)
	}
	return nil
}

// discard moves the release out of releases, into the trash, where pruning
// deletes it. It returns the new path of the release.
func (l *Layout) discard(r Release) (string, error) {
	// Only the user of capze needs the trash: a release keeps there the
	// files, setuid programs included, that the permissions of releases may
	// have protected.
	if err := os.MkdirAll(l.trashPath(), 0o700); err != nil {
		return "", err
	}
	for n := 0; ; n++ {
		name := r.ID
		if n > 0 {
			// A directory left in the trash by hand has the name.
			name = fmt.Sprintf("%s.%d", r.ID, n)
		}
		dst := filepath.Join(l.trashPath(), name)
		if _, err := os.Lstat(dst); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if err := os.Rename(r.Path, dst); err != nil {
			return "", err
		}
		return dst, nil
	}
}
