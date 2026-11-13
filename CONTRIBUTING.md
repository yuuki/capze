# Contributing

Thanks for helping with capze.

- **Discuss first** for anything beyond a small fix: open an issue describing
  the problem, the command line, the platform and file system, and what you
  expected.
- **Read [docs/design.md](docs/design.md)** before changing what capze moves,
  switches or deletes: it explains why capze behaves as it does.
- **Keep pull requests focused.** One change per PR, with tests for any
  behaviour change. The tests work in temporary directories and need neither
  root nor a network.
- **Run `make check`** before pushing. It runs the linters, the workflow
  audit, `go vet` for Linux and macOS, the race-enabled tests and the build.
  Touching what is deleted or moved? Run `make test-root` too, as root on
  Linux: these tests bind-mount directories of their own to check that
  nothing is deleted through a mount point. Touching release names, the
  pruning rule or the command line parser? Run `make test-fuzz`.
- **Keep the docs in sync.** A change to commands, options, output or
  behaviour updates `README.md`, `docs/design.md` and the help texts
  together.
- **Commits, branches and pull request titles** follow the Git conventions
  in [AGENTS.md](AGENTS.md#git-conventions). The release notes are made from
  the commit titles.

The rules and compatibility contracts are in [AGENTS.md](AGENTS.md).
Security issues go through [SECURITY.md](SECURITY.md), not public issues.

## Releasing

Push a tag `vX.Y.Z` on a commit of `master`. The release workflow runs CI on
the tagged commit, builds the archives and the deb and rpm packages with
GoReleaser, publishes the GitHub release and attests the build provenance of
every asset. `make snapshot` builds the same assets locally without
publishing them.
