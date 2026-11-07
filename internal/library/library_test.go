// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeZotero is an in-process Zotero Web API for one user library.
type fakeZotero struct {
	mu          sync.Mutex
	version     int
	collections map[string]fakeObj
	items       map[string]fakeObj
	deleted     map[string]int // key to the version it was deleted at
	requests    int
	status      int // when set, every request answers it
}

type fakeObj struct {
	version int
	json    string // the API's JSON for the object
}

func newFakeZotero() *fakeZotero {
	return &fakeZotero{collections: map[string]fakeObj{}, items: map[string]fakeObj{}, deleted: map[string]int{}}
}

func (f *fakeZotero) addCollection(key, name, parent string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version++
	p := `false`
	if parent != "" {
		p = strconv.Quote(parent)
	}
	f.collections[key] = fakeObj{f.version, fmt.Sprintf(`{"key":%q,"version":%d,"data":{"key":%q,"name":%q,"parentCollection":%s}}`,
		key, f.version, key, name, p)}
}

func (f *fakeZotero) addItem(key, title, year string, collections []string, extra string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version++
	cols, _ := json.Marshal(collections)
	data := fmt.Sprintf(`{"key":%q,"itemType":"conferencePaper","title":%q,"date":%q,"creators":[{"creatorType":"author","firstName":"Ada","lastName":"Lovelace"},{"creatorType":"author","firstName":"Alan","lastName":"Turing"}],"proceedingsTitle":"OSDI","collections":%s,"tags":[{"tag":"systems"}]%s}`,
		key, title, year, cols, extra)
	bib := fmt.Sprintf("@inproceedings{lovelace_%s,\n\ttitle = {%s},\n\tyear = {%s}\n}", year, title, year)
	b, _ := json.Marshal(bib)
	f.items[key] = fakeObj{f.version, fmt.Sprintf(`{"key":%q,"version":%d,"data":%s,"bibtex":%s}`, key, f.version, data, b)}
}

func (f *fakeZotero) deleteItem(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.version++
	delete(f.items, key)
	f.deleted[key] = f.version
}

func (f *fakeZotero) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if f.status != 0 {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(f.status)
		return
	}
	if r.Header.Get("Zotero-API-Key") != "key" {
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))
	start, _ := strconv.Atoi(r.URL.Query().Get("start"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	w.Header().Set("Last-Modified-Version", strconv.Itoa(f.version))

	var objs map[string]fakeObj
	switch r.URL.Path {
	case "/users/1/collections":
		objs = f.collections
	case "/users/1/items/top":
		objs = f.items
	case "/users/1/deleted":
		var d deletedJSON
		for k, v := range f.deleted {
			if v > since {
				d.Items = append(d.Items, k)
			}
		}
		json.NewEncoder(w).Encode(d)
		return
	default:
		http.NotFound(w, r)
		return
	}
	var out []string
	for _, o := range objs {
		if o.version > since {
			out = append(out, o.json)
		}
	}
	w.Header().Set("Total-Results", strconv.Itoa(len(out)))
	end := min(start+limit, len(out))
	if start > len(out) {
		start = len(out)
	}
	io.WriteString(w, "["+strings.Join(out[start:end], ",")+"]")
}

// newTestService returns a service backed by a fake Zotero with two topics,
// a subtopic, and three items, synced once.
func newTestService(t *testing.T) (*Service, *fakeZotero) {
	t.Helper()
	z := newFakeZotero()
	z.addCollection("SYS", "Systems", "")
	z.addCollection("GPU", "GPU scheduling", "SYS")
	z.addCollection("HCI", "HCI", "")
	z.addItem("AAAA", "Pollux: Co-adaptive Cluster Scheduling", "2021", []string{"GPU"}, `,"DOI":"10.5555/pollux"`)
	z.addItem("BBBB", "Borg", "2015", []string{"SYS", "HCI"}, "")
	z.addItem("CCCC", "Direct Manipulation", "1983", []string{"HCI"}, `,"url":"https://arxiv.org/abs/2101.00001"`)
	srv := httptest.NewServer(z)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv("ZOTERO_USER_ID", "1")
	t.Setenv("ZOTERO_API_KEY", "key")
	t.Setenv("ZOTERO_API_URL", srv.URL)
	t.Setenv("LIBRARY_DIR", dir)
	t.Setenv("LIBRARY_DEV_OWNER", "")
	t.Setenv("AUTH_ALLOWED_PRINCIPALS", "")
	s, err := New(log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	return s, z
}

func TestNewUnconfigured(t *testing.T) {
	t.Setenv("ZOTERO_USER_ID", "")
	t.Setenv("ZOTERO_API_KEY", "")
	s, err := New(log.New(io.Discard, "", 0))
	if s != nil || err != nil {
		t.Fatalf("New() = %v, %v; want nil, nil", s, err)
	}
	rec := httptest.NewRecorder()
	Handler(nil).ServeHTTP(rec, httptest.NewRequest("GET", "/library/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestSync(t *testing.T) {
	s, z := newTestService(t)
	ix := s.ix.Load()
	if len(ix.entries) != 3 || len(ix.cache.Collections) != 3 {
		t.Fatalf("got %d entries and %d topics, want 3 and 3", len(ix.entries), len(ix.cache.Collections))
	}
	// Systems includes its subtopic; Borg is in two topics but listed once.
	if es := ix.entriesIn([]string{"SYS", "HCI"}); len(es) != 3 {
		t.Fatalf("entriesIn(SYS, HCI) = %d entries, want 3", len(es))
	}
	if es := ix.entriesIn([]string{"SYS"}); len(es) != 2 || es[0].Key != "AAAA" {
		t.Fatalf("entriesIn(SYS) = %v, want Pollux first", es)
	}

	// An incremental sync applies changes, trash, and deletions.
	z.addItem("DDDD", "New paper", "2024", []string{"HCI"}, "")
	z.addItem("BBBB", "Borg", "2015", []string{"SYS"}, `,"deleted":1`)
	z.deleteItem("CCCC")
	before := z.requests
	if err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	ix = s.ix.Load()
	if ix.entries["DDDD"] == nil || ix.entries["BBBB"] != nil || ix.entries["CCCC"] != nil {
		t.Fatalf("after sync: entries %v", keys(ix.entries))
	}
	if n := z.requests - before; n != 3 {
		t.Fatalf("incremental sync made %d requests, want 3", n)
	}

	// The cache survives a restart.
	s2, err := New(log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if got := s2.ix.Load().cache.Version; got != z.version {
		t.Fatalf("cache version after restart = %d, want %d", got, z.version)
	}
}

func TestSyncFailureKeepsServing(t *testing.T) {
	s, z := newTestService(t)
	z.status = http.StatusTooManyRequests
	err := s.Sync(context.Background())
	if err == nil {
		t.Fatal("Sync() = nil, want an error")
	}
	if len(s.ix.Load().entries) != 3 {
		t.Fatal("the index changed after a failed sync")
	}
	_, msg, _ := s.status()
	if msg == "" {
		t.Fatal("status has no error")
	}
	// Zotero asked to wait, so the next sync does not even ask.
	before := z.requests
	if err := s.Sync(context.Background()); err == nil || z.requests != before {
		t.Fatalf("Sync() during backoff = %v after %d requests, want an error and none", err, z.requests-before)
	}
}

func TestPaging(t *testing.T) {
	z := newFakeZotero()
	for i := range 250 {
		z.addItem(fmt.Sprintf("K%04d", i), fmt.Sprintf("Paper %d", i), "2020", nil, "")
	}
	srv := httptest.NewServer(z)
	defer srv.Close()
	c := &zoteroClient{baseURL: srv.URL, userID: "1", key: "key"}
	ch, err := c.fetch(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.items) != 250 {
		t.Fatalf("fetched %d items, want 250", len(ch.items))
	}
}

func keys[V any](m map[string]V) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func get(t *testing.T, h http.Handler, path string, owner bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if owner {
		req.RemoteAddr = "127.0.0.1:1234"
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, path string, form url.Values, owner bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if owner {
		req.RemoteAddr = "127.0.0.1:1234"
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestOwnerPages(t *testing.T) {
	s, _ := newTestService(t)
	h := s.routes()

	// Without login, owner pages redirect to the login page.
	if rec := get(t, h, "/library/", false); rec.Code != http.StatusSeeOther ||
		!strings.HasPrefix(rec.Header().Get("Location"), "/library/login") {
		t.Fatalf("anonymous GET /library/ = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := post(t, h, "/library/sync", nil, false); rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous POST /library/sync = %d, want 403", rec.Code)
	}

	s.owner.dev = true
	for path, want := range map[string]string{
		"/library/":                   "Pollux",
		"/library/?q=borg":            "Borg",
		"/library/t/SYS":              "GPU scheduling",
		"/library/e/AAAA":             "10.5555/pollux",
		"/library/all.bib":            "@inproceedings",
		"/library/t/GPU.bib":          "Pollux",
		"/library/inbox":              "Nothing yet",
		"/library/shares":             "No shares yet",
		"/library/healthz":            "ok",
		"/library/assets/library.css": "--navy",
	} {
		rec := get(t, h, path, true)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("GET %s = %d, want 200 with %q:\n%.400s", path, rec.Code, want, rec.Body.String())
		}
	}
	if body := get(t, h, "/library/?q=borg", true).Body.String(); strings.Contains(body, "Pollux") {
		t.Error("the filter did not filter")
	}
	if rec := get(t, h, "/library/e/NOPE", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown entry = %d, want 404", rec.Code)
	}

	// Through a proxy, loopback is not the owner.
	req := httptest.NewRequest("GET", "/library/", nil)
	req.RemoteAddr = "127.0.0.1:1"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("proxied loopback request = %d, want a redirect to login", rec.Code)
	}
}

func TestSessionCookie(t *testing.T) {
	s, _ := newTestService(t)
	exp := "99999999999"
	good := &http.Cookie{Name: cookieName, Value: exp + "." + s.owner.sign(exp)}
	bad := &http.Cookie{Name: cookieName, Value: exp + "." + strings.Repeat("0", 64)}
	expired := &http.Cookie{Name: cookieName, Value: "1." + s.owner.sign("1")}
	for _, tc := range []struct {
		c    *http.Cookie
		want bool
	}{{good, true}, {bad, false}, {expired, false}} {
		r := httptest.NewRequest("GET", "/library/", nil)
		r.AddCookie(tc.c)
		if got := s.owner.is(r); got != tc.want {
			t.Errorf("owner.is(%s) = %v, want %v", tc.c.Value, got, tc.want)
		}
	}
	// Without an allowlist, login is refused.
	rec := httptest.NewRecorder()
	s.owner.login(rec, httptest.NewRequest("POST", "/library/login", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login without allowlist = %d, want 403", rec.Code)
	}
}

func TestShares(t *testing.T) {
	s, _ := newTestService(t)
	h := s.routes()
	s.owner.dev = true

	rec := post(t, h, "/library/shares", url.Values{"topic": {"SYS"}, "title": {"Reading group"},
		"names": {"Alice\nBob\n"}, "public": {"on"}}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create share = %d: %s", rec.Code, rec.Body)
	}
	sh := s.store.allShares()[0]
	if len(sh.Members) != 2 || !sh.Public || sh.Topics[0] != "SYS" || len(sh.Members[0].Token) != 26 {
		t.Fatalf("share = %+v", sh)
	}
	alice, bob := sh.Members[0].Token, sh.Members[1].Token

	// A member sees the share's topics, and nothing else.
	body := get(t, h, "/library/s/"+alice, false).Body.String()
	if !strings.Contains(body, "Pollux") || !strings.Contains(body, "Borg") || strings.Contains(body, "Direct Manipulation") {
		t.Fatalf("member share page:\n%s", body)
	}
	if rec := get(t, h, "/library/s/"+alice+"/e/CCCC", false); rec.Code != http.StatusNotFound {
		t.Fatalf("entry outside the share = %d, want 404", rec.Code)
	}
	// Borg is also in HCI, which is not shared: the page must not name it.
	if body := get(t, h, "/library/s/"+alice+"/e/BBBB", false).Body.String(); strings.Contains(body, "HCI") {
		t.Fatal("the entry page leaks a topic outside the share")
	}
	if rec := get(t, h, "/library/s/"+alice, false); rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("no Referrer-Policy on a share page")
	}
	if body := get(t, h, "/library/s/"+alice+".bib", false).Body.String(); !strings.Contains(body, "Pollux") || strings.Contains(body, "Direct") {
		t.Fatalf("share .bib:\n%s", body)
	}

	// Comments and suggestions.
	rec = post(t, h, "/library/s/"+alice+"/comment", url.Values{"entry": {"AAAA"}, "text": {"Read §3, see https://example.com/x."}}, false)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "done=") {
		t.Fatalf("comment = %d %s", rec.Code, rec.Header().Get("Location"))
	}
	post(t, h, "/library/s/"+bob+"/suggest", url.Values{"ref": {"arXiv:2101.00001"}, "note": {"classic"}}, false)
	post(t, h, "/library/s/"+bob+"/comment", url.Values{"entry": {"CCCC"}, "text": {"outside"}}, false)
	body = get(t, h, "/library/s/"+bob+"/e/AAAA", false).Body.String()
	if !strings.Contains(body, "Alice") || !strings.Contains(body, `href="https://example.com/x"`) {
		t.Fatalf("entry comments:\n%s", body)
	}
	posts := s.store.visiblePosts(sh.ID)
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2 (the comment outside the share is refused)", len(posts))
	}

	// The public page has the entries and no comments.
	body = get(t, h, "/library/p/"+sh.ID+"/e/AAAA", false).Body.String()
	if !strings.Contains(body, "Pollux") || strings.Contains(body, "Read §3") {
		t.Fatalf("public entry page:\n%s", body)
	}

	// The owner's inbox lists both, unread; mark one read, hide the other.
	body = get(t, h, "/library/inbox", true).Body.String()
	if !strings.Contains(body, "Inbox <b>(2)</b>") || !strings.Contains(body, "arXiv:2101.00001") {
		t.Fatalf("inbox:\n%s", body)
	}
	post(t, h, "/library/inbox/read", url.Values{"id": {posts[0].ID}}, true)
	post(t, h, "/library/shares/"+sh.ID+"/hide", url.Values{"post": {posts[1].ID}}, true)
	if s.unread() != 0 || len(s.store.visiblePosts(sh.ID)) != 1 {
		t.Fatalf("unread = %d, visible = %d; want 0 and 1", s.unread(), len(s.store.visiblePosts(sh.ID)))
	}

	// Revoking Bob leaves Alice working.
	post(t, h, "/library/shares/"+sh.ID+"/revoke", url.Values{"token": {bob}}, true)
	if get(t, h, "/library/s/"+bob, false).Code != http.StatusNotFound || get(t, h, "/library/s/"+alice, false).Code != http.StatusOK {
		t.Fatal("revoke did not revoke exactly one member")
	}

	// Not public any more: the public page is gone.
	post(t, h, "/library/shares/"+sh.ID, url.Values{"title": {"Reading group"}}, true)
	if get(t, h, "/library/p/"+sh.ID, false).Code != http.StatusNotFound {
		t.Fatal("public page still served")
	}

	// State survives a restart.
	st, err := openStore(filepath.Join(s.dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if len(st.shares) != 1 || len(st.posts[sh.ID]) != 3 || !st.read[posts[0].ID] {
		t.Fatalf("reloaded state: %d shares, %d posts", len(st.shares), len(st.posts[sh.ID]))
	}
	raw, _ := os.ReadFile(filepath.Join(s.dir, "state", "shares.yaml"))
	if !strings.Contains(string(raw), "revoked:") {
		t.Fatalf("shares.yaml:\n%s", raw)
	}
}

func TestLimits(t *testing.T) {
	s, _ := newTestService(t)
	h := s.routes()
	sh, err := s.store.createShare("x", []string{"SYS"}, []string{"Eve"}, false)
	if err != nil {
		t.Fatal(err)
	}
	tok := sh.Members[0].Token
	long := strings.Repeat("a", maxText+1)
	if rec := post(t, h, "/library/s/"+tok+"/comment", url.Values{"text": {long}}, false); !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Fatal("an overlong comment was accepted")
	}
	for range postsPerDay {
		post(t, h, "/library/s/"+tok+"/comment", url.Values{"text": {"hi"}}, false)
	}
	rec := post(t, h, "/library/s/"+tok+"/comment", url.Values{"text": {"one more"}}, false)
	if !strings.Contains(rec.Header().Get("Location"), "error=") || len(s.store.visiblePosts(sh.ID)) != postsPerDay {
		t.Fatalf("post %d was accepted", postsPerDay+1)
	}
	if rec := get(t, h, "/library/s/nonsense", false); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token = %d, want 404", rec.Code)
	}
}

func TestSuggestionMatch(t *testing.T) {
	s, _ := newTestService(t)
	es := s.ix.Load().sorted
	for ref, want := range map[string]string{
		"https://doi.org/10.5555/POLLUX":        "AAAA",
		"arXiv:2101.00001v2":                    "CCCC",
		"https://www.arxiv.org/abs/2101.00001/": "CCCC",
		"some other paper":                      "",
	} {
		got := ""
		if e := matchSuggestion(ref, es); e != nil {
			got = e.Key
		}
		if got != want {
			t.Errorf("matchSuggestion(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestBibtexDuplicateKeys(t *testing.T) {
	es := []*Entry{
		{BibTeX: "@article{smith_2020,\n title={A}}"},
		{BibTeX: "@article{smith_2020,\n title={B}}"},
		{BibTeX: "@article{smith_2020,\n title={C}}"},
	}
	got := bibtexFile(es)
	for _, k := range []string{"{smith_2020,", "{smith_2020a,", "{smith_2020b,"} {
		if !strings.Contains(got, k) {
			t.Errorf("bibtexFile lacks %s:\n%s", k, got)
		}
	}
}

func TestTitleMarkup(t *testing.T) {
	it := &Item{Key: "K", Data: json.RawMessage(`{"title":"<span style=\"font-variant:small-caps;\">ReplicatorBench:</span> <i>x</i> < y"}`)}
	e, err := parseEntry(it)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ReplicatorBench: x < y"; e.Title != want {
		t.Fatalf("title = %q, want %q", e.Title, want)
	}
}
