package api

// The per-request JSONL debug log (telemetry.requests.* in config): one JSON
// line per completed GraphQL request, written where responses settle in
// Client.query — the same site that records apiMetrics and where the budget
// admission observes the response headers. This is an application debug log,
// NOT an OTEL signal (the metrics-only/traces-never policy is untouched); it
// exists for offline analysis of observation runs — see
// docs/plans/2026-07-09-coldstart-observation-plan.md.
//
// The full variables map is logged deliberately: duplicate-fetch detection
// needs to see WHICH entity/cursor was fetched twice, so grouping lines by
// (op, vars) is the analysis primitive. Complexity is the response's actual
// X-Complexity, threaded from the budget's reconcile (the one place the
// header is parsed) via admission.actualComplexity — never parsed twice.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"
)

// requestLogEntry is one requests.jsonl line.
type requestLogEntry struct {
	TS         string           `json:"ts"` // RFC3339Nano, UTC
	Op         string           `json:"op"`
	Vars       map[string]any   `json:"vars,omitempty"`
	DurationMS float64          `json:"duration_ms"`
	Outcome    string           `json:"outcome"` // ok|error|ratelimited — same classification as linearfs.api.requests
	Complexity *float64         `json:"complexity,omitempty"`
	Error      *requestLogError `json:"error,omitempty"`
}

// requestLogError is the failed request's rejection, decoded. The outcome enum
// says only ok|error|ratelimited; this says WHAT Linear sent, which is the
// question an after-the-fact investigation actually asks (#448) — and the one
// #409 could not answer, because the run's only surviving artifact was the
// message. Fields inside are written unconditionally: `"code": ""` records that
// Linear tagged the rejection with no code, which is the census observation, and
// omitting it would make absence indistinguishable from a line never written.
//
// UserPresentableMessage is here because it is where the wording a census greps
// for actually lands. Two predicates read it and not just Message —
// IsUsageLimited and IsFieldTooLong — because Linear routinely rejects a
// mutation with the generic Message "Argument Validation Error" and puts the
// quota or cap sentence only in the presentable field. Recording the other four
// fields but not this one would leave `grep "usage limit" requests.jsonl` empty
// for exactly the rejection the log exists to catch, while runtime
// classification got it right.
type requestLogError struct {
	Message                string `json:"message"`
	Code                   string `json:"code"`
	Type                   string `json:"type"`
	UserError              bool   `json:"user_error"`
	UserPresentableMessage string `json:"user_presentable_message"`
}

// maxRequestLogMessage caps each remote string on a request-log line.
//
// The uncapped case is not hypothetical: a non-GraphQL failure carries
// `API error (status %d): <entire response body>`, built from an unbounded
// io.ReadAll, so a proxy or WAF answering with a multi-MB HTML error page would
// put that whole page on one JSONL line (JSON escaping roughly doubles it).
// The rotating writer deliberately lets a single oversize write land whole in a
// fresh file, so the 100 MB cap does not save the artifact and a line-oriented
// analysis pipeline gets one line it cannot hold in memory. 2 KB keeps every
// phrasing a predicate matches on — those are sentences — while bounding the
// line.
const maxRequestLogMessage = 2048

// truncateLogMessage bounds one remote string, marking the cut explicitly so a
// reader never mistakes a truncated message for the whole of what Linear sent.
// The cut is byte-indexed and then scrubbed to valid UTF-8, since landing
// mid-rune would otherwise smuggle an invalid byte into the JSON encoder.
func truncateLogMessage(s string) string {
	if len(s) <= maxRequestLogMessage {
		return s
	}
	return strings.ToValidUTF8(s[:maxRequestLogMessage], "") +
		fmt.Sprintf("…[truncated, %d bytes total]", len(s))
}

// newRequestLogError projects a completed request's error onto its log object.
// A *GraphQLError (through any wrapping) contributes its decoded extensions; any
// other failure — HTTP-level, transport, a budget deferral — has no extensions
// to decode, so it carries its rendered string and empty extension fields.
//
// Every string here is remote text, so every one goes through
// truncateLogMessage: the HTTP fallback provably embeds a whole response body,
// and a GraphQL message is a decoded field of a body that was equally unbounded.
func newRequestLogError(err error) *requestLogError {
	if err == nil {
		return nil
	}
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) {
		return &requestLogError{
			Message:                truncateLogMessage(gqlErr.Message),
			Code:                   truncateLogMessage(gqlErr.Code),
			Type:                   truncateLogMessage(gqlErr.Type),
			UserError:              gqlErr.UserError,
			UserPresentableMessage: truncateLogMessage(gqlErr.UserPresentableMessage),
		}
	}
	return &requestLogError{Message: truncateLogMessage(err.Error())}
}

// SetRequestLog enables the per-request JSONL debug log: every completed
// request (one actually sent — budget deferrals never reach the log, exactly
// like linearfs.api.requests) appends one line to w. Set it once, before the
// client issues any requests; the field is read without synchronization. The
// writer must be safe for concurrent Write calls (telemetry.NewRequestLog's
// rotating writer is). nil (the default) disables logging — the log site
// does zero work beyond one branch.
func (c *Client) SetRequestLog(w io.Writer) {
	c.reqLog = w
}

// logRequest writes one request-log line. A debug log must never fail the
// request it describes: encode/write trouble is logged and dropped.
func (c *Client) logRequest(op string, vars map[string]any, elapsed time.Duration, err error, adm *admission) {
	if c.reqLog == nil {
		return
	}
	entry := requestLogEntry{
		TS:         time.Now().UTC().Format(time.RFC3339Nano),
		Op:         op,
		Vars:       vars,
		DurationMS: float64(elapsed.Microseconds()) / 1000.0,
		Outcome:    outcomeFor(err),
		Error:      newRequestLogError(err),
	}
	if adm != nil {
		if v, ok := adm.actualComplexity(); ok {
			entry.Complexity = &v
		}
	}
	line, jerr := json.Marshal(entry)
	if jerr != nil {
		log.Printf("[requestlog] encode failed for %s: %v", op, jerr)
		return
	}
	if _, werr := c.reqLog.Write(append(line, '\n')); werr != nil {
		log.Printf("[requestlog] write failed: %v", werr)
	}
}
