package db

// OTEL instruments for the persistence layer (meter "linearfs/db"). Every
// SQLite operation in the process runs through ctxDetachDBTX, so instrumenting
// its four methods covers the layer with no call-site changes.
//
// The question these exist to answer (#489): what share of real wall time
// SQLite writes are, and which logical operations write unbatched. A single
// statement is not the defect — under journal_mode=WAL with the inherited
// synchronous=FULL each autocommit statement fsyncs, so the cost is volume,
// and volume is what write_burst detects.
//
// Instruments bind once, per Store, from the globally registered provider
// (otel.Meter); with no provider the global no-op makes every record free, and
// dbMetrics tolerates its zero value — a nil instrument must not panic a query.

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jra3/linear-fuse/internal/telemetry"
)

// The op attribute's closed vocabulary: which DBTX method ran. Four values,
// bounded by the interface rather than by the query catalog — deliberately not
// a per-statement name, which would be one series per query in queries.sql on
// both instruments, exactly the cardinality the journald projection already
// refuses for the api layer. "Which operation" is answered one level up, by
// write_burst's caller.
//
// sqlc routes every `:exec` query (INSERT/UPDATE/DELETE — no query in
// queries.sql uses RETURNING) through ExecContext and every SELECT through
// QueryContext/QueryRowContext, so opExec IS "a write" and needs no statement
// parsing. Transaction control does not appear here: BeginTx/Commit/Rollback
// are called on *sql.DB and *sql.Tx directly, not through DBTX.
const (
	opExec     = "exec"
	opQuery    = "query"
	opQueryRow = "query_row"
	opPrepare  = "prepare"
)

// opDurationBoundaries are the explicit histogram buckets for
// linearfs.db.op_duration, in seconds.
//
// They are attached to the instrument as an OTEL advisory
// (metric.WithExplicitBucketBoundaries), which the SDK honours at instrument
// creation, so any provider gets them — production and a bare ManualReader in
// a test alike. A View would only reach the provider that registered it.
//
// The SDK default ladder (0, 5, 10, 25, … 10000) is built for millisecond-ish
// HTTP timings and is useless here: expressed in seconds, a 3 µs
// in-transaction write and a 667 µs autocommit fsync both land in the first
// bucket, which is exactly the distinction this histogram exists to draw. A
// 1-2-5 ladder from 1 µs to 1 s puts them seven boundaries apart, so the
// bimodality that appears when fsync stalls arrive is visible in the buckets
// rather than only in the mean.
//
// The tail runs past 1 s to 5 s on purpose: busy_timeout(5000) bounds how long
// a statement can block on the WAL write lock, so a read that waited out a
// long write transaction lands under 5 and stays distinguishable from a real
// hang. Lock contention is the cost batching might trade fsyncs for, and it
// would be invisible if every waiting read piled into one overflow bucket.
var opDurationBoundaries = []float64{
	0.000001, 0.000002, 0.000005, // 1–5 µs: in-transaction writes, cached reads
	0.00001, 0.00002, 0.00005,
	0.0001, 0.0002, 0.0005,
	0.001, 0.002, 0.005, // ~667 µs autocommit fsync lands just under 1 ms
	0.01, 0.05, 0.25, 1, 5, // tail: 5 s is busy_timeout, the lock-wait ceiling
}

// Burst-detector tuning. The defect #489 exists to price is not a slow
// statement, it is N unbatched statements from one logical operation.
//
// N = 64: at the measured ~667 µs per autocommit write, 64 writes is ~43 ms of
// pure fsync — the point where per-statement durability stops being noise. It
// also sits in the gap that matters: above every deliberate single-logical
// write in the tree (a FUSE flush upserts an issue and a handful of child
// rows; DeleteIssueCascade is 8 statements for one issue) and below the sync
// worker's 100-issue page, which is the smallest unbatched loop the map exists
// to measure. So it separates "a write" from "a loop".
//
// T = 1 s: at 667 µs each, 64 writes inside one second means at least ~43 ms
// of that second went to fsync, and they arrived consecutively rather than
// scattered — which is the definition of a loop. Spread wider than a second
// the same 64 writes are ordinary background traffic and must not alarm.
//
// Tripping resets the window rather than latching, so a long unbatched run
// keeps counting and the counter's magnitude tracks burst volume (~writes/N)
// while the caller attribute carries identity. One alarm for a
// 40,000-statement team rebuild would have hidden its size.
const (
	burstThreshold = 64
	burstWindow    = time.Second
)

// dbMetrics holds the persistence-layer instruments. It is copied by value
// into every ctxDetachDBTX (the store's own and each transaction's), so the
// burst window is a pointer: all wrappers derived from one Store share it.
//
// The zero value is inert and safe. Each recording site nil-checks its own
// instrument: ctxDetachDBTX is constructed in more than one place and tests
// build it by literal, and telemetry must never panic a query.
type dbMetrics struct {
	ops        metric.Int64Counter     // linearfs.db.ops {op, in_tx}
	opDuration metric.Float64Histogram // linearfs.db.op_duration {op, in_tx}, seconds
	writeBurst metric.Int64Counter     // linearfs.db.write_burst {caller}
	burst      *burstDetector
}

func newDBMetrics() dbMetrics {
	m := otel.Meter("linearfs/db")
	return dbMetrics{
		ops: telemetry.MustInt64Counter(m, "linearfs.db.ops",
			metric.WithDescription("SQLite operations at the ctxDetachDBTX chokepoint, by op (exec|query|query_row|prepare) and whether the Queries was transaction-bound")),
		opDuration: telemetry.MustFloat64Histogram(m, "linearfs.db.op_duration",
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(opDurationBoundaries...),
			metric.WithDescription("Wall time of one SQLite operation, by op and in_tx; sub-millisecond buckets separate an in-transaction write from an autocommit fsync")),
		writeBurst: telemetry.MustInt64Counter(m, "linearfs.db.write_burst",
			metric.WithDescription("Bursts of unbatched autocommit writes (burstThreshold within burstWindow), attributed to the calling function")),
		burst: &burstDetector{},
	}
}

// start returns the operation's begin time, or the zero Time when nothing is
// bound — the unobserved path skips both clock reads. The zero Time is the
// sentinel observe checks; time.Now never returns it.
func (m dbMetrics) start() time.Time {
	if m.ops == nil && m.opDuration == nil && m.writeBurst == nil {
		return time.Time{}
	}
	return time.Now()
}

// observe records one completed SQLite operation.
//
// Duration for opQuery is the statement's execution, not the caller's full
// scan: QueryContext returns *sql.Rows and the rows are pulled afterwards.
// opExec and opQueryRow are whole-operation.
func (m dbMetrics) observe(op string, inTx bool, start time.Time) {
	if start.IsZero() {
		return
	}
	end := time.Now()
	attrs := metric.WithAttributes(
		attribute.String("op", op),
		attribute.Bool("in_tx", inTx),
	)
	ctx := context.Background()
	if m.ops != nil {
		m.ops.Add(ctx, 1, attrs)
	}
	if m.opDuration != nil {
		m.opDuration.Record(ctx, end.Sub(start).Seconds(), attrs)
	}
	// Only autocommit writes can burst. An in-transaction write is the shape
	// the burst detector wants callers to reach, so counting it would flag the
	// fix as the defect.
	if op != opExec || inTx || m.burst == nil || m.writeBurst == nil {
		return
	}
	if m.burst.observe(end) {
		m.writeBurst.Add(ctx, 1, metric.WithAttributes(
			attribute.String("caller", burstCaller())))
	}
}

// burstDetector is a tumbling-window write counter. It holds no per-caller
// state: the stack is walked only on a trip, so the common path costs one
// uncontended mutex.
//
// Attribution caveat: the caller named is whichever goroutine landed the
// tripping write. Two unbatched loops running concurrently are attributed
// probabilistically, in proportion to how much of the window each filled — the
// dominant writer is the one usually named, which is the one worth naming.
type burstDetector struct {
	mu          sync.Mutex
	windowStart time.Time
	n           int
}

// observe counts one autocommit write and reports whether it completed a
// burst. A trip restarts the window so a long unbatched run keeps tripping.
func (b *burstDetector) observe(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.windowStart.IsZero() || now.Sub(b.windowStart) > burstWindow {
		b.windowStart = now
		b.n = 0
	}
	b.n++
	if b.n < burstThreshold {
		return false
	}
	b.windowStart = now
	b.n = 0
	return true
}

const (
	modulePath = "github.com/jra3/linear-fuse/"
	dbPackage  = modulePath + "internal/db."
)

// burstCaller names the function to blame for a burst: the innermost frame
// outside internal/db, module path trimmed.
//
// Skipping the whole package is what makes the answer useful — the frames
// inside it are sqlc's generated Queries methods and shared helpers like
// DeleteIssueCascade, which are the same for every caller. What comes out is
// the logical operation: internal/sync.(*Worker).syncTeamIssues,
// internal/repo.(*SQLiteRepository).deleteOrphanIssue,
// internal/reconcile.PersistIssueDetails.func2.
//
// Cardinality is a property of the code, not of the workspace: the attribute
// can only take as many values as there are unbatched write sites in the tree,
// single digits today and fewer as #489's fixes land.
func burstCaller() string {
	var pcs [32]uintptr
	n := runtime.Callers(2, pcs[:]) // skip runtime.Callers and burstCaller
	if n == 0 {
		return "unknown"
	}
	frames := runtime.CallersFrames(pcs[:n])
	for {
		f, more := frames.Next()
		if f.Function != "" && !strings.HasPrefix(f.Function, dbPackage) {
			return strings.TrimPrefix(f.Function, modulePath)
		}
		if !more {
			return "unknown"
		}
	}
}
