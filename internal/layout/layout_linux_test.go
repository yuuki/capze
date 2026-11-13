package layout

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuuki/capze/internal/fsutil"
)

// bindMount mounts src on dst, both below root, and skips the test where
// mounting is not allowed. The mount lasts until the test calls the function
// it returns with the path the mount point has by then, or ends.
//
// Only directories of the test are ever mounted: if a removal under test, or
// the cleanup of t.TempDir, went through the mount, it would delete nothing
// else.
func bindMount(t *testing.T, root, src, dst string) (unmount func(at string)) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("mounting needs root")
	}
	if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		t.Skipf("cannot mount here: %v", err)
	}
	mounted := true
	unmount = func(at string) {
		if !mounted {
			return
		}
		mounted = false
		if err := unix.Unmount(at, unix.MNT_DETACH); err != nil {
			t.Errorf("unmount %s: %v", at, err)
		}
	}
	// Registered after t.TempDir: runs before its cleanup. The mount point
	// may have moved with its release: find it again.
	t.Cleanup(func() {
		if !mounted {
			return
		}
		for _, at := range fsutil.MountsBelow(root) {
			if err := unix.Unmount(at, unix.MNT_DETACH); err != nil {
				t.Errorf("unmount %s: %v", at, err)
			}
		}
	})
	return unmount
}

// hostDir returns a directory of the test that stands for a directory of the
// host, and the path of a file in it that must survive.
func hostDir(t *testing.T, f *fixture) (dir, precious string) {
	t.Helper()
	dir = filepath.Join(f.root, "host")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	precious = filepath.Join(dir, "precious")
	if err := os.WriteFile(precious, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, precious
}

// deployWithLog deploys a release that has a directory log, to mount on.
func deployWithLog(t *testing.T, f *fixture, version string) Release {
	t.Helper()
	src := f.source(version)
	if err := os.Mkdir(filepath.Join(src, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := f.Deploy(t.Context(), src, false)
	if err != nil {
		t.Fatal(err)
	}
	f.deployed++
	return r
}

// A release that a container still uses has bind mounts of host directories
// below it. Pruning leaves it whole in releases, deletes nothing through the
// mount, and deletes it once the mount is gone.
func TestPruneLeavesMountedRelease(t *testing.T) {
	f := newFixture(t)
	host, precious := hostDir(t, f)
	a := deployWithLog(t, f, "a")
	mountPoint := filepath.Join(a.Path, "log")
	unmount := bindMount(t, f.root, host, mountPoint)
	b, c := f.deploy("b"), f.deploy("c")

	var removed []string
	err := f.Prune(t.Context(), 1, func(path string) { removed = append(removed, path) })
	var mpe *fsutil.MountPointError
	if !errors.As(err, &mpe) || mpe.Path != mountPoint {
		t.Fatalf("Prune = %v, want a MountPointError for %s", err, mountPoint)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("a file was deleted through the bind mount: %v", err)
	}
	// Nothing of the release in use is deleted, and the other one is.
	if data, err := os.ReadFile(filepath.Join(a.Path, "version")); err != nil || string(data) != "a" {
		t.Errorf("the release in use was changed: version = %q, %v", data, err)
	}
	if want := f.releasePaths(b); !slices.Equal(removed, want) {
		t.Errorf("Prune deleted %v, want %v", removed, want)
	}
	f.wantHistory(a.ID, c+"=current")
	if got := f.trash(); len(got) != 0 {
		t.Errorf("trash = %v, want nothing", got)
	}

	unmount(mountPoint)
	if got := f.prune(1); !slices.Equal(got, f.releasePaths(a.ID)) {
		t.Errorf("Prune after the unmount deleted %v, want %v", got, f.releasePaths(a.ID))
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("a file of the directory that was mounted was deleted: %v", err)
	}
	f.wantHistory(c + "=current")
}

// A rolled-back release that is still in use moves to the trash with its
// mount, and stays whole there until the mount is gone.
func TestDiscardedReleaseWithMountStaysWhole(t *testing.T) {
	f := newFixture(t)
	host, precious := hostDir(t, f)
	a := f.deploy("a")
	b := deployWithLog(t, f, "b")
	unmount := bindMount(t, f.root, host, filepath.Join(b.Path, "log"))
	f.rollback("")
	c := f.deploy("c")
	f.wantHistory(a, c+"=current")

	trashed := filepath.Join(f.trashPath(), b.ID)
	mountPoint := filepath.Join(trashed, "log")
	err := f.Prune(t.Context(), DefaultKeep, nil)
	var mpe *fsutil.MountPointError
	if !errors.As(err, &mpe) || mpe.Path != mountPoint {
		t.Fatalf("Prune = %v, want a MountPointError for %s", err, mountPoint)
	}
	if data, err := os.ReadFile(filepath.Join(trashed, "version")); err != nil || string(data) != "b" {
		t.Errorf("the release in use was changed: version = %q, %v", data, err)
	}
	if plan, err := f.Plan(DefaultKeep); err != nil || !slices.Equal(plan, []string{trashed}) {
		t.Errorf("Plan = %v, %v, want %v", plan, err, []string{trashed})
	}

	unmount(mountPoint)
	if got := f.prune(DefaultKeep); !slices.Equal(got, []string{trashed}) {
		t.Errorf("Prune after the unmount deleted %v, want %v", got, []string{trashed})
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("a file of the directory that was mounted was deleted: %v", err)
	}
}

// A source reached through another mount of the same file system has the
// device number of the deploy directory, and still cannot be renamed into
// it. The deployment fails as a whole: the releases that were rolled back
// are still there to return to.
func TestFailedDeployChangesNothing(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
	f.rollback(a)

	build := filepath.Join(f.root, "build")
	alias := filepath.Join(f.root, "alias")
	for _, dir := range []string{filepath.Join(build, "new"), alias} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	unmount := bindMount(t, f.root, build, alias)
	defer unmount(alias)
	src := filepath.Join(alias, "new")

	_, err := f.Deploy(t.Context(), src, false)
	if err == nil || !strings.Contains(err.Error(), "another mount") {
		t.Fatalf("Deploy = %v, want an error about another mount", err)
	}
	checkUnchanged(t, f, src, a+"=current", b+"=rolled-back", c+"=rolled-back")

	// A source that is a mount point cannot be renamed either.
	unmount(alias)
	mounted := filepath.Join(f.root, "mounted")
	if err := os.Mkdir(mounted, 0o755); err != nil {
		t.Fatal(err)
	}
	unmount = bindMount(t, f.root, build, mounted)
	defer unmount(mounted)
	if _, err := f.Deploy(t.Context(), mounted, false); err == nil {
		t.Fatal("Deploy of a mount point succeeded")
	}
	checkUnchanged(t, f, mounted, a+"=current", b+"=rolled-back", c+"=rolled-back")
}

// When a rolled-back release cannot be discarded (here it is a mount point),
// the deployment is undone: the source is back, and so are the rolled-back
// releases that were discarded before the failure.
func TestDeployUndoneWhenDiscardFails(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
	f.rollback(a)
	cPath := filepath.Join(f.Dir(), "releases", c)
	unmount := bindMount(t, f.root, cPath, cPath)
	defer unmount(cPath)

	src := f.source("d")
	_, err := f.Deploy(t.Context(), src, false)
	if err == nil || !strings.Contains(err.Error(), "discard the rolled-back release "+c) {
		t.Fatalf("Deploy = %v, want a failure to discard %s", err, c)
	}
	checkUnchanged(t, f, src, a+"=current", b+"=rolled-back", c+"=rolled-back")
}

// checkUnchanged checks that a failed deployment left the source, the
// releases and current as they were, and nothing behind.
func checkUnchanged(t *testing.T, f *fixture, src string, history ...string) {
	t.Helper()
	if info, err := os.Stat(src); err != nil || !info.IsDir() {
		t.Errorf("the source is no longer where it was: %v", err)
	}
	f.wantHistory(history...)
	if got := f.trash(); len(got) != 0 {
		t.Errorf("trash = %v, want nothing", got)
	}
	if _, err := os.Lstat(f.nextLink()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the link of the switch is left behind: %v", err)
	}
}

// The deploy directory seen under another path, with a current link made by
// hand through that path: the current release is still the one it leads to,
// and pruning keeps it.
func TestCurrentFoundThroughAnotherMount(t *testing.T) {
	f := newFixture(t)
	a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
	alias := filepath.Join(f.root, "alias")
	if err := os.Mkdir(alias, 0o755); err != nil {
		t.Fatal(err)
	}
	unmount := bindMount(t, f.root, f.Dir(), alias)
	defer unmount(alias)

	current := filepath.Join(f.Dir(), "current")
	if err := os.Remove(current); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(alias, "releases", a), current); err != nil {
		t.Fatal(err)
	}
	f.wantHistory(a+"=current", b+"=rolled-back", c+"=rolled-back")
	if got := f.prune(1); len(got) != 0 {
		t.Errorf("Prune deleted %v", got)
	}
	// A source in the deploy directory, named through the other path.
	if _, err := f.Deploy(t.Context(), filepath.Join(alias, "releases", b), false); err == nil || !strings.Contains(err.Error(), "already belongs") {
		t.Errorf("Deploy of a release through another mount = %v", err)
	}
	f.wantHistory(a+"=current", b+"=rolled-back", c+"=rolled-back")
}

// A file system mounted on the trash, or on .capze, is not the trash: what
// is mounted there is not deleted.
func TestPruneRefusesMountedTrash(t *testing.T) {
	for _, name := range []string{".capze", ".capze/trash"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			host, precious := hostDir(t, f)
			if err := os.MkdirAll(filepath.Join(host, "trash", "data"), 0o755); err != nil {
				t.Fatal(err)
			}
			a, b, c := f.deploy("a"), f.deploy("b"), f.deploy("c")
			mountPoint := filepath.Join(f.Dir(), name)
			unmount := bindMount(t, f.root, host, mountPoint)
			defer unmount(mountPoint)

			if err := f.Prune(t.Context(), 1, nil); err == nil || !strings.Contains(err.Error(), "another mount") {
				t.Errorf("Prune = %v, want an error about another mount", err)
			}
			if plan, err := f.Plan(1); err == nil {
				t.Errorf("Plan = %v", plan)
			}
			if _, err := f.Deploy(t.Context(), f.source("d"), false); err == nil {
				t.Error("Deploy succeeded")
			}
			for _, path := range []string{precious, filepath.Join(host, "trash", "data")} {
				if _, err := os.Stat(path); err != nil {
					t.Errorf("a file was deleted through the mount: %v", err)
				}
			}
			unmount(mountPoint)
			f.wantHistory(a, b, c+"=current")
		})
	}
}
