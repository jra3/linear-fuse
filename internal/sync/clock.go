package sync

import "time"

// The Worker's clock seam — the worker-side sibling of rateBudget's injected
// now (internal/api/ratebudget.go) — is three function fields on Worker
// (now/newTimer/newTicker) that NewWorker defaults to the real clock via the
// wrappers below. Tests swap in a fake that pins now() and hands out
// channels they fire explicitly, so backoff arithmetic, the probe delay, and
// the run-loop cadence are testable without real waiting.
//
// These wrappers are deliberately the ONLY place the package's non-test code
// touches the wall clock's constructors: worker.go must stay free of bare
// time.Now/Since/Until/NewTimer/NewTicker/Sleep/After calls so the seam
// discipline is enforceable by grep, not just review (see CONTEXT.md
// "Worker clock seam").

// realNow is the now seam's default.
func realNow() time.Time { return time.Now() }

// realNewTimer is the newTimer seam's default: a one-shot channel that fires
// once after d, plus its Stop.
func realNewTimer(d time.Duration) (<-chan time.Time, func() bool) {
	t := time.NewTimer(d)
	return t.C, t.Stop
}

// realNewTicker is the newTicker seam's default: a channel that fires every
// d, plus its Stop.
func realNewTicker(d time.Duration) (<-chan time.Time, func()) {
	t := time.NewTicker(d)
	return t.C, t.Stop
}
