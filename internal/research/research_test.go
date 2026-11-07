// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package research

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const readme = "# Research\n<!--begin-->\n" +
	"Open-source work: [GitHub profile](https://github.com/changkun).\n\n" +
	"## Publications\n\n" +
	"- 🇬🇧 **Changkun Ou**. 2026. A paper. [doi:10.1/x](https://doi.org/10.1/x). [PDF](https://changkun.de/paper/a.pdf). [GitHub](https://github.com/changkun/a)\n" +
	"- 🇨🇳 **Changkun Ou**. 2019. A talk. [PDF](https://changkun.de/talk/b.pdf)\n" +
	"- 🇨🇳 🇬🇧 **Changkun Ou**. 2021. A book. [Website](https://changkun.de/modern-cpp)\n" +
	"\n## Scientific Activities\n\n- CHI (2025)\n" +
	"<!--end-->\n\n## License\n\nnot rendered\n"

func TestRenderPage(t *testing.T) {
	var b strings.Builder
	updated := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if err := renderPage(&b, []byte(readme), updated, "abc1234"); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{
		`href="assets/research.css?v=` + cssVersion + `"`,
		// The intro comes before the menu, which lists the sections.
		`<p>Open-source work: <a href="https://github.com/changkun">GitHub profile</a>.</p>`,
		`<a href="#publications">Publications</a>`,
		`<a href="#scientific-activities">Scientific Activities</a>`,
		`<h2 id="publications">Publications</h2>`,
		// An entry only in English carries no tag.
		`<li><strong>Changkun Ou</strong>. 2026. A paper.`,
		// The links an entry closes with become chips; a DOI does not.
		`<a href="https://doi.org/10.1/x">doi:10.1/x</a>. <a class="chip" href="https://changkun.de/paper/a.pdf">PDF</a> <a class="chip" href="https://github.com/changkun/a">GitHub</a></li>`,
		`<li><span class="lang">中文</span> <strong>Changkun Ou</strong>. 2019. A talk.`,
		`<li><span class="lang">中文 · EN</span> <strong>Changkun Ou</strong>. 2021. A book. <a class="chip" href="https://changkun.de/modern-cpp">Website</a></li>`,
		`Last updated Sep 27, 2026 (<a href="https://github.com/changkun/research/commit/abc1234">abc1234</a>)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page lacks %q", want)
		}
	}
	if strings.Index(out, "Open-source work") > strings.Index(out, `href="#publications"`) {
		t.Error("the intro follows the menu")
	}
	for _, unwanted := range []string{"🇬🇧", "🇨🇳", "not rendered", "License"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("rendered page has %q", unwanted)
		}
	}

	b.Reset()
	if err := renderPage(&b, []byte(readme), updated, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "/commit/") {
		t.Error("without a commit, the page links one")
	}

	for _, broken := range []string{"no markers", "<!--begin--> no end", "no begin <!--end-->"} {
		if err := renderPage(io.Discard, []byte(broken), updated, ""); err == nil {
			t.Errorf("renderPage(%q) did not fail", broken)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// checkout lays out a checkout of changkun/research in a WWW_DIR.
func checkout(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "research")
	for name, body := range map[string]string{
		"README.md":                  readme,
		".git/HEAD":                  "ref: refs/heads/main\n",
		".git/refs/heads/main":       "0123456789abcdef0123456789abcdef01234567\n",
		"papers/p.pdf":               "paper",
		"talks/202001/t.pdf":         "talk",
		"talks/202001/shared.pdf":    "the first shared",
		"talks/202103/shared.pdf":    "the second shared",
		"talks/202103/top.pdf":       "the dated top",
		"talks/top.pdf":              "top",
		"assets/research.css":        "the checkout's stylesheet",
		"assets/only-in-checkout.js": "script",
	} {
		write(t, filepath.Join(dir, name), body)
	}
	return dir
}

// serve serves dir at /research/ as the website does, with the WWW_DIR that
// holds it, and returns the handler and what it logs.
func serve(t *testing.T, dir string) (http.Handler, *bytes.Buffer) {
	t.Helper()
	var logged bytes.Buffer
	files := http.FileServer(http.Dir(filepath.Dir(dir)))
	return Handler(dir, "/research/", files, log.New(&logged, "", 0)), &logged
}

func get(h http.Handler, path string, header ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandler(t *testing.T) {
	h, logged := serve(t, checkout(t))
	for _, tt := range []struct {
		path     string
		code     int
		body     string // contained in the body
		location string
	}{
		{"/research/", http.StatusOK, `<a href="https://github.com/changkun/research/commit/0123456">0123456</a>`, ""},
		{"/research/index.html", http.StatusMovedPermanently, "", "/research/"},
		{"/research", http.StatusMovedPermanently, "", "research/"},
		// The page's own assets, rather than the checkout's.
		{"/research/assets/research.css", http.StatusOK, ".chip", ""},
		{"/research/assets/only-in-checkout.js", http.StatusOK, "script", ""},
		{"/research/papers/p.pdf", http.StatusOK, "paper", ""},
		// A talk at its address, found in its dated folder.
		{"/research/talks/t.pdf", http.StatusOK, "talk", ""},
		{"/research/talks/202001/t.pdf", http.StatusOK, "talk", ""},
		{"/research/talks/top.pdf", http.StatusOK, "top", ""},
		{"/research/talks/shared.pdf", http.StatusOK, "the first shared", ""},
		{"/research/talks/missing.pdf", http.StatusNotFound, "", ""},
		{"/research/talks/*.pdf", http.StatusNotFound, "", ""},
		{"/research/talks/", http.StatusOK, `href="202001/"`, ""},
	} {
		t.Run(tt.path, func(t *testing.T) {
			w := get(h, tt.path)
			if w.Code != tt.code {
				t.Errorf("got status %d, want %d", w.Code, tt.code)
			}
			if !strings.Contains(w.Body.String(), tt.body) {
				t.Errorf("got body %.200q, want it to contain %q", w.Body.String(), tt.body)
			}
			if got := w.Header().Get("Location"); got != tt.location {
				t.Errorf("got Location %q, want %q", got, tt.location)
			}
		})
	}
	if !strings.Contains(logged.String(), "talks/shared.pdf") {
		t.Errorf("talks sharing an address went unlogged: %q", logged)
	}
}

func TestHandlerPage(t *testing.T) {
	h, _ := serve(t, checkout(t))
	w := get(h, "/research/")
	if ct := w.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("got Content-Type %q", ct)
	}
	etag := w.Header().Get("ETag")
	if etag == "" {
		t.Fatal("the page has no ETag")
	}
	if w := get(h, "/research/", "If-None-Match", etag); w.Code != http.StatusNotModified {
		t.Errorf("a request for the page the client has got status %d, want %d", w.Code, http.StatusNotModified)
	}
}

// TestHandlerRerenders checks that the page follows the README, and keeps the
// last page that rendered while the README cannot.
func TestHandlerRerenders(t *testing.T) {
	dir := checkout(t)
	h, logged := serve(t, dir)
	path := filepath.Join(dir, "README.md")
	edit := func(body string, at time.Time) {
		t.Helper()
		write(t, path, body)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	day := func(d int) time.Time { return time.Date(2026, 10, d, 12, 0, 0, 0, time.UTC) }

	edit(readme, day(1))
	if body := get(h, "/research/").Body.String(); !strings.Contains(body, "Oct 01, 2026") {
		t.Errorf("the page does not date the README: %.200q", body)
	}

	edit(strings.Replace(readme, "A paper.", "A new paper.", 1), day(2))
	body := get(h, "/research/").Body.String()
	if !strings.Contains(body, "A new paper.") || !strings.Contains(body, "Oct 02, 2026") {
		t.Errorf("the page did not follow the README")
	}

	// A pull that changes the commit, not the README.
	write(t, filepath.Join(dir, ".git/refs/heads/main"), "fedcba9876543210fedcba9876543210fedcba98\n")
	if body := get(h, "/research/").Body.String(); !strings.Contains(body, "fedcba9") {
		t.Errorf("the page does not name the commit checked out: %.200q", body)
	}

	edit("# Research\n<!--begin-->\nunfinished", day(3))
	w := get(h, "/research/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "A new paper.") {
		t.Errorf("a README that cannot render replaced the page: %d %.200q", w.Code, w.Body.String())
	}
	if !strings.Contains(logged.String(), "cannot render") {
		t.Errorf("a README that cannot render went unlogged: %q", logged)
	}
}

func TestHandlerFirstRenderFails(t *testing.T) {
	dir := checkout(t)
	write(t, filepath.Join(dir, "README.md"), "no markers")
	h, _ := serve(t, dir)
	// Again too, when the README has not changed since.
	for range 2 {
		if w := get(h, "/research/"); w.Code != http.StatusInternalServerError {
			t.Errorf("got status %d, want %d", w.Code, http.StatusInternalServerError)
		}
	}
}

// TestHandlerWithoutREADME checks that a folder that is not a checkout, such
// as one a copy publishes, is served as files.
func TestHandlerWithoutREADME(t *testing.T) {
	dir := checkout(t)
	if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "index.html"), "a copied page")
	h, _ := serve(t, dir)
	for path, want := range map[string]string{
		"/research/":                    "a copied page",
		"/research/index.html":          "", // the file server's redirect to ./
		"/research/assets/research.css": "the checkout's stylesheet",
		"/research/talks/t.pdf":         "404",
	} {
		if body := get(h, path).Body.String(); !strings.Contains(body, want) {
			t.Errorf("%s: got %.200q, want it to contain %q", path, body, want)
		}
	}
}

func TestHeadCommit(t *testing.T) {
	const hash = "0123456789abcdef0123456789abcdef01234567"
	for name, files := range map[string]map[string]string{
		"loose ref": {
			"HEAD":            "ref: refs/heads/main\n",
			"refs/heads/main": hash + "\n",
		},
		"packed ref": {
			"HEAD":        "ref: refs/heads/main\n",
			"packed-refs": "# pack-refs with: peeled fully-peeled sorted\n" + "ffffffffffffffffffffffffffffffffffffffff refs/heads/other\n" + hash + " refs/heads/main\n",
		},
		"detached": {
			"HEAD": hash + "\n",
		},
	} {
		dir := t.TempDir()
		for f, body := range files {
			write(t, filepath.Join(dir, f), body)
		}
		if got := headCommit(dir); got != "0123456" {
			t.Errorf("%s: got %q, want %q", name, got, "0123456")
		}
	}
	for name, files := range map[string]map[string]string{
		"no repository": {},
		"unborn branch": {"HEAD": "ref: refs/heads/main\n"},
	} {
		dir := t.TempDir()
		for f, body := range files {
			write(t, filepath.Join(dir, f), body)
		}
		if got := headCommit(dir); got != "" {
			t.Errorf("%s: got %q, want none", name, got)
		}
	}
}
