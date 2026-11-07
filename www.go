// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"changkun.de/x/website/internal/research"
)

// withWWW serves the folders of WWW_DIR in front of next, each at /<name>/,
// as WWW_URLSTAT and WWW_CORS configure. Publishing a folder takes a copy or
// a clone into WWW_DIR, and no restart. The research folder, a checkout of
// changkun/research, also has its page rendered; see package research.
// Without WWW_DIR, it returns next.
func withWWW(l *log.Logger, next http.Handler) (http.Handler, error) {
	dir := os.Getenv("WWW_DIR")
	if dir == "" {
		return next, nil
	}
	counted, err := parseCounted(os.Getenv("WWW_URLSTAT"))
	if err != nil {
		return nil, err
	}
	cors, err := parseCORS(os.Getenv("WWW_CORS"))
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		l.Printf("WWW_DIR %s is not a folder, no folder is served", dir)
		return next, nil
	}
	l.Printf("serving the folders of %s, counting %d, open to other origins %v", dir, len(counted), cors)

	report := func(page, ua string) { go reportUrlstat(l, page, ua) }
	files := http.FileServer(http.Dir(dir))
	return &folders{
		dir:   dir,
		files: files,
		own: map[string]http.Handler{
			"research": research.Handler(filepath.Join(dir, "research"), "/research/", files, l),
		},
		site:  fsys,
		count: counted.counter(report),
		cors:  cors,
		next:  next,
	}, nil
}

// folders serves the folders of dir as the static server it replaced did:
// files, listings of folders without an index.html, and symlinks followed.
// Hidden files are not served, and a name the embedded site has is left to
// next, so a folder cannot shadow the site.
type folders struct {
	dir   string
	files http.Handler            // serves dir
	own   map[string]http.Handler // serve the folders they name, in place of files
	site  fs.FS                   // the embedded site
	count func(*http.Request)
	cors  []string // path prefixes that any origin may fetch
	next  http.Handler
}

func (f *folders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !f.isFolder(name) {
		f.next.ServeHTTP(w, r)
		return
	}
	if hidden(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	f.count(r)
	for _, p := range f.cors {
		// The folder itself too, whose answer redirects to the folder/.
		if strings.HasPrefix(r.URL.Path, p) || r.URL.Path+"/" == p {
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			break
		}
	}
	// Some files are 80 MB. A slow reader takes longer than the server's
	// write timeout, which is set for the API.
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	if h, ok := f.own[name]; ok {
		h.ServeHTTP(w, r)
		return
	}
	f.files.ServeHTTP(w, r)
}

// isFolder reports whether name is a folder of f.dir that the embedded site
// does not also have.
func (f *folders) isFolder(name string) bool {
	if name == "" || strings.HasPrefix(name, ".") {
		return false
	}
	if _, err := fs.Stat(f.site, name); err == nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil && fi.IsDir()
}

// hidden reports whether a segment of p starts with a dot, such as .git.
func hidden(p string) bool {
	for seg := range strings.SplitSeq(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}

// parseCORS parses WWW_CORS, a comma-separated list of path prefixes such as
// /demos/.
func parseCORS(s string) ([]string, error) {
	var prefixes []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if !strings.HasPrefix(p, "/") || !strings.HasSuffix(p, "/") {
			return nil, fmt.Errorf("WWW_CORS must list paths of folders such as /demos/, got %q", p)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}

// countedFolder is a folder whose files urlstat counts: the requests below
// prefix, recorded as pages of origin.
type countedFolder struct{ prefix, origin string }

type countedFolders []countedFolder

// parseCounted parses WWW_URLSTAT, a comma-separated list of the URLs of
// folders, such as https://changkun.de/research/.
func parseCounted(s string) (countedFolders, error) {
	var counted countedFolders
	for f := range strings.SplitSeq(s, ",") {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		u, err := url.Parse(f)
		if err != nil || u.Scheme == "" || u.Host == "" || !strings.HasSuffix(u.Path, "/") {
			return nil, fmt.Errorf("WWW_URLSTAT must list URLs of folders such as https://changkun.de/research/, got %q", f)
		}
		counted = append(counted, countedFolder{prefix: u.Path, origin: u.Scheme + "://" + u.Host})
	}
	return counted, nil
}

// counter returns a func that reports a request below a counted folder to
// urlstat, as the page at the folder's origin. A folder's own page is left
// out, since urlstat's client script counts it in the browser. What this
// counts is what a script cannot: the files a page links to, such as the PDFs
// of changkun.de/research.
func (c countedFolders) counter(report func(page, ua string)) func(*http.Request) {
	return func(r *http.Request) {
		p := r.URL.Path
		for _, f := range c {
			if strings.HasPrefix(p, f.prefix) && p != f.prefix && p != f.prefix+"index.html" {
				report(f.origin+p, r.UserAgent())
			}
		}
	}
}

// urlstatEndpoint receives the reports, as from urlstat's client script.
var (
	urlstatEndpoint = "https://www.changkun.de/urlstat?report=page+site"
	urlstatClient   = &http.Client{Timeout: 10 * time.Second}
)

func reportUrlstat(l *log.Logger, page, ua string) {
	req, err := http.NewRequest(http.MethodGet, urlstatEndpoint, nil)
	if err != nil {
		l.Printf("urlstat: cannot report %s: %v", page, err)
		return
	}
	req.Header.Set("urlstat-url", page)
	req.Header.Set("urlstat-ua", ua)
	resp, err := urlstatClient.Do(req)
	if err != nil {
		l.Printf("urlstat: cannot report %s: %v", page, err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		l.Printf("urlstat: cannot report %s: %s", page, resp.Status)
	}
}
