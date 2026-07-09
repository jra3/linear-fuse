package telemetry

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func syntheticResourceMetrics() *metricdata.ResourceMetrics {
	return &metricdata.ResourceMetrics{
		ScopeMetrics: []metricdata.ScopeMetrics{
			{
				Metrics: []metricdata.Metrics{
					{
						Name: "linearfs.process.uptime_seconds",
						Data: metricdata.Gauge[float64]{
							DataPoints: []metricdata.DataPoint[float64]{
								{Value: 42.5},
							},
						},
					},
					{
						Name: "linearfs.build.info",
						Data: metricdata.Gauge[int64]{
							DataPoints: []metricdata.DataPoint[int64]{
								{
									Attributes: attribute.NewSet(
										attribute.String("version", "v1.2.3"),
										attribute.String("commit", "abc1234"),
									),
									Value: 1,
								},
							},
						},
					},
					{
						Name: "linearfs.api.requests",
						Data: metricdata.Sum[int64]{
							DataPoints: []metricdata.DataPoint[int64]{
								{
									Attributes: attribute.NewSet(attribute.String("outcome", "ok")),
									Value:      7,
								},
							},
						},
					},
					{
						Name: "linearfs.api.duration",
						Data: metricdata.Histogram[float64]{
							DataPoints: []metricdata.HistogramDataPoint[float64]{
								{Count: 3, Sum: 1.5},
							},
						},
					},
				},
			},
		},
	}
}

func TestRenderSummary(t *testing.T) {
	t.Parallel()
	line := renderSummary(syntheticResourceMetrics())

	if strings.Contains(line, "\n") {
		t.Errorf("summary must be a single line, got %q", line)
	}
	for _, want := range []string{
		"linearfs.process.uptime_seconds=42.5",
		"linearfs.build.info{commit=abc1234,version=v1.2.3}=1",
		"linearfs.api.requests{outcome=ok}=7",
		"linearfs.api.duration=count:3,sum:1.5",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("summary %q missing %q", line, want)
		}
	}
}

func TestRenderSummaryEmpty(t *testing.T) {
	t.Parallel()
	line := renderSummary(&metricdata.ResourceMetrics{})
	if line != "metrics: (no data)" {
		t.Errorf("empty summary = %q", line)
	}
}

func TestSummaryExporterLogsOneLinePerExport(t *testing.T) {
	t.Parallel()
	var logged []string
	exp := newSummaryExporter(func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})

	if err := exp.Export(context.Background(), syntheticResourceMetrics()); err != nil {
		t.Fatalf("Export: %v", err)
	}
	if len(logged) != 1 {
		t.Fatalf("logged %d lines, want 1", len(logged))
	}
	if !strings.Contains(logged[0], "linearfs.process.uptime_seconds") {
		t.Errorf("logged line %q missing heartbeat", logged[0])
	}

	if err := exp.ForceFlush(context.Background()); err != nil {
		t.Errorf("ForceFlush: %v", err)
	}
	if err := exp.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}
