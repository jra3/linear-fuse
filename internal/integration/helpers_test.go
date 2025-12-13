package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Path builders

func rootPath() string {
	return mountPoint
}

func teamsPath() string {
	return filepath.Join(mountPoint, "teams")
}

func teamPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey)
}

func teamInfoPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, ".team.md")
}

func teamStatesPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, ".states.md")
}

func teamLabelsPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, ".labels.md")
}

func issuesPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues")
}

func issueDirPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID)
}

func issueFilePath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "issue.md")
}

func commentsPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "comments")
}

func commentFilePath(teamKey, issueID, filename string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "comments", filename)
}

func newCommentPath(teamKey, issueID string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "issues", issueID, "comments", "new.md")
}

func cyclesPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "cycles")
}

func projectsPath(teamKey string) string {
	return filepath.Join(mountPoint, "teams", teamKey, "projects")
}

func myPath() string {
	return filepath.Join(mountPoint, "my")
}

func myAssignedPath() string {
	return filepath.Join(mountPoint, "my", "assigned")
}

func myCreatedPath() string {
	return filepath.Join(mountPoint, "my", "created")
}

func myActivePath() string {
	return filepath.Join(mountPoint, "my", "active")
}

func usersPath() string {
	return filepath.Join(mountPoint, "users")
}

func userPath(username string) string {
	return filepath.Join(mountPoint, "users", username)
}

func userInfoPath(username string) string {
	return filepath.Join(mountPoint, "users", username, ".user.md")
}

// Retry helpers

func readFileWithRetry(path string, maxWait time.Duration) ([]byte, error) {
	deadline := time.Now().Add(maxWait)
	var lastErr error

	for time.Now().Before(deadline) {
		content, err := os.ReadFile(path)
		if err == nil {
			return content, nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	return nil, fmt.Errorf("failed to read %s after %v: %w", path, maxWait, lastErr)
}

func waitForFile(path string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("file %s not found after %v", path, maxWait)
}

func waitForFileGone(path string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("file %s still exists after %v", path, maxWait)
}

func waitForDirEntry(dir, name string, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)

	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range entries {
				if e.Name() == name {
					return nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	return fmt.Errorf("entry %s not found in %s after %v", name, dir, maxWait)
}

const defaultWaitTime = 5 * time.Second

func waitForCacheExpiry() {
	time.Sleep(3 * time.Second) // Cache TTL is 2s, wait a bit longer
}

// Frontmatter helpers

type Document struct {
	Frontmatter map[string]any
	Body        string
}

func parseFrontmatter(content []byte) (*Document, error) {
	str := string(content)

	if !strings.HasPrefix(str, "---\n") {
		return &Document{Body: str}, nil
	}

	end := strings.Index(str[4:], "\n---")
	if end == -1 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}

	yamlContent := str[4 : 4+end]
	body := strings.TrimPrefix(str[4+end+4:], "\n")

	var frontmatter map[string]any
	if err := yaml.Unmarshal([]byte(yamlContent), &frontmatter); err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	return &Document{
		Frontmatter: frontmatter,
		Body:        body,
	}, nil
}

func extractField(content []byte, field string) (any, error) {
	doc, err := parseFrontmatter(content)
	if err != nil {
		return nil, err
	}

	val, ok := doc.Frontmatter[field]
	if !ok {
		return nil, fmt.Errorf("field %q not found", field)
	}

	return val, nil
}

func modifyFrontmatter(content []byte, field string, value any) ([]byte, error) {
	doc, err := parseFrontmatter(content)
	if err != nil {
		return nil, err
	}

	if doc.Frontmatter == nil {
		doc.Frontmatter = make(map[string]any)
	}
	doc.Frontmatter[field] = value

	yamlBytes, err := yaml.Marshal(doc.Frontmatter)
	if err != nil {
		return nil, err
	}

	return []byte(fmt.Sprintf("---\n%s---\n%s", string(yamlBytes), doc.Body)), nil
}

func removeFrontmatterField(content []byte, field string) ([]byte, error) {
	doc, err := parseFrontmatter(content)
	if err != nil {
		return nil, err
	}

	delete(doc.Frontmatter, field)

	yamlBytes, err := yaml.Marshal(doc.Frontmatter)
	if err != nil {
		return nil, err
	}

	return []byte(fmt.Sprintf("---\n%s---\n%s", string(yamlBytes), doc.Body)), nil
}

// Directory listing helpers

func listDirNames(path string) ([]string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}

	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	return names, nil
}

func dirContains(path, name string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}

	for _, e := range entries {
		if e.Name() == name {
			return true
		}
	}
	return false
}
