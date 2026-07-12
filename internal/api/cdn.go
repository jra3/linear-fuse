package api

// CDNClient is the second (and only other) network caller besides Client. Client
// talks GraphQL to the Linear API; CDNClient talks HTTP to Linear's file CDN
// (uploads.linear.app) for embedded-attachment bytes. Both embedded-file
// consumers route through here — the FUSE read-path byte cache
// (internal/fs/embeddedfilecache.go, GET) and the sync-side size probe
// (internal/reconcile/extract.go, HEAD) — so CDN traffic shares one auth header,
// one timeout policy, and one set of OTEL instruments instead of each wiring its
// own invisible http.Client. This makes "who talks to the network" exactly two
// clients in one package.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/jra3/linear-fuse/internal/telemetry"
)

// cdnTimeout caps a single CDN request. The GET and HEAD previously ran on
// http.DefaultClient with NO timeout, so a stalled CDN could hang a FUSE read
// (or a sync HEAD) indefinitely; a generous ceiling bounds that without cutting
// off a legitimately large byte transfer. Per-request context cancellation
// (e.g. unmount) still applies on top.
const cdnTimeout = 120 * time.Second

type CDNClient struct {
	httpClient *http.Client
	auth       func() string
	metrics    cdnMetrics
}

// NewCDNClient builds a CDN client authenticating with auth() — the same
// Authorization header value Client.AuthHeader returns.
func NewCDNClient(auth func() string) *CDNClient {
	return &CDNClient{
		httpClient: &http.Client{Timeout: cdnTimeout},
		auth:       auth,
		metrics:    newCDNMetrics(),
	}
}

// SetHTTPClient overrides the transport, for testing against an httptest CDN.
func (c *CDNClient) SetHTTPClient(h *http.Client) { c.httpClient = h }

// Get downloads the full bytes of a CDN object, authenticated. A non-200
// response is an error. Records linearfs.cdn.* under method "get".
func (c *CDNClient) Get(ctx context.Context, url string) ([]byte, error) {
	body, _, err := c.do(ctx, http.MethodGet, url, true)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// Size returns a CDN object's byte length via an authenticated HEAD, or 0 on any
// failure — best-effort, since a missing size is not worth failing a sync.
// Records linearfs.cdn.* under method "head".
func (c *CDNClient) Size(ctx context.Context, url string) int64 {
	_, size, err := c.do(ctx, http.MethodHead, url, false)
	if err != nil {
		return 0
	}
	return size
}

// do issues one authenticated CDN request, records its outcome, and returns the
// body (only when readBody) and the response's ContentLength.
func (c *CDNClient) do(ctx context.Context, method, url string, readBody bool) (body []byte, size int64, err error) {
	start := time.Now()
	defer func() { c.metrics.record(ctx, method, time.Since(start), err) }()

	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, 0, err
	}
	if c.auth != nil {
		req.Header.Set("Authorization", c.auth())
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}
	if readBody {
		if body, err = io.ReadAll(resp.Body); err != nil {
			return nil, 0, err
		}
	}
	return body, resp.ContentLength, nil
}

// cdnMetrics holds the CDN-layer instruments (meter "linearfs/cdn"): what
// happened on the CDN wire, per HTTP method. Bound once at NewCDNClient from the
// global provider, like apiMetrics — no provider registered means the no-op
// makes every record free.
type cdnMetrics struct {
	requests metric.Int64Counter     // linearfs.cdn.requests {method, outcome}
	duration metric.Float64Histogram // linearfs.cdn.duration {method}, seconds
}

func newCDNMetrics() cdnMetrics {
	m := otel.Meter("linearfs/cdn")
	return cdnMetrics{
		requests: telemetry.MustInt64Counter(m, "linearfs.cdn.requests",
			metric.WithDescription("CDN requests completed, by HTTP method (get|head) and outcome (ok|error)")),
		duration: telemetry.MustFloat64Histogram(m, "linearfs.cdn.duration",
			metric.WithUnit("s"),
			metric.WithDescription("CDN request duration by HTTP method")),
	}
}

// record counts one completed CDN request. The method attribute is lowercased
// to a tiny closed set (get|head); outcome is ok on success, error otherwise —
// the CDN has no rate-limit tier of its own to distinguish.
func (cm cdnMetrics) record(ctx context.Context, method string, elapsed time.Duration, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	attrs := metric.WithAttributes(
		attribute.String("method", strings.ToLower(method)),
		attribute.String("outcome", outcome))
	cm.requests.Add(ctx, 1, attrs)
	cm.duration.Record(ctx, elapsed.Seconds(),
		metric.WithAttributes(attribute.String("method", strings.ToLower(method))))
}
