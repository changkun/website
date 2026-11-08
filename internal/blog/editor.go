// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"
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
	// TranslatePost translates a post written in one language into the
	// other, telling progress how many of its parts are done.
	TranslatePost(ctx context.Context, title, titleZh, body string, progress func(done, total int)) (ideas.Post, error)
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
	m.HandleFunc("GET "+Prefix+"new/jobs/{id}", h.jobStatus)
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
		CanPublish: h.editor.Publisher != nil, Tags: h.knownTags()}
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

	if d.Title == "" && d.TitleZh == "" {
		http.Error(w, "a post needs a title", http.StatusBadRequest)
		return
	}
	if langBlock.MatchString(d.Body) {
		// Written in both languages already: published as it is.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
		defer cancel()
		u, code, err := h.createPost(ctx, d)
		if err != nil {
			http.Error(w, err.Error(), code)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"url": u,
			"message": "Published. The post shows once the server has pulled it, in a minute or so."})
		return
	}
	// Written in one language, it is translated into the other first,
	// which takes longer than a request may last, so it is a job the
	// editor follows. A name it already has is checked now.
	if name := postName(d.Name); name != "" || isASCII(d.Title) {
		if name == "" {
			name = postName(d.Title)
		}
		if name == "" {
			http.Error(w, "the post's address needs letters or digits", http.StatusBadRequest)
			return
		}
		if err := h.free(name); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	j := h.startJob(func(j *job) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		tr, err := pub.TranslatePost(ctx, d.Title, d.TitleZh, d.Body, func(done, total int) {
			j.update(func() { j.Done, j.Total = done, total })
		})
		if err != nil {
			h.log.Printf("blog: translate %q: %v", d.Title, err)
			j.update(func() { j.State, j.Error = "failed", "The post could not be translated: "+err.Error() })
			return
		}
		d.Title, d.TitleZh = tr.Title, tr.TitleZh
		d.Body = "{{% en %}}\n\n" + tr.En + "\n\n{{% /en %}}\n\n{{% zh %}}\n\n" + tr.Zh + "\n\n{{% /zh %}}"
		j.update(func() { j.State = "publishing" })
		u, _, err := h.createPost(ctx, d)
		if err != nil {
			j.update(func() { j.State, j.Error = "failed", err.Error() })
			return
		}
		j.update(func() {
			j.State, j.URL = "done", u
			j.Message = "Published in English and Chinese. The post shows once the server has pulled it, in a minute or so."
		})
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"job": Prefix + "new/jobs/" + j.id})
}

// langBlock finds a block of one language in a post's text.
var langBlock = regexp.MustCompile(`\{\{%\s*(en|zh)\s*%\}\}`)

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return s != ""
}

// free returns an error if a new post called name would take the address
// of a page, or an old address of one.
func (h *handler) free(name string) error {
	at := "/posts/" + name + "/"
	s, err := h.current()
	if err != nil {
		return nil
	}
	if _, ok := s.byPath[at]; ok {
		return errors.New("a page is already at " + Prefix + strings.TrimPrefix(at, "/"))
	}
	if _, ok := s.aliases[at]; ok {
		return errors.New(Prefix + strings.TrimPrefix(at, "/") + " is an old address of another page")
	}
	return nil
}

// createPost commits d as a new post, and returns its address, or an error
// to show and the status it goes with.
func (h *handler) createPost(ctx context.Context, d draft) (string, int, error) {
	if d.Title == "" {
		return "", http.StatusBadRequest, errors.New("a post needs a title")
	}
	name := postName(d.Name)
	if d.Name == "" {
		name = postName(d.Title)
	}
	if name == "" {
		return "", http.StatusBadRequest, errors.New("the post's address needs letters or digits")
	}
	if err := h.free(name); err != nil {
		return "", http.StatusConflict, err
	}
	now := time.Now()
	text, err := postMarkdown(d, name, now)
	if err != nil {
		return "", http.StatusBadRequest, err
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
	if err := h.editor.Publisher.Create(ctx, root, files, "posts: "+d.Title); err != nil {
		if errors.Is(err, ideas.ErrExists) {
			return "", http.StatusConflict, errors.New("a post is already at " + root)
		}
		h.log.Printf("blog: publish %s: %v", root, err)
		return "", http.StatusBadGateway, errors.New("the post could not be committed")
	}
	h.log.Printf("blog: published %s", root)
	return Prefix + "posts/" + name + "/", 0, nil
}

// A job is a post being translated and published, which the editor
// follows by its id.
type job struct {
	id string
	at time.Time

	mu      sync.Mutex
	State   string `json:"state"` // "translating", "publishing", "done" or "failed"
	Done    int    `json:"done"`  // parts translated
	Total   int    `json:"total"` // parts in all
	URL     string `json:"url,omitempty"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (j *job) update(f func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	f()
}

// startJob runs work as a new job, and forgets jobs older than a day.
func (h *handler) startJob(work func(*job)) *job {
	b := make([]byte, 8)
	rand.Read(b)
	j := &job{id: hex.EncodeToString(b), at: time.Now(), State: "translating"}
	h.jobsMu.Lock()
	if h.jobs == nil {
		h.jobs = map[string]*job{}
	}
	for id, old := range h.jobs {
		if time.Since(old.at) > 24*time.Hour {
			delete(h.jobs, id)
		}
	}
	h.jobs[j.id] = j
	h.jobsMu.Unlock()
	go work(j)
	return j
}

// jobStatus replies with where a job is.
func (h *handler) jobStatus(w http.ResponseWriter, r *http.Request) {
	h.jobsMu.Lock()
	j := h.jobs[r.PathValue("id")]
	h.jobsMu.Unlock()
	if j == nil {
		http.Error(w, "no such job; the server may have restarted", http.StatusNotFound)
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	writeJSON(w, http.StatusOK, j)
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

// knownTags returns the blog's tags, the most used first, for the editor
// to offer and to tell a new tag from one in use.
func (h *handler) knownTags() []*tag {
	s, err := h.current()
	if err != nil {
		return nil
	}
	tags := slices.Clone(s.tags)
	slices.SortStableFunc(tags, func(a, b *tag) int { return len(b.Pages) - len(a.Pages) })
	return tags
}
