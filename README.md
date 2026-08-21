# LinearFS

Mount your Linear workspace as a FUSE filesystem. Browse and edit issues as markdown files.

## Project status

**LinearFS is a `v0` tool — capable and daily-usable, but with no stability guarantees.**
Breaking changes are acceptable and expected. It is solo-maintained with heavy AI assistance,
and **contributions are welcome** — see [CONTRIBUTING.md](CONTRIBUTING.md).

- **For:** CLI-first people who'd rather `ls`/`cat`/`grep`/`vim` their issues than click a UI,
  and anyone scripting against Linear through ordinary file operations.
- **Not for:** real-time collaboration (sync is eventually consistent) or production tooling
  that needs a stable API — use Linear's [official API](https://developers.linear.app) for that.
- **Handling secrets?** Read [SECURITY.md](SECURITY.md) before mounting, especially on a shared machine.
- **Known limitations & bugs** are tracked as [GitHub issues](https://github.com/jra3/linear-fuse/issues).

## Features

- Browse teams, issues, projects, initiatives, and labels as directories/files
- Issues rendered as markdown with YAML frontmatter
- Edit frontmatter to update issue status, assignee, priority, labels
- Full CRUD for comments, documents, and labels
- Create/archive issues and projects with standard filesystem operations
- Multiple views: by team, by user, personal (assigned/created/active)
- Initiatives with linked projects

## Installation

See [INSTALL.md](INSTALL.md) for detailed platform-specific installation instructions.

### Prebuilt packages (recommended)

**Arch Linux** — [`linearfs-bin`](https://aur.archlinux.org/packages/linearfs-bin) on the AUR:

```bash
yay -S linearfs-bin        # or: paru -S linearfs-bin
```

**Debian/Ubuntu (`.deb`) and Fedora/RHEL/openSUSE (`.rpm`)** — download the package
for your architecture from the [latest release](https://github.com/jra3/linear-fuse/releases/latest):

```bash
# Debian/Ubuntu (swap _linux_arm64 for ARM)
sudo apt install ./linearfs_<version>_linux_amd64.deb

# Fedora/RHEL/openSUSE
sudo dnf install ./linearfs_<version>_linux_amd64.rpm
```

Every release artifact carries [SLSA build provenance](docs/THREAT-MODEL.md) —
verify it before installing:

```bash
gh attestation verify linearfs_<version>_linux_amd64.deb -R jra3/linear-fuse
```

### Build from source

```bash
# Build from source
make build

# Install to ~/.local/bin
make install
```

### Requirements

- **Go 1.25+**
- **FUSE filesystem:**
  - **macOS:** macFUSE (`brew install --cask macfuse`)
    - ⚠️ Apple Silicon requires enabling kernel extensions in Recovery Mode
  - **Linux:** FUSE3 (`sudo pacman -S fuse3` on Arch, `sudo apt install fuse3` on Debian/Ubuntu, `sudo dnf install fuse3` on Fedora) — installed automatically by the prebuilt packages

## Usage

```bash
# Set your Linear API key
export LINEAR_API_KEY="lin_api_xxxxx"

# Mount the filesystem
linearfs mount ~/linear

# Browse your issues (replace TEAM with your team key)
ls ~/linear/teams/
ls ~/linear/teams/TEAM/issues/
cat ~/linear/teams/TEAM/issues/TEAM-123/issue.md

# View comments on an issue
ls ~/linear/teams/TEAM/issues/TEAM-123/comments/
cat ~/linear/teams/TEAM/issues/TEAM-123/comments/001-2025-01-10T14-30.md

# Add a comment
echo "My comment" > ~/linear/teams/TEAM/issues/TEAM-123/comments/_create

# View your assigned issues
ls ~/linear/my/assigned/

# Unmount
# macOS
umount ~/linear

# Linux
fusermount3 -u ~/linear

# or Ctrl+C if running in foreground
```

## Checking status

`linearfs status` prints a health snapshot — the live mount, the local cache
(workspace size, last full sync, pending detail-sync backlog), and, when the
JSONL metrics export is enabled, the current rate-limit budget. It reads the
local cache and config read-only, so it works whether or not the daemon is
running:

```bash
$ linearfs status
linearfs v0.1.0 (abc1234), built 2026-07-18T…, go1.25 linux/amd64

Mount:
  /home/you/linear  [live]

Cache:
  db:        ~/.config/linearfs/cache.db
  size:      41.3 MiB
  teams:     4
  issues:    3397
  last full sync: 9m (2026-07-18 10:36)
  pending detail sync: 0 issues

Budget:
  requests:   10 / 2,500 used (0.4%), resets in 59m
  complexity: 9,372 / 3,000,000 used (0.3%), resets in 59m
```

A wedged mount ("Transport endpoint is not connected") is reported with the
`fusermount3 -uz` recovery command. The budget line needs the JSONL export on
(`telemetry.file.enabled: true`); otherwise it points you at the journald
summary.

## File Permissions

Use `ls -l` to see what operations are allowed on each file:

| Permission | Meaning | Example |
|------------|---------|---------|
| `-r--r--r--` | Read-only | `team.md`, `states.md`, every `*.meta` sidecar |
| `-rw-r--r--` | **Editable** | `issue.md`, `project.md`, `initiative.md`, existing docs/comments/labels |
| `--w-------` | Write-only trigger | `_create` (creates new items) |
| `lrwxrwxrwx` | Symlink | Issues in cycles/projects/filtered views |

**Important:** Existing documents and comments are editable. Edit them directly—don't write to `_create` to update existing content.

## File Timestamps

Files and directories have meaningful timestamps from Linear, enabling time-based sorting:

```bash
# Sort by modification time (most recently updated first)
ls -lt ~/linear/my/active/
ls -lt ~/linear/teams/TEAM/by/status/Todo/

# Sort by creation time (oldest first)
ls -ltr ~/linear/teams/TEAM/issues/
```

> **Note:** If using `eza` (aliased as `ls`), use `ls --sort=modified` instead of `ls -lt`.

| Timestamp | Source | Example |
|-----------|--------|---------|
| **mtime** (modified) | Issue's `updatedAt` | Last edit to issue |
| **ctime** (changed) | Issue's `createdAt` | When issue was created |
| **atime** (accessed) | Same as mtime | Not separately tracked |

Timestamps are preserved across all views:
- Issue directories and symlinks in `/my/`, `/users/`, `/by/`, `/cycles/`, `/projects/`
- Project and initiative directories
- Cycle directories (use cycle start/end dates)

**Note:** All files are owned by the user who mounted the filesystem, not `root`.

## Directory Structure

```
~/linear/
├── README.md                    # In-filesystem documentation
├── teams/
│   └── <TEAM>/                  # Your team key (e.g., ENG, PROD)
│       ├── team.md              # Team metadata (read-only)
│       ├── states.md            # Workflow states (read-only)
│       ├── labels.md            # Labels reference (read-only)
│       ├── parent               # Symlink to parent team (sub-teams only)
│       ├── subteams/            # Sub-team symlinks (teams/ itself stays flat)
│       ├── by/                  # Filter issues by attribute
│       │   ├── status/<name>/   # Issues filtered by status (symlinks)
│       │   ├── label/<name>/    # Issues filtered by label (symlinks)
│       │   └── assignee/<name>/ # Issues by assignee (includes "unassigned")
│       ├── issues/
│       │   └── <TEAM-nnn>/       # Issue identifier (e.g., TEAM-123)
│       │       ├── issue.md     # Editable fields + description (read/write)
│       │       ├── issue.meta   # Server-managed fields (read-only)
│       │       ├── comments/
│       │       │   ├── 001-*.md   # Comments (read/write/delete)
│       │       │   ├── 001-*.meta # Server-managed comment fields (read-only)
│       │       │   └── _create    # Write here to create comment
│       │       ├── docs/
│       │       │   ├── *.md     # Issue documents (read/write/rename/delete)
│       │       │   ├── *.meta   # Server-managed document fields (read-only)
│       │       │   └── _create   # Write here to create document
│       │       ├── children/    # Sub-issues (symlinks to sibling issues)
│       │       ├── .error       # Last validation error (read-only)
│       │       └── .last        # Outcome log for recent creates (read-only)
│       ├── labels/              # Label management
│       │   ├── *.md             # Labels (read/write/rename/delete)
│       │   ├── *.meta           # Server-managed label fields (read-only)
│       │   └── _create           # Write here to create label
│       ├── docs/                # Team documents
│       │   ├── *.md             # Documents (read/write/rename/delete)
│       │   ├── *.meta           # Server-managed document fields (read-only)
│       │   └── _create           # Write here to create document
│       ├── cycles/              # Sprint cycles
│       │   ├── current          # Symlink to active cycle (if any)
│       │   └── <cycle-name>/    # Cycle directories with issue symlinks
│       └── projects/
│           └── <project-slug>/
│               ├── project.md   # Editable fields + content (read/write)
│               ├── project.meta # Server-managed fields (read-only)
│               ├── docs/        # Project documents
│               ├── updates/     # Status updates (write to _create)
│               └── TEAM-*       # Symlinks to issue directories
├── initiatives/
│   └── <initiative-slug>/
│       ├── initiative.md        # Editable fields + content (read/write)
│       ├── initiative.meta      # Server-managed fields (read-only)
│       ├── projects/            # Symlinks to team projects
│       └── updates/             # Status updates (write to _create)
├── users/
│   └── <username>/
│       ├── user.md              # User metadata (read-only)
│       └── TEAM-*               # Symlinks to issue directories
└── my/
    ├── assigned/                # Issues assigned to you
    ├── created/                 # Issues you created
    └── active/                  # Non-completed assigned issues
```

## Issue File Format

Every editable file is split in two: the `.md` holds only the fields you can
change, and a read-only `.meta` sidecar next to it holds the server-managed
ones. So `issue.md` is the editable frontmatter plus the description body:

```markdown
---
title: "Fix authentication bug"
status: "In Progress"
assignee: "alice@example.com"
priority: high
labels:
  - bug
  - backend
parent: TEAM-100
---

The login flow fails when users attempt to authenticate with SSO.
```

and `issue.meta` holds what Linear owns — identity and timestamps, plus
`creator`, `branch`, `links` and `relations` when the issue has them:

```markdown
---
id: "abc123-def456"
identifier: TEAM-123
url: "https://linear.app/myworkspace/issue/TEAM-123"
created: 2025-01-10T10:30:00Z
updated: 2025-01-11T14:22:00Z
---
```

The same split applies to `project.md`/`project.meta`,
`initiative.md`/`initiative.meta`, and `<name>.md`/`<name>.meta` for each
comment, document, label and milestone.

### Editable Fields

- `title` - Issue title
- `team` - Team key; changing it **moves** the issue, and Linear re-numbers it
  (TEAM-123 becomes OTHER-45), so the old path stops existing
- `status` - Workflow state name (check states.md for valid values)
- `assignee` - User email or name
- `priority` - none/low/medium/high/urgent
- `labels` - List of label names (check labels.md for valid values)
- `due` - Due date (YYYY-MM-DD format)
- `estimate` - Point estimate
- `parent` - Parent issue identifier (e.g., TEAM-100)
- `project` - Project name
- `milestone` - Milestone name within the project
- `cycle` - Cycle name
- Description (content after frontmatter)

That list is exhaustive and enforced. A frontmatter key `issue.md` does not
accept — a misspelling, or a server-managed key that belongs in `issue.meta` —
fails the whole write with `EINVAL`; nothing partially applies, and `.error`
names the offending key and lists the accepted ones. (`issues/_create` is laxer
in one way: it ignores the read-only keys, so a spec assembled from a rendered
`issue.md` plus its `issue.meta` still creates.)

### Validation Errors

Writes fail with `EINVAL` (Invalid argument) for invalid frontmatter values. After a failed write, check the `.error` file to see what went wrong:

```bash
# Example: invalid priority value
$ echo "priority: critical" >> ~/linear/teams/TEAM/issues/TEAM-123/issue.md
# Write fails with EINVAL

$ cat ~/linear/teams/TEAM/issues/TEAM-123/.error
Field: priority
Value: "critical"
Error: invalid priority "critical": must be none, low, medium, high, or urgent
Time: 2025-01-15T09:41:07Z
```

`Time:` is when the error was recorded. It matters for a collection `.error`
(`comments/`, `docs/`, `labels/`, `milestones/`), which is directory-level and is
retired only by the next successful write to that directory — the timestamp and the
`Operation:` line are how you tell an earlier file's failure from your own.

**Validated fields:** status, assignee, labels, priority, project, milestone, cycle, parent

**Reference files:** Check `states.md` for valid workflow states, `labels.md` for valid labels,
and `team.md` for the team's issue-creation defaults — the state new issues open in, whether triage
intercepts them, and the estimation scale `estimate:` is denominated in (its `defaults` block is
absent when the team's settings haven't synced yet, which is not the same as "all off").

The `.error` file is cleared on successful writes, with one exception: a save whose
body Linear reformatted server-side leaves an informational note there ("saved, but
Linear reformatted the markdown server-side") — the write succeeded and no text was
lost.

## File Operations

LinearFS maps standard filesystem operations to Linear API actions:

### Issues

| Operation | Command | Effect |
|-----------|---------|--------|
| Create issue | `mkdir issues/"Issue title"` | Creates new issue with title |
| Archive issue | `rmdir issues/TEAM-123` | Archives issue (soft delete) |
| Edit issue | Edit `issue.md` and save | Updates issue fields |

```bash
# Create a new issue
mkdir ~/linear/teams/TEAM/issues/"Fix login bug"

# Archive an issue
rmdir ~/linear/teams/TEAM/issues/TEAM-123
```

### Sub-Issues

| Operation | Command | Effect |
|-----------|---------|--------|
| View sub-issues | `ls issues/TEAM-123/children/` | Lists child issues as symlinks |
| Set parent | Edit `parent:` in issue.md | Sets parent issue |
| Remove parent | Remove `parent:` line | Clears parent relationship |

```bash
# View sub-issues of TEAM-123
ls ~/linear/teams/TEAM/issues/TEAM-123/children/

# Set parent by editing frontmatter (editors work here, unlike _create)
# Add: parent: TEAM-100
vim ~/linear/teams/TEAM/issues/TEAM-456/issue.md
```

### Comments

| Operation | Command | Effect |
|-----------|---------|--------|
| Read comments | `cat comments/001-*.md` | View comment content |
| Create comment | `echo "text" > comments/_create` | Posts new comment |
| Edit comment | Edit comment file and save | Updates comment |
| Delete comment | `rm comments/001-*.md` | Deletes comment |

> **Note:** `_create` is a write-only trigger file. It's always empty (0 bytes) and cannot be read.
> Write content to it using `echo` or `cat` with redirect. Editors that read before writing won't work.

```bash
# Add a comment
echo "This needs review" > ~/linear/teams/TEAM/issues/TEAM-123/comments/_create

# Delete a comment
rm ~/linear/teams/TEAM/issues/TEAM-123/comments/001-2025-01-10T14-30.md
```

### Documents

| Operation | Command | Effect |
|-----------|---------|--------|
| Create document | `echo "..." > docs/_create` | Creates document with title from frontmatter |
| Edit document | Edit doc file and save | Updates title/content |
| Rename document | `mv docs/old.md docs/_create` | Renames document title |
| Delete document | `rm docs/spec.md` | Deletes document |

> **Note:** `_create` is a write-only trigger file (see Comments section above).

```bash
# Create a document (with YAML frontmatter for title)
cat > ~/linear/teams/TEAM/issues/TEAM-123/docs/_create << 'EOF'
---
title: "Technical Spec"
---
Document content here...
EOF

# Rename a document
mv docs/old-name.md docs/new-name.md
```

### Labels

| Operation | Command | Effect |
|-----------|---------|--------|
| Create label | `echo "..." > labels/_create` | Creates label with name/color |
| Edit label | Edit label file and save | Updates name/color/description |
| Rename label | `mv labels/Bug.md labels/Defect.md` | Renames label |
| Delete label | `rm labels/OldLabel.md` | Deletes label |

> **Note:** `_create` is a write-only trigger file (see Comments section above).

`labels/<name>.md` accepts exactly `name`, `color` and `description`, and has no
body — a label has no content field. Any other key, or text below the closing
`---`, fails the whole write with `EINVAL` and the reason lands in
`labels/.error`. Quote hex colors (`color: '#FF0000'`); unquoted, YAML reads
`#FF0000` as a comment.

Clearing works differently here than on `issue.md`: an **absent** key leaves that
field unchanged rather than clearing it, so write `description: ""` to send an
empty description.

```bash
# Create a new label
cat > ~/linear/teams/TEAM/labels/_create << 'EOF'
---
name: "Critical"
color: "#FF0000"
description: "Critical priority items"
---
EOF

# Rename a label
mv ~/linear/teams/TEAM/labels/Bug.md ~/linear/teams/TEAM/labels/Defect.md

# Delete a label
rm ~/linear/teams/TEAM/labels/OldLabel.md
```

### Projects

| Operation | Command | Effect |
|-----------|---------|--------|
| Create project | `mkdir projects/"Project Name"` | Creates new project |
| Archive project | `rmdir projects/project-slug` | Archives project (soft delete) |

```bash
# Create a new project
mkdir ~/linear/teams/TEAM/projects/"Q1 Launch"

# Archive a project
rmdir ~/linear/teams/TEAM/projects/q1-launch
```

### Team Documents

Teams can have their own documents separate from issues:

| Operation | Command | Effect |
|-----------|---------|--------|
| List documents | `ls teams/TEAM/docs/` | Shows team documents |
| Create document | `echo "..." > teams/TEAM/docs/"Title.md"` | Creates document with title |
| Edit document | Edit doc file and save | Updates title/content |
| Delete document | `rm teams/TEAM/docs/spec.md` | Deletes document |

```bash
# Create a team document
cat > /mnt/linear/teams/TEAM/docs/"Engineering Standards.md" << 'EOF'
---
title: "Engineering Standards"
---
Our coding standards and best practices...
EOF
```

### Project Updates

Post status updates to projects with health indicators:

| Operation | Command | Effect |
|-----------|---------|--------|
| Create update | `echo "..." > projects/slug/updates/_create` | Posts status update |
| View updates | `ls projects/slug/updates/` | Lists all updates |

```bash
# Post a project status update
cat > /mnt/linear/teams/TEAM/projects/q1-launch/updates/_create << 'EOF'
---
health: atRisk
---
We're blocked on external API dependencies. ETA for resolution is next week.
EOF
```

Health values: `onTrack`, `atRisk`, `offTrack`

### Editing Labels on Issues

Edit the `labels` array in an issue's frontmatter:

```yaml
---
title: "Fix bug"
status: "In Progress"
labels:
  - Bug
  - Backend
  - Critical
---
```

Save the file to update the issue's labels in Linear.

## Freshness and Caching

Metadata reads never touch the Linear API. Every listing, and every issue,
project, comment, document and label, is served from a local SQLite store, so
`ls` and `cat` are local operations that cannot hang on a slow or unreachable
Linear — and cannot show you anything newer than what the store holds. (The
exception is an embedded attachment's *bytes*: a `*.png` or `*.pdf` under an
issue's `attachments/` is fetched from Linear's CDN on first read and cached on
disk after that.) Three things keep the store current:

- **The background sync worker** polls Linear and reconciles what it fetches into
  SQLite. Most cycles are lean (recently-changed issues and their details) and
  run every couple of minutes; a fuller cycle that re-drains team metadata —
  states, labels, cycles, projects, members — and the workspace runs roughly
  every ten minutes.
- **Read-triggered refreshes** cover the surfaces a cycle reaches slowly. When a
  read finds its surface stale (older than five minutes, or thirty while the
  mount is catching up on a large backlog), it kicks a refresh off in the
  background and **still returns the bytes it already has** —
  stale-while-revalidate, never a blocking fetch. The refresh lands behind you,
  so a teammate's change shows up on a *later* read, not on the one that noticed
  it was stale. Issue detail and history, project/initiative/team documents,
  status updates, external links, and the team label catalog work this way.
- **Your own writes** go to Linear and into SQLite in the same operation, so
  anything you change through the mount is visible immediately.

Remote *deletions* are the one thing that can linger: most read-triggered
refreshes only add and update, so an entity deleted in Linear may keep listing
until a sync cycle licensed to prune reconciles it. The team label catalog is
the exception — its refresh fetches the whole catalog, so it prunes too, and a
label deleted in Linear can disappear between full cycles.

For the detail — which surfaces refresh on read, how staleness is decided for
each, and what licenses a prune — see
[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

### FUSE Kernel Caching

Underneath all of that, the Linux kernel caches filesystem attributes and
directory entries on its own:

Mount defaults are **60s attr / 30s entry**, overridable per mount
(`fs.WithKernelCacheTimeouts`). Each surface then picks one named policy
(`internal/fs/renderfile.go`):

- `inheritTimeout` — everything not listed below: teams and their subdirectories,
  cycles, `my/`, `by/`, users, the root views. Mount defaults, 60s attr / 30s entry.
- `mountDefaultTimeout` — the `.meta`/`.error`/`.last` sidecars, `teams/<KEY>/docs`,
  `teams/<KEY>/labels`, and the project and initiative directories' own children
  (`project.md`, `initiative.md`, their sidecars and subdirs). Also mount defaults.
- the mount's **entry** bound — the issue, project and initiative directories
  themselves, the issue directory's children (`issue.md` and its static siblings),
  and both attachment kinds. 30s by default, for *both* attr and entry.
- `editableFileTimeout` — the editable `.md` files under `comments/`, `docs/`,
  `labels/` and `milestones/`. 5 seconds for both.
- `transientFileTimeout` — `_create` and the scratch file an editor's atomic save
  renames over. 1 second for both.

No surface is uncached: `mountDefaultTimeout` and `inheritTimeout` are two
spellings of one policy, and both resolve to the mount's configured defaults.

This reduces kernel-to-userspace calls, but means `ls` output can lag slightly
behind a refresh that has already landed in SQLite. Writes through the mount
punch through it: the write tails invalidate the affected kernel entries and
inodes, which is why your own changes are visible right away.

### Limitations

- **No real-time sync**: Linear's WebSocket-based sync engine is internal only; the public API offers webhooks (requires HTTP server) but not subscriptions
- **Eventual consistency**: a teammate's change appears once a sync cycle or a
  read-triggered refresh has landed it, not on the read that first sees it stale
- **Rate limits**: Linear meters API keys on two axes — request count and query *complexity* —
  and reports both on every response. LinearFS governs itself against the live limits from
  those headers (a priority ladder sheds background detail fetches first). Bulk reads over a
  large workspace can still exhaust the hourly budget; reads keep working — they are served
  from SQLite regardless — but the store then falls further behind Linear until the budget recovers.

## Configuration

Create `~/.config/linearfs/config.yaml`:

```yaml
api_key: "lin_api_xxxxx"  # or use LINEAR_API_KEY env var

cache:
  ttl: 60s

mount:
  default_path: ~/linear

log:
  level: info

user_feedback: false  # or use USER_FEEDBACK env var (see below)
```

### Environment variables

| Variable | Effect |
|---|---|
| `LINEAR_API_KEY` | API key; overrides `api_key` in the config file |
| `USER_FEEDBACK` | Opt-in agent feedback mode (default off); overrides `user_feedback` in the config file |
| `XDG_CONFIG_HOME` | Config lookup root — `$XDG_CONFIG_HOME/linearfs/config.yaml` (default `~/.config`) |
| `LINEARFS_MOUNT` | Mount point used by the systemd/launchd service files (default `~/linear`) |
| `LINEARFS_DEBUG_API` | Any non-empty value logs every GraphQL request/response |
| `LINEARFS_DEBUG_RATE` | Any non-empty value logs rate-limit budget accounting |

The common ones; see [INSTALL.md](INSTALL.md#environment-variables) for the full
table with defaults and config-file keys.

#### `USER_FEEDBACK` — agent feedback mode

Set `USER_FEEDBACK=1` and the generated `<mount>/README.md` gains an **agent
feedback protocol** section: an agent reading the mount is told to treat any
friction with the filesystem's contracts (a confusing `.error`, a write that
appears to no-op, a doc that turned out not to be true) as a bug in LinearFS and
to file it on this repo itself — `gh issue create --repo jra3/linear-fuse --label
dx-friction`, deduped against open issues, batched to a natural break in its
task, with a one-line receipt for the human.

Because this repo is public, the protocol also carries a redaction rule: the
agent reports the *shape* of the friction — the errno, the `.error` reason line
with any echoed field value elided, paths with the team key and issue identifier
replaced by placeholders — and never pastes issue/document titles or bodies,
people's names, or anything else it read out of your workspace. That rule is an
instruction to an LLM, not an enforced boundary; see
[docs/THREAT-MODEL.md](docs/THREAT-MODEL.md) (TB5) for the accepted residual risk.

It is off by default, and off means off: with the env var unset *and*
`user_feedback` not enabled in `config.yaml`, the generated README is
byte-for-byte the normal one. Turn it on if you are dogfooding LinearFS with an
agent and want its friction to reach the issue tracker; leave it off otherwise.
Reported friction lands under the
[`dx-friction`](https://github.com/jra3/linear-fuse/labels/dx-friction) label.

## Running as a Service

### macOS (launchd)

To start LinearFS automatically on login:

```bash
# Install the service
cp contrib/launchd/com.linearfs.mount.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.linearfs.mount.plist
launchctl start com.linearfs.mount

# Your Linear workspace will now be mounted at ~/linear on every login
```

See [INSTALL.md](INSTALL.md#running-as-a-launchd-service-automatic-startup) for details.

### Linux (systemd)

```bash
# Install the service — skip these two lines if you installed the AUR/.deb/.rpm
# package, which already ships the unit pointed at /usr/bin/linearfs
mkdir -p ~/.config/systemd/user
cp contrib/systemd/linearfs.service ~/.config/systemd/user/

systemctl --user enable linearfs.service
systemctl --user start linearfs.service
```

See [INSTALL.md](INSTALL.md#running-as-a-systemd-user-service-linux) for details.

## Contributing

Contributions are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md) — it explains the
development workflow, the testing philosophy, and the two living design docs
([`CONTEXT.md`](CONTEXT.md) and [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md)) that keep the
codebase navigable. Bugs and feature ideas go through the
[issue templates](https://github.com/jra3/linear-fuse/issues/new/choose); security reports
go through [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE) © John Allen
