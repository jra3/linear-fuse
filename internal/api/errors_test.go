package api

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsRateLimited(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"rate limited", &RateLimitedError{Msg: "rate limited"}, true},
		{"wrapped rate limited", fmt.Errorf("context: %w", &RateLimitedError{Msg: "rl"}), true},
		{"transient (circuit breaker)", &transientError{msg: "circuit breaker open"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsRateLimited(c.err); got != c.want {
				t.Errorf("IsRateLimited(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestIsRetryable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"rate limited", &RateLimitedError{Msg: "rl"}, true},
		{"transient (circuit breaker)", &transientError{msg: "circuit breaker open"}, true},
		{"wrapped transient", fmt.Errorf("ctx: %w", &transientError{msg: "cb"}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsRetryable(c.err); got != c.want {
				t.Errorf("IsRetryable(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}
