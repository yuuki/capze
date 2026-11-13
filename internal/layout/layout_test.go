package layout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yuuki/capze/internal/fsutil"
)

// The time of the first deployment of the tests.
var epoch = time.Date(2026, 11, 13, 10, 15, 0, 0, time.UTC)

// A fixture is a deploy directory, and a directory of the same file system
// to prepare the releases in.
type fixture struct {
	t *testing.T
	*Layout
	root string
	// deployed counts the deployments: the clock gives each one its own
	// minute.
	deployed int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "app")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, Layout: l, root: root}
	l.Now = func() time.Time { return epoch.Add(time.Duration(f.deployed) * time.Minute) }
	return f
}

// source returns a new directory to deploy, which holds the file "version"
// with the given content.
func (f *fixture) source(version string) string {
	f.t.Helper()
	dir, err := os.MkdirTemp(f.root, "source-")
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "version"), []byte(version), 0o644); err != nil {
		f.t.Fatal(err)
	}
	return dir
}

// deploy deploys a new release and returns its name.
func (f *fixture) deploy(version string) string {
	f.t.Helper()
	r, err := f.Deploy(f.t.Context(), f.source(version), false)
	if err != nil {
		f.t.Fatal(err)
	}
	f.deployed++
	return r.ID
}

func (f *fixture) rollback(to string) string {
	f.t.Helper()
	r, err := f.Rollback(f.t.Context(), to)
	if err != nil {
		f.t.Fatal(err)
	}
	return r.ID
}

// prune prunes and returns what was deleted.
func (f *fixture) prune(keep int) []string {
	f.t.Helper()
	var removed []string
	if err := f.Prune(f.t.Context(), keep, func(path string) { removed = append(removed, path) }); err != nil {
		f.t.Fatal(err)
	}
	return removed
}

// history returns the releases as "ID", "ID=current" or "ID=rolled-back".
func (f *fixture) history() []string {
	f.t.Helper()
	releases, err := f.Releases()
	if err != nil {
		f.t.Fatal(err)
	}
	var got []string
	for _, r := range releases {
		if r.Path != filepath.Join(f.Dir(), "releases", r.ID) {
			f.t.Errorf("release %s has path %s", r.ID, r.Path)
		}
		if s := r.State.String(); s != "" {
			got = append(got, r.ID+"="+s)
		} else {
			got = append(got, r.ID)
		}
	}
	return got
}

// wantHistory checks the releases, and that current leads to the version
// that the current release holds.
func (f *fixture) wantHistory(want ...string) {
	f.t.Helper()
	if got := f.history(); !slices.Equal(got, want) {
		f.t.Errorf("releases = %v, want %v", got, want)
	}
}

// currentVersion returns the content of current/version.
func (f *fixture) currentVersion() string {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.Dir(), "current", "version"))
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *fixture) trash() []string {
	f.t.Helper()
	entries, err := os.ReadDir(f.trashPath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		f.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func (f *fixture) releasePaths(ids ...string) []string {
	var paths []string
	for _, id := range ids {
		paths = append(paths, filepath.Join(f.Dir(), "releases", id))
	}
	return paths
}

func TestOpen(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Join(root, "missing"), file, ""} {
		if _, err := Open(dir); err == nil {
			t.Errorf("Open(%s) succeeded", dir)
		}
	}
	// After a symbolic link, ".." leads to the parent of its target, not to
	// the directory of the link: such a path is refused rather than guessed.
	for _, dir := range []string{"app", "sub/app", "sub/inner"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink("sub/inner", link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link + "/../app"); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("Open of a path with .. after a symbolic link = %v", err)
	}
	if _, err := Open(filepath.Join(root, "missing") + "/.."); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("Open of a path through a missing directory = %v", err)
	}
	// Without a link on the way, a path with ".." is what it seems to be,
	// and a link to the deploy directory is as good as its path.
	for _, dir := range []string{filepath.Join(root, "app") + "/../app/.", link} {
		if _, err := Open(dir); err != nil {
			t.Errorf("Open(%s): %v", dir, err)
		}
	}
	for _, name := range []string{"link", "app", "sub"} {
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}

	// Nothing is created before the first deployment.
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if releases, err := l.Releases(); err != nil || len(releases) != 0 {
		t.Errorf("Releases = %v, %v, want none", releases, err)
	}
	if plan, err := l.Plan(1); err != nil || len(plan) != 0 {
		t.Errorf("Plan = %v, %v, want nothing", plan, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 1 {
		t.Errorf("reading the deploy directory created files: %v", entries)
	}
}

func TestDeploy(t *testing.T) {
	f := newFixture(t)
	src := f.source("v1")
	r, err := f.Deploy(t.Context(), src, true)
	if err != nil {
		t.Fatal(err)
	}
	want := Release{ID: "20261113101500", Path: filepath.Join(f.Dir(), "releases", "20261113101500"), State: Current}
	if r != want {
		t.Errorf("Deploy = %+v, want %+v", r, want)
	}
	if _, err := os.Lstat(src); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the source is still there: %v", err)
	}
	// A relative link: the deploy directory can be moved.
	if target, err := os.Readlink(filepath.Join(f.Dir(), "current")); err != nil || target != "releases/20261113101500" {
		t.Errorf("current -> %q, %v, want releases/20261113101500", target, err)
	}
	if got := f.currentVersion(); got != "v1" {
		t.Errorf("current/version = %q, want v1", got)
	}
	f.wantHistory("20261113101500=current")

	moved := filepath.Join(f.root, "moved")
	if err := os.Rename(f.Dir(), moved); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(moved, "current", "version")); err != nil || string(data) != "v1" {
		t.Errorf("current/version of the moved deploy directory = %q, %v", data, err)
	}
}

func TestDeployOrder(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
	f.wantHistory(a, b, c+"=current")
	if got := f.currentVersion(); got != "c" {
		t.Errorf("current/version = %q, want c", got)
	}
}

// The names of the releases follow the order of the deployments, whatever
// the clock says.
func TestDeployNamesWhenClockDoesNotAdvance(t *testing.T) {
	f := newFixture(t)
	f.Now = func() time.Time { return epoch }
	for _, v := range []string{"a", "b", "c"} {
		f.deploy(v)
	}
	// Set back by a year.
	f.Now = func() time.Time { return epoch.AddDate(-1, 0, 0) }
	f.deploy("d")
	f.wantHistory("20261113101500", "20261113101501", "20261113101502", "20261113101503=current")
	if got := f.currentVersion(); got != "d" {
		t.Errorf("current/version = %q, want d", got)
	}

	// A file that has the next name is not a release, and is not replaced.
	taken := filepath.Join(f.Dir(), "releases", "20261113101504")
	if err := os.WriteFile(taken, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.deploy("e")
	f.wantHistory("20261113101500", "20261113101501", "20261113101502", "20261113101503", "20261113101505=current")
	if data, err := os.ReadFile(taken); err != nil || string(data) != "mine" {
		t.Errorf("releases/20261113101504 = %q, %v, want the file left as it was", data, err)
	}
}

func TestDeployRefusesSource(t *testing.T) {
	f := newFixture(t)
	id := f.deploy("v1")

	file := filepath.Join(f.root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.root, "link")
	if err := os.Symlink(f.source("linked"), link); err != nil {
		t.Fatal(err)
	}
	// deep/.. is the directory sub for the kernel, and the root otherwise:
	// both have a directory other.
	for _, dir := range []string{"other", "sub/other", "sub/inner"} {
		if err := os.MkdirAll(filepath.Join(f.root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	deep := filepath.Join(f.root, "deep")
	if err := os.Symlink("sub/inner", deep); err != nil {
		t.Fatal(err)
	}
	// The deploy directory under another name.
	alias := filepath.Join(f.root, "alias")
	if err := os.Symlink(f.Dir(), alias); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, source, want string
	}{
		{"empty", "", "is empty"},
		{"missing", filepath.Join(f.root, "missing"), "does not exist"},
		{"through a missing directory", filepath.Join(f.root, "missing") + "/..", "does not exist"},
		{".. after a symbolic link", deep + "/../other", "symbolic link"},
		{"file", file, "not a directory"},
		{"symbolic link", link, "not a directory"},
		{"deploy directory", f.Dir(), "contains the deploy directory"},
		{"parent of the deploy directory", f.root, "contains the deploy directory"},
		{"releases", filepath.Join(f.Dir(), "releases"), "already belongs"},
		{"a release", filepath.Join(f.Dir(), "releases", id), "already belongs"},
		{"a release through a link", filepath.Join(alias, "releases", id), "already belongs"},
		{"state", filepath.Join(f.Dir(), ".capze"), "already belongs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.Deploy(t.Context(), tt.source, false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Deploy(%s) = %v, want an error with %q", tt.source, err, tt.want)
			}
		})
	}
	f.wantHistory(id + "=current")

	// A directory of the deploy directory itself is a good place to prepare
	// a release: it is on the right file system.
	inside := filepath.Join(f.Dir(), "incoming")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	f.deployed++
	if _, err := f.Deploy(t.Context(), inside, false); err != nil {
		t.Errorf("Deploy of a directory of the deploy directory: %v", err)
	}
}

func TestDeployFromOtherFileSystem(t *testing.T) {
	f := newFixture(t)
	old := f.deploy("old")
	other, err := os.MkdirTemp("/dev/shm", "capze-test-")
	if err != nil {
		t.Skipf("no other file system: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(other) })
	if same, err := fsutil.SameDevice(other, f.Dir()); err != nil || same {
		t.Skipf("no other file system: SameDevice = %v, %v", same, err)
	}
	if err := os.WriteFile(filepath.Join(other, "version"), []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = f.Deploy(t.Context(), other, true)
	if err == nil || !strings.Contains(err.Error(), "not on the file system") {
		t.Fatalf("Deploy = %v, want a file system error", err)
	}
	// Nothing changed, on either side.
	if _, err := os.Stat(filepath.Join(other, "version")); err != nil {
		t.Errorf("the source was changed: %v", err)
	}
	f.wantHistory(old + "=current")
}

func TestDeployNeedsSymbolicLink(t *testing.T) {
	f := newFixture(t)
	if err := os.Mkdir(filepath.Join(f.Dir(), "current"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := f.source("v1")
	_, err := f.Deploy(t.Context(), src, false)
	if err == nil || !strings.Contains(err.Error(), "not a symbolic link") {
		t.Fatalf("Deploy = %v, want a symbolic link error", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source was moved: %v", err)
	}
	f.wantHistory()
	if _, err := f.Rollback(t.Context(), ""); err == nil {
		t.Error("Rollback succeeded")
	}
}

// Links made by hand, or by capze 0.2, are absolute.
func TestCurrentFoundThroughAnyLink(t *testing.T) {
	f := newFixture(t)
	a, b := f.deploy("a"), f.deploy("b")
	current := filepath.Join(f.Dir(), "current")
	relink := func(target string) {
		t.Helper()
		if err := os.Remove(current); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, current); err != nil {
			t.Fatal(err)
		}
	}

	relink(filepath.Join(f.Dir(), "releases", a))
	f.wantHistory(a+"=current", b+"=rolled-back")

	relink("releases/" + b + "/")
	f.wantHistory(a, b+"=current")

	// Not a release: there is no current release.
	relink("releases")
	f.wantHistory(a, b)
	relink("releases/20200101000000")
	f.wantHistory(a, b)
	relink(f.root)
	f.wantHistory(a, b)

	// Into a release: that release is in use.
	relink(filepath.Join("releases", a, "version"))
	f.wantHistory(a+"=current", b+"=rolled-back")
	if got := f.prune(1); len(got) != 0 {
		t.Errorf("Prune deleted %v", got)
	}
	relink("releases/" + b)

	// A deployment replaces whatever link is there.
	c := f.deploy("c")
	f.wantHistory(a, b, c+"=current")
	if target, _ := os.Readlink(current); target != "releases/"+c {
		t.Errorf("current -> %q, want releases/%s", target, c)
	}
}

func TestRollback(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")

	if got := f.rollback(""); got != b {
		t.Errorf("Rollback = %s, want %s", got, b)
	}
	f.wantHistory(a, b+"=current", c+"=rolled-back")
	if got := f.currentVersion(); got != "b" {
		t.Errorf("current/version = %q, want b", got)
	}

	// Again: one release further back.
	if got := f.rollback(""); got != a {
		t.Errorf("Rollback = %s, want %s", got, a)
	}
	f.wantHistory(a+"=current", b+"=rolled-back", c+"=rolled-back")

	_, err := f.Rollback(t.Context(), "")
	if err == nil || !strings.Contains(err.Error(), "no release older") {
		t.Errorf("Rollback from the oldest release = %v", err)
	}
	f.wantHistory(a+"=current", b+"=rolled-back", c+"=rolled-back")

	// A rollback is undone by name. Nothing was deleted.
	if got := f.rollback(c); got != c {
		t.Errorf("Rollback to %s = %s", c, got)
	}
	f.wantHistory(a, b, c+"=current")
	// To the current release: nothing to do.
	if got := f.rollback(c); got != c {
		t.Errorf("Rollback to %s = %s", c, got)
	}
	if got := f.trash(); len(got) != 0 {
		t.Errorf("trash = %v, want nothing", got)
	}
}

func TestRollbackErrors(t *testing.T) {
	f := newFixture(t)
	if _, err := f.Rollback(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "no release in") {
		t.Errorf("Rollback without releases = %v", err)
	}
	a, b := f.deploy("a"), f.deploy("b")

	for _, to := range []string{"20200101000000", "v1", "../" + a, filepath.Join(f.Dir(), "releases", a)} {
		if _, err := f.Rollback(t.Context(), to); err == nil {
			t.Errorf("Rollback to %q succeeded", to)
		}
	}
	f.wantHistory(a, b+"=current")

	// Without a current release, only a name tells where to go.
	if err := os.Remove(filepath.Join(f.Dir(), "current")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Rollback(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "--to") {
		t.Errorf("Rollback without a current release = %v", err)
	}
	if got := f.rollback(a); got != a {
		t.Errorf("Rollback to %s = %s", a, got)
	}
	f.wantHistory(a+"=current", b+"=rolled-back")
}

// The history is linear: a deployment discards the releases that were rolled
// back, so that the next rollback returns to the release that was current
// before, not to one that was given up.
func TestDeployDiscardsRolledBackReleases(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
	f.rollback(a)
	f.wantHistory(a+"=current", b+"=rolled-back", c+"=rolled-back")

	d := f.deploy("d")
	f.wantHistory(a, d+"=current")
	if got := f.trash(); !slices.Equal(got, []string{b, c}) {
		t.Errorf("trash = %v, want %v", got, []string{b, c})
	}
	if got := f.rollback(""); got != a {
		t.Errorf("Rollback = %s, want %s", got, a)
	}

	// Pruning empties the trash, even when it keeps every release.
	want := []string{filepath.Join(f.trashPath(), b), filepath.Join(f.trashPath(), c)}
	if plan, err := f.Plan(DefaultKeep); err != nil || !slices.Equal(plan, want) {
		t.Errorf("Plan = %v, %v, want %v", plan, err, want)
	}
	if got := f.prune(DefaultKeep); !slices.Equal(got, want) {
		t.Errorf("Prune deleted %v, want %v", got, want)
	}
	if got := f.trash(); len(got) != 0 {
		t.Errorf("trash = %v, want nothing", got)
	}
	f.wantHistory(a+"=current", d+"=rolled-back")
}

func TestPrune(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for _, v := range []string{"a", "b", "c", "d", "e"} {
		ids = append(ids, f.deploy(v))
	}
	// Files of the user in releases are not releases.
	foreign := []string{"README", "shared", "20261113101500.bak", "2026111310150"}
	for _, name := range foreign {
		if err := os.Mkdir(filepath.Join(f.Dir(), "releases", name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(ids[4], filepath.Join(f.Dir(), "releases", "20270101000000")); err != nil {
		t.Fatal(err)
	}

	if plan, err := f.Plan(5); err != nil || len(plan) != 0 {
		t.Errorf("Plan(5) = %v, %v, want nothing", plan, err)
	}
	if got := f.prune(5); len(got) != 0 {
		t.Errorf("Prune(5) deleted %v", got)
	}

	want := f.releasePaths(ids[:3]...)
	plan, err := f.Plan(2)
	if err != nil || !slices.Equal(plan, want) {
		t.Errorf("Plan(2) = %v, %v, want %v", plan, err, want)
	}
	f.wantHistory(ids[0], ids[1], ids[2], ids[3], ids[4]+"=current")
	// Prune deletes what Plan told, in that order.
	if got := f.prune(2); !slices.Equal(got, want) {
		t.Errorf("Prune(2) deleted %v, want %v", got, want)
	}
	f.wantHistory(ids[3], ids[4]+"=current")
	for _, path := range want {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there: %v", path, err)
		}
	}
	if got := f.trash(); len(got) != 0 {
		t.Errorf("trash = %v, want nothing", got)
	}
	for _, name := range append(foreign, "20270101000000") {
		if _, err := os.Lstat(filepath.Join(f.Dir(), "releases", name)); err != nil {
			t.Errorf("releases/%s, which is not a release, was deleted: %v", name, err)
		}
	}

	if got := f.prune(1); !slices.Equal(got, f.releasePaths(ids[3])) {
		t.Errorf("Prune(1) deleted %v", got)
	}
	f.wantHistory(ids[4] + "=current")

	for _, keep := range []int{0, -1} {
		if err := f.Prune(t.Context(), keep, nil); err == nil {
			t.Errorf("Prune(%d) succeeded", keep)
		}
		if _, err := f.Plan(keep); err == nil {
			t.Errorf("Plan(%d) succeeded", keep)
		}
	}
	f.wantHistory(ids[4] + "=current")
}

// The current release and the newer ones are what a rollback can choose:
// pruning counts back from the current release and leaves the newer ones.
func TestPruneAfterRollback(t *testing.T) {
	f := newFixture(t)
	var ids []string
	for _, v := range []string{"a", "b", "c", "d", "e"} {
		ids = append(ids, f.deploy(v))
	}
	f.rollback(ids[2])

	if got := f.prune(2); !slices.Equal(got, f.releasePaths(ids[0])) {
		t.Errorf("Prune(2) deleted %v, want %v", got, f.releasePaths(ids[0]))
	}
	f.wantHistory(ids[1], ids[2]+"=current", ids[3]+"=rolled-back", ids[4]+"=rolled-back")
	if got := f.prune(1); !slices.Equal(got, f.releasePaths(ids[1])) {
		t.Errorf("Prune(1) deleted %v, want %v", got, f.releasePaths(ids[1]))
	}
	f.wantHistory(ids[2]+"=current", ids[3]+"=rolled-back", ids[4]+"=rolled-back")
}

func TestPruneWithoutCurrent(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
	if err := os.Remove(filepath.Join(f.Dir(), "current")); err != nil {
		t.Fatal(err)
	}
	if got := f.prune(2); !slices.Equal(got, f.releasePaths(a)) {
		t.Errorf("Prune(2) deleted %v, want %v", got, f.releasePaths(a))
	}
	f.wantHistory(b, c)
}

// What is in the trash is deleted whatever it is, and a name taken there
// does not stop a release from being discarded.
func TestPruneEmptiesTrash(t *testing.T) {
	f := newFixture(t)
	a, b := f.deploy("a"), f.deploy("b")
	leftover := filepath.Join(f.trashPath(), a)
	if err := os.MkdirAll(filepath.Join(leftover, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.trashPath(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	want := []string{leftover, file, filepath.Join(f.Dir(), "releases", a)}
	if plan, err := f.Plan(1); err != nil || !slices.Equal(plan, want) {
		t.Errorf("Plan = %v, %v, want %v", plan, err, want)
	}
	if got := f.prune(1); !slices.Equal(got, want) {
		t.Errorf("Prune deleted %v, want %v", got, want)
	}
	if got := f.trash(); len(got) != 0 {
		t.Errorf("trash = %v, want nothing", got)
	}
	f.wantHistory(b + "=current")
}

func TestPruneStopsWhenCancelled(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
	ctx, cancel := context.WithCancel(t.Context())
	var removed []string
	err := f.Prune(ctx, 1, func(path string) {
		removed = append(removed, path)
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Prune = %v, want context.Canceled", err)
	}
	if !slices.Equal(removed, f.releasePaths(a)) {
		t.Errorf("Prune deleted %v, want %v", removed, f.releasePaths(a))
	}
	// What was not deleted is no longer a release, and the next Prune
	// deletes it.
	f.wantHistory(c + "=current")
	if got := f.trash(); !slices.Equal(got, []string{b}) {
		t.Errorf("trash = %v, want %v", got, []string{b})
	}
	if got := f.prune(1); !slices.Equal(got, []string{filepath.Join(f.trashPath(), b)}) {
		t.Errorf("the next Prune deleted %v", got)
	}
}

// Deployments at the same time each get their own release, in some order.
func TestConcurrentDeployments(t *testing.T) {
	f := newFixture(t)
	f.Now = func() time.Time { return epoch }
	const n = 8
	sources := make([]string, n)
	for i := range sources {
		sources[i] = f.source(string(rune('a' + i)))
	}
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i, src := range sources {
		wg.Go(func() {
			l, err := Open(f.Dir())
			if err != nil {
				t.Error(err)
				return
			}
			l.Now = f.Now
			r, err := l.Deploy(t.Context(), src, false)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = r.ID
		})
	}
	wg.Wait()

	slices.Sort(ids)
	if got := slices.Compact(slices.Clone(ids)); len(got) != n {
		t.Fatalf("%d deployments made the releases %v", n, ids)
	}
	want := slices.Clone(ids)
	want[n-1] += "=current"
	f.wantHistory(want...)
}

// A command waits for the one that holds the lock, as long as its context
// lets it.
func TestCommandsWaitForLock(t *testing.T) {
	f := newFixture(t)
	a := f.deploy("a")
	unlock, err := f.lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	waits := 0
	f.Waiting = func() { waits++ }
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	src := f.source("b")
	if _, err := f.Deploy(ctx, src, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Deploy = %v, want context.DeadlineExceeded", err)
	}
	if waits != 1 {
		t.Errorf("Waiting was called %d times, want 1", waits)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source was moved: %v", err)
	}
	f.wantHistory(a + "=current")
}

func TestDeployCancelled(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	src := f.source("a")
	if _, err := f.Deploy(ctx, src, false); !errors.Is(err, context.Canceled) {
		t.Errorf("Deploy = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source was moved: %v", err)
	}
	f.wantHistory()
}
