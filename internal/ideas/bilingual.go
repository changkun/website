// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// bilingual holds a text in English and Chinese.
type bilingual struct{ en, zh string }

// inLang pairs source, written in lang, with its translation.
func inLang(lang, source, translated string) bilingual {
	if lang == "en" {
		return bilingual{en: source, zh: translated}
	}
	return bilingual{en: translated, zh: source}
}

// otherLang returns the language an idea in lang is translated into.
func otherLang(lang string) string {
	if lang == "en" {
		return "zh"
	}
	return "en"
}

// detectLang returns "zh" if Han characters make up more than half of the
// runes in s that are neither space nor punctuation, and "en" otherwise.
func detectLang(s string) string {
	var cjk, total int
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		total++
		if unicode.Is(unicode.Han, r) {
			cjk++
		}
	}
	if total > 0 && cjk*2 > total {
		return "zh"
	}
	return "en"
}

// normalizeTranslateResult trims the fields of tr, and swaps source and
// translation back if the model inverted them. lang is the idea's language.
func normalizeTranslateResult(tr *translateResult, lang string) *translateResult {
	n := *tr
	n.PolishedTitle = strings.TrimSpace(n.PolishedTitle)
	n.PolishedContent = strings.TrimSpace(n.PolishedContent)
	n.TranslatedTitle = strings.TrimSpace(n.TranslatedTitle)
	n.TranslatedContent = strings.TrimSpace(n.TranslatedContent)

	if detectLang(n.PolishedContent) == otherLang(lang) && detectLang(n.TranslatedContent) == lang {
		n.PolishedTitle, n.TranslatedTitle = n.TranslatedTitle, n.PolishedTitle
		n.PolishedContent, n.TranslatedContent = n.TranslatedContent, n.PolishedContent
	}
	return &n
}

// isUsableTranslateResult reports whether tr has two plausible titles and two
// non-empty contents.
func isUsableTranslateResult(tr *translateResult) bool {
	return isLikelyTitle(tr.PolishedTitle) && isLikelyTitle(tr.TranslatedTitle) &&
		strings.TrimSpace(tr.PolishedContent) != "" && strings.TrimSpace(tr.TranslatedContent) != ""
}

// isLikelyTitle reports whether s is one non-empty line of at most 120 runes.
func isLikelyTitle(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, "\r\n") {
		return false
	}
	if len([]rune(s)) > 120 {
		return false
	}
	return true
}

// checkBilingual returns an error if the post is not fit to publish. Titles may
// read the same in both languages; contents may not, and each must be written
// in its own language.
func checkBilingual(title, content bilingual) error {
	for _, f := range []struct{ name, value string }{
		{"the en title", title.en},
		{"the zh title", title.zh},
		{"the en content", content.en},
		{"the zh content", content.zh},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("%s is empty", f.name)
		}
	}
	if strings.TrimSpace(content.en) == strings.TrimSpace(content.zh) {
		return errors.New("the en and zh content are identical, nothing was translated")
	}
	if detectLang(content.en) != "en" {
		return errors.New("the en content is not English")
	}
	if detectLang(content.zh) != "zh" {
		return errors.New("the zh content is not Chinese")
	}
	return nil
}
