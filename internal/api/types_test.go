package api

import "testing"

func TestPriorityName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		priority int
		want     string
	}{
		{0, "none"},
		{1, "urgent"},
		{2, "high"},
		{3, "medium"},
		{4, "low"},
		{5, "none"},  // Out of range
		{-1, "none"}, // Negative
		{100, "none"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := PriorityName(tt.priority)
			if got != tt.want {
				t.Errorf("PriorityName(%d) = %q, want %q", tt.priority, got, tt.want)
			}
		})
	}
}
