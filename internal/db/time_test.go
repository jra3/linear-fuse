package db

import (
	"testing"
	"time"
)

func TestParseTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    any
		wantZero bool
	}{
		{"nil", nil, true},
		{"time.Time", time.Now(), false},
		{"string RFC3339", "2024-01-15T10:30:00Z", false},
		{"sqlite space-separated with tz", "2024-01-15 10:30:00+00:00", false},
		{"sqlite space-separated no tz", "2024-01-15 10:30:00", false},
		{"sqlite fractional with tz", "2024-01-15 10:30:00.017+00:00", false},
		{"empty string", "", true},
		{"invalid string", "not a date", true},
		{"int (unsupported)", 12345, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseTime(tt.input)
			if tt.wantZero && !got.IsZero() {
				t.Errorf("ParseTime(%v) = %v, want zero", tt.input, got)
			}
			if !tt.wantZero && got.IsZero() {
				t.Errorf("ParseTime(%v) = zero, want non-zero", tt.input)
			}
		})
	}
}
