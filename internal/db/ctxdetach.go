package db

import (
	"context"
	"database/sql"
)

// ctxDetachDBTX wraps a DBTX so every SQLite operation detaches from the
// caller's context cancellation, keeping only its values. It is also the
// process-wide chokepoint every SQLite operation passes through, and therefore
// where the persistence layer is instrumented (metrics.go).
//
// The store is a local cache: a query is sub-millisecond and the
// busy_timeout(5000) DSN pragma already bounds the only legitimate wait (a
// writer racing the sync worker). Honoring the caller's ctx cancellation buys
// nothing — but it costs correctness. The callers are FUSE request handlers, and
// under load the kernel cancels a request's context (a spurious interrupt, not
// the user abandoning the op). That cancellation, reaching SQLite, makes
// database/sql return context.Canceled regardless of busy_timeout — surfacing an
// otherwise-clean read as EIO on a directory listing and an otherwise-committed
// write's reflection as EIO on close. That was the offline-integration-suite
// flake (#296): a different unrelated op failed each run because whichever one
// happened to catch a kernel interrupt returned a spurious EIO.
//
// Detaching with context.WithoutCancel keeps the ctx's values (so anything a
// query reads off ctx still resolves) while dropping only its cancellation and
// deadline. A mutation Linear already accepted MUST reflect locally, and a local
// read MUST NOT fail for a reason the data doesn't warrant — neither should hinge
// on the liveness of the FUSE request that triggered it.
//
// Individual operations are short — every statement here is a single-row
// upsert, delete or indexed read — so dropping mid-operation cancellation
// cannot strand a request for long, and the worker checks its own context
// between operations, so cooperative shutdown is unaffected.
//
// This comment used to generalize that into "there are no long-running SQLite
// operations", which was false when it was written: the #427 team rebuild
// deleted a team issue by issue, ~8 statements each — ~40,000 statements and
// ~27 s on a 5,000-issue team — before Store.WithTx batched it into one
// commit. The correct statement is narrower. Detachment is a PER-STATEMENT
// property, so what keeps a long loop abortable is the loop, not this wrapper:
// rebuildTeamIssues checks ctx between issues and returns an error, which its
// transaction turns into a deterministic rollback. A loop that declines to
// look is uninterruptible whether or not its statements are detached.
type ctxDetachDBTX struct {
	inner DBTX
	// inTx marks a Queries bound to a transaction, the in_tx metric attribute.
	// Store.WithTx is the only place a transaction-bound wrapper is built.
	inTx    bool
	metrics dbMetrics
}

func (d ctxDetachDBTX) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	start := d.metrics.start()
	res, err := d.inner.ExecContext(context.WithoutCancel(ctx), query, args...)
	d.metrics.observe(opExec, d.inTx, start)
	return res, err
}

func (d ctxDetachDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	start := d.metrics.start()
	stmt, err := d.inner.PrepareContext(context.WithoutCancel(ctx), query)
	d.metrics.observe(opPrepare, d.inTx, start)
	return stmt, err
}

func (d ctxDetachDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	start := d.metrics.start()
	rows, err := d.inner.QueryContext(context.WithoutCancel(ctx), query, args...)
	d.metrics.observe(opQuery, d.inTx, start)
	return rows, err
}

func (d ctxDetachDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	start := d.metrics.start()
	row := d.inner.QueryRowContext(context.WithoutCancel(ctx), query, args...)
	d.metrics.observe(opQueryRow, d.inTx, start)
	return row
}
