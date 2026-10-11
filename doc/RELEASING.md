# Releasing

Back to the [README](../README.md).

A release is a `v*` tag. `.github/workflows/release.yml` builds it, tests it
and publishes a GitHub release; the AUR packages in `packaging/aur/` are
updated from that by hand. The layout follows ranma's
(`masshirodev/ranma`, `doc/RELEASING.md`), so one maintainer keeps one habit.

## What a release contains

For each of `amd64` and `arm64`, `kizuna-<version>-linux-<arch>.tar.gz`, with
its `.sha256` beside it:

- `kizuna`, the binary. kizuna is pure Go (its SQLite is `modernc.org/sqlite`),
  so it is built with `CGO_ENABLED=0`: static, linking nothing, and the same
  file runs on every Linux whatever its glibc. ranma has to build on an old
  Ubuntu to keep its glibc floor low; kizuna has no floor.
- `LICENSE`, `README.md`, and `doc/` without the design briefs and handoffs.

**Packaged builds do not update themselves.** The workflow and both AUR
packages link with `-X main.packaged=<where it came from>`. Such a kizuna
never asks GitHub whether it is behind, `kizuna self-update` and the TUI's
`U` refuse and point at the package manager, and `kizuna version` prints the
name (`kizuna 0.1.0 (<sha>, from the AUR (kizuna-git))`). A build without it
is a build from source and updates as before ([updates.md](updates.md)).

## Cutting one

1. Bump `version` in `cmd/kizuna/version.go` (semver, starting at 0.1.0) and
   commit it. The workflow refuses a tag that does not match it.
2. Tag and push:

   ```sh
   git tag -a v0.2.0 -m "kizuna 0.2.0"
   git push origin v0.2.0
   ```

3. The workflow runs `go vet` and `go test`, builds both architectures, and
   creates the release with notes generated from the commits since the last
   tag.

To try the build without releasing, run the workflow by hand
(`gh workflow run release.yml`): it builds the same tarballs as a run
artifact and skips the release step.

The repository is public, so Actions minutes are not metered and the build
belongs there, not on the workstation. A build from the workstation would also
work (it is static), but it would not be the one the tag points at.

## The AUR packages

`packaging/aur/` holds the two packages, each a directory that is the whole
AUR repository: `PKGBUILD`, `kizuna.install` and the generated `.SRCINFO`.

- **kizuna-bin** installs the release tarball. After each release, set
  `pkgver`, reset `pkgrel=1`, put each architecture's sha256 (the `.sha256`
  files in the release) in `sha256sums_x86_64` and `sha256sums_aarch64`, and
  regenerate `.SRCINFO` (`makepkg --printsrcinfo > .SRCINFO`).
- **kizuna-git** builds `main` with Go, following the Arch Go package
  guidelines (PIE, `-trimpath`, external linking so makepkg's `LDFLAGS`
  apply). Its `pkgver()` computes the version from `version.go` and the commit
  count, so the PKGBUILD only changes when the build itself does.

Build a package before pushing it: `makepkg -Cf` in a copy of the directory.

Publishing is pushing a directory to `ssh://aur@aur.archlinux.org/<pkgname>.git`
with the maintainer's AUR key.

**pacman cannot do what the rename migration needs from a user's home.**
`kizuna.install` says so on install: a source build in `GOPATH/bin` shadows
`/usr/bin/kizuna` on most `PATH`s, and hooks written by a source build name
that binary's path until `kizuna integrate messaging <profile>` re-points
them. [rename.md](rename.md) has the migration itself.
