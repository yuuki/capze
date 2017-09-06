package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/pkg/errors"
	"github.com/yuuki/capze/log"
	"github.com/yuuki/capze/release"
)

// CLI is the command line object
type CLI struct {
	// outStream and errStream are the stdout and stderr
	// to write message from the CLI.
	outStream, errStream io.Writer
}

func main() {
	cli := &CLI{outStream: os.Stdout, errStream: os.Stderr}
	os.Exit(cli.Run(os.Args))
}

// Run invokes the CLI with the given arguments.
func (cli *CLI) Run(args []string) int {
	var (
		keep       int
		isRollback bool
		originPath string
		deployPath string
		prunedDirs bool
		prune      bool
		skipPrune  bool
		version    bool
		isDebug    bool
	)

	flags := flag.NewFlagSet(Name, flag.ContinueOnError)
	flags.SetOutput(cli.errStream)
	flags.Usage = func() {
		fmt.Fprint(cli.errStream, helpText)
	}
	flags.IntVar(&keep, "keep", 3, "")
	flags.IntVar(&keep, "k", 3, "")
	flags.BoolVar(&isRollback, "rollback", false, "")
	flags.BoolVar(&isRollback, "r", false, "")
	flags.BoolVar(&prunedDirs, "pruned-dirs", false, "")
	flags.BoolVar(&prune, "prune", false, "")
	flags.BoolVar(&skipPrune, "skip-prune", false, "")
	flags.BoolVar(&version, "version", false, "")
	flags.BoolVar(&version, "v", false, "")
	flags.BoolVar(&isDebug, "debug", false, "")
	flags.BoolVar(&isDebug, "d", false, "")

	if err := flags.Parse(args[1:]); err != nil {
		return 10
	}

	log.IsDebug = isDebug

	if version {
		fmt.Fprintf(cli.errStream, "%s version %s, build %s \n", Name, Version, GitCommit)
		return 0
	}

	if prunedDirs {
		// show pruned directories mode
		arg := flags.Args()
		if len(arg) < 1 {
			fmt.Fprintf(cli.errStream, "Too few arguments (%d!=1): must specify one arguments", len(arg))
			return 11
		}

		deployPath = filepath.Clean(arg[0])

		r := release.NewRelease(deployPath)
		dirs, err := r.PrunedDirs(keep)
		if err != nil {
			if isDebug {
				fmt.Fprintf(cli.errStream, "%+v\n", err)
			} else {
				fmt.Fprintf(cli.errStream, "%s\n", errors.Cause(err))
			}
		}
		for _, dir := range dirs {
			fmt.Printf("%v\n", dir)
		}
	} else if prune {
		arg := flags.Args()
		if len(arg) < 1 {
			fmt.Fprintf(cli.errStream, "Too few arguments (%d!=1): must specify one arguments", len(arg))
			return 11
		}

		deployPath = filepath.Clean(arg[0])

		r := release.NewRelease(deployPath)
		if err := r.Cleanup(keep); err != nil {
			if isDebug {
				fmt.Fprintf(cli.errStream, "%+v\n", err)
			} else {
				fmt.Fprintf(cli.errStream, "%s\n", errors.Cause(err))
			}
			return -1
		}
	} else if isRollback {
		// rollback mode
		arg := flags.Args()
		if len(arg) != 1 {
			fmt.Fprint(cli.errStream, "Too few arguments (!=1): must specify one arguments")
			return 11
		}

		deployPath = filepath.Clean(arg[0])

		r := release.NewRelease(deployPath)
		if err := r.Rollback(); err != nil {
			if isDebug {
				fmt.Fprintf(cli.errStream, "%+v\n", err)
			} else {
				fmt.Fprintf(cli.errStream, "%s\n", errors.Cause(err))
			}
			return -1
		}
	} else {
		// deploy mode
		paths := flags.Args()
		if len(paths) != 2 {
			fmt.Fprint(cli.errStream, "Too few arguments (!=2): must specify two arguments")
			return 11
		}

		originPath, deployPath = filepath.Clean(paths[0]), filepath.Clean(paths[1])

		r := release.NewRelease(deployPath)
		if err := r.Deploy(originPath, keep, !skipPrune); err != nil {
			if isDebug {
				fmt.Fprintf(cli.errStream, "%+v\n", err)
			} else {
				fmt.Fprintf(cli.errStream, "%s\n", errors.Cause(err))
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

  --pruned-dirs        Show directories pruned (Optional)

  --prune              Just cleanup old directories

  --skip-prune         Do not cleanup old directories (the --keep option is ignored)

  --debug, -d          Run with debug print

Examples:

  $ capze --keep 5 /tmp/app /var/www/app

  $ capze --keep 5 --pruned-dirs /var/www/app

`
