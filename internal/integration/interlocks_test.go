package integration

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/db"
	"github.com/jra3/linear-fuse/internal/sync"
)

// skipRecorder stands in for *testing.T at the interlock boundary. The real
// t.Skip aborts the goroutine (runtime.Goexit), so only the FIRST Skip a guard
// reaches is observable to a running test; this records that one and ignores
// any later call, which is what makes the ordering inside skipIfNoWriteTests
// visible rather than hidden behind a combined verdict.
type skipRecorder struct {
	skipped bool
	reason  string
}

func (r *skipRecorder) Skip(args ...any) {
	if r.skipped {
		return
	}
	r.skipped = true
	r.reason = fmt.Sprint(args...)
}

func (r *skipRecorder) verdict() string {
	if !r.skipped {
		return "RUNS"
	}
	return "SKIPS"
}

// TestWriteInterlocksAreLiveAPIGated pins the harness contract #386 was filed
// against: the CI job set LINEARFS_WRITE_TESTS=1 and nothing else, and every
// write test skipped anyway, because skipIfNoWriteTests consults liveAPIMode
// FIRST — so the job ran the offline fixture suite under a name promising it
// mutated Linear. The interesting cell below is (live=false, WRITE_TESTS=1):
// it skips, and it skips for "requires live API", not for the write flag. That
// ordering is the whole defect, so assert the reason and not just the boolean.
//
// The same table covers skipIfLiveAPI, the inverse guard the fixture-mode
// write-contract tests use: the two must never agree to run the same test, and
// the fixture guards must survive in the default offline suite (live=false,
// no flag) — the column that would vanish if they were ever converted to
// skipIfNoWriteTests.
func TestWriteInterlocksAreLiveAPIGated(t *testing.T) {
	origLive := liveAPIMode
	origFlag, hadFlag := os.LookupEnv("LINEARFS_WRITE_TESTS")
	t.Cleanup(func() {
		liveAPIMode = origLive
		if hadFlag {
			_ = os.Setenv("LINEARFS_WRITE_TESTS", origFlag)
		} else {
			_ = os.Unsetenv("LINEARFS_WRITE_TESTS")
		}
	})

	cases := []struct {
		name string

		live      bool
		writeFlag string

		// wantWriteSkip: does a skipIfNoWriteTests-guarded test (the 55 that
		// mutate a real workspace) skip under this environment?
		wantWriteSkip   bool
		wantWriteReason string
		// wantFixtureSkip: does a skipIfLiveAPI-guarded write-contract test skip?
		wantFixtureSkip bool
	}{
		{
			name:            "offline default suite (make test)",
			live:            false,
			writeFlag:       "",
			wantWriteSkip:   true,
			wantWriteReason: "requires live API",
			wantFixtureSkip: false,
		},
		{
			name:            "#386: WRITE_TESTS alone, as the CI job set it",
			live:            false,
			writeFlag:       "1",
			wantWriteSkip:   true,
			wantWriteReason: "requires live API",
			wantFixtureSkip: false,
		},
		{
			name:            "live read-only (make integration-tests-ro)",
			live:            true,
			writeFlag:       "",
			wantWriteSkip:   true,
			wantWriteReason: "write tests disabled",
			wantFixtureSkip: true,
		},
		{
			name:            "live + writes (make integration-tests-rw)",
			live:            true,
			writeFlag:       "1",
			wantWriteSkip:   false,
			wantFixtureSkip: true,
		},
	}

	t.Logf("%-46s %-12s %-14s %-12s %s", "environment", "liveAPIMode", "WRITE_TESTS", "write tests", "fixture write-contract guards")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			liveAPIMode = tc.live
			if tc.writeFlag == "" {
				_ = os.Unsetenv("LINEARFS_WRITE_TESTS")
			} else {
				_ = os.Setenv("LINEARFS_WRITE_TESTS", tc.writeFlag)
			}

			write := &skipRecorder{}
			skipIfNoWriteTests(write)
			fixture := &skipRecorder{}
			skipIfLiveAPI(fixture)

			t.Logf("%-46s %-12v %-14q %-12s %s", tc.name, tc.live, tc.writeFlag, write.verdict(), fixture.verdict())

			if write.skipped != tc.wantWriteSkip {
				t.Errorf("skipIfNoWriteTests: skipped = %v (%s), want %v", write.skipped, write.reason, tc.wantWriteSkip)
			}
			if tc.wantWriteReason != "" && !strings.Contains(write.reason, tc.wantWriteReason) {
				t.Errorf("skipIfNoWriteTests reason = %q, want it to mention %q", write.reason, tc.wantWriteReason)
			}
			if fixture.skipped != tc.wantFixtureSkip {
				t.Errorf("skipIfLiveAPI: skipped = %v (%s), want %v", fixture.skipped, fixture.reason, tc.wantFixtureSkip)
			}
			if !write.skipped && !fixture.skipped {
				t.Errorf("both interlocks let the test run under %s; they are inverses and must never overlap", tc.name)
			}
		})
	}
}

// TestInitialSyncTimeoutStaysInsideTestBudget pins the live setup gate's
// deadline arithmetic, which is only ever exercised for real by a live run.
// Two ways it can rot silently: the share stops leaving the tests room, or
// TestMain loses its flag.Parse and test.timeout reads as the zero it holds
// before parsing — at which point the gate would fall back to the 5m cap and
// could outlive a shorter -timeout, letting go test win the tie and bury the
// gate's diagnosis under a goroutine dump.
func TestInitialSyncTimeoutStaysInsideTestBudget(t *testing.T) {
	f := flag.Lookup("test.timeout")
	if f == nil {
		t.Fatal("test.timeout flag missing; the gate has nothing to derive its deadline from")
	}
	g, ok := f.Value.(flag.Getter)
	if !ok {
		t.Fatal("test.timeout is not a flag.Getter; initialSyncTimeout cannot read it")
	}

	// go test always passes -test.timeout explicitly (10m by default), so a
	// zero here means TestMain's flag.Parse did not happen.
	if budget, _ := g.Get().(time.Duration); budget == 0 {
		t.Error("test.timeout is zero at test time: TestMain must flag.Parse before setup, or the gate sizes itself off the unparsed default")
	}

	orig := f.Value.String()
	t.Cleanup(func() { _ = flag.Set("test.timeout", orig) })

	cases := []struct {
		timeout string
		want    time.Duration
		why     string
	}{
		{"15m", 5 * time.Minute, "make integration-tests-ro: a third exceeds the cap, so the cap wins"},
		{"25m", 5 * time.Minute, "make integration-tests-rw / the CI write job: same"},
		{"6m", 2 * time.Minute, "short budget: a third of it, leaving the tests the rest"},
		{"0", initialSyncTimeoutCap, "-timeout 0 (no limit): nothing to derive from, fall back to the cap"},
	}
	for _, tc := range cases {
		if err := flag.Set("test.timeout", tc.timeout); err != nil {
			t.Fatalf("set test.timeout=%s: %v", tc.timeout, err)
		}
		got := initialSyncTimeout()
		t.Logf("-timeout %-4s -> gate waits up to %-6v (%s)", tc.timeout, got, tc.why)
		if got != tc.want {
			t.Errorf("initialSyncTimeout() with -timeout %s = %v, want %v", tc.timeout, got, tc.want)
		}
		if budget, _ := g.Get().(time.Duration); budget > 0 && got >= budget {
			t.Errorf("gate deadline %v is not comfortably inside the %v test budget", got, budget)
		}
	}
}

// TestLiveSetupGateReleaseCondition exercises waitForInitialSync — the live
// setup gate — offline, by driving the one input it actually keys off: the
// worker's persisted full-cycle stamp. Live mode is the only place it runs for
// real, and "the live suite skipped setup and read an empty store" is precisely
// the failure it exists to convert into one legible error, so its three
// outcomes are worth pinning somewhere that runs on every PR.
//
// The fixture store stands in for a warm one: teams and issues are present, so
// stamping the schedule reproduces a finished cold-start cycle, and pointing
// testTeamKey at a team the store does not hold reproduces the cycle that
// stamped after log-and-continuing past a failed fetch.
func TestLiveSetupGateReleaseCondition(t *testing.T) {
	skipIfLiveAPI(t)

	store := lfs.GetStore()
	if store == nil {
		t.Fatal("fixture setup left no SQLite store; the gate has nothing to poll")
	}
	ctx := context.Background()

	clearStamp := func() {
		if _, err := store.DB().ExecContext(ctx, `DELETE FROM sync_schedule WHERE key = ?`, sync.ScheduleKeyFullCycle); err != nil {
			t.Fatalf("clear %q stamp: %v", sync.ScheduleKeyFullCycle, err)
		}
	}
	stamp := func() {
		if err := store.Queries().UpsertSyncSchedule(ctx, db.UpsertSyncScheduleParams{
			Key:     sync.ScheduleKeyFullCycle,
			LastRun: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("stamp %q: %v", sync.ScheduleKeyFullCycle, err)
		}
	}
	t.Cleanup(clearStamp)

	t.Run("stamped and populated releases setup", func(t *testing.T) {
		stamp()
		if err := waitForInitialSync(); err != nil {
			t.Fatalf("gate held a stamped, populated store: %v", err)
		}
		present, teams := teamVisibleInStore()
		t.Logf("released: %d teams listed, team %s present: %v, %d issues visible", teams, testTeamKey, present, visibleIssueCount())
	})

	t.Run("stamped but store unusable fails setup loudly", func(t *testing.T) {
		stamp()
		orig := testTeamKey
		testTeamKey = "NOSUCHTEAM"
		defer func() { testTeamKey = orig }()

		err := waitForInitialSync()
		if err == nil {
			t.Fatal("gate released with the test team missing from the store; a live run would have gone on to fail every test instead")
		}
		t.Logf("setup error:\n  %v", err)
		for _, want := range []string{"left the store unusable", "NOSUCHTEAM", sync.ScheduleKeyFullCycle} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("setup error does not mention %q: %v", want, err)
			}
		}
	})

	t.Run("no stamp times out inside the test budget", func(t *testing.T) {
		clearStamp()
		f := flag.Lookup("test.timeout")
		orig := f.Value.String()
		// 6s of budget -> a 2s gate: the same arithmetic the live run does with
		// 25m, small enough to assert the timeout path on every PR.
		if err := flag.Set("test.timeout", "6s"); err != nil {
			t.Fatalf("shrink test.timeout: %v", err)
		}
		t.Cleanup(func() { _ = flag.Set("test.timeout", orig) })

		start := time.Now()
		err := waitForInitialSync()
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("gate released with no full-cycle stamp; an unsynced store would reach the tests")
		}
		t.Logf("gave up after %v with:\n  %v", elapsed.Round(100*time.Millisecond), err)
		if want := initialSyncTimeout(); elapsed > want+5*time.Second {
			t.Errorf("gate waited %v, want it to give up near %v", elapsed, want)
		}
		if !strings.Contains(err.Error(), "did not complete within") {
			t.Errorf("timeout error does not read as a timeout: %v", err)
		}
	})
}
