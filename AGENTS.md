# AGENTS.md

## Rules
- `docs/design.md` is the source of truth for the behaviour and its reasons: read the section of what you change first, and update it with the change.
- A release enters and leaves `releases` by one `rename(2)`; capze never copies a release, and never deletes a file below `releases`: it renames the release to `.capze/trash` first.
- `current` changes only in `switchTo` (a link created aside, then renamed over it); a failed `deploy` or `rollback` leaves it on the old release.
- Delete only with `fsutil.RemoveTree`, never `os.RemoveAll`: it must not cross a mount point (mount IDs, not device numbers), follow a symbolic link, or change a directory outside the tree.
- The order of the release names is the only record of the history: no state file, no file written into a release; a new name sorts after every existing one (`nextID`).
- Never delete or switch to something capze did not create: only directories of `releases` named `YYYYMMDDhhmmss` are releases.
- Pruning never deletes the current release or a newer one, and a pruning failure never fails `capze deploy` (it warns); `capze prune` reports it with status 1.
- `deploy`, `rollback` and `prune` hold the lock from their first read of `releases` to their last change; `list` and `prune --dry-run` take no lock and create nothing.
- Tests that mount file systems mount only directories of their own `t.TempDir`, and register the unmount after it: a removal through the mount must have nothing else to delete.
- Dependencies: `golang.org/x/sys` only; a new one needs a strong reason.

## Compatibility contracts
- The layout of a deploy directory: `releases/YYYYMMDDhhmmss`, the relative `current` link, `.capze/lock`, `.capze/trash` (`internal/layout/layout.go`, `id.go`).
- Commands, options, defaults (`--keep 5`), the standard output of each command and the exit statuses 0, 1, 2 (`internal/cli/cli.go`, README).
- `main.go` stays at the root so that `go install github.com/yuuki/capze@latest` works.
- Release asset names (`capze_<version>_<os>_<arch>.tar.gz`, deb, rpm, `capze_<version>_checksums.txt`): `.goreleaser.yaml`.

## Before opening a pull request
- `make check`; also `make test-root` (root, Linux) for `internal/fsutil`, pruning or anything that moves or deletes releases.
- Release names, the pruning rule, the command line parser or the `fdinfo` parser → `make test-fuzz`.
- `.goreleaser.yaml` or the release workflow → `make snapshot`; a `uses:` line → `make lock-actions`.
- Commands, options or output → `README.md`, `docs/design.md` and the help texts in `internal/cli/cli.go`.

## Git conventions
- Branches: `<type>/<issue>-<summary>` (`<type>/<summary>` without an issue); tools' `agent/*`, `claude/*`, `copilot/*`, `dependabot/*` keep their names.
- Commits and PR titles: Conventional Commits 1.0.0, types `feat` `fix` `perf` `refactor` `test` `docs` `build` `ci` `chore` `revert`; scopes: `deploy`, `rollback`, `prune`, `layout`, `fsutil`, `cli`, `release`.
