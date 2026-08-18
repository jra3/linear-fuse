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
	"io"
	"log"
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
type requestLogError struct {
	Message   string `json:"message"`
	Code      string `json:"code"`
	Type      string `json:"type"`
	UserError bool   `json:"user_error"`
}

// newRequestLogError projects a completed request's error onto its log object.
// A *GraphQLError (through any wrapping) contributes its decoded extensions; any
// other failure — HTTP-level, transport, a budget deferral — has no extensions
// to decode, so it carries its rendered string and empty extension fields.
func newRequestLogError(err error) *requestLogError {
	if err == nil {
		return nil
	}
	var gqlErr *GraphQLError
	if errors.As(err, &gqlErr) {
		return &requestLogError{
			Message:   gqlErr.Message,
			Code:      gqlErr.Code,
			Type:      gqlErr.Type,
			UserError: gqlErr.UserError,
		}
	}
	return &requestLogError{Message: err.Error()}
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
