# Quick Start Guide

Get started with linear-fuse in 5 minutes!

## Prerequisites

- Go 1.20+ installed
- FUSE support on your system
- Linear API key

## Installation

### From Source

```bash
git clone https://github.com/jra3/linear-fuse.git
cd linear-fuse
make build
```

The binary will be created as `linear-fuse` in the current directory.

### Install to PATH

```bash
make install
```

This installs the binary to `$GOPATH/bin`, which should be in your PATH.

## Configuration

### Get Your API Key

1. Go to https://linear.app/settings/api
2. Generate a new Personal API Key
3. Copy the key

### Set Up Environment

```bash
export LINEAR_API_KEY="lin_api_xxxxxxxxxxxx"
```

Or create a config file at `~/.linear-fuse.yaml`:

```yaml
api-key: lin_api_xxxxxxxxxxxx
```

## First Mount

### 1. Create a Mount Point

```bash
mkdir ~/linear-issues
```

### 2. Mount the Filesystem

```bash
linear-fuse mount ~/linear-issues
```

You should see:
```
Mounted Linear filesystem at /home/user/linear-issues
Press Ctrl+C to unmount
```

### 3. Browse Your Issues

Open a new terminal and explore:

```bash
# List all issues
ls ~/linear-issues/

# View an issue
cat ~/linear-issues/ENG-123.md

# Edit an issue
vim ~/linear-issues/ENG-123.md
```

### 4. Unmount

Press `Ctrl+C` in the terminal running linear-fuse.

## Common Use Cases

### View Issues by State

```bash
linear-fuse mount --layout=by-state ~/linear-issues
```

Then browse:
```bash
ls ~/linear-issues/              # Lists states: Todo, In Progress, Done, etc.
ls ~/linear-issues/Todo/         # Lists all Todo issues
cat ~/linear-issues/Todo/ENG-123.md
```

### View Issues by Team

```bash
linear-fuse mount --layout=by-team ~/linear-issues
```

Then browse:
```bash
ls ~/linear-issues/              # Lists teams: ENG, DESIGN, PRODUCT, etc.
ls ~/linear-issues/ENG/          # Lists all ENG team issues
cat ~/linear-issues/ENG/ENG-123.md
```

### Create a New Issue

```bash
cat > ~/linear-issues/NEW-FEATURE.md << EOF
Implement user authentication

Add OAuth2 authentication support for Google and GitHub.

## Requirements
- Google OAuth
- GitHub OAuth
- Session management
EOF
```

The issue will be created in Linear immediately!

### Edit an Issue

```bash
# Open in your favorite editor
vim ~/linear-issues/ENG-123.md

# Or use sed, awk, etc.
sed -i 's/priority: 1/priority: 2/' ~/linear-issues/ENG-123.md
```

Changes are synced to Linear when you save.

### Grep Through Issues

```bash
# Find issues mentioning "authentication"
grep -r "authentication" ~/linear-issues/

# Find high priority issues
grep -r "priority: 1" ~/linear-issues/
```

### Use with Git

```bash
# Clone issues as a git repo
cd ~/linear-issues
git init
git add .
git commit -m "Initial snapshot of Linear issues"

# Track changes over time
git diff
```

## Tips

1. **Use Debug Mode**: Add `--debug` flag to see what's happening:
   ```bash
   linear-fuse mount --debug ~/linear-issues
   ```

2. **Auto-mount on Login**: Add to your shell's rc file (`.bashrc`, `.zshrc`):
   ```bash
   linear-fuse mount ~/linear-issues &
   ```

3. **Use with Your Workflow**: Integrate with your text editor, scripts, or automation tools.

4. **Backup Your Work**: Issues are synced in real-time, but consider keeping backups of important content.

## Troubleshooting

### "API key is required"

Make sure you've set the API key via environment variable or config file.

### "Mount failed"

- Ensure FUSE is installed on your system
- Check that the mount point exists and is empty
- Try with `--debug` flag for more information

### "Permission denied"

Make sure you have permission to access the mount point and that FUSE is properly installed.

### Changes Not Syncing

- Check your internet connection
- Verify your API key is valid
- Look for errors in the debug output

## Next Steps

- Read the full [README](../README.md) for detailed documentation
- Check out [CONTRIBUTING](../CONTRIBUTING.md) to contribute
- Open an issue if you find bugs or have feature requests

## Need Help?

- [Open an issue](https://github.com/jra3/linear-fuse/issues)
- Check existing issues for similar problems
- Read the documentation in the `docs/` directory
