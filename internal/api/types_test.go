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

func TestPriorityValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		want int
	}{
		{"none", 0},
		{"urgent", 1},
		{"high", 2},
		{"medium", 3},
		{"low", 4},
		{"", 0},         // Empty
		{"URGENT", 0},   // Case sensitive
		{"unknown", 0},  // Unknown value
		{"critical", 0}, // Invalid
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PriorityValue(tt.name)
			if got != tt.want {
				t.Errorf("PriorityValue(%q) = %d, want %d", tt.name, got, tt.want)
			}
		})
	}
}

func TestPriorityRoundtrip(t *testing.T) {
	t.Parallel()
	// Test that valid priorities roundtrip correctly
	validPriorities := []int{0, 1, 2, 3, 4}

	for _, p := range validPriorities {
		name := PriorityName(p)
		back := PriorityValue(name)
		if back != p {
			t.Errorf("Roundtrip failed: %d -> %q -> %d", p, name, back)
		}
	}
}
