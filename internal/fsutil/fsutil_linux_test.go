package fsutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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

// The mount ID is that of the first mnt_id line, or there is none: the
// parser agrees with a slower one, and reads back what it would be given.
func FuzzParseFdinfoMountID(f *testing.F) {
	f.Add([]byte("pos:\t0\nflags:\t02200000\nmnt_id:\t29\nino:\t1234\n"))
	f.Add([]byte("mnt_id:"))
	f.Add([]byte("mnt_id:\t18446744073709551616\n"))
	f.Add([]byte("x\nmnt_id: 5\nmnt_id: 6"))
	f.Fuzz(func(t *testing.T, data []byte) {
		id, ok := parseFdinfoMountID(data)
		var want uint64
		wantOK := false
		for _, line := range strings.Split(string(data), "\n") {
			if value, found := strings.CutPrefix(line, "mnt_id:"); found {
				n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
				want, wantOK = n, err == nil
				if !wantOK {
					want = 0
				}
				break
			}
		}
		if id != want || ok != wantOK {
			t.Errorf("parseFdinfoMountID(%q) = %d, %v, want %d, %v", data, id, ok, want, wantOK)
		}
		if ok {
			again, ok := parseFdinfoMountID(fmt.Appendf(nil, "pos:\t0\nmnt_id:\t%d\n", id))
			if !ok || again != id {
				t.Errorf("mount ID %d read back as %d, %v", id, again, ok)
			}
		}
	})
}

func TestParseMountPoints(t *testing.T) {
	const mountinfo = `22 28 0:21 / /sys rw,nosuid shared:7 - sysfs sysfs rw
36 28 8:1 /var/log /var/containers/app/releases/20261113101500/var/log rw shared:1 - ext4 /dev/sda1 rw
40 28 8:1 / /mnt/with\040space\011tab\012newline\134backslash rw - ext4 /dev/sda1 rw
41 28 8:1 / /mnt/not\09octal\04 rw - ext4 /dev/sda1 rw
short line
`
	want := []string{
		"/sys",
		"/var/containers/app/releases/20261113101500/var/log",
		"/mnt/with space\ttab\nnewline\\backslash",
		`/mnt/not\09octal\04`,
	}
	if got := parseMountPoints([]byte(mountinfo)); !slices.Equal(got, want) {
		t.Errorf("parseMountPoints = %q, want %q", got, want)
	}
}

// A mount point never holds a space or a newline unescaped, whatever the
// file is, and parsing it does not fail.
func FuzzParseMountPoints(f *testing.F) {
	f.Add([]byte("36 28 8:1 / /mnt/a\\040b rw - ext4 /dev/sda1 rw\n"))
	f.Add([]byte("1 2 3 4 \\"))
	f.Add([]byte("1 2 3 4 \\777 \\12"))
	f.Fuzz(func(t *testing.T, data []byte) {
		lines := bytes.Count(data, []byte("\n")) + 1
		points := parseMountPoints(data)
		if len(points) > lines {
			t.Errorf("%d mount points in %d lines", len(points), lines)
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
	if openDir(t, host).ID().Dev != openDir(t, tree).ID().Dev {
		t.Fatal("the test needs one file system")
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

func TestMountsBelow(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host")
	tree := filepath.Join(root, "tree")
	other := filepath.Join(root, "tree-other")
	deep := filepath.Join(tree, "a", "b")
	for _, dir := range []string{host, deep, other} {
		mustMkdir(t, dir)
	}
	if got := MountsBelow(tree); len(got) != 0 {
		t.Errorf("MountsBelow = %v before any mount", got)
	}
	bindMount(t, host, deep)
	// A directory whose name starts with that of the tree is not below it.
	bindMount(t, host, other)

	if got := MountsBelow(tree); !slices.Equal(got, []string{deep}) {
		t.Errorf("MountsBelow(tree) = %v, want %v", got, []string{deep})
	}
	if got := MountsBelow(deep); !slices.Equal(got, []string{deep}) {
		t.Errorf("MountsBelow(mount point) = %v, want %v", got, []string{deep})
	}
	// Through a symbolic link to the tree.
	link := filepath.Join(root, "link")
	if err := os.Symlink(tree, link); err != nil {
		t.Fatal(err)
	}
	if got := MountsBelow(link); !slices.Equal(got, []string{deep}) {
		t.Errorf("MountsBelow(link) = %v, want %v", got, []string{deep})
	}
	if got := MountsBelow(filepath.Join(root, "missing")); len(got) != 0 {
		t.Errorf("MountsBelow(missing) = %v", got)
	}
}

// makeAccessible changes the directory it is given and nothing else: not
// the target of a symbolic link, not the root of a file system mounted on
// the directory.
func TestMakeAccessible(t *testing.T) {
	root := t.TempDir()
	parent, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(parent)
	mount, err := mountOf(parent)
	if err != nil {
		t.Fatal(err)
	}
	mode := func(path string) os.FileMode {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode().Perm()
	}

	closed := filepath.Join(root, "closed")
	if err := os.Mkdir(closed, 0); err != nil {
		t.Fatal(err)
	}
	if err := makeAccessible(parent, "closed", mount); err != nil {
		t.Fatal(err)
	}
	if got := mode(closed); got != 0o700 {
		t.Errorf("mode = %o, want 700", got)
	}

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := makeAccessible(parent, "link", mount); err == nil {
		t.Error("makeAccessible of a symbolic link succeeded")
	}
	if got := mode(target); got != 0o500 {
		t.Errorf("the target of the link has mode %o, want 500", got)
	}

	host := filepath.Join(root, "host")
	if err := os.Mkdir(host, 0o500); err != nil {
		t.Fatal(err)
	}
	mountPoint := filepath.Join(root, "mount-point")
	mustMkdir(t, mountPoint)
	bindMount(t, host, mountPoint)
	if err := makeAccessible(parent, "mount-point", mount); !errors.Is(err, errOtherMount) {
		t.Errorf("makeAccessible of a mount point = %v, want errOtherMount", err)
	}
	if got := mode(host); got != 0o500 {
		t.Errorf("the directory that is mounted has mode %o, want 500", got)
	}
}

// A directory with a file system mounted on it is not a directory of its
// own, even when the file system is the one of its parent.
func TestDirSubRefusesMountPoint(t *testing.T) {
	root := t.TempDir()
	host := filepath.Join(root, "host")
	mountPoint := filepath.Join(root, "mount-point")
	mustMkdir(t, host)
	mustMkdir(t, mountPoint)
	bindMount(t, host, mountPoint)
	d := openDir(t, root)

	var mpe *MountPointError
	if sub, err := d.Sub("mount-point"); !errors.As(err, &mpe) || mpe.Path != mountPoint {
		sub.Close()
		t.Errorf("Sub(mount point) = %v, want a MountPointError for %s", err, mountPoint)
	}
	if sub, err := d.MkSub("mount-point", 0o755); !errors.As(err, &mpe) {
		sub.Close()
		t.Errorf("MkSub(mount point) = %v, want a MountPointError", err)
	}
	// Follow goes through it, and tells that it is another mount.
	followed, err := d.Follow("mount-point")
	if err != nil {
		t.Fatal(err)
	}
	defer followed.Close()
	if followed.ID() != openDir(t, host).ID() || d.SameMount(followed) {
		t.Errorf("Follow(mount point): the directory mounted %v, same mount %v", followed.ID() == openDir(t, host).ID(), d.SameMount(followed))
	}
}
