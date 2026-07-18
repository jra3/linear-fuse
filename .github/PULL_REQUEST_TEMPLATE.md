<!--
Title format: type(scope): summary   e.g. fix(fs): bound kernel-notify tail steps
Keep the PR to one logical change.
-->

## What & why

<!-- What does this change do, and what problem does it solve? -->

## How it was verified

<!-- Tests added/updated? `make test && make lint && make staticcheck` green? Manual check? -->

## Checklist

- [ ] `make test` passes (unit tests run offline, no API key needed)
- [ ] `make lint` and `make staticcheck` are clean
- [ ] New behavior has a test — ideally a pure-projection unit test, not mount-only
- [ ] Updated `CONTEXT.md` / `docs/ARCHITECTURE.md` if a seam, data flow, or filesystem surface changed
- [ ] Linked related issues (`Closes #NN` / `Part of #NN`)

<!--
New to the codebase? Read the relevant section of CONTEXT.md first — it names the concepts
this project uses (edit path, WriteBack tail, persist gate, …). See CONTRIBUTING.md.
-->
