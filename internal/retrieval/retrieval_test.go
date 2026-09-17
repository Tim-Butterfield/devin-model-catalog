package retrieval

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPGet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if !strings.HasPrefix(r.UserAgent(), "devmodels-test") {
				http.Error(w, "no user agent", http.StatusBadRequest)
				return
			}
			w.Write([]byte("hello"))
		case "/short":
			w.Header().Set("Content-Length", "100")
			w.Write([]byte("only ten b"))
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
			}
		case "/big":
			w.Write([]byte(strings.Repeat("x", 64)))
		case "/empty":
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	h := NewHTTP("devmodels-test/1")
	h.Client = srv.Client()
	ctx := context.Background()

	if body, err := h.Get(ctx, srv.URL+"/ok"); err != nil || string(body) != "hello" {
		t.Fatalf("ok: %q %v", body, err)
	}
	if _, err := h.Get(ctx, srv.URL+"/short"); err == nil || !errors.Is(err, ErrTruncated) {
		t.Fatalf("short read must be ErrTruncated, got %v", err)
	}
	h.MaxBytes = 16
	if _, err := h.Get(ctx, srv.URL+"/big"); !errors.Is(err, ErrTruncated) {
		t.Fatalf("oversized body must be ErrTruncated, got %v", err)
	}
	h.MaxBytes = 0
	if _, err := h.Get(ctx, srv.URL+"/missing"); err == nil {
		t.Fatal("non-200 must fail")
	}
	if _, err := h.Get(ctx, srv.URL+"/empty"); err == nil {
		t.Fatal("empty body must fail")
	}
}
