// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// wwwDir lays out a WWW_DIR as on the server: a book, a folder without an
// index, a counted folder whose talk is linked from its dated folder, and a
// folder named like a page of the embedded site.
func wwwDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"book/index.html":             "book",
		"book/ch1.html":               "chapter 1",
		"book/.travis.yml":            "hidden",
		"demos/bunny.obj":             "bunny",
		"research/index.html":         "research",
		"research/talks/202001/t.pdf": "talk",
		"books/index.html":            "a folder the site shadows",
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("202001/t.pdf", filepath.Join(dir, "research/talks/t.pdf")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// testFolders serves dir as production does, with a next that answers "site".
func testFolders(t *testing.T, dir string, report func(page, ua string)) *folders {
	t.Helper()
	counted, err := parseCounted("https://changkun.de/research/")
	if err != nil {
		t.Fatal(err)
	}
	cors, err := parseCORS("/demos/")
	if err != nil {
		t.Fatal(err)
	}
	return &folders{
		dir:   dir,
		files: http.FileServer(http.Dir(dir)),
		site:  fsys,
		count: counted.counter(report),
		cors:  cors,
		next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "site")
		}),
	}
}

func TestFolders(t *testing.T) {
	f := testFolders(t, wwwDir(t), func(string, string) {})
	for _, tt := range []struct {
		path string
		code int
		body string // contained in the body
	}{
		{"/book/", http.StatusOK, "book"},
		{"/book/ch1.html", http.StatusOK, "chapter 1"},
		{"/book", http.StatusMovedPermanently, ""},
		{"/book/.travis.yml", http.StatusNotFound, ""},
		{"/book/.git/config", http.StatusNotFound, ""},
		{"/book/missing.html", http.StatusNotFound, ""},
		{"/demos/", http.StatusOK, `href="bunny.obj"`},
		{"/research/talks/t.pdf", http.StatusOK, "talk"},
		// Not folders: the embedded site answers.
		{"/", http.StatusOK, "site"},
		{"/books/", http.StatusOK, "site"},
		{"/nowhere/x", http.StatusOK, "site"},
		{"/.git/config", http.StatusOK, "site"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			f.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if w.Code != tt.code {
				t.Errorf("got status %d, want %d", w.Code, tt.code)
			}
			if !strings.Contains(w.Body.String(), tt.body) {
				t.Errorf("got body %q, want it to contain %q", w.Body.String(), tt.body)
			}
		})
	}
}

func TestFoldersCount(t *testing.T) {
	var reported []string
	f := testFolders(t, wwwDir(t), func(page, ua string) {
		reported = append(reported, page+" "+ua)
	})
	for _, path := range []string{
		"/research/talks/t.pdf",
		"/research/",           // the page, which its script counts
		"/research/index.html", // the page too
		"/book/ch1.html",       // not counted
	} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("User-Agent", "reader")
		f.ServeHTTP(httptest.NewRecorder(), r)
	}
	if want := []string{"https://changkun.de/research/talks/t.pdf reader"}; !slices.Equal(reported, want) {
		t.Errorf("reported %q, want %q", reported, want)
	}
}

func TestFoldersCORS(t *testing.T) {
	f := testFolders(t, wwwDir(t), func(string, string) {})
	for path, want := range map[string]string{
		"/demos/bunny.obj": "*",
		"/demos":           "*", // the redirect to /demos/
		"/book/ch1.html":   "",
		"/book":            "",
	} {
		w := httptest.NewRecorder()
		f.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("%s: Access-Control-Allow-Origin = %q, want %q", path, got, want)
		}
	}
}

// TestFoldersOwn checks that a folder with a handler of its own is served by
// it, behind what every folder gets: the hidden files kept back, the counting
// and the CORS headers.
func TestFoldersOwn(t *testing.T) {
	var reported []string
	f := testFolders(t, wwwDir(t), func(page, ua string) { reported = append(reported, page) })
	f.own = map[string]http.Handler{
		"research": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "own "+r.URL.Path)
		}),
	}
	f.cors = []string{"/research/"}
	for path, want := range map[string]string{
		"/research/talks/t.pdf": "own /research/talks/t.pdf",
		"/research/.git/config": "404 page not found\n",
		"/book/ch1.html":        "chapter 1",
	} {
		w := request(f, path)
		if got := w.Body.String(); got != want {
			t.Errorf("%s: got %q, want %q", path, got, want)
		}
	}
	if want := []string{"https://changkun.de/research/talks/t.pdf"}; !slices.Equal(reported, want) {
		t.Errorf("reported %q, want %q", reported, want)
	}
	if w := request(f, "/research/x"); w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("a folder with its own handler lost its CORS headers")
	}
}

func request(h http.Handler, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestFoldersOutlastWriteTimeout checks that a file slower than the server's
// write timeout still arrives, while the rest of the site keeps the timeout.
func TestFoldersOutlastWriteTimeout(t *testing.T) {
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		io.WriteString(w, "late")
	})
	f := testFolders(t, wwwDir(t), func(string, string) {})
	f.files, f.next = slow, slow

	s := httptest.NewUnstartedServer(f)
	s.Config.WriteTimeout = 100 * time.Millisecond
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.Start()
	defer s.Close()

	resp, err := s.Client().Get(s.URL + "/book/ch1.html")
	if err != nil {
		t.Fatalf("a slow file did not arrive: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || string(body) != "late" {
		t.Errorf("got body %q, %v, want %q", body, err, "late")
	}

	if resp, err := s.Client().Get(s.URL + "/"); err == nil {
		resp.Body.Close()
		t.Error("a slow page of the site arrived, so the write timeout this test relies on did not apply")
	}
}

func TestParseCounted(t *testing.T) {
	got, err := parseCounted(" https://changkun.de/research/ , https://changkun.de/demos/ ")
	if err != nil {
		t.Fatal(err)
	}
	want := countedFolders{{"/research/", "https://changkun.de"}, {"/demos/", "https://changkun.de"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	for _, bad := range []string{"changkun.de/research/", "https://changkun.de/research", "/research/"} {
		if _, err := parseCounted(bad); err == nil {
			t.Errorf("parseCounted(%q) did not fail", bad)
		}
	}
}

func TestParseCORS(t *testing.T) {
	got, err := parseCORS("/demos/, /books/")
	if err != nil || !slices.Equal(got, []string{"/demos/", "/books/"}) {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"demos/", "/demos", "https://changkun.de/demos/"} {
		if _, err := parseCORS(bad); err == nil {
			t.Errorf("parseCORS(%q) did not fail", bad)
		}
	}
}

func TestWithWWW(t *testing.T) {
	l := log.New(io.Discard, "", 0)
	next := http.NotFoundHandler()

	t.Setenv("WWW_DIR", "")
	if h, err := withWWW(l, next); err != nil || h == nil {
		t.Errorf("without WWW_DIR: %v, %v", h, err)
	} else if _, ok := h.(*folders); ok {
		t.Error("without WWW_DIR, folders are served")
	}

	t.Setenv("WWW_DIR", filepath.Join(t.TempDir(), "missing"))
	if h, err := withWWW(l, next); err != nil {
		t.Errorf("a missing WWW_DIR failed: %v", err)
	} else if _, ok := h.(*folders); ok {
		t.Error("a missing WWW_DIR is served")
	}

	t.Setenv("WWW_DIR", wwwDir(t))
	t.Setenv("WWW_URLSTAT", "changkun.de/research/")
	if _, err := withWWW(l, next); err == nil {
		t.Error("a malformed WWW_URLSTAT did not fail")
	}

	t.Setenv("WWW_URLSTAT", "https://changkun.de/research/")
	t.Setenv("WWW_CORS", "/demos/")
	if h, err := withWWW(l, next); err != nil {
		t.Errorf("a valid configuration failed: %v", err)
	} else if _, ok := h.(*folders); !ok {
		t.Errorf("WWW_DIR is not served, got %T", h)
	}
}
