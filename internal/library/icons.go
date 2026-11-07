// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"strings"
	"unicode"
)

// typeIcons maps Zotero item types to the icons in layout.html's sprite, so
// rows of different kinds look different. Other types get "doc".
var typeIcons = map[string]string{
	"journalArticle":   "article",
	"magazineArticle":  "article",
	"newspaperArticle": "article",
	"conferencePaper":  "conf",
	"preprint":         "preprint",
	"book":             "book",
	"bookSection":      "chapter",
	"thesis":           "thesis",
	"report":           "report",
	"webpage":          "web",
	"blogPost":         "web",
	"forumPost":        "web",
	"computerProgram":  "code",
	"dataset":          "data",
	"presentation":     "slides",
	"videoRecording":   "media",
	"audioRecording":   "media",
	"podcast":          "media",
}

func typeIcon(itemType string) string {
	if i, ok := typeIcons[itemType]; ok {
		return i
	}
	return "doc"
}

// typeName turns an item type into words: "conferencePaper" is
// "Conference paper".
func typeName(itemType string) string {
	var b strings.Builder
	for i, r := range itemType {
		switch {
		case i == 0:
			b.WriteRune(unicode.ToUpper(r))
		case unicode.IsUpper(r):
			b.WriteByte(' ')
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
