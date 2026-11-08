// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

// A Post is a post in both languages.
type Post struct {
	Title, TitleZh string // the title in English, and in Chinese
	En, Zh         string // the text in English, and in Chinese
}

// The size of a part of a post that one model call translates, in bytes,
// and how many parts are translated at once. A long post in one call would
// run past the model's reply limit.
const (
	postPart  = 5000
	postCalls = 4
)

// TranslatePost translates a post written in one language into the other.
// The text is translated in parts, side by side; parts that are only code
// or math stay as they are. title and titleZh are the titles the author
// gave, in either field and either language; the one missing is
// translated. progress, if not nil, is told how many parts are done.
//
// It fails, and translates nothing, if any part comes back empty, the same
// as it went, or still in the source's language.
func (s *Service) TranslatePost(ctx context.Context, title, titleZh, body string, progress func(done, total int)) (Post, error) {
	if s == nil || s.llm == nil {
		return Post{}, errors.New("translation is not configured")
	}
	from := detectLang(proseOf(body))
	to := otherLang(from)

	parts := splitPost(body, postPart)
	out := make([]string, len(parts))
	errs := make([]error, len(parts))
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		done int
		sem  = make(chan struct{}, postCalls)
	)
	report := func() {
		mu.Lock()
		done++
		n := done
		mu.Unlock()
		if progress != nil {
			progress(n, len(parts))
		}
	}
	if progress != nil {
		progress(0, len(parts))
	}
	for i, p := range parts {
		if !hasProse(p) {
			out[i] = p
			report()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			t, err := s.llm.translatePart(ctx, p, from, to)
			if err == nil {
				err = checkPart(p, t, to)
			}
			if err != nil {
				errs[i] = fmt.Errorf("part %d of %d: %w", i+1, len(parts), err)
			}
			out[i] = t
			report()
		}()
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return Post{}, err
	}
	translated := strings.Join(out, "\n\n")

	// The titles go by their language, not by the field they were typed in.
	var en, zh string
	for _, t := range []string{title, titleZh} {
		if t = strings.TrimSpace(t); t == "" {
			continue
		}
		if detectLang(t) == "zh" {
			zh = cmpOr(zh, t)
		} else {
			en = cmpOr(en, t)
		}
	}
	var err error
	switch {
	case en == "" && zh == "":
		return Post{}, errors.New("the post has no title")
	case en == "":
		en, err = s.postTitle(ctx, zh, "en")
	case zh == "":
		zh, err = s.postTitle(ctx, en, "zh")
	}
	if err != nil {
		return Post{}, err
	}

	p := Post{Title: en, TitleZh: zh, En: strings.TrimSpace(body), Zh: translated}
	if from == "zh" {
		p.En, p.Zh = translated, strings.TrimSpace(body)
	}
	return p, nil
}

// postTitle translates a post's title into lang, whole, as a part of the
// post: the prompt for idea titles would cut it to a few words.
func (s *Service) postTitle(ctx context.Context, title, lang string) (string, error) {
	t, err := s.llm.translatePart(ctx, title, otherLang(lang), lang)
	if err != nil {
		return "", fmt.Errorf("the title: %w", err)
	}
	t = strings.Trim(strings.TrimSpace(t), `"'“”`)
	if !isLikelyTitle(t) || (lang == "zh") != (detectLang(t) == "zh") {
		return "", fmt.Errorf("the title came back as %q", t)
	}
	return t, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// translatePart translates one part of a post from one language to another.
func (c *llmClient) translatePart(ctx context.Context, part, from, to string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	system, err := renderPrompt(promptTranslatePost, postData{From: languageName(from), Language: languageName(to)})
	if err != nil {
		return "", err
	}
	t, err := c.complete(ctx, c.titleModel, system, part, maxTokensLong)
	if err != nil {
		return "", err
	}
	return unwrapFence(t, part), nil
}

// unwrapFence takes off a code block the model put around the whole of its
// reply, when the part it was given was not one.
func unwrapFence(t, part string) string {
	if strings.HasPrefix(strings.TrimSpace(part), "```") || !strings.HasPrefix(t, "```") || !strings.HasSuffix(t, "```") {
		return t
	}
	first := strings.IndexByte(t, '\n')
	if first < 0 || first >= len(t)-3 {
		return t
	}
	return strings.TrimSpace(t[first+1 : len(t)-3])
}

// checkPart returns an error if t is not a translation of part into to.
func checkPart(part, t, to string) error {
	if strings.TrimSpace(t) == "" {
		return errors.New("the translation came back empty")
	}
	src, got := proseOf(part), proseOf(t)
	if letters(src) < 40 {
		return nil // too little prose to tell
	}
	if strings.TrimSpace(t) == strings.TrimSpace(part) {
		return errors.New("the translation came back as the text itself")
	}
	if detectLang(got) != to {
		return fmt.Errorf("the translation came back in %s, not %s", languageName(detectLang(got)), languageName(to))
	}
	return nil
}

var (
	fencedRe = regexp.MustCompile("(?ms)^[ \t]*(```|~~~).*?^[ \t]*(```|~~~)[ \t]*$")
	mathRe   = regexp.MustCompile(`(?s)\$\$.*?\$\$|\$[^$\n]+\$`)
	codeRe   = regexp.MustCompile("`[^`\n]*`")
	linkRe   = regexp.MustCompile(`\]\([^)]*\)|https?://\S+|<[^>]+>|\{\{[^}]*\}\}`)
)

// proseOf is s without its code, math, URLs, link targets, HTML and
// shortcodes: the words a translation changes.
func proseOf(s string) string {
	for _, re := range []*regexp.Regexp{fencedRe, mathRe, codeRe, linkRe} {
		s = re.ReplaceAllString(s, " ")
	}
	return s
}

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// hasProse reports whether part has words to translate.
func hasProse(part string) bool { return letters(proseOf(part)) > 0 }

// splitPost splits a post's markdown into parts of about max bytes, at
// blank lines outside fenced code and blocks of math, starting a new part
// at a heading once a part is half full.
func splitPost(body string, max int) []string {
	var blocks, cur []string
	fence, math := "", false
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case fence != "":
			cur = append(cur, l)
			if len(t) >= len(fence) && strings.Trim(t, fence[:1]) == "" {
				fence = ""
			}
			continue
		case math:
			cur = append(cur, l)
			if strings.Contains(t, "$$") {
				math = false
			}
			continue
		case t == "":
			flush()
			continue
		}
		cur = append(cur, l)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:len(t)-len(strings.TrimLeft(t, t[:1]))]
		} else if strings.HasPrefix(t, "$$") && (t == "$$" || !strings.Contains(t[2:], "$$")) {
			math = true
		}
	}
	flush()

	var parts []string
	var b strings.Builder
	for _, blk := range blocks {
		heading := strings.HasPrefix(blk, "#")
		if b.Len() > 0 && (b.Len()+len(blk) > max || heading && b.Len() > max/2) {
			parts = append(parts, b.String())
			b.Reset()
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(blk)
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}
