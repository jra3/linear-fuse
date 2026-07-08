package fs

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// Project-label selection (see CONTEXT.md): the pure half of the workspace
// project-label surface — catalog rendering here, name→ID resolution and the
// selection policy in this file's siblings (write path). Everything in this
// file is unit-testable with a literal catalog slice: no mount, no interface.

// projectLabelsMarkdown renders the root project-labels.md catalog. The
// assignment rules live IN the file — it is what an agent reads after a
// validation .error — and the render is stable for an empty catalog (never
// ENOENT; the README promises the file exists). The frontmatter goes through
// renderWithFrontmatter so hostile names (`Q3: Bets`, quotes) stay valid YAML.
func projectLabelsMarkdown(labels []api.ProjectLabel) []byte {
	entries := make([]map[string]any, 0, len(labels))
	var table strings.Builder
	for _, l := range labels {
		entry := map[string]any{"id": l.ID, "name": l.Name}
		if l.Color != "" {
			entry["color"] = l.Color
		}
		if l.Description != "" {
			entry["description"] = l.Description
		}
		if l.IsGroup {
			entry["group"] = true
		}
		if l.Parent != nil {
			parent := l.Parent.Name
			if parent == "" {
				parent = l.Parent.ID
			}
			entry["parent"] = parent
		}
		if l.RetiredAt != nil {
			entry["retired"] = true
		}
		entries = append(entries, entry)

		group := "—"
		if l.Parent != nil {
			group = l.Parent.Name
			if group == "" {
				group = l.Parent.ID
			}
		}
		color := "—"
		if l.Color != "" {
			color = l.Color
		}
		var flags []string
		if l.IsGroup {
			flags = append(flags, "group (assign a child)")
		}
		if l.RetiredAt != nil {
			flags = append(flags, "retired")
		}
		table.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			l.Name, group, color, strings.Join(flags, ", "), l.ID))
	}

	body := table.String()
	if len(labels) == 0 {
		body = "No project labels defined.\n"
	} else {
		body = `| Name | Group | Color | Flags | ID |
|------|-------|-------|-------|-----|
` + body
	}

	return renderWithFrontmatter(map[string]any{"labels": entries}, fmt.Sprintf(`
# Project Labels (workspace-wide)

Assign in any project.md frontmatter: `+"`labels: [Platform, Q3-Bet]`"+`
(Names are resolved case-insensitively; a raw label ID is also accepted.)

Rules:
- Labels marked `+"`group: true`"+` are containers and CANNOT be assigned; assign one
  of their children instead.
- At most ONE child from each group may be on a project at a time.
- Labels marked `+"`retired: true`"+` cannot be newly assigned; existing assignments remain.

%s`, body))
}

// projectLabelCatalogTimes derives the catalog file's times from the entities
// themselves — mtime = newest UpdatedAt, ctime = oldest CreatedAt; zero when
// the catalog is empty (renderFile's never-fabricate-now() contract).
func projectLabelCatalogTimes(labels []api.ProjectLabel) (mtime, ctime time.Time) {
	for _, l := range labels {
		if l.UpdatedAt.After(mtime) {
			mtime = l.UpdatedAt
		}
		if !l.CreatedAt.IsZero() && (ctime.IsZero() || l.CreatedAt.Before(ctime)) {
			ctime = l.CreatedAt
		}
	}
	return mtime, ctime
}

const seeCatalog = " See project-labels.md for valid project labels."

// resolveProjectLabels resolves a project.md labels: list (names and/or raw
// IDs) against the catalog, returning the deduplicated ID set plus the
// resolved entities for validation. currentIDs is the project's present
// labelIds set.
//
// Resolution order per entry:
//  1. Exact-ID passthrough — a catalog ID, or a current-member ID absent from
//     the catalog (stale/cold catalog). The round-trip invariant: the render
//     side emits unknown IDs verbatim, so re-saving an untouched file must
//     resolve them back, never EINVAL.
//  2. Case-insensitive name match. On a duplicate-name tie: prefer a label
//     already on the project (untouched files round-trip), then the single
//     active candidate over retired ones, else a loud ambiguity error listing
//     candidate IDs — never a silent sibling pick (assign by ID to
//     disambiguate).
func resolveProjectLabels(catalog []api.ProjectLabel, names []string, currentIDs map[string]bool) ([]string, []api.ProjectLabel, *FieldError) {
	byID := make(map[string]api.ProjectLabel, len(catalog))
	byName := make(map[string][]api.ProjectLabel)
	for _, l := range catalog {
		byID[l.ID] = l
		key := strings.ToLower(l.Name)
		byName[key] = append(byName[key], l)
	}

	var ids []string
	var selected []api.ProjectLabel
	seen := make(map[string]bool)
	add := func(l api.ProjectLabel) {
		if !seen[l.ID] {
			seen[l.ID] = true
			ids = append(ids, l.ID)
			selected = append(selected, l)
		}
	}

	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if l, ok := byID[name]; ok { // exact catalog ID
			add(l)
			continue
		}
		if currentIDs[name] { // current member the catalog does not know
			add(api.ProjectLabel{ID: name, Name: name})
			continue
		}
		candidates := byName[strings.ToLower(name)]
		switch len(candidates) {
		case 0:
			return nil, nil, &FieldError{Field: "labels", Value: name,
				Message: "unknown project label." + seeCatalog}
		case 1:
			add(candidates[0])
		default:
			picked := false
			for _, c := range candidates { // (a) prefer a label already applied
				if currentIDs[c.ID] {
					add(c)
					picked = true
					break
				}
			}
			if !picked { // (b) prefer the single active candidate
				var active []api.ProjectLabel
				for _, c := range candidates {
					if c.RetiredAt == nil {
						active = append(active, c)
					}
				}
				if len(active) == 1 {
					add(active[0])
					picked = true
				}
			}
			if !picked { // (c) loud ambiguity, never a coin flip
				candIDs := make([]string, len(candidates))
				for i, c := range candidates {
					candIDs[i] = c.ID
				}
				return nil, nil, &FieldError{Field: "labels", Value: name,
					Message: fmt.Sprintf("ambiguous project label name; %d labels share it. Assign by ID instead: %s.",
						len(candidates), strings.Join(candIDs, ", "))}
			}
		}
	}
	return ids, selected, nil
}

// validateProjectLabelSelection enforces the documented assignment semantics
// BEFORE any mutation — deliberately stricter than the API where its
// enforcement is lax (live-verified 2026-07-08: the wire accepts retired
// assignment; the docs forbid it). Policy in one sentence: we enforce what
// Linear's docs say about label assignment, even where the API is lax.
// Rules: a group label cannot be applied (the error names its assignable
// children); a retired label cannot be NEWLY applied (existing assignments
// carry through — labelIds is a full-set write, so carried labels re-send);
// at most one child per group among the selected set.
func validateProjectLabelSelection(selected []api.ProjectLabel, currentIDs map[string]bool, catalog []api.ProjectLabel) *FieldError {
	childrenOf := func(groupID string) []string {
		var names []string
		for _, l := range catalog {
			if l.Parent != nil && l.Parent.ID == groupID && !l.IsGroup && l.RetiredAt == nil {
				names = append(names, l.Name)
			}
		}
		return names
	}

	groupPick := make(map[string]string) // group ID -> first selected child name
	for _, l := range selected {
		if l.IsGroup {
			msg := fmt.Sprintf("%q is a label group and cannot be applied directly.", l.Name)
			if children := childrenOf(l.ID); len(children) > 0 {
				msg += " Apply one of its children instead: " + strings.Join(children, ", ") + "."
			}
			return &FieldError{Field: "labels", Value: l.Name, Message: msg + seeCatalog}
		}
		if l.RetiredAt != nil && !currentIDs[l.ID] {
			return &FieldError{Field: "labels", Value: l.Name,
				Message: fmt.Sprintf("%q is retired and cannot be newly applied (existing assignments are unaffected).", l.Name) + seeCatalog}
		}
		if l.Parent != nil {
			groupName := l.Parent.Name
			if groupName == "" {
				groupName = l.Parent.ID
			}
			if prev, ok := groupPick[l.Parent.ID]; ok {
				return &FieldError{Field: "labels",
					Message: fmt.Sprintf("at most one child from each label group may be applied: %q and %q are both children of %q.",
						prev, l.Name, groupName) + seeCatalog}
			}
			groupPick[l.Parent.ID] = l.Name
		}
	}
	return nil
}

// labelsEdit is the labels front half of a project.md edit — sibling of
// scalarEdit (scalar fields) and reconcileLinks (per-pair link lists) in the
// edit-front-half family. It COMPOSES this file's pure primitives
// (resolveProjectLabels, validateProjectLabelSelection, sameIDSet) and owns the
// whole label decision in one place: parse the frontmatter list, fire the
// stale-blob clobber guard in exactly the at-risk state, resolve + validate,
// compute "changed" exactly once, map onto the update input (pointer-or-omit),
// and classify the read-your-writes divergence. ProjectInfoNode.Flush used to
// smear those concerns across three points (the front block, the input build,
// and the commitWriteBack compare closure), stating the change test twice.
type labelsEdit struct {
	desiredIDs []string // resolved target set; nil means clear (when changed)
	isChanged  bool     // the ONE change decision, computed at construction
}

// newLabelsEdit evaluates the labels edit. rawLabels/labelsPresent are the
// parsed doc's `labels` frontmatter value and key presence; currentIDs is the
// project's present label set. catalog fetches the workspace project-label
// catalog (a fetch error degrades to a nil catalog — exact-ID passthrough via
// the current set still resolves, the round-trip invariant). refreshCurrent is
// the stale-blob clobber guard's fetch; the module decides WHEN it fires.
//
// Stale-blob clobber guard: a project blob predating the LabelIds field reads
// current as empty, so an agent ADDING one label would full-set-write just that
// label and silently wipe the project's real labels in Linear — invisible to
// the divergence check (fresh == sent). refreshCurrent fires exactly once, in
// exactly the at-risk state: current empty AND the write would apply labels.
//
// Change semantics (the mount-wide contract): key absent + current empty =
// untouched; key absent + current non-empty = delete-the-line clear; an
// explicit empty list clears too. The set diff is order-insensitive.
//
// A validation failure returns a *FieldError; the caller maps it to
// SetWriteError + EINVAL.
func newLabelsEdit(ctx context.Context, rawLabels any, labelsPresent bool, currentIDs []string,
	catalog func(context.Context) ([]api.ProjectLabel, error),
	refreshCurrent func(context.Context) []string,
) (labelsEdit, *FieldError) {
	desiredNames := marshal.StringSliceFromYAML(rawLabels)

	if len(currentIDs) == 0 && len(desiredNames) > 0 {
		currentIDs = refreshCurrent(ctx)
	}

	currentIDSet := make(map[string]bool, len(currentIDs))
	for _, id := range currentIDs {
		currentIDSet[id] = true
	}

	var e labelsEdit
	if labelsPresent || len(currentIDs) > 0 {
		if len(desiredNames) > 0 {
			cat, err := catalog(ctx)
			if err != nil {
				cat = nil // ID passthrough via currentIDSet still resolves
			}
			ids, selected, ferr := resolveProjectLabels(cat, desiredNames, currentIDSet)
			if ferr == nil {
				ferr = validateProjectLabelSelection(selected, currentIDSet, cat)
			}
			if ferr != nil {
				return labelsEdit{}, ferr
			}
			e.desiredIDs = ids
		}
		e.isChanged = !sameIDSet(e.desiredIDs, currentIDs)
	}
	return e, nil
}

// changed reports whether the label set needs an API update.
func (e labelsEdit) changed() bool { return e.isChanged }

// applyTo maps the edit onto the update input. Pointer-or-omit: untouched
// labels leave LabelIds nil; a clear sends the empty set (live-verified:
// labelIds: [] empties it). labelIds is a full-set atomic write — there is no
// removedLabelIds analog, which is why labels are edit-shaped, not
// reconcileLinks-shaped.
func (e labelsEdit) applyTo(input *api.ProjectUpdateInput) {
	if !e.isChanged {
		return
	}
	ids := e.desiredIDs
	if ids == nil {
		ids = []string{}
	}
	input.LabelIds = &ids
}

// divergences classifies the read-your-writes result for the label set.
// Guarded on changed: an untouched label set must produce zero label
// divergence (a plain rename would otherwise EIO whenever another writer
// touches labels concurrently). Order is not divergence — the set is.
func (e labelsEdit) divergences(freshIDs []string) []writeBackResult {
	if !e.isChanged || sameIDSet(freshIDs, e.desiredIDs) {
		return nil
	}
	return []writeBackResult{{
		message: fmt.Sprintf("Field: labels\nError: the write was accepted but the persisted label set differs (sent %d labels, %d persisted). Re-read the file to see the stored set.",
			len(e.desiredIDs), len(freshIDs)),
		fatal: true,
	}}
}

// sameIDSet reports whether two label-ID lists carry the same set —
// order-insensitive, so a reordered frontmatter list is not a change.
func sameIDSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, id := range a {
		set[id] = true
	}
	for _, id := range b {
		if !set[id] {
			return false
		}
	}
	return true
}
