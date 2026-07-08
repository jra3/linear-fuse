package reconcile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/jra3/linear-fuse/internal/db"
)

// =============================================================================
// Embedded Files Extraction
// =============================================================================

// markdownLinkPattern matches markdown links/images with Linear CDN URLs
// Captures: [1] = display name, [2] = full URL
// Matches: ![image.png](https://uploads.linear.app/...) or [file.md](https://uploads.linear.app/...)
var markdownLinkPattern = regexp.MustCompile(`!?\[([^\]]*)\]\((https://uploads\.linear\.app/[^\s\)]+)\)`)

// linearCDNPattern matches bare Linear CDN URLs (fallback when not in markdown syntax)
var linearCDNPattern = regexp.MustCompile(`https://uploads\.linear\.app/[^\s\)\]"'<>]+`)

// embeddedFileSpec is one embedded file parsed out of content — everything about
// it that is derivable without I/O: a stable id (from the URL), the display name
// (markdown link text, else derived from the URL), and its MIME type. The size
// (a HEAD request) and persistence are the caller's job.
type embeddedFileSpec struct {
	ID       string
	IssueID  string
	URL      string
	Filename string
	MimeType string
	Source   string
}

// extractEmbeddedFiles is the pure half of embedded-file sync: it parses content
// for Linear CDN URLs and returns one spec per URL, associating a markdown link's
// display text with its URL where present. No HEAD request, no DB — so the
// tricky parts (name↔URL association, id stability, filename/MIME derivation)
// are unit-testable on literal strings. One spec per URL occurrence, matching the
// upsert-per-occurrence the store loop did (the id is stable, so repeats are
// idempotent).
func extractEmbeddedFiles(content, issueID, source string) []embeddedFileSpec {
	// Markdown-formatted links carry display names: [name](url).
	urlToName := make(map[string]string)
	for _, match := range markdownLinkPattern.FindAllStringSubmatch(content, -1) {
		if len(match) >= 3 {
			if displayName := strings.TrimSpace(match[1]); displayName != "" {
				urlToName[match[2]] = displayName
			}
		}
	}

	urls := linearCDNPattern.FindAllString(content, -1)
	specs := make([]embeddedFileSpec, 0, len(urls))
	for _, url := range urls {
		// Clean up trailing punctuation that the URL pattern may have captured.
		url = strings.TrimRight(url, ".,;:!?")

		// Stable ID from the URL (first 16 bytes of the SHA-256).
		hash := sha256.Sum256([]byte(url))
		id := hex.EncodeToString(hash[:16])

		filename := urlToName[url]
		if filename == "" {
			filename = extractFilename(url)
		}

		specs = append(specs, embeddedFileSpec{
			ID:       id,
			IssueID:  issueID,
			URL:      url,
			Filename: filename,
			MimeType: detectMIMEType(filename),
			Source:   source,
		})
	}
	return specs
}

// Extractor owns the I/O tail of embedded-file sync: HEAD each parsed CDN URL
// for its size and upsert the row. HTTPClient nil means http.DefaultClient —
// injectable for tests (the embeddedFileCache precedent in internal/fs).
type Extractor struct {
	Q          *db.Queries
	AuthHeader func() string
	HTTPClient *http.Client
}

// ExtractAndStore parses content for Linear CDN URLs, fetches each one's size,
// and upserts it. Parsing is the pure extractEmbeddedFiles; this owns the I/O
// tail (HEAD + upsert).
func (e *Extractor) ExtractAndStore(ctx context.Context, issueID, content, source string) {
	for _, spec := range extractEmbeddedFiles(content, issueID, source) {
		// Fetch file size via HEAD request (doesn't download the file).
		fileSize := e.fetchFileSize(ctx, spec.URL)

		params := db.UpsertEmbeddedFileParams{
			ID:        spec.ID,
			IssueID:   spec.IssueID,
			Url:       spec.URL,
			Filename:  spec.Filename,
			MimeType:  sql.NullString{String: spec.MimeType, Valid: spec.MimeType != ""},
			FileSize:  sql.NullInt64{Int64: fileSize, Valid: fileSize > 0},
			Source:    spec.Source,
			CreatedAt: db.Now(),
			SyncedAt:  db.Now(),
		}

		if err := e.Q.UpsertEmbeddedFile(ctx, params); err != nil {
			log.Printf("[reconcile] upsert embedded file %s failed: %v", spec.Filename, err)
		}
	}
}

// fetchFileSize gets the file size via HTTP HEAD request without downloading
func (e *Extractor) fetchFileSize(ctx context.Context, url string) int64 {
	req, err := http.NewRequestWithContext(ctx, "HEAD", url, nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", e.AuthHeader())

	client := e.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0
	}

	return resp.ContentLength
}

// extractFilename extracts a clean filename from a Linear CDN URL
func extractFilename(url string) string {
	// Linear CDN URLs look like:
	// https://uploads.linear.app/abc123/def456/filename.png
	// or with UUID-prefixed filenames

	// Get the last path segment
	parts := strings.Split(url, "/")
	if len(parts) == 0 {
		return "file"
	}
	filename := parts[len(parts)-1]

	// Remove query parameters
	if idx := strings.Index(filename, "?"); idx != -1 {
		filename = filename[:idx]
	}

	// If the filename looks like a UUID-prefixed file, try to clean it up
	// e.g., "abc123-def456-screenshot.png" -> "screenshot.png"
	// But keep it if it seems intentional
	if len(filename) > 40 && strings.Count(filename, "-") >= 4 {
		// This might be a UUID-prefixed filename, extract just the meaningful part
		lastDash := strings.LastIndex(filename, "-")
		if lastDash > 0 && lastDash < len(filename)-1 {
			potentialName := filename[lastDash+1:]
			if strings.Contains(potentialName, ".") {
				filename = potentialName
			}
		}
	}

	// Ensure we have at least some filename
	if filename == "" {
		filename = "file"
	}

	return filename
}

// detectMIMEType detects MIME type from filename extension
func detectMIMEType(filename string) string {
	ext := strings.ToLower(path.Ext(filename))
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".doc":
		return "application/msword"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".xls":
		return "application/vnd.ms-excel"
	case ".xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case ".zip":
		return "application/zip"
	case ".mp4":
		return "video/mp4"
	case ".mov":
		return "video/quicktime"
	case ".mp3":
		return "audio/mpeg"
	default:
		return "application/octet-stream"
	}
}
