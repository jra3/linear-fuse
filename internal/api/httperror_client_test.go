package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNonOKResponseIsTypedHTTPError closes the wire half of #447. errors_test.go
// pins what the predicates do with an *HTTPError; this pins that the client
// actually MINTS one — both non-200 sites in query() used to render fmt.Errorf,
// and a regression there would leave every predicate below correct and unreached.
//
// The two sites are separate arms with separate admission bookkeeping (429 settles
// the ladder as rate-limited, everything else observes), so both are exercised.
func TestNonOKResponseIsTypedHTTPError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		body   string
		// what the typed fact must let a caller conclude
		wantAuth      bool
		wantTransient bool
		wantRateLimit bool
	}{
		{
			// The generic non-200 arm. A proxy's HTML is the realistic body here:
			// it names no phrase any message predicate matches, so before #447
			// this was indistinguishable from a backend fault.
			name:     "401 from the generic arm",
			status:   http.StatusUnauthorized,
			body:     `<html><head><title>401 Unauthorized</title></head></html>`,
			wantAuth: true,
		},
		{
			name:     "403 from the generic arm",
			status:   http.StatusForbidden,
			body:     `{"errors":[{"message":"Access denied"}]}`,
			wantAuth: true,
		},
		{
			name:          "503 from the generic arm",
			status:        http.StatusServiceUnavailable,
			body:          `<html><body>upstream connect error</body></html>`,
			wantTransient: true,
		},
		{
			// The dedicated 429 arm, which also settles the admission ladder.
			// Body names neither "RATELIMITED" nor "rate limit": the status is
			// the only signal, which is the case that used to fall through.
			name:          "bare 429 from the rate-limit arm",
			status:        http.StatusTooManyRequests,
			body:          `<html><body>Too Many Requests</body></html>`,
			wantRateLimit: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()

			client := NewClient("test-api-key")
			client.SetAPIURL(srv.URL)

			_, err := client.CreateIssue(context.Background(), map[string]any{"title": "probe"})
			if err == nil {
				t.Fatal("CreateIssue against a non-200 returned nil error")
			}

			var httpErr *HTTPError
			if !errors.As(err, &httpErr) {
				t.Fatalf("error is not an *HTTPError (the status did not survive as a typed fact): %T %v", err, err)
			}
			if httpErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", httpErr.StatusCode, tc.status)
			}
			// Body is kept RAW: IsUsageLimited parses Linear's envelope out of
			// exactly these bytes.
			if httpErr.Body != tc.body {
				t.Errorf("Body = %q, want it verbatim: %q", httpErr.Body, tc.body)
			}

			if got, ok := HTTPStatus(err); !ok || got != tc.status {
				t.Errorf("HTTPStatus() = (%d, %v), want (%d, true)", got, ok, tc.status)
			}
			if got := IsAuthFailure(err); got != tc.wantAuth {
				t.Errorf("IsAuthFailure() = %v, want %v", got, tc.wantAuth)
			}
			if got := IsServerTransient(err); got != tc.wantTransient {
				t.Errorf("IsServerTransient() = %v, want %v", got, tc.wantTransient)
			}
			if got := IsRateLimited(err); got != tc.wantRateLimit {
				t.Errorf("IsRateLimited() = %v, want %v", got, tc.wantRateLimit)
			}
		})
	}
}

// TestAuthRejectionBodyStillReachesTheRequestLog pins the SCOPE of #447's
// redaction. classifyMutationErr withholds a 401's body from .error — at that
// layer it is usually a proxy's error page, and .error is a surface an agent
// pastes around — but the operator-facing debug log is the opposite trade: it
// exists so an after-the-fact investigation can see what Linear actually sent.
// docs/THREAT-MODEL.md records the split in exactly those terms, so a change
// that quietly redacted here too would leave that doc wrong.
//
// The body still rides through *HTTPError's rendering, and still under the
// 2KB-per-string cap that keeps one remote response from owning the log file.
func TestAuthRejectionBodyStillReachesTheRequestLog(t *testing.T) {
	t.Parallel()

	body := `<html><head><title>401 Unauthorized</title></head><body>nginx/1.25</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	client := NewClient("test-api-key")
	client.SetAPIURL(srv.URL)
	var buf bytes.Buffer
	client.SetRequestLog(&buf)

	if _, err := client.CreateIssue(context.Background(), map[string]any{"title": "probe"}); err == nil {
		t.Fatal("CreateIssue against a 401 returned nil error")
	}

	entries := decodeRequestLog(t, &buf)
	if len(entries) != 1 {
		t.Fatalf("got %d log entries, want 1", len(entries))
	}
	if entries[0].Error == nil {
		t.Fatal("401 line carries no error object")
	}
	if !strings.Contains(entries[0].Error.Message, body) {
		t.Errorf("request log dropped the rejection body; message = %q", entries[0].Error.Message)
	}
	if len(entries[0].Error.Message) > maxRequestLogMessage+64 {
		t.Errorf("request log message is unbounded: %d bytes", len(entries[0].Error.Message))
	}
}
