package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/testutil"
)

// rawRoot decodes a JSON object literal into the shape walkPath descends.
func rawRoot(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &root); err != nil {
		t.Fatalf("rawRoot: %v", err)
	}
	return root
}

func TestWalkPath(t *testing.T) {
	tests := []struct {
		name    string
		root    string
		path    []string
		want    string // expected raw terminal (exact JSON), "" if error expected
		wantErr string // substring the error must contain, "" if success expected
	}{
		{
			name: "terminal decode",
			root: `{"team": {"states": {"nodes": [{"id": "s1"}]}}}`,
			path: []string{"team", "states"},
			want: `{"nodes": [{"id": "s1"}]}`,
		},
		{
			name: "deep path",
			root: `{"a": {"b": {"c": {"d": 42}}}}`,
			path: []string{"a", "b", "c", "d"},
			want: `42`,
		},
		{
			name:    "missing key",
			root:    `{"team": {"labels": {}}}`,
			path:    []string{"team", "states"},
			wantErr: `"team.states" missing or null in response`,
		},
		{
			name:    "null value",
			root:    `{"team": null}`,
			path:    []string{"team", "states"},
			wantErr: `"team" missing or null in response`,
		},
		{
			name:    "null terminal",
			root:    `{"issue": null}`,
			path:    []string{"issue"},
			wantErr: `"issue" missing or null in response`,
		},
		{
			name:    "empty path",
			root:    `{"issue": {}}`,
			path:    nil,
			wantErr: "empty path",
		},
		{
			name:    "non-object intermediate",
			root:    `{"team": [1, 2]}`,
			path:    []string{"team", "states"},
			wantErr: `decode object at "team"`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := walkPath(rawRoot(t, tc.root), tc.path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want substring %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("walkPath: %v", err)
			}
			if string(raw) != tc.want {
				t.Errorf("terminal = %s, want %s", raw, tc.want)
			}
		})
	}
}

// fetchTestClient builds a Client pointed at the mock server.
func fetchTestClient(t *testing.T) (*testutil.MockLinearServer, *Client) {
	t.Helper()
	mock := testutil.NewMockLinearServer()
	t.Cleanup(mock.Close)
	c := NewClient("test-key")
	c.SetAPIURL(mock.URL())
	return mock, c
}

const fetchTestQuery = `query Things($id: String!) { thing(id: $id) { things { nodes { id } } } }`

func TestFetchNodesTripwire(t *testing.T) {
	nodes := []map[string]any{{"id": "n1"}, {"id": "n2"}}

	t.Run("hasNextPage true is an error", func(t *testing.T) {
		mock, c := fetchTestClient(t)
		mock.SetResponse("Things", map[string]any{
			"thing": map[string]any{"things": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c1"},
				"nodes":    nodes,
			}},
		})
		_, err := fetchNodes[idNode](context.Background(), c, fetchTestQuery,
			map[string]any{"id": "t1"}, "thing", "things")
		if err == nil || !strings.Contains(err.Error(), "more pages") || !strings.Contains(err.Error(), `"thing.things"`) {
			t.Fatalf("err = %v, want truncation tripwire naming thing.things", err)
		}
	})

	t.Run("hasNextPage false is ok", func(t *testing.T) {
		mock, c := fetchTestClient(t)
		mock.SetResponse("Things", map[string]any{
			"thing": map[string]any{"things": map[string]any{
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": ""},
				"nodes":    nodes,
			}},
		})
		got, err := fetchNodes[idNode](context.Background(), c, fetchTestQuery,
			map[string]any{"id": "t1"}, "thing", "things")
		if err != nil {
			t.Fatalf("fetchNodes: %v", err)
		}
		if len(got) != 2 || got[0].ID != "n1" || got[1].ID != "n2" {
			t.Errorf("nodes = %v, want [n1 n2]", got)
		}
	})

	t.Run("absent pageInfo is ok", func(t *testing.T) {
		mock, c := fetchTestClient(t)
		mock.SetResponse("Things", map[string]any{
			"thing": map[string]any{"things": map[string]any{"nodes": nodes}},
		})
		got, err := fetchNodes[idNode](context.Background(), c, fetchTestQuery,
			map[string]any{"id": "t1"}, "thing", "things")
		if err != nil {
			t.Fatalf("fetchNodes: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("nodes = %v, want 2 nodes", got)
		}
	})
}

func TestFetchNodesNullConnectionIsError(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("Things", map[string]any{"thing": map[string]any{"things": nil}})
	_, err := fetchNodes[idNode](context.Background(), c, fetchTestQuery,
		map[string]any{"id": "t1"}, "thing", "things")
	if err == nil || !strings.Contains(err.Error(), `fetch: "thing.things" missing or null in response`) {
		t.Fatalf("err = %v, want fetch-prefixed missing-or-null error", err)
	}
}

func TestFetchOneNullTerminalIsError(t *testing.T) {
	// The old anonymous structs decoded {"issue": null} as a zero-value
	// Issue with a nil error; the fetch front refuses (the recorded
	// null-policy behavior change).
	mock, c := fetchTestClient(t)
	mock.SetResponse("Issue", map[string]any{"issue": nil})
	_, err := fetchOne[Issue](context.Background(), c, queryIssue,
		map[string]any{"id": "gone"}, "issue")
	if err == nil || !strings.Contains(err.Error(), `fetch: "issue" missing or null in response`) {
		t.Fatalf("err = %v, want fetch-prefixed missing-or-null error", err)
	}
}

func TestFetchOneDecodesTerminal(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("Issue", map[string]any{
		"issue": map[string]any{"id": "i1", "identifier": "TST-1", "title": "One"},
	})
	got, err := fetchOne[Issue](context.Background(), c, queryIssue,
		map[string]any{"id": "i1"}, "issue")
	if err != nil {
		t.Fatalf("fetchOne: %v", err)
	}
	if got.ID != "i1" || got.Identifier != "TST-1" || got.Title != "One" {
		t.Errorf("issue = %+v, want i1/TST-1/One", got)
	}
}

func TestFetchConnMissingPageInfoIsError(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("Things", map[string]any{
		"thing": map[string]any{"things": map[string]any{
			"nodes": []map[string]any{{"id": "n1"}},
		}},
	})
	_, err := fetchConn[idNode](context.Background(), c, fetchTestQuery,
		map[string]any{"id": "t1"}, "thing", "things")
	if err == nil || !strings.Contains(err.Error(), "missing pageInfo") || !strings.Contains(err.Error(), `"thing.things"`) {
		t.Fatalf("err = %v, want missing-pageInfo error naming thing.things", err)
	}
}

func TestFetchConnReturnsEnvelope(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("Things", map[string]any{
		"thing": map[string]any{"things": map[string]any{
			"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c9"},
			"nodes":    []map[string]any{{"id": "n1"}},
		}},
	})
	cn, err := fetchConn[idNode](context.Background(), c, fetchTestQuery,
		map[string]any{"id": "t1"}, "thing", "things")
	if err != nil {
		t.Fatalf("fetchConn: %v", err)
	}
	if cn.PageInfo == nil || !cn.PageInfo.HasNextPage || cn.PageInfo.EndCursor != "c9" {
		t.Errorf("pageInfo = %+v, want hasNextPage=true endCursor=c9", cn.PageInfo)
	}
	if len(cn.Nodes) != 1 || cn.Nodes[0].ID != "n1" {
		t.Errorf("nodes = %v, want [n1]", cn.Nodes)
	}
}

func TestFetchDoesNotMutateCallerVars(t *testing.T) {
	mock, c := fetchTestClient(t)
	mock.SetResponse("Things", map[string]any{
		"thing": map[string]any{"things": map[string]any{"nodes": []map[string]any{}}},
	})
	vars := map[string]any{"id": "t1"}
	if _, err := fetchNodes[idNode](context.Background(), c, fetchTestQuery, vars, "thing", "things"); err != nil {
		t.Fatalf("fetchNodes: %v", err)
	}
	if len(vars) != 1 || vars["id"] != "t1" {
		t.Errorf("caller vars mutated: %v", vars)
	}
}
