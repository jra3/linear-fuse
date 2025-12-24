package api

import (
	"errors"
	"testing"
	"time"
)

func TestAPIStats_Record(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	// Record some calls
	stats.Record("GetTeams", 100*time.Millisecond, nil)
	stats.Record("GetTeams", 150*time.Millisecond, nil)
	stats.Record("UpdateIssue", 200*time.Millisecond, nil)
	stats.Record("UpdateIssue", 250*time.Millisecond, errors.New("failed"))

	stats.mu.RLock()
	defer stats.mu.RUnlock()

	// Check GetTeams stats
	getTeams := stats.operations["GetTeams"]
	if getTeams == nil {
		t.Fatal("GetTeams operation not recorded")
	}
	if getTeams.Count != 2 {
		t.Errorf("GetTeams count = %d, want 2", getTeams.Count)
	}
	if getTeams.Errors != 0 {
		t.Errorf("GetTeams errors = %d, want 0", getTeams.Errors)
	}

	// Check UpdateIssue stats
	updateIssue := stats.operations["UpdateIssue"]
	if updateIssue == nil {
		t.Fatal("UpdateIssue operation not recorded")
	}
	if updateIssue.Count != 2 {
		t.Errorf("UpdateIssue count = %d, want 2", updateIssue.Count)
	}
	if updateIssue.Errors != 1 {
		t.Errorf("UpdateIssue errors = %d, want 1", updateIssue.Errors)
	}
}

func TestAPIStats_HourlyCount(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	// Record some calls
	for i := 0; i < 10; i++ {
		stats.Record("TestOp", 50*time.Millisecond, nil)
	}

	count := stats.HourlyCount()
	if count != 10 {
		t.Errorf("HourlyCount() = %d, want 10", count)
	}
}

func TestAPIStats_HourlyRate(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	// Record 150 calls (10% of 1500 limit)
	for i := 0; i < 150; i++ {
		stats.Record("TestOp", 10*time.Millisecond, nil)
	}

	rate := stats.HourlyRate()
	if rate != 10.0 {
		t.Errorf("HourlyRate() = %.1f, want 10.0", rate)
	}
}

func TestAPIStats_RecordRateLimitWait(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	stats.RecordRateLimitWait(100 * time.Millisecond)
	stats.RecordRateLimitWait(200 * time.Millisecond)

	total := stats.RateLimitWaitTotal()
	if total != 300*time.Millisecond {
		t.Errorf("RateLimitWaitTotal() = %v, want 300ms", total)
	}
}

func TestAPIStats_Summary(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	// Record some varied calls
	stats.Record("GetTeamIssuesPage", 180*time.Millisecond, nil)
	stats.Record("GetTeamIssuesPage", 200*time.Millisecond, nil)
	stats.Record("UpdateIssue", 220*time.Millisecond, nil)
	stats.Record("UpdateIssue", 250*time.Millisecond, errors.New("failed"))
	stats.RecordRateLimitWait(500 * time.Millisecond)

	summary := stats.Summary()

	// Check that summary contains expected elements
	if summary == "" {
		t.Fatal("Summary() returned empty string")
	}
	if !contains(summary, "[API-STATS]") {
		t.Error("Summary missing [API-STATS] prefix")
	}
	if !contains(summary, "GetTeamIssuesPage") {
		t.Error("Summary missing GetTeamIssuesPage")
	}
	if !contains(summary, "UpdateIssue") {
		t.Error("Summary missing UpdateIssue")
	}
	if !contains(summary, "errors:1") {
		t.Error("Summary missing error count")
	}
	if !contains(summary, "rate-wait") {
		t.Error("Summary missing rate-wait")
	}
}

func TestAPIStats_Summary_NoRateLimitWait(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	stats.Record("GetTeams", 100*time.Millisecond, nil)

	summary := stats.Summary()
	if contains(summary, "rate-wait") {
		t.Error("Summary should not include rate-wait when zero")
	}
}

func TestAPIStats_CleanupOldTimestamps(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	// Manually inject old timestamps
	stats.mu.Lock()
	oldTime := time.Now().Add(-2 * time.Hour)
	stats.recentCalls = append(stats.recentCalls, oldTime, oldTime, oldTime)
	stats.mu.Unlock()

	// Record a new call - should trigger cleanup
	stats.Record("TestOp", 50*time.Millisecond, nil)

	stats.mu.RLock()
	callCount := len(stats.recentCalls)
	stats.mu.RUnlock()

	// Should only have the 1 recent call, old ones cleaned up
	if callCount != 1 {
		t.Errorf("recentCalls count = %d, want 1 (old calls should be cleaned)", callCount)
	}
}

func TestExtractOpName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "simple query",
			query: "query GetTeams { teams { nodes { id } } }",
			want:  "GetTeams",
		},
		{
			name:  "query with variables",
			query: "query GetTeamIssues($teamId: String!) { team(id: $teamId) { issues { nodes { id } } } }",
			want:  "GetTeamIssues",
		},
		{
			name:  "mutation",
			query: "mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }",
			want:  "UpdateIssue",
		},
		{
			name: "multiline query",
			query: `query GetTeamIssuesPage($teamId: String!, $first: Int!, $after: String) {
  team(id: $teamId) {
    issues(first: $first, after: $after) {
      nodes { id }
    }
  }
}`,
			want: "GetTeamIssuesPage",
		},
		{
			name:  "no operation name",
			query: "{ teams { nodes { id } } }",
			want:  "unknown",
		},
		{
			name:  "empty query",
			query: "",
			want:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOpName(tt.query)
			if got != tt.want {
				t.Errorf("extractOpName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{2 * time.Second, "2.0s"},
		{100 * time.Millisecond, "100ms"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatDuration(tt.d)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

func TestFormatMillis(t *testing.T) {
	t.Parallel()
	tests := []struct {
		ms   float64
		want string
	}{
		{150, "150ms"},
		{1500, "1.5s"},
		{2000, "2.0s"},
		{50.5, "50ms"}, // rounds down
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatMillis(tt.ms)
			if got != tt.want {
				t.Errorf("formatMillis(%.1f) = %q, want %q", tt.ms, got, tt.want)
			}
		})
	}
}

func TestAPIStats_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	stats := NewAPIStats(false)
	defer stats.Close()

	// Hammer it from multiple goroutines
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 100; j++ {
				stats.Record("ConcurrentOp", 10*time.Millisecond, nil)
				stats.RecordRateLimitWait(1 * time.Millisecond)
				_ = stats.HourlyCount()
				_ = stats.Summary()
			}
			done <- struct{}{}
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Should have 1000 calls recorded
	stats.mu.RLock()
	count := stats.operations["ConcurrentOp"].Count
	stats.mu.RUnlock()

	if count != 1000 {
		t.Errorf("Concurrent count = %d, want 1000", count)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
