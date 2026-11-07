// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"changkun.de/x/website/internal/research"
)

// withWWW serves the folders of WWW_DIR in front of next, each at /<name>/,
// as WWW_CORS configures. Publishing a folder takes a copy or
// a clone into WWW_DIR, and no restart. The research folder, a checkout of
// changkun/research, also has its page rendered; see package research.
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
	if hidden(r.URL.Path) {
		http.NotFound(w, r)
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
