package api

// Tests for the per-request JSONL debug log (requestlog.go): entries carry
// op/vars/duration/outcome, complexity appears exactly when the response
// carried X-Complexity, and the outcome classification matches
// linearfs.api.requests' enum. Rotation is the telemetry rotatingWriter's
// job (tested there); here the writer is a plain buffer.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// decodeRequestLog splits buf into parsed JSONL entries, failing the test on
// any malformed line.
func decodeRequestLog(t *testing.T, buf *bytes.Buffer) []requestLogEntry {
	t.Helper()
	var entries []requestLogEntry
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var e requestLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("request log line not valid JSON: %v\nline: %s", err, line)
		}
		entries = append(entries, e)
	}
	return entries
}

func TestRequestLogEntryFields(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Complexity", "1234")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf) // sequential queries only; no concurrency here

	var result struct{}
	err := client.query(context.Background(),
		`query TestOp($id: String!) { team(id: $id) { id } }`,
		map[string]any{"id": "team-abc"}, &result)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}

	entries := decodeRequestLog(t, &buf)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1", len(entries))
	}
	e := entries[0]
	if _, terr := time.Parse(time.RFC3339Nano, e.TS); terr != nil {
		t.Errorf("ts %q is not RFC3339Nano: %v", e.TS, terr)
	}
	if e.Op != "TestOp" {
		t.Errorf("op = %q, want TestOp", e.Op)
	}
	if got := e.Vars["id"]; got != "team-abc" {
		t.Errorf("vars.id = %v, want team-abc (full vars are the duplicate-fetch key)", got)
	}
	if e.DurationMS < 0 {
		t.Errorf("duration_ms = %v, want >= 0", e.DurationMS)
	}
	if e.Outcome != "ok" {
		t.Errorf("outcome = %q, want ok", e.Outcome)
	}
	if e.Complexity == nil || *e.Complexity != 1234 {
		t.Errorf("complexity = %v, want 1234 (the response's X-Complexity)", e.Complexity)
	}
}

// TestRequestLogComplexityOmittedWithoutHeader pins the omit-when-absent
// contract: a response with no X-Complexity produces a line with NO
// complexity key at all (not a fabricated zero).
func TestRequestLogComplexityOmittedWithoutHeader(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf)

	if _, err := client.GetTeams(context.Background()); err != nil {
		t.Fatalf("GetTeams failed: %v", err)
	}

	raw := buf.String()
	if strings.Contains(raw, "complexity") {
		t.Errorf("line carries a complexity key without an X-Complexity header:\n%s", raw)
	}
	entries := decodeRequestLog(t, &buf)
	if len(entries) != 1 || entries[0].Outcome != "ok" {
		t.Fatalf("entries = %+v, want one ok entry", entries)
	}
}

// TestRequestLogOutcomes pins the error and ratelimited classifications —
// the same enum as linearfs.api.requests (outcomeFor is shared).
func TestRequestLogOutcomes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		handler http.HandlerFunc
		outcome string
	}{
		{
			name: "graphql error",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"errors": [{"message": "boom"}]}`)
			},
			outcome: "error",
		},
		{
			name: "rate limited",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				fmt.Fprintf(w, `{"errors": [{"message": "RATELIMITED"}]}`)
			},
			outcome: "ratelimited",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(tc.handler)
			defer server.Close()

			client := NewClient("test-api-key")
			client.SetAPIURL(server.URL)
			var buf bytes.Buffer
			client.SetRequestLog(&buf)

			if _, err := client.GetTeams(context.Background()); err == nil {
				t.Fatal("GetTeams succeeded, want failure")
			}

			entries := decodeRequestLog(t, &buf)
			if len(entries) != 1 {
				t.Fatalf("got %d log entries, want 1", len(entries))
			}
			if entries[0].Outcome != tc.outcome {
				t.Errorf("outcome = %q, want %q", entries[0].Outcome, tc.outcome)
			}
		})
	}
}

// TestRequestLogDisabledByDefault: with no writer set the log site is a
// no-op branch — queries run normally and nothing is recorded anywhere.
func TestRequestLogDisabledByDefault(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data": {"teams": {"nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	if _, err := client.GetTeams(context.Background()); err != nil {
		t.Fatalf("GetTeams failed with nil request log: %v", err)
	}
}
