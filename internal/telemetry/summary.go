package telemetry

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// summaryExporter renders each export as ONE compact human-readable log line —
// the always-on journald summary. It is generic over whatever instruments the
// provider carries, so later phases enrich the line without touching it.
type summaryExporter struct {
	logf func(format string, args ...any)
}

var _ sdkmetric.Exporter = (*summaryExporter)(nil)

func newSummaryExporter(logf func(format string, args ...any)) *summaryExporter {
	return &summaryExporter{logf: logf}
}

func (e *summaryExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(k)
}

func (e *summaryExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *summaryExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	e.logf("%s", renderSummary(rm))
	return nil
}

func (e *summaryExporter) ForceFlush(context.Context) error { return nil }
func (e *summaryExporter) Shutdown(context.Context) error   { return nil }

// renderSummary is the pure projection from collected metric data to the one
// summary line: "metrics: name{attrs}=value ..." with histograms rendered as
// count/sum.
func renderSummary(rm *metricdata.ResourceMetrics) string {
	var parts []string
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			parts = append(parts, renderMetric(m)...)
		}
	}
	if len(parts) == 0 {
		return "metrics: (no data)"
	}
	return "metrics: " + strings.Join(parts, " ")
}

func renderMetric(m metricdata.Metrics) []string {
	var parts []string
	switch data := m.Data.(type) {
	case metricdata.Gauge[int64]:
		for _, dp := range data.DataPoints {
			parts = append(parts, fmt.Sprintf("%s%s=%d", m.Name, renderAttrs(dp.Attributes), dp.Value))
		}
	case metricdata.Gauge[float64]:
		for _, dp := range data.DataPoints {
			parts = append(parts, fmt.Sprintf("%s%s=%s", m.Name, renderAttrs(dp.Attributes), formatFloat(dp.Value)))
		}
	case metricdata.Sum[int64]:
		for _, dp := range data.DataPoints {
			parts = append(parts, fmt.Sprintf("%s%s=%d", m.Name, renderAttrs(dp.Attributes), dp.Value))
		}
	case metricdata.Sum[float64]:
		for _, dp := range data.DataPoints {
			parts = append(parts, fmt.Sprintf("%s%s=%s", m.Name, renderAttrs(dp.Attributes), formatFloat(dp.Value)))
		}
	case metricdata.Histogram[int64]:
		for _, dp := range data.DataPoints {
			parts = append(parts, fmt.Sprintf("%s%s=count:%d,sum:%d", m.Name, renderAttrs(dp.Attributes), dp.Count, dp.Sum))
		}
	case metricdata.Histogram[float64]:
		for _, dp := range data.DataPoints {
			parts = append(parts, fmt.Sprintf("%s%s=count:%d,sum:%s", m.Name, renderAttrs(dp.Attributes), dp.Count, formatFloat(dp.Sum)))
		}
	default:
		parts = append(parts, fmt.Sprintf("%s=?(%T)", m.Name, m.Data))
	}
	return parts
}

// renderAttrs renders an attribute set as {k=v,k=v} (keys are already sorted
// inside attribute.Set), or "" for the empty set.
func renderAttrs(set attribute.Set) string {
	if set.Len() == 0 {
		return ""
	}
	kvs := set.ToSlice()
	pairs := make([]string, 0, len(kvs))
	for _, kv := range kvs {
		pairs = append(pairs, string(kv.Key)+"="+kv.Value.Emit())
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}
