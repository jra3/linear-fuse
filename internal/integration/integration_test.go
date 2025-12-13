package integration

import (
	"context"
	"fmt"
	"log"
	"os"
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

