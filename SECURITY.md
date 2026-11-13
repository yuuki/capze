# Security policy

capze usually runs as root, or as the user that owns an application, and it
moves and deletes directory trees. It trusts its command line and the owner
of the deploy directory. It does not trust the content of a release: a
release may be the file system of a container image.

Examples of what is in scope:

- A release whose content (symbolic links, deep or odd file names, mount
  points) makes capze delete, move or change the permissions of a file
  outside the release.
- Deleting files through a mount point below a release.
- A `current` link that points, even for an instant, to something that is
  not a whole release as a result of a capze command.
- Files of a discarded release becoming reachable by users who could not
  reach them in `releases`.
- Problems with the release archives, packages, checksums or attestations.

Out of scope: what a user who can write to the deploy directory, or who
chooses the command line, can make capze do there; they can do the same
without capze.

## Supported versions

Security fixes are made on the `master` branch and released in the next
version. Only the latest release is supported.

## Reporting a vulnerability

Please do **not** open a public issue, pull request or discussion.

Report the vulnerability privately with GitHub's
[private vulnerability reporting](https://github.com/yuuki/capze/security/advisories/new)
(the "Report a vulnerability" button of the Security tab). If that is not
possible, contact the author privately via the contact information on
https://github.com/yuuki.

Please include the version (`capze --version`), the platform and file
system, the command line, what an attacker controls, steps to reproduce and
the impact. You should receive an answer within a few weeks; capze is
maintained on a best-effort basis.

## Verifying releases

Every release publishes `capze_<version>_checksums.txt`, and every asset and
the checksums file have a signed
[build provenance attestation](https://github.com/yuuki/capze/attestations)
stating that the release workflow of this repository built them from the
commit of the release:

```console
$ gh attestation verify capze_X.Y.Z_linux_amd64.tar.gz --repo yuuki/capze
```
