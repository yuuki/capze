package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestParseFdinfoMountID(t *testing.T) {
	tests := []struct {
		name string
		data string
		want uint64
		ok   bool
	}{
		{"directory", "pos:\t0\nflags:\t02200000\nmnt_id:\t29\nino:\t1234\n", 29, true},
		{"no final newline", "pos:\t0\nmnt_id:\t4294967297", 4294967297, true},
		{"spaces", "mnt_id:   7  \n", 7, true},
		{"first of two", "mnt_id:\t1\nmnt_id:\t2\n", 1, true},
		{"missing", "pos:\t0\nflags:\t02200000\n", 0, false},
		{"empty", "", 0, false},
		{"not a number", "mnt_id:\tx\n", 0, false},
		{"negative", "mnt_id:\t-1\n", 0, false},
		{"no value", "mnt_id:\n", 0, false},
		{"not at the start of a line", "x mnt_id:\t3\n", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseFdinfoMountID([]byte(tt.data))
			if got != tt.want || ok != tt.ok {
				t.Errorf("parseFdinfoMountID(%q) = %d, %v, want %d, %v", tt.data, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func FuzzParseFdinfoMountID(f *testing.F) {
	f.Add([]byte("pos:\t0\nflags:\t02200000\nmnt_id:\t29\nino:\t1234\n"))
	f.Add([]byte("mnt_id:"))
	f.Add([]byte("mnt_id:\t18446744073709551616\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		id, ok := parseFdinfoMountID(data)
		if !ok && id != 0 {
			t.Errorf("parseFdinfoMountID(%q) = %d without a mount ID", data, id)
		}
	})
}

// Both sources of the mount ID agree, so that an old kernel, which only has
// the second one, tells mount points apart the same way.
func TestMountOfAgreesWithFdinfo(t *testing.T) {
	for _, dir := range []string{t.TempDir(), "/", "/proc"} {
		fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		statx, err1 := mountOf(fd)
		fdinfo, err2 := fdinfoMountOf(fd)
		_ = unix.Close(fd)
		if err1 != nil || err2 != nil {
			t.Fatalf("%s: mountOf: %v, fdinfoMountOf: %v", dir, err1, err2)
		}
		if statx != fdinfo {
			t.Errorf("%s: mountOf = %d, fdinfoMountOf = %d", dir, statx, fdinfo)
		}
	}
}

// bindMount mounts src on dst, and skips the test where mounting is not
// allowed. The mount lasts until the test calls the function it returns, or
// ends.
//
// Only directories and files of the test are ever mounted: if the removal
// under test, or the cleanup of t.TempDir, went through the mount, it would
// delete nothing else.
func bindMount(t *testing.T, src, dst string) (unmount func()) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("mounting needs root")
	}
	if err := unix.Mount(src, dst, "", unix.MS_BIND, ""); err != nil {
		t.Skipf("cannot mount here: %v", err)
	}
	mounted := true
	unmount = func() {
		if !mounted {
			return
		}
		mounted = false
		if err := unix.Unmount(dst, unix.MNT_DETACH); err != nil {
			t.Errorf("unmount %s: %v", dst, err)
		}
	}
	// Registered after t.TempDir: runs before its cleanup.
	t.Cleanup(unmount)
	return unmount
}

// A bind mount of a directory of the same file system has the device number
// of the tree: only its mount ID tells that it is not part of it.
func TestRemoveTreeStopsAtBindMount(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host")
	mustMkdir(t, host)
	mustWrite(t, filepath.Join(host, "precious"))

	tree := filepath.Join(root, "tree")
	mountPoint := filepath.Join(tree, "var", "log")
	mustMkdir(t, mountPoint)
	mustWrite(t, filepath.Join(tree, "file"))
	unmount := bindMount(t, host, mountPoint)
	if same, err := SameDevice(host, tree); err != nil || !same {
		t.Fatalf("SameDevice = %v, %v: the test needs one file system", same, err)
	}

	err := RemoveTree(t.Context(), tree)
	var mpe *MountPointError
	if !errors.As(err, &mpe) || mpe.Path != mountPoint {
		t.Fatalf("RemoveTree = %v, want a MountPointError for %s", err, mountPoint)
	}
	if _, err := os.Stat(filepath.Join(host, "precious")); err != nil {
		t.Fatalf("a file was deleted through the bind mount: %v", err)
	}

	// Once unmounted, the rest of the tree goes.
	unmount()
	if err := RemoveTree(t.Context(), tree); err != nil {
		t.Fatalf("RemoveTree after the unmount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(host, "precious")); err != nil {
		t.Fatalf("a file of the directory that was mounted was deleted: %v", err)
	}
}

func TestRemoveTreeStopsAtMountedFile(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "resolv.conf")
	mustWrite(t, host)
	tree := filepath.Join(root, "tree")
	mustMkdir(t, filepath.Join(tree, "etc"))
	mountPoint := filepath.Join(tree, "etc", "resolv.conf")
	mustWrite(t, mountPoint)
	bindMount(t, host, mountPoint)

	err := RemoveTree(t.Context(), tree)
	var mpe *MountPointError
	if !errors.As(err, &mpe) || mpe.Path != mountPoint {
		t.Fatalf("RemoveTree = %v, want a MountPointError for %s", err, mountPoint)
	}
	if _, err := os.Stat(host); err != nil {
		t.Fatalf("the mounted file was deleted: %v", err)
	}
}

func TestRemoveTreeRefusesMountPoint(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host")
	mustMkdir(t, host)
	mustWrite(t, filepath.Join(host, "precious"))
	tree := filepath.Join(root, "tree")
	mustMkdir(t, tree)
	bindMount(t, host, tree)

	err := RemoveTree(t.Context(), tree)
	var mpe *MountPointError
	if !errors.As(err, &mpe) || mpe.Path != tree {
		t.Fatalf("RemoveTree = %v, want a MountPointError for %s", err, tree)
	}
	if _, err := os.Stat(filepath.Join(host, "precious")); err != nil {
		t.Fatalf("a file was deleted through the mount: %v", err)
	}
}
