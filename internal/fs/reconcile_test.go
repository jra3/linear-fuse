package fs

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/db"
)

// reconcileLinks is pure of the errorSink, SQLite, and the API — it drives only
// the resolve/link/unlink closures. These tests feed it recording fakes and
// assert which links fire, the diff direction, and the failure classification.

// recordingLinker resolves names via a map (absence = "unknown") and records the
// IDs passed to link/unlink so a test can assert exactly what changed.
type recordingLinker struct {
	ids      map[string]string // name -> id; absence means resolve fails
	linked   []string          // ids passed to link, in order
	unlinked []string          // ids passed to unlink, in order
	linkErr  error             // if set, link returns it (mutation failure)
}

func (r *recordingLinker) resolve(_ context.Context, name string) (string, error) {
	if id, ok := r.ids[name]; ok {
		return id, nil
	}
	return "", errors.New("unknown " + name)
}

func (r *recordingLinker) spec(current, desired []string) linkReconcileSpec {
	return linkReconcileSpec{
		current: current,
		desired: desired,
		resolve: r.resolve,
		link: func(_ context.Context, id string) error {
			if r.linkErr != nil {
				return r.linkErr
			}
			r.linked = append(r.linked, id)
			return nil
		},
		unlink: func(_ context.Context, id string) error {
			r.unlinked = append(r.unlinked, id)
			return nil
		},
		field: "projects",
		hint:  ". use a valid slug.",
	}
}

func TestReconcileLinksDiffsAdditionsAndRemovals(t *testing.T) {
	r := &recordingLinker{ids: map[string]string{"a": "id-a", "b": "id-b", "c": "id-c"}}
	// current {a,b}, desired {b,c}: add c, remove a, leave b untouched.
	if err := reconcileLinks(context.Background(), r.spec([]string{"a", "b"}, []string{"b", "c"})); err != nil {
		t.Fatalf("reconcileLinks: unexpected error %v", err)
	}
	if len(r.linked) != 1 || r.linked[0] != "id-c" {
		t.Errorf("linked = %v, want [id-c]", r.linked)
	}
	if len(r.unlinked) != 1 || r.unlinked[0] != "id-a" {
		t.Errorf("unlinked = %v, want [id-a]", r.unlinked)
	}
}

func TestReconcileLinksNoChangeDoesNothing(t *testing.T) {
	r := &recordingLinker{ids: map[string]string{"a": "id-a", "b": "id-b"}}
	if err := reconcileLinks(context.Background(), r.spec([]string{"a", "b"}, []string{"b", "a"})); err != nil {
		t.Fatalf("reconcileLinks: unexpected error %v", err)
	}
	if len(r.linked) != 0 || len(r.unlinked) != 0 {
		t.Errorf("expected no changes, got linked=%v unlinked=%v", r.linked, r.unlinked)
	}
}

func TestReconcileLinksUnresolvableNameReturnsFieldError(t *testing.T) {
	r := &recordingLinker{ids: map[string]string{"a": "id-a"}}
	// "ghost" is desired but not resolvable.
	err := reconcileLinks(context.Background(), r.spec([]string{"a"}, []string{"a", "ghost"}))
	var ferr *FieldError
	if !errors.As(err, &ferr) {
		t.Fatalf("expected *FieldError, got %T: %v", err, err)
	}
	if ferr.Field != "projects" || ferr.Value != "ghost" {
		t.Errorf("FieldError = %+v, want Field=projects Value=ghost", ferr)
	}
	if !strings.Contains(ferr.Message, "use a valid slug") {
		t.Errorf("FieldError.Message = %q, want it to carry the hint", ferr.Message)
	}
	if len(r.linked) != 0 {
		t.Errorf("no link should fire when a name fails to resolve, got %v", r.linked)
	}
}

func TestReconcileLinksMutationFailurePropagates(t *testing.T) {
	boom := errors.New("api down")
	r := &recordingLinker{ids: map[string]string{"a": "id-a"}, linkErr: boom}
	err := reconcileLinks(context.Background(), r.spec(nil, []string{"a"}))
	if !errors.Is(err, boom) {
		t.Fatalf("expected the wrapped mutation error, got %v", err)
	}
	var ferr *FieldError
	if errors.As(err, &ferr) {
		t.Errorf("a mutation failure must not be classified as a FieldError")
	}
}

// TestPersistInitiativeProjectLinkFailsLoud confirms the #276 contract for the
// one link path sync can't reconcile (link/unlink bumps no updatedAt): a failed
// junction write is returned, not swallowed, so the reconcile aborts and the
// edit surfaces it in .error rather than a silent stale. A nil store (SQLite
// disabled) stays a no-op. The mutation is idempotent, so the message tells the
// caller re-saving is safe.
func TestPersistInitiativeProjectLinkFailsLoud(t *testing.T) {
	lfs, err := NewLinearFS(&config.Config{APIKey: "test-key"}, true)
	if err != nil {
		t.Fatalf("NewLinearFS: %v", err)
	}
	defer lfs.Close()

	// No store -> no-op, no error (SQLite disabled path).
	if err := lfs.persistInitiativeProjectLink(context.Background(), "init-1", "proj-1", true); err != nil {
		t.Errorf("nil store should be a no-op, got %v", err)
	}

	// A closed store makes every query fail; the write must now be reported.
	store, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	lfs.store = store
	if err := store.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	for _, linked := range []bool{true, false} {
		err := lfs.persistInitiativeProjectLink(context.Background(), "init-1", "proj-1", linked)
		if err == nil {
			t.Fatalf("linked=%v: junction-write failure must be returned, not swallowed", linked)
		}
		if !strings.Contains(err.Error(), "applied on Linear") || !strings.Contains(err.Error(), "re-saving is safe") {
			t.Errorf("linked=%v: message = %q, want it to note the Linear-succeeded + safe-retry guidance", linked, err.Error())
		}
	}
}
