package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// capze runs the command line and returns what it printed and its exit
// status.
func capze(t *testing.T, args ...string) (stdout, stderr string, status int) {
	t.Helper()
	var out, errs bytes.Buffer
	status = Run(t.Context(), args, &out, &errs, "1.2.3")
	return out.String(), errs.String(), status
}

// mustCapze runs a command line that must succeed and returns its output.
func mustCapze(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, status := capze(t, args...)
	if status != exitOK {
		t.Fatalf("capze %s: exit status %d\n%s", strings.Join(args, " "), status, stderr)
	}
	return stdout
}

// newDirs returns a new deploy directory, and a function that makes source
// directories on its file system.
func newDirs(t testing.TB) (deployDir string, source func() string) {
	t.Helper()
	root := t.TempDir()
	deployDir = filepath.Join(root, "app")
	if err := os.Mkdir(deployDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return deployDir, func() string {
		t.Helper()
		dir, err := os.MkdirTemp(root, "source-")
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
}

var releasePath = regexp.MustCompile(`^/.*/releases/\d{14}\n$`)

func TestDeployRollbackPruneList(t *testing.T) {
	dir, source := newDirs(t)

	var paths []string
	for range 3 {
		out := mustCapze(t, "deploy", source(), dir)
		if !releasePath.MatchString(out) {
			t.Fatalf("capze deploy printed %q, want the path of the release", out)
		}
		paths = append(paths, strings.TrimSpace(out))
	}
	ids := make([]string, len(paths))
	for i, path := range paths {
		ids[i] = filepath.Base(path)
	}
	if got, err := filepath.EvalSymlinks(filepath.Join(dir, "current")); err != nil || !samePath(t, got, paths[2]) {
		t.Errorf("current leads to %s, %v, want %s", got, err, paths[2])
	}

	want := ids[0] + "\n" + ids[1] + "\n" + ids[2] + "  current\n"
	if got := mustCapze(t, "list", dir); got != want {
		t.Errorf("capze list printed:\n%swant:\n%s", got, want)
	}

	if got := mustCapze(t, "rollback", dir); got != paths[1]+"\n" {
		t.Errorf("capze rollback printed %q, want %q", got, paths[1]+"\n")
	}
	want = ids[0] + "\n" + ids[1] + "  current\n" + ids[2] + "  rolled-back\n"
	if got := mustCapze(t, "list", dir); got != want {
		t.Errorf("capze list printed:\n%swant:\n%s", got, want)
	}
	if got := mustCapze(t, "rollback", "--to", ids[2], dir); got != paths[2]+"\n" {
		t.Errorf("capze rollback --to printed %q, want %q", got, paths[2]+"\n")
	}

	// A dry run tells what the real run deletes, and deletes nothing.
	want = paths[0] + "\n" + paths[1] + "\n"
	if got := mustCapze(t, "prune", "--keep", "1", "--dry-run", dir); got != want {
		t.Errorf("capze prune --dry-run printed:\n%swant:\n%s", got, want)
	}
	if got := mustCapze(t, "list", dir); strings.Count(got, "\n") != 3 {
		t.Errorf("capze prune --dry-run deleted releases:\n%s", got)
	}
	if got := mustCapze(t, "prune", "--keep=1", dir); got != want {
		t.Errorf("capze prune printed:\n%swant:\n%s", got, want)
	}
	if got := mustCapze(t, "list", dir); got != ids[2]+"  current\n" {
		t.Errorf("capze list printed %q", got)
	}
	if got := mustCapze(t, "prune", dir); got != "" {
		t.Errorf("capze prune with nothing to delete printed %q", got)
	}
}

func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatal(err)
	}
	return ra == rb
}

func TestDeployPrunes(t *testing.T) {
	dir, source := newDirs(t)
	first := strings.TrimSpace(mustCapze(t, "deploy", "--no-sync", source(), dir))
	second := strings.TrimSpace(mustCapze(t, "deploy", "--no-prune", "--keep", "1", source(), dir))
	if got := mustCapze(t, "list", dir); strings.Count(got, "\n") != 2 {
		t.Fatalf("capze deploy --no-prune deleted a release:\n%s", got)
	}

	// The standard output is the new release alone, for scripts; what was
	// deleted goes to the standard error.
	stdout, stderr, status := capze(t, "deploy", "--keep", "2", source(), dir)
	if status != exitOK || !releasePath.MatchString(stdout) {
		t.Fatalf("capze deploy: status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	if want := "capze: deleted " + first + "\n"; stderr != want {
		t.Errorf("capze deploy wrote %q to the standard error, want %q", stderr, want)
	}
	want := filepath.Base(second) + "\n" + filepath.Base(strings.TrimSpace(stdout)) + "  current\n"
	if got := mustCapze(t, "list", dir); got != want {
		t.Errorf("capze list printed:\n%swant:\n%s", got, want)
	}
}

// Once current points to the new release, the deployment succeeded, whatever
// happens to the old releases.
func TestDeploySucceedsWhenPruningFails(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can always delete")
	}
	dir, source := newDirs(t)
	mustCapze(t, "deploy", source(), dir)
	mustCapze(t, "deploy", source(), dir)
	// The trash cannot be used: pruning fails.
	trash := filepath.Join(dir, ".capze", "trash")
	if err := os.Chmod(trash, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(trash, 0o700) })

	stdout, stderr, status := capze(t, "deploy", "--keep", "1", source(), dir)
	if status != exitOK || !releasePath.MatchString(stdout) {
		t.Fatalf("capze deploy: status %d, stdout %q, stderr %q", status, stdout, stderr)
	}
	if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "capze prune") {
		t.Errorf("capze deploy did not warn: %q", stderr)
	}
	// The same failure is the failure of capze prune.
	if _, stderr, status := capze(t, "prune", "--keep", "1", dir); status != exitFailed || stderr == "" {
		t.Errorf("capze prune: status %d, stderr %q, want a failure", status, stderr)
	}
}

func TestFailures(t *testing.T) {
	dir, source := newDirs(t)
	missing := filepath.Join(dir, "missing")
	empty, _ := newDirs(t)
	mustCapze(t, "deploy", source(), dir)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing deploy directory", []string{"list", missing}, "does not exist"},
		{"empty deploy directory", []string{"prune", ""}, "is empty"},
		{"empty source", []string{"deploy", "", dir}, "is empty"},
		{"missing source", []string{"deploy", missing, dir}, "does not exist"},
		{"no release", []string{"rollback", empty}, "no release in"},
		{"nothing to roll back to", []string{"rollback", dir}, "no release older"},
		{"empty release name", []string{"rollback", "--to", "", dir}, "not the name of a release"},
		{"empty release name with =", []string{"rollback", "--to=", dir}, "not the name of a release"},
		{"unknown release", []string{"rollback", "--to", "20200101000000", dir}, "no release 20200101000000"},
		{"bad release name", []string{"rollback", "--to", "v1", dir}, "not the name of a release"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, status := capze(t, tt.args...)
			if status != exitFailed || stdout != "" {
				t.Errorf("status %d, stdout %q, want status %d and no output", status, stdout, exitFailed)
			}
			if !strings.HasPrefix(stderr, "capze: ") || !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want a message with %q", stderr, tt.want)
			}
		})
	}
}

func TestInterrupted(t *testing.T) {
	dir, source := newDirs(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out, errs bytes.Buffer
	src := source()
	if status := Run(ctx, []string{"deploy", src, dir}, &out, &errs, "1.2.3"); status != exitFailed {
		t.Errorf("status %d, want %d", status, exitFailed)
	}
	if got := errs.String(); got != "capze: interrupted\n" {
		t.Errorf("stderr = %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source was moved: %v", err)
	}
}

func TestUsageErrors(t *testing.T) {
	dir, source := newDirs(t)
	mustCapze(t, "deploy", source(), dir)
	src := source()
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no command", nil, "Usage:"},
		{"unknown command", []string{"destroy", dir}, `unknown command "destroy"`},
		{"0.2 command line", []string{"--keep", "5", src, dir}, `unknown command "--keep"`},
		{"unknown option", []string{"prune", "--force", dir}, "flag provided but not defined: -force"},
		{"option of another command", []string{"rollback", "--keep", "1", dir}, "flag provided but not defined: -keep"},
		{"missing argument", []string{"deploy", src}, "expected 2 arguments, got 1"},
		{"extra argument", []string{"prune", dir, dir}, "expected 1 arguments, got 2"},
		{"no argument", []string{"list"}, "expected 1 arguments, got 0"},
		{"keep 0", []string{"prune", "--keep", "0", dir}, "--keep must be at least 1"},
		{"negative keep", []string{"deploy", "--keep=-1", src, dir}, "--keep must be at least 1"},
		{"keep is not a number", []string{"prune", "--keep", "all", dir}, "invalid value"},
		{"missing value", []string{"rollback", dir, "--to"}, "flag needs an argument"},
		{"option after --", []string{"prune", "--", dir, "--dry-run"}, "expected 1 arguments, got 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, stderr, status := capze(t, tt.args...)
			if status != exitUsage || stdout != "" {
				t.Errorf("status %d, stdout %q, want status %d and no output", status, stdout, exitUsage)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want a message with %q", stderr, tt.want)
			}
		})
	}
	// Nothing was done.
	if _, err := os.Stat(src); err != nil {
		t.Errorf("the source was moved: %v", err)
	}
	if got := mustCapze(t, "list", dir); strings.Count(got, "\n") != 1 {
		t.Errorf("capze list printed %q, want one release", got)
	}
}

// An option after the arguments is an option: taken for an argument, or
// ignored, --dry-run would delete.
func TestOptionsAnywhere(t *testing.T) {
	dir, source := newDirs(t)
	for range 3 {
		mustCapze(t, "deploy", source(), "--no-prune", dir, "--no-sync")
	}
	for _, args := range [][]string{
		{"prune", dir, "--dry-run", "--keep", "1"},
		{"prune", "--keep=1", dir, "--dry-run"},
		{"prune", "--dry-run", "--keep", "1", "--", dir},
		{"prune", "-dry-run", "-keep", "1", dir},
	} {
		if got := mustCapze(t, args...); strings.Count(got, "\n") != 2 {
			t.Errorf("capze %s printed %q, want 2 paths", strings.Join(args, " "), got)
		}
	}
	if got := mustCapze(t, "list", dir); strings.Count(got, "\n") != 3 {
		t.Errorf("a dry run deleted releases:\n%s", got)
	}
	// After --, a name that looks like an option is an argument.
	t.Chdir(filepath.Dir(dir))
	if err := os.Mkdir("--dry-run", 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mustCapze(t, "list", "--", "--dry-run"); got != "" {
		t.Errorf("capze list -- --dry-run printed %q", got)
	}
}

func TestHelpAndVersion(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"help"}} {
		if got := mustCapze(t, args...); got != usage {
			t.Errorf("capze %s printed %q, want the usage", args[0], got)
		}
	}
	for _, args := range [][]string{{"version"}, {"--version"}} {
		if got := mustCapze(t, args...); got != "capze 1.2.3\n" {
			t.Errorf("capze %s printed %q", args[0], got)
		}
	}
	// The help of each command describes each of its options, and the usage
	// names each command.
	for name, cmd := range commands {
		got := mustCapze(t, name, "--help")
		if !strings.HasPrefix(got, "Usage: capze "+cmd.synopsis+"\n") {
			t.Errorf("capze %s --help starts with %q", name, strings.SplitN(got, "\n", 2)[0])
		}
		for _, option := range regexp.MustCompile(`--[a-z-]+`).FindAllString(cmd.synopsis, -1) {
			if !strings.Contains(got, "\n  "+option+" ") {
				t.Errorf("capze %s --help does not describe %s:\n%s", name, option, got)
			}
		}
		if !strings.Contains(usage, "\n  capze "+cmd.synopsis+"\n") {
			t.Errorf("the usage lacks the synopsis of %s", name)
		}
		if got2 := mustCapze(t, name, "-h"); got2 != got {
			t.Errorf("capze %s -h and --help differ", name)
		}
	}
}

func TestBuildVersion(t *testing.T) {
	if got := buildVersion("v1.2.3"); got != "1.2.3" {
		t.Errorf("buildVersion(v1.2.3) = %q", got)
	}
	if got := buildVersion("1.2.3-next"); got != "1.2.3-next" {
		t.Errorf("buildVersion(1.2.3-next) = %q", got)
	}
	// A test binary has no version of its own.
	if got := buildVersion(""); got == "" {
		t.Error(`buildVersion("") is empty`)
	}
}

// A command line that names no existing file can do nothing: it never
// panics, it succeeds only by printing help, and it fails with a message.
func FuzzParse(f *testing.F) {
	f.Add("prune\x00--keep\x001\x00--dry-run\x00dir")
	f.Add("deploy\x00src\x00--no-prune\x00dir")
	f.Add("rollback\x00--to\x00--\x00dir")
	f.Add("prune\x00--\x00--dry-run")
	f.Add("list\x00-\x00")
	f.Add("prune\x00")
	// Not the working directory: there is no directory 0 to come back from.
	f.Add("prune\x000/..")
	// Relative paths lead nowhere.
	f.Chdir(f.TempDir())
	f.Fuzz(func(t *testing.T, line string) {
		args := strings.Split(line, "\x00")
		cmd, ok := commands[args[0]]
		if !ok {
			t.Skip()
		}
		// Only command lines that cannot do anything: they name no
		// existing file.
		for _, arg := range args[1:] {
			if _, err := os.Lstat(arg); err == nil {
				t.Skip()
			}
		}
		var out, errs bytes.Buffer
		status := Run(t.Context(), args, &out, &errs, "1.2.3")
		if status == exitOK && !strings.Contains(out.String(), "Usage: capze "+cmd.synopsis) {
			t.Errorf("capze %q succeeded: %q", args, out.String())
		}
		if status != exitOK && errs.Len() == 0 {
			t.Errorf("capze %q failed with status %d without a message", args, status)
		}
	})
}

// However --dry-run and the other arguments of capze prune are arranged,
// nothing is deleted: the option is never taken for a directory, nor
// ignored. Either the dry run runs, or the command fails with a message.
func FuzzDryRunNeverDeletes(f *testing.F) {
	dir, source := newDirs(f)
	for range 3 {
		var out, errs bytes.Buffer
		if status := Run(f.Context(), []string{"deploy", "--no-prune", "--no-sync", source(), dir}, &out, &errs, "1.2.3"); status != exitOK {
			f.Fatalf("capze deploy: status %d: %s", status, errs.String())
		}
	}
	releases := func() []string {
		entries, err := os.ReadDir(filepath.Join(dir, "releases"))
		if err != nil {
			f.Fatal(err)
		}
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		return names
	}
	want := releases()
	// The arguments to arrange: each byte of the input picks one.
	words := []string{"--dry-run", "-dry-run", "--dry-run=true", "--keep", "--keep=1", "1", "--", "-", dir, dir + "/", "--no-prune", ""}

	f.Add([]byte{8, 0})
	f.Add([]byte{0, 3, 5, 8})
	f.Add([]byte{6, 8, 0})
	f.Add([]byte{3, 0, 8})
	f.Add([]byte{8, 8, 0})
	f.Fuzz(func(t *testing.T, picks []byte) {
		if len(picks) > 8 {
			t.Skip()
		}
		args := []string{"prune"}
		dryRun := false
		for _, pick := range picks {
			word := words[int(pick)%len(words)]
			dryRun = dryRun || strings.Contains(word, "dry-run")
			args = append(args, word)
		}
		if !dryRun {
			t.Skip()
		}
		var out, errs bytes.Buffer
		status := Run(t.Context(), args, &out, &errs, "1.2.3")
		if got := releases(); !slices.Equal(got, want) {
			t.Fatalf("capze %q deleted releases: %v are left of %v", args, got, want)
		}
		if status != exitOK && errs.Len() == 0 {
			t.Errorf("capze %q failed with status %d without a message", args, status)
		}
	})
}

func TestPrintErrors(t *testing.T) {
	// A file of a release may have any name.
	forged := errors.New("open /app/releases/2026/a\ncapze: all is well\x1b[2J: permission denied")
	err := errors.Join(errors.New("first"), fmt.Errorf("wrapped: %w", forged), context.Canceled)
	var out bytes.Buffer
	printErrors(&out, "capze: ", err)
	want := "capze: first\n" +
		`capze: wrapped: open /app/releases/2026/a\ncapze: all is well\x1b[2J: permission denied` + "\n" +
		"capze: interrupted\n"
	if got := out.String(); got != want {
		t.Errorf("printErrors wrote:\n%swant:\n%s", got, want)
	}

	// An error that wraps the interruption keeps what it says.
	out.Reset()
	printErrors(&out, "capze: ", fmt.Errorf("/app/.capze/trash/2026 is left to delete: %w", context.Canceled))
	if got, want := out.String(), "capze: /app/.capze/trash/2026 is left to delete: context canceled\n"; got != want {
		t.Errorf("printErrors wrote %q, want %q", got, want)
	}
}
