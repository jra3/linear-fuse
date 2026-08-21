package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// seedIdentifiedIssue inserts a bare issue row carrying just the identity the
// drift count reads: id, identifier, team.
func seedIdentifiedIssue(t *testing.T, store *Store, id, identifier, teamID string) {
	t.Helper()
	now := time.Now()
	err := store.Queries().UpsertIssue(context.Background(), UpsertIssueParams{
		ID:         id,
		Identifier: identifier,
		TeamID:     teamID,
		Title:      identifier,
		CreatedAt:  now,
		UpdatedAt:  now,
		SyncedAt:   now,
		Data:       json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("seed issue %s: %v", identifier, err)
	}
}

func countForeign(t *testing.T, store *Store, teamID, key string) int64 {
	t.Helper()
	prefix := key + "-"
	n, err := store.Queries().CountTeamIssuesWithForeignIdentifier(context.Background(), CountTeamIssuesWithForeignIdentifierParams{
		TeamID:    teamID,
		KeyPrefix: prefix,
	})
	if err != nil {
		t.Fatalf("CountTeamIssuesWithForeignIdentifier: %v", err)
	}
	return n
}

// TestCountTeamIssuesWithForeignIdentifierIsExact pins the two ways a prefix
// test can be wrong: wildcard metacharacters in a remote-controlled team key
// (which is why the predicate uses substr rather than LIKE or GLOB), and one
// key being a prefix of another (which is why the caller appends the hyphen).
func TestCountTeamIssuesWithForeignIdentifierIsExact(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	defer store.Close()

	t.Run("wildcard metacharacters in the key are literal", func(t *testing.T) {
		// _ and % are single-char and any-run wildcards to LIKE; [ opens a
		// character class to GLOB. Under any of those, these correctly-keyed
		// identifiers would still match, but a FOREIGN one would match too.
		for _, key := range []string{"A_C", "A%C", "A[C"} {
			seedIdentifiedIssue(t, store, "ok-"+key, key+"-1", "team-"+key)
			if got := countForeign(t, store, "team-"+key, key); got != 0 {
				t.Errorf("key %q: healthy team counted %d stale issues, want 0", key, got)
			}
			// ABC would be matched by all three patterns; it must not be.
			seedIdentifiedIssue(t, store, "foreign-"+key, "ABC-"+key, "team-"+key)
			if got := countForeign(t, store, "team-"+key, key); got != 1 {
				t.Errorf("key %q: got %d stale issues, want 1", key, got)
			}
		}
	})

	// substr() on TEXT slices by character; Go's len() counts bytes. Passing a
	// byte length in would take a longer substring than the prefix it is
	// compared against, so <> would be true for every row of a healthy team
	// and the drift check would rebuild it on every full cycle forever.
	t.Run("a multi-byte key counts by character, not byte", func(t *testing.T) {
		seedIdentifiedIssue(t, store, "mb-ok", "QÄ-1", "team-mb")
		if got := countForeign(t, store, "team-mb", "QÄ"); got != 0 {
			t.Errorf("healthy multi-byte key counted %d stale issues, want 0", got)
		}
		seedIdentifiedIssue(t, store, "mb-foreign", "ZZ-1", "team-mb")
		if got := countForeign(t, store, "team-mb", "QÄ"); got != 1 {
			t.Errorf("got %d stale issues, want 1", got)
		}
	})

	t.Run("a shorter key does not match a longer prefix", func(t *testing.T) {
		seedIdentifiedIssue(t, store, "ts-1", "TS-1", "team-ts")
		seedIdentifiedIssue(t, store, "tst-1", "TST-1", "team-ts")
		if got := countForeign(t, store, "team-ts", "TS"); got != 1 {
			t.Errorf("got %d stale issues, want 1 (TST-1 is not a TS- identifier)", got)
		}
	})

	t.Run("a whole team re-keyed counts every issue", func(t *testing.T) {
		for i := 1; i <= 3; i++ {
			seedIdentifiedIssue(t, store, "renamed-"+string(rune('0'+i)), "SPY-"+string(rune('0'+i)), "team-renamed")
		}
		if got := countForeign(t, store, "team-renamed", "AGT"); got != 3 {
			t.Errorf("got %d stale issues, want 3", got)
		}
		if got := countForeign(t, store, "team-renamed", "SPY"); got != 0 {
			t.Errorf("pre-rename key counted %d stale issues, want 0", got)
		}
	})
}
