// Copyright 2022 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package research serves changkun.de/research from a checkout of
// github.com/changkun/research. It renders the page from the checkout's
// README, again whenever the README changes, so a git pull publishes it. It
// finds a talk at talks/<name>.pdf, the address changkun.de/talk/<name>.pdf
// redirects to, in the dated folder that holds it. The rest of the checkout,
// such as the papers, is served as files.
package research

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

//go:embed assets
var assetsFS embed.FS

// assets are the page's stylesheet and portrait, served below prefix/assets/.
var assets = func() fs.FS {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}()

// Handler serves the research page at prefix, from dir, a checkout of
// changkun/research, and passes the other requests below prefix to files,
// which serves dir at prefix. Without a README.md in dir, every request goes
// to files.
func Handler(dir, prefix string, files http.Handler, l *log.Logger) http.Handler {
	return &handler{
		dir:    dir,
		prefix: prefix,
		files:  files,
		assets: http.StripPrefix(prefix+"assets/", http.FileServerFS(assets)),
		log:    l,
	}
}

type handler struct {
	dir, prefix string
	files       http.Handler
	assets      http.Handler
	log         *log.Logger

	mu       sync.Mutex
	rendered string // what the last render was from
	page     []byte // the last page that rendered
	etag     string // the last page's ETag
	err      error  // why the last render failed, if it did
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	readme, err := os.Stat(filepath.Join(h.dir, "README.md"))
	if err != nil {
		h.files.ServeHTTP(w, r)
		return
	}
	p := r.URL.Path
	switch {
	case p == h.prefix:
		h.servePage(w, r, readme)
	case p == h.prefix+"index.html":
		http.Redirect(w, r, "./", http.StatusMovedPermanently)
	case strings.HasPrefix(p, h.prefix+"assets/") && isAsset(strings.TrimPrefix(p, h.prefix+"assets/")):
		h.assets.ServeHTTP(w, r)
	case strings.HasPrefix(p, h.prefix+"talks/"):
		h.serveTalk(w, r)
	default:
		h.files.ServeHTTP(w, r)
	}
}

func isAsset(name string) bool {
	fi, err := fs.Stat(assets, name)
	return err == nil && !fi.IsDir()
}

func (h *handler) servePage(w http.ResponseWriter, r *http.Request, readme fs.FileInfo) {
	b, etag, err := h.render(readme)
	if err != nil {
		http.Error(w, "the research page cannot be rendered", http.StatusInternalServerError)
		return
	}
	// An ETag rather than a modification time, since the page also changes
	// with the template.
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(b))
}

// render returns the page, rendered again if the README, the checked-out
// commit, or the year changed since. If rendering fails, it logs why and
// returns the last page that rendered, or the error if none has.
func (h *handler) render(readme fs.FileInfo) ([]byte, string, error) {
	commit := headCommit(filepath.Join(h.dir, ".git"))
	from := fmt.Sprint(readme.ModTime().UnixNano(), readme.Size(), commit, time.Now().Year())

	h.mu.Lock()
	defer h.mu.Unlock()
	if from != h.rendered {
		h.rendered = from // a README that fails is not rendered again until it changes
		src, err := os.ReadFile(filepath.Join(h.dir, "README.md"))
		var b bytes.Buffer
		if err == nil {
			err = renderPage(&b, src, readme.ModTime(), commit)
		}
		if err != nil {
			h.log.Printf("research: cannot render the page, the last one stays: %v", err)
			h.err = err
		} else {
			sum := sha256.Sum256(b.Bytes())
			h.page, h.etag, h.err = b.Bytes(), fmt.Sprintf(`"%x"`, sum[:8]), nil
		}
	}
	if h.page == nil {
		return nil, "", h.err
	}
	return h.page, h.etag, nil
}

// serveTalk serves talks/<name> from the dated folder that holds it, when no
// file of that name is at the top of talks/.
func (h *handler) serveTalk(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, h.prefix+"talks/")
	if name == "" || strings.ContainsAny(name, `/\*?[`) {
		h.files.ServeHTTP(w, r)
		return
	}
	if _, err := os.Lstat(filepath.Join(h.dir, "talks", name)); err == nil {
		h.files.ServeHTTP(w, r)
		return
	}
	found, _ := filepath.Glob(filepath.Join(h.dir, "talks", "*", name))
	if len(found) == 0 {
		h.files.ServeHTTP(w, r)
		return
	}
	if len(found) > 1 {
		h.log.Printf("research: talks %q share the address talks/%s, serving the first", found, name)
	}
	rel, err := filepath.Rel(h.dir, found[0])
	if err != nil {
		h.files.ServeHTTP(w, r)
		return
	}
	r = r.Clone(r.Context())
	r.URL.Path, r.URL.RawPath = h.prefix+filepath.ToSlash(rel), ""
	h.files.ServeHTTP(w, r)
}

// headCommit returns the short hash of the commit checked out in gitDir, or
// an empty string if it cannot tell.
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
	if len(hash) < 7 {
		return ""
	}
	return hash[:7]
}
