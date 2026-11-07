// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"regexp"
	"strings"
)

// citekeyRE finds the citekey in a record's first line, such as
// "@article{vaswani_attention_2017,".
var citekeyRE = regexp.MustCompile(`^\s*@\w+\s*\{\s*([^,\s]+)\s*,`)

// citekey returns the citekey of a BibTeX record, or "".
func citekey(bib string) string {
	if m := citekeyRE.FindStringSubmatch(bib); m != nil {
		return m[1]
	}
	return ""
}

// bibtexFile joins the records of es into one file. When two records have the
// same citekey, the later ones get a, b, and so on appended, so the file stays
// valid for LaTeX.
func bibtexFile(es []*Entry) string {
	used := map[string]bool{}
	var b strings.Builder
	for _, e := range es {
		bib := strings.TrimSpace(e.BibTeX)
		if bib == "" {
			continue
		}
		if key := citekey(bib); key != "" {
			unique := key
			for i := 0; used[unique]; i++ {
				unique = key + suffix(i)
			}
			used[unique] = true
			if unique != key {
				bib = strings.Replace(bib, key, unique, 1)
			}
		}
		b.WriteString(bib)
		b.WriteString("\n\n")
	}
	return b.String()
}

// suffix returns a, b, ..., z, aa, ab, ...
func suffix(i int) string {
	s := ""
	for i++; i > 0; i = (i - 1) / 26 {
		s = string(rune('a'+(i-1)%26)) + s
	}
	return s
}
