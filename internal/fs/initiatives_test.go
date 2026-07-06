package fs

import (
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/marshal"
)

// =============================================================================
// Inode Generation Tests
// =============================================================================

// =============================================================================
// Content Round-Trip Tests
// =============================================================================

// TestInitiativeContentBodyRoundTripsDescription guards against the description
// "heading fold" bug: generateContent() must emit the description as the bare
// body — exactly like project.md — so Flush's body->description mapping is
// idempotent. Previously the body carried a rendered "# <Name>" H1 heading that
// Flush (comparing TrimSpace(body) against initiative.Description) folded into
// the description on every write, doubling the heading with each save.
func TestInitiativeContentBodyRoundTripsDescription(t *testing.T) {
	node := &InitiativeInfoNode{
		initiative: api.Initiative{
			ID:          "init-1",
			Name:        "Activate users in the webapp",
			Description: "Increase webapp adoption to improve the feedback flywheel.",
		},
	}

	doc, err := marshal.Parse(node.generateContent())
	if err != nil {
		t.Fatalf("parse generated initiative.md: %v", err)
	}

	// The body Flush would read back must equal the description it would write —
	// i.e. a no-op save must not mutate the description.
	if got := strings.TrimSpace(doc.Body); got != node.initiative.Description {
		t.Fatalf("body does not round-trip to description:\n got: %q\nwant: %q", got, node.initiative.Description)
	}

	// The rendered name heading must not appear in the body; its presence is the
	// signature of the fold bug (the title lives in the `name:` frontmatter).
	if heading := "# " + node.initiative.Name; strings.Contains(doc.Body, heading) {
		t.Fatalf("body contains rendered name heading %q; it would be folded into the description on write:\n%s", heading, doc.Body)
	}
}

// =============================================================================
// Initiative Directory Name Tests
// =============================================================================

func TestInitiativeDirName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "Platform Modernization",
			expected: "platform-modernization",
		},
		{
			name:     "name with special chars",
			input:    "API Gateway 2.0 (New)",
			expected: "api-gateway-20-new",
		},
		{
			name:     "name with multiple spaces",
			input:    "Cloud   Migration   Plan",
			expected: "cloud---migration---plan",
		},
		{
			name:     "name with underscores",
			input:    "Tech_Debt_Reduction",
			expected: "techdebtreduction",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			init := api.Initiative{
				Name: tt.input,
				ID:   "test-id",
			}
			result := initiativeDirName(init)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestInitiativeDirNameFallback(t *testing.T) {
	// Empty name should fallback to ID
	init := api.Initiative{
		Name: "",
		ID:   "fallback-id-123",
	}
	result := initiativeDirName(init)
	if result != "fallback-id-123" {
		t.Errorf("Expected ID as fallback, got %q", result)
	}

	// Name with only special chars should fallback to ID
	init2 := api.Initiative{
		Name: "!@#$%",
		ID:   "fallback-id-456",
	}
	result2 := initiativeDirName(init2)
	if result2 != "fallback-id-456" {
		t.Errorf("Expected ID as fallback for special chars, got %q", result2)
	}
}

// =============================================================================
// Initiative Project Directory Name Tests
// =============================================================================

func TestInitiativeProjectDirName(t *testing.T) {
	tests := []struct {
		name     string
		input    api.InitiativeProject
		expected string
	}{
		{
			name: "simple name",
			input: api.InitiativeProject{
				ID:   "proj-1",
				Name: "API Gateway",
				Slug: "api-gateway",
			},
			expected: "api-gateway",
		},
		{
			name: "name with special chars",
			input: api.InitiativeProject{
				ID:   "proj-2",
				Name: "Auth Service 2.0",
				Slug: "auth-service",
			},
			expected: "auth-service-20",
		},
		{
			name: "fallback to slug",
			input: api.InitiativeProject{
				ID:   "proj-3",
				Name: "!@#$",
				Slug: "my-project",
			},
			expected: "my-project",
		},
		{
			name: "fallback to ID",
			input: api.InitiativeProject{
				ID:   "proj-4",
				Name: "",
				Slug: "",
			},
			expected: "proj-4",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := initiativeProjectDirName(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}
