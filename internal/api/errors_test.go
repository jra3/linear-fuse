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
			"client-side deferred rate limit message",
			errors.New("rate limit: query GetIssue deferred (reserve)"),
			true,
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
