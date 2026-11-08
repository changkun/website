// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"net/http"
	"strings"

	"changkun.de/x/website/internal/blog"
	"changkun.de/x/website/internal/library"
)

// edgeControl is how long Cloudflare keeps a public answer at its edge,
// near each reader. For a minute it is fresh. After that Cloudflare still
// answers from the edge at once, and asks the server for the new answer in
// the background, so a change shows from the next reader on; it also
// answers from the edge while the server cannot answer at all. A server in
// Germany is a few hundred milliseconds from a reader in America, and the
// edge is a few.
//
// It goes in Cloudflare-CDN-Cache-Control, which Cloudflare reads in place of
// Cache-Control and does not pass on, so browsers keep revalidating each page
// as before. s-maxage is not used: it would stop Cloudflare from answering
// with a stale page while it revalidates.
const edgeControl = "max-age=60, stale-while-revalidate=604800, stale-if-error=604800"

// edgeCache marks the public answers of next for Cloudflare to keep at its
// edge. A Cache Rule on the zone makes pages eligible and leaves an answer
// without the mark to the server, so what is not marked is not kept: what
// is set apart for one person (private, no-store), sets a cookie, is not a
// whole 200 or 304, or is under the library or the blog's editor, where
// what one is shown depends on who asks and on shares that can be revoked.
// Files the server names for good (immutable) keep their own Cache-Control.
func edgeCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) || personal(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(&edgeWriter{ResponseWriter: w}, r)
	})
}

// personal reports whether the answer at path depends on who asks.
func personal(path string) bool {
	if strings.HasPrefix(path, library.Prefix) {
		return !strings.HasPrefix(path, library.Prefix+"assets/")
	}
	return strings.HasPrefix(path, blog.Prefix+"new") || strings.HasPrefix(path, blog.Prefix+"edit")
}

// edgeWriter adds the mark to an answer that may have it, as its header is
// written.
type edgeWriter struct {
	http.ResponseWriter
	wrote bool
}

func (e *edgeWriter) WriteHeader(code int) {
	if !e.wrote {
		e.wrote = true
		if h := e.Header(); keepable(code, h) {
			h.Set("Cloudflare-CDN-Cache-Control", edgeControl)
			// A page without its own Cache-Control would be given
			// Cloudflare's browser TTL of hours once it is kept;
			// browsers ask again each time, as they do for the blog.
			if h.Get("Cache-Control") == "" && strings.HasPrefix(h.Get("Content-Type"), "text/html") {
				h.Set("Cache-Control", "no-cache")
			}
		}
	}
	e.ResponseWriter.WriteHeader(code)
}

func (e *edgeWriter) Write(b []byte) (int, error) {
	if !e.wrote {
		e.WriteHeader(http.StatusOK)
	}
	return e.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the connection, as the file
// server does to lift the write deadline for large files.
func (e *edgeWriter) Unwrap() http.ResponseWriter { return e.ResponseWriter }

// keepable reports whether an answer with code and h may be kept at the
// edge for everyone.
func keepable(code int, h http.Header) bool {
	if code != http.StatusOK && code != http.StatusNotModified {
		return false
	}
	if h.Get("Set-Cookie") != "" {
		return false
	}
	cc := strings.ToLower(h.Get("Cache-Control"))
	return !strings.Contains(cc, "private") && !strings.Contains(cc, "no-store") && !strings.Contains(cc, "immutable")
}
