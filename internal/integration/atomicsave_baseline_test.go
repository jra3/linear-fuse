package integration

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/marshal"
	"github.com/jra3/linear-fuse/internal/testutil/mockmutation"
)

// #415 / #417: what an atomic save diffs against.
//
// The three entity directories (issues/{ID}, projects/{slug}, initiatives/{slug})
// each cache their entity on the DIRECTORY node, captured whenever that node was
// built. An in-place save (truncate+write+fsync+close — the Claude Write tool,
// `>`, any tool that writes a file where it stands) commits through the FILE
// node: it adopts the fresh entity there and upserts SQLite, and leaves the
// directory node's copy untouched.
//
// So the next atomic save — temp file renamed over the canonical .md, which is
// how vim, VS Code and the Claude Edit tool all write — built its transient file
// node from that stale snapshot and diffed against it. Restoring the body the
// in-place save replaced then diffed "the value I am writing" against "the value
// from before the write I am undoing", concluded nothing had changed, and sent NO
// mutation while returning success. A silent lost write, on the canonical file of
// an entity, from two ordinary save styles in sequence.
//
// The fix is adoptUp (adoptup.go): the committed in-place save propagates its
// fresh entity up to the directory node, so the baseline tracks our own writes.
// Deliberately NOT by reading the baseline through to SQLite — that decouples it
// from the entity the document was rendered from, and since an absent
// frontmatter key means "clear this field", it clears every field the writer
// never saw (measured: an identical re-save emitting `estimate: nil`).
//
// The test below pins both halves of what that costs, because they need
// different evidence:
//
//   - the mutation must go out — provable only from what the mutator RECEIVED
//     (mockmutation.Client.Updates). The stored value cannot tell a dropped write
//     from a write that persisted the same value.
//   - the mount must stop claiming otherwise — provable only after the
//     serve-your-own-writes pin lapses. Inside its window a read is answered from
//     the bytes the client wrote, which is exactly why #415 reads as success:
//     "Reads immediately afterwards show the new content (the pin serves the
//     written bytes), so it looks like it worked."

// pinWindowWait is how long to wait for the serve-your-own-writes pin to lapse
// so a read is answered from what actually PERSISTED. It must exceed
// internal/fs's pinTTL (10s), which this package cannot import. One wait covers
// every surface in the test, so the cost is paid once rather than per surface.
const pinWindowWait = 11 * time.Second

// atomicSaveBaselineCase is one editable canonical .md and the mock-update kind
// its saves must produce.
type atomicSaveBaselineCase struct {
	name string
	kind string // mockmutation.UpdateCall.Kind
	path string
	// restored is the body written by the atomic save under test — equal to the
	// body the in-place save replaced, which is the case a stale baseline reads
	// as "no change".
	restored string
	// marker is the text the in-place save added and the atomic save removes. Its
	// survival in a re-read is the lost write, visible.
	marker string
}

// bodiesOfKind returns, in order, the bodies every recorded update of kind
// carried. An update that carried no body at all reads as "" — it is still a
// mutation that went out, which is the distinction these tests turn on.
func bodiesOfKind(mock *mockmutation.Client, kind string) []string {
	var out []string
	for _, u := range mock.Updates() {
		if u.Kind != kind {
			continue
		}
		body := ""
		if u.Body != nil {
			body = *u.Body
		}
		out = append(out, body)
	}
	return out
}

// withBody re-renders doc with a new body, keeping the frontmatter exactly as
// read — the shape every real editor save has.
func withBody(t *testing.T, orig []byte, body string) []byte {
	t.Helper()
	doc, err := marshal.Parse(orig)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	doc.Body = body
	rendered, err := marshal.Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return rendered
}

// TestOffline_AtomicSaveAfterInPlaceSaveSendsItsMutation is #415 reproduced at
// the syscall level, for all three entity directories that accept an atomic save.
//
// The sequence is the one a person actually performs: a tool writes the file in
// place, then an editor saves it back — an undo, a revert, a restore. The third
// step's body is deliberately EQUAL to the first step's, because that is the
// case the stale baseline gets wrong: diffed against the fresh entity it is a
// real change (B → A), diffed against the pre-write snapshot it looks like no
// change at all.
//
// The three surfaces run in one test function rather than as subtests so the
// post-save re-read can share a single pin window (see pinWindowWait) and so the
// cleanups that put the fixtures back do not run before that read.
func TestOffline_AtomicSaveAfterInPlaceSaveSendsItsMutation(t *testing.T) {
	skipIfLiveAPI(t, "asserts what the MOCK mutator received; live has no such audit log, "+
		"and the sequence would leave real edits behind")
	mock := enableMockMutations(t)

	initiativeDir, err := firstInitiativeDir()
	if err != nil {
		t.Skipf("no initiative fixture: %v", err)
	}

	cases := []*atomicSaveBaselineCase{
		{
			name: "issue.md",
			kind: "issue",
			path: issueFilePath(testTeamKey, createRefreshTestIssue(t, "Atomic Save Baseline Probe")),
		},
		{name: "project.md", kind: "project", path: projectMDPath()},
		{name: "initiative.md", kind: "initiative", path: filepath.Join(initiativeDir, "initiative.md")},
	}

	for _, tc := range cases {
		tc.restored = fmt.Sprintf("the body an undo puts back (%s)", tc.name)
		tc.marker = fmt.Sprintf("in-place-overwrite-%s", strings.ReplaceAll(tc.name, ".", "-"))

		orig, err := readFileWithRetry(tc.path, defaultWaitTime)
		if err != nil {
			t.Fatalf("read %s: %v", tc.name, err)
		}
		// Restore through the atomic path, which adopts into the directory node
		// too — an in-place restore would leave the next test's node holding this
		// test's body. Registered on the parent, so it runs after the re-read
		// below rather than between the saves and it.
		t.Cleanup(func() { atomicSave(t, tc.path, orig) })

		// (1) Establish a known body through the atomic path, so the directory
		// node's snapshot and the stored entity agree before the sequence under
		// test begins. Whatever the fixture body was is now irrelevant.
		atomicSave(t, tc.path, withBody(t, orig, tc.restored))
		seeded := len(bodiesOfKind(mock, tc.kind))
		if seeded == 0 {
			t.Fatalf("seeding %s sent no mutation; the sequence under test cannot start", tc.name)
		}

		// (2) Overwrite in place. This commits through the FILE node and is what
		// leaves the DIRECTORY node's snapshot stale.
		claudeToolWrite(t, tc.path, withBody(t, orig, tc.restored+"\n\n"+tc.marker))
		afterInPlace := bodiesOfKind(mock, tc.kind)
		if len(afterInPlace) != seeded+1 {
			t.Fatalf("in-place save of %s sent %d mutation(s), want exactly 1 — without it the "+
				"atomic save below has no stale baseline to diff against and the test proves nothing",
				tc.name, len(afterInPlace)-seeded)
		}
		if !strings.Contains(afterInPlace[len(afterInPlace)-1], tc.marker) {
			t.Fatalf("in-place save of %s sent %q, which does not carry the marker %q",
				tc.name, afterInPlace[len(afterInPlace)-1], tc.marker)
		}

		// (3) Save the ORIGINAL body back atomically. Against the fresh entity
		// this is a real change; against the stale snapshot it looks like none.
		atomicSave(t, tc.path, withBody(t, orig, tc.restored))

		afterRestore := bodiesOfKind(mock, tc.kind)
		if len(afterRestore) != len(afterInPlace)+1 {
			t.Errorf("atomic save restoring the %s body sent NO mutation (#415): the write was "+
				"silently dropped because it diffed against the directory node's pre-write snapshot.\n"+
				"mutations seen: %q", tc.name, afterRestore)
			continue
		}
		if got := afterRestore[len(afterRestore)-1]; !strings.Contains(got, tc.restored) || strings.Contains(got, tc.marker) {
			t.Errorf("atomic save of %s sent %q, want the restored body %q with the marker gone",
				tc.name, got, tc.restored)
		}
	}

	// The other half of the report: what the mount says once it is no longer
	// serving the client its own bytes back. A dropped write is invisible until
	// then — and on project.md/initiative.md it is worse than invisible, because
	// the flush still commits (their front half always proceeds, to catch link
	// changes a scalar diff would miss) and so still pins the bytes it never
	// sent. Past the window the read falls back to what persisted, and the marker
	// the save removed would reappear.
	t.Logf("waiting %s for the serve-your-own-writes pin to lapse, then re-reading all %d surfaces",
		pinWindowWait, len(cases))
	time.Sleep(pinWindowWait)

	for _, tc := range cases {
		after, err := readFileWithRetry(tc.path, defaultWaitTime)
		if err != nil {
			t.Fatalf("re-read %s after the pin window: %v", tc.name, err)
		}
		if strings.Contains(string(after), tc.marker) {
			t.Errorf("%s still shows the body the atomic save removed once the pin lapsed (#415): "+
				"the write was reported as success but never persisted.\n--- got ---\n%s", tc.name, after)
		}
		if !strings.Contains(string(after), tc.restored) {
			t.Errorf("%s does not show the restored body after the pin window\n--- want body ---\n%s\n--- got ---\n%s",
				tc.name, tc.restored, after)
		}
	}
}
