package integration

import (
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
)

// The workspace a live run acts on is chosen by pickTestTeam, and these are its
// unit tests: they run in every mode because the function is pure — teams in, a
// team or a legible error out, no network.
//
// What they are guarding is the failure the suite could not previously express.
// A live run authenticates with whatever LINEAR_API_KEY happens to be in the
// environment; if that is a developer's real workspace key rather than the test
// workspace's, the old selection ("prefer TST, else teams[0]") found *a* team
// there and ran — reading the whole workspace and, under LINEARFS_WRITE_TESTS,
// creating issues and projects in it. Nothing in the run said whose data it was.
// LINEARFS_TEST_TEAM turns that into a setup error: name the team you mean, and
// a key pointed at the wrong workspace fails before the mount instead of after
// the mutations.

func teams(keys ...string) []api.Team {
	out := make([]api.Team, 0, len(keys))
	for _, k := range keys {
		out = append(out, api.Team{ID: "id-" + strings.ToLower(k), Key: k, Name: k + " team"})
	}
	return out
}

func TestPickTestTeamHonorsTheRequestedKey(t *testing.T) {
	got, err := pickTestTeam(teams("SPY", "TST", "FUS"), "FUS")
	if err != nil {
		t.Fatalf("pickTestTeam(want=FUS) returned %v", err)
	}
	if got.Key != "FUS" {
		t.Errorf("picked %q, want FUS — a named team must win over the TST preference", got.Key)
	}
}

// The whole point: a requested team the workspace does not have is a wrong-key
// diagnosis, not an invitation to run somewhere else.
func TestPickTestTeamRefusesToSubstituteAnotherTeam(t *testing.T) {
	_, err := pickTestTeam(teams("SPY", "DES", "GTM", "TST", "ENG"), "FUS")
	if err == nil {
		t.Fatal("pickTestTeam accepted a workspace with no FUS team; a wrong key must fail setup, not fall back")
	}
	// The message has to be enough to recognise the wrong workspace by eye.
	for _, want := range []string{"FUS", "ENG", "LINEARFS_TEST_TEAM"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, got: %v", want, err)
		}
	}
}

// Unset keeps the historical behaviour, so an existing live invocation that
// never set the variable is unchanged.
func TestPickTestTeamWithoutARequestPrefersTST(t *testing.T) {
	got, err := pickTestTeam(teams("SPY", "TST", "ENG"), "")
	if err != nil {
		t.Fatalf("pickTestTeam(want=\"\") returned %v", err)
	}
	if got.Key != "TST" {
		t.Errorf("picked %q, want the TST preference", got.Key)
	}

	got, err = pickTestTeam(teams("SPY", "ENG"), "")
	if err != nil {
		t.Fatalf("pickTestTeam(want=\"\", no TST) returned %v", err)
	}
	if got.Key != "SPY" {
		t.Errorf("picked %q, want the first team as the fallback", got.Key)
	}
}

func TestPickTestTeamOnAnEmptyWorkspace(t *testing.T) {
	if _, err := pickTestTeam(nil, ""); err == nil {
		t.Error("pickTestTeam accepted a workspace with no teams")
	}
	if _, err := pickTestTeam(nil, "FUS"); err == nil {
		t.Error("pickTestTeam accepted a requested team on a workspace with no teams")
	}
}
