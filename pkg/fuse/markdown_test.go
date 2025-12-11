package fuse

import (
	"strings"
	"testing"
	"time"

	"github.com/jra3/linear-fuse/pkg/linear"
)

func TestIssueToMarkdown(t *testing.T) {
	now := time.Now()

	issue := &linear.Issue{
		ID:          "issue-1",
		Identifier:  "TEST-1",
		Title:       "Test Issue",
		Description: "This is a test issue\nwith multiple lines",
		Priority:    2,
		CreatedAt:   now,
		UpdatedAt:   now,
		State: linear.State{
			ID:   "state-1",
			Name: "In Progress",
			Type: "started",
		},
		Creator: linear.User{
			ID:    "user-1",
			Name:  "John Doe",
			Email: "john@example.com",
		},
		Team: linear.Team{
			ID:   "team-1",
			Key:  "TEST",
			Name: "Test Team",
		},
		Labels: linear.LabelConnection{
			Nodes: []linear.Label{
				{ID: "label-1", Name: "bug"},
				{ID: "label-2", Name: "urgent"},
			},
		},
	}

	markdown, err := issueToMarkdown(issue)
	if err != nil {
		t.Fatalf("Failed to convert issue to markdown: %v", err)
	}

	// Check that it contains frontmatter delimiters
	if !strings.HasPrefix(markdown, "---\n") {
		t.Error("Expected markdown to start with frontmatter delimiter")
	}

	// Check that it contains key fields
	if !strings.Contains(markdown, "identifier: TEST-1") {
		t.Error("Expected markdown to contain identifier")
	}
	if !strings.Contains(markdown, "title: Test Issue") {
		t.Error("Expected markdown to contain title")
	}
	if !strings.Contains(markdown, "state: In Progress") {
		t.Error("Expected markdown to contain state")
	}
	if !strings.Contains(markdown, "priority: 2") {
		t.Error("Expected markdown to contain priority")
	}
	if !strings.Contains(markdown, "team: Test Team") {
		t.Error("Expected markdown to contain team")
	}
	if !strings.Contains(markdown, "This is a test issue") {
		t.Error("Expected markdown to contain description")
	}
}

func TestParseMarkdownToIssue(t *testing.T) {
	markdown := `---
id: issue-1
identifier: TEST-1
title: Updated Title
state: In Progress
priority: 3
creator: John Doe
team: Test Team
---

This is the updated description.
More content here.`

	updates, err := parseMarkdownToIssue(markdown)
	if err != nil {
		t.Fatalf("Failed to parse markdown: %v", err)
	}

	if updates["title"] != "Updated Title" {
		t.Errorf("Expected title 'Updated Title', got %v", updates["title"])
	}

	if updates["priority"] != 3 {
		t.Errorf("Expected priority 3, got %v", updates["priority"])
	}

	description := updates["description"].(string)
	if !strings.Contains(description, "This is the updated description") {
		t.Error("Expected description to contain original text")
	}
}

func TestParseMarkdownToNewIssue(t *testing.T) {
	markdown := `---
title: New Issue
priority: 2
---

This is a new issue description.`

	input, err := parseMarkdownToNewIssue(markdown)
	if err != nil {
		t.Fatalf("Failed to parse new issue markdown: %v", err)
	}

	if input["title"] != "New Issue" {
		t.Errorf("Expected title 'New Issue', got %v", input["title"])
	}

	if input["priority"] != 2 {
		t.Errorf("Expected priority 2, got %v", input["priority"])
	}

	if input["description"] != "This is a new issue description." {
		t.Errorf("Unexpected description: %v", input["description"])
	}
}

func TestParseMarkdownInvalidFormat(t *testing.T) {
	markdown := "Just some text without frontmatter"

	_, err := parseMarkdownToIssue(markdown)
	if err == nil {
		t.Error("Expected error for invalid markdown format")
	}
}

func TestParseFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"TEST-1.md", "TEST-1"},
		{"ENG-123.md", "ENG-123"},
		{"invalid", ""},
		{"test.txt", ""},
		{".md", ""},
	}

	for _, tt := range tests {
		result := parseFilename(tt.input)
		if result != tt.expected {
			t.Errorf("parseFilename(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestIsValidFilename(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"TEST-1.md", true},
		{"issue.md", true},
		{"test.txt", false},
		{"noextension", false},
		{".md", false},
	}

	for _, tt := range tests {
		result := isValidFilename(tt.input)
		if result != tt.expected {
			t.Errorf("isValidFilename(%q) = %v, want %v", tt.input, result, tt.expected)
		}
	}
}
