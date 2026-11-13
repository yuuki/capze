// Package cli is the command line of capze.
package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"unicode"

	"github.com/yuuki/capze/internal/layout"
)

// The exit statuses of capze.
const (
	exitOK = 0
	// exitFailed: the command failed. Its message tells what was done.
	exitFailed = 1
	// exitUsage: the command line is wrong, and nothing was done.
	exitUsage = 2
)

const usage = `capze deploys directories in a Capistrano-like layout:

  DEPLOY_DIR
  ├── current -> releases/20261113101500
  └── releases
      ├── 20261112093000
      └── 20261113101500

Usage:
  capze deploy [--keep N] [--no-prune] [--no-sync] SOURCE_DIR DEPLOY_DIR
  capze rollback [--to RELEASE] DEPLOY_DIR
  capze prune [--keep N] [--dry-run] DEPLOY_DIR
  capze list DEPLOY_DIR
  capze version

Commands:
  deploy    Move SOURCE_DIR into DEPLOY_DIR/releases as a new release, point
            current to it, then delete the old releases
  rollback  Point current back to the release before it
  prune     Delete the old releases
  list      List the releases, oldest first
  version   Print the version of capze

Run 'capze COMMAND --help' for the options of a command.
`

// A command is one subcommand of capze.
type command struct {
	// synopsis is the command line after "capze".
	synopsis string
	// help describes the command, below the synopsis.
	help string
	// flags registers the options of the command.
	flags func(*flag.FlagSet, *options)
	// args is the number of arguments after the options.
	args int
	run  func(context.Context, *env, *options, []string) error
}

// options are the values of the options of all the commands.
type options struct {
	keep    int
	noPrune bool
	noSync  bool
	to      string
	dryRun  bool
}

// env is where a command writes.
type env struct {
	stdout, stderr io.Writer
}

var commands = map[string]*command{
	"deploy": {
		synopsis: "deploy [--keep N] [--no-prune] [--no-sync] SOURCE_DIR DEPLOY_DIR",
		help: `Move the directory SOURCE_DIR into DEPLOY_DIR/releases, under a new name made
of the time (UTC), and point DEPLOY_DIR/current to it. Then delete the old
releases, as 'capze prune' does. Print the path of the new release.

SOURCE_DIR must be on the file system of DEPLOY_DIR: it is moved, never
copied. DEPLOY_DIR must exist. The releases newer than the current one, which
were rolled back, are discarded.

The exit status is 0 once current points to the new release, even if deleting
the old releases then fails: capze warns, and the next deployment or
'capze prune' tries again.`,
		flags: func(fs *flag.FlagSet, o *options) {
			keepFlag(fs, o)
			fs.BoolVar(&o.noPrune, "no-prune", false, "do not delete the old releases")
			fs.BoolVar(&o.noSync, "no-sync", false, "do not flush the files of SOURCE_DIR to disk before the switch")
		},
		args: 2,
		run:  runDeploy,
	},
	"rollback": {
		synopsis: "rollback [--to RELEASE] DEPLOY_DIR",
		help: `Point DEPLOY_DIR/current back to the release before the current one, and print
its path. Nothing is deleted: the next deployment discards the releases that
were rolled back, and until then --to can return to one of them.`,
		flags: func(fs *flag.FlagSet, o *options) {
			fs.StringVar(&o.to, "to", "", "point current to `RELEASE`, a name that 'capze list' prints, instead")
		},
		args: 1,
		run:  runRollback,
	},
	"prune": {
		synopsis: "prune [--keep N] [--dry-run] DEPLOY_DIR",
		help: `Delete the old releases of DEPLOY_DIR: all but the current release and the
N-1 releases before it. Also delete what an earlier deletion left behind.
Print the path of each directory once it is deleted.

The current release and the releases newer than it are never deleted. Nothing
is deleted through a mount point: a release that has one below it is left,
whole, and reported.`,
		flags: func(fs *flag.FlagSet, o *options) {
			keepFlag(fs, o)
			fs.BoolVar(&o.dryRun, "dry-run", false, "print the paths that would be deleted, and delete nothing")
		},
		args: 1,
		run:  runPrune,
	},
	"list": {
		synopsis: "list DEPLOY_DIR",
		help: `List the releases of DEPLOY_DIR, oldest first: one name per line, followed by
'current' for the release that current points to, and by 'rolled-back' for
the releases newer than it.`,
		args: 1,
		run:  runList,
	},
}

func keepFlag(fs *flag.FlagSet, o *options) {
	fs.IntVar(&o.keep, "keep", layout.DefaultKeep, "keep `N` releases, the current one included")
}

// Main runs capze with the arguments of the process, until a SIGINT or a
// SIGTERM, and returns its exit status. version is the version of a release
// build, or "".
func Main(version string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// The first signal asks capze to stop where nothing is left half done;
	// a second one ends the process as usual.
	context.AfterFunc(ctx, stop)
	return Run(ctx, os.Args[1:], os.Stdout, os.Stderr, buildVersion(version))
}

// buildVersion returns the version to print: that of the release build, or
// else the one the Go toolchain recorded (go install, a build from a
// checkout).
func buildVersion(version string) string {
	if version == "" {
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	if version == "" {
		return "dev"
	}
	return strings.TrimPrefix(version, "v")
}

// Run runs capze with the arguments args (without the program name) and
// returns its exit status.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer, version string) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	name, args := args[0], args[1:]
	switch name {
	case "help", "--help", "-help", "-h":
		fmt.Fprint(stdout, usage)
		return exitOK
	case "version", "--version", "-version":
		fmt.Fprintf(stdout, "capze %s\n", version)
		return exitOK
	}
	cmd, ok := commands[name]
	if !ok {
		fmt.Fprintf(stderr, "capze: unknown command %q\nRun 'capze --help' for usage.\n", name)
		return exitUsage
	}

	opts := options{keep: layout.DefaultKeep}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the errors are printed below
	if cmd.flags != nil {
		cmd.flags(fs, &opts)
	}
	operands, err := parse(fs, args)
	switch {
	case errors.Is(err, flag.ErrHelp):
		printHelp(stdout, cmd, fs)
		return exitOK
	case err == nil && len(operands) != cmd.args:
		err = fmt.Errorf("expected %d arguments, got %d", cmd.args, len(operands))
	case err == nil && opts.keep < 1:
		err = errors.New("--keep must be at least 1")
	}
	if err != nil {
		fmt.Fprintf(stderr, "capze %s: %v\nUsage: capze %s\nRun 'capze %s --help' for details.\n", name, err, cmd.synopsis, name)
		return exitUsage
	}

	if err := cmd.run(ctx, &env{stdout: stdout, stderr: stderr}, &opts, operands); err != nil {
		printErrors(stderr, "capze: ", err)
		return exitFailed
	}
	return exitOK
}

// parse parses args with fs and returns the arguments that are not options.
// Unlike fs.Parse alone, it accepts options after arguments
// ("capze prune DIR --dry-run"): an option that would silently be taken for
// an argument could change what a command deletes.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var operands []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return operands, nil
		}
		// fs.Parse stopped at "--", which ends the options, or at the first
		// argument that is not an option.
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(operands, rest...), nil
		}
		operands = append(operands, rest[0])
		args = rest[1:]
	}
}

func printHelp(w io.Writer, cmd *command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "Usage: capze %s\n\n%s\n", cmd.synopsis, cmd.help)
	options := false
	fs.VisitAll(func(*flag.Flag) { options = true })
	if !options {
		return
	}
	fmt.Fprint(w, "\nOptions:\n")
	fs.VisitAll(func(f *flag.Flag) {
		name, usage := flag.UnquoteUsage(f)
		line := "  --" + f.Name
		if name != "" {
			line += " " + name
		}
		if f.DefValue != "" && f.DefValue != "false" {
			usage += fmt.Sprintf(" (default %s)", f.DefValue)
		}
		fmt.Fprintf(w, "%-16s  %s\n", line, usage)
	})
}

// printErrors prints each error that errors.Join put in err on its own line.
func printErrors(w io.Writer, prefix string, err error) {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, err := range joined.Unwrap() {
			printErrors(w, prefix, err)
		}
		return
	}
	message := err.Error()
	// Only the error of the context itself: one that wraps it says more.
	if err == context.Canceled { //nolint:errorlint // on purpose, see above
		message = "interrupted"
	}
	fmt.Fprintf(w, "%s%s\n", prefix, printable(message))
}

// printable returns s with its control characters escaped. A message may
// name a file of a release, whose name is anything: a newline in it must not
// start what looks like another message of capze, nor an escape sequence
// reach the terminal.
func printable(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			quoted := strconv.QuoteRune(r)
			b.WriteString(quoted[1 : len(quoted)-1])
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// open returns the Layout of dir, which tells on stderr when it waits for
// another capze.
func open(e *env, dir string) (*layout.Layout, error) {
	l, err := layout.Open(dir)
	if err != nil {
		return nil, err
	}
	l.Waiting = func() {
		fmt.Fprintf(e.stderr, "capze: waiting for another capze to finish with %s\n", l.Dir())
	}
	l.Warn = func(message string) {
		fmt.Fprintf(e.stderr, "capze: warning: %s\n", printable(message))
	}
	return l, nil
}

func runDeploy(ctx context.Context, e *env, o *options, args []string) error {
	l, err := open(e, args[1])
	if err != nil {
		return err
	}
	release, err := l.Deploy(ctx, args[0], !o.noSync)
	if err != nil {
		return err
	}
	fmt.Fprintln(e.stdout, release.Path)
	if o.noPrune {
		return nil
	}
	// The deployment is done: whether the old releases could be deleted does
	// not change what the caller must do next (restart the service), so it
	// does not change the exit status either.
	err = l.Prune(ctx, o.keep, func(path string) {
		fmt.Fprintf(e.stderr, "capze: deleted %s\n", path)
	})
	if err != nil {
		fmt.Fprintf(e.stderr, "capze: warning: %s is deployed, but old releases are left; 'capze prune' tries again:\n", release.ID)
		printErrors(e.stderr, "capze:   ", err)
	}
	return nil
}

func runRollback(ctx context.Context, e *env, o *options, args []string) error {
	l, err := open(e, args[0])
	if err != nil {
		return err
	}
	release, err := l.Rollback(ctx, o.to)
	if err != nil {
		return err
	}
	fmt.Fprintln(e.stdout, release.Path)
	return nil
}

func runPrune(ctx context.Context, e *env, o *options, args []string) error {
	l, err := open(e, args[0])
	if err != nil {
		return err
	}
	if o.dryRun {
		paths, err := l.Plan(o.keep)
		if err != nil {
			return err
		}
		for _, path := range paths {
			fmt.Fprintln(e.stdout, path)
		}
		return nil
	}
	return l.Prune(ctx, o.keep, func(path string) {
		fmt.Fprintln(e.stdout, path)
	})
}

func runList(_ context.Context, e *env, _ *options, args []string) error {
	l, err := open(e, args[0])
	if err != nil {
		return err
	}
	releases, err := l.Releases()
	if err != nil {
		return err
	}
	for _, r := range releases {
		if state := r.State.String(); state != "" {
			fmt.Fprintf(e.stdout, "%s  %s\n", r.ID, state)
		} else {
			fmt.Fprintln(e.stdout, r.ID)
		}
	}
	return nil
}
