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
			"typed GraphQLError with unrelated message",
			&GraphQLError{Message: "something else went wrong"},
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
