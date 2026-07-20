# LinearFS Threat Model

This is the security reference for LinearFS: who we defend against, where
untrusted data crosses into the process, and what we deliberately do *not*
defend against. It is the companion to `docs/ARCHITECTURE.md` — the architecture
doc says how the system works; this doc says where it can be attacked.

It exists to answer one recurring question: **"is this change security-relevant?"**
If a change moves remote data closer to a filename, a symlink target, a disk
write, a subprocess, or the secret — or changes a file mode, a fetch URL, or the
build/release path — it crosses a boundary described here and warrants a look.

> Maintained under the same discipline as `docs/ARCHITECTURE.md`: when a change
> adds, removes, or reshapes a trust boundary — a new consumer of remote data, a
> new on-disk artifact, a new network caller, a change to how names become paths
> — update this doc **in the same change**. The discipline is the guard; there is
> no automated test for it.

## What LinearFS is, in security terms

A single-user daemon that mounts one person's Linear workspace as a FUSE
filesystem. Linear is the source of truth; SQLite is a local cache; the
filesystem is the UI. The process holds one secret (the Linear API key), talks
to exactly two remote origins (Linear's GraphQL API and Linear's uploads CDN),
and writes several artifacts to local disk (the SQLite cache, embedded-file
bytes, and optional telemetry/request logs).

The security-interesting fact is that **almost everything the process handles is
attacker-controllable data from a SaaS other people can write to.** A coworker
who can edit an issue in your Linear workspace controls issue titles, project
and document slugs, label names, markdown bodies, and attachment URLs — and this
code turns those into filenames, symlink targets, local disk writes, and SQLite
rows. That is the primary threat, and it is not the threat a generic web-app
checklist looks for.

## Personas in scope

| # | Persona | Controls | Attacks via |
|---|---------|----------|-------------|
| **P1** | Malicious / compromised Linear **workspace member** | Issue titles, project & doc slugs, label names, markdown bodies, attachment titles & URLs, user display names | Filenames, directory names, symlink targets, disk-write paths, SQLite rows — reaching path traversal, arbitrary write, or serving the wrong file |
| **P2** | Compromised **CDN / attachment host** | The bytes returned for an embedded-file fetch, and (via P1) the URL that fetch targets | The embedded-file download path: SSRF (URL pointed at localhost / metadata endpoints), arbitrary local write, unbounded download → disk/memory exhaustion |
| **P3** | Another **local user** on the machine | Nothing in-process; reads whatever LinearFS leaves on disk | The API key (config file, logs) and the cached workspace (SQLite DB, embedded files, telemetry) if their modes are world-readable |
| **P4** | **Supply chain** | The build/release path | The `linearfs-bin` AUR package (PKGBUILD integrity, checksums), CI workflow token scope & unpinned actions, Go module dependencies |

## Trust boundaries

The boundaries below are keyed to the data-flow in `docs/ARCHITECTURE.md`. Each
is a point where data the process does not control enters a context where it can
do harm.

### TB1 — Remote data → filesystem surface (P1)

The load-bearing boundary. Remote strings enter at `api.Client` (from the Linear
GraphQL API) and flow, unchanged in trust, through `internal/marshal` (render)
and into `internal/fs`, where they become **names and targets on a real
filesystem**:

- **Filenames / directory names** — every name/target builder in `internal/fs`
  routes its output through the single `safeName(raw, id)` chokepoint
  (`internal/fs/safename.go`, #345): `cycleDirName`, `userDirName`,
  `sanitizeFilename` (attachment/link `.link` + embedded-file names),
  `labelFilename`, `documentFilename`, `milestoneFilename`, `projectDirName`,
  `initiativeDirName`, `initiativeProjectDirName`, and the `by/` status/label/
  assignee value names. `safeName` replaces `/`, `\`, NUL, and C0 controls with
  `-`, trims trailing spaces/dots, falls back to the stable entity id when the
  result is `""`/`.`/`..`, and escapes an exact collision with a reserved control
  literal (`_create`, `.error`, `.last`, `.meta`, `current`, `unassigned`) by
  appending `-<id>`. Each builder keeps its own cosmetic transform; `safeName` is
  the final safety pass layered over it. A CI grep-rule
  (`scripts/check-safename.sh`, `make check-safename`) flags any builder
  returning a raw remote name field without it.
- **Symlink targets** — `symlinkNode` backs every symlink view (`by/`, `cycles/`,
  `recent/`, `users/`, `my/`, `children/`, project issue links, initiative→project
  links). A target is remote-derived; every interpolated component (issue
  identifier, team key, project dir name) passes through `safeName` so a hostile
  value cannot traverse out of its directory.
- **Disk-write paths** — the embedded-file cache writes bytes to a path derived
  from remote data (see also TB2).

The questions this boundary raises: does a title/slug/label containing `..`, `/`,
a NUL, a leading `-`, an empty string, or a unicode-normalization trick survive
into a path that escapes the mount or the cache dir, collides with another
entity, or serves the wrong file? `safeName` is the answer for the name/target
surfaces; the corpus test (`internal/fs/safename_test.go`, #341) drives the
hostile inputs through every builder. Names that are *resolution keys* (labels
and milestones resolve by name; `.rel` names feed `rm`) carry extra risk — a
mangled name that resolves elsewhere is worse than a broken one, so `safeName`
is deterministic (same raw+id → same output) and does not deduplicate (collision
policy is unchanged; see `namedListing`).

Note the in-scope sliver of the "malicious server" idea lives here too: the
GraphQL/CDN transport must stay HTTPS and must not follow redirects to non-Linear
hosts, because that is the difference between "P1 sends hostile data" (in scope)
and a network attacker injecting it (which the transport must prevent). Enforced:
both network callers refuse every redirect via `CheckRedirect` (`errCDNRedirect`
in the CDN client, #348; `errAPIRedirect` in the GraphQL client, #353), so no
request carrying the API key ever makes a second hop.

### TB2 — Linear CDN → local bytes on disk (P2)

Embedded-attachment bytes are fetched lazily: `embeddedFileCache` calls
`api.CDNClient.Get` on read, and `internal/reconcile`'s `Extractor` calls
`CDNClient.Size` (a HEAD) during sync. The **URL** is parsed out of a
remote-controlled markdown body (P1 supplies it); the **bytes** come from
whatever answers that URL (P2). This boundary asks: is the fetch host pinned to
Linear's CDN (else SSRF via a crafted attachment URL)? Are redirects followed to
arbitrary hosts? Is there a size cap (else an unbounded body exhausts disk or
memory)? Is the local write path constructed safely from remote data?

### TB3 — The secret and the cache, at rest and in transit (P3)

One secret: the Linear API key, loaded by `internal/config` from
`LINEAR_API_KEY` or `~/.config/linearfs/config.yaml`, sent to Linear as a raw
`Authorization` header (`api/client.go`). Two questions: **at rest** — is the
config file's mode restrictive, or world-readable? — and **in transit through
our own logs** — can the key leak into `requests.jsonl` (the optional request
trace), `metrics.jsonl`, `.error` files, error strings, or the `status`
command's output?

Alongside the secret, the whole cached workspace lands on disk: the SQLite cache
DB (`os.UserConfigDir()/linearfs/cache.db`), embedded-file bytes, and the
optional telemetry/request logs. Their file and parent-directory modes decide
whether another local user can read a colleague's entire issue tracker. The
mount itself is always owner-only: FUSE denies other users by default, and
LinearFS never sets `fuse.MountOptions.AllowOther` (the `allow_other` config
key that once suggested otherwise was a dead knob, removed in #355).

**At-rest posture (enforced).** Every on-disk artifact LinearFS writes is
owner-only: `0700` directories, `0600` files. The mode constants and the
best-effort `Chmod` self-heal live in one place, `internal/atrest`, and every
artifact-creating site routes through it — the SQLite dir + `cache.db` (chmodded
*after* open, since the driver creates the file; its `-wal`/`-shm` sidecars are
tightened alongside and otherwise sit inside the `0700` dir), the embedded-file
cache dir + byte files (`internal/fs/embeddedfilecache.go`), and the
telemetry/request logs + their rotated `.1` sidecars (`internal/telemetry/rotate.go`).
The chmod runs at startup on every known artifact regardless of creator, so a
`0644` file an older binary left is tightened on the next start (self-heal) and
future drift self-corrects; a chmod that fails (foreign owner, removed under us)
is logged and swallowed rather than blocking the mount. Separately, `internal/config`
**hard-refuses** to load when the API key's source is `config.yaml` and that file
is group/other-accessible (`mode & 0o077 != 0`), naming the fix (`chmod 600`);
the `LINEAR_API_KEY` env path is the escape hatch and is unaffected. The
mountpoint itself stays `0755` — the FUSE mount is owner-only regardless
(AllowOther is never set), so tightening it is cosmetic.

### TB4 — Build & release (P4)

The path from source to running binary: the `linearfs-bin` AUR package (PKGBUILD
integrity, checksum pinning, build reproducibility), the CI workflows (token
scopes, handling of untrusted input in workflow runs, whether third-party
actions are pinned by commit SHA), and the Go module dependency set.

**Provenance posture (enforced, #354).** Every release artifact (the archives
and `checksums.txt`) carries SLSA build provenance: the release workflow's
attest step signs, via GitHub's OIDC identity (keyless Sigstore), a statement
binding the artifact's digest to this repo, the workflow, and the source
commit. `checksums.txt` alone authenticates nothing — it is produced and
uploadable by the same job that builds the binaries — so verification means
`gh attestation verify <file> -R jra3/linear-fuse` (see SECURITY.md), which
detects an artifact swapped after the build even by an actor holding release
credentials.

## Out of scope

Ruled beyond this effort's destination. These are scoping decisions, not
oversights:

- **Linear-the-company as a fully malicious server.** Linear is the source of
  truth; if it is adversarial, the game is over by definition. It collapses into
  P1/P2 (a hostile server sends the same hostile *data* a hostile workspace
  member can). Only the transport sliver — HTTPS pinning, no redirect to
  non-Linear hosts — is kept, and it lives under TB1/TB2.
- **General DoS / resource-exhaustion hardening.** In scope only where *remote
  data* sizes memory or disk (unbounded CDN downloads, unbounded cache growth) —
  i.e. under TB2. "Survive a hostile 10GB issue body" as a standalone robustness
  campaign is not a goal.

## Non-goals

Not merely deprioritized — explicitly not this system's job:

- **The user's own agent/LLM misusing the mount.** LinearFS faithfully exposes
  what the user's Linear credentials can already reach. Constraining what the
  operator (or an agent acting for them) may do *within* their own permissions is
  Linear's authorization model, not the filesystem's. LinearFS holds one key and
  acts wholly as that one user.
- **Multi-tenant isolation.** LinearFS is a single-user daemon. There is no
  in-process notion of separate principals to isolate.

## How findings are handled

Findings from the audit that produced this doc are filed as public,
`security`-labelled issues on `jra3/linear-fuse`, severity-ranked
(`sev:high` / `sev:medium` / `sev:low`). The realistic blast radius —
local access or a hostile workspace member, on a single-user daemon — makes
public disclosure the right default; anything judged remotely exploitable would
instead go through a GitHub private security advisory first (see `SECURITY.md`).
