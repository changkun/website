// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bufio"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

func newTestHandler(t *testing.T, dir string) http.Handler {
	t.Helper()
	files := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "files", http.StatusTeapot)
	})
	return Handler(dir, files, log.New(io.Discard, "", 0))
}

func get(t *testing.T, h http.Handler, p string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", (&url.URL{Path: p}).EscapedPath(), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestPages(t *testing.T) {
	h := newTestHandler(t, "testdata/site")
	for _, c := range []struct {
		path string
		code int
		has  []string
		not  []string
	}{
		{"/blog/", 200, []string{`href="/blog/posts/hello/"`, `id="idea-2026-01-01-one"`, "Read More", `<span class="lang-zh">你好</span>`}, []string{"Draft"}},
		{"/blog/posts/hello/", 200, []string{
			"<title>Hello - Changkun&#39;s Blog</title>",
			`<h2 id="math">Math</h2>`, `<h2 id="math-1">Math</h2>`, // one page, one set of anchors
			`<a href="#math">Math</a>`, `class="toc-rail"`,
			`$a_1 \\ b$`, `$\{x\}$`, `$a_b_c$`, `x &amp;= 1 \\ y &amp;= 2`, // math as written
			"a single ~ that stays", "<del>a strike</del>",
			`<img src="/blog/images/x.png" alt="a figure" loading="lazy" decoding="async">`,
			`href="/blog/archives/2020/01/1/"`,
			`<div class="highlight">`, `class="language-go" data-lang="go"`,
			`href="/blog/tags/c&#43;&#43;/"`, `rel="next"`, "MathJax.js",
		}, []string{"HUGOMORE42"}},
		{"/blog/posts/a-bundle/", 200, []string{`<img src="one.png"`, `/blog/assets/ifig.js?v=`, `src="/blog/posts/a-bundle/figures.js"`, `rel="prev"`}, []string{"toc-rail", "MathJax"}},
		{"/blog/posts/a-bundle/one.png", 200, []string{"PNG"}, nil},
		{"/blog/posts/2021-02-02-bundle/one.png", 404, nil, nil},
		{"/blog/images/x.png", 200, []string{"PNG"}, nil},
		{"/blog/ideas/", 200, []string{"Scattered thoughts.", `"1 / 1 ideas"`, `fetch("/blog/ideas/index.json")`}, nil},
		{"/blog/ideas/idea-one/", 200, []string{`<div class="idea-augmented">`, `id="context"`, `id="context-1"`}, []string{"toc-rail"}},
		{"/blog/about/", 200, []string{"Last update: 2026/10/03", `aria-current="page">`}, nil},
		{"/blog/posts/", 200, []string{`id="archive-year-2021"`, "2 <span"}, nil},
		{"/blog/tags/", 200, []string{`href="/blog/tags/go/"`, `>go<span class="tag-count">2</span>`}, []string{"apache"}},
		{"/blog/tags/c++/", 200, []string{"#C&#43;&#43;"}, nil},
		{"/blog/tags/go/index.xml", 200, []string{"<title>go on Changkun&#39;s Blog</title>"}, nil},
		{"/blog/index.xml", 200, []string{"https://changkun.de/blog/posts/hello/", "https://changkun.de/blog/about/"}, []string{"draft"}},
		{"/blog/ideas/index.json", 200, []string{`"url":"/blog/ideas/idea-one/"`, `"title_zh":"想法一"`}, nil},
		{"/blog/sitemap.xml", 200, []string{"<loc>https://changkun.de/blog/tags/c++/</loc>", "<lastmod>2026-10-03T00:00:00Z</lastmod>"}, nil},
		{"/blog/posts/draft/", 404, nil, nil},
		{"/blog/_todo/note/", 404, nil, nil},
		{"/blog/_todo/note.md", 404, nil, nil},
		{"/blog/tags/apache/", 404, nil, nil},
		{"/blog/page/2/", 404, nil, nil},
		{"/blog/posts/hello/index.md", 404, nil, nil},
	} {
		rec := get(t, h, c.path)
		if rec.Code != c.code {
			t.Errorf("GET %s = %d, want %d", c.path, rec.Code, c.code)
			continue
		}
		body := rec.Body.String()
		for _, s := range c.has {
			if !strings.Contains(body, s) {
				t.Errorf("GET %s lacks %q", c.path, s)
			}
		}
		for _, s := range c.not {
			if strings.Contains(body, s) {
				t.Errorf("GET %s has %q", c.path, s)
			}
		}
	}
}

func TestRedirects(t *testing.T) {
	h := newTestHandler(t, "testdata/site")
	for path, to := range map[string]string{
		"/blog":                        "/blog/",
		"/blog/archives/2020/01/1/":    "/blog/posts/hello/",
		"/blog/archives/2020/01/1":     "/blog/archives/2020/01/1/",
		"/blog/posts/hello":            "/blog/posts/hello/",
		"/blog/tags/c++":               "/blog/tags/c++/",
		"/blog/page/1/":                "/blog/",
		"/blog/index.html":             "/blog/",
		"/blog/posts/hello/index.html": "/blog/posts/hello/",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
			t.Errorf("GET %s = %d to %q, want 301 to %q", path, rec.Code, rec.Header().Get("Location"), to)
		}
	}
}

func TestWithoutContent(t *testing.T) {
	h := newTestHandler(t, t.TempDir())
	if rec := get(t, h, "/blog/"); rec.Code != http.StatusTeapot {
		t.Fatalf("GET /blog/ without a content folder = %d, want the files handler", rec.Code)
	}
}

func TestAssets(t *testing.T) {
	h := newTestHandler(t, "testdata/site")
	rec := get(t, h, "/blog/assets/ink.css")
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("GET ink.css = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestUrlize(t *testing.T) {
	for in, want := range map[string]string{
		"Go 1.16 Big Changes":           "go-1.16-big-changes",
		"Proebsting's Law":              "proebstings-law",
		`"Worse is Better"`:             "worse-is-better",
		"Tool: bench":                   "tool-bench",
		"Nesting Issues with t.Cleanup": "nesting-issues-with-t.cleanup",
		"A First Look at io/fs":         "a-first-look-at-io/fs",
		"C++":                           "c++",
		"Apple Watch":                   "apple-watch",
		"CAP 定理":                        "cap-定理",
	} {
		if got := urlize(in); got != want {
			t.Errorf("urlize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"one two three", 6, "one …"},
		{"一二三四五", 3, "一二三 …"},
	} {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// TestLive checks a checkout of changkun/blog, named by BLOG_CHECKOUT,
// against the addresses the blog had when Hugo built it.
func TestLive(t *testing.T) {
	dir := os.Getenv("BLOG_CHECKOUT")
	if dir == "" {
		t.Skip("BLOG_CHECKOUT names no checkout of changkun/blog")
	}
	h := newTestHandler(t, dir)
	f, err := os.Open("testdata/live.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		code, p := fields[0], "/blog"+fields[1]
		rec := get(t, h, p)
		switch code {
		case "200":
			if rec.Code != 200 {
				t.Errorf("GET %s = %d, want 200", p, rec.Code)
			}
		case "301":
			to, _ := url.PathUnescape(rec.Header().Get("Location"))
			if rec.Code != 301 || to != "/blog"+fields[2] {
				t.Errorf("GET %s = %d to %s, want 301 to /blog%s", p, rec.Code, to, fields[2])
			}
		case "404":
			if rec.Code != 404 {
				t.Errorf("GET %s = %d, want 404", p, rec.Code)
			}
		}
	}
}
