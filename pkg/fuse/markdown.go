package fuse

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/jra3/linear-fuse/pkg/linear"
	"gopkg.in/yaml.v3"
)

// FrontMatter represents the YAML frontmatter for an issue
type FrontMatter struct {
	ID         string   `yaml:"id"`
	Identifier string   `yaml:"identifier"`
	Title      string   `yaml:"title"`
	State      string   `yaml:"state"`
	Priority   int      `yaml:"priority"`
	Assignee   string   `yaml:"assignee,omitempty"`
	Creator    string   `yaml:"creator"`
	Team       string   `yaml:"team"`
	Labels     []string `yaml:"labels,omitempty"`
	CreatedAt  string   `yaml:"created_at"`
	UpdatedAt  string   `yaml:"updated_at"`
}

// issueToMarkdown converts a Linear issue to markdown with YAML frontmatter
func issueToMarkdown(issue *linear.Issue) (string, error) {
	// Create frontmatter
	fm := FrontMatter{
		ID:         issue.ID,
		Identifier: issue.Identifier,
		Title:      issue.Title,
		State:      issue.State.Name,
		Priority:   issue.Priority,
		Creator:    issue.Creator.Name,
		Team:       issue.Team.Name,
		CreatedAt:  issue.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt:  issue.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}

	if issue.Assignee != nil {
		fm.Assignee = issue.Assignee.Name
	}

	// Add labels
	for _, label := range issue.Labels.Nodes {
		fm.Labels = append(fm.Labels, label.Name)
	}

	// Marshal frontmatter to YAML
	var buf bytes.Buffer
	buf.WriteString("---\n")
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&fm); err != nil {
		return "", fmt.Errorf("failed to encode frontmatter: %w", err)
	}
	buf.WriteString("---\n\n")

	// Add description
	if issue.Description != "" {
		buf.WriteString(issue.Description)
		buf.WriteString("\n")
	}

	return buf.String(), nil
}

// parseMarkdownToIssue parses markdown with frontmatter and returns update fields
func parseMarkdownToIssue(content string) (map[string]interface{}, error) {
	// Split frontmatter and description
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid markdown format: missing frontmatter")
	}

	// Parse frontmatter
	var fm FrontMatter
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	// Extract description (everything after second ---)
	description := strings.TrimSpace(parts[2])

	// Build update input
	updates := make(map[string]interface{})

	// Only include fields that can be updated
	// Note: Some fields like ID, identifier, creator are immutable
	updates["title"] = fm.Title
	updates["description"] = description
	updates["priority"] = fm.Priority

	return updates, nil
}

// parseMarkdownToNewIssue parses markdown with frontmatter for creating a new issue
func parseMarkdownToNewIssue(content string) (map[string]interface{}, error) {
	// Split frontmatter and description
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid markdown format: missing frontmatter")
	}

	// Parse frontmatter
	var fm FrontMatter
	if err := yaml.Unmarshal([]byte(parts[1]), &fm); err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	// Extract description (everything after second ---)
	description := strings.TrimSpace(parts[2])

	// Build create input
	input := make(map[string]interface{})

	// Title is required
	if fm.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	input["title"] = fm.Title

	if description != "" {
		input["description"] = description
	}

	if fm.Priority > 0 {
		input["priority"] = fm.Priority
	}

	// Team is typically required, but we'll let the API handle validation
	// Users can specify it in the frontmatter if needed

	return input, nil
}
