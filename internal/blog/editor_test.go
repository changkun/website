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
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"changkun.de/x/website/internal/ideas"
)

// fakePublisher keeps in memory what the editor publishes.
type fakePublisher struct {
	mu      sync.Mutex
	blobs   map[string][]byte
	created map[string][]ideas.File // by root
	ideas   []string                // titles and bodies of the ideas published
	atts    [][]ideas.Attachment
	updates []fakeUpdate
	changed bool // every page changed in the repository since it was read
}

// fakeUpdate is an edit committed with Update.
type fakeUpdate struct {
	file, base, msg string
	files           []ideas.File
	remove          []string
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

func (f *fakePublisher) Update(ctx context.Context, file, base string, files []ideas.File, remove []string, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.changed {
		return fmt.Errorf("%s: %w", file, ideas.ErrChanged)
	}
	f.updates = append(f.updates, fakeUpdate{file: file, base: base, msg: msg, files: files, remove: remove})
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

func newFakePublisher() *fakePublisher {
	return &fakePublisher{blobs: map[string][]byte{}, created: map[string][]ideas.File{}}
}

// sha is the blob SHA of a file of the test site's content folder.
func sha(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/site/content/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return blobSHA(b)
}

func TestBlobSHA(t *testing.T) {
	// As git hash-object names them.
	if got := blobSHA(nil); got != "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391" {
		t.Errorf("blobSHA of nothing = %s", got)
	}
	if got := blobSHA([]byte("hello\n")); got != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Errorf("blobSHA(hello) = %s", got)
	}
}

// TestEditLink checks that the owner, and only the owner, is served a post
// or an idea with a link to edit it, and that everyone else is served the
// page as it is kept.
func TestEditLink(t *testing.T) {
	h := newEditorHandler(t, newFakePublisher())
	for page, edit := range map[string]string{
		"/blog/posts/hello/":    `<a class="article-edit" href="/blog/edit/posts/hello/" rel="nofollow">`,
		"/blog/ideas/idea-one/": `<a class="article-edit" href="/blog/edit/ideas/idea-one/" rel="nofollow">`,
	} {
		rec := do(t, h, "GET", page, "", nil, true)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), edit) || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("the owner's GET %s = %d, %q, without %s", page, rec.Code, rec.Header().Get("Cache-Control"), edit)
		}
		rec = do(t, h, "GET", page, "", nil, false)
		if rec.Code != 200 || strings.Contains(rec.Body.String(), "article-edit") || rec.Header().Get("ETag") == "" {
			t.Errorf("anonymous GET %s = %d, ETag %q, with the edit link: %v", page, rec.Code,
				rec.Header().Get("ETag"), strings.Contains(rec.Body.String(), "article-edit"))
		}
	}
	// A page of its own is not edited here.
	if rec := do(t, h, "GET", "/blog/about/", "", nil, true); strings.Contains(rec.Body.String(), "article-edit") {
		t.Error("the about page has an edit link")
	}
	// Without an editor, no one is the owner.
	if rec := do(t, newTestHandler(t, "testdata/site"), "GET", "/blog/posts/hello/", "", nil, true); strings.Contains(rec.Body.String(), "article-edit") {
		t.Error("a blog without an editor has an edit link")
	}
}

// openEntry returns the entry the editor opens at path.
func openEntry(t *testing.T, h http.Handler, path string) entry {
	t.Helper()
	rec := do(t, h, "GET", path, "", nil, true)
	body := rec.Body.String()
	_, after, ok := strings.Cut(body, `<script type="application/json" id="editor-entry">`)
	js, _, ok2 := strings.Cut(after, "</script>")
	var e entry
	if rec.Code != 200 || !ok || !ok2 || json.Unmarshal([]byte(js), &e) != nil {
		t.Fatalf("GET %s = %d:\n%.500s", path, rec.Code, body)
	}
	if !strings.Contains(body, " data-edit") || !strings.Contains(body, `<span class="lang-en">Save</span>`) {
		t.Errorf("GET %s is not the editor in edit mode", path)
	}
	return e
}

func TestEditPage(t *testing.T) {
	h := newEditorHandler(t, newFakePublisher())

	e := openEntry(t, h, "/blog/edit/posts/hello/")
	if e.Kind != "post" || e.Title != "Hello" || e.TitleZh != "你好" || !reflect.DeepEqual(e.Tags, []string{"Go", "C++"}) ||
		e.Name != "hello" || e.Prefix != "/blog/posts/" || e.URL != "/blog/posts/hello/" || e.Path != "/posts/hello/" ||
		e.Date != "2020-01-01" || len(e.Files) != 0 || e.Base != sha(t, "posts/2020-01-01-hello.md") ||
		!strings.HasPrefix(e.Body, "{{% en %}}\nThe start, with") {
		t.Errorf("the post opens as %+v", e)
	}
	e = openEntry(t, h, "/blog/edit/posts/a-bundle/")
	if want := []upload{{Name: "figures.js", Size: 30}, {Name: "one.png", Size: 4}}; len(e.Files) != 2 ||
		e.Files[0].Name != want[0].Name || e.Files[1].Name != want[1].Name || e.Files[0].Size == 0 || e.Files[1].SHA != "" {
		t.Errorf("the bundle's files are %+v", e.Files)
	}
	e = openEntry(t, h, "/blog/edit/ideas/idea-one/")
	if e.Kind != "idea" || e.Title != "Idea One" || e.TitleZh != "想法一" || e.Prefix != "/blog/ideas/" || e.Name != "idea-one" {
		t.Errorf("the idea opens as %+v", e)
	}

	rec := do(t, h, "GET", "/blog/edit/posts/hello/?base", "", nil, true)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"base":"`+sha(t, "posts/2020-01-01-hello.md")+`"`) {
		t.Errorf("GET ?base = %d %s", rec.Code, rec.Body)
	}
	rec = do(t, h, "GET", "/blog/edit/posts/hello/", "", nil, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/library/login?next=%2Fblog%2Fedit%2Fposts%2Fhello%2F" {
		t.Errorf("anonymous GET of the editor = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, p := range []string{"/blog/edit/about/", "/blog/edit/posts/nope/", "/blog/edit/posts/hello", "/blog/edit/"} {
		if rec := do(t, h, "GET", p, "", nil, true); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, rec.Code)
		}
	}
	// Without a publisher, the edit previews only.
	if rec := do(t, newEditorHandler(t, nil), "GET", "/blog/edit/posts/hello/", "", nil, true); rec.Code != 200 ||
		strings.Contains(rec.Body.String(), "data-can-publish") {
		t.Errorf("GET of the editor without a publisher = %d", rec.Code)
	}
}

func TestEditPreview(t *testing.T) {
	h := newEditorHandler(t, nil)
	d := draft{Kind: "post", Path: "/posts/a-bundle/", Body: "![](one.png) ![](two.png)",
		Files: []upload{{Name: "one.png"}, {Name: "two.png", SHA: "abc"}}}
	rec := do(t, h, "POST", "/blog/new/preview", "application/json", asJSON(t, d), true)
	var res struct {
		HTML string `json:"html"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &res) != nil {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body)
	}
	for _, want := range []string{`src="/blog/posts/a-bundle/one.png"`, `src="/blog/new/files/abc/two.png"`} {
		if !strings.Contains(res.HTML, want) {
			t.Errorf("the preview of an edit lacks %s:\n%s", want, res.HTML)
		}
	}
}

func TestEditSave(t *testing.T) {
	pub := newFakePublisher()
	h := newEditorHandler(t, pub)
	up, _ := pub.Upload(context.Background(), []byte("PNG"))
	save := func(path string, d draft) *httptest.ResponseRecorder {
		t.Helper()
		pub.updates = nil
		return do(t, h, "POST", path, "application/json", asJSON(t, d), true)
	}

	// A bundle: its file rewritten, a file added and one deleted.
	bundle := draft{Kind: "post", Title: "A Bundle, Edited", Tags: []string{"go", "ink"}, Body: "New text.",
		Files: []upload{{Name: "figures.js"}, {Name: "two.png", SHA: up}}, Base: sha(t, "posts/2021-02-02-bundle/index.md")}
	rec := save("/blog/edit/posts/a-bundle/", bundle)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"url":"/blog/posts/a-bundle/"`) || len(pub.updates) != 1 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	u := pub.updates[0]
	dir := "content/posts/2021-02-02-bundle"
	if u.file != dir+"/index.md" || u.base != bundle.Base || u.msg != "posts: A Bundle, Edited" ||
		len(u.files) != 2 || u.files[0].Path != dir+"/index.md" || u.files[1] != (ideas.File{Path: dir + "/two.png", SHA: up}) ||
		!reflect.DeepEqual(u.remove, []string{dir + "/one.png"}) {
		t.Fatalf("updated %+v", u)
	}
	md := u.files[0].Text
	if !strings.Contains(rec.Body.String(), `"base":"`+blobSHA([]byte(md))+`"`) {
		t.Errorf("the reply does not name the new file's SHA: %s", rec.Body)
	}
	lastmod := "\nlastmod: " + time.Now().Format("2006-01-02T")
	for _, want := range []string{"---\ndate: 2021-02-02T08:00:00+01:00" + lastmod, "\ntitle: A Bundle, Edited\n",
		"\nslug: /posts/a-bundle\nid: 7\naliases: [/images/posts/old-bundle/]\ntoc: false\ntags: [go, ink]\nscripts: [figures.js]\n---\n\nNew text.\n"} {
		if !strings.Contains(md, want) {
			t.Errorf("the edited post lacks %q:\n%s", want, md)
		}
	}

	// A page of one file stays one file, with the rest of its front matter.
	hello := draft{Kind: "post", Title: "Hello Again", Tags: []string{"Go", "C++"}, Body: "Short now.\n",
		Base: sha(t, "posts/2020-01-01-hello.md")}
	if rec := save("/blog/edit/posts/hello/", hello); rec.Code != 200 || len(pub.updates) != 1 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	u = pub.updates[0]
	if u.file != "content/posts/2020-01-01-hello.md" || len(u.files) != 1 || u.files[0].Path != u.file || len(u.remove) != 0 {
		t.Fatalf("updated %+v", u)
	}
	want := "---\ndate: \"2020-01-01 10:00:00\"" + lastmod
	if md := u.files[0].Text; !strings.HasPrefix(md, want) || strings.Contains(md, "title_zh") ||
		!strings.HasSuffix(md, "\ntitle: Hello Again\nslug: /posts/hello\nid: 1\ntoc: true\naliases:\n    - /archives/2020/01/1/\ntags:\n    - Go\n    - C++\n---\n\nShort now.\n") {
		t.Errorf("the edited post is\n%s", md)
	}

	// One that gains a file moves into a folder of its name.
	hello.Files = []upload{{Name: "a.png", SHA: up}}
	if rec := save("/blog/edit/posts/hello/", hello); rec.Code != 200 || len(pub.updates) != 1 {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	u = pub.updates[0]
	if dir := "content/posts/2020-01-01-hello"; u.file != dir+".md" || len(u.files) != 2 || u.files[0].Path != dir+"/index.md" ||
		u.files[1].Path != dir+"/a.png" || !reflect.DeepEqual(u.remove, []string{dir + ".md"}) {
		t.Fatalf("updated %+v", u)
	}

	// An idea is committed as ideas are.
	idea := draft{Kind: "idea", Title: "Idea One", TitleZh: "想法一", Body: "{{% en %}}\nEdited.\n{{% /en %}}\n",
		Base: sha(t, "ideas/2026-01-01-one.md")}
	if rec := save("/blog/edit/ideas/idea-one/", idea); rec.Code != 200 || len(pub.updates) != 1 ||
		pub.updates[0].msg != "ideas: Idea One" || !strings.Contains(pub.updates[0].files[0].Text, "\nslug: idea-one\n") {
		t.Fatalf("save idea = %d %s %+v", rec.Code, rec.Body, pub.updates)
	}

	for _, c := range []struct {
		path string
		d    draft
		code int
		msg  string
	}{
		{"/blog/edit/posts/hello/", draft{Title: "Hello", Body: "x", Base: "old"}, http.StatusConflict, changedHere},
		{"/blog/edit/posts/hello/", draft{Title: "", Body: "x", Base: hello.Base}, http.StatusBadRequest, "title"},
		{"/blog/edit/posts/hello/", draft{Title: "T", Body: " ", Base: hello.Base}, http.StatusBadRequest, "nothing to save"},
		{"/blog/edit/posts/hello/", draft{Title: "T", Body: "x", Base: hello.Base, Files: []upload{{Name: "x.png"}}}, http.StatusBadRequest, "no file"},
		{"/blog/edit/posts/hello/", draft{Title: "T", Body: "x", Base: hello.Base, Files: []upload{{Name: "../x", SHA: up}}}, http.StatusBadRequest, "cannot be saved"},
		{"/blog/edit/posts/a-bundle/", draft{Title: "T", Body: "x", Base: bundle.Base, Files: []upload{{Name: "one.png"}, {Name: "one.png", SHA: up}}}, http.StatusBadRequest, "cannot be saved"},
		{"/blog/edit/about/", draft{Title: "T", Body: "x"}, http.StatusNotFound, ""},
		// Saving it as it is commits nothing.
		{"/blog/edit/posts/a-bundle/", draft{Kind: "post", Title: "A Bundle", Tags: []string{"go"},
			Body:  "![one](one.png)\n\nText after the figure, long enough. Another sentence here.\n",
			Files: []upload{{Name: "figures.js"}, {Name: "one.png"}}, Base: bundle.Base}, http.StatusBadRequest, "nothing has changed"},
	} {
		if rec := save(c.path, c.d); rec.Code != c.code || !strings.Contains(rec.Body.String(), c.msg) || len(pub.updates) != 0 {
			t.Errorf("save %s %+v = %d %s, want %d %q", c.path, c.d, rec.Code, rec.Body, c.code, c.msg)
		}
	}

	// A page that changed in the repository is not overwritten.
	pub.changed = true
	if rec := save("/blog/edit/posts/hello/", hello); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "changed in the blog's repository") {
		t.Errorf("save over a change = %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, "POST", "/blog/edit/posts/hello/", "application/json", asJSON(t, hello), false); rec.Code != http.StatusForbidden {
		t.Errorf("anonymous save = %d, want 403", rec.Code)
	}
	if rec := do(t, newEditorHandler(t, nil), "POST", "/blog/edit/posts/hello/", "application/json", asJSON(t, hello), true); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("save without a publisher = %d, want 503", rec.Code)
	}
}

// TestEditedMarkdown checks that a page saved as it is comes out as it was,
// and that an idea whose address comes from its title keeps it.
func TestEditedMarkdown(t *testing.T) {
	for _, name := range []string{"posts/2020-01-01-hello.md", "posts/2021-02-02-bundle/index.md", "ideas/2026-01-01-one.md"} {
		src, err := os.ReadFile("testdata/site/content/" + name)
		if err != nil {
			t.Fatal(err)
		}
		p, err := newPage(os.DirFS("testdata/site/content"), name, src)
		if err != nil {
			t.Fatal(err)
		}
		_, body := splitFrontMatter(src)
		d := draft{Title: p.Title, TitleZh: p.TitleZh, Tags: p.Tags, Body: string(bytes.TrimLeft(body, "\n"))}
		if got, err := editedMarkdown(p, src, d, time.Time{}); err != nil || got != string(src) {
			t.Errorf("%s saved as it is:\n%s\nwant\n%s", name, got, src)
		}
	}

	src := []byte("---\ntitle: Old Words\ndate: 2026-01-01\n---\nText\n")
	p, err := newPage(os.DirFS("testdata/site/content"), "ideas/x.md", src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := editedMarkdown(p, src, draft{Title: "New Words", Body: "Text\n"}, time.Time{})
	if want := "---\ntitle: New Words\nslug: old-words\ndate: 2026-01-01\n---\nText\n"; err != nil || got != want {
		t.Errorf("the idea is\n%s\nwant\n%s", got, want)
	}
}
