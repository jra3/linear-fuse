package reconcile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jra3/linear-fuse/internal/api"
	"github.com/jra3/linear-fuse/internal/db"
)

// Helper to open test store
func openTestStore(t *testing.T) *db.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	store, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	return store
}

func TestExtractFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "simple filename",
			url:      "https://uploads.linear.app/abc123/def456/screenshot.png",
			expected: "screenshot.png",
		},
		{
			name:     "filename with query params",
			url:      "https://uploads.linear.app/abc123/def456/image.jpg?token=xyz",
			expected: "image.jpg",
		},
		{
			name:     "UUID-prefixed filename",
			url:      "https://uploads.linear.app/abc123/def456/a1b2c3d4-e5f6-7890-abcd-ef1234567890-screenshot.png",
			expected: "screenshot.png",
		},
		{
			name:     "simple filename without UUID",
			url:      "https://uploads.linear.app/abc123/design.pdf",
			expected: "design.pdf",
		},
		{
			name:     "empty path segment",
			url:      "https://uploads.linear.app/",
			expected: "file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilename(tt.url)
			if got != tt.expected {
				t.Errorf("extractFilename(%q) = %q, want %q", tt.url, got, tt.expected)
			}
		})
	}
}

func TestDetectMIMEType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		filename string
		expected string
	}{
		{"image.png", "image/png"},
		{"photo.jpg", "image/jpeg"},
		{"photo.jpeg", "image/jpeg"},
		{"animation.gif", "image/gif"},
		{"icon.webp", "image/webp"},
		{"logo.svg", "image/svg+xml"},
		{"document.pdf", "application/pdf"},
		{"report.doc", "application/msword"},
		{"report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"data.xls", "application/vnd.ms-excel"},
		{"data.xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"archive.zip", "application/zip"},
		{"video.mp4", "video/mp4"},
		{"video.mov", "video/quicktime"},
		{"audio.mp3", "audio/mpeg"},
		{"unknown.xyz", "application/octet-stream"},
		{"noextension", "application/octet-stream"},
		{"IMAGE.PNG", "image/png"}, // Case insensitive
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := detectMIMEType(tt.filename)
			if got != tt.expected {
				t.Errorf("detectMIMEType(%q) = %q, want %q", tt.filename, got, tt.expected)
			}
		})
	}
}

func TestLinearCDNPattern(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  string
		expected []string
	}{
		{
			name:    "single URL in markdown",
			content: "Check out this image: ![screenshot](https://uploads.linear.app/abc123/def456/screenshot.png)",
			expected: []string{
				"https://uploads.linear.app/abc123/def456/screenshot.png",
			},
		},
		{
			name: "multiple URLs",
			content: `Here are the designs:
![design1](https://uploads.linear.app/org1/file1/design1.png)
![design2](https://uploads.linear.app/org2/file2/design2.jpg)`,
			expected: []string{
				"https://uploads.linear.app/org1/file1/design1.png",
				"https://uploads.linear.app/org2/file2/design2.jpg",
			},
		},
		{
			name:     "URL with query params",
			content:  "Image: https://uploads.linear.app/abc/def/image.png?token=xyz123",
			expected: []string{"https://uploads.linear.app/abc/def/image.png?token=xyz123"},
		},
		{
			name:     "no Linear CDN URLs",
			content:  "Regular text with https://example.com/image.png",
			expected: nil,
		},
		{
			name:     "empty content",
			content:  "",
			expected: nil,
		},
		{
			name:    "URL in angle brackets",
			content: "See <https://uploads.linear.app/abc/def/file.pdf>",
			expected: []string{
				"https://uploads.linear.app/abc/def/file.pdf",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := linearCDNPattern.FindAllString(tt.content, -1)

			if len(got) != len(tt.expected) {
				t.Errorf("Found %d URLs, want %d\nGot: %v\nWant: %v", len(got), len(tt.expected), got, tt.expected)
				return
			}

			for i, url := range tt.expected {
				if got[i] != url {
					t.Errorf("URL[%d] = %q, want %q", i, got[i], url)
				}
			}
		})
	}
}

func TestExtractAndStoreEmbeddedFiles(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	defer store.Close()

	// The CDN client's injectable transport routes the size HEADs to a local
	// server instead of the real CDN.
	headCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headCalls++
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	cdn := api.NewCDNClient(func() string { return "test-auth" })
	cdn.SetHTTPClient(&http.Client{Transport: rewriteTransport{target: srv.URL}})
	extractor := &Extractor{
		Q:   store.Queries(),
		CDN: cdn,
	}

	ctx := context.Background()
	issueID := "test-issue-123"

	// Content with embedded files
	// - First file: markdown with display name becomes the filename
	// - Second file: bare URL, filename extracted from path
	content := `Here's a screenshot of the bug:
![bug-screenshot.png](https://uploads.linear.app/workspace1/issue1/bug-screenshot.png)

And here's the design spec:
https://uploads.linear.app/workspace1/issue1/design-spec.pdf`

	extractor.ExtractAndStore(ctx, issueID, content, "description")

	// Verify files were stored
	files, err := store.Queries().ListIssueEmbeddedFiles(ctx, issueID)
	if err != nil {
		t.Fatalf("ListIssueEmbeddedFiles failed: %v", err)
	}

	if len(files) != 2 {
		t.Errorf("Expected 2 embedded files, got %d", len(files))
	}
	if headCalls != 2 {
		t.Errorf("Expected 2 HEAD size probes through the injected client, got %d", headCalls)
	}

	// Verify file details
	foundPNG := false
	foundPDF := false
	for _, f := range files {
		if f.Filename == "bug-screenshot.png" {
			foundPNG = true
			if f.MimeType.String != "image/png" {
				t.Errorf("Expected MIME type image/png, got %s", f.MimeType.String)
			}
			if f.Source != "description" {
				t.Errorf("Expected source 'description', got %s", f.Source)
			}
		}
		if f.Filename == "design-spec.pdf" {
			foundPDF = true
			if f.MimeType.String != "application/pdf" {
				t.Errorf("Expected MIME type application/pdf, got %s", f.MimeType.String)
			}
		}
	}

	if !foundPNG {
		t.Error("Did not find bug-screenshot.png in stored files")
	}
	if !foundPDF {
		t.Error("Did not find design-spec.pdf in stored files")
	}
}

// rewriteTransport redirects every request to the test server, preserving the
// original path, so CDN-shaped URLs never leave the process.
type rewriteTransport struct {
	target string
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	redirected := rt.target + req.URL.Path
	newReq := req.Clone(req.Context())
	u, err := newReq.URL.Parse(redirected)
	if err != nil {
		return nil, err
	}
	newReq.URL = u
	newReq.Host = u.Host
	return http.DefaultTransport.RoundTrip(newReq)
}

// TestExtractEmbeddedFiles covers the pure parsing half — no DB, no HEAD: the
// markdown display-name→URL association, bare-URL filename derivation, trailing
// punctuation trimming, MIME detection, and a stable URL-derived id. This is the
// tricky logic that previously could only be exercised through SQLite.
func TestExtractEmbeddedFiles(t *testing.T) {
	t.Parallel()
	content := `A screenshot:
![bug shot.png](https://uploads.linear.app/ws1/i1/bug-shot.png)

A bare link ending a sentence:
https://uploads.linear.app/ws1/i1/design-spec.pdf.`

	specs := extractEmbeddedFiles(content, "issue-1", "description")
	if len(specs) != 2 {
		t.Fatalf("got %d specs, want 2: %+v", len(specs), specs)
	}

	// Markdown link: display text becomes the filename.
	md := specs[0]
	if md.Filename != "bug shot.png" {
		t.Errorf("markdown filename = %q, want %q", md.Filename, "bug shot.png")
	}
	if md.MimeType != "image/png" {
		t.Errorf("markdown mime = %q, want image/png", md.MimeType)
	}
	if md.IssueID != "issue-1" || md.Source != "description" {
		t.Errorf("spec carry-through wrong: %+v", md)
	}

	// Bare URL: trailing '.' trimmed, filename derived from the path.
	bare := specs[1]
	if strings.HasSuffix(bare.URL, ".") {
		t.Errorf("trailing punctuation not trimmed: %q", bare.URL)
	}
	if bare.Filename != "design-spec.pdf" {
		t.Errorf("bare filename = %q, want design-spec.pdf", bare.Filename)
	}
	if bare.MimeType != "application/pdf" {
		t.Errorf("bare mime = %q, want application/pdf", bare.MimeType)
	}

	// The id is a stable function of the (trimmed) URL.
	again := extractEmbeddedFiles(bare.URL, "issue-2", "comment")
	if len(again) != 1 || again[0].ID != bare.ID {
		t.Errorf("id not stable for the same URL: %v vs %s", again, bare.ID)
	}

	// No CDN URLs → no specs.
	if got := extractEmbeddedFiles("just prose, no links", "i", "s"); len(got) != 0 {
		t.Errorf("want no specs for plain content, got %d", len(got))
	}
}
