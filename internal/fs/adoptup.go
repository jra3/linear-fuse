package fs

// Adopt-up: a committed edit reaching the directory node that built the file.
//
// The three entity directories (issues/{ID}, projects/{slug}, initiatives/{slug})
// cache their entity, and every child they build is rendered from that one copy:
// the canonical .md's content, and — because the atomic-save path builds its
// transient file node from it too — the BASELINE every save diffs the written
// document against.
//
// Those two must stay the same entity. An absent frontmatter key means "clear
// this field", so a document diffed against a fresher entity than it was
// rendered from clears every field the writer never saw. That is not
// hypothetical: with the baseline read through to SQLite while the render stayed
// on the directory's snapshot, a byte-for-byte identical re-save of issue.md
// emitted `estimate: nil`, wiping an estimate set after the node was built. One
// entity behind both means saving back what you read is a genuine no-op, and a
// deliberate edit sends only the field it changed.
//
// Keeping them together leaves one question: what makes that entity fresh? The
// nodeRefresher seam (refresh.go) fires only when the kernel re-Looks-up the
// directory, which a write does not force. So an edit through the canonical file
// adopted onto the FILE node, upserted SQLite, and left the DIRECTORY node
// holding its pre-write copy — and the next atomic save diffed against that.
// A save restoring what the in-place save had replaced read as no change: no
// mutation, no .error, success returned, write lost (#415).
//
// adoptUp closes that gap from the write side. A committed edit pushes its fresh
// entity up to the directory node that built it, so the directory tracks our own
// writes without either node reaching around the other. The atomic-save path
// needs no hook of its own: renameSave's adopt already writes the directory
// node, which is the same destination by a different route.
//
// What this deliberately does NOT do is chase staleness the write path did not
// cause — a sync-worker update lands in SQLite without touching any node, so the
// directory keeps serving its snapshot until the next Lookup refreshes it. That
// is the safe direction: render and baseline stay on one entity, so a save-back
// is a no-op rather than a revert of fields the writer never saw. The staleness
// itself is the read path's problem, not something a write should silently
// resolve by clearing data.
//
// The no-op guarantee is therefore conditional, and worth stating plainly: it
// holds only while nothing else writes between the read and the save. Two
// writers make it last-writer-wins. A reads issue.md rendered from E0; B saves
// the file in place, setting a field A's copy has no key for, and adopts E1 up
// here; A then saves its buffer, which is now diffed against E1 — and an absent
// key means "clear this field", so B's value goes. Before adopt-up, A's save
// diffed against E0 and left B's field alone.
//
// That is a deliberate trade, not an oversight. The alternative it replaces is
// worse and not concurrent at all: a SINGLE writer saving a file back through
// the two ordinary save styles in sequence had the write silently dropped
// (#415) — no mutation, no .error, success returned. Clobbering a field a
// concurrent writer set is visible in the entity's history and recoverable;
// a write that never happened while reporting that it did is neither.

// entityAdopter is the write-side half of that contract: the file node calls it
// with the entity a committed edit produced, and the directory node it came from
// stores it. Set by the manifest closure that builds the file node, which is the
// one place that has both. Nil where there is no directory to update — the
// transient node the atomic-save path flushes through — so every call site
// guards.
type entityAdopter[E any] func(fresh E)

// adopt pushes fresh up when a directory is listening, and is a no-op otherwise.
// Method on the func type so the nil check lives in one place rather than at
// each of the three call sites.
func (a entityAdopter[E]) adopt(fresh E) {
	if a != nil {
		a(fresh)
	}
}
