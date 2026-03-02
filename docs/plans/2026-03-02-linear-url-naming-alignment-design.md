# Design: Linear URL Naming Alignment

**Date:** 2026-03-02
**Status:** Approved

## Problem

The LinearFS filesystem naming conventions are inconsistent with Linear's own URL scheme. Linear uses a `{normalized-name}-{12-char-slugId}` pattern for projects and initiatives, and a bare integer for cycles, but the FS uses name-only slugs for projects/initiatives and name-based strings for cycles.

### Audit Results

| Entity | Linear URL | Current FS | Issue |
|--------|-----------|------------|-------|
| Issues | `/issue/ENG-2019/title-slug` | `ENG-2019/` | ✅ Correct |
| Teams | `/team/ENG/` | `ENG/` | ✅ Correct |
| Projects | `/project/api-client-libraries-7ed5886b9e52/` | `api-client-libraries/` | ❌ Missing slugId suffix |
| Initiatives | `/initiative/context-engineering-d8d0cb56ce31/` | `context-engineering/` | ❌ Missing slugId suffix |
| Cycles | `/team/ENG/cycle/76` | `Cycle-28/` | ❌ Uses name, should use `cycle.Number` |
| Documents | (not verified) | `7ed5886b9e52.md` | ❌ Missing title prefix |

### Data Verified from Live Workspace and DB

- `project.Slug` (API `slugId`) = 12-char hex hash (e.g., `feb3b4efd664`) — hash only, name not included
- `initiative.Slug` (API `slugId`) = 12-char hex hash (e.g., `77d439e363bb`)
- `cycle.Number` = the integer used in Linear's URL (e.g., `76` for "Cycle 28", `77` for "Cycle 29") — confirmed from DB
- `cycle.Name` = user-facing display name (e.g., "Cycle 28")
- Linear constructs full project/initiative URL slugs as `{normalized-name}-{slugId}`

## Design

### Naming Changes

| Entity | Old | New | Data field |
|--------|-----|-----|-----------|
| Projects | `onboarding-revamp` | `onboarding-revamp-feb3b4efd664` | `project.Slug` |
| Initiatives | `context-engineering` | `context-engineering-d8d0cb56ce31` | `initiative.Slug` |
| Initiative/projects/ symlinks | `onboarding-revamp` | `onboarding-revamp-feb3b4efd664` | `proj.Slug` |
| Cycles | `Cycle-28` | `76` | `cycle.Number` |
| Documents | `7ed5886b9e52.md` | `my-doc-title-7ed5886b9e52.md` | `doc.Title` + `doc.SlugID` |

### Implementation

Five localized function changes:

1. **`internal/fs/projects.go`** — `projectDirName(project)`:
   Append `-{project.Slug}` to the normalized name. All lookup/readdir/rmdir code calls through this function symmetrically.

2. **`internal/fs/initiatives.go`** — `initiativeDirName(init)`:
   Append `-{init.Slug}` to the normalized name.

3. **`internal/fs/initiatives.go`** — `initiativeProjectDirName(proj)`:
   Append `-{proj.Slug}` to the normalized name (for symlinks inside `initiative/X/projects/`).

4. **`internal/fs/cycles.go`** — `cycleDirName(cycle)`:
   Return `strconv.Itoa(cycle.Number)`. The `current` symlink target auto-updates since it calls `cycleDirName()`.

5. **`internal/fs/documents.go`** — `documentFilename(doc)`:
   Prepend the normalized title to the existing `SlugID`: `{title-slug}-{slugId}.md`.

### Properties

- **Collision-free**: Two projects with the same name get different directories
- **Rename-stable**: The slugId suffix is immutable even if the entity is renamed (though the name prefix would change — same as Linear's own URL behavior)
- **Consistent**: Matches Linear's URL scheme exactly for all entity types
- **Breaking change**: Existing hardcoded paths (scripts, `.claude/settings.json` permissions) must be updated after deployment

### Non-Changes

- Issue directories remain `{IDENTIFIER}/` (already correct)
- Team directories remain `{KEY}/` (already correct)
- `by/`, `users/`, `my/` paths unchanged
- Resolution logic (ResolveProjectID, etc.) works by entity name/slug, not dir name — unaffected
