// Copyright 2022 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package research

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

//go:embed page.html
var pageHTML string

//go:embed assets/research.css
var css []byte

var (
	page = template.Must(template.New("page").Parse(pageHTML))

	// cssVersion changes the stylesheet's URL whenever it changes.
	cssVersion = fmt.Sprintf("%x", sha256.Sum256(css))[:8]

	md = goldmark.New(
		goldmark.WithExtensions(extension.Table),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
)

type pageData struct {
	Intro       template.HTML // the README before its first section
	Body        template.HTML // the sections
	Sections    []section     // one entry per section, for the page's menu
	CurrentYear string
	Updated     string // the README's date
	Commit      string // the checkout's commit, or empty
	CSSVersion  string
}

type section struct{ ID, Title string }

var (
	// A link the README closes an entry with, such as [PDF](...), becomes a
	// labelled chip. The period that separates two of them in the README
	// goes, since the chips separate themselves.
	chipLink = regexp.MustCompile(`<a href="([^"]*)">(PDF|GitHub|YouTube|OSF|Website)</a>\.?`)

	// The flags an entry opens with name the languages it is in.
	leadingFlags = regexp.MustCompile(`<li>((?:(?:🇬🇧|🇨🇳|🇩🇪) ?)+)`)
	flagName     = map[string]string{"🇬🇧": "EN", "🇨🇳": "中文", "🇩🇪": "Deutsch"}

	sectionHeading = regexp.MustCompile(`<h2 id="([^"]+)">([^<]+)</h2>`)
)

// renderPage writes the page for readme, of which it renders the part
// between <!--begin--> and <!--end-->.
func renderPage(w io.Writer, readme []byte, updated time.Time, commit string) error {
	_, rest, begun := bytes.Cut(readme, []byte("<!--begin-->"))
	content, _, ended := bytes.Cut(rest, []byte("<!--end-->"))
	if !begun || !ended {
		return errors.New("README.md lacks <!--begin--> or <!--end-->")
	}

	var b bytes.Buffer
	if err := md.Convert(content, &b); err != nil {
		return fmt.Errorf("cannot convert README.md: %w", err)
	}
	out := chipLink.ReplaceAllString(b.String(), `<a class="chip" href="$1">$2</a>`)
	out = tagLanguages(out)

	intro, body := out, ""
	if i := strings.Index(out, "<h2"); i >= 0 {
		intro, body = out[:i], out[i:]
	}
	var sections []section
	for _, m := range sectionHeading.FindAllStringSubmatch(body, -1) {
		sections = append(sections, section{ID: m[1], Title: m[2]})
	}

	return page.Execute(w, pageData{
		Intro:       template.HTML(intro),
		Body:        template.HTML(body),
		Sections:    sections,
		CurrentYear: time.Now().Format("2006"),
		Updated:     updated.Format("Jan 02, 2006"),
		Commit:      commit,
		CSSVersion:  cssVersion,
	})
}

// tagLanguages replaces the flags an entry opens with by a small tag naming
// its languages. English is the page's own language, so an entry only in
// English carries no tag.
func tagLanguages(html string) string {
	return leadingFlags.ReplaceAllStringFunc(html, func(m string) string {
		var names []string
		for _, f := range strings.Fields(strings.TrimPrefix(m, "<li>")) {
			names = append(names, flagName[f])
		}
		if len(names) == 1 && names[0] == "EN" {
			return "<li>"
		}
		return `<li><span class="lang">` + strings.Join(names, " · ") + `</span> `
	})
}
