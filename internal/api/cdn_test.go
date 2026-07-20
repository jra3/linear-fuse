package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCDNClientGet proves the GET path: auth header applied, 200 bytes returned,
// non-200 surfaced as an error.
func TestCDNClientGet(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var gotAuth, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotMethod = r.Method
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	c := NewCDNClient(func() string { return "Bearer test" })
	c.SetHTTPClient(srv.Client())

	body, err := c.Get(ctx, srv.URL+"/f1.png")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != "PNGDATA" {
		t.Errorf("body = %q, want PNGDATA", body)
	}
	if gotAuth != "Bearer test" {
		t.Errorf("auth = %q, want Bearer test", gotAuth)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q, want GET", gotMethod)
	}

	if _, err := c.Get(ctx, srv.URL+"/missing"); err == nil {
		t.Error("Get on 404 should error")
	}
}

// TestCDNClientSize proves the HEAD path returns ContentLength on 200 and 0 on
// any failure (best-effort).
func TestCDNClientSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		if r.URL.Path == "/missing" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", "42")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewCDNClient(func() string { return "Bearer test" })
	c.SetHTTPClient(srv.Client())

	if size := c.Size(ctx, srv.URL+"/f1.png"); size != 42 {
		t.Errorf("Size = %d, want 42", size)
	}
	if gotMethod != http.MethodHead {
		t.Errorf("method = %q, want HEAD", gotMethod)
	}

	// A non-200 is swallowed to 0 — a missing size never fails a sync.
	if size := c.Size(ctx, srv.URL+"/missing"); size != 0 {
		t.Errorf("Size on 404 = %d, want 0", size)
	}
}

// TestCDNClientNilAuth confirms a nil auth seam sends no Authorization header
// rather than panicking.
func TestCDNClientNilAuth(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	hadAuth := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hadAuth = r.Header.Get("Authorization") != ""
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := NewCDNClient(nil)
	c.SetHTTPClient(srv.Client())

	if _, err := c.Get(ctx, srv.URL); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if hadAuth {
		t.Error("nil auth should send no Authorization header")
	}
}

// TestCDNClientSizeCap proves the GET body cap (#335): a body over maxCDNBytes
// errors and returns NO partial bytes; a body at the limit still succeeds.
func TestCDNClientSizeCap(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Serve a body one byte larger than the cap. Stream it so the server never
	// has to hold 100 MiB+1 in memory at once.
	oversized := int64(maxCDNBytes) + 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/atlimit" {
			// Exactly maxCDNBytes: must succeed.
			w.Header().Set("Content-Length", fmt.Sprintf("%d", maxCDNBytes))
			w.WriteHeader(http.StatusOK)
			streamZeros(w, maxCDNBytes)
			return
		}
		// Oversized: must be refused.
		w.WriteHeader(http.StatusOK)
		streamZeros(w, oversized)
	}))
	defer srv.Close()

	c := NewCDNClient(func() string { return "Bearer test" })
	c.SetHTTPClient(srv.Client())

	body, err := c.Get(ctx, srv.URL+"/big")
	if err == nil {
		t.Fatal("Get on oversized body should error")
	}
	if body != nil {
		t.Errorf("Get on oversized body returned %d partial bytes, want nil", len(body))
	}

	// A body exactly at the cap must still be returned in full.
	body, err = c.Get(ctx, srv.URL+"/atlimit")
	if err != nil {
		t.Fatalf("Get at-limit body: %v", err)
	}
	if int64(len(body)) != int64(maxCDNBytes) {
		t.Errorf("at-limit body = %d bytes, want %d", len(body), maxCDNBytes)
	}
}

// TestCDNClientRefusesRedirect proves the redirect policy (#336 SSRF, #337
// key-downgrade): the CDN client follows NO redirect, for GET and HEAD alike, at
// every redirect status. Refusal is target-agnostic, so we deliberately do NOT
// enumerate internal SSRF hosts — the guarantee is that the second hop is never
// made, so the Authorization key cannot ride onto ANY target (internal, external,
// or a cleartext downgrade). Each case points the redirect at a REACHABLE
// recording sink: a regression that starts following redirects is then observed
// instantly as sinkReached, rather than passing the err!=nil check for the wrong
// reason (a slow dial failure to a dead address, which the earlier
// internal-address cases silently did).
func TestCDNClientRefusesRedirect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// A reachable sink that records whether it was ever reached / saw the key.
	var sinkReached, sinkSawAuth bool
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sinkReached = true
		if r.Header.Get("Authorization") != "" {
			sinkSawAuth = true
		}
		_, _ = w.Write([]byte("SECRET"))
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

		c := NewCDNClient(func() string { return "Bearer test" })
		c.SetHTTPClient(redir.Client())

		t.Run(fmt.Sprintf("get-%d", code), func(t *testing.T) {
			sinkReached, sinkSawAuth = false, false
			body, err := c.Get(ctx, redir.URL+"/f.png")
			if err == nil {
				t.Fatal("Get should refuse the redirect")
			}
			if !strings.Contains(err.Error(), "refusing redirect") {
				t.Errorf("error = %q, want it to name the redirect refusal", err)
			}
			if body != nil {
				t.Errorf("refused redirect returned %d bytes, want nil", len(body))
			}
			if sinkReached {
				t.Error("redirect target was followed — should have been refused")
			}
			if sinkSawAuth {
				t.Error("Authorization key leaked to redirect target")
			}
		})

		t.Run(fmt.Sprintf("head-%d", code), func(t *testing.T) {
			sinkReached, sinkSawAuth = false, false
			// Size swallows transport errors to 0; the security guarantee under
			// test is that the sink is never reached (so the key never rides the
			// HEAD redirect either).
			if sz := c.Size(ctx, redir.URL+"/f.png"); sz != 0 {
				t.Errorf("Size on refused redirect = %d, want 0", sz)
			}
			if sinkReached {
				t.Error("HEAD redirect target was followed — should have been refused")
			}
		})

		redir.Close()
	}
}

// TestCDNClientLegitimateGetStillWorks confirms the hardening did not break the
// happy path: a direct https-style 200 returns its bytes with the key applied.
func TestCDNClientLegitimateGetStillWorks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	c := NewCDNClient(func() string { return "Bearer test" })
	c.SetHTTPClient(srv.Client())

	body, err := c.Get(ctx, srv.URL+"/ok.png")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != "PNGDATA" {
		t.Errorf("body = %q, want PNGDATA", body)
	}
	if gotAuth != "Bearer test" {
		t.Errorf("auth = %q, want Bearer test", gotAuth)
	}
}

// streamZeros writes n zero bytes to w in chunks without allocating the whole
// buffer, keeping the oversized-body test cheap.
func streamZeros(w http.ResponseWriter, n int64) {
	const chunk = 1 << 20
	buf := bytes.Repeat([]byte{0}, chunk)
	for n > 0 {
		sz := int64(len(buf))
		if n < sz {
			sz = n
		}
		if _, err := w.Write(buf[:sz]); err != nil {
			return
		}
		n -= sz
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
