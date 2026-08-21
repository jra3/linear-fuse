# LinearFS Threat Model

This is the security reference for LinearFS: who we defend against, where
untrusted data crosses into the process (and where workspace content can cross
back out), and what we deliberately do *not* defend against. It is the companion
to `docs/ARCHITECTURE.md` — the architecture doc says how the system works; this
doc says where it can be attacked.

It exists to answer one recurring question: **"is this change security-relevant?"**
If a change moves remote data closer to a filename, a symlink target, a disk
write, a subprocess, the secret, or off the machine entirely — or changes a file
mode, a fetch URL, or the build/release path — it crosses a boundary described
here and warrants a look.

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
bytes, and optional telemetry/request logs). It never initiates a third outbound
origin itself — but with feedback mode on it *instructs* the operator's agent to
publish to one (TB5).

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
| **P4** | **Supply chain** | The build/release path | The `linearfs-bin` AUR package and the `.deb`/`.rpm` release packages (package integrity, checksums), CI workflow token scope & unpinned actions, Go module dependencies |

## Trust boundaries

The boundaries below are keyed to the data-flow in `docs/ARCHITECTURE.md`. Each
is a point where data the process does not control enters a context where it can
do harm — or, for TB5, where content from inside the mount leaves for one.

### TB1 — Remote data → filesystem surface (P1)

The load-bearing boundary. Remote strings enter at `api.Client` (from the Linear
GraphQL API) and flow, unchanged in trust, through `internal/marshal` (render)
and into `internal/fs`, where they become **names and targets on a real
filesystem**:

- **Filenames / directory names** — every name/target builder in `internal/fs`
  routes its output through the single `safeName(raw, id)` chokepoint
  (`internal/fs/safename.go`, #345): `teamDirName`, `cycleDirName`, `userDirName`,
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
  `recent/`, `users/`, `my/`, `children/`, the team hierarchy —
  `teams/{KEY}/parent` and `subteams/` — project issue links,
  initiative→project links). A target is remote-derived; every interpolated
  component (issue identifier, team key, project dir name) passes through
  `safeName` so a hostile value cannot traverse out of its directory. The team
  hierarchy is the case where a remote key crosses *into another entity's*
  target: `parent` interpolates the PARENT team's key, so the render and the
  listing both take it from `parentLinkTarget`, and a parent the local
  workspace copy cannot name yields no entry at all rather than a guessed one.
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

**Accepted collision risk (P1, #333).** Linear permits duplicate label/document/
milestone names, so a hostile member can *deliberately* forge a name that collides
with a victim's to **shadow** it (their entity resolves first) or **strand** it
(the victim's is visible in `readdir`, but Lookup serves the other). The
first-match/emit-once policy holds anyway: the bound is low — confined to the
name-addressed projection (no traversal, no arbitrary write), and Linear itself
shadows same-named entities in-product — and disambiguating (`Bug (2).md`) would
resolve *nowhere*, since `ResolveMilestoneID`/`GetLabelByName` match the raw entity
name; the whole name→entity stack is assume-first. The heterogeneous collections
(attachments/links) *do* deduplicate, because nothing name-resolves them by title;
#333 also aligned their create tail to record and invalidate the **same**
deduplicated name Lookup serves, closing a round-trip strand where a colliding
create landed at a name the reader could not open.

**A remote key now schedules local work (P1, #427).** `team.Key` was already a
path component under this boundary; it is now also an input to the sync
worker's team-key drift check, which counts a team's cached issues whose
identifier prefix disagrees with the team's current key and, on a nonzero
count, deletes and re-fetches that team's issues. So a remote value the
attacker controls can cause local deletes and API refetches. Four things bound
it. The check runs on **full cycles only**, so flipping a key in a loop buys at
most one rebuild per full-cycle interval per team, not one per flip. The delete
is cheap and **scoped to the one affected team** — other teams' directories are
untouched, which is also why the repair is not a cache wipe. The refill takes
its identifiers **from the server**, so a rebuild cannot invent a name in a
namespace LinearFS does not own, and the refilled names re-enter through
`safeName` exactly as fetched ones always have; the worst case is paid-for
detail data re-fetched, not a corrupted path. And the detection predicate is
`substr(identifier, 1, ?) <> ?` through **bound sqlc parameters** — never string
interpolation, and deliberately not `LIKE`/`GLOB`, in which `_`, `%` and `[` in
a remote key would be wildcards.

The same ticket closes the mis-resolution hazard on the other side of this
boundary. Identifier resolution is workspace-wide (it must be: cross-team
project members, sub-issues, parents and relations are all reached through a
containing team's directory), so a stale identifier left by a rename could
resolve to another team's issue once the freed key was reused — and every path
that resolves one hands the result to a mutation, so that was a wrong-issue
**write** reachable by writing a path or a line: `issues/` Lookup captures the
entity `Flush` writes back, issue.md's `parent:` line becomes
`IssueUpdateInput.parentId`, and the relations create surface names the far end
of a relation. All three now validate, through one shared predicate, that the
requested prefix equals the current key of the team that owns the resolved
issue, read from the `teams` row rather than from the issue's own blob, which
goes stale in lockstep with the identifier. A stale identifier reads as an
unknown issue in each — a resolution miss, in the error shape that path already
had.

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

**What the request log carries (answered).** Not the key: it lives only in the
`Authorization` header and nothing in the process renders headers, so neither
sink below can reach it. What both sinks *do* carry is workspace-derived and
remote-controlled. `requests.jsonl` already logged the full `vars` map (entity
IDs, and on a mutation the content being written); since #448 a failed line also
carries an `error` object — `message`, `code`, `type`, `user_error`,
`user_presentable_message` — every string of it server-authored, and Linear
routinely echoes user-supplied entity names back inside them ("The label 'X' is
a group …"). The same decoded rejection lands in the **process log** (journald)
on an always-on `<op> … by Linear API: <fields>` line (the prefix carries the
severity verdict; `docs/ARCHITECTURE.md` owns that shape), which is new reach:
server text now appears in the operator's log without the request log being
enabled at all. So the artifact's sensitivity is unchanged in *kind* — it
was already a workspace trace — but a rejection now quotes back the content that
provoked it, and one sink is no longer opt-in.

Three containments. `requests.jsonl` stays off by default and is `0600` inside a
`0700` directory like every other artifact (below). Every remote string reaching
either sink is escaped on the way in — `json.Marshal` for the JSONL, `%q` for
every field of `GraphQLError.LogDetail()` and for the two raw-body log lines
beside it — so a message containing a newline cannot forge a second line in a
line-oriented log — TB1's discipline (neutralize a remote string before it
becomes structure) applied to a log sink instead of a filename.
And every string on a JSONL line is capped at 2 KB with an explicit truncation
marker, because a non-GraphQL failure's message embeds the entire response body
from an unbounded `io.ReadAll`: a proxy answering with a multi-MB error page
would otherwise write one line the rotating writer lets through whole (it never
splits a single oversize write) and no downstream reader can hold. Journald
applies its own limits to the process-log side.

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
is logged, counted (`linearfs.atrest.chmod_failures{artifact}`, #352), and
swallowed rather than blocking the mount. Separately, `internal/config`
**hard-refuses** to load when the API key's source is `config.yaml` and that file
is group/other-accessible (`mode & 0o077 != 0`), naming the fix (`chmod 600`);
the `LINEAR_API_KEY` env path is the escape hatch and is unaffected. The
mountpoint itself stays `0755` — the FUSE mount is owner-only regardless
(AllowOther is never set), so tightening it is cosmetic.

**A developer checkout holds a second copy of the secret.** `.env` in the repo
root is the local home for live-test credentials (`LINEAR_API_KEY`,
`LINEARFS_TEST_TEAM`); the live make targets source it into the recipe's own
shell (`set -a; . ./.env; set +a`) and refuse to run without it. Three properties
keep it from becoming a leak. It is gitignored, so it cannot be committed — the
one control that actually matters, since a key in git history outlives any
chmod. It is deliberately *not* `~/.config/linearfs/env`, which is the systemd
unit's `EnvironmentFile`: a throwaway-workspace test key placed there would
silently repoint the running mount, so the split is a correctness boundary as
much as a security one. And **nothing chmods it** — `internal/atrest` covers
artifacts LinearFS *writes*, and this file is created by hand, so a default
`umask` leaves it `0644` and world-readable to P3 (observed in practice). Unlike
`config.yaml` there is no load-time mode refusal, because the reader is `make`,
not `internal/config`. Treat `chmod 600 .env` as the developer's job; the same
caveat applies to any shell that `export`s the key, whose value is visible in
`/proc/<pid>/environ` to its owner alone but lands in shell history if typed.

### TB4 — Build & release (P4)

The path from source to running binary: the release artifacts goreleaser
produces on a tag — the per-platform archives and the `.deb`/`.rpm` system
packages (Debian/Ubuntu, Fedora/RHEL/openSUSE) — and the `linearfs-bin` AUR
package that repins from them (package integrity, checksum pinning, build
reproducibility), the CI workflows (token scopes, handling of untrusted input in
workflow runs, whether third-party actions are pinned by commit SHA), and the Go
module dependency set.

**CI's use of the live workspace credential (#386).** One workflow authenticates
to Linear with a real key and mutates a real workspace: the `workflow_dispatch`-only
"Integration Tests (Write)" job, which sets `LINEARFS_LIVE_API=1` +
`LINEARFS_WRITE_TESTS=1` and runs every test gated behind `skipIfNoWriteTests`. Before #386 it set `LINEARFS_WRITE_TESTS` but never
`LINEARFS_LIVE_API` — and `skipIfNoWriteTests` skips on `!liveAPIMode` first — so
the injected `LINEAR_API_KEY` secret went unused and the job silently ran the
offline fixture suite; the label promised mutation and the run delivered none.
Now that it does what it says, the exposure is real and worth stating: whoever can dispatch that
workflow can write to the Linear workspace the secret belongs to, so the secret
should scope to a throwaway/test workspace rather than a production one, and the
job now states which workspace it accepts rather than trusting the secret to be
the right one: it sets `LINEARFS_TEST_TEAM`, and setup fails if the key's
workspace has no team by that key (`pickTestTeam`). That check is the enforcement
point for "throwaway workspace, not a production one" — a rotated-in key for the
wrong workspace fails before the first mutation instead of writing to whatever
team it found. Local runs get the same guarantee from `.env`, which the live make
targets read in place of the ambient environment for the same reason: a developer's
exported `LINEAR_API_KEY` is normally their real workspace. The exposure this
closes is not hypothetical — a local write run against a work workspace is how it
was found, its `TST` team having been enough to satisfy the old fallback. The
job stays manual-dispatch (never `push`/`pull_request`, never `pull_request_target`,
where a fork could reach it) behind its `run_write_tests` confirmation input — which
was itself declared-but-unread until #386 wired it to a job-level `if`, so the
"creates/modifies Linear data" box is now load-bearing and an unchecked run never
puts the secret on a runner. The job also refuses to start the suite when the
secret resolves empty, rather than degrading back to the fixture suite and
reporting green: `liveAPIMode` is `LINEARFS_LIVE_API=1 && apiKey != ""`, so a
rotated-away or renamed secret would otherwise reinstate exactly the #386 lie one
layer down. The `test.yml` read-only job injects the same secret but leaves
`LINEARFS_LIVE_API` unset, so it cannot use it — an unused secret in a job's env
is exposure without benefit, and whether that job goes live-read or is deleted
along with the secret is open in #386.

Locally the same credential reaches the same suite through `make
integration-tests-ro` (live, reads only), `make integration-tests-rw` (live,
CREATES AND MODIFIES REAL LINEAR DATA), and `make integration-tests` (both, in
that order); the default `make test` needs no key and touches no network. Those
targets read the key out of `.env` and never name it in a recipe line, which
closes two P3 reads that the earlier `LINEAR_API_KEY=$(LINEAR_API_KEY) go test …`
prefix left open: make echoes recipe lines with variables already expanded, so
the raw key was printed into every terminal scrollback and CI log, and it sat in
the test process's `argv` for the length of a run — up to 25 minutes of `ps` for
any other local user. The emptiness guard reads `$$LINEAR_API_KEY` (the shell's
own environment, sourced from `.env` a statement earlier) rather than expanding a
make variable, so the value never enters a command string at all; `bench-dirs`,
which still takes its key from the ambient environment, guards the same way for
the same reason. Passing the key as a make-level argument (`make
integration-tests-rw LINEAR_API_KEY=…`) re-opens both reads against `make`'s own
process and your shell history, and is now inert besides — `.env` overrides it.
`CONTRIBUTING.md` documents the `.env` form instead. The
read-only target is read-only by enforcement, not convention: the write-contract
guards that write through the mount now skip under a live key (`skipIfLiveAPI`),
so a `-ro` run cannot leak a probe issue into the workspace. #395 widened the set
of tests that a `-ro` run reaches — the fixture-only read tests gained the guard,
and the mount-level contract tests that had no guard now derive their issue and
project from the workspace instead of hardcoding `TST-1` — without widening what
it can do: those tests stat, read, and fsync clean handles (an fsync with no bytes
written flushes an empty buffer, which every create surface no-ops on), and the
only writes they attempt are the ones asserted to be *rejected* by a read-only
`.meta` sidecar, which has no writer to reach the API with. Live mode also opens
its SQLite cache in a per-run temp dir instead of `db.DefaultDBPath()`, so a
developer's real `~/.config/linearfs/cache.db` — normally held open by a running
linearfs service — is never written by a test run.

**Provenance posture (enforced, #354).** Every release artifact — the archives,
the `.deb`/`.rpm` packages, and `checksums.txt` — carries SLSA build provenance:
the release workflow's attest step signs, via GitHub's OIDC identity (keyless
Sigstore), a statement binding the artifact's digest to this repo, the workflow,
and the source commit. `checksums.txt` alone authenticates nothing — it is
produced and uploadable by the same job that builds the binaries — so
verification means `gh attestation verify <file> -R jra3/linear-fuse` (see
SECURITY.md), which detects an artifact swapped after the build even by an actor
holding release credentials. An apt/dnf user can verify the downloaded package
the same way before installing it.

**Maintainer scripts.** `apt`/`dnf`/`pacman` run a package's maintainer scripts
as root, so they are part of what provenance verification is protecting. The
invariant across every channel: **the maintainer scripts LinearFS ships only
print setup guidance — none of them act.** They create no files, touch nothing
under the installing user's `$HOME`, and run no network or package-manager
commands. Two exist:

- `contrib/nfpm/postinstall.sh` — the `.deb`/`.rpm`, wired in as nfpm's
  `scripts.postinstall`.
- `contrib/aur/linearfs-bin.install` — the AUR package's `post_install` and
  `post_upgrade`. `post_upgrade` additionally *queries* whether the user unit is
  running (`systemctl --user is-active`) so it can print a restart hint; that is
  a read-only check, not a state change.

Everything else the packages place on disk is static content (the binary, the
systemd user unit, docs, LICENSE). Keep it that way; a maintainer script that
acts is a root-privileged step no user reviews.

### TB5 — Mount content → a public GitHub issue, via the operator's agent (P1)

**Off by default; opt-in per operator.** With `USER_FEEDBACK` set (env, or
`user_feedback` in `config.yaml`), the generated `<mount>/README.md` gains one
static section — `agentFeedbackProtocol` in `internal/fs/root.go` — telling the
agent reading the mount to treat friction with LinearFS's contracts as a bug and
file it on `jra3/linear-fuse` itself, autonomously, without human approval. That
makes LinearFS the *origin* of an outbound path: it is the only place the tool
tells anyone to move workspace-derived content off the machine.

The content at risk is P1's. A hostile workspace member controls the issue
titles, bodies, label names, and field values that a `.error` echoes back, so an
unredacted report carries two harms: **disclosure** (private workspace strings
published to a public repo) and **injection** (attacker-authored text landing in
the maintainer's triage context).

Mitigations, in order of load-bearing-ness:

1. **Off by default, and off means off.** The flag-off README is byte-for-byte
   the plain one — the protocol is not merely inert, it is absent, so a normal
   install never carries the instruction. Enabling it is an operator decision
   about one specific mount.
2. **Report the shape, not the payload.** The protocol carries an explicit
   REDACTION block: the errno and the `.error` reason line with any echoed field
   *value* elided; paths with the team key and issue identifier replaced by
   placeholders (`teams/<TEAM>/issues/<ID>/issue.md`); from `.last`, only whether
   the entity is present and its outcome (created / failed) — a failed entry's
   `error:` line is workspace-derived and its echoed field values are elided
   exactly like the `.error` reason; never titles, bodies, assignee names or emails,
   project/initiative/milestone/label names, or URLs into the workspace; and
   never a verbatim quote of text the agent did not author — summarize it, or
   skip filing. `internal/fs/readme_test.go` pins the rule so it cannot silently
   drop out of the const.

**Residual risk, accepted.** This is best-effort, not an enforcement boundary. The
redaction rule is in-context instruction to an LLM, and nothing in LinearFS
inspects, filters, or even sees what the agent posts — there is no egress
chokepoint to enforce at. An operator who turns feedback mode on accepts that a
hostile workspace-member-controlled string could still reach a public issue or a
maintainer's triage context. The opt-in *is* the control: do not enable feedback
mode on a mount of a workspace whose contents you would not publish.

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
  acts wholly as that one user. **This covers only what the agent decides to do
  on its own.** Where LinearFS itself *instructs* the agent to move workspace
  content somewhere — feedback mode's public issue filing — the instruction is
  ours, and so is the mitigation: that is TB5, in scope, not this non-goal.
- **Multi-tenant isolation.** LinearFS is a single-user daemon. There is no
  in-process notion of separate principals to isolate.

## How findings are handled

Findings from the audit that produced this doc are filed as public,
`security`-labelled issues on `jra3/linear-fuse`, severity-ranked
(`sev:high` / `sev:medium` / `sev:low`). The realistic blast radius —
local access or a hostile workspace member, on a single-user daemon — makes
public disclosure the right default; anything judged remotely exploitable would
instead go through a GitHub private security advisory first (see `SECURITY.md`).
