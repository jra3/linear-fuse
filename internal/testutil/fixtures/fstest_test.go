package fixtures

import (
	"context"
	"database/sql"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

func TestNewTestMockRepository(t *testing.T) {
	team := FixtureAPITeam()
	issues := FixtureAPIIssues(3)

	mockRepo := NewTestMockRepository(t, &TestLinearFSConfig{
		WithTeams:  []api.Team{team},
		WithIssues: issues,
		WithStates: map[string][]api.State{
			team.ID: FixtureAPIStates(),
		},
	})

	ctx := context.Background()

	// Verify teams
	teams, err := mockRepo.GetTeams(ctx)
	if err != nil {
		t.Fatalf("GetTeams failed: %v", err)
	}
	if len(teams) != 1 {
		t.Errorf("expected 1 team, got %d", len(teams))
	}

	// Verify issues
	teamIssues, err := mockRepo.GetTeamIssues(ctx, team.ID)
	if err != nil {
		t.Fatalf("GetTeamIssues failed: %v", err)
	}
	if len(teamIssues) != 3 {
		t.Errorf("expected 3 issues, got %d", len(teamIssues))
	}
}

func TestNewTestSQLiteRepository(t *testing.T) {
	sqliteRepo, store := NewTestSQLiteRepository(t)

	ctx := context.Background()

	// Populate with test data
	if err := PopulateTestData(ctx, store); err != nil {
		t.Fatalf("PopulateTestData failed: %v", err)
	}

	// Verify teams
	teams, err := sqliteRepo.GetTeams(ctx)
	if err != nil {
		t.Fatalf("GetTeams failed: %v", err)
	}
	if len(teams) != 1 {
		t.Errorf("expected 1 team, got %d", len(teams))
	}

	// Verify issues
	issues, err := sqliteRepo.GetTeamIssues(ctx, "team-1")
	if err != nil {
		t.Fatalf("GetTeamIssues failed: %v", err)
	}
	if len(issues) != 5 {
		t.Errorf("expected 5 issues, got %d", len(issues))
	}
}

func TestPopulateTeam(t *testing.T) {
	store := NewTestSQLiteStore(t)
	ctx := context.Background()

	team := FixtureAPITeam()
	states := FixtureAPIStates()
	labels := FixtureAPILabels()
	issues := FixtureAPIIssues(2)

	if err := PopulateTeam(ctx, store, team, states, labels, issues); err != nil {
		t.Fatalf("PopulateTeam failed: %v", err)
	}

	// Verify via raw queries
	dbTeams, err := store.Queries().ListTeams(ctx)
	if err != nil {
		t.Fatalf("ListTeams failed: %v", err)
	}
	if len(dbTeams) != 1 {
		t.Errorf("expected 1 team, got %d", len(dbTeams))
	}

	dbStates, err := store.Queries().ListTeamStates(ctx, team.ID)
	if err != nil {
		t.Fatalf("ListTeamStates failed: %v", err)
	}
	if len(dbStates) != len(states) {
		t.Errorf("expected %d states, got %d", len(states), len(dbStates))
	}

	dbLabels, err := store.Queries().ListTeamLabels(ctx, sql.NullString{String: team.ID, Valid: true})
	if err != nil {
		t.Fatalf("ListTeamLabels failed: %v", err)
	}
	if len(dbLabels) != len(labels) {
		t.Errorf("expected %d labels, got %d", len(labels), len(dbLabels))
	}

	dbIssues, err := store.Queries().ListTeamIssues(ctx, team.ID)
	if err != nil {
		t.Fatalf("ListTeamIssues failed: %v", err)
	}
	if len(dbIssues) != len(issues) {
		t.Errorf("expected %d issues, got %d", len(issues), len(dbIssues))
	}
}
