// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bytes"
	"fmt"
	gohtml "html"
	"html/template"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/gohugoio/hugo-goldmark-extensions/passthrough"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The markdown is rendered as Hugo 0.91, which built the blog before,
// rendered it, so that pages keep their look and their heading anchors,
// with one change: math between $ or $$ is passed to MathJax untouched,
// where Hugo dropped the backslashes of \\ and \{ and turned _x_ into
// emphasis.
var md = goldmark.New(
	goldmark.WithExtensions(
		extension.Table,
		extension.Linkify,
		extension.TaskList,
		extension.DefinitionList,
		extension.Footnote,
		extension.Typographer,
		passthrough.New(passthrough.Config{
			InlineDelimiters: []passthrough.Delimiters{{Open: "$", Close: "$"}},
			BlockDelimiters:  []passthrough.Delimiters{{Open: "$$", Close: "$$"}},
		}),
	),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithHeadingAttribute(),
		parser.WithInlineParsers(util.Prioritized(strikeParser{}, 500)),
	),
	goldmark.WithRendererOptions(
		html.WithUnsafe(),
		renderer.WithNodeRenderers(
			util.Prioritized(&blogRenderer{}, 10),
			util.Prioritized(extension.NewStrikethroughHTMLRenderer(), 500),
		),
	),
)

// strikeParser strikes through text between ~~, and only ~~: a single ~,
// which the Chinese posts write after a sentence, stays a ~, as it did in
// Hugo.
type strikeParser struct{}

func (strikeParser) Trigger() []byte { return []byte{'~'} }

func (strikeParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	before := block.PrecendingCharacter()
	line, segment := block.PeekLine()
	node := parser.ScanDelimiter(line, before, 2, strikeDelimiter{})
	if node == nil || node.OriginalLength != 2 || before == '~' {
		return nil
	}
	node.Segment = segment.WithStop(segment.Start + node.OriginalLength)
	block.Advance(node.OriginalLength)
	pc.PushDelimiter(node)
	return node
}

type strikeDelimiter struct{}

func (strikeDelimiter) IsDelimiter(b byte) bool { return b == '~' }

func (strikeDelimiter) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}

func (strikeDelimiter) OnMatch(consumes int) ast.Node { return extast.NewStrikethrough() }

// blogRenderer renders the nodes the blog draws its own way: code with
// line numbers and colours, images that load lazily, links to the blog's
// root below /blog/, and math as escaped text for MathJax to read.
type blogRenderer struct{}

func (r *blogRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, r.code)
	reg.Register(ast.KindImage, r.image)
	reg.Register(ast.KindLink, r.link)
	reg.Register(passthrough.KindPassthroughInline, r.math)
	reg.Register(passthrough.KindPassthroughBlock, r.math)
}

func (r *blogRenderer) math(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	switch n := n.(type) {
	case *passthrough.PassthroughInline:
		w.Write(util.EscapeHTML(n.Segment.Value(src)))
	case *passthrough.PassthroughBlock:
		w.WriteString("<p>")
		for i := 0; i < n.Lines().Len(); i++ {
			l := n.Lines().At(i)
			w.Write(util.EscapeHTML(l.Value(src)))
		}
		w.WriteString("</p>\n")
	}
	return ast.WalkSkipChildren, nil
}

// canonical is u with the blog's prefix in front when u is a path from the
// site's root, such as /images/x.png, which Hugo wrote as
// https://changkun.de/blog/images/x.png.
func canonical(u string) string {
	if strings.HasPrefix(u, "/") && !strings.HasPrefix(u, "//") {
		return Prefix + strings.TrimPrefix(u, "/")
	}
	return u
}

// resolve points the links and images of doc that name a file beside the
// page, such as fig.png in a post's folder, at the file's address, so that
// they also work away from the page: in lists, summaries and feeds.
func resolve(doc ast.Node, links map[string]string) {
	if len(links) == 0 {
		return
	}
	to := func(dest []byte) []byte {
		name, err := url.PathUnescape(strings.TrimPrefix(string(dest), "./"))
		if a, ok := links[name]; ok && err == nil {
			return []byte(a)
		}
		return dest
	}
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch n := n.(type) {
		case *ast.Image:
			if entering {
				n.Destination = to(n.Destination)
			}
		case *ast.Link:
			if entering {
				n.Destination = to(n.Destination)
			}
		}
		return ast.WalkContinue, nil
	})
}

// media is the element that shows a file of each kind other than an image,
// by its extension.
var media = map[string]string{
	".mp4": "video", ".m4v": "video", ".mov": "video", ".webm": "video",
	".mp3": "audio", ".m4a": "audio", ".aac": "audio", ".ogg": "audio", ".oga": "audio", ".wav": "audio", ".flac": "audio",
}

// image draws an image, or plays a video or a sound written as one.
func (r *blogRenderer) image(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	n := node.(*ast.Image)
	dest := gohtml.EscapeString(canonical(string(n.Destination)))
	if el, ok := media[strings.ToLower(path.Ext(string(n.Destination)))]; ok {
		w.WriteString("<" + el + ` src="` + dest + `" controls preload="metadata"`)
		if el == "video" {
			w.WriteString(" playsinline")
		}
		if alt := plainText(n, src); len(alt) > 0 {
			w.WriteString(` aria-label="`)
			w.Write(util.EscapeHTML(alt))
			w.WriteString(`"`)
		}
		w.WriteString("></" + el + ">")
		return ast.WalkSkipChildren, nil
	}
	w.WriteString(`<img src="`)
	w.WriteString(dest)
	w.WriteString(`" alt="`)
	w.Write(util.EscapeHTML(plainText(n, src)))
	w.WriteString(`"`)
	if n.Title != nil {
		w.WriteString(` title="`)
		w.Write(util.EscapeHTML(n.Title))
		w.WriteString(`"`)
	}
	w.WriteString(` loading="lazy" decoding="async">`)
	return ast.WalkSkipChildren, nil
}

func (r *blogRenderer) link(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	n := node.(*ast.Link)
	if !entering {
		w.WriteString("</a>")
		return ast.WalkContinue, nil
	}
	w.WriteString(`<a href="`)
	w.Write(util.EscapeHTML(util.URLEscape([]byte(canonical(string(n.Destination))), true)))
	w.WriteString(`"`)
	if n.Title != nil {
		w.WriteString(` title="`)
		w.Write(util.EscapeHTML(n.Title))
		w.WriteString(`"`)
	}
	w.WriteString(">")
	return ast.WalkContinue, nil
}

// plainText is the text of n's children, without markup.
func plainText(n ast.Node, src []byte) []byte {
	var b bytes.Buffer
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(src))
		case *ast.String:
			b.Write(c.Value)
		default:
			b.Write(plainText(c, src))
		}
	}
	return b.Bytes()
}

// code renders a fenced code block as Hugo did, with Chroma, line numbers
// in a table, and classes that syntax.css colours. A block without a known
// language takes the language Chroma guesses.
func (r *blogRenderer) code(w util.BufWriter, src []byte, node ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkSkipChildren, nil
	}
	n := node.(*ast.FencedCodeBlock)
	var code strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		l := n.Lines().At(i)
		code.Write(l.Value(src))
	}
	lang := ""
	if n.Info != nil {
		lang, _, _ = strings.Cut(strings.TrimSpace(string(n.Info.Segment.Value(src))), " ")
		lang, _, _ = strings.Cut(lang, "{")
	}
	highlight(w, code.String(), lang)
	return ast.WalkSkipChildren, nil
}

// preWrapper writes the <pre> that Hugo wrote around code, which names the
// language on the <code>.
type preWrapper struct{ lang string }

func (p preWrapper) Start(code bool, style string) string {
	if !code {
		return `<pre tabindex="0" class="chroma"><code>`
	}
	return fmt.Sprintf(`<pre tabindex="0" class="chroma"><code class="language-%s" data-lang="%s">`, p.lang, p.lang)
}

func (p preWrapper) End(code bool) string { return "</code></pre>" }

func highlight(w util.BufWriter, code, lang string) {
	var lexer chroma.Lexer
	if lang != "" {
		lexer = lexers.Get(lang)
	}
	if lexer == nil {
		if lexer = lexers.Analyse(code); lexer == nil {
			lexer = lexers.Fallback
		}
		lang = strings.ToLower(lexer.Config().Name)
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err == nil {
		f := chromahtml.New(
			chromahtml.WithClasses(true),
			chromahtml.WithLineNumbers(true),
			chromahtml.LineNumbersInTable(true),
			chromahtml.TabWidth(4),
			chromahtml.WithPreWrapper(preWrapper{lang}),
		)
		var b bytes.Buffer
		if err = f.Format(&b, styles.Fallback, it); err == nil {
			w.WriteString(`<div class="highlight">`)
			w.Write(b.Bytes())
			w.WriteString("</div>\n")
			return
		}
	}
	w.WriteString(preWrapper{lang}.Start(true, ""))
	w.WriteString(gohtml.EscapeString(code))
	w.WriteString("</code></pre>\n")
}

// headingIDs gives headings the anchors Hugo gave them: the heading's
// markdown in lower case, letters, digits and _ kept, spaces and - as -,
// the rest dropped, and -1, -2 after a repeat.
type headingIDs struct{ seen map[string]bool }

func (ids *headingIDs) Generate(value []byte, kind ast.NodeKind) []byte {
	var b strings.Builder
	for _, r := range string(value) {
		switch {
		case r == '-' || r == ' ':
			b.WriteRune('-')
		case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}
	id := b.String()
	if id == "" {
		id = "heading"
	}
	if ids.seen[id] {
		for i := 1; ; i++ {
			if c := id + "-" + strconv.Itoa(i); !ids.seen[c] {
				id = c
				break
			}
		}
	}
	ids.seen[id] = true
	return []byte(id)
}

func (ids *headingIDs) Put(value []byte) { ids.seen[string(value)] = true }

// shortcode matches the shortcodes the content uses that wrap markdown.
var shortcode = regexp.MustCompile(`\{\{%\s*(/?)(en|zh|augmented)\s*%\}\}`)

// lastupdate is the one shortcode that writes a value, the page's date of
// last change.
var lastupdate = regexp.MustCompile(`\{\{<\s*lastupdate\s*>\}\}`)

// moreDivider ends a summary written by hand. Hugo knew only <!--more-->;
// the spaced form, which half the posts use, is taken too. Only the first
// ends the summary; the rest stay comments, as a post in two languages has
// one in each.
var moreDivider = regexp.MustCompile(`<!--\s*more\s*-->`)

const moreMarker = "HUGOMORE42"

// expand replaces the shortcodes of body with the HTML Hugo put in their
// place before it rendered the markdown: the wrapped markdown stays
// markdown between blank lines, inside a div.
func expand(body []byte, p *Page) []byte {
	body = shortcode.ReplaceAllFunc(body, func(m []byte) []byte {
		sm := shortcode.FindSubmatch(m)
		if len(sm[1]) > 0 {
			return []byte("\n</div>")
		}
		class := "lang-" + string(sm[2])
		if string(sm[2]) == "augmented" {
			class = "idea-augmented"
		}
		return []byte(`<div class="` + class + `">` + "\n")
	})
	return lastupdate.ReplaceAll(body, []byte(p.Lastmod.Format("2006/01/02")))
}

// render renders p's markdown body into its content, summary, table of
// contents, plain text and reading time.
func render(p *Page, body []byte) error {
	body = expand(body, p)
	divider := false
	if loc := moreDivider.FindIndex(body); loc != nil {
		divider = true
		body = slices.Concat(body[:loc[0]], []byte(moreMarker), body[loc[1]:])
	}
	ctx := parser.NewContext(parser.WithIDs(&headingIDs{seen: map[string]bool{}}))
	doc := md.Parser().Parse(text.NewReader(body), parser.WithContext(ctx))
	resolve(doc, p.links)
	var out bytes.Buffer
	if err := md.Renderer().Render(&out, body, doc); err != nil {
		return err
	}
	content := out.Bytes()
	if divider {
		var summary []byte
		summary, content = splitSummary(content)
		p.Summary = template.HTML(summary)
		p.Truncated = len(bytes.TrimSpace(content)) > len(bytes.TrimSpace(summary))
	}
	p.Content = template.HTML(content)
	p.TOC = toc(doc, body)
	p.Plain = stripHTML(string(content))
	words := strings.Fields(p.Plain)
	cjk := cjkRe.Match(body)
	if !divider {
		var s string
		if cjk {
			s, p.Truncated = truncateWordsByRune(words)
		} else {
			s, p.Truncated = truncateToSentence(p.Plain)
		}
		p.Summary = template.HTML(s)
	}
	count := len(words)
	if cjk {
		count = 0
		for _, w := range words {
			if n := utf8.RuneCountInString(w); n == len(w) {
				count++
			} else {
				count += n
			}
		}
		p.ReadingTime = (count + 500) / 501
	} else {
		p.ReadingTime = (count + 212) / 213
	}
	p.HasMath = hasMath(doc)
	return nil
}

// splitSummary splits rendered content at the summary marker as Hugo did:
// the paragraph that holds the marker goes, the summary is what came before.
func splitSummary(c []byte) (summary, content []byte) {
	at := bytes.Index(c, []byte(moreMarker))
	if at < 0 {
		return nil, c
	}
	start := bytes.LastIndex(c[:at], []byte("<p"))
	if start < 0 {
		start = at
	}
	end := bytes.Index(c[at:], []byte("</p"))
	if end < 0 {
		end = at + len(moreMarker)
	} else {
		end = at + end + len("</p>")
	}
	without := append(bytes.Clone(c[:start]), bytes.Trim(c[end:], "\n")...)
	return bytes.TrimSpace(without[:start]), bytes.TrimSpace(without)
}

var cjkRe = regexp.MustCompile(`\p{Han}|\p{Hangul}|\p{Hiragana}|\p{Katakana}`)

const summaryLength = 70

// truncateToSentence is the first summaryLength words of s, and on to the
// end of that sentence.
func truncateToSentence(s string) (string, bool) {
	words, last := 0, -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			words++
			last = i
			if words >= summaryLength {
				break
			}
		}
	}
	if last == -1 {
		return s, false
	}
	for j, r := range s[last:] {
		if r == '.' || r == '?' || r == '!' || r == '"' || r == '\n' {
			end := last + j + utf8.RuneLen(r)
			return strings.TrimSpace(s[:end]), end < len(s)
		}
	}
	return s, false
}

// truncateWordsByRune is the first summaryLength words of words, where
// each CJK character counts as a word.
func truncateWordsByRune(words []string) (string, bool) {
	count := 0
	for i, w := range words {
		if count >= summaryLength {
			return strings.Join(words[:i], " "), true
		}
		n := utf8.RuneCountInString(w)
		switch {
		case n == len(w):
			count++
		case count+n < summaryLength:
			count += n
		default:
			for ri := range w {
				if count >= summaryLength {
					return strings.Join(append(words[:i:i], w[:ri]), " "), true
				}
				count++
			}
		}
	}
	return strings.Join(words, " "), false
}

var stripReplacer = strings.NewReplacer("\n", " ", "</p>", "\n", "<br>", "\n", "<br />", "\n")

// stripHTML is s without its tags, with runs of spaces as one, as Hugo's
// .Plain is.
func stripHTML(s string) string {
	if !strings.ContainsAny(s, "<>") {
		return s
	}
	s = stripReplacer.Replace(s)
	var b strings.Builder
	var inTag, isSpace, wasSpace bool
	for _, r := range s {
		if !inTag {
			isSpace = false
		}
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case unicode.IsSpace(r):
			isSpace = true
			fallthrough
		default:
			if !inTag && (!isSpace || !wasSpace) {
				b.WriteRune(r)
			}
		}
		wasSpace = isSpace
	}
	return b.String()
}

// hasMath reports whether doc has math for MathJax to typeset.
func hasMath(doc ast.Node) bool {
	found := false
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if k := n.Kind(); k == passthrough.KindPassthroughInline || k == passthrough.KindPassthroughBlock {
			found = true
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	return found
}

// tocEntry is a heading in the table of contents.
type tocEntry struct {
	id, text string
	children []*tocEntry
}

// toc is the table of contents of doc's h2 and h3, as Hugo built it: an
// h3 without an h2 before it sits in an entry with no link.
func toc(doc ast.Node, src []byte) template.HTML {
	var rows []*tocEntry // one per h1, with the headings after it
	ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		h, ok := n.(*ast.Heading)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		e := &tocEntry{}
		if id, ok := h.AttributeString("id"); ok {
			if b, ok := id.([]byte); ok {
				e.id = string(b)
			}
		}
		var b bytes.Buffer
		for c := h.FirstChild(); c != nil; c = c.NextSibling() {
			md.Renderer().Render(&b, src, c)
		}
		e.text = b.String()
		if h.Level == 1 || rows == nil {
			rows = append(rows, &tocEntry{})
		}
		if h.Level == 1 {
			*rows[len(rows)-1] = tocEntry{id: e.id, text: e.text}
			return ast.WalkSkipChildren, nil
		}
		parent := rows[len(rows)-1]
		for i := 2; i < h.Level; i++ {
			if len(parent.children) == 0 {
				parent.children = append(parent.children, &tocEntry{})
			}
			parent = parent.children[len(parent.children)-1]
		}
		parent.children = append(parent.children, e)
		return ast.WalkSkipChildren, nil
	})
	var b strings.Builder
	var write func(entries []*tocEntry, level int)
	write = func(entries []*tocEntry, level int) {
		if level > 3 || len(entries) == 0 {
			return
		}
		b.WriteString("<ul>\n")
		for _, e := range entries {
			b.WriteString("<li>")
			if e.id != "" {
				b.WriteString(`<a href="#` + gohtml.EscapeString(e.id) + `">` + e.text + "</a>")
			}
			write(e.children, level+1)
			b.WriteString("</li>\n")
		}
		b.WriteString("</ul>\n")
	}
	for _, r := range rows {
		write(r.children, 2)
	}
	if !strings.Contains(b.String(), "<li>") {
		return ""
	}
	return template.HTML("<nav>\n" + b.String() + "</nav>")
}

// urlize turns a title or tag into a path segment as Hugo did: lower case,
// letters, digits and ./_#+~ kept, spaces and - between them as one -.
func urlize(s string) string {
	var out []rune
	hyphen := false
	for _, r := range s {
		ok := strings.ContainsRune("./\\_#+~", r) || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.M, r)
		switch {
		case ok:
			if hyphen {
				out = append(out, '-')
				hyphen = false
			}
			out = append(out, unicode.ToLower(r))
		case len(out) > 0 && (r == '-' || unicode.IsSpace(r)):
			hyphen = true
		}
	}
	return string(out)
}
