// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"cmp"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed assets
var assetsFS embed.FS

// rowsPerPage is how many entries a list page shows.
const rowsPerPage = 200

// maxText is the longest comment or suggestion note, in characters.
const maxText = 2000

var funcs = template.FuncMap{
	"ago":  ago,
	"date": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") },
	"add":  func(a, b int) int { return a + b },
	"join": strings.Join,
	// side is the arguments of the "side" template: the topic tree, each
	// topic linking to base+key, under a first link that shows everything.
	"side": func(base, allHref, allName string, allCurrent bool, nodes []*node) map[string]any {
		return map[string]any{"base": base, "allHref": allHref, "allName": allName, "allCurrent": allCurrent, "nodes": nodes}
	},
	"tree":     func(base string, nodes []*node) map[string]any { return map[string]any{"base": base, "nodes": nodes} },
	"icon":     typeIcon,
	"typeName": typeName,
	"linkify":  linkify,
	"plural": func(n int, one, many string) string {
		if n == 1 {
			return fmt.Sprintf("%d %s", n, one)
		}
		return fmt.Sprintf("%d %s", n, many)
	},
	"num": func(n int) string { // 6086 -> 6,086
		s := strconv.Itoa(n)
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + "," + s[i:]
		}
		return s
	},
}

// pages are parsed once: layout.html plus one page each.
var pages = func() map[string]*template.Template {
	layout := template.Must(template.New("layout.html").Funcs(funcs).ParseFS(templatesFS, "templates/layout.html", "templates/pane.html"))
	names, _ := fs.Glob(templatesFS, "templates/*.html")
	out := map[string]*template.Template{"pane": layout}
	for _, n := range names {
		if n == "templates/layout.html" || n == "templates/pane.html" {
			continue
		}
		t := template.Must(template.Must(layout.Clone()).ParseFS(templatesFS, n))
		out[strings.TrimSuffix(strings.TrimPrefix(n, "templates/"), ".html")] = t
	}
	return out
}()

// view is what every page template gets.
type view struct {
	Title   string
	Owner   bool   // the owner's pages: nav, inbox count, sync status
	Base    string // entry links are Base + key
	Unread  int
	Synced  time.Time
	SyncErr string
	Fetched int // items fetched so far, while a sync runs
	Total   int
	Data    any
	Pane    *view  // the entry shown in the right pane of a list page, if any
	Home    string // where "Library" in the header leads: the owner's library or the share
	Show    string // ?show=: the panel the right pane opens with when no entry is selected
}

func (s *Service) render(w http.ResponseWriter, r *http.Request, status int, name string, v view) {
	if v.Owner {
		v.Home = Prefix
		v.Unread = s.unread()
		v.Synced, v.SyncErr, _ = s.status()
		v.Fetched, v.Total = s.progress()
	}
	v.Show = r.URL.Query().Get("show")
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	if err := pages[name].ExecuteTemplate(w, "layout.html", v); err != nil {
		s.log.Printf("library: render %s: %v", name, err)
	}
}

// renderEntry writes an entry page, or with ?pane= only the right pane of a
// list page, which the script loads when a row is selected.
func (s *Service) renderEntry(w http.ResponseWriter, r *http.Request, v view) {
	if !r.URL.Query().Has("pane") {
		s.render(w, r, http.StatusOK, "entry", v)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	if err := pages["pane"].ExecuteTemplate(w, "pane", v); err != nil {
		s.log.Printf("library: render pane: %v", err)
	}
}

// listBack returns the address of the list page a pane was loaded into, from
// ?back=, if it is under prefix, and "" otherwise. Forms in the pane return
// there.
func listBack(r *http.Request, prefix string) string {
	b := r.FormValue("back")
	if !strings.HasPrefix(b, prefix) || strings.ContainsAny(b, "\\\r\n") {
		return ""
	}
	return b
}

func (s *Service) notFound(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusNotFound, "message", view{Title: "Not found",
		Data: "There is nothing at this address. If someone sent you this link, it may have been revoked."})
}

func (s *Service) fail(w http.ResponseWriter, r *http.Request, status int, msg string) {
	s.render(w, r, status, "message", view{Title: http.StatusText(status), Owner: s.owner.is(r), Data: msg})
}

func (s *Service) routes() http.Handler {
	m := http.NewServeMux()
	assets, _ := fs.Sub(assetsFS, "assets")
	m.Handle("GET /library/assets/", http.StripPrefix("/library/assets/", http.FileServerFS(assets)))
	m.HandleFunc("GET /library/healthz", s.healthz)
	m.HandleFunc("GET /library/login", s.loginPage)
	m.HandleFunc("POST /library/login", s.owner.login)
	m.HandleFunc("POST /library/logout", s.owner.logout)

	own := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if s.owner.is(r) {
				h(w, r)
				return
			}
			if r.Method == http.MethodGet {
				http.Redirect(w, r, Prefix+"login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "forbidden", http.StatusForbidden)
		}
	}
	m.HandleFunc("GET /library/{$}", own(s.home))
	m.HandleFunc("GET /library/all.bib", own(s.home))
	m.HandleFunc("GET /library/t/{key}", own(s.topic))
	m.HandleFunc("GET /library/e/{key}", own(s.ownerEntry))
	m.HandleFunc("GET /library/inbox", own(s.inbox))
	m.HandleFunc("POST /library/inbox/read", own(s.markRead))
	m.HandleFunc("POST /library/sync", own(s.syncNow))
	m.HandleFunc("GET /library/shares", own(s.shares))
	m.HandleFunc("POST /library/shares", own(s.createShare))
	m.HandleFunc("POST /library/shares/{id}", own(s.updateShare))
	m.HandleFunc("POST /library/shares/{id}/members", own(s.addMembers))
	m.HandleFunc("POST /library/shares/{id}/revoke", own(s.revoke))
	m.HandleFunc("POST /library/shares/{id}/hide", own(s.mark("hide")))
	m.HandleFunc("POST /library/shares/{id}/unhide", own(s.mark("unhide")))

	m.HandleFunc("GET /library/s/{token}", s.memberShare)
	m.HandleFunc("GET /library/s/{token}/e/{key}", s.memberEntry)
	m.HandleFunc("POST /library/s/{token}/comment", s.comment)
	m.HandleFunc("POST /library/s/{token}/suggest", s.suggest)
	m.HandleFunc("GET /library/p/{id}", s.publicShare)
	m.HandleFunc("GET /library/p/{id}/e/{key}", s.publicEntry)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Share links carry their token in the path: never send it on.
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Robots-Tag", "noindex")
		m.ServeHTTP(w, r)
	})
}

func (s *Service) healthz(w http.ResponseWriter, r *http.Request) {
	ok, msg, _ := s.status()
	if time.Since(ok) > time.Hour {
		http.Error(w, "last successful sync: "+ago(ok)+"; "+msg, http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok, synced", ago(ok))
}

func (s *Service) loginPage(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, Prefix) || strings.HasPrefix(next, "//") {
		next = Prefix
	}
	if s.owner.is(r) {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.render(w, r, http.StatusOK, "login", view{Title: "Log in", Data: next})
}

// list is a filtered, sorted, paged list of entries.
type list struct {
	Q        string
	T        string // ?t=, the topic a share page is narrowed to
	Sort     string // ?sort=: a sortKeys key, "-" in front for descending
	Page     int
	Pages    int
	Total    int // entries before filtering
	Matched  int // entries after filtering
	From, To int
	Rows     []row
	Path     string // this page's path, for the pager and .bib

	HasComments bool // some row on this page has comments, so the column shows
}

type row struct {
	*Entry
	Comments int
}

func (l *list) Prev() string      { return l.link(l.Page - 1) }
func (l *list) Next() string      { return l.link(l.Page + 1) }
func (l *list) link(p int) string { return l.url(l.Sort, p) }

// Here is the list's own address, without a selected entry or panel.
func (l *list) Here() string { return l.url(l.Sort, l.Page) }

// Panel is the list's address with the right pane open on a panel.
func (l *list) Panel(name string) string {
	u, _ := url.Parse(l.Here())
	q := u.Query()
	q.Set("show", name)
	u.RawQuery = q.Encode()
	return u.String()
}

// url is the list's address with the given sort and page, keeping the filter
// and topic.
func (l *list) url(sort string, p int) string {
	q := l.params()
	if sort != "" && sort != defaultSort {
		q.Set("sort", sort)
	}
	if p > 1 {
		q.Set("page", strconv.Itoa(p))
	}
	if len(q) == 0 {
		return l.Path
	}
	return l.Path + "?" + q.Encode()
}

// BibPath is the list's .bib export, with the same filter.
func (l *list) BibPath() string {
	p := strings.TrimSuffix(l.Path, "/")
	if p+"/" == Prefix {
		p = Prefix + "all"
	}
	if q := l.params(); len(q) > 0 {
		return p + ".bib?" + q.Encode()
	}
	return p + ".bib"
}

// params is the list's filter and topic as query parameters.
func (l *list) params() url.Values {
	q := url.Values{}
	if l.Q != "" {
		q.Set("q", l.Q)
	}
	if l.T != "" {
		q.Set("t", l.T)
	}
	return q
}

// SortBy returns the address that sorts the list by column k: descending for
// year and added, ascending otherwise, and reversed if k is already the sort.
func (l *list) SortBy(k string) string {
	next := k
	if k == "year" || k == "added" {
		next = "-" + k
	}
	if l.Sort == next {
		next = strings.TrimPrefix(next, "-")
		if next == l.Sort {
			next = "-" + k
		}
	}
	return l.url(next, 1)
}

// Arrow marks the column the list is sorted by.
func (l *list) Arrow(k string) string {
	switch l.Sort {
	case k:
		return " ↑"
	case "-" + k:
		return " ↓"
	}
	return ""
}

const defaultSort = "-year"

// sortKeys compare two entries in ascending order, for ?sort=. Ties fall back
// to the default order, newest first, then by title.
var sortKeys = map[string]func(a, b *Entry) int{
	"year":  func(a, b *Entry) int { return cmp.Compare(a.Year, b.Year) },
	"title": func(a, b *Entry) int { return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)) },
	"authors": func(a, b *Entry) int {
		return cmp.Compare(strings.ToLower(a.ShortAuthors()), strings.ToLower(b.ShortAuthors()))
	},
	"venue": func(a, b *Entry) int { return cmp.Compare(strings.ToLower(a.Venue), strings.ToLower(b.Venue)) },
	"added": func(a, b *Entry) int { return cmp.Compare(a.Added, b.Added) },
}

// sorted returns es ordered by sort, which is a sortKeys key with an
// optional "-"; es itself is not changed. es is already in the default order.
func sorted(es []*Entry, sort string) []*Entry {
	key, desc := strings.CutPrefix(sort, "-")
	f := sortKeys[key]
	if f == nil || sort == defaultSort {
		return es
	}
	out := slices.Clone(es)
	slices.SortStableFunc(out, func(a, b *Entry) int {
		if desc {
			return f(b, a)
		}
		return f(a, b)
	})
	return out
}

// newList filters es by ?q= and cuts out ?page=. counts maps item keys to
// comment counts.
func newList(r *http.Request, path string, es []*Entry, counts map[string]int) *list {
	l := &list{Q: strings.TrimSpace(r.URL.Query().Get("q")), Total: len(es), Path: path, Sort: defaultSort}
	if s := r.URL.Query().Get("sort"); sortKeys[strings.TrimPrefix(s, "-")] != nil {
		l.Sort = s
	}
	es = sorted(es, l.Sort)
	if words := queryWords(l.Q); len(words) > 0 {
		var kept []*Entry
		for _, e := range es {
			if e.match(words) {
				kept = append(kept, e)
			}
		}
		es = kept
	}
	l.Matched = len(es)
	l.Pages = max(1, (len(es)+rowsPerPage-1)/rowsPerPage)
	l.Page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	l.Page = min(max(l.Page, 1), l.Pages)
	l.From = (l.Page - 1) * rowsPerPage
	l.To = min(l.From+rowsPerPage, len(es))
	for _, e := range es[l.From:l.To] {
		l.Rows = append(l.Rows, row{Entry: e, Comments: counts[e.Key]})
		l.HasComments = l.HasComments || counts[e.Key] > 0
	}
	return l
}

// filtered applies ?q= to es, for .bib exports.
func filtered(r *http.Request, es []*Entry) []*Entry {
	if s := r.URL.Query().Get("sort"); sortKeys[strings.TrimPrefix(s, "-")] != nil {
		es = sorted(es, s)
	}
	words := queryWords(r.URL.Query().Get("q"))
	if len(words) == 0 {
		return es
	}
	var out []*Entry
	for _, e := range es {
		if e.match(words) {
			out = append(out, e)
		}
	}
	return out
}

func writeBib(w http.ResponseWriter, name string, es []*Entry) {
	w.Header().Set("Content-Type", "application/x-bibtex; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", name+".bib"))
	w.Header().Set("Cache-Control", "private, no-store")
	fmt.Fprint(w, bibtexFile(es))
}

// commentCounts counts the visible comments per item key, of one share or of
// every share.
func (s *Service) commentCounts(shareID string) map[string]int {
	out := map[string]int{}
	for _, p := range s.store.visiblePosts(shareID) {
		if p.Type == "comment" && p.Entry != "" {
			out[p.Entry]++
		}
	}
	return out
}

func (s *Service) unread() int {
	n := 0
	for _, p := range s.store.visiblePosts("") {
		if !s.store.isRead(p.ID) {
			n++
		}
	}
	return n
}

// node is one topic in a tree.
type node struct {
	Key      string
	Name     string
	Count    int
	Children []*node
	Current  bool // the topic the page shows
	Open     bool // Current is this node or below it, so the tree shows it open
}

// mark sets Current on the node with key and Open on it and its ancestors,
// and reports whether key is among nodes.
func mark(nodes []*node, key string) bool {
	found := false
	for _, n := range nodes {
		n.Current = key != "" && n.Key == key
		n.Open = mark(n.Children, key) || n.Current
		found = found || n.Open
	}
	return found
}

// tree returns the topics below each of roots, which are included.
func (ix *index) tree(roots []string, base string) []*node {
	var build func(key string, depth int) *node
	build = func(key string, depth int) *node {
		c := ix.cache.Collections[key]
		if c == nil || depth > 32 {
			return nil
		}
		n := &node{Key: key, Name: c.Name, Count: ix.countIn(key)}
		for _, ck := range ix.children[key] {
			if child := build(ck, depth+1); child != nil {
				n.Children = append(n.Children, child)
			}
		}
		return n
	}
	var out []*node
	for _, k := range roots {
		if n := build(k, 0); n != nil {
			out = append(out, n)
		}
	}
	return out
}

// listPage is the data of the owner's home and topic pages.
type listPage struct {
	Topic    *Collection
	Path     []*Collection // from the root, for the breadcrumb
	Tree     []*node
	List     *list
	Shares   []*Share // to add this topic to
	Unsorted int      // entries in no topic, on the home page
}

func (s *Service) home(w http.ResponseWriter, r *http.Request) {
	ix := s.ix.Load()
	if strings.HasSuffix(r.URL.Path, ".bib") {
		writeBib(w, "library", filtered(r, ix.sorted))
		return
	}
	unsorted := 0
	for _, e := range ix.sorted {
		if len(e.Collections) == 0 {
			unsorted++
		}
	}
	l := newList(r, Prefix, ix.sorted, s.commentCounts(""))
	s.render(w, r, http.StatusOK, "list", view{Title: "Library", Owner: true, Base: Prefix + "e/",
		Pane: pane(r, true, Prefix+"e/", l, func(e *Entry) *entryPage { return s.ownerPane(ix, e) }),
		Data: &listPage{
			Tree:     ix.tree(ix.children[""], Prefix),
			List:     l,
			Unsorted: unsorted,
		}})
}

func (s *Service) topic(w http.ResponseWriter, r *http.Request) {
	ix := s.ix.Load()
	key, bib := strings.CutSuffix(r.PathValue("key"), ".bib")
	c := ix.cache.Collections[key]
	if c == nil {
		s.notFound(w, r)
		return
	}
	es := ix.entriesIn([]string{key})
	if bib {
		writeBib(w, c.Name, filtered(r, es))
		return
	}
	l := newList(r, Prefix+"t/"+key, es, s.commentCounts(""))
	tree := ix.tree(ix.children[""], Prefix)
	mark(tree, key)
	s.render(w, r, http.StatusOK, "list", view{Title: c.Name, Owner: true, Base: Prefix + "e/",
		Pane: pane(r, true, Prefix+"e/", l, func(e *Entry) *entryPage { return s.ownerPane(ix, e) }),
		Data: &listPage{
			Topic:  c,
			Path:   ix.path(key),
			Tree:   tree,
			List:   l,
			Shares: s.store.allShares(),
		}})
}

// entryPage is the data of an entry page.
type entryPage struct {
	*Entry
	Topics  [][]*Collection // paths of the entry's topics that the reader may see
	Threads []thread
	Token   string // a member's token, for the comment form
	Done    string // a message after posting
	Back    string // the list page the pane is shown in; "" on the entry page
	Hidden  int    // owner: hidden comments on the entry, listed in the inbox
}

// Close is the list page without this entry selected.
func (p *entryPage) Close() string {
	u, err := url.Parse(p.Back)
	if err != nil {
		return Prefix
	}
	q := u.Query()
	for _, k := range []string{"e", "show", "done", "error"} {
		q.Del(k)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// thread is the comments of one share on one entry.
type thread struct {
	Share *Share
	Posts []*Post
}

func (ix *index) topicPaths(e *Entry, allowed func(string) bool) [][]*Collection {
	var out [][]*Collection
	for _, ck := range e.Collections {
		if ix.cache.Collections[ck] != nil && allowed(ck) {
			out = append(out, ix.path(ck))
		}
	}
	slices.SortFunc(out, func(a, b []*Collection) int {
		return strings.Compare(pathString(a), pathString(b))
	})
	return out
}

func pathString(p []*Collection) string {
	var names []string
	for _, c := range p {
		names = append(names, c.Name)
	}
	return strings.Join(names, " / ")
}

func (s *Service) ownerEntry(w http.ResponseWriter, r *http.Request) {
	ix := s.ix.Load()
	e := ix.entries[r.PathValue("key")]
	if e == nil {
		s.notFound(w, r)
		return
	}
	p := s.ownerPane(ix, e)
	p.Back = listBack(r, Prefix)
	s.renderEntry(w, r, view{Title: e.Title, Owner: true, Base: Prefix + "e/", Data: p})
}

// ownerPane is an entry as the owner sees it, with comments from every share.
func (s *Service) ownerPane(ix *index, e *Entry) *entryPage {
	p := &entryPage{Entry: e, Topics: ix.topicPaths(e, func(string) bool { return true })}
	for _, sh := range s.store.allShares() {
		var posts []*Post
		for _, q := range s.store.visiblePosts(sh.ID) {
			if q.Type == "comment" && q.Entry == e.Key {
				posts = append(posts, q)
			}
		}
		if len(posts) > 0 {
			p.Threads = append(p.Threads, thread{Share: sh, Posts: posts})
		}
		for _, q := range s.store.hiddenPosts(sh.ID) {
			if q.Type == "comment" && q.Entry == e.Key {
				p.Hidden++
			}
		}
	}
	return p
}

// pane returns the right pane for ?e= on a list page, or nil.
func pane(r *http.Request, owner bool, base string, list *list, build func(*Entry) *entryPage) *view {
	key := r.URL.Query().Get("e")
	if key == "" {
		return nil
	}
	for _, row := range list.Rows {
		if row.Key == key {
			p := build(row.Entry)
			p.Back = r.URL.RequestURI()
			return &view{Owner: owner, Base: base, Data: p}
		}
	}
	return nil
}

// inboxItem is a post as the inbox lists it.
type inboxItem struct {
	*Post
	ShareTitle string
	EntryTitle string // "" for share-wide comments and suggestions
	Removed    bool   // the entry is no longer in the library
	Read       bool
}

// inboxPage is the data of the inbox: the visible posts, or with ?hidden=1
// the hidden ones.
type inboxPage struct {
	Items       []inboxItem
	Hidden      bool
	HiddenCount int
}

func (s *Service) inbox(w http.ResponseWriter, r *http.Request) {
	ix := s.ix.Load()
	titles := map[string]string{}
	for _, sh := range s.store.allShares() {
		titles[sh.ID] = sh.Title
	}
	hidden := r.URL.Query().Get("hidden") == "1"
	posts := s.store.visiblePosts("")
	if hidden {
		posts = s.store.hiddenPosts("")
	}
	var items []inboxItem
	for _, p := range slices.Backward(posts) {
		it := inboxItem{Post: p, ShareTitle: titles[p.Share], Read: s.store.isRead(p.ID)}
		if p.Entry != "" {
			if e := ix.entries[p.Entry]; e != nil {
				it.EntryTitle = e.Title
			} else {
				it.Removed = true
			}
		}
		items = append(items, it)
	}
	s.render(w, r, http.StatusOK, "inbox", view{Title: "Inbox", Owner: true, Base: Prefix + "e/",
		Data: inboxPage{Items: items, Hidden: hidden, HiddenCount: len(s.store.hiddenPosts(""))}})
}

func (s *Service) markRead(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ids := r.Form["id"]
	if r.Form.Get("all") != "" {
		ids = nil
		for _, p := range s.store.visiblePosts("") {
			ids = append(ids, p.ID)
		}
	}
	if err := s.store.markRead(ids); err != nil {
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, Prefix+"inbox", http.StatusSeeOther)
}

func (s *Service) syncNow(w http.ResponseWriter, r *http.Request) {
	// The sync runs on after the redirect, until the server stops; the status
	// line shows its progress and outcome. A sync already running finishes
	// first. Tying it to the request would hold shutdown until it ends.
	ctx := s.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() { _ = s.Sync(ctx) }()
	back := r.FormValue("next")
	if !strings.HasPrefix(back, Prefix) {
		back = Prefix
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// sharesPage is the data of the owner's shares page.
type sharesPage struct {
	Shares  []shareView
	Created string // id of a share just created, to point at its links
}

type shareView struct {
	*Share
	Topics  []*Collection // nil entries for topics no longer in Zotero
	Missing []string      // keys of those topics
	Links   map[string]string
	Public  string // the public address, if public
}

// origin is the scheme and host the request came to, for absolute links.
func origin(r *http.Request) string {
	scheme := "http"
	if secure(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Service) shares(w http.ResponseWriter, r *http.Request) {
	ix := s.ix.Load()
	p := &sharesPage{Created: r.URL.Query().Get("created")}
	for _, sh := range s.store.allShares() {
		v := shareView{Share: sh, Links: map[string]string{}}
		for _, t := range sh.Topics {
			if c := ix.cache.Collections[t]; c != nil {
				v.Topics = append(v.Topics, c)
			} else {
				v.Missing = append(v.Missing, t)
			}
		}
		for _, m := range sh.Members {
			v.Links[m.Token] = origin(r) + Prefix + "s/" + m.Token
		}
		if sh.Public {
			v.Public = origin(r) + Prefix + "p/" + sh.ID
		}
		p.Shares = append(p.Shares, v)
	}
	s.render(w, r, http.StatusOK, "shares", view{Title: "Shares", Owner: true, Data: p})
}

// names splits a textarea into one name per line.
// names splits a list of names on new lines and commas.
func names(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == ',' }) {
		if n := strings.TrimSpace(f); n != "" && utf8.RuneCountInString(n) <= 80 {
			out = append(out, n)
		}
	}
	return out
}

func (s *Service) createShare(w http.ResponseWriter, r *http.Request) {
	ix := s.ix.Load()
	topic := r.FormValue("topic")
	if ix.cache.Collections[topic] == nil {
		s.fail(w, r, http.StatusBadRequest, "That topic does not exist.")
		return
	}
	// Adding the topic to an existing share is the same form, with a share id.
	if id := r.FormValue("share"); id != "" {
		sh := s.store.shareByID(id)
		if sh == nil {
			s.notFound(w, r)
			return
		}
		if !slices.Contains(sh.Topics, topic) {
			sh.Topics = append(sh.Topics, topic)
		}
		if err := s.store.updateShare(id, sh.Title, sh.Topics, sh.Public); err != nil {
			s.fail(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		http.Redirect(w, r, Prefix+"shares#"+id, http.StatusSeeOther)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		title = ix.cache.Collections[topic].Name
	}
	sh, err := s.store.createShare(title, []string{topic}, names(r.FormValue("names")), r.FormValue("public") != "")
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, Prefix+"shares?created="+sh.ID+"#"+sh.ID, http.StatusSeeOther)
}

// updateShare sets the title and the public flag, and removes topics.
func (s *Service) updateShare(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sh := s.store.shareByID(id)
	if sh == nil {
		s.notFound(w, r)
		return
	}
	r.ParseForm()
	title := strings.TrimSpace(r.Form.Get("title"))
	if title == "" {
		title = sh.Title
	}
	topics := slices.DeleteFunc(sh.Topics, func(t string) bool { return slices.Contains(r.Form["remove"], t) })
	if err := s.store.updateShare(id, title, topics, r.Form.Get("public") != ""); err != nil {
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, Prefix+"shares#"+id, http.StatusSeeOther)
}

func (s *Service) addMembers(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.addMembers(id, names(r.FormValue("names"))); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.notFound(w, r)
			return
		}
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, Prefix+"shares#"+id, http.StatusSeeOther)
}

func (s *Service) revoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.store.revoke(id, r.FormValue("token")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.notFound(w, r)
			return
		}
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	http.Redirect(w, r, Prefix+"shares#"+id, http.StatusSeeOther)
}

// mark hides or unhides a comment or suggestion, by appending a record of
// type typ. Hidden posts are listed at /library/inbox?hidden=1.
func (s *Service) mark(typ string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { s.markPost(w, r, typ) }
}

func (s *Service) markPost(w http.ResponseWriter, r *http.Request, typ string) {
	id := r.PathValue("id")
	if _, err := s.store.appendPost(id, Post{By: "owner", Type: typ, Target: r.FormValue("post")}); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			s.notFound(w, r)
			return
		}
		s.fail(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	back := r.FormValue("next")
	if !strings.HasPrefix(back, Prefix) {
		back = Prefix + "inbox"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// sharePage is the data of a member's or the public's share page.
type sharePage struct {
	Share       *Share
	Member      *Member // nil on the public page
	Token       string
	Tree        []*node
	TreeBase    string        // a topic's link is TreeBase + its key
	Sub         []*Collection // the selected topic's path below the share's topic, if one is selected
	List        *list
	Comments    []*Post // on the whole share
	Suggestions []suggestion
	Done        string // a message after posting
	Error       string
}

type suggestion struct {
	*Post
	Added *Entry // the entry that now matches it, if any
}

// inShare reports whether collection key is one of a share's topics or below
// one.
func (ix *index) inShare(sh *Share, key string) bool {
	for _, t := range sh.Topics {
		if slices.Contains(ix.subtree(t), key) {
			return true
		}
	}
	return false
}

// shareEntries returns the entries in a share's topics.
func (ix *index) shareEntries(sh *Share) []*Entry {
	return ix.entriesIn(sh.Topics)
}

func (s *Service) memberShare(w http.ResponseWriter, r *http.Request) {
	token, bib := strings.CutSuffix(r.PathValue("token"), ".bib")
	sh, m := s.store.member(token)
	if sh == nil {
		s.notFound(w, r)
		return
	}
	s.sharePage(w, r, sh, m, token, bib, Prefix+"s/"+token)
}

func (s *Service) publicShare(w http.ResponseWriter, r *http.Request) {
	id, bib := strings.CutSuffix(r.PathValue("id"), ".bib")
	sh := s.store.shareByID(id)
	if sh == nil || !sh.Public {
		s.notFound(w, r)
		return
	}
	s.sharePage(w, r, sh, nil, "", bib, Prefix+"p/"+id)
}

func (s *Service) sharePage(w http.ResponseWriter, r *http.Request, sh *Share, m *Member, token string, bib bool, path string) {
	ix := s.ix.Load()
	es := ix.shareEntries(sh)
	// ?t= narrows the page to one topic of the share. A key outside the
	// share is ignored, so the share's own pages never reveal other topics.
	t := r.URL.Query().Get("t")
	shown := es
	if t != "" && ix.inShare(sh, t) {
		shown = ix.entriesIn([]string{t})
	} else {
		t = ""
	}
	if bib {
		writeBib(w, sh.Title, filtered(r, shown))
		return
	}
	p := &sharePage{Share: sh, Member: m, Token: token, Tree: ix.tree(sh.Topics, path), TreeBase: path + "?t=",
		Done: r.URL.Query().Get("done"), Error: r.URL.Query().Get("error")}
	mark(p.Tree, t)
	if t != "" {
		// The path from the share's own topic down to t, without it.
		path := ix.path(t)
		for i, c := range path {
			if slices.Contains(sh.Topics, c.Key) {
				if len(sh.Topics) == 1 {
					i++ // the share's title already names its one topic
				}
				path = path[i:]
				break
			}
		}
		p.Sub = path
	}
	if len(p.Tree) == 1 { // the one root topic is the whole share, which "All" shows
		p.Tree = p.Tree[0].Children
	}
	counts := map[string]int{}
	if m != nil { // the public sees no comments
		counts = s.commentCounts(sh.ID)
		for _, q := range s.store.visiblePosts(sh.ID) {
			switch {
			case q.Type == "comment" && q.Entry == "":
				p.Comments = append(p.Comments, q)
			case q.Type == "suggest":
				p.Suggestions = append(p.Suggestions, suggestion{Post: q, Added: matchSuggestion(q.Ref, es)})
			}
		}
	}
	p.List = newList(r, path, shown, counts)
	p.List.T = t
	s.render(w, r, http.StatusOK, "share", view{Title: sh.Title, Base: path + "/e/", Home: path, Data: p,
		Pane: pane(r, false, path+"/e/", p.List, func(e *Entry) *entryPage { return s.sharePane(ix, sh, m, token, e) })})
}

func (s *Service) memberEntry(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sh, m := s.store.member(token)
	if sh == nil {
		s.notFound(w, r)
		return
	}
	s.shareEntry(w, r, sh, m, token, Prefix+"s/"+token)
}

func (s *Service) publicEntry(w http.ResponseWriter, r *http.Request) {
	sh := s.store.shareByID(r.PathValue("id"))
	if sh == nil || !sh.Public {
		s.notFound(w, r)
		return
	}
	s.shareEntry(w, r, sh, nil, "", Prefix+"p/"+sh.ID)
}

func (s *Service) shareEntry(w http.ResponseWriter, r *http.Request, sh *Share, m *Member, token, path string) {
	ix := s.ix.Load()
	key := r.PathValue("key")
	if !ix.inTopics(key, sh.Topics) {
		s.notFound(w, r)
		return
	}
	p := s.sharePane(ix, sh, m, token, ix.entries[key])
	p.Done = r.URL.Query().Get("done")
	p.Back = listBack(r, path)
	s.renderEntry(w, r, view{Title: p.Title, Base: path + "/e/", Data: p})
}

// sharePane is an entry as a share's member or the public sees it: only the
// share's topics, and the share's comments for a member.
func (s *Service) sharePane(ix *index, sh *Share, m *Member, token string, e *Entry) *entryPage {
	key := e.Key
	inShare := map[string]bool{}
	for _, t := range sh.Topics {
		for _, ck := range ix.subtree(t) {
			inShare[ck] = true
		}
	}
	p := &entryPage{Entry: e, Token: token,
		Topics: ix.topicPaths(e, func(ck string) bool { return inShare[ck] })}
	// A topic path shows only the part inside the share.
	for i, tp := range p.Topics {
		for j, c := range tp {
			if inShare[c.Key] {
				p.Topics[i] = tp[j:]
				break
			}
		}
	}
	if m != nil {
		var posts []*Post
		for _, q := range s.store.visiblePosts(sh.ID) {
			if q.Type == "comment" && q.Entry == key {
				posts = append(posts, q)
			}
		}
		p.Threads = []thread{{Share: sh, Posts: posts}}
	}
	return p
}

// back returns to the page a member posted from, with a message.
// A form in the right pane returns to the list page it was shown in.
func back(w http.ResponseWriter, r *http.Request, token, entry, key, msg string) {
	path := Prefix + "s/" + token
	if b := listBack(r, path); b != "" {
		if u, err := url.Parse(b); err == nil {
			q := u.Query()
			q.Del("done")
			q.Del("error")
			q.Set(key, msg)
			u.RawQuery = q.Encode()
			http.Redirect(w, r, u.String(), http.StatusSeeOther)
			return
		}
	}
	if entry != "" {
		path += "/e/" + entry
	}
	http.Redirect(w, r, path+"?"+key+"="+url.QueryEscape(msg)+"#comments", http.StatusSeeOther)
}

func (s *Service) comment(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sh, m := s.store.member(token)
	if sh == nil {
		s.notFound(w, r)
		return
	}
	entry := r.FormValue("entry")
	if entry != "" && !s.ix.Load().inTopics(entry, sh.Topics) {
		s.notFound(w, r)
		return
	}
	text := strings.TrimSpace(r.FormValue("text"))
	if text == "" || utf8.RuneCountInString(text) > maxText {
		back(w, r, token, entry, "error", fmt.Sprintf("A comment is 1 to %d characters.", maxText))
		return
	}
	s.post(w, r, sh, token, entry, Post{By: m.Name, Type: "comment", Entry: entry, Text: text}, "Comment posted.")
}

func (s *Service) suggest(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sh, m := s.store.member(token)
	if sh == nil {
		s.notFound(w, r)
		return
	}
	ref := strings.TrimSpace(r.FormValue("ref"))
	note := strings.TrimSpace(r.FormValue("note"))
	if ref == "" || utf8.RuneCountInString(ref) > maxText || utf8.RuneCountInString(note) > maxText {
		back(w, r, token, "", "error", fmt.Sprintf("A suggestion needs a DOI, arXiv ID, URL, or BibTeX record, and at most %d characters each.", maxText))
		return
	}
	s.post(w, r, sh, token, "", Post{By: m.Name, Type: "suggest", Ref: ref, Note: note}, "Suggestion added.")
}

func (s *Service) post(w http.ResponseWriter, r *http.Request, sh *Share, token, entry string, p Post, done string) {
	if _, err := s.store.appendPost(sh.ID, p); err != nil {
		if errors.Is(err, errLimit) {
			back(w, r, token, entry, "error", fmt.Sprintf("You have posted %d times today, which is the limit. Please try again tomorrow.", postsPerDay))
			return
		}
		s.log.Printf("library: post to share %s: %v", sh.ID, err)
		back(w, r, token, entry, "error", "Your post could not be saved. Please try again.")
		return
	}
	back(w, r, token, entry, "done", done)
}

var (
	doiRE   = regexp.MustCompile(`(?i)\b(10\.\d{4,9}/[^\s"'<>{},]+)`)
	arxivRE = regexp.MustCompile(`(?i)\b(\d{4}\.\d{4,5})(v\d+)?\b`)
)

// matchSuggestion returns the entry among es that a suggestion's reference
// names, by DOI, arXiv ID, or URL, or nil.
func matchSuggestion(ref string, es []*Entry) *Entry {
	doi := strings.TrimRight(strings.ToLower(doiRE.FindString(ref)), ".")
	arxiv := ""
	if m := arxivRE.FindStringSubmatch(ref); m != nil {
		arxiv = m[1]
	}
	u := normURL(ref)
	for _, e := range es {
		switch {
		case doi != "" && strings.ToLower(e.DOI) == doi:
			return e
		case arxiv != "" && (strings.Contains(e.URL, arxiv) || strings.Contains(e.DOI, arxiv) || otherHas(e, arxiv)):
			return e
		case u != "" && e.URL != "" && normURL(e.URL) == u:
			return e
		}
	}
	return nil
}

func otherHas(e *Entry, s string) bool {
	for _, kv := range e.Other {
		if strings.Contains(kv[1], s) {
			return true
		}
	}
	return false
}

// normURL returns a URL without scheme, www., and trailing slash, or "" if s
// is not a URL.
func normURL(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return ""
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.") + strings.TrimSuffix(u.EscapedPath(), "/") +
		map[bool]string{true: "?" + u.RawQuery}[u.RawQuery != ""]
}

var urlRE = regexp.MustCompile(`https?://[^\s<>"']+[^\s<>"'.,;:!?)\]]`)

// linkify escapes s and turns URLs in it into links.
func linkify(s string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range urlRE.FindAllStringIndex(s, -1) {
		b.WriteString(template.HTMLEscapeString(s[last:m[0]]))
		u := template.HTMLEscapeString(s[m[0]:m[1]])
		b.WriteString(`<a href="` + u + `" rel="nofollow noopener noreferrer" target="_blank">` + u + `</a>`)
		last = m[1]
	}
	b.WriteString(template.HTMLEscapeString(s[last:]))
	return template.HTML(b.String())
}

// ago is a coarse "12 min ago".
func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}
