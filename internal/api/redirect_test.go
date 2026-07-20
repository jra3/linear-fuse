package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestClientRefusesRedirect proves the GraphQL client's redirect policy (#353,
// the API-client twin of the CDN's #336/#337 hardening): the client follows NO
// redirect, at every redirect status. The endpoint is a pinned https constant,
// so a 3xx can only come from the real Linear server misbehaving or a forged
// TLS peer — either way, following it would replay the raw lin_api_ key in the
// Authorization header onto the redirect target. As in the CDN test, each case
// points the redirect at a REACHABLE recording sink so a regression that starts
// following redirects is observed as sinkReached, not masked by a dial failure.
func TestClientRefusesRedirect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A reachable sink that records whether it was ever reached / saw the key.
	var sinkReached, sinkSawAuth bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sinkReached = true
		if r.Header.Get("Authorization") != "" {
			sinkSawAuth = true
		}
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer sink.Close()

	// Every redirect status must be refused — Go treats 301/302/303/307/308 all
	// as follow-worthy by default, so each must be independently blocked.
	for _, code := range []int{
		http.StatusMovedPermanently,  // 301
		http.StatusFound,             // 302
		http.StatusSeeOther,          // 303
		http.StatusTemporaryRedirect, // 307
		http.StatusPermanentRedirect, // 308
	} {
		code := code
		redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", sink.URL+"/leak")
			w.WriteHeader(code)
		}))

		t.Run(fmt.Sprintf("query-%d", code), func(t *testing.T) {
			sinkReached, sinkSawAuth = false, false
			c := NewClient("lin_api_test")
			c.SetAPIURL(redir.URL)

			var out struct{}
			err := c.query(ctx, `query Probe { viewer { id } }`, nil, &out)
			if err == nil {
				t.Fatal("query should refuse the redirect")
			}
			if !strings.Contains(err.Error(), "refusing redirect") {
				t.Errorf("error = %q, want it to name the redirect refusal", err)
			}
			if sinkReached {
				t.Error("redirect target was followed — should have been refused")
			}
			if sinkSawAuth {
				t.Error("Authorization key leaked to redirect target")
			}
		})
		redir.Close()
	}
}
