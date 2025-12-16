# Installation Guide

This guide covers installing LinearFS on macOS, Arch Linux, and Ubuntu/Debian.

## Prerequisites (All Platforms)

- **Go 1.21+** - for building from source
- **Linear API Key** - get one from [Linear Settings → API](https://linear.app/settings/api)

## macOS

### 1. Install macFUSE

macFUSE provides the FUSE kernel extension for macOS.

```bash
brew install --cask macfuse
```

After installation, you must approve the kernel extension:

1. Open **System Settings** → **Privacy & Security**
2. Scroll down to find the blocked extension from "Benjamin Fleischer"
3. Click **Allow**
4. Restart your Mac

> **Note:** On Apple Silicon Macs, you may need to enable kernel extensions in Recovery Mode:
> 1. Shut down your Mac
> 2. Press and hold the power button until "Loading startup options" appears
> 3. Select **Options** → **Startup Security Utility**
> 4. Select **Reduced Security** and check "Allow user management of kernel extensions"

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
mkdir -p /tmp/linear
linearfs mount /tmp/linear
```

### macOS Troubleshooting

| Issue | Solution |
|-------|----------|
| "macFUSE is not installed" | Reboot after installing macFUSE |
| "System Extension Blocked" | Approve in System Settings → Privacy & Security |
| "Operation not permitted" | Check SIP isn't blocking; try mounting to ~/mnt instead |

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
mkdir -p /tmp/linear
linearfs mount /tmp/linear
```

### Arch Linux Troubleshooting

| Issue | Solution |
|-------|----------|
| "fusermount3: command not found" | `sudo pacman -S fuse3` |
| "Permission denied" | Add user to `fuse` group, or run with sudo |
| Mount point busy | `fusermount3 -uz /tmp/linear` to force unmount |

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
wget https://go.dev/dl/go1.23.4.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.23.4.linux-amd64.tar.gz
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
mkdir -p /tmp/linear
linearfs mount /tmp/linear
```

### Ubuntu/Debian Troubleshooting

| Issue | Solution |
|-------|----------|
| "fusermount3: command not found" | `sudo apt install fuse3` |
| "fuse: device not found" | `sudo modprobe fuse` |
| "Permission denied" | Add user to `fuse` group and edit `/etc/fuse.conf` |
| Mount point busy | `fusermount3 -uz /tmp/linear` to force unmount |

---

## Verification

After mounting, verify LinearFS is working:

```bash
# Check mount
mount | grep linear

# List teams
ls /tmp/linear/teams/

# Read an issue
cat /tmp/linear/teams/YOUR_TEAM/issues/ISSUE-123/issue.md
```

## Unmounting

```bash
# Clean unmount
fusermount3 -u /tmp/linear

# Force unmount (if busy)
fusermount3 -uz /tmp/linear
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
fusermount3 -uz /tmp/linear
linearfs mount /tmp/linear
```

### "Input/output error"

Usually indicates an API error. Check:
1. Your API key is valid
2. You have network connectivity
3. Linear's API is not down

Run with debug mode for more info:
```bash
linearfs mount -d /tmp/linear
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

> **Note:** The mount point (`/mnt/linear`) must be writable by your user. Create it with:
> ```bash
> sudo mkdir -p /mnt/linear && sudo chown $USER:$USER /mnt/linear
> ```

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
