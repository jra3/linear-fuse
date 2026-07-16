package fs

import (
	"context"
	"log"
	"syscall"
	"time"
)

// The persist gate: retry a local reflection of a mutation Linear already
// accepted, and on exhaustion fail loud instead of swallowing the divergence.
//
// A write that reached Linear must eventually show locally. The store is the
// source of truth for reads, so a dropped SQLite reflection leaves a stale row
// (edit/rename) or a phantom row (delete) until the next sync reconciles it —
// and #276 proved that "the sync worker reconciles later" becomes "never" when
// the daemon is wedged, the exact condition under which the reflection also
// fails. So the reflection is retried against the common transient
// (`SQLITE_BUSY` racing the sync worker) and, only when retries are exhausted —
// i.e. a genuine wedge, not a benign hiccup — surfaced as `EIO` with a `.error`
// that states the *safe recovery*. Unlike a create (whose retry duplicates), an
// edit/rename/delete is idempotent, so the message says re-issuing is safe.
//
// This is the shared gate the edit tail ([[writeback-tail]]), the hand-rolled
// label/document renames, and the delete tail ([[delete-tail]]) all commit
// through, so "confirmed reflection or say why in `.error`" lives in one place.

// sqliteRetryBackoff is the delay before each reflection attempt (the first is
// immediate). A package var so tests can zero the sleeps; production uses the
// stress-tuned schedule that rides out a `SQLITE_BUSY` race with the sync
// worker (the connection-level `busy_timeout` DSN pragma already makes it rare).
var sqliteRetryBackoff = []time.Duration{0, 200 * time.Millisecond, time.Second}

// retrySQLite retries a local SQLite write that reflects a mutation Linear
// already accepted. The write must not be lost to a transient (`SQLITE_BUSY`
// racing the sync worker): a dropped reflection leaves a stale/phantom row the
// next sync is not guaranteed to reconcile. Shared by the edit/rename persist
// gate (persistOrEIO) and the delete tail's forget.
func retrySQLite[T any](ctx context.Context, op func(ctx context.Context, v *T) error, v *T) error {
	var err error
	for attempt, delay := range sqliteRetryBackoff {
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return err
			}
		}
		if err = op(ctx, v); err == nil {
			return nil
		}
		log.Printf("SQLite reflection attempt %d failed: %v", attempt+1, err)
	}
	return err
}

// persistOrEIO commits a mutation's local reflection through retrySQLite. On
// success it returns 0 and leaves `.error` untouched — the caller owns the
// clear (an edit still runs its read-your-writes compare, which may leave a
// benign note). On retry exhaustion it writes the caller-supplied `.error`
// message (which MUST state the safe recovery — the mutation is idempotent) and
// returns EIO, so a reflection the local cache can't yet serve fails loud
// instead of reporting a clean save on a diverged view (#276/#278).
func persistOrEIO[T any](
	ctx context.Context,
	sink errorSink,
	key string,
	msg func(err error) string,
	persist func(ctx context.Context, v *T) error,
	v *T,
) syscall.Errno {
	if err := retrySQLite(ctx, persist, v); err != nil {
		log.Printf("Reflection failed after a mutation succeeded on Linear (%s): %v", key, err)
		sink.SetWriteError(key, msg(err))
		return syscall.EIO
	}
	return 0
}

// unconfirmedEditMsg renders the `.error` for an edit or rename Linear accepted
// but whose local reflection failed after retries. Unlike a create's de-dupe
// message, it tells the caller re-saving is SAFE: the update is idempotent, so a
// retry re-applies the same change rather than duplicating anything.
func unconfirmedEditMsg(op string, err error) string {
	return "Operation: " + op +
		"\nError: this change SUCCEEDED on Linear but could not be cached locally after retries: " + err.Error() +
		". Re-saving is safe (the update is idempotent). If it persists, the local cache may be wedged — restart the daemon (systemctl --user restart linearfs) or wait for the next sync to reflect it."
}

// unconfirmedDeleteMsg renders the `.error` for a delete Linear accepted but
// whose local forget failed after retries, leaving a phantom row in the
// listing. It disambiguates local-cache failure from a server failure (the
// entity IS gone on Linear) and points at the self-heal: re-running `rm` finds
// the phantom, hits the already-gone path, and forgets the row.
func unconfirmedDeleteMsg(op, name, err string) string {
	return "Operation: " + op +
		"\nError: this delete SUCCEEDED on Linear (" + name + " is gone) but the local listing entry could not be removed after retries: " + err +
		". Re-run rm to clear it (safe — it is already gone on Linear). If it persists, the local cache may be wedged — restart the daemon (systemctl --user restart linearfs) or wait for the next sync."
}
