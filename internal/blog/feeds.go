// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package blog

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"strings"
)

const rssDate = "Mon, 02 Jan 2006 15:04:05 -0700"

// rss is the RSS feed of pages, the list called title at base, or the
// whole blog for an empty title. It has every page, with its summary.
func (s *site) rss(title, base string, pages []*Page) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8" standalone="yes"?>` + "\n")
	b.WriteString(`<rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom">` + "\n<channel>\n")
	ch, desc := siteTitle, "Recent content on "+siteTitle
	if title != "" {
		ch, desc = title+" on "+siteTitle, "Recent content in "+title+" on "+siteTitle
	}
	elem(&b, "title", ch)
	elem(&b, "link", origin+base)
	elem(&b, "description", desc)
	elem(&b, "language", "en-US")
	if len(pages) > 0 {
		elem(&b, "lastBuildDate", pages[0].Date.Format(rssDate))
	}
	b.WriteString(`<atom:link href="`)
	xml.EscapeText(&b, []byte(origin+base+"index.xml"))
	b.WriteString(`" rel="self" type="application/rss+xml"/>` + "\n")
	for _, p := range pages {
		link := origin + pagePath(p)
		b.WriteString("<item>\n")
		elem(&b, "title", p.Title)
		elem(&b, "link", link)
		elem(&b, "pubDate", p.Date.Format(rssDate))
		elem(&b, "guid", link)
		elem(&b, "description", string(p.Summary))
		b.WriteString("</item>\n")
	}
	b.WriteString("</channel>\n</rss>\n")
	return b.Bytes()
}

func elem(b *bytes.Buffer, name, text string) {
	b.WriteString("<" + name + ">")
	xml.EscapeText(b, []byte(text))
	b.WriteString("</" + name + ">\n")
}

// sitemap lists the blog's lists and pages, with when each last changed.
func (s *site) sitemap() []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="utf-8" standalone="yes"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	url := func(loc string, p *Page) {
		b.WriteString("<url><loc>")
		xml.EscapeText(&b, []byte(origin+loc))
		b.WriteString("</loc>")
		if p != nil {
			b.WriteString("<lastmod>" + p.Lastmod.Format("2006-01-02T15:04:05Z07:00") + "</lastmod>")
		}
		b.WriteString("</url>\n")
	}
	first := func(ps []*Page) *Page {
		if len(ps) == 0 {
			return nil
		}
		return ps[0]
	}
	url(Prefix, first(s.stream))
	url(Prefix+"posts/", first(s.posts))
	url(Prefix+"ideas/", first(s.ideas))
	url(Prefix+"tags/", first(s.pages))
	for _, t := range s.tags {
		url(t.URL(), first(t.Pages))
	}
	for _, p := range s.pages {
		url(pagePath(p), p)
	}
	b.WriteString("</urlset>\n")
	return b.Bytes()
}

// ideasJSON is the index the ideas page searches: every idea's titles, the
// start of its text, its address and its date.
func (s *site) ideasJSON() []byte {
	type entry struct {
		Title   string `json:"title"`
		TitleZh string `json:"title_zh"`
		Plain   string `json:"plain"`
		URL     string `json:"url"`
		Date    string `json:"date"`
	}
	out := make([]entry, 0, len(s.ideas))
	for _, p := range s.ideas {
		out = append(out, entry{
			Title: p.Title, TitleZh: p.TitleZh, Plain: truncate(unescape(p.Plain), 500),
			URL: pagePath(p), Date: p.Date.Format("2006-01-02"),
		})
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(out)
	return []byte(strings.TrimSpace(b.String()))
}
