// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"changkun.de/x/website/internal/ideas"
)

// An Editor serves Prefix+"new": a page for writing a new post or idea,
// seeing it as the blog will show it, and publishing it to the blog's
// repository, from which the server's checkout pulls it. It serves
// Prefix+"edit/" too, which opens a post or an idea there already and
// commits the edit over its file.
type Editor struct {
	// Owner reports whether a request comes from the site's owner, the
	// only one the editor serves.
	Owner func(*http.Request) bool
	// Login is the login page, which goes on to the page named by ?next=.
	Login string
	// Publisher commits to the blog's repository. Without one, the editor
	// previews but does not publish.
	Publisher Publisher
}

// A Publisher commits to the blog's repository. *ideas.Service is one.
type Publisher interface {
	// Upload stores a file, to be committed with a page later.
	Upload(ctx context.Context, data []byte) (sha string, err error)
	// Download returns a file stored with Upload.
	Download(ctx context.Context, sha string) ([]byte, error)
	// Create commits files as a new page at root, a file or a folder. It
	// fails with ideas.ErrExists if root is taken.
	Create(ctx context.Context, root string, files []ideas.File, msg string) error
	// Update commits files over the page whose markdown is file, and
	// deletes the paths of remove, in one commit. It fails with
	// ideas.ErrChanged if file is no longer the blob base.
	Update(ctx context.Context, file, base string, files []ideas.File, remove []string, msg string) error
	// PublishIdea writes up an idea with models and publishes it, in the
	// background.
	PublishIdea(title, content string, files []ideas.Attachment)
}

// Limits on what the editor takes.
const (
	maxFile  = 25 << 20 // a file
	maxDraft = 2 << 20  // the text and fields of a draft
)

// draft is a post or an idea, as the editor sends it.
type draft struct {
	Kind    string   `json:"kind"` // "post" or "idea"
	Title   string   `json:"title"`
	TitleZh string   `json:"title_zh"`
	Name    string   `json:"name"` // a post's name in its address; from its title if empty
	Tags    []string `json:"tags"`
	Body    string   `json:"body"`
	Files   []upload `json:"files"`

	// For an edit: the page's address below the blog, and the SHA of its
	// file as the editor opened it.
	Path string `json:"path,omitempty"`
	Base string `json:"base,omitempty"`
}

// upload is a file uploaded for a draft.
type upload struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
	Size int64  `json:"size,omitempty"`
}

// editorRoutes serves the editor's addresses to the owner, and sends
// everyone else to the login page.
func (h *handler) editorRoutes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET "+Prefix+"new", h.editorPage)
	m.HandleFunc("POST "+Prefix+"new", h.publish)
	m.HandleFunc("POST "+Prefix+"new/preview", h.preview)
	m.HandleFunc("POST "+Prefix+"new/files", h.upload)
	m.HandleFunc("GET "+Prefix+"new/files/{sha}/{name}", h.download)
	m.HandleFunc("GET "+Prefix+"edit/{path...}", h.editPage)
	m.HandleFunc("POST "+Prefix+"edit/{path...}", h.save)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ed := h.editor
		if ed == nil || ed.Owner == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Robots-Tag", "noindex")
		if !ed.Owner(r) {
			if r.Method == http.MethodGet && ed.Login != "" {
				http.Redirect(w, r, ed.Login+"?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		m.ServeHTTP(w, r)
	})
}

func (h *handler) editorPage(w http.ResponseWriter, r *http.Request) {
	v := &view{Title: "New", URL: origin + Prefix + "new", MainClass: "page-editor", Math: true,
		CanPublish: h.editor.Publisher != nil}
	w.Header().Set("Content-Type", htmlType)
	w.Write(execute("new", v))
}

// readDraft reads the draft that r posts as JSON. It writes the error and
// reports false if there is none.
func readDraft(w http.ResponseWriter, r *http.Request) (draft, bool) {
	var d draft
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		http.Error(w, "the draft must be JSON", http.StatusUnsupportedMediaType)
		return d, false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDraft)).Decode(&d); err != nil {
		http.Error(w, "the draft cannot be read", http.StatusBadRequest)
		return d, false
	}
	d.Body = strings.ReplaceAll(d.Body, "\r\n", "\n")
	d.Title = strings.TrimSpace(d.Title)
	d.TitleZh = strings.TrimSpace(d.TitleZh)
	if d.Kind != "post" {
		d.Kind = "idea"
	}
	return d, true
}

// preview renders a draft's text as the blog will, with its files from
// where they were uploaded to.
func (h *handler) preview(w http.ResponseWriter, r *http.Request) {
	d, ok := readDraft(w, r)
	if !ok {
		return
	}
	now := time.Now()
	p := &Page{Section: d.Kind + "s", Title: d.Title, Path: "/new/", Date: now, Lastmod: now, links: map[string]string{}, lines: true}
	for _, f := range d.Files {
		if f.SHA != "" {
			p.links[f.Name] = "/new/files/" + url.PathEscape(f.SHA) + "/" + url.PathEscape(f.Name)
		}
	}
	if d.Path != "" {
		h.editedPreview(d.Path, d, p)
	}
	if err := render(p, []byte(d.Body)); err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": p.Content, "math": p.HasMath})
}

// upload stores the file r posts, and replies with the name it is
// committed under and how to refer to it.
func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	pub := h.editor.Publisher
	if pub == nil {
		http.Error(w, "publishing is not configured here", http.StatusServiceUnavailable)
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		http.Error(w, "the file must be sent as application/octet-stream", http.StatusUnsupportedMediaType)
		return
	}
	name := fileName(r.URL.Query().Get("name"))
	if name == "" {
		http.Error(w, "the file needs a name, and cannot be markdown", http.StatusBadRequest)
		return
	}
	// A large file on a slow line takes longer than the server's limits.
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(10 * time.Minute))
	rc.SetWriteDeadline(time.Now().Add(12 * time.Minute))
	var b bytes.Buffer
	if _, err := b.ReadFrom(http.MaxBytesReader(w, r.Body, maxFile)); err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			http.Error(w, "a file can be at most 25 MB", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "the file did not arrive whole", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	sha, err := pub.Upload(ctx, b.Bytes())
	if err != nil {
		h.log.Printf("blog: upload %s: %v", name, err)
		http.Error(w, "the file could not be stored", http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, upload{Name: name, SHA: sha, Size: int64(b.Len())})
}

// download serves an uploaded file to the preview.
func (h *handler) download(w http.ResponseWriter, r *http.Request) {
	pub := h.editor.Publisher
	if pub == nil {
		http.NotFound(w, r)
		return
	}
	data, err := pub.Download(r.Context(), r.PathValue("sha"))
	if err != nil {
		h.log.Printf("blog: download %s: %v", r.PathValue("sha"), err)
		http.NotFound(w, r)
		return
	}
	name := r.PathValue("name")
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		w.Header().Set("Content-Type", t)
	}
	// Uploads are named by their content, so they never change.
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// publish commits a post, or starts writing up an idea, and replies with
// where it will be.
func (h *handler) publish(w http.ResponseWriter, r *http.Request) {
	pub := h.editor.Publisher
	if pub == nil {
		http.Error(w, "publishing is not configured here", http.StatusServiceUnavailable)
		return
	}
	d, ok := readDraft(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(d.Body) == "" {
		http.Error(w, "there is nothing to publish", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, f := range d.Files {
		if f.Name == "" || fileName(f.Name) != f.Name || seen[f.Name] || f.SHA == "" {
			http.Error(w, "the file "+f.Name+" cannot be published", http.StatusBadRequest)
			return
		}
		seen[f.Name] = true
	}

	if d.Kind == "idea" {
		atts := make([]ideas.Attachment, len(d.Files))
		for i, f := range d.Files {
			atts[i] = ideas.Attachment{Name: f.Name, SHA: f.SHA}
		}
		pub.PublishIdea(d.Title, d.Body, atts)
		writeJSON(w, http.StatusAccepted, map[string]string{"url": Prefix + "ideas/",
			"message": "The idea is being written up, and shows on the ideas page in a few minutes."})
		return
	}

	if d.Title == "" {
		http.Error(w, "a post needs a title", http.StatusBadRequest)
		return
	}
	name := postName(d.Name)
	if d.Name == "" {
		name = postName(d.Title)
	}
	if name == "" {
		http.Error(w, "the post's address needs letters or digits", http.StatusBadRequest)
		return
	}
	at := "/posts/" + name + "/"
	if s, err := h.current(); err == nil {
		if _, ok := s.byPath[at]; ok {
			http.Error(w, "a page is already at "+Prefix+strings.TrimPrefix(at, "/"), http.StatusConflict)
			return
		}
		if _, ok := s.aliases[at]; ok {
			http.Error(w, Prefix+strings.TrimPrefix(at, "/")+" is an old address of another page", http.StatusConflict)
			return
		}
	}

	now := time.Now()
	text, err := postMarkdown(d, name, now)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	root := "content/posts/" + now.Format("2006-01-02") + "-" + name
	files := []ideas.File{{Path: root + "/index.md", Text: text}}
	for _, f := range d.Files {
		files = append(files, ideas.File{Path: root + "/" + f.Name, SHA: f.SHA})
	}
	if len(d.Files) == 0 {
		root += ".md"
		files = []ideas.File{{Path: root, Text: text}}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := pub.Create(ctx, root, files, "posts: "+d.Title); err != nil {
		if errors.Is(err, ideas.ErrExists) {
			http.Error(w, "a post is already at "+root, http.StatusConflict)
			return
		}
		h.log.Printf("blog: publish %s: %v", root, err)
		http.Error(w, "the post could not be committed", http.StatusBadGateway)
		return
	}
	h.log.Printf("blog: published %s", root)
	writeJSON(w, http.StatusCreated, map[string]string{"url": Prefix + strings.TrimPrefix(at, "/"),
		"message": "Published. The post shows once the server has pulled it, in a minute or so."})
}

// postMarkdown is the markdown file of a new post called name.
func postMarkdown(d draft, name string, date time.Time) (string, error) {
	fm := struct {
		Date    string   `yaml:"date"`
		Slug    string   `yaml:"slug"`
		Title   string   `yaml:"title"`
		TitleZh string   `yaml:"title_zh,omitempty"`
		Tags    []string `yaml:"tags,omitempty"`
	}{Date: date.Format(time.RFC3339), Slug: "/posts/" + name, Title: d.Title, TitleZh: d.TitleZh}
	for _, t := range d.Tags {
		if t = strings.TrimSpace(t); t != "" {
			fm.Tags = append(fm.Tags, t)
		}
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(4)
	if err := enc.Encode(fm); err != nil {
		return "", err
	}
	return "---\n" + b.String() + "---\n\n" + strings.TrimSpace(d.Body) + "\n", nil
}

// postName is a post's name in its address, made from s as Hugo would,
// with only what one path segment can hold.
func postName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`/\#?%`, r) {
			return '-'
		}
		return r
	}, urlize(s))
	return strings.Trim(dashes.ReplaceAllString(s, "-"), "-.")
}

var (
	dashes     = regexp.MustCompile(`-{2,}`)
	unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// fileName is the name a file is committed under, made from the name it
// was uploaded with: letters, digits, dots, dashes and underscores, and its
// extension. It is empty for markdown, which the blog would read as a page.
// editor.js names files the same way.
func fileName(s string) string {
	s = path.Base(strings.ReplaceAll(s, `\`, "/"))
	ext := path.Ext(s)
	stem, ext := clean(strings.TrimSuffix(s, ext)), clean(strings.TrimPrefix(ext, "."))
	if len(ext) > 10 {
		ext = ""
	}
	if strings.EqualFold(ext, "md") {
		return ""
	}
	if len(stem) > 80 {
		stem = clean(stem[:80])
	}
	if stem == "" {
		stem = "file"
	}
	if ext != "" {
		return stem + "." + ext
	}
	return stem
}

// clean keeps letters, digits, dots, dashes and underscores of s, with one
// dash for each run of anything else, and nothing at either end but letters,
// digits and underscores.
func clean(s string) string {
	return strings.Trim(dashes.ReplaceAllString(unsafeName.ReplaceAllString(s, "-"), "-"), "-.")
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
