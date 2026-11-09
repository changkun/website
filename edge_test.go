// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestEdgeCache checks which answers are marked for Cloudflare to keep.
func TestEdgeCache(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		code               int
		header             map[string]string
		noWriteHeader      bool
		want               bool
		cache              string // the Cache-Control browsers get
	}{
		{name: "page", method: "GET", path: "/blog/posts/x/", code: 200, header: map[string]string{"Cache-Control": "no-cache"}, want: true, cache: "no-cache"},
		{name: "page without headers", method: "GET", path: "/bobook/", code: 200, header: map[string]string{"Content-Type": "text/html; charset=utf-8"}, want: true, cache: "no-cache"},
		{name: "file without headers", method: "GET", path: "/bobook/fig.png", code: 200, header: map[string]string{"Content-Type": "image/png"}, want: true},
		{name: "body without WriteHeader", method: "GET", path: "/", noWriteHeader: true, want: true},
		{name: "head", method: "HEAD", path: "/books/", code: 200, want: true},
		{name: "not modified", method: "GET", path: "/blog/", code: 304, want: true},
		{name: "library asset", method: "GET", path: "/library/assets/library.css", code: 200, want: true},
		{name: "post", method: "POST", path: "/blog/posts/x/", code: 200},
		{name: "not found", method: "GET", path: "/nope", code: 404},
		{name: "redirect", method: "GET", path: "/blog/p/1/", code: 301},
		{name: "partial", method: "GET", path: "/modern-cpp/pdf/x.pdf", code: 206},
		{name: "private", method: "GET", path: "/research/", code: 200, header: map[string]string{"Cache-Control": "private, no-store"}},
		{name: "no-store", method: "GET", path: "/x/", code: 200, header: map[string]string{"Cache-Control": "no-store"}},
		{name: "immutable", method: "GET", path: "/blog/assets/ink.css", code: 200, header: map[string]string{"Cache-Control": "public, max-age=31536000, immutable"}},
		{name: "cookie", method: "GET", path: "/", code: 200, header: map[string]string{"Set-Cookie": "a=b"}},
		{name: "library", method: "GET", path: "/library/", code: 200},
		{name: "library share", method: "GET", path: "/library/s/abc", code: 200},
		{name: "editor", method: "GET", path: "/blog/new", code: 200},
		{name: "editor job", method: "GET", path: "/blog/new/jobs/1", code: 200},
		{name: "edit", method: "GET", path: "/blog/edit/posts/x/", code: 200},
		{name: "login", method: "GET", path: "/login", code: 200},
		{name: "login with next", method: "GET", path: "/login?next=/blog/", code: 200},
	} {
		h := edgeCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for k, v := range tc.header {
				w.Header().Set(k, v)
			}
			if !tc.noWriteHeader {
				w.WriteHeader(tc.code)
			}
			w.Write([]byte("x"))
		}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		got := rec.Header().Get("Cloudflare-CDN-Cache-Control")
		if (got == edgeControl) != tc.want || (!tc.want && got != "") {
			t.Errorf("%s: Cloudflare-CDN-Cache-Control = %q, want marked %v", tc.name, got, tc.want)
		}
		if tc.cache != "" || tc.header["Cache-Control"] == "" {
			if got := rec.Header().Get("Cache-Control"); got != tc.cache {
				t.Errorf("%s: Cache-Control = %q, want %q", tc.name, got, tc.cache)
			}
		}
	}
}

// TestEdgeCacheController checks that the file server can still lift the
// write deadline through the wrapper.
func TestEdgeCacheController(t *testing.T) {
	var err error
	h := edgeCache(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	}))
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, gerr := http.Get(srv.URL + "/bobook/")
	if gerr != nil {
		t.Fatal(gerr)
	}
	resp.Body.Close()
	if err != nil {
		t.Errorf("SetWriteDeadline through edgeCache: %v", err)
	}
}
