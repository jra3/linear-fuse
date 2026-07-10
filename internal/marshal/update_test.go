package marshal

import (
	"errors"
	"testing"
)

func TestMarkdownToStatusUpdate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		content    string
		wantBody   string
		wantHealth string
		wantField  string // non-empty: expect a *FieldError on this field
	}{
		{
			name:       "plain text",
			content:    "This is a status update.",
			wantBody:   "This is a status update.",
			wantHealth: "onTrack",
		},
		{
			name: "with frontmatter onTrack",
			content: `---
health: onTrack
---
All systems go!`,
			wantBody:   "All systems go!",
			wantHealth: "onTrack",
		},
		{
			name: "with frontmatter atRisk",
			content: `---
health: atRisk
---
Some delays expected.`,
			wantBody:   "Some delays expected.",
			wantHealth: "atRisk",
		},
		{
			name: "with frontmatter offTrack",
			content: `---
health: offTrack
---
Blocked by dependencies.`,
			wantBody:   "Blocked by dependencies.",
			wantHealth: "offTrack",
		},
		{
			name: "health with spaces (on track)",
			content: `---
health: "on track"
---
Update body`,
			wantBody:   "Update body",
			wantHealth: "onTrack",
		},
		{
			name: "health with hyphens (at-risk)",
			content: `---
health: at-risk
---
Body text`,
			wantBody:   "Body text",
			wantHealth: "atRisk",
		},
		{
			name: "health with quotes",
			content: `---
health: 'off-track'
---
Critical issues`,
			wantBody:   "Critical issues",
			wantHealth: "offTrack",
		},
		{
			// Real YAML: unquoted and quoted spellings of the same value must
			// land on the same normalized health.
			name: "unquoted health uppercase-normalized",
			content: `---
health: OffTrack
---
Case-insensitive`,
			wantBody:   "Case-insensitive",
			wantHealth: "offTrack",
		},
		{
			name:       "empty content",
			content:    "",
			wantBody:   "",
			wantHealth: "onTrack",
		},
		{
			name:       "whitespace only",
			content:    "   \n\n   ",
			wantBody:   "",
			wantHealth: "onTrack",
		},
		{
			// Recorded behavior change: the old hand scanner silently posted
			// the raw bytes as the update body; the marshal parse rejects
			// loudly with a FieldError (-> EINVAL, reason in .error).
			name: "frontmatter without closing delimiter",
			content: `---
health: atRisk
No closing delimiter`,
			wantField: "frontmatter",
		},
		{
			name: "multiline body",
			content: `---
health: onTrack
---
Line 1
Line 2
Line 3`,
			wantBody:   "Line 1\nLine 2\nLine 3",
			wantHealth: "onTrack",
		},
		{
			name: "unknown health rejected, not coerced to onTrack",
			content: `---
health: critical
---
Everything is on fire`,
			wantField: "health",
		},
		{
			name: "frontmatter with empty body rejected",
			content: `---
health: atRisk
---
`,
			wantField: "body",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBody, gotHealth, err := MarkdownToStatusUpdate([]byte(tt.content))
			if tt.wantField != "" {
				var ferr *FieldError
				if !errors.As(err, &ferr) {
					t.Fatalf("MarkdownToStatusUpdate() err = %v, want *FieldError on %q", err, tt.wantField)
				}
				if ferr.Field != tt.wantField {
					t.Errorf("MarkdownToStatusUpdate() FieldError.Field = %q, want %q", ferr.Field, tt.wantField)
				}
				return
			}
			if err != nil {
				t.Fatalf("MarkdownToStatusUpdate() unexpected error: %v", err)
			}
			if gotBody != tt.wantBody {
				t.Errorf("MarkdownToStatusUpdate() body = %q, want %q", gotBody, tt.wantBody)
			}
			if gotHealth != tt.wantHealth {
				t.Errorf("MarkdownToStatusUpdate() health = %q, want %q", gotHealth, tt.wantHealth)
			}
		})
	}
}
