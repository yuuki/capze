package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/yuuki/capze/log"
)

func main() {
	os.Exit(Run(os.Args))
}

// Run invokes the CLI with the given arguments.
func Run(args []string) int {
	var (
		keep       int
		isRollback bool
		originDir  string
		deployDir  string
		isDebug    bool
	)

	flags := flag.NewFlagSet(Name, flag.ContinueOnError)
	flags.Usage = func() {
		fmt.Fprint(os.Stderr, helpText)
	}
	flags.IntVar(&keep, "keep", 3, "")
	flags.IntVar(&keep, "k", 3, "")
	flags.BoolVar(&isRollback, "rollback", false, "")
	flags.BoolVar(&isRollback, "r", false, "")
	flags.BoolVar(&isDebug, "debug", false, "")
	flags.BoolVar(&isDebug, "d", false, "")

	if err := flags.Parse(args[1:]); err != nil {
		return 10
	}

	log.IsDebug = isDebug

	if isRollback {
		// rollback mode
		arg := flags.Args()
		if len(arg) != 1 {
			fmt.Fprint(os.Stderr, "Too few arguments (!=1): must specify one arguments")
			return 11
		}

		deployDir = filepath.Clean(arg[0])

		release := NewRelease(deployDir, keep)
		if err := release.Rollback(); err != nil {
			if isDebug {
				fmt.Fprintf(os.Stderr, "%+v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "%s\n", errors.Cause(err))
			}
			return -1
		}
	} else {
		// deploy mode
		paths := flags.Args()
		if len(paths) != 2 {
			fmt.Fprint(os.Stderr, "Too few arguments (!=2): must specify two arguments")
			return 11
		}

		originDir, deployDir = filepath.Clean(paths[0]), filepath.Clean(paths[1])

		release := NewRelease(deployDir, keep)
		if err := release.Deploy(originDir); err != nil {
			if isDebug {
				fmt.Fprintf(os.Stderr, "%+v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "%s\n", errors.Cause(err))
			}
			return -1
		}
	}

	return 0
}

var helpText = `
Usage: capze [options] ORIGIN_DIR DEPLOY_DIR

  capze is a tool to make Capistrano-like directory structure.

Options:

  --keep, -k           The number of releases that it keeps

  --rollback, -r       Run as rollback mode

  --debug, -d          Run with debug print

Examples:

  $ capze --keep 5 /tmp/app /var/www/app

`
