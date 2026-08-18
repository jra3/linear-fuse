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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
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
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
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
		fmt.Fprintf(w, `{"data": {"teams": {"pageInfo": {"hasNextPage": false, "endCursor": ""}, "nodes": []}}}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)

	if _, err := client.GetTeams(context.Background()); err != nil {
		t.Fatalf("GetTeams failed with nil request log: %v", err)
	}
}

// TestNewRequestLogError pins the projection from a completed request's error
// onto its log object — the pure half of the request log's #448 fix.
func TestNewRequestLogError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want *requestLogError
	}{
		{"no error", nil, nil},
		{
			// The #409 shape, wrapped the way a mutation site wraps it. errors.As
			// sees through the wrap, so the extensions still land in the line.
			name: "wrapped graphql rejection keeps its extensions",
			err:  fmt.Errorf("mutation IssueCreate failed: %w", &GraphQLError{Message: "usage limit exceeded"}),
			want: &requestLogError{Message: "usage limit exceeded"},
		},
		{
			name: "tagged rejection",
			err:  &GraphQLError{Message: "Argument Validation Error", Code: "INPUT_ERROR", Type: "invalid input", UserError: true},
			want: &requestLogError{Message: "Argument Validation Error", Code: "INPUT_ERROR", Type: "invalid input", UserError: true},
		},
		{
			// The shape the predicates actually key on: Linear rejects with a
			// generic Message and puts the cap sentence only in the presentable
			// field (IsFieldTooLong reads both; IsUsageLimited likewise). Drop this
			// field from the line and a census greps the artifact for the wording
			// that made the run fail and finds nothing.
			name: "cap phrasing rides in userPresentableMessage",
			err: &GraphQLError{
				Message:                "Argument Validation Error",
				UserPresentableMessage: "name must be at most 80 characters",
			},
			want: &requestLogError{
				Message:                "Argument Validation Error",
				UserPresentableMessage: "name must be at most 80 characters",
			},
		},
		{
			// Only the first rejection is decoded, so the tally rides along on
			// the line: without it the artifact cannot say whether the recorded
			// rejection was the whole of what Linear sent.
			name: "the response's error count rides on the line",
			err:  &GraphQLError{Message: "Argument Validation Error", ErrorCount: 3},
			want: &requestLogError{Message: "Argument Validation Error", Errors: 3},
		},
		{
			// No extensions to decode: an HTTP-level failure carries Linear's
			// envelope verbatim in its rendered string. There is no errors array
			// at all, so the count is 0.
			name: "http failure carries its rendered string",
			err:  errors.New(`API error (status 400): {"errors":[{"message":"boom"}]}`),
			want: &requestLogError{Message: `API error (status 400): {"errors":[{"message":"boom"}]}`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newRequestLogError(tc.err)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("newRequestLogError(nil) = %+v, want nil", got)
				}
				return
			}
			if got == nil || *got != *tc.want {
				t.Errorf("newRequestLogError() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRequestLogRecordsRejectionExtensions is the artifact end of #448: after a
// live run, requests.jsonl must answer "what did Linear actually send?" — the
// question #409 could not answer once the run was over. An untagged rejection
// writes its absences explicitly, so a missing code is recorded, not inferred.
func TestRequestLogRecordsRejectionExtensions(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errors":[{"message":"usage limit exceeded"}]}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf)

	var result struct{}
	if err := client.query(context.Background(), `query TestOp { viewer { id } }`, nil, &result); err == nil {
		t.Fatal("query succeeded, want a GraphQL rejection")
	}

	entries := decodeRequestLog(t, &buf)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one", entries)
	}
	got := entries[0]
	if got.Outcome != "error" || got.Error == nil {
		t.Fatalf("entry = %+v, want an error entry carrying a decoded rejection", got)
	}
	if got.Error.Message != "usage limit exceeded" {
		t.Errorf("error.message = %q, want %q", got.Error.Message, "usage limit exceeded")
	}
	// The absences are the observation: an untagged rejection must be
	// distinguishable from one whose tags were simply never written down.
	raw := buf.String()
	for _, key := range []string{`"code":""`, `"type":""`, `"user_error":false`} {
		if !strings.Contains(raw, key) {
			t.Errorf("line does not record %s, so absence is indistinguishable from silence:\n%s", key, raw)
		}
	}
}

// TestRequestLogRecordsErrorCount pins the census field on the JSONL line. Only
// the FIRST of a response's errors becomes the returned Go error and the decoded
// object, so a line recording a generic untagged rejection is ambiguous on its
// own: it could be all Linear sent, or the first of several with the tagged one
// dropped. The census that asks this is the jq recipe in docs/telemetry.md,
// which reads this artifact and not journald, so the tally has to be here.
func TestRequestLogRecordsErrorCount(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errors":[{"message":"Argument Validation Error"},{"message":"second one","extensions":{"code":"INPUT_ERROR","userError":true}}]}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf)

	var result struct{}
	if err := client.query(context.Background(), `mutation IssueCreate { x }`, nil, &result); err == nil {
		t.Fatal("query succeeded, want a GraphQL rejection")
	}

	entries := decodeRequestLog(t, &buf)
	if len(entries) != 1 || entries[0].Error == nil {
		t.Fatalf("entries = %+v, want one carrying a decoded rejection", entries)
	}
	got := entries[0].Error
	if got.Errors != 2 {
		t.Errorf("error.errors = %d, want 2 — the line cannot say it is showing one of several", got.Errors)
	}
	// The recorded rejection is the first, which is also the returned error.
	if got.Message != "Argument Validation Error" {
		t.Errorf("error.message = %q, want the first rejection", got.Message)
	}
	if got.Code != "" {
		t.Errorf("error.code = %q, want the first rejection's (empty) code, not the second's", got.Code)
	}
}

// TestRequestLogErrorCountWrittenWhenAbsent: the count is written even when
// there was no errors array — a non-GraphQL failure records 0 rather than
// dropping the key, because a key that vanishes reintroduces exactly the
// absence-vs-silence defect its four siblings are written unconditionally to
// avoid.
func TestRequestLogErrorCountWrittenWhenAbsent(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "upstream is unwell")
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf)

	var result struct{}
	if err := client.query(context.Background(), `query TestOp { viewer { id } }`, nil, &result); err == nil {
		t.Fatal("query succeeded, want an HTTP failure")
	}

	if raw := buf.String(); !strings.Contains(raw, `"errors":0`) {
		t.Errorf("line omits the error count for a non-GraphQL failure:\n%s", raw)
	}
}

// TestRequestLogRecordsUserPresentableMessage is the artifact end of the
// predicates' actual input. Linear rejects a create with the generic message
// "Argument Validation Error" and puts the cap or quota sentence only in
// extensions.userPresentableMessage — the shape IsFieldTooLong and
// IsUsageLimited both read. Runtime classification is right either way, but a
// line recording only `"message":"Argument Validation Error"` makes the run's
// artifact answer "no such rejection" to the grep that goes looking for it.
func TestRequestLogRecordsUserPresentableMessage(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"errors":[{"message":"Argument Validation Error","extensions":{"userPresentableMessage":"name must be at most 80 characters"}}]}`)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf)

	var result struct{}
	err := client.query(context.Background(), `mutation IssueCreate { x }`, nil, &result)
	// The classifier sees it; the artifact must too.
	if !IsFieldTooLong(err) {
		t.Fatalf("IsFieldTooLong(%v) = false, want true — fixture no longer models the case", err)
	}

	entries := decodeRequestLog(t, &buf)
	if len(entries) != 1 || entries[0].Error == nil {
		t.Fatalf("entries = %+v, want one carrying a decoded rejection", entries)
	}
	if got := entries[0].Error.UserPresentableMessage; got != "name must be at most 80 characters" {
		t.Errorf("error.user_presentable_message = %q, want the cap sentence", got)
	}
	// The grep an investigator actually runs over the artifact.
	if !strings.Contains(buf.String(), "must be at most") {
		t.Errorf("the wording that failed the run is not greppable in the line:\n%s", buf.String())
	}
	// Empty is still written: absence must be recorded, not implied.
	if !strings.Contains(buf.String(), `"user_presentable_message":`) {
		t.Errorf("line omits the field entirely:\n%s", buf.String())
	}
}

// TestRequestLogTruncatesOversizedRemoteText pins the size bound on the line.
//
// A non-GraphQL failure carries `API error (status N): <entire response body>`,
// built from an unbounded io.ReadAll — so a proxy or WAF answering with a
// multi-MB HTML error page would otherwise put that whole page on ONE JSONL
// line, roughly doubled by JSON escaping. The rotating writer lets an oversize
// write land whole in a fresh file, so the 100 MB cap does not contain it and
// the line-oriented analysis pipeline is the thing that breaks.
func TestRequestLogTruncatesOversizedRemoteText(t *testing.T) {
	t.Parallel()

	huge := strings.Repeat("<html>error</html>", 200_000) // ~3.6 MB
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, huge)
	}))
	defer server.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(server.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf)

	var result struct{}
	if err := client.query(context.Background(), `query TestOp { viewer { id } }`, nil, &result); err == nil {
		t.Fatal("query succeeded, want an HTTP failure")
	}

	// Generous headroom over the 2 KB cap; the point is bounded, not exact.
	if buf.Len() > 8*1024 {
		t.Errorf("request log line is %d bytes for a %d byte body — the cap is not holding", buf.Len(), len(huge))
	}
	entries := decodeRequestLog(t, &buf)
	if len(entries) != 1 || entries[0].Error == nil {
		t.Fatalf("entries = %+v, want one carrying an error", entries)
	}
	msg := entries[0].Error.Message
	// The cut is marked, so a reader never mistakes it for all Linear sent.
	if !strings.Contains(msg, "truncated") {
		t.Errorf("truncated message carries no truncation marker: %q", msg[:min(200, len(msg))])
	}
	// The head survives — the status code is the diagnostic value.
	if !strings.HasPrefix(msg, "API error (status 502)") {
		t.Errorf("message lost its head: %q", msg[:min(200, len(msg))])
	}
}

// TestTruncateLogMessage pins the boundary behavior of the cap itself: a short
// string passes through untouched, and a cut that lands mid-rune must not
// smuggle an invalid UTF-8 byte into the JSON encoder.
func TestTruncateLogMessage(t *testing.T) {
	t.Parallel()

	if got := truncateLogMessage("short"); got != "short" {
		t.Errorf("truncateLogMessage(%q) = %q, want it unchanged", "short", got)
	}
	exact := strings.Repeat("a", maxRequestLogMessage)
	if got := truncateLogMessage(exact); got != exact {
		t.Error("a string exactly at the cap was truncated")
	}
	// Multi-byte runes straddling the cut.
	multi := strings.Repeat("é", maxRequestLogMessage)
	got := truncateLogMessage(multi)
	if !utf8.ValidString(got) {
		t.Errorf("truncation produced invalid UTF-8")
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("no truncation marker: %q", got[:min(120, len(got))])
	}
}
