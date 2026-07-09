package telemetry

import (
	"context"
	"fmt"
	"math"
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

// summaryAttrKeys is the projection the one-line summary applies to every
// attribute set: only these keys survive; datapoints that collide after the
// projection are merged (values and histogram count/sum summed). This keeps
// the always-on journald line bounded and readable — per-op series (~30
// operation names, three api instruments) would otherwise blow it up — while
// the full-cardinality data remains in the JSONL file export (one source,
// two renderings; this one is the compact projection).
var summaryAttrKeys = map[string]bool{
	"outcome":    true,
	"decision":   true,
	"tier":       true,
	"axis":       true,
	"kind":       true, // phase-3 SWR instruments
	"collection": true, // phase-3 sync instruments
	"version":    true, // build.info
	"commit":     true, // build.info
}

// renderSummary is the pure projection from collected metric data to the one
// summary line: "metrics: name{attrs}=value ..." with histograms rendered as
// count/sum and attributes projected onto summaryAttrKeys.
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
	switch data := m.Data.(type) {
	case metricdata.Gauge[int64]:
		return renderScalars(m.Name, data.DataPoints, func(dp metricdata.DataPoint[int64]) (attribute.Set, float64) {
			return dp.Attributes, float64(dp.Value)
		})
	case metricdata.Gauge[float64]:
		return renderScalars(m.Name, data.DataPoints, func(dp metricdata.DataPoint[float64]) (attribute.Set, float64) {
			return dp.Attributes, dp.Value
		})
	case metricdata.Sum[int64]:
		return renderScalars(m.Name, data.DataPoints, func(dp metricdata.DataPoint[int64]) (attribute.Set, float64) {
			return dp.Attributes, float64(dp.Value)
		})
	case metricdata.Sum[float64]:
		return renderScalars(m.Name, data.DataPoints, func(dp metricdata.DataPoint[float64]) (attribute.Set, float64) {
			return dp.Attributes, dp.Value
		})
	case metricdata.Histogram[int64]:
		return renderHistograms(m.Name, data.DataPoints, func(dp metricdata.HistogramDataPoint[int64]) (attribute.Set, uint64, float64) {
			return dp.Attributes, dp.Count, float64(dp.Sum)
		})
	case metricdata.Histogram[float64]:
		return renderHistograms(m.Name, data.DataPoints, func(dp metricdata.HistogramDataPoint[float64]) (attribute.Set, uint64, float64) {
			return dp.Attributes, dp.Count, dp.Sum
		})
	default:
		return []string{fmt.Sprintf("%s=?(%T)", m.Name, m.Data)}
	}
}

// series accumulates the datapoints that share one projected attribute set,
// in first-seen order.
type series struct {
	key   string
	value float64
	count uint64
	sum   float64
}

// mergeSeries folds datapoints into one series per projected attribute set.
func mergeSeries[T any](dps []T, project func(T) (key string, value float64, count uint64, sum float64)) []*series {
	var order []*series
	byKey := make(map[string]*series)
	for _, dp := range dps {
		key, value, count, sum := project(dp)
		s, ok := byKey[key]
		if !ok {
			s = &series{key: key}
			byKey[key] = s
			order = append(order, s)
		}
		s.value += value
		s.count += count
		s.sum += sum
	}
	return order
}

func renderScalars[T any](name string, dps []T, extract func(T) (attribute.Set, float64)) []string {
	merged := mergeSeries(dps, func(dp T) (string, float64, uint64, float64) {
		set, v := extract(dp)
		return projectAttrs(set), v, 0, 0
	})
	parts := make([]string, 0, len(merged))
	for _, s := range merged {
		parts = append(parts, fmt.Sprintf("%s%s=%s", name, s.key, formatFloat(s.value)))
	}
	return parts
}

func renderHistograms[T any](name string, dps []T, extract func(T) (attribute.Set, uint64, float64)) []string {
	merged := mergeSeries(dps, func(dp T) (string, float64, uint64, float64) {
		set, count, sum := extract(dp)
		return projectAttrs(set), 0, count, sum
	})
	parts := make([]string, 0, len(merged))
	for _, s := range merged {
		parts = append(parts, fmt.Sprintf("%s%s=count:%d,sum:%s", name, s.key, s.count, formatFloat(s.sum)))
	}
	return parts
}

// projectAttrs renders an attribute set as {k=v,k=v}, keeping only the
// summaryAttrKeys (keys are already sorted inside attribute.Set); "" when
// nothing survives.
func projectAttrs(set attribute.Set) string {
	var pairs []string
	for _, kv := range set.ToSlice() {
		if summaryAttrKeys[string(kv.Key)] {
			pairs = append(pairs, string(kv.Key)+"="+kv.Value.Emit())
		}
	}
	if len(pairs) == 0 {
		return ""
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// formatFloat prints integers exactly (counters must not hit scientific
// notation as they grow) and everything else with 6 significant digits.
func formatFloat(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'g', 6, 64)
}
