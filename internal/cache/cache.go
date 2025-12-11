package cache

import (
	"sync"
	"time"

	"github.com/jra3/linear-fuse/pkg/linear"
)

// Cache provides a simple in-memory cache for Linear issues
type Cache struct {
	mu         sync.RWMutex
	issues     map[string]*CachedIssue
	issuesList []string
	ttl        time.Duration
}

// CachedIssue represents a cached issue with expiration
type CachedIssue struct {
	Issue     *linear.Issue
	ExpiresAt time.Time
}

// New creates a new cache with the specified TTL
func New(ttl time.Duration) *Cache {
	return &Cache{
		issues:     make(map[string]*CachedIssue),
		issuesList: make([]string, 0),
		ttl:        ttl,
	}
}

// Get retrieves an issue from the cache
func (c *Cache) Get(id string) (*linear.Issue, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	cached, ok := c.issues[id]
	if !ok {
		return nil, false
	}

	if time.Now().After(cached.ExpiresAt) {
		return nil, false
	}

	return cached.Issue, true
}

// Set stores an issue in the cache
func (c *Cache) Set(id string, issue *linear.Issue) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.issues[id] = &CachedIssue{
		Issue:     issue,
		ExpiresAt: time.Now().Add(c.ttl),
	}
}

// SetList stores a list of issues in the cache
func (c *Cache) SetList(issues []linear.Issue) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.issuesList = make([]string, 0, len(issues))
	for _, issue := range issues {
		c.issues[issue.ID] = &CachedIssue{
			Issue:     &issue,
			ExpiresAt: time.Now().Add(c.ttl),
		}
		c.issuesList = append(c.issuesList, issue.ID)
	}
}

// GetList retrieves the list of cached issue IDs
func (c *Cache) GetList() ([]string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if len(c.issuesList) == 0 {
		return nil, false
	}

	// Check if any issue in the list has expired
	for _, id := range c.issuesList {
		cached, ok := c.issues[id]
		if !ok || time.Now().After(cached.ExpiresAt) {
			return nil, false
		}
	}

	return c.issuesList, true
}

// Delete removes an issue from the cache
func (c *Cache) Delete(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.issues, id)

	// Remove from list
	for i, issueID := range c.issuesList {
		if issueID == id {
			c.issuesList = append(c.issuesList[:i], c.issuesList[i+1:]...)
			break
		}
	}
}

// Clear removes all items from the cache
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.issues = make(map[string]*CachedIssue)
	c.issuesList = make([]string, 0)
}
