// Command capze deploys directories in a Capistrano-like layout: each
// release in its own directory of releases, and a current symbolic link to
// the release in use.
package main

import (
	"os"

	"github.com/yuuki/capze/internal/cli"
)

// version is the version of a release build, set with -ldflags by GoReleaser
// and the Makefile.
var version string

func main() {
	os.Exit(cli.Main(version))
}
