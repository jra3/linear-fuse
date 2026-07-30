#!/usr/bin/env bash
# Make an unprivileged FUSE mount possible, and fail loudly here if it isn't.
#
# Every CI job that mounts LinearFS runs this first. Without it the failure
# surfaces from inside the test harness as
#
#   /usr/local/bin/fusermount3: mount failed: Operation not permitted
#   Failed to setup SQLite fixtures: mount filesystem: fusermount exited with code 256
#
# which reads like a test bug rather than an environment one (#384).
#
# What goes wrong: go-fuse resolves the helper by PATH ORDER, not by absolute
# path (fusermountBinary → lookPathFallback("fusermount3", "/bin")). The helper
# must be setuid root to mount for an unprivileged user. GitHub runner images
# have carried a fusermount3 at /usr/local/bin that is mode 0777, owned by
# `runner`, and NOT setuid:
#
#   -rwxrwxrwx 1 runner runner 95944 /usr/local/bin/fusermount3   (3.18.2)
#   -rwsr-xr-x 1 root   root   39296 /usr/bin/fusermount3         (3.14.0, apt)
#
# and /usr/local/bin precedes /usr/bin, so the unusable copy wins and every mount
# fails with EPERM. It is per-image, which is why some runners worked and others
# did not on the same commit.
#
# The fix is to neutralize any non-setuid helper that shadows a working one — NOT
# to make that binary setuid root: it is writable by the unprivileged user, so
# setuid-rooting it would hand that user root.
set -euo pipefail

log() { printf '[fuse-preflight] %s\n' "$1"; }

# fuse3 is preinstalled on GitHub's ubuntu images, so this is normally a no-op —
# kept so a future image that drops it fails as a slow install, not a mount error.
if ! command -v fusermount3 >/dev/null 2>&1 && ! command -v fusermount >/dev/null 2>&1; then
    log "no fusermount helper found; installing fuse3"
    sudo apt-get update
    sudo apt-get install -y fuse3
fi

setuid_root() { [ -u "$1" ] && [ "$(stat -c %u "$1")" = "0" ]; }

# Walk the PATH-resolved candidates in order. Anything ahead of a usable helper
# that cannot mount is a shadow, and has to go.
mapfile -t candidates < <(type -aP fusermount3 2>/dev/null || true)
for c in "${candidates[@]:-}"; do
    [ -n "$c" ] || continue
    if setuid_root "$c"; then
        log "usable helper: $c ($(stat -c '%A %U:%G' "$c"))"
        break
    fi
    log "shadowing non-setuid helper: $c ($(stat -c '%A %U:%G' "$c")) — removing"
    sudo rm -f "$c"
done

# Assert the outcome rather than trusting the loop: PATH resolution is the thing
# that was wrong, so re-resolve from scratch and check what a mount would pick.
resolved="$(command -v fusermount3 2>/dev/null || command -v fusermount 2>/dev/null || true)"
if [ -z "$resolved" ]; then
    log "FATAL: no fusermount helper on PATH after preflight"
    "$(dirname "$0")/fuse-diagnostics.sh" || true
    exit 1
fi
if ! setuid_root "$resolved"; then
    log "FATAL: $resolved is not setuid root; an unprivileged mount cannot succeed"
    "$(dirname "$0")/fuse-diagnostics.sh" || true
    exit 1
fi
if [ ! -w /dev/fuse ] || [ ! -r /dev/fuse ]; then
    log "FATAL: /dev/fuse is not readable+writable by $(id -un)"
    "$(dirname "$0")/fuse-diagnostics.sh" || true
    exit 1
fi

log "ready: $resolved ($("$resolved" --version 2>&1 | head -1)), /dev/fuse ok"
