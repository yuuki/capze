# Design

This document records what capze is for, the decisions behind its behaviour,
and the reasons for them. Read the section of what you change first, and
update it with the change.

## Purpose

capze keeps the releases of an application on a server in a Capistrano-like
layout, without Capistrano: a directory per release, and a `current` symbolic
link to the release in use. Something else produces the release (a build, an
archive from object storage, an image exported by
[droot](https://github.com/yuuki/droot)) and something else uses it (a
service manager). capze does the part in between that is easy to get almost
right with `mv`, `ln` and `rm`:

- make a prepared directory the newest release;
- switch `current` so that it never points to something incomplete;
- go back to an earlier release;
- delete old releases without deleting anything else.

It is one static binary with four commands, and it keeps no state other than
the directory names. An operator can always read, and repair, a deploy
directory with `ls`, `ln` and `mv`.

Out of scope: transport, building, restarting services, hooks, shared
directories linked into releases, and remote execution. A script composes
capze with the tools that do these.

## Layout

```
DEPLOY_DIR
├── current -> releases/20261113101500
├── releases
│   ├── 20261112093000
│   └── 20261113101500
└── .capze
    ├── lock
    └── trash
```

`DEPLOY_DIR` is one file system: every change below is a `rename(2)` inside
it.

## Decisions

### A release is moved into place, never copied

`capze deploy` renames the source directory to `releases/NAME`. The source
must therefore be on the file system of the deploy directory, and on the
same mount of it; otherwise the command fails before changing anything, and
says so.

A copy would make `releases` hold a partial release for the time of the copy
and after a crash, and it would have to reproduce owners, modes, setuid bits,
hard links, extended attributes, file capabilities, ACLs and sparse files
exactly: a release of a container file system depends on all of them. capze
0.2 ran `mv`, which copies across file systems, into `releases` directly. A
rename is atomic, instant and exact. Preparing the release on the right file
system costs the user one path (`mktemp -d -p DEPLOY_DIR`, or a directory of
`DEPLOY_DIR`), and avoids writing the release twice.

For the same reason a symbolic link is refused as a source: the link, not
the directory, would become the release.

### The deploy directory must exist

capze creates `releases` and `.capze`, but not `DEPLOY_DIR`. A mistyped path
would otherwise start a new deploy directory, and the deployment would
succeed while the service keeps running the old release.

capze works on absolute paths, and two kinds of path are refused because
their absolute form could name another directory than the one meant. An
empty path: `filepath.Abs("")` is the working directory, which an unset
shell variable must not turn into the deploy directory. And a path with a
`..` after a symbolic link: `filepath.Abs` removes the `..` with the link,
while the kernel follows the link first.

`releases` and `.capze/trash` must be on one file system, or no release
could be discarded or deleted: a deployment checks it before it changes
anything.

### Release names are deployment times that always increase

A release is named after the UTC time of its deployment, `YYYYMMDDhhmmss`,
as Capistrano names its releases. The order of the names is the only record
of the order of the deployments: capze 0.2 sorted by modification time,
which is the time of the build or of the extraction, and is the same for
every release of a reproducible archive.

So the names must sort in deployment order whatever the clock does. Under
the lock, the name of a new release is the current time, or one second after
the newest existing name if the time is not later (two deployments in one
second, a clock set back). Names that are taken by files that are not
releases count too.

Only directories of `releases` with such a name are releases. capze ignores
everything else there: it never deletes, or rolls back to, a file it did not
create.

Releases have no user-chosen names or labels. A label would need either an
order of its own or a second record of the order; the name of a release can
be found from its content when needed.

### `current` is a relative link, replaced by rename

The new link is created as `.capze/current.next` and renamed over `current`:
at every instant `current` leads to the old release or to the new one. The
target is the relative path `releases/NAME`, so that the deploy directory
can be moved, or seen under another path (a bind mount, a chroot).

capze finds the current release by resolving the link and comparing files,
not paths: the current release is the entry of `releases` that `current`
ends in. A link made by hand or by capze 0.2, absolute, through another
link or through another path to the deploy directory, is understood, and so
is a link to a directory inside a release: that release is in use, and
pruning must not delete it.

A `current` that is not a symbolic link is an error before anything
changes. A link that leads elsewhere, or nowhere, does not stop a
deployment, which replaces it, but it stops pruning: see below.

### The history is linear

The releases, sorted by name, are one history, and `current` is its head.

- `capze rollback` moves the head back by one release. It deletes nothing:
  in an incident the command must be fast and must not fail on a file that
  cannot be deleted, and the operator may want to inspect the bad release or
  return to it (`--to`).
- The releases newer than the head were rolled back. They are shown as
  `rolled-back`, and pruning leaves them.
- `capze deploy` discards them, by moving them to the trash, before it adds
  the new release.

Without the last rule, a release that was given up would sit between the new
release and the one it replaced, and the next `capze rollback` would return
to it: the same incident a second time. Capistrano avoids this by deleting
the bad release during the rollback; capze defers the deletion to the next
deployment, which is the moment the operator has moved on.

The rule needs no record of what happened. "Newer than `current`" can be
read from the directory at any time, also after `current` was changed by
hand. A release that was moved into `releases` by a deployment that crashed
before the switch is in the same state, and is discarded the same way.

A deployment either does all of this or none of it. The source is moved
first, because that is the step that can fail for reasons no check sees (the
source is a mount point, or is reached through another mount of the same
file system). If a rolled-back release then cannot be discarded, the
releases already discarded and the source are moved back, and the
deployment fails with everything where it was.

Alternatives considered: a journal of the switches (state that can disagree
with the directory, and cannot be repaired with `ls`), a marker file in the
bad release (capze would write into the content it deploys), and leaving the
problem to the operator (the failure appears only during a second incident).

### Releases are deleted through the trash

To delete a release, capze first renames it to `.capze/trash/NAME`, then
deletes the files. A deletion that fails, or is interrupted, leaves the rest
in the trash, where the next pruning deletes it. With capze 0.2, `rm -fr` in
`releases` could leave a partial release that a rollback would choose.

`.capze/trash` has mode 0700. A release may have been protected by the
permissions of `releases` or of the release directory; in the trash, its
setuid programs must not become reachable by other users. The trash is
created by the first deployment, with the link of the switch, before
anything changes: on a full file system, the deployment fails before it
moves the source, and the pruning that frees space does not have to create
a directory.

`capze prune --dry-run` lists the releases to delete and the entries of the
trash, so that a script can prepare them (unmount) before the real run. It
therefore needs to read the trash, like `capze prune`: it is for the user
who prunes.

### Nothing is deleted through a mount point

A release that runs as a container root has mounts below it: `/proc`, and
bind mounts of host directories. Deleting such a release with `rm -rf`
deletes the files of the host through the bind mounts. This is the mistake
capze must make impossible.

`fsutil.RemoveTree` walks the tree with directory file descriptors
(`openat(2)` with `O_NOFOLLOW`, `unlinkat(2)`), and compares the mount of
each directory it opens with the mount of the parent of the tree. It refuses
to enter a directory on another mount and returns a `MountPointError`;
`unlink(2)` and `rmdir(2)` themselves refuse a mount point (`EBUSY`), which
covers files that are mounted.

The mount is identified by its mount ID (`statx(2)` with `STATX_MNT_ID`, or
`/proc/self/fdinfo` on Linux before 5.8), not by the device number: a bind
mount of a directory of the same file system has the same device number as
the tree. Device numbers are used on macOS only, which has no bind mounts.
The check is made on the open file descriptor, so a file system mounted
after the check cannot be entered.

The deletion stops at the first error. The release is in the trash already,
so nothing depends on how much of it is left.

Stopping there is not enough for a release that is in use: a part of the
file system of a running container would be deleted before the mount point
is met. So before pruning touches a release, or an entry of the trash, it
looks for mount points below it in `/proc/self/mountinfo` (Linux), and
leaves it whole, with the same error, if there is one. This look is a
courtesy, not the guarantee: a mount made after it, or a system where the
list cannot be read, is still stopped by the check on every directory.

Known limits. `RemoveTree` keeps one directory open per level of the tree,
so a tree deeper than the limit of open files of the process is not deleted
(it stays in the trash, and pruning reports it each time). A release that
its owner may not read or write is still deleted by its owner: the
permissions of its directories are given back first, never those of a
symbolic link's target or of a mount point.

### Pruning counts back from the current release

`--keep N` keeps the current release and the N-1 releases before it.
Normally these are the N newest releases. After a rollback they are the
releases that a further rollback can reach; counting from the newest release
would delete them, and capze 0.2 could delete the current release itself.
Releases newer than `current` are never pruned (see the linear history).
Without a `current` link, the N newest are kept.

If `current` exists but does not lead to a release (a link to a release
deleted by hand, or to another directory), capze does not know which release
is in use. Pruning then deletes nothing and fails: unknown is not the same
as none, and the N newest releases may not include the one in use.

### A deployment succeeds when `current` is switched

`capze deploy` exits with 0 once `current` points to the new release, even
if deleting the old releases then fails, or if the switch could not be
flushed to disk. It warns on the standard error, and the next deployment or
`capze prune` tries the deletion again.

The caller's next step is the same in both cases: restart the service. With
a non-zero status, `capze deploy ... && systemctl restart app` would leave
`current` on a release that is not running, until some later restart starts
it unannounced. `capze prune` on its own does fail (status 1), for those who
want the deletion checked: `capze deploy --no-prune`, restart, `capze
prune`.

### The new release is on disk before `current` points to it

Before the switch, capze flushes the file system of the source
(`syncfs(2)`; `sync(2)` on macOS), then the `releases` directory after the
rename, then the deploy directory after the switch. A crash shortly after a
deployment must not leave `current` on a release whose files were still in
the page cache (empty files on ext4). `--no-sync` skips the first, slow
step for callers that do not need it. The flush runs before the lock is
taken: it is the only step that can take long. On macOS, `sync(2)` only
schedules the writes, so the guarantee is weaker there.

### One lock per deploy directory

`deploy`, `rollback` and `prune` hold `flock(2)` on `.capze/lock` from their
first look at the releases to their last change, so that two of them never
choose the same name or delete what the other is switching to. A command
waits for the lock, and says so; it does not fail, because a deployment that
overlaps a scheduled `prune` is normal. The kernel releases the lock when
the process ends, so a lock is never stale. `list` and `prune --dry-run`
take no lock and create nothing; `list` works for a user who may only read
the deploy directory.

### Interruption

On SIGINT or SIGTERM, capze stops at the next point where nothing is half
done. A deployment gives up only before its first change; after that it
finishes the switch, which takes no time. A deletion stops between two
directories, and the rest of the release stays in the trash. A second signal
ends the process as usual.

### Command line

- Subcommands, one per action. capze 0.2 selected the action with options
  (`--rollback`, `--prune`, `--pruned-dirs`), which made it possible to
  combine them in ways that had no meaning.
- Options are accepted after the arguments. `capze prune DIR --dry-run`
  must not delete, so an option is never taken for an argument, and a wrong
  number of arguments is an error.
- The standard output carries only the result, one path or name per line,
  for scripts. Everything else goes to the standard error.
- Exit status 2 means that nothing was done.
- Messages may name files of a release, whose names are anything: control
  characters are escaped, so that a file name cannot forge a line of capze
  or send an escape sequence to the terminal.
- The only dependency is `golang.org/x/sys`. The command line uses the
  standard `flag` package.

## Platforms

Linux and macOS, amd64 and arm64. The mount point check is exact on Linux,
where bind mounts exist.
