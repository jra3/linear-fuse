# Installation Guide

This guide covers installing LinearFS on macOS, Arch Linux, and Ubuntu/Debian.

## Prerequisites (All Platforms)

- **Go 1.21+** - for building from source
- **Linear API Key** - get one from [Linear Settings → API](https://linear.app/settings/api)

## Mount Point

This guide uses `/mnt/linear` as the standard mount point. Create it before mounting:

```bash
# Linux
sudo mkdir -p /mnt/linear && sudo chown $USER:$USER /mnt/linear

# macOS
sudo mkdir -p /mnt/linear
```

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
make install  # Copies binary to ~/bin
```

> **Note:** Ensure `~/bin` is in your PATH. Add to your shell profile if needed:
> ```bash
> echo 'export PATH="$HOME/bin:$PATH"' >> ~/.zshrc  # or ~/.bashrc
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
linearfs mount /mnt/linear
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

The default mount point is `/mnt/linear`. To customize it, edit the env file:

```bash
mkdir -p ~/.config/linearfs
cat > ~/.config/linearfs/env << 'EOF'
LINEAR_API_KEY=lin_api_YOUR_KEY_HERE
LINEARFS_MOUNT=/mnt/linear
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
sudo mkdir -p /mnt/linear
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

## Arch Linux

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
make install  # Copies binary to ~/bin
```

> **Note:** Ensure `~/bin` is in your PATH. Add to your shell profile if needed:
> ```bash
> echo 'export PATH="$HOME/bin:$PATH"' >> ~/.zshrc  # or ~/.bashrc
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
linearfs mount /mnt/linear
```

### Arch Linux Troubleshooting

| Issue | Solution |
|-------|----------|
| "fusermount3: command not found" | `sudo pacman -S fuse3` |
| "Permission denied" | Add user to `fuse` group, or run with sudo |
| Mount point busy | `fusermount3 -uz /mnt/linear` to force unmount |

---

## Ubuntu / Debian

### 1. Install FUSE

```bash
sudo apt update
sudo apt install fuse3 libfuse3-dev
```

### 2. Configure FUSE (Optional)

To allow non-root users to mount FUSE filesystems:

```bash
sudo nano /etc/fuse.conf
# Uncomment the line: user_allow_other
```

### 3. Add User to fuse Group

```bash
sudo usermod -aG fuse $USER
# Log out and back in for group change to take effect
```

### 4. Install Go

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

### 5. Build and Install LinearFS

```bash
git clone https://github.com/jra3/linear-fuse.git
cd linear-fuse
make build
make install  # Copies binary to ~/bin
```

> **Note:** Ensure `~/bin` is in your PATH. Add to your shell profile if needed:
> ```bash
> echo 'export PATH="$HOME/bin:$PATH"' >> ~/.bashrc
> ```

### 6. Configure

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

### 7. Mount

```bash
linearfs mount /mnt/linear
```

### Ubuntu/Debian Troubleshooting

| Issue | Solution |
|-------|----------|
| "fusermount3: command not found" | `sudo apt install fuse3` |
| "fuse: device not found" | `sudo modprobe fuse` |
| "Permission denied" | Add user to `fuse` group and edit `/etc/fuse.conf` |
| Mount point busy | `fusermount3 -uz /mnt/linear` to force unmount |

---

## Verification

After mounting, verify LinearFS is working:

```bash
# Check mount
mount | grep linear

# List teams
ls /mnt/linear/teams/

# Read an issue (replace TEAM with your team key, e.g., ENG, PROD)
cat /mnt/linear/teams/TEAM/issues/TEAM-123/issue.md
```

## Unmounting

```bash
# Clean unmount
fusermount3 -u /mnt/linear

# Force unmount (if busy)
fusermount3 -uz /mnt/linear
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
fusermount3 -uz /mnt/linear
linearfs mount /mnt/linear
```

### "Input/output error"

Usually indicates an API error. Check:
1. Your API key is valid
2. You have network connectivity
3. Linear's API is not down

Run with debug mode for more info:
```bash
linearfs mount -d /mnt/linear
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
LINEARFS_MOUNT=/mnt/linear
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

Add these permissions to your `~/.claude/settings.json`:

```json
{
  "allow": [
    "Read(//mnt/linear/**)",
    "Bash(ls /mnt/linear/:*)",
    "Bash(cat /mnt/linear/:*)"
  ]
}
```

This allows Claude Code to read issues, list directories, and view file contents without prompting for approval.

### 2. Add Context

Add to your global `~/.claude/CLAUDE.md`:

```markdown
# Linear.app issues via FUSE mount on disk
- data is found in /mnt/linear
- the README.md file should be fully read and understood before reading/writing data there

@/mnt/linear/README.md
```

The `@/mnt/linear/README.md` directive automatically imports the mounted filesystem's documentation into Claude's context, giving it full knowledge of the directory structure and available operations.

### 3. Usage

Now you can ask Claude Code things like:
- "What issues are assigned to me?" → reads `/mnt/linear/my/assigned/`
- "Show me the bug issues" → reads `/mnt/linear/teams/TEAM/by/label/bug/`
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
make install    # Copy to ~/bin
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
