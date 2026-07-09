package fs

// Mountpoint preflight: probe the mountpoint before mounting and self-heal
// the classic wedged state. Born from a real incident — a dead FUSE mount
// ("Transport endpoint is not connected") left at the service's own
// mountpoint made ExecStartPre's mkdir fail, and systemd restart-looped
// forever because nothing recovered without a manual `fusermount3 -uz`.
//
// The policy has exactly three cases:
//   - not a mountpoint (plain dir or missing)  → proceed normally
//   - DEAD mount (statfs ENOTCONN, or any statfs failure on a path the
//     mount table still lists)                 → lazy-unmount, verify, proceed
//   - HEALTHY live mount                       → refuse loudly; never unmount
//     a live mount (that would kill a concurrent instance)
//
// The unmount is `fusermount3 -uz` by construction, never syscall umount2 —
// unprivileged umount2 is EPERM on FUSE (recorded lesson).

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// mountPreflight carries the three OS seams so the policy is unit-testable
// without a real mount: statfs, the mount-table lookup, and the unmount exec.
type mountPreflight struct {
	statfs    func(path string) error
	isMounted func(path string) (bool, error)
	unmount   func(path string) error
	logf      func(format string, args ...interface{})
}

func newMountPreflight() *mountPreflight {
	return &mountPreflight{
		statfs: func(path string) error {
			var st syscall.Statfs_t
			return syscall.Statfs(path, &st)
		},
		isMounted: procMounted,
		unmount: func(path string) error {
			out, err := exec.Command("fusermount3", "-uz", path).CombinedOutput()
			if err != nil {
				return fmt.Errorf("fusermount3 -uz %s: %v (%s)", path, err, strings.TrimSpace(string(out)))
			}
			return nil
		},
		logf: log.Printf,
	}
}

// PreflightMountpoint probes path before mounting: heals a dead FUSE mount
// left by a crashed/killed instance (lazy unmount + verify), refuses a
// healthy live mount, and is a no-op for a plain directory or missing path.
// Called by `linearfs mount` before creating the mountpoint, and by the
// integration harness to clean stale test mounts from killed prior runs.
func PreflightMountpoint(path string) error {
	return newMountPreflight().run(path)
}

func (p *mountPreflight) run(path string) error {
	err := p.statfs(path)
	switch {
	case err == nil:
		mounted, merr := p.isMounted(path)
		if merr != nil {
			// Mount table unavailable (non-Linux, unreadable /proc).
			// statfs succeeded, so nothing is wedged — proceed.
			return nil
		}
		if mounted {
			// Healthy live mount: refuse, never unmount. A lazy unmount
			// here would yank the filesystem out from under a concurrently
			// running instance.
			return fmt.Errorf("%s is already a live mount — is another linearfs running? (unmount it first: fusermount3 -u %s)", path, path)
		}
		return nil // plain directory
	case errors.Is(err, syscall.ENOENT):
		return nil // missing; MkdirAll creates it
	}

	// statfs failed. ENOTCONN is the classic dead-FUSE wedge; any other
	// errno on a path the mount table still lists gets the same treatment.
	if !errors.Is(err, syscall.ENOTCONN) {
		mounted, merr := p.isMounted(path)
		if merr != nil || !mounted {
			return fmt.Errorf("statfs %s: %w", path, err)
		}
	}

	p.logf("linearfs: dead mount at %s (%v); detaching with fusermount3 -uz", path, err)
	if uerr := p.unmount(path); uerr != nil {
		return fmt.Errorf("dead mount at %s and lazy unmount failed: %w (clean manually: fusermount3 -uz %s)", path, uerr, path)
	}

	// Verify the detach took before letting the mount proceed.
	if verr := p.statfs(path); verr != nil && !errors.Is(verr, syscall.ENOENT) {
		return fmt.Errorf("mount at %s still wedged after fusermount3 -uz (statfs: %v); clean manually and retry", path, verr)
	}
	if mounted, merr := p.isMounted(path); merr == nil && mounted {
		return fmt.Errorf("mount at %s still present after fusermount3 -uz; clean manually and retry", path)
	}
	return nil
}

// procMounted reports whether path is a mount point per /proc/self/mounts.
func procMounted(path string) (bool, error) {
	f, err := os.Open("/proc/self/mounts")
	if err != nil {
		return false, err
	}
	defer f.Close()
	target := filepath.Clean(path)
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		if unescapeMountPath(fields[1]) == target {
			return true, nil
		}
	}
	return false, sc.Err()
}

// unescapeMountPath decodes the octal escapes /proc/self/mounts applies to
// mount points (\040 space, \011 tab, \012 newline, \134 backslash).
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
