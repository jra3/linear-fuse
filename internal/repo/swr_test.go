package repo

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

// newSWRTestRepo builds a repository with a (never-dialed) client and live
// refresh machinery but no store — the specs under test carry recording
// closures, so nothing touches SQLite or the network.
func newSWRTestRepo(t *testing.T) *SQLiteRepository {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := &SQLiteRepository{
		client:             api.NewClient("test-key"),
		stalenessThreshold: defaultStalenessThreshold,
		refreshing:         make(map[string]bool),
		refreshContext:     ctx,
		refreshCancel:      cancel,
		refreshSem:         make(chan struct{}, maxConcurrentRefreshes),
	}
	t.Cleanup(r.Close)
	return r
}

// TestRefreshKindKey pins the one dedup-key factory: distinct kinds with the
// same id must produce distinct keys, in the kind:id shape the dedup map has
// always used.
func TestRefreshKindKey(t *testing.T) {
	t.Parallel()
	kinds := []refreshKind{
		kindIssueDetails, kindHistory,
		kindProjectDocs, kindInitiativeDocs,
		kindProjectUpdates, kindInitiativeUpdates,
	}
	seen := make(map[string]refreshKind)
	for _, k := range kinds {
		key := k.key("same-id")
		if want := string(k) + ":same-id"; key != want {
			t.Errorf("%s.key() = %q, want %q", k, key, want)
		}
		if prev, dup := seen[key]; dup {
			t.Errorf("kinds %s and %s collide on key %q", prev, k, key)
		}
		seen[key] = k
	}
}

// TestSWRStale pins the staleness decision for both flavors — the pure core
// behind maybeRefreshSWR.
func TestSWRStale(t *testing.T) {
	t.Parallel()
	const threshold = time.Hour
	now := time.Now()

	cases := []struct {
		name        string
		syncedAt    interface{}
		syncedErr   error
		changed     time.Time
		eventDriven bool
		threshold   time.Duration
		want        bool
	}{
		// TTL flavor (threshold-driven — the flavor SetCatchUpMode reaches).
		{"ttl: never synced (nil) is stale", nil, nil, time.Time{}, false, threshold, true},
		{"ttl: query error is stale", now, fmt.Errorf("boom"), time.Time{}, false, threshold, true},
		{"ttl: recently synced is fresh", now, nil, time.Time{}, false, threshold, false},
		{"ttl: older than threshold is stale", now.Add(-2 * threshold), nil, time.Time{}, false, threshold, true},
		// The threshold is live for TTL: the same instant flips with the
		// threshold — this is the seam catch-up mode (30min) moves.
		{"ttl: 10min-old stale at 5min threshold", now.Add(-10 * time.Minute), nil, time.Time{}, false, defaultStalenessThreshold, true},
		{"ttl: 10min-old fresh at catch-up threshold", now.Add(-10 * time.Minute), nil, time.Time{}, false, catchUpStaleness, false},

		// Event-driven flavor (changed-vs-synced; threshold ignored).
		{"event: never synced (nil) is stale", nil, nil, now, true, threshold, true},
		{"event: never synced (zero) is stale", time.Time{}, nil, now, true, threshold, true},
		{"event: query error is stale", now, fmt.Errorf("boom"), now, true, threshold, true},
		{"event: changed after synced is stale", now.Add(-time.Minute), nil, now, true, threshold, true},
		{"event: unchanged since synced is fresh", now, nil, now.Add(-time.Minute), true, threshold, false},
		// SetCatchUpMode must NOT reach this flavor: the same
		// changed-after-synced inputs are stale at any threshold, including
		// the catch-up one.
		{"event: threshold ignored (tiny)", now.Add(-10 * time.Minute), nil, now, true, time.Nanosecond, true},
		{"event: threshold ignored (catch-up)", now.Add(-10 * time.Minute), nil, now, true, catchUpStaleness, true},
		{"event: fresh at tiny threshold too", now.Add(-10 * time.Minute), nil, now.Add(-time.Hour), true, time.Nanosecond, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := swrStale(c.syncedAt, c.syncedErr, c.changed, c.eventDriven, c.threshold); got != c.want {
				t.Errorf("swrStale(%v, %v, %v, %v, %v) = %v, want %v",
					c.syncedAt, c.syncedErr, c.changed, c.eventDriven, c.threshold, got, c.want)
			}
		})
	}
}

// TestOrphanOnNotFound pins the module-owned orphan classification: only a
// not-found-shaped refresh error triggers the orphan closure; every error
// passes through unchanged.
func TestOrphanOnNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	notFound := fmt.Errorf("GraphQL error: Entity not found: Issue")
	other := fmt.Errorf("connection refused")

	cases := []struct {
		name       string
		refreshErr error
		wantOrphan bool
	}{
		{"not-found triggers orphan", notFound, true},
		{"other error does not", other, false},
		{"success does not", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			orphaned := false
			wrapped := orphanOnNotFound(
				func(context.Context) error { return c.refreshErr },
				func(context.Context) { orphaned = true },
			)
			if err := wrapped(ctx); err != c.refreshErr {
				t.Errorf("wrapped refresh error = %v, want passthrough %v", err, c.refreshErr)
			}
			if orphaned != c.wantOrphan {
				t.Errorf("orphan called = %v, want %v", orphaned, c.wantOrphan)
			}
		})
	}

	// nil orphan must be tolerated (a spec may have nothing to delete).
	wrapped := orphanOnNotFound(func(context.Context) error { return notFound }, nil)
	if err := wrapped(ctx); err != notFound {
		t.Errorf("nil-orphan wrapped error = %v, want %v", err, notFound)
	}
}

// TestMaybeRefreshSWR_NilClientNoop: in fixture mode (nil client) the
// coordinator must short-circuit before consulting any closure.
func TestMaybeRefreshSWR_NilClientNoop(t *testing.T) {
	t.Parallel()
	repo := &SQLiteRepository{} // nil client
	touched := false
	repo.maybeRefreshSWR(swrSpec{
		kind:      kindProjectDocs,
		id:        "p1",
		syncedAt:  func() (interface{}, error) { touched = true; return nil, nil },
		changedAt: func() (time.Time, bool) { touched = true; return time.Time{}, true },
		refresh:   func(context.Context) error { touched = true; return nil },
	})
	if touched {
		t.Error("maybeRefreshSWR consulted a closure with a nil client; want a no-op")
	}
}

// TestMaybeRefreshSWR_ChangedAtUnknownNoRefresh: ok=false from changedAt
// (entity not in DB) suppresses the refresh without even querying syncedAt —
// discovery belongs to the sync worker.
func TestMaybeRefreshSWR_ChangedAtUnknownNoRefresh(t *testing.T) {
	t.Parallel()
	repo := newSWRTestRepo(t)
	queried := false
	fired := false
	repo.maybeRefreshSWR(swrSpec{
		kind:      kindIssueDetails,
		id:        "unknown-issue",
		syncedAt:  func() (interface{}, error) { queried = true; return nil, nil },
		changedAt: func() (time.Time, bool) { return time.Time{}, false },
		refresh:   func(context.Context) error { fired = true; return nil },
	})
	time.Sleep(50 * time.Millisecond)
	if queried || fired {
		t.Errorf("queried=%v fired=%v; want neither when changedAt reports unknown", queried, fired)
	}
}

// TestMaybeRefreshSWR_EventDrivenFires: the event-driven flavor triggers the
// background refresh on changed-after-synced and on never-synced.
func TestMaybeRefreshSWR_EventDrivenFires(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cases := []struct {
		name     string
		syncedAt interface{}
	}{
		{"changed after synced", now.Add(-time.Minute)},
		{"never synced", nil},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := newSWRTestRepo(t)
			fired := make(chan struct{}, 1)
			repo.maybeRefreshSWR(swrSpec{
				kind:      kindHistory,
				id:        fmt.Sprintf("issue-%d", i),
				syncedAt:  func() (interface{}, error) { return c.syncedAt, nil },
				changedAt: func() (time.Time, bool) { return now, true },
				refresh: func(context.Context) error {
					fired <- struct{}{}
					return nil
				},
			})
			select {
			case <-fired:
			case <-time.After(2 * time.Second):
				t.Error("event-driven refresh did not fire")
			}
		})
	}
}

// TestMaybeRefreshSWR_CatchUpReachesTTLOnly pins the grilled policy at the
// module level: with catch-up mode active (30min threshold), a 10min-old TTL
// surface stays quiet while an event-driven surface with the same 10min-old
// synced_at (and a fresher change) still fires.
func TestMaybeRefreshSWR_CatchUpReachesTTLOnly(t *testing.T) {
	t.Parallel()
	repo := newSWRTestRepo(t)
	repo.SetCatchUpMode(true)

	syncedTenMinAgo := time.Now().Add(-10 * time.Minute)
	var ttlFired atomic.Bool
	repo.maybeRefreshSWR(swrSpec{
		kind:     kindProjectDocs,
		id:       "p1",
		syncedAt: func() (interface{}, error) { return syncedTenMinAgo, nil },
		refresh: func(context.Context) error {
			ttlFired.Store(true)
			return nil
		},
	})

	eventFired := make(chan struct{}, 1)
	repo.maybeRefreshSWR(swrSpec{
		kind:      kindIssueDetails,
		id:        "i1",
		syncedAt:  func() (interface{}, error) { return syncedTenMinAgo, nil },
		changedAt: func() (time.Time, bool) { return time.Now(), true },
		refresh: func(context.Context) error {
			eventFired <- struct{}{}
			return nil
		},
	})

	select {
	case <-eventFired:
	case <-time.After(2 * time.Second):
		t.Error("event-driven refresh suppressed by catch-up mode; the threshold must reach TTL only")
	}
	time.Sleep(50 * time.Millisecond)
	if ttlFired.Load() {
		t.Error("TTL refresh fired for 10min-old data in catch-up mode (30min threshold)")
	}

	// Same TTL spec fires once catch-up mode is off (5min threshold).
	repo.SetCatchUpMode(false)
	ttlFired2 := make(chan struct{}, 1)
	repo.maybeRefreshSWR(swrSpec{
		kind:     kindProjectDocs,
		id:       "p2",
		syncedAt: func() (interface{}, error) { return syncedTenMinAgo, nil },
		refresh: func(context.Context) error {
			ttlFired2 <- struct{}{}
			return nil
		},
	})
	select {
	case <-ttlFired2:
	case <-time.After(2 * time.Second):
		t.Error("TTL refresh did not fire for 10min-old data at the default 5min threshold")
	}
}
