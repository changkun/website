// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"cmp"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Collection is a Zotero collection, which the site calls a topic.
type Collection struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Parent string `json:"parent,omitempty"`
}

// Item is a Zotero item as the cache stores it: the API's data, unchanged,
// and the BibTeX Zotero exports for it.
type Item struct {
	Key     string          `json:"key"`
	Version int             `json:"version"`
	Data    json.RawMessage `json:"data"`
	BibTeX  string          `json:"bibtex"`
}

// cacheFormat changes when the cache misses something an older server did
// not keep, so a cache of another format is dropped and rebuilt by a full
// sync. 2: trashed collections are left out.
const cacheFormat = 2

// cacheFile is cache.json: the whole library at Version.
type cacheFile struct {
	Format      int                    `json:"format"` // cacheFormat
	Version     int                    `json:"version"`
	Collections map[string]*Collection `json:"collections"`
	Items       map[string]*Item       `json:"items"`
}

// Entry is an item's fields as the pages show them.
type Entry struct {
	Key         string
	Type        string
	Title       string
	Authors     []string // "First Last", in order
	Year        string
	Venue       string
	DOI         string
	URL         string
	Abstract    string
	Tags        []string
	Collections []string
	Other       [][2]string // any other non-empty field, as is
	BibTeX      string
	Added       string // dateAdded, for sorting ties

	search string // lowercase title, authors, venue, and year
}

// ShortAuthors is "Last", "Last, Last", or "Last et al.".
func (e *Entry) ShortAuthors() string {
	switch len(e.Authors) {
	case 0:
		return ""
	case 1:
		return lastName(e.Authors[0])
	case 2:
		return lastName(e.Authors[0]) + ", " + lastName(e.Authors[1])
	}
	return lastName(e.Authors[0]) + " et al."
}

func lastName(name string) string {
	if i := strings.LastIndexByte(name, ' '); i >= 0 {
		return name[i+1:]
	}
	return name
}

// Link is the entry's DOI as a URL, or else its URL.
func (e *Entry) Link() string {
	if e.DOI != "" {
		return "https://doi.org/" + e.DOI
	}
	return e.URL
}

// index is the library in memory. An index is never changed once built:
// sync builds a new one and swaps it in, so readers never see half of a sync.
type index struct {
	cache    cacheFile
	entries  map[string]*Entry
	sorted   []*Entry            // every entry, newest first
	children map[string][]string // collection key to child keys, by name; "" is the root
	members  map[string][]string // collection key to its own item keys
	counts   map[string]int      // collection key to entries in it and below
}

func newIndex(c cacheFile) *index {
	if c.Collections == nil {
		c.Collections = map[string]*Collection{}
	}
	if c.Items == nil {
		c.Items = map[string]*Item{}
	}
	ix := &index{
		cache:    c,
		entries:  make(map[string]*Entry, len(c.Items)),
		children: map[string][]string{},
		members:  map[string][]string{},
	}
	for _, it := range c.Items {
		e, err := parseEntry(it)
		if err != nil {
			continue // logged when the item was synced
		}
		ix.entries[e.Key] = e
		ix.sorted = append(ix.sorted, e)
		for _, ck := range e.Collections {
			ix.members[ck] = append(ix.members[ck], e.Key)
		}
	}
	sortEntries(ix.sorted)
	for _, col := range c.Collections {
		parent := col.Parent
		if _, ok := c.Collections[parent]; !ok {
			parent = ""
		}
		ix.children[parent] = append(ix.children[parent], col.Key)
	}
	for _, keys := range ix.children {
		slices.SortFunc(keys, func(a, b string) int {
			return cmp.Or(
				cmp.Compare(strings.ToLower(c.Collections[a].Name), strings.ToLower(c.Collections[b].Name)),
				cmp.Compare(a, b))
		})
	}
	ix.counts = make(map[string]int, len(c.Collections))
	for k := range c.Collections {
		seen := map[string]bool{}
		for _, ck := range ix.subtree(k) {
			for _, ik := range ix.members[ck] {
				if ix.entries[ik] != nil {
					seen[ik] = true
				}
			}
		}
		ix.counts[k] = len(seen)
	}
	return ix
}

// sortEntries orders entries newest first, then by title.
func sortEntries(es []*Entry) {
	slices.SortFunc(es, func(a, b *Entry) int {
		return cmp.Or(
			cmp.Compare(b.Year, a.Year),
			cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title)),
			cmp.Compare(a.Key, b.Key))
	})
}

// apply returns the cache with ch applied, and the keys of items it could not
// parse. It does not change c.
func apply(c cacheFile, ch *changes) (cacheFile, []string) {
	next := cacheFile{
		Format:      cacheFormat,
		Version:     ch.version,
		Collections: make(map[string]*Collection, len(c.Collections)),
		Items:       make(map[string]*Item, len(c.Items)),
	}
	for k, v := range c.Collections {
		next.Collections[k] = v
	}
	for k, v := range c.Items {
		next.Items[k] = v
	}
	for _, col := range ch.collections {
		if trashed(col.Data.Deleted) {
			delete(next.Collections, col.Key)
			continue
		}
		parent := ""
		_ = json.Unmarshal(col.Data.ParentCollection, &parent) // false stays ""
		next.Collections[col.Key] = &Collection{Key: col.Key, Name: col.Data.Name, Parent: parent}
	}
	var bad []string
	for _, it := range ch.items {
		var d struct {
			Deleted json.RawMessage `json:"deleted"`
		}
		if err := json.Unmarshal(it.Data, &d); err != nil {
			bad = append(bad, it.Key)
			continue
		}
		if trashed(d.Deleted) {
			delete(next.Items, it.Key)
			continue
		}
		item := &Item{Key: it.Key, Version: it.Version, Data: it.Data, BibTeX: strings.TrimSpace(it.BibTeX)}
		if _, err := parseEntry(item); err != nil {
			bad = append(bad, it.Key)
			continue
		}
		next.Items[it.Key] = item
	}
	for _, k := range ch.deleted.Collections {
		delete(next.Collections, k)
	}
	for _, k := range ch.deleted.Items {
		delete(next.Items, k)
	}
	// A collection under a trashed or deleted one goes with it.
	for changed := true; changed; {
		changed = false
		for k, col := range next.Collections {
			if col.Parent != "" && next.Collections[col.Parent] == nil {
				delete(next.Collections, k)
				changed = true
			}
		}
	}
	return next, bad
}

// trashed reports whether the deleted field of an item or collection marks it
// as in the trash.
// Zotero sends 1 or true.
func trashed(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s == "1" || s == "true"
}

// fields the pages show by name, so Other leaves them out.
var known = map[string]bool{
	"key": true, "version": true, "itemType": true, "title": true, "creators": true,
	"date": true, "DOI": true, "url": true, "abstractNote": true, "tags": true,
	"collections": true, "relations": true, "dateAdded": true, "dateModified": true,
	"deleted": true, "accessDate": true,
}

// venueFields name where an item was published, in order of preference.
var venueFields = []string{
	"publicationTitle", "proceedingsTitle", "conferenceName", "bookTitle",
	"websiteTitle", "blogTitle", "repository", "university", "institution", "publisher",
}

var yearRE = regexp.MustCompile(`\b(1[5-9]|20)\d\d\b`)

// richTextRE matches the markup Zotero allows in titles: <i>, <b>, <sub>,
// <sup>, and <span> for small caps or nocase. Pages show titles as plain text.
var richTextRE = regexp.MustCompile(`</?(?:i|b|sub|sup|span)(?:\s[^>]*)?>`)

// parseEntry reads an item's data into an Entry.
func parseEntry(it *Item) (*Entry, error) {
	var d map[string]json.RawMessage
	if err := json.Unmarshal(it.Data, &d); err != nil {
		return nil, fmt.Errorf("item %s: %w", it.Key, err)
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(d[k], &s)
		return strings.TrimSpace(s)
	}

	e := &Entry{
		Key:      it.Key,
		Type:     str("itemType"),
		Title:    richTextRE.ReplaceAllString(str("title"), ""),
		DOI:      strings.TrimPrefix(strings.TrimPrefix(str("DOI"), "https://doi.org/"), "doi:"),
		URL:      str("url"),
		Abstract: str("abstractNote"),
		Added:    str("dateAdded"),
		BibTeX:   it.BibTeX,
	}
	if e.Title == "" {
		e.Title = cmp.Or(str("caseName"), str("nameOfAct"), str("subject"), "(untitled)")
	}
	e.Year = yearRE.FindString(str("date"))
	for _, f := range venueFields {
		if v := str(f); v != "" {
			e.Venue = v
			break
		}
	}

	var creators []struct {
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
		Name      string `json:"name"`
	}
	_ = json.Unmarshal(d["creators"], &creators)
	for _, c := range creators {
		name := strings.TrimSpace(cmp.Or(c.Name, strings.TrimSpace(c.FirstName+" "+c.LastName)))
		if name != "" {
			e.Authors = append(e.Authors, name)
		}
	}

	var tags []struct {
		Tag string `json:"tag"`
	}
	_ = json.Unmarshal(d["tags"], &tags)
	for _, t := range tags {
		e.Tags = append(e.Tags, t.Tag)
	}
	_ = json.Unmarshal(d["collections"], &e.Collections)

	for k, raw := range d {
		if known[k] || slices.Contains(venueFields, k) && str(k) == e.Venue {
			continue
		}
		var s string
		if json.Unmarshal(raw, &s) == nil && strings.TrimSpace(s) != "" {
			e.Other = append(e.Other, [2]string{k, strings.TrimSpace(s)})
		}
	}
	slices.SortFunc(e.Other, func(a, b [2]string) int { return cmp.Compare(a[0], b[0]) })

	e.search = strings.ToLower(strings.Join([]string{e.Title, strings.Join(e.Authors, " "), e.Venue, e.Year}, " "))
	return e, nil
}

// match reports whether every word of q is in the entry's title, authors,
// venue, or year. q is lowercase.
func (e *Entry) match(words []string) bool {
	for _, w := range words {
		if !strings.Contains(e.search, w) {
			return false
		}
	}
	return true
}

// queryWords splits a filter into lowercase words.
func queryWords(q string) []string {
	return strings.Fields(strings.ToLower(q))
}

// subtree returns key and every collection below it.
func (ix *index) subtree(key string) []string {
	out := []string{key}
	for i := 0; i < len(out); i++ {
		out = append(out, ix.children[out[i]]...)
	}
	return out
}

// entriesIn returns the entries in the given collections and their
// subcollections, each once, newest first. An entry in several of them is
// listed once.
func (ix *index) entriesIn(keys []string) []*Entry {
	seen := map[string]bool{}
	var out []*Entry
	for _, k := range keys {
		for _, ck := range ix.subtree(k) {
			for _, ik := range ix.members[ck] {
				if !seen[ik] {
					seen[ik] = true
					if e := ix.entries[ik]; e != nil {
						out = append(out, e)
					}
				}
			}
		}
	}
	sortEntries(out)
	return out
}

// inTopics reports whether entry key is in one of the given collections or
// their subcollections.
func (ix *index) inTopics(key string, topics []string) bool {
	e := ix.entries[key]
	if e == nil {
		return false
	}
	for _, t := range topics {
		for _, ck := range ix.subtree(t) {
			if slices.Contains(e.Collections, ck) {
				return true
			}
		}
	}
	return false
}

// path returns the names from the root down to collection key.
func (ix *index) path(key string) []*Collection {
	var out []*Collection
	for k := key; k != ""; {
		c := ix.cache.Collections[k]
		if c == nil || len(out) > 64 {
			break
		}
		out = append(out, c)
		k = c.Parent
	}
	slices.Reverse(out)
	return out
}

// countIn returns how many entries are in collection key and below.
func (ix *index) countIn(key string) int {
	return ix.counts[key]
}

// duplicateDOIs returns groups of item keys that share a DOI, for the sync log.
func (ix *index) duplicateDOIs() [][]string {
	by := map[string][]string{}
	for _, e := range ix.entries {
		if e.DOI != "" {
			d := strings.ToLower(e.DOI)
			by[d] = append(by[d], e.Key)
		}
	}
	var out [][]string
	for _, keys := range by {
		if len(keys) > 1 {
			slices.Sort(keys)
			out = append(out, keys)
		}
	}
	return out
}
