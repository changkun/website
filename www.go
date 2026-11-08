// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"changkun.de/x/website/internal/blog"
	"changkun.de/x/website/internal/research"
)

// withWWW serves the folders of WWW_DIR in front of next, each at /<name>/,
// as WWW_CORS configures. Publishing a folder takes a copy or
// a clone into WWW_DIR, and no restart. The research folder, a checkout of
// changkun/research, also has its page rendered; see package research. The
// blog folder, a checkout of changkun/blog, has its pages rendered; see
// package blog.
// Without WWW_DIR, it returns next.
func withWWW(l *log.Logger, next http.Handler) (http.Handler, error) {
	dir := os.Getenv("WWW_DIR")
	if dir == "" {
		return next, nil
	}
	cors, err := parseCORS(os.Getenv("WWW_CORS"))
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		l.Printf("WWW_DIR %s is not a folder, no folder is served", dir)
		return next, nil
	}
	l.Printf("serving the folders of %s, open to other origins %v", dir, cors)

	files := http.FileServer(http.Dir(dir))
	return &folders{
		dir:   dir,
		files: files,
		own: map[string]http.Handler{
			"research": research.Handler(filepath.Join(dir, "research"), "/research/", files, l),
			"blog":     blog.Handler(filepath.Join(dir, "blog"), files, l),
		},
		site: fsys,
		cors: cors,
		next: next,
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
	cors  []string                // path prefixes that any origin may fetch
	next  http.Handler
}

func (f *folders) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !f.isFolder(name) {
		f.next.ServeHTTP(w, r)
		return
	}
	for _, p := range f.cors {
		// The folder itself too, whose answer redirects to the folder/.
		if strings.HasPrefix(r.URL.Path, p) || r.URL.Path+"/" == p {
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			break
		}
	}
	h := f.files
	if own, ok := f.own[name]; ok {
		h = own
	}
	serveFiles(w, r, h)
}

// serveFiles answers r with h, which serves files on disk, except for hidden
// files such as .git, which are not served. The response has no write
// deadline: some files are 80 MB, and a slow reader takes longer than the
// server's write timeout, which is set for the API.
func serveFiles(w http.ResponseWriter, r *http.Request, h http.Handler) {
	if hidden(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h.ServeHTTP(w, r)
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

// withSites serves each site WWW_SITES lists from a folder of its own, in
// front of next. A request for the site, or for its www. name, is answered
// from the folder alone, as the folders of WWW_DIR are, so nothing of next,
// neither the embedded site nor the APIs, answers for it. Any other host goes
// to next. Without WWW_SITES, it returns next.
func withSites(l *log.Logger, next http.Handler) (http.Handler, error) {
	dirs, err := parseSites(os.Getenv("WWW_SITES"))
	if err != nil {
		return nil, err
	}
	if len(dirs) == 0 {
		return next, nil
	}
	s := &sites{hosts: make(map[string]http.Handler), next: next}
	for host, dir := range dirs {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			// Still the site's: a missing folder answers 404, rather than
			// changkun.de answering for the site.
			l.Printf("WWW_SITES: %s is not a folder, %s answers 404", dir, host)
		}
		files := http.FileServer(http.Dir(dir))
		s.hosts[host] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serveFiles(w, r, files)
		})
		l.Printf("serving %s from %s", host, dir)
	}
	return s, nil
}

// sites serves the hosts it has, each from its folder, and leaves the rest to
// next.
type sites struct {
	hosts map[string]http.Handler // by siteName
	next  http.Handler
}

func (s *sites) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h, ok := s.hosts[siteName(r.Host)]; ok {
		h.ServeHTTP(w, r)
		return
	}
	s.next.ServeHTTP(w, r)
}

// siteName is host in lower case, without its port and a leading www.
func siteName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}

// parseSites parses WWW_SITES, a comma-separated list of host=folder pairs
// such as golang.design=/www/golang.design, into folders by siteName.
func parseSites(s string) (map[string]string, error) {
	dirs := make(map[string]string)
	for pair := range strings.SplitSeq(s, ",") {
		if pair = strings.TrimSpace(pair); pair == "" {
			continue
		}
		host, dir, ok := strings.Cut(pair, "=")
		host, dir = strings.TrimSpace(host), strings.TrimSpace(dir)
		if !ok || host == "" || strings.ContainsAny(host, "/:") || !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("WWW_SITES must list host=folder pairs such as golang.design=/www/golang.design, got %q", pair)
		}
		if host = siteName(host); host == "" {
			return nil, fmt.Errorf("WWW_SITES names no host in %q", pair)
		}
		if _, dup := dirs[host]; dup {
			return nil, fmt.Errorf("WWW_SITES lists %s twice", host)
		}
		dirs[host] = dir
	}
	return dirs, nil
}
