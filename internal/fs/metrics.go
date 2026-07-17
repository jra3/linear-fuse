package fs

// OTEL instruments for the serving layer (meter "linearfs/fuse"). Until this
// change the fs package recorded nothing — the layer users actually feel was
// the observability blind spot while api/sync/repo/reconcile were all wired.
//
// The instruments bind lazily on first use (the internal/reconcile
// prunesCounter precedent): the commit tails are free generic functions with no
// single construction choke to bind at, so threading a metrics handle through
// every sink would be far more intrusive than a package-level lazy bind. Until
// telemetry.Init registers a provider the global no-op makes every record free,
// so there are no nil checks or enable flags.
//
// Coverage is deliberately the cheap choke points, not every node type: the
// four commit tails (create/delete/flush/rename), the editBuffer read/write and
// renderFile read entry points, and the embedded-file fetch tiers. Lookup and
// readdir are spread across every node type with no shared tail, so they are
// left out rather than instrumented invasively — see #264.

import (
	"context"
	"sync"
	"syscall"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jra3/linear-fuse/internal/telemetry"
)

type fuseMetrics struct {
	ops      metric.Int64Counter     // linearfs.fuse.ops {op, outcome}
	duration metric.Float64Histogram // linearfs.fuse.duration {op}, seconds
	embedded metric.Int64Counter     // linearfs.embedded_files.fetch {source}
}

var (
	fuseMetricsOnce sync.Once
	fuseMetricsInst fuseMetrics
)

func fuseMetricsInstance() fuseMetrics {
	fuseMetricsOnce.Do(func() {
		m := otel.Meter("linearfs/fuse")
		fuseMetricsInst = fuseMetrics{
			ops: telemetry.MustInt64Counter(m, "linearfs.fuse.ops",
				metric.WithDescription("FUSE operations completed, by op (create|delete|flush|rename|write|read) and outcome (ok|einval|eio|eagain|...)")),
			duration: telemetry.MustFloat64Histogram(m, "linearfs.fuse.duration",
				metric.WithUnit("s"),
				metric.WithDescription("FUSE operation duration by op")),
			embedded: telemetry.MustInt64Counter(m, "linearfs.embedded_files.fetch",
				metric.WithDescription("Embedded-file byte fetches, by serving tier (memory|disk|cdn)")),
		}
	})
	return fuseMetricsInst
}

// outcomeForErrno maps a syscall.Errno to the closed outcome enum shared by the
// write-outcome breakdown — the failure-model health check the issue calls for.
// Anything unlisted collapses to "other" so cardinality stays bounded.
func outcomeForErrno(errno syscall.Errno) string {
	switch errno {
	case 0:
		return "ok"
	case syscall.EINVAL:
		return "einval"
	case syscall.EIO:
		return "eio"
	case syscall.EAGAIN:
		return "eagain"
	case syscall.ENOENT:
		return "enoent"
	case syscall.EMSGSIZE:
		return "emsgsize"
	case syscall.EPERM:
		return "eperm"
	case syscall.EACCES:
		return "eacces"
	default:
		return "other"
	}
}

// recordFuseOp counts one completed FUSE op with its duration and outcome.
func recordFuseOp(ctx context.Context, op string, start time.Time, errno syscall.Errno) {
	m := fuseMetricsInstance()
	m.ops.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op),
		attribute.String("outcome", outcomeForErrno(errno))))
	m.duration.Record(ctx, time.Since(start).Seconds(),
		metric.WithAttributes(attribute.String("op", op)))
}

// recordEmbeddedFetch counts one embedded-file byte fetch by the tier that
// served it (memory|disk|cdn) — the CDN visibility the CDN-seam issue wanted.
func recordEmbeddedFetch(ctx context.Context, source string) {
	fuseMetricsInstance().embedded.Add(ctx, 1,
		metric.WithAttributes(attribute.String("source", source)))
}
