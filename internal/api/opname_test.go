package api

import "testing"

func TestExtractOpName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "simple query",
			query: "query GetTeams { teams { nodes { id } } }",
			want:  "GetTeams",
		},
		{
			name:  "query with variables",
			query: "query GetTeamIssues($teamId: String!) { team(id: $teamId) { issues { nodes { id } } } }",
			want:  "GetTeamIssues",
		},
		{
			name:  "mutation",
			query: "mutation UpdateIssue($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }",
			want:  "UpdateIssue",
		},
		{
			name: "multiline query",
			query: `query GetTeamIssuesPage($teamId: String!, $first: Int!, $after: String) {
  team(id: $teamId) {
    issues(first: $first, after: $after) {
      nodes { id }
    }
  }
}`,
			want: "GetTeamIssuesPage",
		},
		{
			name:  "no operation name",
			query: "{ teams { nodes { id } } }",
			want:  "unknown",
		},
		{
			name:  "empty query",
			query: "",
			want:  "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOpName(tt.query)
			if got != tt.want {
				t.Errorf("extractOpName() = %q, want %q", got, tt.want)
			}
		})
	}
}
