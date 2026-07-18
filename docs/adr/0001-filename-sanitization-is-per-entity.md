# Filename sanitization is per-entity, not unified

Each collection derives its filenames with its own small function — `labelFilename`,
`documentFilename`, `milestoneFilename` (all `internal/fs`), and attachments' `linkName`
(via the shared `sanitizeFilename`). Only `sanitizeFilename` has a full safety floor
(strips NUL, trims dots, maps empty → `untitled`); the other three only replace path
separators. We deliberately keep them separate rather than routing all four through one
sanitizer.

## Why

The four derivations differ in **style**, not just safety: labels and documents hyphenate
spaces, documents lowercase, milestones and attachments keep spaces, documents short-circuit
to the unique `SlugID`, and the extension is `.md` vs `.link`. A single sanitizer would have
to carry all that variation as parameters — interface width for four callers.

The safety gap is **not reachable in practice**: these are *virtual FUSE names* used only as
Lookup-matching keys (`entries()` and `find()` re-derive through the same function, and entity
resolution goes through the raw `Name`, never the filename), so a weird-but-deterministic name
is never stranded — it round-trips. The only inputs the missing floor would change are NUL
bytes, all-dots, and empty names, which Linear entity names essentially never are. Unifying
would be near-zero-real-behavior-change hardening that nonetheless churns existing filenames
for the pathological cases and adds a filename-round-trip test obligation across all four.

## Consequences

- The `FuzzSanitizeFilename` target holds **only `sanitizeFilename`** to the path-safety bar
  (no `/` `\` NUL, never `""` / `.` / `..`). The entity derivations are fuzzed for
  `listed ⇒ openable` and emit-once only — the invariants they actually uphold.
- If a real need for uniform path-safety appears (e.g. Linear starts permitting control
  characters in names), the unification is a standalone production PR: extract a `safeName`
  floor, compose each entity's existing style on top, and pin every derivation with a
  filename-round-trip test. This ADR is the license to do it then, not now.
