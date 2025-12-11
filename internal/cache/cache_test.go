package cache

import (
	"testing"
	"time"

	"github.com/jra3/linear-fuse/pkg/linear"
)

func TestCacheGetSet(t *testing.T) {
	c := New(1 * time.Second)

	issue := &linear.Issue{
		ID:         "issue-1",
		Identifier: "TEST-1",
		Title:      "Test Issue",
	}

	// Set and get
	c.Set(issue.ID, issue)
	retrieved, ok := c.Get(issue.ID)

	if !ok {
		t.Fatal("Expected to find issue in cache")
	}

	if retrieved.ID != issue.ID {
		t.Errorf("Expected ID %s, got %s", issue.ID, retrieved.ID)
	}
}

func TestCacheExpiration(t *testing.T) {
	c := New(100 * time.Millisecond)

	issue := &linear.Issue{
		ID:         "issue-1",
		Identifier: "TEST-1",
		Title:      "Test Issue",
	}

	c.Set(issue.ID, issue)

	// Should exist immediately
	_, ok := c.Get(issue.ID)
	if !ok {
		t.Fatal("Expected to find issue in cache")
	}

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Should be expired
	_, ok = c.Get(issue.ID)
	if ok {
		t.Fatal("Expected issue to be expired")
	}
}

func TestCacheSetList(t *testing.T) {
	c := New(1 * time.Second)

	issues := []linear.Issue{
		{ID: "issue-1", Identifier: "TEST-1", Title: "Test 1"},
		{ID: "issue-2", Identifier: "TEST-2", Title: "Test 2"},
	}

	c.SetList(issues)

	// Check list
	ids, ok := c.GetList()
	if !ok {
		t.Fatal("Expected to find list in cache")
	}

	if len(ids) != 2 {
		t.Errorf("Expected 2 issues, got %d", len(ids))
	}

	// Check individual items
	for _, issue := range issues {
		cached, ok := c.Get(issue.ID)
		if !ok {
			t.Errorf("Expected to find issue %s in cache", issue.ID)
		}
		if cached.Title != issue.Title {
			t.Errorf("Expected title %s, got %s", issue.Title, cached.Title)
		}
	}
}

func TestCacheDelete(t *testing.T) {
	c := New(1 * time.Second)

	issue := &linear.Issue{
		ID:         "issue-1",
		Identifier: "TEST-1",
		Title:      "Test Issue",
	}

	c.Set(issue.ID, issue)

	// Should exist
	_, ok := c.Get(issue.ID)
	if !ok {
		t.Fatal("Expected to find issue in cache")
	}

	// Delete
	c.Delete(issue.ID)

	// Should not exist
	_, ok = c.Get(issue.ID)
	if ok {
		t.Fatal("Expected issue to be deleted")
	}
}

func TestCacheClear(t *testing.T) {
	c := New(1 * time.Second)

	issues := []linear.Issue{
		{ID: "issue-1", Identifier: "TEST-1", Title: "Test 1"},
		{ID: "issue-2", Identifier: "TEST-2", Title: "Test 2"},
	}

	c.SetList(issues)

	// Clear cache
	c.Clear()

	// List should be empty
	_, ok := c.GetList()
	if ok {
		t.Fatal("Expected cache to be cleared")
	}

	// Individual items should be gone
	_, ok = c.Get("issue-1")
	if ok {
		t.Fatal("Expected cache to be cleared")
	}
}
