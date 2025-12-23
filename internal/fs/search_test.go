package fs

import (
	"context"
	"syscall"
	"testing"

	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/jra3/linear-fuse/internal/api"
)

// TestDecodeSearchQuery is in linearfs_test.go - additional edge cases here
func TestDecodeSearchQuery_EdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		encoded string
		want    string
	}{
		{
			name:    "only plus signs",
			encoded: "+++",
			want:    "   ",
		},
		{
			name:    "leading and trailing plus",
			encoded: "+query+",
			want:    " query ",
		},
		{
			name:    "consecutive plus signs",
			encoded: "a++b",
			want:    "a  b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decodeSearchQuery(tt.encoded)
			if got != tt.want {
				t.Errorf("decodeSearchQuery(%q) = %q, want %q", tt.encoded, got, tt.want)
			}
		})
	}
}

func TestMatchesQuery(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		issue api.Issue
		query string
		want  bool
	}{
		{
			name: "matches identifier",
			issue: api.Issue{
				Identifier:  "ENG-123",
				Title:       "Some Title",
				Description: "Some description",
			},
			query: "eng-123",
			want:  true,
		},
		{
			name: "matches title",
			issue: api.Issue{
				Identifier:  "ENG-123",
				Title:       "Login error handling",
				Description: "Some description",
			},
			query: "login",
			want:  true,
		},
		{
			name: "matches description",
			issue: api.Issue{
				Identifier:  "ENG-123",
				Title:       "Some Title",
				Description: "Fix the authentication bug",
			},
			query: "authentication",
			want:  true,
		},
		{
			name: "case insensitive match",
			issue: api.Issue{
				Identifier:  "ENG-123",
				Title:       "API Rate Limiting",
				Description: "Some description",
			},
			query: "api rate",
			want:  true,
		},
		{
			name: "no match",
			issue: api.Issue{
				Identifier:  "ENG-123",
				Title:       "Some Title",
				Description: "Some description",
			},
			query: "nonexistent",
			want:  false,
		},
		{
			name: "empty query matches everything (strings.Contains behavior)",
			issue: api.Issue{
				Identifier:  "ENG-123",
				Title:       "Some Title",
				Description: "Some description",
			},
			query: "",
			want:  true,
		},
		{
			name: "partial identifier match",
			issue: api.Issue{
				Identifier:  "ENG-1234",
				Title:       "Title",
				Description: "Description",
			},
			query: "123",
			want:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchesQuery(tt.issue, tt.query)
			if got != tt.want {
				t.Errorf("matchesQuery() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestScopedSearchSymlink_Target(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		symlink      ScopedSearchSymlink
		wantTarget   string
	}{
		{
			name: "depth 3 (my/assigned/search/query)",
			symlink: ScopedSearchSymlink{
				teamKey:      "ENG",
				identifier:   "ENG-123",
				symlinkDepth: 4,
			},
			wantTarget: "../../../../teams/ENG/issues/ENG-123",
		},
		{
			name: "depth 6 (teams/ENG/by/status/Todo/search/query)",
			symlink: ScopedSearchSymlink{
				teamKey:      "ENG",
				identifier:   "ENG-456",
				symlinkDepth: 7,
			},
			wantTarget: "../../../../../../../teams/ENG/issues/ENG-456",
		},
		{
			name: "depth 0",
			symlink: ScopedSearchSymlink{
				teamKey:      "TST",
				identifier:   "TST-1",
				symlinkDepth: 0,
			},
			wantTarget: "teams/TST/issues/TST-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.symlink.target()
			if got != tt.wantTarget {
				t.Errorf("target() = %q, want %q", got, tt.wantTarget)
			}
		})
	}
}

func TestScopedSearchSymlink_Readlink(t *testing.T) {
	t.Parallel()
	symlink := &ScopedSearchSymlink{
		teamKey:      "ENG",
		identifier:   "ENG-123",
		symlinkDepth: 4,
	}

	target, errno := symlink.Readlink(nil)
	if errno != 0 {
		t.Fatalf("Readlink returned error: %v", errno)
	}

	want := "../../../../teams/ENG/issues/ENG-123"
	if string(target) != want {
		t.Errorf("Readlink() = %q, want %q", string(target), want)
	}
}

func TestScopedSearchSymlink_Getattr(t *testing.T) {
	t.Parallel()
	symlink := &ScopedSearchSymlink{
		teamKey:      "ENG",
		identifier:   "ENG-123",
		symlinkDepth: 4,
	}

	var out fuse.AttrOut
	errno := symlink.Getattr(nil, nil, &out)
	if errno != 0 {
		t.Fatalf("Getattr returned error: %v", errno)
	}

	// Check mode is symlink
	if out.Mode&syscall.S_IFLNK == 0 {
		t.Error("Mode should indicate symlink")
	}

	// Check size matches target length
	expectedTarget := "../../../../teams/ENG/issues/ENG-123"
	if out.Size != uint64(len(expectedTarget)) {
		t.Errorf("Size = %d, want %d", out.Size, len(expectedTarget))
	}
}

func TestSearchResultSymlink_Readlink(t *testing.T) {
	t.Parallel()
	symlink := &SearchResultSymlink{
		identifier: "TST-456",
	}

	target, errno := symlink.Readlink(nil)
	if errno != 0 {
		t.Fatalf("Readlink returned error: %v", errno)
	}

	want := "../../issues/TST-456"
	if string(target) != want {
		t.Errorf("Readlink() = %q, want %q", string(target), want)
	}
}

func TestSearchResultSymlink_Getattr(t *testing.T) {
	t.Parallel()
	symlink := &SearchResultSymlink{
		identifier: "TST-456",
	}

	var out fuse.AttrOut
	errno := symlink.Getattr(nil, nil, &out)
	if errno != 0 {
		t.Fatalf("Getattr returned error: %v", errno)
	}

	// Check mode is symlink
	if out.Mode&syscall.S_IFLNK == 0 {
		t.Error("Mode should indicate symlink")
	}

	// Check size matches target length
	expectedTarget := "../../issues/TST-456"
	if out.Size != uint64(len(expectedTarget)) {
		t.Errorf("Size = %d, want %d", out.Size, len(expectedTarget))
	}
}

func TestIssueSourceFunc(t *testing.T) {
	t.Parallel()

	issues := []api.Issue{
		{ID: "1", Identifier: "TST-1", Title: "Test Issue 1"},
		{ID: "2", Identifier: "TST-2", Title: "Test Issue 2"},
	}

	source := IssueSourceFunc(func(ctx context.Context) ([]api.Issue, error) {
		return issues, nil
	})

	got, err := source.GetIssues(nil)
	if err != nil {
		t.Fatalf("GetIssues returned error: %v", err)
	}

	if len(got) != len(issues) {
		t.Errorf("GetIssues returned %d issues, want %d", len(got), len(issues))
	}
}

func TestIssueSourceFunc_Error(t *testing.T) {
	t.Parallel()

	expectedErr := syscall.EIO
	source := IssueSourceFunc(func(ctx context.Context) ([]api.Issue, error) {
		return nil, expectedErr
	})

	_, err := source.GetIssues(nil)
	if err != expectedErr {
		t.Errorf("GetIssues error = %v, want %v", err, expectedErr)
	}
}

func TestSearchNode_Getattr(t *testing.T) {
	t.Parallel()
	node := &SearchNode{}

	var out fuse.AttrOut
	errno := node.Getattr(nil, nil, &out)
	if errno != 0 {
		t.Fatalf("Getattr returned error: %v", errno)
	}

	// Check mode is directory
	if out.Mode&syscall.S_IFDIR == 0 {
		t.Error("Mode should indicate directory")
	}
}

func TestSearchNode_Readdir(t *testing.T) {
	t.Parallel()
	node := &SearchNode{}

	stream, errno := node.Readdir(nil)
	if errno != 0 {
		t.Fatalf("Readdir returned error: %v", errno)
	}

	// Search directory should be empty
	if stream == nil {
		t.Error("Readdir should return a stream")
	}
}

func TestSearchResultsNode_Getattr(t *testing.T) {
	t.Parallel()
	node := &SearchResultsNode{
		query: "test",
	}

	var out fuse.AttrOut
	errno := node.Getattr(nil, nil, &out)
	if errno != 0 {
		t.Fatalf("Getattr returned error: %v", errno)
	}

	// Check mode is directory
	if out.Mode&syscall.S_IFDIR == 0 {
		t.Error("Mode should indicate directory")
	}
}

func TestScopedSearchNode_Getattr(t *testing.T) {
	t.Parallel()
	node := &ScopedSearchNode{}

	var out fuse.AttrOut
	errno := node.Getattr(nil, nil, &out)
	if errno != 0 {
		t.Fatalf("Getattr returned error: %v", errno)
	}

	// Check mode is directory
	if out.Mode&syscall.S_IFDIR == 0 {
		t.Error("Mode should indicate directory")
	}
}

func TestScopedSearchNode_Readdir(t *testing.T) {
	t.Parallel()
	node := &ScopedSearchNode{}

	stream, errno := node.Readdir(nil)
	if errno != 0 {
		t.Fatalf("Readdir returned error: %v", errno)
	}

	// Search directory should be empty
	if stream == nil {
		t.Error("Readdir should return a stream")
	}
}

func TestScopedSearchResultsNode_Getattr(t *testing.T) {
	t.Parallel()
	node := &ScopedSearchResultsNode{
		query: "test",
	}

	var out fuse.AttrOut
	errno := node.Getattr(nil, nil, &out)
	if errno != 0 {
		t.Fatalf("Getattr returned error: %v", errno)
	}

	// Check mode is directory
	if out.Mode&syscall.S_IFDIR == 0 {
		t.Error("Mode should indicate directory")
	}
}
