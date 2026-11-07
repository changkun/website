// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// mustRead returns a file of the embedded site.
func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		t.Fatalf("cannot read %s from the embedded site: %v", name, err)
	}
	return b
}

func TestFileServerStatus(t *testing.T) {
	for _, tt := range []struct {
		name   string
		target string
		code   int
		want   string // embedded file expected as the body
	}{
		{"root serves index", "/", http.StatusOK, "index.html"},
		{"unauthorized page", "/401.html", http.StatusUnauthorized, "401.html"},
		{"not found page", "/404.html", http.StatusNotFound, "404.html"},
		{"login sdk", "/login-sdk.js", http.StatusOK, "login-sdk.js"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			FileServer(fsys).ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.target, nil))

			if w.Code != tt.code {
				t.Errorf("got status %d, want %d", w.Code, tt.code)
			}
			if got, want := w.Body.Bytes(), mustRead(t, tt.want); !bytes.Equal(got, want) {
				t.Errorf("got %d bytes of body, want the %d bytes of %s", len(got), len(want), tt.want)
			}
		})
	}
}

// TestFileServerKeepsNotModified checks that a 304 is not replaced by the
// page's error status. embed.FS has no modification times, hence fstest.MapFS.
func TestFileServerKeepsNotModified(t *testing.T) {
	modTime := time.Date(2026, time.August, 30, 12, 0, 0, 0, time.UTC)
	site := fstest.MapFS{
		"401.html": &fstest.MapFile{Data: []byte("<h1>401</h1>"), ModTime: modTime},
	}

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/401.html", nil)
	r.Header.Set("If-Modified-Since", modTime.Format(http.TimeFormat))
	FileServer(site).ServeHTTP(w, r)

	if w.Code != http.StatusNotModified {
		t.Errorf("got status %d, want %d", w.Code, http.StatusNotModified)
	}
	if w.Body.Len() != 0 {
		t.Errorf("got a %d byte body, want none", w.Body.Len())
	}
}

// TestFileServerLogsNothing guards against a superfluous WriteHeader call,
// which net/http would log for every error page.
func TestFileServerLogsNothing(t *testing.T) {
	var errorLog bytes.Buffer
	s := httptest.NewUnstartedServer(FileServer(fsys))
	s.Config.ErrorLog = log.New(&errorLog, "", 0)
	s.Start()
	defer s.Close()

	for _, target := range []string{"/", "/401.html", "/404.html", "/index.html"} {
		resp, err := s.Client().Get(s.URL + target)
		if err != nil {
			t.Fatalf("GET %s: %v", target, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	if got := strings.TrimSpace(errorLog.String()); got != "" {
		t.Errorf("the server logged %q, want no output", got)
	}
}

func TestStatusOverride(t *testing.T) {
	t.Run("a write without a header uses the page status", func(t *testing.T) {
		w := httptest.NewRecorder()
		s := &statusOverride{ResponseWriter: w, code: http.StatusNotFound}
		if _, err := s.Write([]byte("gone")); err != nil {
			t.Fatalf("cannot write: %v", err)
		}
		if w.Code != http.StatusNotFound {
			t.Errorf("got status %d, want %d", w.Code, http.StatusNotFound)
		}
		if w.Body.String() != "gone" {
			t.Errorf("got body %q, want %q", w.Body.String(), "gone")
		}
	})

	t.Run("a second header is swallowed", func(t *testing.T) {
		w := httptest.NewRecorder()
		s := &statusOverride{ResponseWriter: w, code: http.StatusUnauthorized}
		s.WriteHeader(http.StatusOK)
		s.WriteHeader(http.StatusTeapot)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("got status %d, want %d", w.Code, http.StatusUnauthorized)
		}
	})
}
