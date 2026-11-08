// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"changkun.de/x/website/internal/ideas"
)

// The editor also opens a post or an idea that is there already, at
// Prefix+"edit"+its address, and commits the edit over its file.

// An entry is a post or an idea as the editor opens it.
type entry struct {
	Kind    string   `json:"kind"`   // "post" or "idea"
	Path    string   `json:"path"`   // its address below the blog, such as /posts/x/
	URL     string   `json:"url"`    // its address on the site
	Prefix  string   `json:"prefix"` // the address before its name, such as /blog/posts/
	Name    string   `json:"name"`
	Date    string   `json:"date"`
	Title   string   `json:"title"`
	TitleZh string   `json:"title_zh"`
	Tags    []string `json:"tags"`
	Body    string   `json:"body"`
	Files   []upload `json:"files"` // the page's own, which have no SHA
	Base    string   `json:"base"`  // the git blob SHA of its markdown file as read
}

// The replies to an edit of a page that changed since it was opened.
const (
	changedHere     = "This page changed since you opened it. Copy your changes, reload, and edit again."
	changedUpstream = "This page changed in the blog's repository since you opened it, and saving would undo that. Copy your changes, reload once the server has pulled, and edit again."
)

// editURL is where p is edited.
func editURL(p *Page) string { return Prefix + "edit" + p.Path }

// blobSHA is the SHA git and GitHub name a file of content b by.
func blobSHA(b []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(b))
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))
}

// serveEditable serves the owner a post or an idea with a link to edit it,
// and reports whether it did. The page is rendered for the request and not
// kept, so no one else is served the link.
func (h *handler) serveEditable(w http.ResponseWriter, r *http.Request, s *site, at string) bool {
	p := s.byPath[at]
	if p == nil || !p.Dated() || h.editor == nil || h.editor.Owner == nil || !h.editor.Owner(r) {
		return false
	}
	v := singleView(p)
	v.Edit = editURL(p)
	w.Header().Set("Content-Type", htmlType)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(execute("single", v)))
	return true
}

// editable finds the post or idea r edits, by the address after
// Prefix+"edit", and reads its markdown file. It writes the error and
// reports false if there is none.
func (h *handler) editable(w http.ResponseWriter, r *http.Request) (*Page, []byte, bool) {
	s, err := h.current()
	if err != nil {
		h.log.Printf("blog: %v", err)
		http.Error(w, "the blog is not available", http.StatusServiceUnavailable)
		return nil, nil, false
	}
	p := s.byPath["/"+r.PathValue("path")]
	if p == nil || !p.Dated() {
		http.NotFound(w, r)
		return nil, nil, false
	}
	src, err := os.ReadFile(fileOf(h.content, p.File))
	if err != nil {
		h.log.Printf("blog: %v", err)
		http.Error(w, "the page cannot be read", http.StatusInternalServerError)
		return nil, nil, false
	}
	return p, src, true
}

// editPage opens a post or an idea in the editor. With ?base, it replies
// with the SHA of its file only, which tells the editor when the server
// has pulled an edit.
func (h *handler) editPage(w http.ResponseWriter, r *http.Request) {
	p, src, ok := h.editable(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Has("base") {
		writeJSON(w, http.StatusOK, map[string]string{"base": blobSHA(src)})
		return
	}
	_, body := splitFrontMatter(src)
	u := pagePath(p)
	e := &entry{
		Kind: strings.TrimSuffix(p.Section, "s"), Path: p.Path, URL: u,
		Prefix: path.Dir(strings.TrimSuffix(u, "/")) + "/", Name: path.Base(u),
		Date: p.Date.Format("2006-01-02"), Title: p.Title, TitleZh: p.TitleZh,
		Tags: append([]string{}, p.Tags...), Body: string(bytes.TrimLeft(body, "\n")),
		Files: []upload{}, Base: blobSHA(src),
	}
	for _, name := range slices.Sorted(maps.Keys(p.Files)) {
		f := upload{Name: name}
		if fi, err := os.Stat(fileOf(h.content, p.Files[name])); err == nil {
			f.Size = fi.Size()
		}
		e.Files = append(e.Files, f)
	}
	v := &view{Title: "Edit", URL: origin + editURL(p), MainClass: "page-editor", Math: true,
		CanPublish: h.editor.Publisher != nil, Entry: e, Scripts: scriptURLs(p), Tags: h.knownTags()}
	w.Header().Set("Content-Type", htmlType)
	w.Write(execute("new", v))
}

// save commits an edit of a post or an idea over its file, in one commit
// with the files added to it and without those taken off, and replies with
// where it is and the SHA its file will have.
func (h *handler) save(w http.ResponseWriter, r *http.Request) {
	pub := h.editor.Publisher
	if pub == nil {
		http.Error(w, "publishing is not configured here", http.StatusServiceUnavailable)
		return
	}
	d, ok := readDraft(w, r)
	if !ok {
		return
	}
	p, src, ok := h.editable(w, r)
	if !ok {
		return
	}
	if d.Base != blobSHA(src) {
		http.Error(w, changedHere, http.StatusConflict)
		return
	}
	if strings.TrimSpace(d.Body) == "" {
		http.Error(w, "there is nothing to save", http.StatusBadRequest)
		return
	}
	if d.Title == "" {
		http.Error(w, "the page needs a title", http.StatusBadRequest)
		return
	}

	// The files sent without a SHA are the page's own, which it keeps; those
	// with one are added.
	kept := map[string]bool{}
	var added []upload
	for _, f := range d.Files {
		if f.Name == "" || kept[f.Name] || slices.ContainsFunc(added, func(a upload) bool { return a.Name == f.Name }) {
			http.Error(w, "the file "+f.Name+" cannot be saved", http.StatusBadRequest)
			return
		}
		if f.SHA == "" {
			if _, ok := p.Files[f.Name]; !ok {
				http.Error(w, "the page has no file "+f.Name, http.StatusBadRequest)
				return
			}
			kept[f.Name] = true
			continue
		}
		if fileName(f.Name) != f.Name {
			http.Error(w, "the file "+f.Name+" cannot be saved", http.StatusBadRequest)
			return
		}
		added = append(added, f)
	}

	file := "content/" + p.File
	root := ""
	var files []ideas.File
	var remove []string
	text, err := editedMarkdown(p, src, d, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch {
	case p.Bundle != "":
		root = "content/" + p.Bundle
		files = append(files, ideas.File{Path: file, Text: text})
		for _, name := range slices.Sorted(maps.Keys(p.Files)) {
			if !kept[name] {
				remove = append(remove, root+"/"+name)
			}
		}
	case len(added) > 0:
		// A page of one file that gains files moves into a folder of its
		// name, and keeps its address.
		dir := strings.TrimSuffix(p.File, ".md")
		if _, err := os.Stat(fileOf(h.content, dir)); err == nil {
			http.Error(w, "the files cannot go beside the page: content/"+dir+" is there already", http.StatusConflict)
			return
		}
		root = "content/" + dir
		files = append(files, ideas.File{Path: root + "/index.md", Text: text})
		remove = append(remove, file)
	default:
		files = append(files, ideas.File{Path: file, Text: text})
	}
	for _, f := range added {
		files = append(files, ideas.File{Path: root + "/" + f.Name, SHA: f.SHA})
	}
	if len(added) == 0 && len(remove) == 0 {
		if same, err := editedMarkdown(p, src, d, time.Time{}); err == nil && same == string(src) {
			http.Error(w, "nothing has changed", http.StatusBadRequest)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if err := pub.Update(ctx, file, d.Base, files, remove, p.Section+": "+d.Title); err != nil {
		if errors.Is(err, ideas.ErrChanged) {
			http.Error(w, changedUpstream, http.StatusConflict)
			return
		}
		h.log.Printf("blog: edit %s: %v", file, err)
		http.Error(w, "the edit could not be committed", http.StatusBadGateway)
		return
	}
	h.log.Printf("blog: edited %s", file)
	writeJSON(w, http.StatusOK, map[string]string{"url": pagePath(p), "base": blobSHA([]byte(text)),
		"message": "Saved. The change shows once the server has pulled it, in a minute or so."})
}

// editedMarkdown is the markdown file src of p with the title, Chinese
// title, tags and text of d, and lastmod set to now unless it is zero. The
// rest of the front matter stays as it was, the date and the address too:
// an idea whose address comes from its title keeps it as its slug.
func editedMarkdown(p *Page, src []byte, d draft, now time.Time) (string, error) {
	fmSrc, body := splitFrontMatter(src)
	var doc yaml.Node
	if err := yaml.Unmarshal(fmSrc, &doc); err != nil || fmSrc == nil ||
		len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("the page's front matter cannot be read")
	}
	m := doc.Content[0]
	if p.IsIdea() && lookup(m, "slug") < 0 && urlize(d.Title) != urlize(p.Title) {
		setString(m, "slug", strings.Trim(strings.TrimPrefix(p.Path, "/ideas/"), "/"), "title")
	}
	setString(m, "title", d.Title, "")
	setString(m, "title_zh", d.TitleZh, "title")
	var tags []string
	for _, t := range d.Tags {
		if t = strings.TrimSpace(t); t != "" {
			tags = append(tags, t)
		}
	}
	setStrings(m, "tags", tags)
	if !now.IsZero() {
		// Unquoted, as the dates are.
		put(m, "lastmod", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!timestamp", Value: now.Format(time.RFC3339)}, "date")
	}
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(4) // as the blog's files have it, so the rest stays as it was
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	lead := body[:len(body)-len(bytes.TrimLeft(body, "\n"))]
	text := d.Body
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return "---\n" + b.String() + "---\n" + string(lead) + text, nil
}

// lookup is the index of the value of key in the mapping m, or -1.
func lookup(m *yaml.Node, key string) int {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Kind == yaml.ScalarNode && m.Content[i].Value == key {
			return i + 1
		}
	}
	return -1
}

// put sets key of the mapping m to v. A new key goes after the key after,
// or at the end.
func put(m *yaml.Node, key string, v *yaml.Node, after string) {
	if i := lookup(m, key); i >= 0 {
		m.Content[i] = v
		return
	}
	at := len(m.Content)
	if i := lookup(m, after); i >= 0 {
		at = i + 1
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	m.Content = slices.Insert(m.Content, at, k, v)
}

// remove takes key out of the mapping m.
func remove(m *yaml.Node, key string) {
	if i := lookup(m, key); i >= 0 {
		m.Content = slices.Delete(m.Content, i-1, i+1)
	}
}

// setString sets key of the mapping m to s, quoted as it was, or takes it
// out if s is empty. A value that is s already is left as written.
func setString(m *yaml.Node, key, s, after string) {
	if s == "" {
		remove(m, key)
		return
	}
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
	if i := lookup(m, key); i >= 0 {
		old := m.Content[i]
		if old.Kind == yaml.ScalarNode && old.Value == s {
			return
		}
		n.Style = old.Style & (yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle)
	}
	put(m, key, n, after)
}

// setStrings sets key of the mapping m to the list vals, written as it
// was, or takes it out if vals is empty.
func setStrings(m *yaml.Node, key string, vals []string) {
	if len(vals) == 0 {
		remove(m, key)
		return
	}
	n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	var item yaml.Style
	if i := lookup(m, key); i >= 0 {
		old := m.Content[i]
		if old.Kind == yaml.SequenceNode {
			if slices.EqualFunc(old.Content, vals, func(n *yaml.Node, v string) bool { return n.Value == v }) {
				return
			}
			n.Style = old.Style & yaml.FlowStyle
			if len(old.Content) > 0 {
				item = old.Content[0].Style & (yaml.DoubleQuotedStyle | yaml.SingleQuotedStyle)
			}
		}
	}
	for _, v := range vals {
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v, Style: item})
	}
	put(m, key, n, "")
}

// editedPreview makes p, the preview of d, an edit of the page at address
// at, render as that page: at its address, with its own files where they
// are.
func (h *handler) editedPreview(at string, d draft, p *Page) {
	s, err := h.current()
	if err != nil {
		return
	}
	live := s.byPath[at]
	if live == nil || !live.Dated() {
		return
	}
	p.Section, p.Path, p.Date, p.Lastmod = live.Section, live.Path, live.Date, time.Now()
	for _, f := range d.Files {
		if l, ok := live.links[f.Name]; ok && f.SHA == "" {
			p.links[f.Name] = l
		}
	}
}
