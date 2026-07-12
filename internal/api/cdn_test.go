package api

import (
	"context"
	"net/http"
	"net/http/httptest"
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
