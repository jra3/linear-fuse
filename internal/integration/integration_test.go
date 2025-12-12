package integration

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/config"
	"github.com/jra3/linear-fuse/internal/fs"
)

var (
	mountPoint  string
	server      *fuse.Server
	apiClient   *api.Client
	testTeamID  string
	testTeamKey string
)

func TestMain(m *testing.M) {
	if os.Getenv("LINEARFS_INTEGRATION") != "1" {
		fmt.Println("Skipping integration tests (set LINEARFS_INTEGRATION=1 to run)")
		os.Exit(0)
	}

	apiKey := os.Getenv("LINEAR_API_KEY")
	if apiKey == "" {
		log.Fatal("LINEAR_API_KEY required for integration tests")
	}

	var err error
	mountPoint, err = os.MkdirTemp("", "linearfs-test-*")
	if err != nil {
		log.Fatalf("Failed to create mount point: %v", err)
	}

	cfg := &config.Config{
		APIKey: apiKey,
		Cache: config.CacheConfig{
			TTL: 2 * time.Second,
		},
	}

	server, err = fs.Mount(mountPoint, cfg, false)
	if err != nil {
		os.RemoveAll(mountPoint)
		log.Fatalf("Failed to mount filesystem: %v", err)
	}

	apiClient = api.NewClient(apiKey)

	if err := discoverTestTeam(); err != nil {
		cleanup()
		log.Fatalf("Failed to discover test team: %v", err)
	}

	log.Printf("Integration tests using mount=%s team=%s", mountPoint, testTeamKey)

	code := m.Run()

	cleanup()
	os.Exit(code)
}

func discoverTestTeam() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	teams, err := apiClient.GetTeams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get teams: %w", err)
	}
	if len(teams) == 0 {
		return fmt.Errorf("no teams found in workspace")
	}
	testTeamID = teams[0].ID
	testTeamKey = teams[0].Key
	return nil
}

func cleanup() {
	if server != nil {
		if err := server.Unmount(); err != nil {
			log.Printf("Warning: failed to unmount: %v", err)
		}
	}
	if mountPoint != "" {
		os.RemoveAll(mountPoint)
	}
}

// TestSmokeTest verifies the filesystem mounted correctly
func TestSmokeTest(t *testing.T) {
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		t.Fatalf("Failed to read mount point: %v", err)
	}

	expected := map[string]bool{
		"README.md": false,
		"teams":     false,
		"users":     false,
		"my":        false,
	}

	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; ok {
			expected[entry.Name()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("Expected %q in root directory, not found", name)
		}
	}
}

// TestTeamsDirectoryAccessible verifies teams directory works
func TestTeamsDirectoryAccessible(t *testing.T) {
	teamsPath := filepath.Join(mountPoint, "teams")
	entries, err := os.ReadDir(teamsPath)
	if err != nil {
		t.Fatalf("Failed to read teams directory: %v", err)
	}

	if len(entries) == 0 {
		t.Error("Expected at least one team, got none")
	}

	found := false
	for _, entry := range entries {
		if entry.Name() == testTeamKey {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected to find team %q in teams directory", testTeamKey)
	}
}

// TestTeamIssuesAccessible verifies team issues directory works
func TestTeamIssuesAccessible(t *testing.T) {
	issuesPath := filepath.Join(mountPoint, "teams", testTeamKey, "issues")
	_, err := os.ReadDir(issuesPath)
	if err != nil {
		t.Fatalf("Failed to read issues directory: %v", err)
	}
}
