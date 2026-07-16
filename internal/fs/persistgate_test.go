package fs

import (
	"context"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

// zeroRetryBackoff removes the reflection retry sleeps for the duration of a
// test so exhaustion paths run instantly. The fs unit tests are sequential (no
// t.Parallel), so mutating the package var and restoring it is safe.
func zeroRetryBackoff(t *testing.T) {
	t.Helper()
	saved := sqliteRetryBackoff
	sqliteRetryBackoff = []time.Duration{0, 0, 0}
	t.Cleanup(func() { sqliteRetryBackoff = saved })
}

func TestRetrySQLite_SucceedsOnRetry(t *testing.T) {
	zeroRetryBackoff(t)
	attempts := 0
	err := retrySQLite(context.Background(), func(context.Context, *ent) error {
		attempts++
		if attempts < 2 {
			return errors.New("database is locked (5) (SQLITE_BUSY)")
		}
		return nil
	}, &ent{})
	if err != nil {
		t.Fatalf("err = %v, want nil after a retry", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2 (fail once, succeed on retry)", attempts)
	}
}

func TestRetrySQLite_ExhaustsAndReturnsLastError(t *testing.T) {
	zeroRetryBackoff(t)
	attempts := 0
	want := errors.New("db down")
	err := retrySQLite(context.Background(), func(context.Context, *ent) error {
		attempts++
		return want
	}, &ent{})
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want the last attempt's error", err)
	}
	if attempts != len(sqliteRetryBackoff) {
		t.Errorf("attempts = %d, want %d", attempts, len(sqliteRetryBackoff))
	}
}

func TestRetrySQLite_StopsOnContextCancel(t *testing.T) {
	// A non-zero delay plus an already-cancelled context returns after the first
	// failed attempt rather than sleeping out the schedule.
	saved := sqliteRetryBackoff
	sqliteRetryBackoff = []time.Duration{0, time.Hour}
	t.Cleanup(func() { sqliteRetryBackoff = saved })

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := retrySQLite(ctx, func(context.Context, *ent) error {
		attempts++
		return errors.New("boom")
	}, &ent{})
	if err == nil {
		t.Fatal("err = nil, want the failure returned on cancel")
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1 (cancel cut the backoff short)", attempts)
	}
}

// TestPersistOrEIO_Success: a persisted reflection returns 0 and leaves .error
// untouched — the caller owns the clear (an edit's compare may still note it).
func TestPersistOrEIO_Success(t *testing.T) {
	zeroRetryBackoff(t)
	sink := &fakeSink{}
	errno := persistOrEIO(context.Background(), sink, "K",
		func(error) string { return "unused" },
		func(context.Context, *ent) error { return nil },
		&ent{})
	if errno != 0 {
		t.Errorf("errno = %v, want 0", errno)
	}
	if sink.setCalls != 0 || sink.clears != 0 {
		t.Errorf(".error touched on success: sets=%d clears=%d, want 0/0", sink.setCalls, sink.clears)
	}
}

// TestPersistOrEIO_ExhaustionFailsLoud is the rename path's coverage: on retry
// exhaustion it sets the caller's message and returns EIO.
func TestPersistOrEIO_ExhaustionFailsLoud(t *testing.T) {
	zeroRetryBackoff(t)
	sink := &fakeSink{}
	errno := persistOrEIO(context.Background(), sink, "labels:TST",
		func(err error) string { return unconfirmedEditMsg("rename label a -> b", err) },
		func(context.Context, *ent) error { return errors.New("db down") },
		&ent{})
	if errno != syscall.EIO {
		t.Fatalf("errno = %v, want EIO", errno)
	}
	if sink.setCalls != 1 || sink.setKey != "labels:TST" {
		t.Errorf("SetWriteError: calls=%d key=%q, want 1 on labels:TST", sink.setCalls, sink.setKey)
	}
	for _, want := range []string{"rename label a -> b", "SUCCEEDED on Linear", "Re-saving is safe", "db down"} {
		if !strings.Contains(sink.setMsg, want) {
			t.Errorf(".error = %q, want it to contain %q", sink.setMsg, want)
		}
	}
}

func TestUnconfirmedMessages_StateSafeRecovery(t *testing.T) {
	edit := unconfirmedEditMsg("save issue ENG-1", errors.New("locked"))
	if !strings.Contains(edit, "Re-saving is safe") || !strings.Contains(edit, "idempotent") {
		t.Errorf("edit msg must say re-saving is safe: %q", edit)
	}

	del := unconfirmedDeleteMsg("delete label \"bug.md\"", "bug.md", "locked")
	if !strings.Contains(del, "Re-run rm") || !strings.Contains(del, "already gone") {
		t.Errorf("delete msg must name the re-run rm self-heal: %q", del)
	}
	// The delete message must not imply a server failure — the entity IS gone.
	if !strings.Contains(del, "SUCCEEDED on Linear") {
		t.Errorf("delete msg must clarify the server delete succeeded: %q", del)
	}
}
