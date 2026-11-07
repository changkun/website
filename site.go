// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var static embed.FS

// fsys is static/ with the prefix stripped, so its paths match URL paths.
var fsys = func() fs.FS {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	return sub
}()

// statusPages are served with the error status their content shows, so
// crawlers do not index them as real pages.
var statusPages = map[string]int{
	"/401.html": http.StatusUnauthorized,
	"/404.html": http.StatusNotFound,
}

// FileServer is http.FileServerFS(fsys), except that statusPages are served
// with their error status.
func FileServer(fsys fs.FS) http.Handler {
	files := http.FileServerFS(fsys)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code, ok := statusPages[r.URL.Path]; ok {
			w = &statusOverride{ResponseWriter: w, code: code}
		}
		files.ServeHTTP(w, r)
	})
}

// statusOverride replaces a 200 with code. Any other status, such as a
// redirect, 304, or 206, passes through.
type statusOverride struct {
	http.ResponseWriter
	code    int
	written bool
}

func (s *statusOverride) WriteHeader(code int) {
	if s.written {
		return
	}
	s.written = true
	if code == http.StatusOK {
		code = s.code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusOverride) Write(b []byte) (int, error) {
	if !s.written {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the wrapped writer.
func (s *statusOverride) Unwrap() http.ResponseWriter { return s.ResponseWriter }
