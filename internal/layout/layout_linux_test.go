package layout

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/yuuki/capze/internal/fsutil"
)

// A release that a container still uses has bind mounts of host directories
// below it. Pruning takes it out of releases, deletes nothing through the
// mount, and finishes once the mount is gone.
func TestPruneLeavesMountedReleaseInTrash(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("mounting needs root")
	}
	f := newFixture(t)
	// Only a directory of the test is mounted: nothing else can be lost.
	host := filepath.Join(f.root, "host")
	if err := os.Mkdir(host, 0o755); err != nil {
		t.Fatal(err)
	}
	precious := filepath.Join(host, "precious")
	if err := os.WriteFile(precious, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	src := f.source("a")
	if err := os.Mkdir(filepath.Join(src, "log"), 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := f.Deploy(t.Context(), src, false)
	if err != nil {
		t.Fatal(err)
	}
	f.deployed++
	a := r.ID
	if err := unix.Mount(host, filepath.Join(r.Path, "log"), "", unix.MS_BIND, ""); err != nil {
		t.Skipf("cannot mount here: %v", err)
	}
	trashed := filepath.Join(f.trashPath(), a)
	mounted := true
	unmount := func() {
		if !mounted {
			return
		}
		mounted = false
		// The mount follows the release into the trash.
		if err := unix.Unmount(filepath.Join(trashed, "log"), unix.MNT_DETACH); err != nil {
			if err := unix.Unmount(filepath.Join(r.Path, "log"), unix.MNT_DETACH); err != nil {
				t.Errorf("unmount: %v", err)
			}
		}
	}
	// Registered after t.TempDir: runs before its cleanup.
	t.Cleanup(unmount)
	b, c := f.deploy("b"), f.deploy("c")

	var removed []string
	err = f.Prune(t.Context(), 1, func(path string) { removed = append(removed, path) })
	var mpe *fsutil.MountPointError
	if !errors.As(err, &mpe) || mpe.Path != filepath.Join(trashed, "log") {
		t.Fatalf("Prune = %v, want a MountPointError for %s", err, filepath.Join(trashed, "log"))
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("a file was deleted through the bind mount: %v", err)
	}
	// The other release is deleted all the same.
	if want := f.releasePaths(b); !slices.Equal(removed, want) {
		t.Errorf("Prune deleted %v, want %v", removed, want)
	}
	f.wantHistory(c + "=current")
	if plan, err := f.Plan(1); err != nil || !slices.Equal(plan, []string{trashed}) {
		t.Errorf("Plan = %v, %v, want %v", plan, err, []string{trashed})
	}

	unmount()
	if got := f.prune(1); !slices.Equal(got, []string{trashed}) {
		t.Errorf("Prune after the unmount deleted %v, want %v", got, []string{trashed})
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatalf("a file of the directory that was mounted was deleted: %v", err)
	}
}
