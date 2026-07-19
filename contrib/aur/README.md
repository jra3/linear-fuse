# AUR package — `linearfs-bin`

A prebuilt-binary [AUR](https://aur.archlinux.org) package for Arch Linux: it
downloads the GitHub release tarball for your architecture, verifies its
checksum, and installs the binary (`/usr/bin/linearfs`), the systemd **user**
service (repointed at `/usr/bin/linearfs`), the license, and the docs.

Covers `x86_64` and `aarch64`. Depends on `fuse3`.

## Install locally (from this directory)

```bash
cd contrib/aur
makepkg -si          # build + install with pacman
```

Then follow the post-install notes (set your API key; optionally enable the
user service — the unit reads `~/.config/linearfs/env`, which must set
`LINEARFS_MOUNT`).

## Publishing / updating the AUR package

The AUR is a separate git repo (`ssh://aur@aur.archlinux.org/linearfs-bin.git`).
The files here (`PKGBUILD`, `.SRCINFO`, `linearfs-bin.install`) are the source of
truth; publishing copies them into that repo.

First-time publish:

```bash
git clone ssh://aur@aur.archlinux.org/linearfs-bin.git /tmp/aur-linearfs
cp PKGBUILD .SRCINFO linearfs-bin.install /tmp/aur-linearfs/
cd /tmp/aur-linearfs && git add -A && git commit -m 'Initial import: linearfs-bin 0.1.0' && git push
```

## Bumping to a new release (the checklist)

When a new `vX.Y.Z` is tagged and its release assets exist:

1. Set `pkgver=X.Y.Z` and reset `pkgrel=1` in `PKGBUILD`.
2. Update **both** `sha256sums_x86_64` and `sha256sums_aarch64` from the
   release's `checksums.txt`:
   ```bash
   curl -fsSL https://github.com/jra3/linear-fuse/releases/download/vX.Y.Z/checksums.txt
   ```
   (Match `linearfs_X.Y.Z_linux_amd64.tar.gz` → `_x86_64`, `_linux_arm64` → `_aarch64`.)
3. Regenerate the metadata: `makepkg --printsrcinfo > .SRCINFO`.
4. Test the build end-to-end: `makepkg -f` (downloads, verifies checksum, packages).
5. Commit `PKGBUILD` + `.SRCINFO` here, then copy all three files into the AUR
   repo and `git push` there.

Bump only `pkgrel` (not `pkgver`) if you change packaging without a new upstream
release.

> Build artifacts (`src/`, `pkg/`, `*.pkg.tar.*`, the downloaded `*.tar.gz`) are
> gitignored — never commit them.
