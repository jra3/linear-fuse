# Contributing to LinearFS

Thanks for your interest — contributions are welcome. This document explains how the
project works so your time is well spent and your PR lands smoothly.

## What this project is (and how it's built)

LinearFS is a FUSE filesystem that exposes [Linear](https://linear.app) as editable
markdown files. It is **actively developed with heavy AI assistance** (Claude Code), which
shapes a few things you should know up front:

- **Commits are squash-merged**, one logical change per PR, titled
  `type(scope): summary (#NN)` (e.g. `fix(fs): bound kernel-notify tail steps`). The history
  is the changelog.
- **The design is written down, not just implied.** Two living documents are load-bearing:
  - [`CONTEXT.md`](CONTEXT.md) — the **domain & architecture vocabulary**. It names every
    load-bearing concept (the *edit path*, the *WriteBack tail*, the *persist gate*, the
    *sync reconcile tail*, …). Read the relevant section before touching an area; use its
    terms in your PR so reviews share one language.
  - [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) — the verified orientation map (data flow,
    package seams, the two governing rules).
- **Docs are part of the diff.** If your change reshapes a package seam, a data flow, or a
  filesystem surface, update `CONTEXT.md` / `docs/ARCHITECTURE.md` **in the same PR**. The
  generated mount README (`internal/fs`) and `docs/ARCHITECTURE.md` can silently lie about
  behavior otherwise — see the discipline notes in [`CLAUDE.md`](CLAUDE.md).

You do **not** need to use AI to contribute. Human PRs are equally welcome; the docs above
are just how the codebase stays navigable.

> **Stability:** this is a `v0` tool — breaking changes are acceptable and expected. Don't
> let that stop you; it just means we optimize for the right design over backward
> compatibility.

## Getting set up

Requirements: **Go 1.25+** and FUSE (`fuse3` on Linux, macFUSE on macOS —
see [`INSTALL.md`](INSTALL.md)).

```bash
git clone https://github.com/jra3/linear-fuse
cd linear-fuse
make build          # build ./bin/linearfs
make test           # unit tests (no API key, no mount needed)
make lint           # golangci-lint
make staticcheck    # staticcheck (pinned version)
make fmt            # gofmt
```

Unit tests run fully offline — no Linear API key, no FUSE mount. **Integration tests**
default to SQLite fixtures (also offline); a live-API mode exists but is optional and
budget-hungry. Run live mode through the make targets rather than by hand — they carry
the `-timeout` the live suite's cold-start sync gate needs (see the integration-test notes
in [`CLAUDE.md`](CLAUDE.md)):

The live targets take their credentials from `.env` (gitignored; copy `.env.example`),
not from your shell and not from a `make VAR=value` argument. Both of those are the wrong
source: an exported `LINEAR_API_KEY` is normally the key for the workspace you actually
work in, and a make-level variable additionally lands in your shell history and in this
`make` process's own `argv`, where any other local user can read it out of `ps`.

```bash
go test ./internal/integration/...    # fixtures (default, no key)

cp .env.example .env                  # then fill in the TEST workspace's key + team
make integration-tests-ro             # live API, READS ONLY
make integration-tests-rw             # live API + writes: CREATES AND MODIFIES REAL LINEAR DATA
```

## The testing philosophy (important)

The codebase testing strategy is **pure-projection extraction**: instead of mocking under
FUSE node methods (which need a live inode tree), we extract the branchy decision logic into
a **pure function or a small module behind a seam**, and unit-test *that* with fakes — no
mount, no SQLite, no API. See `editflush.go`, `manifest.go`, the listing modules, and their
`_test.go` twins for the pattern.

When you add logic, prefer to put the decision in a pure surface and test it directly. A PR
that adds branchy behavior only testable through a mounted filesystem will usually be asked
to extract the decision first.

## Submitting a change

1. **Fork** and create a branch off `main` (any descriptive name — the `john/…` convention
   in `CLAUDE.md` is the maintainer's personal one, not required of you).
2. Make the change. Keep it **one logical unit** — smaller PRs review faster.
3. Ensure green: `make test && make lint && make staticcheck`. Add tests for new behavior.
4. Update `CONTEXT.md` / `docs/ARCHITECTURE.md` if you moved a seam or changed a surface.
5. Open a PR against `main` with a `type(scope): summary` title and a body explaining
   **what** and **why**. Link issues with `Closes #NN` / `Part of #NN`.

CI runs unit tests under `-race`, staticcheck, `govulncheck`, and the integration suite in
offline fixture mode. The write-integration job runs only on manual dispatch with its
"Run write tests" box checked — it authenticates with a real key and mutates real Linear
data, so it never runs on a push or a PR.

## Reporting bugs & proposing features

Use the [issue templates](https://github.com/jra3/linear-fuse/issues/new/choose). For bugs,
the single most useful thing you can include is the contents of the relevant `.error` file
(LinearFS writes the reason for every failed write there) plus `linearfs version` output.

## Security

Please **do not** file security issues as public GitHub issues. See [`SECURITY.md`](SECURITY.md).

## License

By contributing, you agree that your contributions are licensed under the project's
[MIT License](LICENSE).
