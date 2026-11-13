# capze

[![CI](https://github.com/yuuki/capze/actions/workflows/ci.yml/badge.svg)](https://github.com/yuuki/capze/actions/workflows/ci.yml)

`capze` deploys directories on a server in a
[Capistrano](https://capistranorb.com/)-like layout: each release in its own
directory, and a `current` symbolic link to the release in use.

```console
$ tree -a -L 2 /var/www/app
/var/www/app
├── .capze
├── current -> releases/20261113101500
└── releases
    ├── 20261111181204
    ├── 20261112093000
    └── 20261113101500
```

You build or extract a release into a directory; `capze` turns that directory
into the newest release, switches `current` to it, and deletes the old
releases. It only works on the directory tree: it does not upload, build or
restart anything, so it fits into any script or tool that does.

- **A release is whole or absent.** A directory enters `releases` by one
  `rename(2)`, and leaves it by one `rename(2)` before any of its files is
  deleted: `releases` never holds a release that is half copied or half
  deleted.
- **`current` always points to a whole release.** It changes by one
  `rename(2)`, after the files of the new release are flushed to disk.
- **Nothing is deleted through a mount point.** A release that still has a
  mount below it (a bind mount of a running container, for instance) is left,
  and reported, instead of deleting the files that are mounted there.
- **Rollbacks follow the history.** `capze rollback` goes to the release
  before the current one, as many times as needed, and the next deployment
  forgets the releases that were rolled back.
- **One at a time.** The commands that change a deploy directory wait for
  each other.

## Installation

Download a binary, or a deb or rpm package, for Linux or macOS (amd64, arm64)
from the [releases page](https://github.com/yuuki/capze/releases):

```console
$ curl -fsSLO https://github.com/yuuki/capze/releases/download/vX.Y.Z/capze_X.Y.Z_linux_amd64.tar.gz
$ tar -xzf capze_X.Y.Z_linux_amd64.tar.gz capze
$ sudo install capze /usr/local/bin/
```

Or build it with Go:

```console
$ go install github.com/yuuki/capze@latest
```

## Usage

```console
$ capze deploy [--keep N] [--no-prune] [--no-sync] SOURCE_DIR DEPLOY_DIR
$ capze rollback [--to RELEASE] DEPLOY_DIR
$ capze prune [--keep N] [--dry-run] DEPLOY_DIR
$ capze list DEPLOY_DIR
```

Options can be written before or after the directories.
`capze COMMAND --help` describes a command.

### Deploying

Create the deploy directory once. Then, for each release, prepare a directory
on the same file system and deploy it:

```console
$ mkdir -p /var/www/app
$ mkdir /var/www/app/incoming
$ tar -xzf app-1.2.3.tar.gz -C /var/www/app/incoming
$ capze deploy --keep 5 /var/www/app/incoming /var/www/app
/var/www/app/releases/20261113101500
```

`capze deploy`:

1. flushes the files of `SOURCE_DIR` to disk (skip with `--no-sync`);
2. moves `SOURCE_DIR` to `DEPLOY_DIR/releases/NAME`, where `NAME` is the time
   in UTC (`YYYYMMDDhhmmss`);
3. points `DEPLOY_DIR/current` to the new release, with the relative link
   `releases/NAME`;
4. prints the path of the new release;
5. deletes the old releases, as `capze prune --keep N` does (skip with
   `--no-prune`).

`SOURCE_DIR` is moved, never copied, so it must be on the file system of
`DEPLOY_DIR`; a directory of `DEPLOY_DIR` itself, as above, always is. It is
no longer there after the deployment. `DEPLOY_DIR` must exist: `capze` does
not create it, so that a mistyped path fails instead of starting a new, empty
deploy directory.

The exit status is 0 as soon as `current` points to the new release. If
deleting the old releases then fails, `capze` warns on the standard error and
the next deployment, or `capze prune`, tries again: a script that goes on to
restart the service is not stopped by an old release that could not be
deleted.

### Rolling back

```console
$ capze rollback /var/www/app
/var/www/app/releases/20261112093000
$ capze list /var/www/app
20261111181204
20261112093000  current
20261113101500  rolled-back
```

`capze rollback` points `current` to the release before the current one and
prints its path. Run it again to go one release further back. Nothing is
deleted, so a rollback can be undone, or any release chosen, by name:

```console
$ capze rollback --to 20261113101500 /var/www/app
```

The releases newer than `current` were rolled back. They stay until the next
deployment, which discards them: the release deployed after a rollback then
has the release it replaced right before it, and a rollback from it never
returns to a release that was given up.

### Deleting old releases

```console
$ capze prune --keep 3 /var/www/app
/var/www/app/releases/20261109120000
/var/www/app/releases/20261110120000
```

`capze prune` keeps the current release and the `N`-1 releases before it
(`--keep`, 5 by default), and deletes the older ones. It prints the path of
each directory once it is deleted; `--dry-run` prints the same paths and
deletes nothing. It never deletes the current release, nor the releases newer
than it.

A release is first moved out of `releases`, into `DEPLOY_DIR/.capze/trash`,
and then deleted. If the deletion fails or is interrupted, what is left stays
in the trash, where the next `capze prune` or `capze deploy` deletes it;
`--dry-run` lists it too.

`capze` never deletes through a mount point. If a release has a file system
mounted below it, the deletion stops with an error that names the mount
point, and the exit status of `capze prune` is 1. Unmount it and run
`capze prune` again.

Only the directories of `releases` named `YYYYMMDDhhmmss` are releases:
`capze` never touches other files kept there.

### Output and exit status

The standard output carries only what a script needs: the path of the new or
chosen release (`deploy`, `rollback`), the deleted paths (`prune`), the
releases (`list`). Warnings and errors go to the standard error.

| Status | Meaning |
|---|---|
| 0 | Done. |
| 1 | The command failed. `current` is unchanged, unless the message says otherwise. |
| 2 | The command line is wrong. Nothing was done. |

## Using capze with droot

[droot](https://github.com/yuuki/droot) runs the file system of a container
image with `chroot(2)`. `capze` keeps the releases of that file system:

```console
# install -d -m 700 /var/containers
# mkdir /var/containers/app
# tmp=$(mktemp -d -p /var/containers/app)
# mkdir -m 755 "$tmp/rootfs"
# tar -xpzf app-1.2.3.tar.gz -C "$tmp/rootfs"
# capze deploy --no-prune "$tmp/rootfs" /var/containers/app && rmdir "$tmp"
# systemctl restart app        # droot run --root /var/containers/app/current ...
```

Without `droot run --unshare`, a container leaves mounts below its release.
`capze prune` refuses to delete through them; unmount the old releases first:

```console
# capze prune --dry-run /var/containers/app | xargs -r -n 1 droot umount --root
# capze prune /var/containers/app
```

## The deploy directory

| Path | Content |
|---|---|
| `releases/YYYYMMDDhhmmss/` | The releases. The names sort in the order of the deployments. |
| `current` | Relative symbolic link to the release in use. |
| `.capze/lock` | Lock held by the commands that change the deploy directory. |
| `.capze/trash/` | Releases that are being deleted. Only the user of `capze` can enter it. |

The link is relative, so the deploy directory can be moved, or mounted under
another path. `capze` also understands a `current` made by hand, relative or
absolute, as long as it leads to a release or into one.

[docs/design.md](docs/design.md) explains the decisions behind this
behaviour.

## Development

```console
$ make check        # lint, workflow audit, vet, race tests, build
$ make test-root    # the tests that mount file systems (as root)
$ make test-fuzz    # every fuzz target, 30s each
$ make help         # the other targets
```

See [CONTRIBUTING.md](CONTRIBUTING.md) and [AGENTS.md](AGENTS.md).

## License

[MIT](LICENSE)
