package fs

import (
	"context"
	"errors"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// scriptedResolve returns a resolve closure that replays outcomes in order and
// counts its calls.
func scriptedResolve(calls *int, outcomes ...func() (string, error)) func() (string, error) {
	i := 0
	return func() (string, error) {
		*calls++
		out := outcomes[min(i, len(outcomes)-1)]
		i++
		return out()
	}
}

func hit(id string) func() (string, error) {
	return func() (string, error) { return id, nil }
}

func missState(name string) func() (string, error) {
	return func() (string, error) { return "", &unknownNameError{label: "state", name: name} }
}

func infraErr(msg string) func() (string, error) {
	return func() (string, error) { return "", errors.New(msg) }
}

// TestResolveWithRefresh pins the whole refresh-and-retry contract: exactly one
// refresh, exactly one retry, refresh only on the typed local miss, and the
// original error message surviving every failure shape.
func TestResolveWithRefresh(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name         string
		outcomes     []func() (string, error)
		refreshErr   error
		wantID       string
		wantErr      string // "" = success expected
		wantResolves int
		wantRefresh  int
	}{
		{
			name:         "hit needs no refresh",
			outcomes:     []func() (string, error){hit("id-1")},
			wantID:       "id-1",
			wantResolves: 1,
			wantRefresh:  0,
		},
		{
			name:         "stale catalog self-heals: miss, refresh, hit",
			outcomes:     []func() (string, error){missState("Paused"), hit("id-2")},
			wantID:       "id-2",
			wantResolves: 2,
			wantRefresh:  1,
		},
		{
			name:         "nonexistent name: one refresh, one retry, same error",
			outcomes:     []func() (string, error){missState("Nope")},
			wantErr:      "unknown state: Nope",
			wantResolves: 2,
			wantRefresh:  1,
		},
		{
			name:         "infrastructure error never refreshes",
			outcomes:     []func() (string, error){infraErr("db down")},
			wantErr:      "db down",
			wantResolves: 1,
			wantRefresh:  0,
		},
		{
			name:         "refresh failure surfaces the original miss",
			outcomes:     []func() (string, error){missState("Paused"), hit("never-reached")},
			refreshErr:   errors.New("budget refused"),
			wantErr:      "unknown state: Paused",
			wantResolves: 1,
			wantRefresh:  1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lfs := &LinearFS{}
			refreshes := 0
			lfs.catalogRefreshImpl = func(ctx context.Context, kind CatalogKind, scopeID string) error {
				refreshes++
				if kind != CatalogStates || scopeID != "team-x" {
					t.Errorf("refresh scoped to %s/%s, want states/team-x", kind, scopeID)
				}
				return tc.refreshErr
			}

			resolves := 0
			id, err := lfs.resolveWithRefresh(ctx, CatalogStates, "team-x",
				scriptedResolve(&resolves, tc.outcomes...))

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("want success, got %v", err)
				}
				if id != tc.wantID {
					t.Errorf("id = %q, want %q", id, tc.wantID)
				}
			} else {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("err = %v, want %q (the pre-refresh message, unchanged)", err, tc.wantErr)
				}
			}
			if resolves != tc.wantResolves {
				t.Errorf("resolve ran %d times, want %d", resolves, tc.wantResolves)
			}
			if refreshes != tc.wantRefresh {
				t.Errorf("refresh ran %d times, want %d", refreshes, tc.wantRefresh)
			}
		})
	}
}

// TestResolveWithRefreshDeclinesWithoutWorker: with no injected stub and no
// sync worker (fixture/offline mode), the default refresh declines and the
// original miss surfaces — no network, no panic. This is what keeps every
// pre-existing offline test's .error content byte-identical.
func TestResolveWithRefreshDeclinesWithoutWorker(t *testing.T) {
	lfs := &LinearFS{}
	resolves := 0
	_, err := lfs.resolveWithRefresh(context.Background(), CatalogStates, "team-x",
		scriptedResolve(&resolves, missState("Ghost")))
	if err == nil || err.Error() != "unknown state: Ghost" {
		t.Fatalf("err = %v, want the original miss unchanged", err)
	}
	if resolves != 1 {
		t.Errorf("resolve ran %d times, want 1 (refresh declined, no retry)", resolves)
	}
}

// TestResolveByNameMintsTypedMiss: the generic resolver's miss is the typed
// local-miss marker with the historical message, so the refresh trigger and
// the .error contract stay in lockstep.
func TestResolveByNameMintsTypedMiss(t *testing.T) {
	_, err := resolveByName([]api.State{{ID: "s1", Name: "Todo"}}, "Ghost", "state",
		func(s api.State) string { return s.Name }, func(s api.State) string { return s.ID })
	if err == nil || err.Error() != "unknown state: Ghost" {
		t.Fatalf("err = %v, want %q", err, "unknown state: Ghost")
	}
	var miss *unknownNameError
	if !errors.As(err, &miss) {
		t.Fatalf("miss is not *unknownNameError: %T", err)
	}
}
