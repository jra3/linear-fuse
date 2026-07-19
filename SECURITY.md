# Security Policy

LinearFS's threat model — the personas it defends against, the trust boundaries
where untrusted data enters, and what it deliberately does not defend against —
is documented in [`docs/THREAT-MODEL.md`](docs/THREAT-MODEL.md). This file is the
operator-facing policy: how to report an issue and how to handle your key and
cache safely.

## Reporting a vulnerability

**Please do not report security issues through public GitHub issues.**

Report privately through
[**GitHub's private vulnerability reporting**](https://github.com/jra3/linear-fuse/security/advisories/new)
("Report a vulnerability" under the repository's **Security** tab). If that is unavailable,
email **github@porcnick.com** with details.

Please include a description, reproduction steps, and the impact you foresee. You can expect
an initial acknowledgement within a few days. As a solo-maintained `v0` project, fix
timelines are best-effort — but reports are taken seriously and credited unless you prefer
otherwise.

## Supported versions

Only the latest `main` / most recent release is supported. There are no backported fixes.

## Handling your Linear API key

LinearFS acts entirely on behalf of your Linear API key, so the key **is** the security
boundary. Treat it like a password:

- **A LinearFS API key can read and write everything your Linear account can** — issues,
  projects, comments, documents, labels — across every team you belong to. It cannot change
  workspace settings, manage users, or see billing.
- **Provide it via environment (`LINEAR_API_KEY`) or `~/.config/linearfs/config.yaml`.**
  If you use the config file, restrict its permissions: `chmod 600 ~/.config/linearfs/config.yaml`.
- **Never commit a key.** Keep it out of dotfiles you publish and out of shell history where
  possible.
- Rotate the key in Linear's settings if you suspect exposure; LinearFS picks up the new key
  on restart.

LinearFS does not log the API key (only operation names and variables are logged at debug
level, never the `Authorization` header). If you find a path where a secret is logged,
please report it as a vulnerability.

## Mounted-filesystem exposure

The mount inherits your user's umask. On a **shared machine**, other local users with
filesystem access could read your Linear data through the mount depending on permissions.
Mount with a restrictive umask (`umask 0077`) if this is a concern, and prefer a personal
machine for sensitive workspaces.

## Local cache

LinearFS mirrors your workspace into a local SQLite database (default
`~/.config/linearfs/cache.db`). It contains your Linear data in plaintext and is protected
only by filesystem permissions — treat it with the same care as the mount. Deleting it is
safe; it is a disposable cache and re-syncs on next start.
