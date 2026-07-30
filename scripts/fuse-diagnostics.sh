#!/usr/bin/env bash
# Print everything that decides whether an unprivileged FUSE mount can succeed.
#
# The failure-path companion to ci-fuse-preflight.sh: the preflight calls this
# when its assertions fail, so a CI job that cannot mount reports WHY in the same
# step instead of dying later inside the test harness with a fixture-setup error
# (#384). Also runnable by hand when debugging a mount problem on any machine.
#
# Why each check matters:
#   - go-fuse picks the helper by PATH order (fusermountBinary →
#     lookPathFallback("fusermount3", "/bin")), so WHICH fusermount3 wins is a
#     property of PATH, not of what is installed.
#   - the helper must be setuid root to mount for an unprivileged user; a
#     non-setuid copy earlier in PATH fails with exactly EPERM.
#   - AppArmor confinement of the helper, and the mode/ownership of /dev/fuse,
#     produce the same EPERM from a different cause.
#
# Never fails the job: this is observation, not a gate.
set -u

section() { printf '\n=== %s ===\n' "$1"; }

section "identity / PATH"
id
echo "PATH=$PATH"

section "fusermount3 resolution (first hit is what go-fuse uses)"
# type -a, not `command -v -a`: bash's command -v takes no -a, so that spelling
# silently reports nothing and reads as "not installed".
type -a fusermount3 2>/dev/null || echo "(fusermount3 not on PATH)"
type -a fusermount 2>/dev/null || echo "(fusermount not on PATH)"

section "candidate binaries (s bit = setuid root, required)"
for p in /usr/local/bin/fusermount3 /usr/bin/fusermount3 /bin/fusermount3 \
         /usr/local/bin/fusermount /usr/bin/fusermount; do
    [ -e "$p" ] && ls -l "$p"
done
echo "--- versions ---"
for p in /usr/local/bin/fusermount3 /usr/bin/fusermount3; do
    [ -x "$p" ] && { printf '%s: ' "$p"; "$p" --version 2>&1 | head -1; }
done

section "/dev/fuse"
ls -l /dev/fuse 2>&1 || echo "(missing)"
grep -w fuse /proc/filesystems 2>&1 || echo "(fuse not in /proc/filesystems)"

section "packaging"
if command -v dpkg >/dev/null 2>&1; then
    dpkg -l fuse3 libfuse3-3 2>/dev/null | tail -n +6
else
    echo "(no dpkg)"
fi

section "AppArmor"
if [ -d /sys/kernel/security/apparmor ]; then
    echo "apparmor present"
    sudo aa-status --json 2>/dev/null | head -c 2000 || sudo aa-status 2>&1 | head -20
    ls /etc/apparmor.d/ 2>/dev/null | grep -i -E 'fuse|mount' || echo "(no fuse/mount profiles in /etc/apparmor.d)"
else
    echo "(no apparmor)"
fi

section "unprivileged userns / sysctls"
for s in kernel.unprivileged_userns_clone kernel.apparmor_restrict_unprivileged_userns; do
    printf '%s = ' "$s"; sysctl -n "$s" 2>/dev/null || echo "(unset)"
done

echo
echo "(diagnostics complete; this step never fails the job)"
