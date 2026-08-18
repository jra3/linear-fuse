package api

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsRateLimited(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"typed GraphQLError with RATELIMITED code",
			&GraphQLError{Message: "you shall not pass", Code: "RATELIMITED"},
			true,
		},
		{
			"typed error wrapped via %w",
			fmt.Errorf("query TeamIssues failed: %w", &GraphQLError{Message: "x", Code: "RATELIMITED"}),
			true,
		},
		{
			"plain string carrying RATELIMITED (HTTP 400 envelope)",
			errors.New(`API error (status 400): {"errors":[{"extensions":{"code":"RATELIMITED"}}]}`),
			true,
		},
		{
			"plain string, case-insensitive rate limit phrasing",
			errors.New("Rate limit exceeded"),
			true,
		},
		{
			// A local budget deferral (typed ErrDeferred) is NOT a server rate
			// limit — the whole point of #257. The typed exclusion must win even
			// when the message literally says "rate limit" (the historical
			// phrasing that caused the misclassification).
			"client-side budget deferral is not a server rate limit",
			fmt.Errorf("rate limit: query GetIssue deferred (reserve): %w", ErrDeferred),
			false,
		},
		{
			"pagination-preflight ErrBudget is not a server rate limit",
			fmt.Errorf("paginate: %w", ErrBudget), // ErrBudget's own message contains "rate-limit"
			false,
		},
		{
			"circuit breaker is NOT rate limiting",
			errors.New("circuit breaker open: skipping GetIssue (connectivity down)"),
			false,
		},
		{
			"typed GraphQLError with unrelated code",
			&GraphQLError{Message: "labelIds contain parent labels", Code: "INPUT_ERROR", UserError: true},
			false,
		},
		{
			// The reciprocal of TestIsUsageLimited's rate-limit rows. A plan wall
			// must never read as a request budget: client.go hands a rate limit to
			// the admission ladder, which zeroes both budget axes for up to 15m —
			// a punishing response to a condition waiting cannot clear.
			"usage limit is not a rate limit",
			&GraphQLError{Message: "usage limit exceeded"},
			false,
		},
		{"unrelated error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRateLimited(tc.err); got != tc.want {
				t.Errorf("IsRateLimited(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsDeferred(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ErrDeferred sentinel", ErrDeferred, true},
		{"ErrDeferred wrapped via %w", fmt.Errorf("query X deferred (reserve): %w", ErrDeferred), true},
		{"pagination ErrBudget", ErrBudget, true},
		{"ErrBudget wrapped via %w", fmt.Errorf("paginate: %w", ErrBudget), true},
		{"server RATELIMITED is not a defer", &GraphQLError{Code: "RATELIMITED"}, false},
		{"plain rate-limit string is not a defer", errors.New("Rate limit exceeded"), false},
		{"unrelated error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDeferred(tc.err); got != tc.want {
				t.Errorf("IsDeferred(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			"typed GraphQLError with Entity not found message",
			&GraphQLError{Message: "Entity not found: Issue - Could not find referenced Issue."},
			true,
		},
		{
			"typed error wrapped via %w",
			fmt.Errorf("refresh failed: %w", &GraphQLError{Message: "Entity not found: Project"}),
			true,
		},
		{
			"plain string carrying the envelope (HTTP 400)",
			errors.New(`API error (status 400): {"errors":[{"message":"Entity not found: Comment - Could not find referenced Comment."}]}`),
			true,
		},
		{
			// Case is not the contract: siblings lowercase before matching, and a
			// rejection this predicate misses degrades a create/edit to the EIO
			// that reads as retryable (#445).
			"lowercased message",
			&GraphQLError{Message: "entity not found: Issue"},
			true,
		},
		{
			// The phrasing can ride in UserPresentableMessage alone, exactly as
			// IsFieldTooLong's cap phrasing can.
			"phrasing only in UserPresentableMessage",
			&GraphQLError{Message: "internal", UserPresentableMessage: "Entity not found: Issue"},
			true,
		},
		{
			"typed GraphQLError with unrelated message",
			&GraphQLError{Message: "something else went wrong"},
			false,
		},
		{
			// The IsUsageLimited echo hazard, same shape: Linear renders
			// user-supplied entity names into UserPresentableMessage, so a
			// workspace that owns a label named "Entity not found" must still get
			// the EINVAL its fixable input rejection earns — not a gone verdict
			// saying retrying will not help.
			"echoed entity name inside a validation sentence",
			&GraphQLError{
				Message:                "Argument Validation Error",
				UserPresentableMessage: "The label 'Entity not found' is a group and cannot be assigned to projects directly.",
				UserError:              true,
			},
			false,
		},
		{
			// The same echo arriving as the plain-string envelope: the whole-
			// message rule applies to the quoted value, not to the body carrying it.
			"echoed entity name inside the envelope (HTTP 400)",
			errors.New(`API error (status 400): {"errors":[{"message":"The label 'Entity not found' is a group and cannot be assigned to projects directly."}]}`),
			false,
		},
		{
			// Our own *FieldError rendering quotes the caller's frontmatter value
			// verbatim. A caller who writes `status: Entity not found` must not
			// get to pick the errno for their own typo.
			"the phrase quoted as a caller-supplied field value",
			errors.New("Field: status\nValue: \"Entity not found\"\nError: unknown state. See states.md"),
			false,
		},
		{
			// A throttled request is not proof the entity is gone, and the two
			// verdicts are opposite: waiting fixes one and never fixes the other.
			// The delete gate asks this predicate directly, so a false hit here
			// forgets the local row for an entity Linear never deleted.
			"rate limit wins even when the envelope also names a missing entity",
			errors.New(`API error (status 429): {"errors":[{"message":"RATELIMITED"},{"message":"Entity not found"}]}`),
			false,
		},
		{"rate limit is not not-found", errors.New("Rate limit exceeded"), false},
		{"unrelated error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Errorf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsFieldTooLong(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"typed GraphQLError, shorter-than-or-equal phrasing",
			&GraphQLError{Message: "description must be shorter than or equal to 255 characters."},
			true,
		},
		{
			"phrasing only in UserPresentableMessage",
			&GraphQLError{Message: "Argument Validation Error", UserPresentableMessage: "name must be at most 80 characters"},
			true,
		},
		{
			"typed error wrapped via %w",
			fmt.Errorf("update failed: %w", &GraphQLError{Message: "title must be shorter than or equal to 255 characters"}),
			true,
		},
		{
			"plain string carrying the envelope (HTTP 400)",
			errors.New(`API error (status 400): {"errors":[{"message":"description must be shorter than or equal to 255 characters."}]}`),
			true,
		},
		{"unrelated userError", &GraphQLError{Message: "labelIds contain parent labels"}, false},
		{"not-found is not too-long", errors.New("Entity not found: Project"), false},
		{"nil error", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsFieldTooLong(tc.err); got != tc.want {
				t.Errorf("IsFieldTooLong(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsUsageLimited(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{
			// The one recorded instance (#409, the first live write dispatch):
			// Linear sent the bare phrase, and whether it set userError is unknown.
			// The predicate must not care.
			"typed GraphQLError, userError unset",
			&GraphQLError{Message: "usage limit exceeded"},
			true,
		},
		{
			"same rejection tagged userError",
			&GraphQLError{Message: "usage limit exceeded", UserError: true},
			true,
		},
		{
			"phrasing only in UserPresentableMessage",
			&GraphQLError{Message: "Argument Validation Error", UserPresentableMessage: "Workspace usage limit reached"},
			true,
		},
		{
			"typed error wrapped via %w",
			fmt.Errorf("mutation IssueCreate failed: %w", &GraphQLError{Message: "usage limit exceeded"}),
			true,
		},
		{
			"plain string carrying the envelope (HTTP 400)",
			errors.New(`API error (status 400): {"errors":[{"message":"usage limit exceeded"}]}`),
			true,
		},
		{
			// Disjointness with IsRateLimited, both directions. A request budget
			// clears by waiting; a plan wall does not, and classifyMutationErr
			// tells the caller opposite things about retrying.
			"server rate limit is not a usage limit",
			&GraphQLError{Message: "you shall not pass", Code: "RATELIMITED"},
			false,
		},
		{"rate-limit phrasing is not a usage limit", errors.New("Rate limit exceeded"), false},
		{"local budget deferral is not a usage limit", fmt.Errorf("query X deferred: %w", ErrDeferred), false},
		{"not-found is not a usage limit", errors.New("Entity not found: Issue"), false},
		{
			// A false positive is strictly worse than a false negative here, and
			// Linear echoes user-supplied entity names into UserPresentableMessage.
			// A workspace that owns a label named "Usage limits" must still get the
			// EINVAL its fixable input rejection earns — not a quota verdict saying
			// retrying will not help.
			"echoed entity name inside a validation sentence",
			&GraphQLError{
				Message:                "Argument Validation Error",
				UserPresentableMessage: "The label 'Usage limits' is a group and cannot be assigned to projects directly.",
				UserError:              true,
			},
			false,
		},
		{
			// The same echo arriving as the plain-string envelope, where the phrase
			// legitimately sits inside a JSON body: the whole-message rule applies
			// to the quoted value, not to the envelope that carries it.
			"echoed entity name inside the envelope (HTTP 400)",
			errors.New(`API error (status 400): {"errors":[{"message":"The label 'Usage limits' is a group and cannot be assigned to projects directly."}]}`),
			false,
		},
		{"unrelated error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUsageLimited(tc.err); got != tc.want {
				t.Errorf("IsUsageLimited(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
