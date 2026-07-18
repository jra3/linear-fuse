package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A metrics.jsonl line in Go stdoutmetric's shape, trimmed to the budget gauges
// status reads (two axes, the four gauges it renders).
const sampleMetricsLine = `{"Resource":{},"ScopeMetrics":[{"Metrics":[` +
	`{"Name":"linearfs.budget.remaining","Data":{"DataPoints":[` +
	`{"Attributes":[{"Key":"axis","Value":{"Type":"STRING","Value":"requests"}}],"Time":"2026-07-18T10:00:00Z","Value":2490},` +
	`{"Attributes":[{"Key":"axis","Value":{"Type":"STRING","Value":"complexity"}}],"Time":"2026-07-18T10:00:00Z","Value":2981685}]}},` +
	`{"Name":"linearfs.budget.limit","Data":{"DataPoints":[` +
	`{"Attributes":[{"Key":"axis","Value":{"Type":"STRING","Value":"requests"}}],"Value":2500},` +
	`{"Attributes":[{"Key":"axis","Value":{"Type":"STRING","Value":"complexity"}}],"Value":3000000}]}},` +
	`{"Name":"linearfs.budget.reset_seconds","Data":{"DataPoints":[` +
	`{"Attributes":[{"Key":"axis","Value":{"Type":"STRING","Value":"requests"}}],"Value":3483.18}]}},` +
	`{"Name":"linearfs.api.requests","Data":{"DataPoints":[` +
	`{"Attributes":[{"Key":"op","Value":{"Type":"STRING","Value":"GetIssue"}}],"Value":42}]}}` +
	`]}]}`

func TestLatestBudget(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metrics.jsonl")
	// Two records; the second (last) is the one status must read.
	content := `{"ScopeMetrics":[]}` + "\n" + sampleMetricsLine + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	budget, stamp, err := latestBudget(path)
	if err != nil {
		t.Fatalf("latestBudget: %v", err)
	}
	if got := budget["requests"]["remaining"]; got != 2490 {
		t.Errorf("requests.remaining = %v, want 2490", got)
	}
	if got := budget["requests"]["limit"]; got != 2500 {
		t.Errorf("requests.limit = %v, want 2500", got)
	}
	if got := budget["complexity"]["remaining"]; got != 2981685 {
		t.Errorf("complexity.remaining = %v, want 2981685", got)
	}
	if got := budget["requests"]["reset_seconds"]; got != 3483.18 {
		t.Errorf("requests.reset_seconds = %v, want 3483.18", got)
	}
	// api.* metrics must not leak into the budget map (only budget.* is kept).
	if _, ok := budget["GetIssue"]; ok {
		t.Errorf("non-budget metric leaked into budget map: %v", budget)
	}
	if want := time.Date(2026, 7, 18, 10, 0, 0, 0, time.UTC); !stamp.Equal(want) {
		t.Errorf("stamp = %v, want %v", stamp, want)
	}
}

func TestLatestBudgetNoFile(t *testing.T) {
	if _, _, err := latestBudget(filepath.Join(t.TempDir(), "absent.jsonl")); err == nil {
		t.Error("expected error for missing metrics file")
	}
}

func TestLatestBudgetNoBudgetMetrics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "m.jsonl")
	// Valid record, but no budget.* metrics — must be a clean error, not a panic.
	os.WriteFile(path, []byte(`{"ScopeMetrics":[{"Metrics":[{"Name":"linearfs.api.requests","Data":{"DataPoints":[]}}]}]}`+"\n"), 0o644)
	if _, _, err := latestBudget(path); err == nil {
		t.Error("expected error when no budget metrics present")
	}
}

func TestLastLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	os.WriteFile(path, []byte("first\nsecond\nlast line here\n"), 0o644)
	got, err := lastLine(path, 512*1024)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "last line here" {
		t.Errorf("lastLine = %q, want %q", got, "last line here")
	}
}

func TestLastLineTailWindow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	// A long first line the small tail window won't fully see, then the real last
	// line — lastLine must still return the complete final line.
	big := make([]byte, 4096)
	for i := range big {
		big[i] = 'x'
	}
	os.WriteFile(path, append(append(big, '\n'), []byte("the tail\n")...), 0o644)
	got, err := lastLine(path, 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "the tail" {
		t.Errorf("lastLine = %q, want %q", got, "the tail")
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:              "0 B",
		512:            "512 B",
		1024:           "1.0 KiB",
		1536:           "1.5 KiB",
		43304550:       "41.3 MiB",
		5 * 1073741824: "5.0 GiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanNum(t *testing.T) {
	cases := map[float64]string{
		0:       "0",
		42:      "42",
		2500:    "2,500",
		2981685: "2,981,685",
		-1500:   "-1,500",
	}
	for in, want := range cases {
		if got := humanNum(in); got != want {
			t.Errorf("humanNum(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanAgo(t *testing.T) {
	cases := map[time.Duration]string{
		30 * time.Second:  "30s",
		9 * time.Minute:   "9m",
		90 * time.Minute:  "1h30m",
		50 * time.Hour:    "2d",
		-45 * time.Second: "45s", // negative clamps to magnitude
	}
	for in, want := range cases {
		if got := humanAgo(in); got != want {
			t.Errorf("humanAgo(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestUnescapeMountField(t *testing.T) {
	if got := unescapeMountField(`/home/john/am/my\040linear`); got != "/home/john/am/my linear" {
		t.Errorf("unescapeMountField = %q", got)
	}
	if got := unescapeMountField("/home/john/am/linear"); got != "/home/john/am/linear" {
		t.Errorf("unescapeMountField changed an unescaped path: %q", got)
	}
}
