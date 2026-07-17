package db

import (
	"context"
	"database/sql"
)

// ctxDetachDBTX wraps a DBTX so every SQLite operation detaches from the
// caller's context cancellation, keeping only its values.
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
// There are no long-running SQLite operations (the sync worker's largest writes
// are still sub-second batch upserts) and the worker checks its own context
// between operations, so dropping mid-operation cancellation does not impair
// cooperative shutdown.
type ctxDetachDBTX struct{ inner DBTX }

func (d ctxDetachDBTX) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return d.inner.ExecContext(context.WithoutCancel(ctx), query, args...)
}

func (d ctxDetachDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return d.inner.PrepareContext(context.WithoutCancel(ctx), query)
}

func (d ctxDetachDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return d.inner.QueryContext(context.WithoutCancel(ctx), query, args...)
}

func (d ctxDetachDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return d.inner.QueryRowContext(context.WithoutCancel(ctx), query, args...)
}
