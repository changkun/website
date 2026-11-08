// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package blog serves changkun.de/blog from a checkout of
// github.com/changkun/blog, which holds the posts and ideas as markdown in
// its content folder. It renders the pages from the checkout, again
// whenever the checkout changes, so a git pull publishes them. The
// addresses are those Hugo, which built the blog before, gave the pages.
package blog

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// Prefix is where the blog is served.
const Prefix = "/blog/"

// origin is what the blog's permalinks start with, before Prefix.
const origin = "https://changkun.de"

const siteTitle = "Changkun's Blog"

//go:embed assets templates i18n.yaml
var embedded embed.FS

var assets = func() fs.FS {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}()

// assetVersions are the first bytes of each asset's SHA-256, put in its
// URL so that a changed asset is fetched anew.
var assetVersions = func() map[string]string {
	v := make(map[string]string)
	fs.WalkDir(assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := fs.ReadFile(assets, name)
		sum := sha256.Sum256(b)
		v[name] = hex.EncodeToString(sum[:6])
		return nil
	})
	return v
}()

// strings are the words of the page in English and in Chinese.
var i18n = func() map[string]struct{ En, Zh string } {
	b, err := embedded.ReadFile("i18n.yaml")
	if err != nil {
		panic(err)
	}
	var m map[string]struct {
		En string `yaml:"en"`
		Zh string `yaml:"zh"`
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		panic(err)
	}
	out := make(map[string]struct{ En, Zh string }, len(m))
	for k, v := range m {
		out[k] = struct{ En, Zh string }{v.En, v.Zh}
	}
	return out
}()

type link struct{ Name, URL, Icon string }

func (l link) Mail() bool { return strings.HasPrefix(l.URL, "mailto:") }

var menu = []link{
	{Name: "Home", URL: Prefix},
	{Name: "Ideas", URL: Prefix + "ideas/"},
	{Name: "Posts", URL: Prefix + "posts/"},
	{Name: "Tags", URL: Prefix + "tags/"},
	{Name: "Bio", URL: Prefix + "about/"},
}

var profiles = []link{
	{"Homepage", "https://changkun.de", "home"},
	{"GitHub", "https://github.com/changkun", "github"},
	{"Email", "mailto:contact@changkun.de", "email"},
	{"Institution", "https://www.medien.ifi.lmu.de/team/changkun.ou/", "lmu"},
	{"Google Scholar", "https://scholar.google.de/citations?user=3qjB-eUAAAAJ", "scholar"},
	{"LinkedIn", "https://www.linkedin.com/in/changkun/", "linkedin"},
	{"X", "https://x.com/changkun", "x"},
	{"Instagram", "https://www.instagram.com/ouchangkun", "instagram"},
	{"YouTube", "https://www.youtube.com/channel/UCtujb7c9eudbVEkAsIRZR3w", "youtube"},
	{"Stack Overflow", "https://stackoverflow.com/users/3819460/changkun", "stackoverflow"},
	{"Zhihu", "https://www.zhihu.com/people/ouchangkun", "zhihu"},
	{"Facebook", "https://www.facebook.com/ouchangkun", "facebook"},
	{"Telegram", "https://t.me/ouchangkun", "telegram"},
}

var friends = []link{
	{Name: "march1993", URL: "https://march1993.com/en/"},
	{Name: "qcrao", URL: "https://qcrao.com"},
	{Name: "maiyang", URL: "https://maiyang.me"},
	{Name: "Xargin", URL: "https://xargin.com/"},
	{Name: "Muniao", URL: "https://www.qtmuniao.com/"},
}

// pagePath is the address of p on the site, such as /blog/posts/x/.
func pagePath(p *Page) string { return Prefix + strings.TrimPrefix(p.Path, "/") }

var funcs = template.FuncMap{
	"t": func(key string) template.HTML {
		s, ok := i18n[key]
		if !ok {
			return template.HTML(template.HTMLEscapeString(key))
		}
		return template.HTML(`<span class="lang-en">` + template.HTMLEscapeString(s.En) +
			`</span><span class="lang-zh">` + template.HTMLEscapeString(s.Zh) + `</span>`)
	},
	"siteTitle": func() string { return siteTitle },
	"prefix":    func() string { return Prefix },
	"asset":     func(name string) string { return Prefix + "assets/" + name + "?v=" + assetVersions[name] },
	"menu":      func() []link { return menu },
	"profiles":  func() []link { return profiles },
	"friends":   func() []link { return friends },
	"year":      func() int { return time.Now().Year() },
	"url":       pagePath,
	"permalink": func(p *Page) string { return origin + pagePath(p) },
	"tagURL":    func(name string) string { return Prefix + "tags/" + urlize(name) + "/" },
	"excerpt":   excerpt,
	"augmented": func(p *Page) bool { return strings.Contains(string(p.Content), `class="idea-augmented"`) },
	"scripts": func(p *Page) []string {
		var urls []string
		for _, s := range p.Scripts {
			if _, ok := p.Files[s]; ok {
				urls = append(urls, pagePath(p)+s)
			}
		}
		return urls
	},
}

// templates are the blog's pages, by name, each with the layout around it.
var templates = func() map[string]*template.Template {
	base := template.Must(template.New("").Funcs(funcs).ParseFS(embedded, "templates/base.html", "templates/icons.html"))
	m := make(map[string]*template.Template)
	for _, name := range []string{"home", "archive", "terms", "ideas", "single", "new"} {
		m[name] = template.Must(template.Must(base.Clone()).ParseFS(embedded, "templates/"+name+".html"))
	}
	return m
}()

// view is what a page template is given.
type view struct {
	Title       string // before the site's title in the window's title
	Description string
	URL         string // the page's permalink
	Feed        string // the RSS feed of the list, if it is one
	Menu        string // the menu item the page belongs to
	MainClass   string
	Rail        bool // a contents rail on the side
	Math        bool // MathJax typesets the page
	CanPublish  bool // the editor can publish, not only preview

	Page   *Page   // for a page of its own
	Pages  []*Page // for a list
	Groups []group
	Pager  *pager
	Count  int
	Intro  *Page
	Tag    *tag
	Tags   []*tag
}

// Handler serves the blog at Prefix from dir, a checkout of changkun/blog,
// and its editor, if ed is not nil. Without a content folder in dir, as
// before the checkout took the place of Hugo's output, it passes every
// request to files, which serves dir at Prefix.
func Handler(dir string, files http.Handler, ed *Editor, l *log.Logger) http.Handler {
	h := &handler{dir: dir, content: filepath.Join(dir, "content"), files: files, editor: ed, log: l}
	h.editorMux = h.editorRoutes()
	return h
}

type handler struct {
	dir, content string
	files        http.Handler
	editor       *Editor
	editorMux    http.Handler
	log          *log.Logger

	mu      sync.Mutex
	site    *site
	key     string    // what the site was built from
	checked time.Time // when the checkout was last looked at
}

// checkEvery is how often the checkout is looked at for changes.
const checkEvery = 2 * time.Second

// current is the site as the checkout has it now, rebuilt when the
// checkout has changed since it was last built.
func (h *handler) current() (*site, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.site != nil && time.Since(h.checked) < checkEvery {
		return h.site, nil
	}
	h.checked = time.Now()
	key, err := stateOf(h.dir, h.content)
	if err != nil {
		return nil, err
	}
	if h.site != nil && key == h.key {
		return h.site, nil
	}
	start := time.Now()
	s, err := build(h.content, h.log)
	if err != nil {
		if h.site != nil {
			h.log.Printf("blog: %v; still serving the last build", err)
			return h.site, nil
		}
		return nil, err
	}
	h.log.Printf("blog: built %d pages in %v", len(s.pages), time.Since(start).Round(time.Millisecond))
	h.site, h.key = s, key
	return s, nil
}

// stateOf is a key that changes when the checkout at dir does: its commit,
// and the count, total size and latest change of the files in content.
func stateOf(dir, content string) (string, error) {
	var n, size int64
	var latest time.Time
	err := filepath.WalkDir(content, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		n++
		size += fi.Size()
		if fi.ModTime().After(latest) {
			latest = fi.ModTime()
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %d %d %d", headCommit(filepath.Join(dir, ".git")), n, size, latest.UnixNano()), nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if fi, err := os.Stat(h.content); err != nil || !fi.IsDir() {
		h.files.ServeHTTP(w, r)
		return
	}
	p := r.URL.Path
	if rest, ok := strings.CutPrefix(p, Prefix); ok && (rest == "new" || strings.HasPrefix(rest, "new/")) {
		h.editorMux.ServeHTTP(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if p+"/" == Prefix {
		http.Redirect(w, r, Prefix, http.StatusMovedPermanently)
		return
	}
	rest, ok := strings.CutPrefix(p, Prefix)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if name, ok := strings.CutPrefix(rest, "assets/"); ok {
		if _, ok := assetVersions[name]; ok {
			if r.URL.Query().Get("v") != "" {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			http.ServeFileFS(w, r, assets, name)
			return
		}
	}
	s, err := h.current()
	if err != nil {
		h.log.Printf("blog: %v", err)
		http.Error(w, "the blog is not available", http.StatusServiceUnavailable)
		return
	}
	at := "/" + rest
	if f, ok := s.files[at]; ok {
		h.serveFile(w, r, f)
		return
	}
	if res := s.response(at); res != nil {
		res.serve(w, r)
		return
	}
	if to, ok := s.aliases[at]; ok {
		redirect(w, r, Prefix+strings.TrimPrefix(to, "/"))
		return
	}
	if !strings.HasSuffix(at, "/") {
		if _, ok := s.aliases[at+"/"]; ok || s.response(at+"/") != nil {
			redirect(w, r, p+"/")
			return
		}
	}
	if dir, ok := strings.CutSuffix(at, "/index.html"); ok && s.response(dir+"/") != nil {
		redirect(w, r, Prefix+strings.TrimPrefix(dir+"/", "/"))
		return
	}
	if at == "/page/1/" || strings.HasSuffix(at, "/page/1/") {
		redirect(w, r, Prefix+strings.TrimPrefix(strings.TrimSuffix(at, "page/1/"), "/"))
		return
	}
	http.NotFound(w, r)
}

// redirect sends r on to the address p for good, escaped as the blog's
// links are, with upper case hex.
func redirect(w http.ResponseWriter, r *http.Request, p string) {
	http.Redirect(w, r, (&url.URL{Path: p}).EscapedPath(), http.StatusMovedPermanently)
}

// serveFile serves a file of the content folder.
func (h *handler) serveFile(w http.ResponseWriter, r *http.Request, name string) {
	f, err := os.Open(fileOf(h.content, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.NotFound(w, r)
		return
	}
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)
}

// A response is a rendered page or feed.
type response struct {
	body        []byte
	contentType string
	etag        string
}

func (res *response) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", res.contentType)
	w.Header().Set("ETag", res.etag)
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(res.body))
}

// response is the page or feed at address at, below Prefix, or nil when
// there is none. A page is rendered once and kept.
func (s *site) response(at string) *response {
	s.mu.Lock()
	res, ok := s.cache[at]
	s.mu.Unlock()
	if ok {
		return res
	}
	body, contentType := s.render(at)
	if body == nil {
		return nil // not kept, so that probes for missing pages add nothing
	}
	sum := sha256.Sum256(body)
	res = &response{body: body, contentType: contentType, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	s.mu.Lock()
	s.cache[at] = res
	s.mu.Unlock()
	return res
}

const (
	htmlType = "text/html; charset=utf-8"
	rssType  = "application/rss+xml; charset=utf-8"
	xmlType  = "application/xml; charset=utf-8"
	jsonType = "application/json; charset=utf-8"
)

// render renders the page or feed at address at, or returns nil.
func (s *site) render(at string) ([]byte, string) {
	// Lists, and the pages of lists.
	list, n := at, 1
	if i := strings.LastIndex(at, "/page/"); i >= 0 {
		num := strings.TrimSuffix(at[i+len("/page/"):], "/")
		k, err := strconv.Atoi(num)
		if err != nil || k < 2 || num != strconv.Itoa(k) || !strings.HasSuffix(at, "/") {
			return nil, ""
		}
		list, n = at[:i+1], k
	}
	switch {
	case list == "/":
		return s.home(n), htmlType
	case list == "/posts/":
		return s.archive(nil, n), htmlType
	case list == "/ideas/":
		return s.ideasPage(n), htmlType
	case list == "/tags/" && n == 1:
		return s.exec("terms", &view{Title: "Tags", URL: origin + Prefix + "tags/", Menu: "Tags", Tags: s.tags, Description: siteDescription}), htmlType
	case strings.HasPrefix(list, "/tags/") && strings.HasSuffix(list, "/") && strings.Count(list, "/") == 3:
		if t := s.tagsBy[strings.Trim(strings.TrimPrefix(list, "/tags/"), "/")]; t != nil {
			return s.archive(t, n), htmlType
		}
		return nil, ""
	}
	if n != 1 {
		return nil, ""
	}
	switch at {
	case "/index.xml":
		return s.rss("", Prefix, s.pages), rssType
	case "/posts/index.xml":
		return s.rss("Posts", Prefix+"posts/", s.posts), rssType
	case "/ideas/index.xml":
		return s.rss("Ideas", Prefix+"ideas/", s.ideas), rssType
	case "/ideas/index.json":
		return s.ideasJSON(), jsonType
	case "/sitemap.xml":
		return s.sitemap(), xmlType
	}
	if key, ok := strings.CutSuffix(strings.TrimPrefix(at, "/tags/"), "/index.xml"); ok && strings.HasPrefix(at, "/tags/") {
		if t := s.tagsBy[key]; t != nil {
			return s.rss(t.Name, t.URL(), t.Pages), rssType
		}
		return nil, ""
	}
	if p := s.byPath[at]; p != nil {
		return s.single(p), htmlType
	}
	return nil, ""
}

const siteDescription = "Bridging HCI, AI, and systems programming. Building intelligent human-in-the-loop optimization systems. Informed by psychology, sociology, cognitive science, and philosophy."

func (s *site) exec(name string, v *view) []byte { return execute(name, v) }

// execute renders the page template name with v.
func execute(name string, v *view) []byte {
	var b bytes.Buffer
	if err := templates[name].ExecuteTemplate(&b, "base", v); err != nil {
		return []byte(fmt.Sprintf("<!DOCTYPE html><p>The page failed to render: %s</p>", template.HTMLEscapeString(err.Error())))
	}
	return b.Bytes()
}

// homeSize is the number of posts and ideas on a page of the home page.
const homeSize = 10

func (s *site) home(n int) []byte {
	pages, total := chunk(s.stream, homeSize, n)
	if pages == nil {
		return nil
	}
	v := &view{
		URL: origin + pageURL(Prefix, n), Feed: origin + Prefix + "index.xml", Menu: "Home",
		MainClass: "page-home", Pages: pages, Pager: paginate(Prefix, n, total),
		Description: siteDescription,
	}
	for _, p := range pages {
		v.Math = v.Math || p.IsIdea() && p.HasMath
	}
	return s.exec("home", v)
}

// archiveSize is the number of pages on a page of the archive and of a tag.
const archiveSize = 50

// archive is page n of the posts, or of the pages of tag t.
func (s *site) archive(t *tag, n int) []byte {
	all, base, v := s.posts, Prefix+"posts/", &view{Title: "Posts", Menu: "Posts", Count: len(s.posts)}
	if t != nil {
		all, base, v = t.Pages, t.URL(), &view{Title: t.Name, Menu: "Tags", Tag: t}
	}
	pages, total := chunk(all, archiveSize, n)
	if pages == nil {
		return nil
	}
	v.URL, v.Feed = origin+pageURL(base, n), origin+base+"index.xml"
	v.Groups, v.Pager, v.Description = byYear(pages), paginate(base, n, total), siteDescription
	return s.exec("archive", v)
}

// ideasSize is the number of ideas on a page of the ideas.
const ideasSize = 10

func (s *site) ideasPage(n int) []byte {
	pages, total := chunk(s.ideas, ideasSize, n)
	if pages == nil {
		return nil
	}
	v := &view{
		Title: "Ideas", URL: origin + pageURL(Prefix+"ideas/", n), Feed: origin + Prefix + "ideas/index.xml",
		Menu: "Ideas", MainClass: "page-ideas", Rail: true, Pages: pages, Groups: byYear(pages),
		Pager: paginate(Prefix+"ideas/", n, total), Count: len(s.ideas), Intro: s.intro,
		Description: siteDescription,
	}
	if s.intro != nil {
		v.Description = describe(s.intro)
	} else {
		v.Intro = &Page{Title: "Ideas"}
	}
	for _, p := range pages {
		v.Math = v.Math || p.HasMath
	}
	return s.exec("ideas", v)
}

func (s *site) single(p *Page) []byte {
	menu := ""
	switch {
	case p.IsPost():
		menu = "Posts"
	case p.IsIdea():
		menu = "Ideas"
	case p.Path == "/about/":
		menu = "Bio"
	}
	v := &view{
		Title: p.Title, URL: origin + pagePath(p), Menu: menu, Page: p,
		Rail: p.HasTOC && p.TOC != "", Math: p.HasMath,
		Description: describe(p),
	}
	if v.Description == "" {
		v.Description = siteDescription
	}
	return s.exec("single", v)
}

// headCommit returns the hash of the commit checked out in gitDir, or an
// empty string if it cannot tell.
func headCommit(gitDir string) string {
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	hash := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(hash, "ref: "); ok {
		hash = ""
		if b, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
			hash = strings.TrimSpace(string(b))
		} else if packed, err := os.ReadFile(filepath.Join(gitDir, "packed-refs")); err == nil {
			for line := range strings.SplitSeq(string(packed), "\n") {
				if h, name, ok := strings.Cut(line, " "); ok && name == ref {
					hash = h
					break
				}
			}
		}
	}
	return hash
}
