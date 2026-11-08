// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"changkun.de/x/website/internal/ideas"
)

// fakePublisher keeps in memory what the editor publishes.
type fakePublisher struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	created map[string][]ideas.File // by root
	ideas   []string                // titles and bodies of the ideas published
	atts    [][]ideas.Attachment
}

func (f *fakePublisher) Upload(ctx context.Context, data []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sha := fmt.Sprintf("sha%d", len(f.blobs))
	f.blobs[sha] = bytes.Clone(data)
	return sha, nil
}

func (f *fakePublisher) Download(ctx context.Context, sha string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.blobs[sha]; ok {
		return b, nil
	}
	return nil, fmt.Errorf("no blob %s", sha)
}

func (f *fakePublisher) Create(ctx context.Context, root string, files []ideas.File, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.created[root]; ok {
		return ideas.ErrExists
	}
	f.created[root] = files
	return nil
}

func (f *fakePublisher) PublishIdea(title, content string, files []ideas.Attachment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ideas = append(f.ideas, title+"|"+content)
	f.atts = append(f.atts, files)
}

func newEditorHandler(t *testing.T, pub Publisher) http.Handler {
	t.Helper()
	ed := &Editor{
		Owner: func(r *http.Request) bool { return r.Header.Get("X-Test-Owner") == "1" },
		Login: "/library/login",
	}
	if pub != nil {
		ed.Publisher = pub
	}
	return Handler("testdata/site", http.NotFoundHandler(), ed, log.New(io.Discard, "", 0))
}

func do(t *testing.T, h http.Handler, method, path, ctype string, body []byte, owner bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if owner {
		req.Header.Set("X-Test-Owner", "1")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func asJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEditorOwnerOnly(t *testing.T) {
	h := newEditorHandler(t, &fakePublisher{blobs: map[string][]byte{}, created: map[string][]ideas.File{}})
	rec := do(t, h, "GET", "/blog/new", "", nil, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/library/login?next=%2Fblog%2Fnew" {
		t.Fatalf("anonymous GET /blog/new = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, p := range []string{"/blog/new", "/blog/new/preview", "/blog/new/files?name=a.png"} {
		if rec := do(t, h, "POST", p, "application/json", []byte("{}"), false); rec.Code != http.StatusForbidden {
			t.Errorf("anonymous POST %s = %d, want 403", p, rec.Code)
		}
	}
	rec = do(t, h, "GET", "/blog/new", "", nil, true)
	if body := rec.Body.String(); rec.Code != 200 || !strings.Contains(body, `id="editor"`) ||
		!strings.Contains(body, "data-can-publish") || !strings.Contains(body, "/blog/assets/editor.js?v=") {
		t.Fatalf("GET /blog/new = %d:\n%.300s", rec.Code, body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("the editor may be cached: %q", rec.Header().Get("Cache-Control"))
	}

	// Without an editor, there is no page.
	if rec := do(t, newTestHandler(t, "testdata/site"), "GET", "/blog/new", "", nil, true); rec.Code != http.StatusNotFound {
		t.Errorf("GET /blog/new without an editor = %d, want 404", rec.Code)
	}
	// Without a publisher, the editor previews only.
	h = newEditorHandler(t, nil)
	if rec := do(t, h, "GET", "/blog/new", "", nil, true); rec.Code != 200 || strings.Contains(rec.Body.String(), "data-can-publish") {
		t.Errorf("GET /blog/new without a publisher = %d", rec.Code)
	}
	if rec := do(t, h, "POST", "/blog/new", "application/json", []byte(`{"body":"x"}`), true); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("publish without a publisher = %d, want 503", rec.Code)
	}
}

func TestEditorFiles(t *testing.T) {
	pub := &fakePublisher{blobs: map[string][]byte{}, created: map[string][]ideas.File{}}
	h := newEditorHandler(t, pub)

	rec := do(t, h, "POST", "/blog/new/files?name=My%20Figure%20(1).PNG", "application/octet-stream", []byte("PNG"), true)
	var up upload
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &up) != nil || up.Name != "My-Figure-1.PNG" || up.Size != 3 {
		t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", "/blog/new/files/"+up.SHA+"/fig.png", "", nil, true); rec.Code != 200 ||
		rec.Body.String() != "PNG" || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("download = %d %q %s", rec.Code, rec.Body, rec.Header().Get("Content-Type"))
	}
	for _, c := range []struct {
		path, ctype string
		body        []byte
		code        int
	}{
		{"/blog/new/files?name=a.png", "image/png", []byte("PNG"), http.StatusUnsupportedMediaType},
		{"/blog/new/files?name=index.md", "application/octet-stream", []byte("x"), http.StatusBadRequest},
		{"/blog/new/files?name=a.png", "application/octet-stream", make([]byte, maxFile+1), http.StatusRequestEntityTooLarge},
	} {
		if rec := do(t, h, "POST", c.path, c.ctype, c.body, true); rec.Code != c.code {
			t.Errorf("POST %s as %s = %d, want %d", c.path, c.ctype, rec.Code, c.code)
		}
	}
}

func TestEditorPreview(t *testing.T) {
	h := newEditorHandler(t, nil)
	d := draft{Kind: "post", Body: "Hello ![a figure](fig.png) and $x_1$.\n\n![](clip.mp4)\n\n{{% en %}}\nEnglish\n{{% /en %}}",
		Files: []upload{{Name: "fig.png", SHA: "abc"}, {Name: "clip.mp4", SHA: "def"}}}
	rec := do(t, h, "POST", "/blog/new/preview", "application/json", asJSON(t, d), true)
	var res struct {
		HTML string `json:"html"`
		Math bool   `json:"math"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &res) != nil {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{`<img src="/blog/new/files/abc/fig.png" alt="a figure"`,
		`<video src="/blog/new/files/def/clip.mp4" controls`, `<div class="lang-en">`, "$x_1$"} {
		if !strings.Contains(res.HTML, want) {
			t.Errorf("preview lacks %q:\n%s", want, res.HTML)
		}
	}
	if !res.Math {
		t.Error("preview has math, but says not")
	}
	if rec := do(t, h, "POST", "/blog/new/preview", "text/plain", asJSON(t, d), true); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("preview as text/plain = %d, want 415", rec.Code)
	}
}

func TestEditorPublish(t *testing.T) {
	pub := &fakePublisher{blobs: map[string][]byte{}, created: map[string][]ideas.File{}}
	h := newEditorHandler(t, pub)
	sha, _ := pub.Upload(context.Background(), []byte("PNG"))

	post := draft{Kind: "post", Title: "A New: Post", TitleZh: "新文章", Tags: []string{"go", " notes ", ""},
		Body: "Text\r\n\r\n![](fig.png)", Files: []upload{{Name: "fig.png", SHA: sha}}}
	rec := do(t, h, "POST", "/blog/new", "application/json", asJSON(t, post), true)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"url":"/blog/posts/a-new-post/"`) {
		t.Fatalf("publish post = %d %s", rec.Code, rec.Body)
	}
	var root string
	for r := range pub.created {
		root = r
	}
	files := pub.created[root]
	if !strings.HasPrefix(root, "content/posts/") || !strings.HasSuffix(root, "-a-new-post") || len(files) != 2 ||
		files[0].Path != root+"/index.md" || files[1].Path != root+"/fig.png" || files[1].SHA != sha {
		t.Fatalf("created %s: %+v", root, files)
	}
	md := files[0].Text
	for _, want := range []string{"---\ndate: ", "\nslug: /posts/a-new-post\n", "\ntitle: 'A New: Post'\n",
		"\ntitle_zh: 新文章\n", "\ntags:\n    - go\n    - notes\n---\n\nText\n\n![](fig.png)\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("the post lacks %q:\n%s", want, md)
		}
	}
	if fm, body := splitFrontMatter([]byte(md)); fm == nil || !bytes.HasPrefix(body, []byte("\nText")) {
		t.Errorf("the post does not read back:\n%s", md)
	}

	// Without files, a post is one file; a name already taken is refused.
	rec = do(t, h, "POST", "/blog/new", "application/json", asJSON(t, draft{Kind: "post", Title: "Plain", Body: "x"}), true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("publish plain post = %d %s", rec.Code, rec.Body)
	}
	plain := false
	for r := range pub.created {
		plain = plain || strings.HasSuffix(r, "-plain.md")
	}
	if !plain {
		t.Errorf("a post without files is not one file: %v", pub.created)
	}
	for _, c := range []struct {
		d    draft
		code int
	}{
		{draft{Kind: "post", Title: "Hello", Body: "x"}, http.StatusConflict},                // /posts/hello/ is taken
		{draft{Kind: "post", Title: "Other", Name: "hello", Body: "x"}, http.StatusConflict}, // so is its name
		{draft{Kind: "post", Body: "x"}, http.StatusBadRequest},                              // no title
		{draft{Kind: "post", Title: "?", Body: "x"}, http.StatusBadRequest},                  // no name
		{draft{Kind: "post", Title: "T", Body: " "}, http.StatusBadRequest},                  // no text
		{draft{Kind: "post", Title: "T", Body: "x", Files: []upload{{Name: "../x", SHA: sha}}}, http.StatusBadRequest},
		{draft{Kind: "post", Title: "T", Body: "x", Files: []upload{{Name: "a.png", SHA: sha}, {Name: "a.png", SHA: sha}}}, http.StatusBadRequest},
	} {
		if rec := do(t, h, "POST", "/blog/new", "application/json", asJSON(t, c.d), true); rec.Code != c.code {
			t.Errorf("publish %+v = %d %s, want %d", c.d, rec.Code, rec.Body, c.code)
		}
	}

	// An idea goes to the pipeline, with its files.
	idea := draft{Kind: "idea", Body: "A thought ![](fig.png)", Files: []upload{{Name: "fig.png", SHA: sha}}}
	rec = do(t, h, "POST", "/blog/new", "application/json", asJSON(t, idea), true)
	if rec.Code != http.StatusAccepted || len(pub.ideas) != 1 || pub.ideas[0] != "|A thought ![](fig.png)" ||
		len(pub.atts[0]) != 1 || pub.atts[0][0] != (ideas.Attachment{Name: "fig.png", SHA: sha}) {
		t.Fatalf("publish idea = %d %s; %v %v", rec.Code, rec.Body, pub.ideas, pub.atts)
	}
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"fig.png":             "fig.png",
		"My Figure (1).PNG":   "My-Figure-1.PNG",
		"../../etc/passwd":    "passwd",
		`C:\Users\x\a b.jpg`:  "a-b.jpg",
		"图.png":               "file.png",
		".hidden":             "file.hidden",
		"a.b.tar.gz":          "a.b.tar.gz",
		"x.verylongextension": "x",
		"index.md":            "",
		"notes.MD":            "",
		"---":                 "file",
	} {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
		if got := fileName(want); want != "" && got != want {
			t.Errorf("fileName(%q) = %q, want it kept", want, got)
		}
	}
	for in, want := range map[string]string{
		"A New: Post":           "a-new-post",
		"Go 1.16 Big Changes":   "go-1.16-big-changes",
		"A First Look at io/fs": "a-first-look-at-io-fs",
		"CAP 定理":                "cap-定理",
		"?":                     "",
	} {
		if got := postName(in); got != want {
			t.Errorf("postName(%q) = %q, want %q", in, got, want)
		}
	}
}
