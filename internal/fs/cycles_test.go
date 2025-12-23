package fs

import (
	"testing"
	"time"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestCycleDirName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		cycle api.Cycle
		want  string
	}{
		{
			name: "cycle with name",
			cycle: api.Cycle{
				Name:   "Sprint 1",
				Number: 1,
			},
			want: "Sprint-1",
		},
		{
			name: "cycle without name uses number",
			cycle: api.Cycle{
				Name:   "",
				Number: 42,
			},
			want: "Cycle-42",
		},
		{
			name: "cycle with multi-word name",
			cycle: api.Cycle{
				Name:   "Q4 Planning Sprint",
				Number: 5,
			},
			want: "Q4-Planning-Sprint",
		},
		{
			name: "cycle name has multiple spaces",
			cycle: api.Cycle{
				Name:   "Sprint  Two",
				Number: 2,
			},
			want: "Sprint--Two",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cycleDirName(tt.cycle)
			if got != tt.want {
				t.Errorf("cycleDirName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsCurrent(t *testing.T) {
	t.Parallel()
	now := time.Now()

	tests := []struct {
		name  string
		cycle api.Cycle
		want  bool
	}{
		{
			name: "current cycle",
			cycle: api.Cycle{
				StartsAt: now.Add(-24 * time.Hour),
				EndsAt:   now.Add(24 * time.Hour),
			},
			want: true,
		},
		{
			name: "past cycle",
			cycle: api.Cycle{
				StartsAt: now.Add(-48 * time.Hour),
				EndsAt:   now.Add(-24 * time.Hour),
			},
			want: false,
		},
		{
			name: "future cycle",
			cycle: api.Cycle{
				StartsAt: now.Add(24 * time.Hour),
				EndsAt:   now.Add(48 * time.Hour),
			},
			want: false,
		},
		{
			name: "cycle ending exactly now",
			cycle: api.Cycle{
				StartsAt: now.Add(-24 * time.Hour),
				EndsAt:   now,
			},
			want: false, // now.Before(now) is false
		},
		{
			name: "cycle starting in the past (just started)",
			cycle: api.Cycle{
				StartsAt: now.Add(-1 * time.Second),
				EndsAt:   now.Add(24 * time.Hour),
			},
			want: true, // started 1 second ago, still current
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isCurrent(tt.cycle)
			if got != tt.want {
				t.Errorf("isCurrent() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCycleFileNode_GenerateContent(t *testing.T) {
	t.Parallel()
	now := time.Now()
	startsAt := now.Add(-24 * time.Hour)
	endsAt := now.Add(24 * time.Hour)

	node := &CycleFileNode{
		team: api.Team{Key: "ENG"},
		cycle: api.Cycle{
			ID:       "cycle-123",
			Number:   5,
			Name:     "Sprint 5",
			StartsAt: startsAt,
			EndsAt:   endsAt,
			IssueCountHistory:          []int{10},
			CompletedIssueCountHistory: []int{3},
		},
	}

	content := node.generateContent()

	// Check that content includes expected fields
	contentStr := string(content)
	checks := []string{
		"id: cycle-123",
		"number: 5",
		"name: Sprint 5",
		"team: ENG",
		"status: current",
		"completed: 3",
		"total: 10",
		"percentage: 30.0",
		"# Sprint 5",
	}

	for _, check := range checks {
		if !contains(contentStr, check) {
			t.Errorf("generateContent() missing %q in:\n%s", check, contentStr)
		}
	}
}

func TestCycleFileNode_GenerateContent_EmptyHistory(t *testing.T) {
	t.Parallel()
	now := time.Now()

	node := &CycleFileNode{
		team: api.Team{Key: "ENG"},
		cycle: api.Cycle{
			ID:       "cycle-456",
			Number:   1,
			StartsAt: now.Add(24 * time.Hour),
			EndsAt:   now.Add(48 * time.Hour),
			// Empty history arrays
			IssueCountHistory:          []int{},
			CompletedIssueCountHistory: []int{},
		},
	}

	content := node.generateContent()
	contentStr := string(content)

	// Should have zero values for progress
	if !contains(contentStr, "completed: 0") {
		t.Error("expected completed: 0 for empty history")
	}
	if !contains(contentStr, "total: 0") {
		t.Error("expected total: 0 for empty history")
	}
	if !contains(contentStr, "percentage: 0.0") {
		t.Error("expected percentage: 0.0 for empty history")
	}
	if !contains(contentStr, "status: upcoming") {
		t.Error("expected status: upcoming for future cycle")
	}
}

// Helper function to check if a string contains a substring
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
