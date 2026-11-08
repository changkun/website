// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"cmp"
	"html"
	"io/fs"
	"log"
	"math"
	"os"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// A site is the blog as one commit of its content folder has it.
type site struct {
	pages   []*Page // posts, ideas and pages of their own, newest first
	posts   []*Page
	ideas   []*Page
	stream  []*Page // posts and ideas, for the home page
	intro   *Page   // the ideas page's introduction
	byPath  map[string]*Page
	aliases map[string]string // old address → the page's
	files   map[string]string // address → file in the content folder
	tags    []*tag            // by key
	tagsBy  map[string]*tag   // by key

	mu    sync.Mutex
	cache map[string]*response // rendered pages, by address
}

// A tag is a tag of the blog, with the pages it is on, newest first.
type tag struct {
	Key   string // the tag in its address form, such as c++ or apple-watch
	Name  string // as first written, such as C++
	Pages []*Page
}

// URL is the tag's page.
func (t *tag) URL() string { return Prefix + "tags/" + t.Key + "/" }

// FontSize is the size of the tag in the cloud, which grows with its
// number of pages.
func (t *tag) FontSize() string {
	return strconv.FormatFloat(12+1.5*math.Sqrt(float64(len(t.Pages))), 'f', -1, 64)
}

// build reads and renders the content folder dir into a site. A page that
// fails to read is left out and logged, so one broken file does not take
// the blog down.
func build(dir string, l *log.Logger) (*site, error) {
	pages, intro, err := load(dir, l)
	if err != nil {
		return nil, err
	}
	all := pages
	if intro != nil {
		all = append(slices.Clone(pages), intro)
	}
	var wg sync.WaitGroup
	work := make(chan *Page)
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for p := range work {
				if err := render(p, p.body); err != nil {
					l.Printf("blog: %s: %v", p.File, err)
				}
				p.body = nil
			}
		})
	}
	for _, p := range all {
		work <- p
	}
	close(work)
	wg.Wait()

	s := &site{
		intro:   intro,
		byPath:  make(map[string]*Page),
		aliases: make(map[string]string),
		files:   make(map[string]string),
		tagsBy:  make(map[string]*tag),
		cache:   make(map[string]*response),
	}
	sortPages(pages)
	for _, p := range pages {
		if q, ok := s.byPath[p.Path]; ok {
			l.Printf("blog: %s and %s both want %s; %s is left out", q.File, p.File, p.Path, p.File)
			continue
		}
		s.byPath[p.Path] = p
		s.pages = append(s.pages, p)
		switch p.Section {
		case "posts":
			s.posts = append(s.posts, p)
		case "ideas":
			s.ideas = append(s.ideas, p)
		}
	}
	for _, p := range s.pages {
		for _, a := range p.Aliases {
			a = "/" + strings.Trim(a, "/") + "/"
			if _, ok := s.byPath[a]; !ok {
				s.aliases[a] = p.Path
			}
		}
		for name, f := range p.Files {
			s.files[p.Path+name] = f
		}
		for _, t := range p.Tags {
			key := urlize(t)
			if key == "" {
				continue
			}
			tg := s.tagsBy[key]
			if tg == nil {
				tg = &tag{Key: key, Name: t}
				s.tagsBy[key] = tg
				s.tags = append(s.tags, tg)
			}
			if !slices.Contains(tg.Pages, p) {
				tg.Pages = append(tg.Pages, p)
			}
		}
	}
	slices.SortFunc(s.tags, func(a, b *tag) int { return strings.Compare(a.Key, b.Key) })
	for i := 1; i < len(s.posts); i++ {
		s.posts[i].next, s.posts[i-1].prev = s.posts[i-1], s.posts[i]
	}
	s.stream = append(slices.Clone(s.posts), s.ideas...)
	sortPages(s.stream)
	if err := s.addFiles(dir); err != nil {
		return nil, err
	}
	return s, nil
}

// addFiles adds the files of the content folder dir that are no page and
// in no bundle, such as images/ and files/, at their own paths.
func (s *site) addFiles(dir string) error {
	bundles := make(map[string]bool)
	for _, p := range s.pages {
		if p.Bundle != "" {
			bundles[p.Bundle] = true
		}
	}
	return fs.WalkDir(os.DirFS(dir), ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && name != "." || name == "_todo" {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if bundles[name] || fileExists(fileOf(dir, name+"/index.md")) {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(name) == ".md" {
			return nil
		}
		s.files["/"+name] = name
		return nil
	})
}

func fileExists(name string) bool {
	fi, err := os.Stat(name)
	return err == nil && !fi.IsDir()
}

// A pager is one page of a list that is paged.
type pager struct {
	N, Total   int
	Prev, Next string
	Window     []pageLink // the pages around this one
}

type pageLink struct {
	N       int
	URL     string
	Current bool
}

// pageURL is the address of page n of the list at base.
func pageURL(base string, n int) string {
	if n == 1 {
		return base
	}
	return base + "page/" + strconv.Itoa(n) + "/"
}

// paginate is page n of a list of total pages at base, with the four pages
// on either side, or more on one side near an end, as the theme drew it.
func paginate(base string, n, total int) *pager {
	p := &pager{N: n, Total: total}
	if n > 1 {
		p.Prev = pageURL(base, n-1)
	}
	if n < total {
		p.Next = pageURL(base, n+1)
	}
	begin, end := n-4, n+4
	if begin < 0 {
		end -= begin
	}
	if over := total - end; over < 0 {
		begin += over
	}
	for i := 1; i <= total; i++ {
		if i >= begin && i <= end {
			p.Window = append(p.Window, pageLink{N: i, URL: pageURL(base, i), Current: i == n})
		}
	}
	return p
}

// chunk is page n of pages, size to a page, and the number of pages.
func chunk(pages []*Page, size, n int) ([]*Page, int) {
	total := max(1, (len(pages)+size-1)/size)
	if n < 1 || n > total {
		return nil, total
	}
	return pages[(n-1)*size : min(n*size, len(pages))], total
}

// A group is the pages of one year.
type group struct {
	Year  string
	Pages []*Page
}

// byYear groups pages, newest first, by their year.
func byYear(pages []*Page) []group {
	var gs []group
	for _, p := range pages {
		y := p.Date.Format("2006")
		if len(gs) == 0 || gs[len(gs)-1].Year != y {
			gs = append(gs, group{Year: y})
		}
		gs[len(gs)-1].Pages = append(gs[len(gs)-1].Pages, p)
	}
	return gs
}

// truncate is s cut to at most n characters at a word's end, with " …"
// after it when cut, as Hugo's truncate did.
func truncate(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	var lastWord, lastNonSpace, count int
	for i, r := range s {
		count++
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' || r == '\v' || r == 0x85 || r == 0xA0:
			lastWord = lastNonSpace
		case isCJK(r):
			lastWord = i
		default:
			lastNonSpace = i + len(string(r))
		}
		if count > n {
			end := cmp.Or(lastWord, i)
			return s[:end] + " …"
		}
	}
	return s
}

func isCJK(r rune) bool { return cjkRe.MatchString(string(r)) }

// excerpt is what the home page shows of a post: its summary, or for a
// post in two languages, the start of each.
type excerptText struct{ En, Zh string }

func excerpt(p *Page) excerptText {
	if p.TitleZh == "" {
		return excerptText{En: unescape(stripHTML(string(p.Summary)))}
	}
	c := string(p.Content)
	en, zh := c, ""
	if i := strings.Index(c, `<div class="lang-zh">`); i >= 0 {
		en, zh = c[:i], c[i+len(`<div class="lang-zh">`):]
	}
	clean := func(s string) string {
		return strings.TrimLeft(unescape(stripHTML(s)), " \t\n\r\f\v")
	}
	return excerptText{En: truncate(clean(en), 300), Zh: truncate(clean(zh), 200)}
}

// describe is the meta description of p: the start of its English text.
var (
	augmentedRe = regexp.MustCompile(`(?s)<div class="idea-augmented">.*?</div>`)
	urlRe       = regexp.MustCompile(`https?://\S+`)
	spaceRe     = regexp.MustCompile(`\s+`)
)

func describe(p *Page) string {
	c := augmentedRe.ReplaceAllString(string(p.Content), "")
	if i := strings.Index(c, `<div class="lang-zh">`); i >= 0 {
		c = c[:i]
	}
	c = urlRe.ReplaceAllString(stripHTML(c), "")
	c = strings.TrimLeft(spaceRe.ReplaceAllString(c, " "), " ")
	return truncate(unescape(c), 200)
}

// unescape is s with its HTML entities decoded.
func unescape(s string) string { return html.UnescapeString(s) }
