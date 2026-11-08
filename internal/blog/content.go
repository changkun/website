// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bytes"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// A Page is a post, an idea or a page of its own, such as about, read from
// a markdown file of the content folder.
type Page struct {
	Section  string // "posts", "ideas", or "" for a page of its own
	File     string // the markdown file, relative to the content folder
	Bundle   string // the folder of a page bundle, relative to the content folder, or ""
	BaseName string // the bundle's folder name, or the file's name without .md
	Path     string // the address below the blog, such as /posts/x/

	Title, TitleZh string
	Date, Lastmod  time.Time
	Tags           []string
	Aliases        []string
	HasTOC         bool     // the front matter sets toc, to any value
	Scripts        []string // files of the bundle to load as scripts

	Content     template.HTML
	TOC         template.HTML // the table of contents, empty without h2 or h3
	Summary     template.HTML
	Truncated   bool // the summary is not the whole content
	Plain       string
	ReadingTime int  // in minutes
	HasMath     bool // the content has a $ outside code

	// Files are the bundle's other files, by their path in the bundle.
	Files map[string]string

	// links are the addresses below the blog of the files that the
	// markdown may name by a relative path, by that path.
	links map[string]string

	prev, next *Page  // in the section, older and newer
	body       []byte // the markdown, until it is rendered
}

// IsPost reports whether p is a post.
func (p *Page) IsPost() bool { return p.Section == "posts" }

// IsIdea reports whether p is an idea.
func (p *Page) IsIdea() bool { return p.Section == "ideas" }

// Dated reports whether p shows its date: posts and ideas do.
func (p *Page) Dated() bool { return p.IsPost() || p.IsIdea() }

// Newer is the next newer page of p's section, or nil.
func (p *Page) Newer() *Page { return p.next }

// Older is the next older page of p's section, or nil.
func (p *Page) Older() *Page { return p.prev }

// frontMatter is the YAML that opens a markdown file between --- lines.
type frontMatter struct {
	Title   string   `yaml:"title"`
	TitleZh string   `yaml:"title_zh"`
	Date    any      `yaml:"date"`
	Lastmod any      `yaml:"lastmod"`
	Slug    string   `yaml:"slug"`
	Tags    []string `yaml:"tags"`
	Aliases []string `yaml:"aliases"`
	TOC     any      `yaml:"toc"`
	Draft   bool     `yaml:"draft"`
	Scripts []string `yaml:"scripts"`
}

// splitFrontMatter returns the front matter and body of src. A file that
// does not open with --- has no front matter.
func splitFrontMatter(src []byte) (fm, body []byte) {
	src = bytes.TrimPrefix(src, []byte("\xef\xbb\xbf"))
	rest, ok := bytes.CutPrefix(src, []byte("---"))
	if !ok {
		return nil, src
	}
	nl := bytes.IndexByte(rest, '\n')
	if nl < 0 || len(bytes.TrimSpace(rest[:nl])) != 0 {
		return nil, src
	}
	rest = rest[nl+1:]
	for i := 0; i < len(rest); {
		end := bytes.IndexByte(rest[i:], '\n')
		line := rest[i:]
		if end >= 0 {
			line = rest[i : i+end]
		}
		if bytes.Equal(bytes.TrimRight(line, " \t\r"), []byte("---")) {
			if end < 0 {
				return rest[:i], nil
			}
			return rest[:i], rest[i+end+1:]
		}
		if end < 0 {
			break
		}
		i += end + 1
	}
	return nil, src
}

// parseTime reads a front matter date. One without a zone is in UTC.
func parseTime(v any) (time.Time, error) {
	switch v := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return v, nil
	case string:
		for _, layout := range []string{
			time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05",
			"2006-01-02T15:04", "2006-01-02 15:04", "2006-01-02",
		} {
			if t, err := time.Parse(layout, strings.TrimSpace(v)); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("cannot read the date %v", v)
}

// load reads the pages of the content folder dir. It skips drafts, the
// _todo notes, the sections' own _index.md, and the tags page, which the
// tag cloud takes the place of. A page it cannot read is logged and left
// out.
func load(dir string, l *log.Logger) (pages []*Page, ideasIntro *Page, err error) {
	root := os.DirFS(dir)
	err = fs.WalkDir(root, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name == "_todo" || strings.HasPrefix(d.Name(), ".") && name != "." {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(name) != ".md" {
			return nil
		}
		top, _, _ := strings.Cut(name, "/")
		switch {
		case name == "ideas/_index.md":
		case top == "tags", strings.HasSuffix(name, "/_index.md"):
			return nil
		case top != "posts" && top != "ideas" && path.Base(name) != "index.md":
			// Markdown files that belong to a bundle, or stray ones.
			return nil
		}
		src, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		p, err := newPage(root, name, src)
		if err != nil {
			l.Printf("blog: %s: %v; left out", name, err)
			return nil
		}
		if p == nil {
			return nil
		}
		if name == "ideas/_index.md" {
			ideasIntro = p
			return nil
		}
		pages = append(pages, p)
		return nil
	})
	return pages, ideasIntro, err
}

// newPage reads the page in file name of root, or returns nil for a draft.
// Its content is rendered later.
func newPage(root fs.FS, name string, src []byte) (*Page, error) {
	fmSrc, body := splitFrontMatter(src)
	var fm frontMatter
	if fmSrc != nil {
		if err := yaml.Unmarshal(fmSrc, &fm); err != nil {
			return nil, err
		}
	}
	if fm.Draft {
		return nil, nil
	}
	p := &Page{
		File:    name,
		Title:   fm.Title,
		TitleZh: fm.TitleZh,
		Tags:    fm.Tags,
		Aliases: fm.Aliases,
		HasTOC:  fm.TOC != nil,
		Scripts: fm.Scripts,
	}
	var err error
	if p.Date, err = parseTime(fm.Date); err != nil {
		return nil, err
	}
	if p.Lastmod, err = parseTime(fm.Lastmod); err != nil {
		return nil, err
	}
	if p.Lastmod.IsZero() {
		p.Lastmod = p.Date
	}
	top, _, _ := strings.Cut(name, "/")
	if top == "posts" || top == "ideas" {
		p.Section = top
	}
	p.BaseName = strings.TrimSuffix(path.Base(name), ".md")
	if path.Base(name) == "index.md" {
		p.Bundle = path.Dir(name)
		p.BaseName = path.Base(p.Bundle)
	}
	switch {
	case name == "ideas/_index.md":
		p.Path = "/ideas/"
	case p.Section == "posts":
		if !strings.HasPrefix(fm.Slug, "/posts/") {
			return nil, fmt.Errorf("slug %q does not start with /posts/", fm.Slug)
		}
		p.Path = strings.TrimSuffix(fm.Slug, "/") + "/"
	case p.Section == "ideas":
		slug := fm.Slug
		if slug == "" {
			slug = urlize(p.Title)
		}
		p.Path = "/ideas/" + strings.Trim(slug, "/") + "/"
	default:
		p.Path = "/" + p.Bundle + "/"
	}
	if p.Bundle != "" {
		p.Files = make(map[string]string)
		p.links = make(map[string]string)
		err := fs.WalkDir(root, p.Bundle, func(f string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || f == name || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel := strings.TrimPrefix(f, p.Bundle+"/")
			if rel == "figs.py" {
				return nil // the script that draws the figures, not published
			}
			p.Files[rel] = f
			p.links[rel] = p.Path + (&url.URL{Path: rel}).EscapedPath()
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	p.body = body
	return p, nil
}

// sortPages sorts pages as Hugo does by default: newest first, then by
// title, then by file.
func sortPages(pages []*Page) {
	slices.SortStableFunc(pages, func(a, b *Page) int {
		if c := b.Date.Compare(a.Date); c != 0 {
			return c
		}
		if c := strings.Compare(a.Title, b.Title); c != 0 {
			return c
		}
		return strings.Compare(a.File, b.File)
	})
}

// fileOf is the path on disk of a file of the content folder dir.
func fileOf(dir, name string) string {
	return filepath.Join(dir, filepath.FromSlash(name))
}
