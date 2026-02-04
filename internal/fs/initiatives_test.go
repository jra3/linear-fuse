package fs

import (
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// =============================================================================
// Inode Generation Tests
// =============================================================================

func TestInitiativeInfoInoStability(t *testing.T) {
	// Same ID should always produce same inode
	id := "test-initiative-id-123"
	ino1 := initiativeInfoIno(id)
	ino2 := initiativeInfoIno(id)

	if ino1 != ino2 {
		t.Errorf("Inode not stable: got %d and %d for same ID", ino1, ino2)
	}

	// Different IDs should produce different inodes
	id2 := "test-initiative-id-456"
	ino3 := initiativeInfoIno(id2)

	if ino1 == ino3 {
		t.Errorf("Different IDs produced same inode: %d", ino1)
	}
}

func TestInitiativeProjectsInoStability(t *testing.T) {
	id := "test-initiative-id-123"
	ino1 := initiativeProjectsIno(id)
	ino2 := initiativeProjectsIno(id)

	if ino1 != ino2 {
		t.Errorf("Projects inode not stable: got %d and %d for same ID", ino1, ino2)
	}

	// Should be different from info inode
	infoIno := initiativeInfoIno(id)
	if ino1 == infoIno {
		t.Errorf("Projects inode same as info inode: %d", ino1)
	}
}

func TestInitiativeUpdatesDirInoStability(t *testing.T) {
	id := "test-initiative-id-123"
	ino1 := initiativeUpdatesDirIno(id)
	ino2 := initiativeUpdatesDirIno(id)

	if ino1 != ino2 {
		t.Errorf("Updates dir inode not stable: got %d and %d for same ID", ino1, ino2)
	}

	// Should be different from other initiative inodes
	infoIno := initiativeInfoIno(id)
	projectsIno := initiativeProjectsIno(id)

	if ino1 == infoIno || ino1 == projectsIno {
		t.Errorf("Updates inode collides with other inodes")
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
