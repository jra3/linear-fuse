// Package testutil provides test utilities including a mock Linear API server.
package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
)

// MockLinearServer is a test server that simulates the Linear GraphQL API.
type MockLinearServer struct {
	Server *httptest.Server

	mu        sync.RWMutex
	responses map[string]any   // query/mutation name -> response data
	errors    map[string]error // query/mutation name -> error to return
	calls     []GraphQLCall    // recorded calls for assertions
}

// GraphQLCall records a GraphQL request for test assertions.
type GraphQLCall struct {
	Query     string
	Variables map[string]any
	Operation string // extracted operation name
}

// NewMockLinearServer creates a new mock server ready for use.
func NewMockLinearServer() *MockLinearServer {
	m := &MockLinearServer{
		responses: make(map[string]any),
		errors:    make(map[string]error),
	}

	m.Server = httptest.NewServer(http.HandlerFunc(m.handleRequest))
	return m
}

// URL returns the test server's URL for use with the API client.
func (m *MockLinearServer) URL() string {
	return m.Server.URL
}

// Close shuts down the test server.
func (m *MockLinearServer) Close() {
	m.Server.Close()
}

// SetResponse configures the mock to return data for a specific operation.
// The operation name is matched against the GraphQL query/mutation name.
func (m *MockLinearServer) SetResponse(operation string, data any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses[operation] = data
}

// SetError configures the mock to return an error for a specific operation.
func (m *MockLinearServer) SetError(operation string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors[operation] = err
}

// Calls returns all recorded GraphQL calls for assertions.
func (m *MockLinearServer) Calls() []GraphQLCall {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return append([]GraphQLCall{}, m.calls...)
}

// LastCall returns the most recent GraphQL call, or nil if none.
func (m *MockLinearServer) LastCall() *GraphQLCall {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.calls) == 0 {
		return nil
	}
	return &m.calls[len(m.calls)-1]
}

// Reset clears all responses, errors, and recorded calls.
func (m *MockLinearServer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = make(map[string]any)
	m.errors = make(map[string]error)
	m.calls = nil
}

// operationRegex matches GraphQL operation names like "query GetTeams" or "mutation UpdateIssue"
var operationRegex = regexp.MustCompile(`(?:query|mutation)\s+(\w+)`)

func (m *MockLinearServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Extract operation name
	operation := extractOperation(req.Query)

	// Record the call
	m.mu.Lock()
	m.calls = append(m.calls, GraphQLCall{
		Query:     req.Query,
		Variables: req.Variables,
		Operation: operation,
	})
	m.mu.Unlock()

	// Check for configured error
	m.mu.RLock()
	if err, ok := m.errors[operation]; ok {
		m.mu.RUnlock()
		resp := map[string]any{
			"errors": []map[string]any{
				{"message": err.Error()},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
		return
	}

	// Check for configured response
	data, ok := m.responses[operation]
	m.mu.RUnlock()

	if !ok {
		// Return empty data if no response configured
		data = map[string]any{}
	}

	resp := map[string]any{
		"data": data,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// extractOperation extracts the operation name from a GraphQL query.
func extractOperation(query string) string {
	matches := operationRegex.FindStringSubmatch(query)
	if len(matches) > 1 {
		return matches[1]
	}
	return ""
}
