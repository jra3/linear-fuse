# Installation Guide

This guide covers installing LinearFS on macOS, Arch Linux, and Ubuntu/Debian.

## Prerequisites (All Platforms)

- **Go 1.21+** - for building from source
- **Linear API Key** - get one from [Linear Settings → API](https://linear.app/settings/api)

## Mount Point

The recommended mount point varies by platform:

- **Linux**: `~/linear` (system-wide location)
- **macOS**: `~~/linear` (user home directory)

Create the mount point before first use:

```bash
# Linux
sudo mkdir -p ~/linear && sudo chown $USER:$USER ~/linear

# macOS
mkdir -p ~~/linear
```

> **Note:** You can use any mount point you prefer. The examples below use the platform-specific defaults.

## macOS

> **⚠️ Apple Silicon Users:** macFUSE requires booting into Recovery Mode to enable kernel extensions. This is a one-time setup. See step 1 below for detailed instructions.

### 1. Install and Configure macFUSE

macFUSE provides the FUSE kernel extension for macOS.

#### Install macFUSE

```bash
brew install --cask macfuse
```

You'll be prompted for your password during installation.

#### Enable Kernel Extensions (Apple Silicon Only)

**On Apple Silicon Macs (M1/M2/M3), you MUST enable kernel extensions in Recovery Mode:**

1. **Shut down** your Mac completely
2. **Press and hold the power button** until you see "Loading startup options"
3. Click **Options**, then click **Continue**
4. Select your user account and enter your password
5. From the menu bar, select **Utilities** → **Startup Security Utility**
6. Click the lock icon and authenticate
7. Select **Reduced Security**
8. Check the box: **"Allow user management of kernel extensions from identified developers"**
9. Click **OK** and restart your Mac

> **Intel Macs:** You can skip the Recovery Mode step. Just approve the extension in System Settings after installation.

#### Approve the Kernel Extension

After restarting:

1. Open **System Settings** → **Privacy & Security**
2. Scroll down to find "System software from developer 'Benjamin Fleischer' was blocked"
3. Click **Allow**
4. Enter your password if prompted
5. **Restart your Mac** again

#### Verify Installation

After the final restart, verify macFUSE is working:

```bash
ls /Library/Filesystems/macfuse.fs
```

If you see output, macFUSE is installed correctly.

### 2. Install Go

```bash
brew install go
```

### 3. Build and Install LinearFS

```bash
git clone https://github.com/jra3/linear-fuse.git
cd linear-fuse
make build
make install  # Copies binary to ~/.local/bin
```

> **Note:** Ensure `~/.local/bin` is in your PATH. Add to your shell profile if needed:
> ```bash
> echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc  # or ~/.bashrc
> ```

### 4. Configure

```bash
mkdir -p ~/.config/linearfs
cat > ~/.config/linearfs/config.yaml << EOF
api_key: "lin_api_YOUR_KEY_HERE"
EOF
```

Or set the environment variable:
```bash
export LINEAR_API_KEY="lin_api_YOUR_KEY_HERE"
```

### 5. Mount

```bash
linearfs mount ~~/linear
```

### macOS Troubleshooting

| Issue | Solution |
|-------|----------|
| "no FUSE mount utility found" | macFUSE not installed or kernel extension not loaded. Verify with `ls /Library/Filesystems/macfuse.fs`. If present but still errors, reboot. |
| "System Extension Blocked" | Open System Settings → Privacy & Security, scroll down, click "Allow" for Benjamin Fleischer, then restart |
| "Operation not permitted" | On Apple Silicon: Enable kernel extensions in Recovery Mode (see step 1). On Intel: Check that SIP isn't blocking |
| macFUSE installed but mount fails | **Apple Silicon:** Did you enable kernel extensions in Recovery Mode? This is required. **All Macs:** Did you approve the extension AND restart twice? |
| Service starts but mount empty | Check logs: `tail -f /tmp/linearfs.err`. Verify API key in `~/.config/linearfs/config.yaml` |

### Running as a launchd Service (Automatic Startup)

To have LinearFS start automatically on login:

#### 1. Copy the Service File

```bash
cp contrib/launchd/com.linearfs.mount.plist ~/Library/LaunchAgents/
```

#### 2. Configure Mount Point

The default mount point is `~~/linear`. To customize it, edit the env file:

```bash
mkdir -p ~/.config/linearfs
cat > ~/.config/linearfs/env << 'EOF'
LINEAR_API_KEY=lin_api_YOUR_KEY_HERE
LINEARFS_MOUNT=~~/linear
EOF
chmod 600 ~/.config/linearfs/env
```

Or simply use the config.yaml file (recommended):

```bash
mkdir -p ~/.config/linearfs
cat > ~/.config/linearfs/config.yaml << 'EOF'
api_key: "lin_api_YOUR_KEY_HERE"
cache:
  ttl: 60s
EOF
chmod 600 ~/.config/linearfs/config.yaml
```

#### 3. Create Mount Point

```bash
mkdir -p ~~/linear
```

#### 4. Load and Start

```bash
launchctl load ~/Library/LaunchAgents/com.linearfs.mount.plist
launchctl start com.linearfs.mount
```

The service will now start automatically on login.

#### 5. Management Commands

```bash
launchctl stop com.linearfs.mount      # Stop the service
launchctl start com.linearfs.mount     # Start the service
launchctl unload ~/Library/LaunchAgents/com.linearfs.mount.plist  # Disable autostart
```

#### 6. View Logs

```bash
tail -f /tmp/linearfs.log   # Standard output
tail -f /tmp/linearfs.err   # Error output
```

---

## Linux — pick your install

Every prebuilt package installs the binary to `/usr/bin/linearfs`, a systemd
**user** service, and the docs, and pulls in `fuse3`. Packages are built for
`x86_64` (amd64) and `aarch64` (arm64) — swap `_linux_amd64` → `_linux_arm64` on ARM.

| Distro family | Method | Command |
|---|---|---|
| Arch, Manjaro, EndeavourOS | AUR | `yay -S linearfs-bin` |
| Debian, Ubuntu, Mint, Pop!_OS | `.deb` from the release | `sudo apt install ./linearfs_<version>_linux_amd64.deb` |
| Fedora, RHEL, CentOS Stream, openSUSE | `.rpm` from the release | `sudo dnf install ./linearfs_<version>_linux_amd64.rpm` |
| Anything else | prebuilt tarball or `go install` | see [Other Linux distributions](#other-linux-distributions) |

The archives, `.deb`, and `.rpm` all carry SLSA build provenance — verify any
download before installing (see
[docs/THREAT-MODEL.md](https://github.com/jra3/linear-fuse/blob/main/docs/THREAT-MODEL.md)):

```bash
gh attestation verify <downloaded-file> -R jra3/linear-fuse
```

Each distro's section below covers the prebuilt install, a from-source build,
and troubleshooting.

---

## Arch Linux

### Install from the AUR (recommended)

[`linearfs-bin`](https://aur.archlinux.org/packages/linearfs-bin) is a
prebuilt-binary package: it installs `/usr/bin/linearfs`, the systemd **user**
service, and the docs, and pulls in `fuse3`.

```bash
yay -S linearfs-bin        # or: paru -S linearfs-bin
```

Then set your API key (the *Configure* step below). The AUR bump checklist
(`contrib/aur/README.md`) verifies each release's `checksums.txt` provenance
before pinning, so the package's checksums trace back to a signed build.

### Build from source

### 1. Install FUSE

```bash
sudo pacman -S fuse3
```

### 2. Add User to fuse Group (Optional)

This allows mounting without root:

```bash
sudo usermod -aG fuse $USER
# Log out and back in for group change to take effect
```

### 3. Install Go

```bash
sudo pacman -S go
```

### 4. Build and Install LinearFS

```bash
git clone https://github.com/jra3/linear-fuse.git
cd linear-fuse
make build
make install  # Copies binary to ~/.local/bin
```

> **Note:** Ensure `~/.local/bin` is in your PATH. Add to your shell profile if needed:
> ```bash
> echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc  # or ~/.bashrc
> ```

### 5. Configure

```bash
mkdir -p ~/.config/linearfs
cat > ~/.config/linearfs/config.yaml << EOF
api_key: "lin_api_YOUR_KEY_HERE"
EOF
```

Or set the environment variable:
```bash
export LINEAR_API_KEY="lin_api_YOUR_KEY_HERE"
```

### 6. Mount

```bash
linearfs mount ~/linear
```

### Arch Linux Troubleshooting

| Issue | Solution |
|-------|----------|
| "fusermount3: command not found" | `sudo pacman -S fuse3` |
| "Permission denied" | Add user to `fuse` group, or run with sudo |
| Mount point busy | `fusermount3 -uz ~/linear` to force unmount |

---

## Ubuntu / Debian

### Install from a released `.deb` (recommended)

Grab the `.deb` for your architecture from the
[latest release](https://github.com/jra3/linear-fuse/releases/latest)
(`linearfs_<version>_linux_amd64.deb` for x86_64, `_linux_arm64.deb` for ARM), then:

```bash
# Optional but recommended: verify SLSA build provenance first (see docs/THREAT-MODEL.md)
gh attestation verify linearfs_<version>_linux_amd64.deb -R jra3/linear-fuse

sudo apt install ./linearfs_<version>_linux_amd64.deb   # resolves the fuse3 dependency
```

This installs `/usr/bin/linearfs` and a systemd **user** unit at
`/usr/lib/systemd/user/linearfs.service`. Set your API key (the *Configure* step
below), then to run it on login:

```bash
mkdir -p ~/.config/linearfs
printf 'LINEAR_API_KEY=lin_api_YOUR_KEY_HERE\nLINEARFS_MOUNT=%s/linear\n' "$HOME" > ~/.config/linearfs/env
chmod 600 ~/.config/linearfs/env
systemctl --user enable --now linearfs.service
```

(Fedora/RHEL/openSUSE users: see the [`.rpm` section](#fedora--rhel--opensuse) below.)

### Build from source

### 1. Install FUSE

```bash
sudo apt update
sudo apt install fuse3 libfuse3-dev
```

### 2. Add User to fuse Group

```bash
sudo usermod -aG fuse $USER
# Log out and back in for group change to take effect
```

### 3. Install Go

```bash
# Option 1: From official repo (may be outdated)
sudo apt install golang-go

# Option 2: From Go website (recommended for latest version)
# Check https://go.dev/dl/ for the latest version
wget https://go.dev/dl/go1.23.linux-amd64.tar.gz  # Replace with latest
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.23.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
```

### 4. Build and Install LinearFS

```bash
git clone https://github.com/jra3/linear-fuse.git
cd linear-fuse
make build
make install  # Copies binary to ~/.local/bin
```

> **Note:** Ensure `~/.local/bin` is in your PATH. Add to your shell profile if needed:
> ```bash
> echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.bashrc
> ```

### 5. Configure

```bash
mkdir -p ~/.config/linearfs
cat > ~/.config/linearfs/config.yaml << EOF
api_key: "lin_api_YOUR_KEY_HERE"
EOF
```

Or set the environment variable:
```bash
export LINEAR_API_KEY="lin_api_YOUR_KEY_HERE"
```

### 6. Mount

```bash
linearfs mount ~/linear
```

### Ubuntu/Debian Troubleshooting

| Issue | Solution |
|-------|----------|
| "fusermount3: command not found" | `sudo apt install fuse3` |
| "fuse: device not found" | `sudo modprobe fuse` |
| "Permission denied" | Add user to `fuse` group, or run with sudo |
| Mount point busy | `fusermount3 -uz ~/linear` to force unmount |

---

## Fedora / RHEL / openSUSE

### Install from a released `.rpm` (recommended)

Grab the `.rpm` for your architecture from the
[latest release](https://github.com/jra3/linear-fuse/releases/latest)
(`linearfs_<version>_linux_amd64.rpm` for x86_64, `_linux_arm64.rpm` for ARM), then:

```bash
# Optional but recommended: verify SLSA build provenance first
gh attestation verify linearfs_<version>_linux_amd64.rpm -R jra3/linear-fuse

sudo dnf install ./linearfs_<version>_linux_amd64.rpm     # openSUSE: sudo zypper install ./linearfs_<version>_linux_amd64.rpm
```

This installs `/usr/bin/linearfs` and a systemd **user** unit at
`/usr/lib/systemd/user/linearfs.service`, and pulls in `fuse3`. Set your API key
(the *Configure* step from any section above applies verbatim), then to run it
on login:

```bash
mkdir -p ~/.config/linearfs
printf 'LINEAR_API_KEY=lin_api_YOUR_KEY_HERE\nLINEARFS_MOUNT=%s/linear\n' "$HOME" > ~/.config/linearfs/env
chmod 600 ~/.config/linearfs/env
systemctl --user enable --now linearfs.service
```

### Build from source

Identical to the Ubuntu/Debian source build, swapping the package manager:

```bash
sudo dnf install fuse3 golang    # openSUSE: sudo zypper install fuse3 go
git clone https://github.com/jra3/linear-fuse.git && cd linear-fuse
make build && make install       # copies to ~/.local/bin (ensure it's on PATH)
```

### Fedora/RHEL/openSUSE Troubleshooting

| Issue | Solution |
|-------|----------|
| "fusermount3: command not found" | `sudo dnf install fuse3` (openSUSE: `zypper install fuse3`) |
| "fuse: device not found" | `sudo modprobe fuse` |
| "Permission denied" | Run with sudo, or ensure `/dev/fuse` is accessible |
| Mount point busy | `fusermount3 -uz ~/linear` to force unmount |

---

## Other Linux distributions

No native package for your distro? Install `fuse3` from your package manager
first (it provides `fusermount3`), then use one of:

### Prebuilt binary tarball

Download `linearfs_<version>_linux_amd64.tar.gz` (or `_linux_arm64`) from the
[latest release](https://github.com/jra3/linear-fuse/releases/latest):

```bash
gh attestation verify linearfs_<version>_linux_amd64.tar.gz -R jra3/linear-fuse   # optional
tar -xzf linearfs_<version>_linux_amd64.tar.gz

# The bundled systemd unit expects the binary at ~/.local/bin/linearfs:
mkdir -p ~/.local/bin && install -m755 linearfs ~/.local/bin/linearfs

# Optional: install the systemd user service shipped inside the archive
mkdir -p ~/.config/systemd/user
install -m644 contrib/systemd/linearfs.service ~/.config/systemd/user/linearfs.service
```

### From Go

```bash
go install github.com/jra3/linear-fuse/cmd/linearfs@latest   # builds to $(go env GOPATH)/bin
```

Then configure (API key + `~/.config/linearfs/env`) and enable the user service
as shown in the sections above.

---

## Environment Variables

Every platform's Configure step above sets `LINEAR_API_KEY`. These are the
environment variables LinearFS reads (all optional except the API key, which can
equally live in `config.yaml`; `HOME` is also consulted as a fallback when the
user config/cache dir cannot be resolved):

| Variable | Config key | Default | Effect |
|---|---|---|---|
| `LINEAR_API_KEY` | `api_key` | — | Linear API key; the env var overrides the config file |
| `USER_FEEDBACK` | `user_feedback` | off | Opt-in agent feedback mode (see below) |
| `XDG_CONFIG_HOME` | — | `~/.config` | Where the config file is looked up: `$XDG_CONFIG_HOME/linearfs/config.yaml`; on Linux it also moves the state files beside it (`cache.db`, `metrics.jsonl`) |
| `XDG_CACHE_HOME` | — | `~/.cache` | Linux only: root of the attachment byte cache, `$XDG_CACHE_HOME/linearfs/files` (macOS always uses `~/Library/Caches`) |
| `LINEARFS_MOUNT` | — | `~/linear` | Mount point, read by the systemd/launchd service files |
| `LINEARFS_DEBUG_API` | — | off | Any non-empty value logs every GraphQL request/response (verbose; may echo workspace content) |
| `LINEARFS_DEBUG_RATE` | — | off | Any non-empty value logs rate-limit budget accounting |

### `USER_FEEDBACK` — agent feedback mode (opt-in, off by default)

```bash
export USER_FEEDBACK=1
```

With this set, the generated `<mount>/README.md` — the doc an agent reads to
learn the filesystem — gains an **agent feedback protocol** section telling the
agent to treat friction with LinearFS's contracts as a bug and file it on the
tool's own repo (`gh issue create --repo jra3/linear-fuse --label dx-friction`),
deduped against open issues, batched to a natural break in its task, leaving a
one-line receipt for you.

Turn it on only if you are dogfooding LinearFS with an agent and want that
friction to reach the issue tracker — `jra3/linear-fuse` is a public repo, so the
protocol tells the agent to report the *shape* of the friction (errno, elided
`.error` reason, placeholder paths) and never to paste workspace content. Left
off — the env var unset *and* `user_feedback` not enabled in `config.yaml` — the
generated README is byte-for-byte the normal one, so a normal install never sees
it. A set env value wins over the file either way: `USER_FEEDBACK=0` (or
`false`/`no`/`off`) turns it off even when the file says true.

## Verification

After mounting, verify LinearFS is working:

```bash
# Check mount
mount | grep linear

# List teams (use ~~/linear on macOS, ~/linear on Linux)
ls ~/linear/teams/        # Linux
ls ~~/linear/teams/       # macOS

# Read an issue (replace TEAM with your team key, e.g., ENG, PROD)
cat ~/linear/teams/TEAM/issues/TEAM-123/issue.md        # Linux
cat ~~/linear/teams/TEAM/issues/TEAM-123/issue.md       # macOS
```

## Unmounting

```bash
# Linux - Clean unmount
fusermount3 -u ~/linear

# Linux - Force unmount (if busy)
fusermount3 -uz ~/linear

# macOS - Unmount
umount ~~/linear
```

## Common Issues

### "LINEAR_API_KEY not set"

Set your API key either via environment variable or config file:

```bash
# Environment variable
export LINEAR_API_KEY="lin_api_YOUR_KEY_HERE"

# Or config file
mkdir -p ~/.config/linearfs
echo 'api_key: "lin_api_YOUR_KEY_HERE"' > ~/.config/linearfs/config.yaml
```

### "Transport endpoint is not connected"

The filesystem crashed or was killed. Force unmount and remount:

```bash
# Linux
fusermount3 -uz ~/linear
linearfs mount ~/linear

# macOS
umount ~~/linear
linearfs mount ~~/linear
```

### "Input/output error"

Usually indicates an API error. Check:
1. Your API key is valid
2. You have network connectivity
3. Linear's API is not down

Run with debug mode for more info:
```bash
# Linux
linearfs mount -d ~/linear

# macOS
linearfs mount -d ~~/linear
```

## Running as a systemd User Service (Linux)

To have LinearFS start automatically on login, set up a systemd user service.

### 1. Copy the Service File

```bash
mkdir -p ~/.config/systemd/user
cp contrib/systemd/linearfs.service ~/.config/systemd/user/
```

### 2. Create the Environment File

The service reads configuration from `~/.config/linearfs/env`:

```bash
mkdir -p ~/.config/linearfs
cat > ~/.config/linearfs/env << 'EOF'
LINEAR_API_KEY=lin_api_YOUR_KEY_HERE
LINEARFS_MOUNT=~/linear
EOF
chmod 600 ~/.config/linearfs/env  # Restrict permissions
```

### 3. Enable and Start

```bash
systemctl --user daemon-reload
systemctl --user enable linearfs.service
systemctl --user start linearfs.service
```

### 4. Check Status

```bash
systemctl --user status linearfs.service
journalctl --user -u linearfs.service -f  # Follow logs
```

### 5. Management Commands

```bash
systemctl --user stop linearfs.service     # Stop
systemctl --user restart linearfs.service  # Restart
systemctl --user disable linearfs.service  # Disable autostart
```

---

## Claude Code Integration

If you use [Claude Code](https://claude.ai/code), you can give it access to your mounted Linear workspace for AI-assisted issue management.

### 1. Add Permissions

Add these permissions to your `~/.claude/settings.json` (adjust the path for your platform):

**Linux:**
```json
{
  "allow": [
    "Read(/~/linear/**)",
    "Bash(ls ~/linear/:*)",
    "Bash(cat ~/linear/:*)"
  ]
}
```

**macOS:**
```json
{
  "allow": [
    "Read(//Users/YOUR_USERNAME~/linear/**)",
    "Bash(ls ~~/linear/:*)",
    "Bash(cat ~~/linear/:*)"
  ]
}
```

This allows Claude Code to read issues, list directories, and view file contents without prompting for approval.

### 2. Add Context

Add to your global `~/.claude/CLAUDE.md` (adjust path for your platform):

**Linux:**
```markdown
# Linear.app issues via FUSE mount on disk
- data is found in ~/linear
- the README.md file should be fully read and understood before reading/writing data there

@~/linear/README.md
```

**macOS:**
```markdown
# Linear.app issues via FUSE mount on disk
- data is found in ~~/linear
- the README.md file should be fully read and understood before reading/writing data there

@~~/linear/README.md
```

The `@` directive automatically imports the mounted filesystem's documentation into Claude's context, giving it full knowledge of the directory structure and available operations.

### 3. Usage

Now you can ask Claude Code things like:
- "What issues are assigned to me?" → reads `my/assigned/`
- "Show me the bug issues" → reads `teams/TEAM/by/label/bug/`
- "What's the status of TEAM-123?" → reads the issue file directly

---

## Building from Source

Requirements:
- Go 1.21+
- make

```bash
git clone https://github.com/jra3/linear-fuse.git
cd linear-fuse
make build      # Build binary to bin/linearfs
make test       # Run tests
make install    # Copy to ~/.local/bin
```

## Updating LinearFS

To update to the latest version:

```bash
cd linear-fuse
git pull
make build
make install

# If running as a service, restart it:
# Linux:
systemctl --user restart linearfs.service

# macOS:
launchctl stop com.linearfs.mount && launchctl start com.linearfs.mount
```
