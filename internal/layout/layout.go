// Package layout manages a deploy directory, laid out like those of
// Capistrano:
//
//	DEPLOY_DIR
//	├── current -> releases/20261113101500
//	├── releases
//	│   ├── 20261112093000
//	│   └── 20261113101500
//	└── .capze
//	    └── trash
//
// Each directory of releases is one release, named after the time it was
// deployed, and current is a symbolic link to the release in use. .capze
// belongs to capze: the commands that change the deploy directory lock it,
// and its trash holds the releases that are being deleted.
//
// The package keeps these promises, which docs/design.md explains:
//
//   - A release appears in releases, and leaves it, by one rename(2): the
//     directory never holds a release that is half copied or half deleted.
//   - current changes by one rename(2): it always points to a whole release.
//   - The names of the releases sort in the order of their deployments.
//   - The releases form one history, whose head is current: a deployment
//     discards the releases newer than current, which were rolled back.
//   - Nothing is deleted through a mount point, nor outside the deploy
//     directory: releases, .capze and its trash are directories of their
//     own, on the mount of the deploy directory, and capze works in them
//     through their file descriptors, not their paths.
package layout

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/yuuki/capze/internal/fsutil"
)

// The names of the files of a deploy directory.
const (
	currentName  = "current"
	releasesName = "releases"
	stateName    = ".capze"
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
	// Warn, if not nil, is called with what went wrong after a command did
	// what it was asked: the command still succeeds.
	Warn func(message string)
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

// absolute returns the absolute path of the existing file path.
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
	// Both through a symbolic link at the end, which filepath.Abs keeps
	// unless a "/" follows it.
	asGiven, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	cleaned, err := os.Stat(abs)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err != nil || !os.SameFile(asGiven, cleaned) {
		return "", fmt.Errorf("%s is not %s, because of a symbolic link before a \"..\": write the path without \"..\"", path, abs)
	}
	return abs, nil
}

// Dir returns the absolute path of the deploy directory.
func (l *Layout) Dir() string { return l.dir }

func (l *Layout) currentPath() string  { return filepath.Join(l.dir, currentName) }
func (l *Layout) releasesPath() string { return filepath.Join(l.dir, releasesName) }
func (l *Layout) trashPath() string    { return filepath.Join(l.dir, stateName, trashName) }

func (l *Layout) warn(format string, args ...any) {
	if l.Warn != nil {
		l.Warn(fmt.Sprintf(format, args...))
	}
}

// handles are the open directories of a deploy directory. Those that do not
// exist are nil.
//
// Every change goes through them: an entry is renamed, linked or removed by
// its name in an open directory. So nothing that is done to the paths while
// a command runs, and no symbolic link or mount found in place of one of
// these directories, can make capze work outside the deploy directory.
type handles struct {
	root, releases, state, trash *fsutil.Dir
}

func (h *handles) close() {
	h.root.Close()
	h.releases.Close()
	h.state.Close()
	h.trash.Close()
}

// open opens the directories of the deploy directory that exist. It fails if
// one of them is not a directory of its own on the mount of the deploy
// directory: a symbolic link or a mount point there would lead the releases,
// or their deletion, somewhere else.
func (l *Layout) open() (*handles, error) {
	root, err := fsutil.OpenDir(l.dir)
	if err != nil {
		return nil, err
	}
	h := &handles{root: root}
	if h.releases, err = sub(h.root, releasesName); err == nil {
		h.state, err = sub(h.root, stateName)
	}
	if err == nil && h.state != nil {
		h.trash, err = sub(h.state, trashName)
	}
	if err != nil {
		h.close()
		return nil, err
	}
	return h, nil
}

// sub opens the directory name of dir, or returns nil if it does not exist.
func sub(dir *fsutil.Dir, name string) (*fsutil.Dir, error) {
	d, err := dir.Sub(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return d, plainDir(err)
}

// mkSub opens the directory name of dir, which it creates if needed.
func mkSub(dir *fsutil.Dir, name string, perm uint32) (*fsutil.Dir, error) {
	d, err := dir.MkSub(name, perm)
	return d, plainDir(err)
}

// plainDir explains the error of a directory of the layout that is not a
// directory of its own.
func plainDir(err error) error {
	var notDir *fsutil.NotPlainDirError
	var mount *fsutil.MountPointError
	switch {
	case errors.As(err, &notDir):
		return fmt.Errorf("%w: capze does not follow it", err)
	case errors.As(err, &mount):
		return fmt.Errorf("%s is on another mount than the deploy directory, which must be one file system", mount.Path)
	}
	return err
}

// ensureState creates .capze, which the commands that change the deploy
// directory lock.
func (h *handles) ensureState() (err error) {
	if h.state == nil {
		h.state, err = mkSub(h.root, stateName, 0o755)
	}
	return err
}

// ensureTrash creates the trash.
//
// Only the user of capze can enter it: a release keeps there the files,
// setuid programs included, that the permissions of releases may have
// protected.
func (h *handles) ensureTrash() (err error) {
	if err := h.ensureState(); err != nil {
		return err
	}
	if h.trash == nil {
		h.trash, err = mkSub(h.state, trashName, 0o700)
	}
	return err
}

// Releases returns the releases of the deploy directory, oldest first.
func (l *Layout) Releases() ([]Release, error) {
	h, err := l.open()
	if err != nil {
		return nil, err
	}
	defer h.close()
	hist, err := l.scan(h)
	if err != nil {
		return nil, err
	}
	return hist.releases, nil
}

// A history is the releases of a deploy directory at one time.
type history struct {
	// releases are sorted by ID: oldest first.
	releases []Release
	// current is the index of the current release, or -1 if current does not
	// lead to a release.
	current int
	// strayCurrent tells that current exists and does not lead to a release:
	// which release is in use is then unknown.
	strayCurrent bool
	// currentIsLink tells that current exists and is a symbolic link.
	currentIsLink bool
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
func (l *Layout) scan(h *handles) (*history, error) {
	hist := &history{current: -1}
	var files []fsutil.FileID
	if h.releases != nil {
		// Sorted by name, and so by time of deployment.
		names, err := h.releases.Names()
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if !validID(name) {
				continue
			}
			st, err := h.releases.Lstat(name)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			hist.newest = name
			if st.IsDir {
				hist.releases = append(hist.releases, Release{ID: name, Path: filepath.Join(l.releasesPath(), name)})
				files = append(files, st.ID)
			}
		}
	}

	link, err := h.root.Lstat(currentName)
	if errors.Is(err, fs.ErrNotExist) {
		return hist, nil
	}
	if err != nil {
		return nil, err
	}
	hist.currentIsLink = link.IsSymlink
	hist.current = currentRelease(h, files)
	hist.strayCurrent = hist.current < 0
	if hist.current >= 0 {
		hist.releases[hist.current].State = Current
		for i := hist.current + 1; i < len(hist.releases); i++ {
			hist.releases[i].State = RolledBack
		}
	}
	return hist, nil
}

// currentRelease returns the index of the release, among those that files
// identifies, that current leads into, or -1 if it leads elsewhere or
// nowhere.
//
// Whatever the link is made of (relative, absolute, through another link or
// another path to the deploy directory), the current release is the
// directory it ends in, so the directories themselves are compared, not
// their paths. A link made by hand to a directory inside a release counts
// too: pruning must not delete that release.
func currentRelease(h *handles, files []fsutil.FileID) int {
	target, err := h.root.Follow(currentName)
	if err != nil {
		return -1
	}
	defer target.Close()
	ancestors, err := target.Ancestors()
	if err != nil {
		return -1
	}
	for i, file := range files {
		if slices.Contains(ancestors, file) {
			return i
		}
	}
	return -1
}

// checkCurrentLink fails if current exists and is not a symbolic link: a
// switch could not replace it, and it tells nothing of the release in use.
func (l *Layout) checkCurrentLink(hist *history) error {
	if hist.strayCurrent && !hist.currentIsLink {
		return fmt.Errorf("%s exists and is not a symbolic link", l.currentPath())
	}
	return nil
}

// prepareSwitch creates, aside, the link that commitSwitch renames over
// current. The link is relative, so that the deploy directory can be moved,
// or seen under another path (a mount, a chroot).
//
// Creating the link is the step of a switch that can fail for lack of space:
// it comes before the changes that the switch completes.
func (h *handles) prepareSwitch(id string) error {
	if err := h.state.Unlink(nextLinkName); err != nil {
		return err
	}
	// The link is read where it ends up, in the deploy directory.
	return h.state.Symlink(filepath.Join(releasesName, id), nextLinkName)
}

// cancelSwitch removes the link of prepareSwitch.
func (h *handles) cancelSwitch() {
	_ = h.state.Unlink(nextLinkName)
}

// commitSwitch points current to the release of prepareSwitch: at every
// instant, current points to the old release or to the new one. After an
// error, it still points to the old one.
func (l *Layout) commitSwitch(h *handles) error {
	if err := h.state.Rename(nextLinkName, h.root, currentName); err != nil {
		h.cancelSwitch()
		return underlying(err)
	}
	// The switch is done: what is left is to make it survive a crash.
	if err := h.root.Sync(); err != nil {
		l.warn("%s is switched, but a crash may undo the switch: %v", l.currentPath(), err)
	}
	return nil
}

// discard moves the release out of releases, into the trash, where pruning
// deletes it. It returns the name of the release in the trash.
func (h *handles) discard(r Release) (string, error) {
	if err := h.ensureTrash(); err != nil {
		return "", err
	}
	for n := 0; ; n++ {
		name := r.ID
		if n > 0 {
			// A directory left in the trash by hand has the name.
			name = fmt.Sprintf("%s.%d", r.ID, n)
		}
		if _, err := h.trash.Lstat(name); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if err := h.releases.Rename(r.ID, h.trash, name); err != nil {
			return "", underlying(err)
		}
		return name, nil
	}
}

// underlying returns the cause of an error of a rename, whose message
// otherwise repeats both paths.
func underlying(err error) error {
	var link *os.LinkError
	if errors.As(err, &link) {
		return link.Err
	}
	return err
}
