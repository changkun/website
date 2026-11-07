// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// processIdea turns an idea into a bilingual post and commits it. It runs in
// the background and reports only to the log. The steps run in the order of
// their model calls, which the tests script by position.
func (s *Service) processIdea(req ideaRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Linked pages inform the title and the deep dive. Translation and the
	// published body use only what the author wrote.
	enriched := req.Content + s.references(ctx, req.Content)

	if req.Title == "" {
		s.log.Printf("generating title for idea...")
		title, err := s.llm.generateTitle(ctx, enriched)
		if err != nil {
			s.log.Printf("idea not published: title generation failed: %v", err)
			return
		}
		req.Title = title
		s.log.Printf("generated title: %s", req.Title)
	}

	lang := detectLang(req.Content)
	title, content := s.translate(ctx, lang, req.Title, req.Content)
	s.log.Printf("detected language: %s", lang)

	// The steps above degrade instead of failing. A gateway outage, or a model
	// that echoes its input, leaves the source text in both languages on
	// either translation path. Publishing is permanent, so withhold the post.
	if err := checkBilingual(title, content); err != nil {
		s.log.Printf("idea not published: %v", err)
		return
	}

	p := post{
		date:    time.Now(),
		slug:    s.slug(ctx, title.en),
		title:   title,
		content: content,
	}
	p.augmented, p.llmGenerated = s.augment(ctx, lang, req.Title, enriched, req.Augmented)

	if err := s.github.createFile(ctx, p.path(), buildMarkdown(p), sanitizeCommitMsg("ideas: "+title.en)); err != nil {
		s.log.Printf("GitHub commit failed: %v", err)
		return
	}
	s.log.Printf("idea published: %s", p.filename())
}

// references fetches the pages content links to and renders them as context
// for the model, skipping any that fail.
func (s *Service) references(ctx context.Context, content string) string {
	var refs strings.Builder
	for _, u := range urlRe.FindAllString(content, maxURLs) {
		s.log.Printf("fetching linked content: %s", u)
		text, err := fetchURL(ctx, u)
		if err != nil {
			s.log.Printf("failed to fetch %s: %v", u, err)
			continue
		}
		ref, err := renderPrompt(promptReference, referenceData{URL: u, Text: text})
		if err != nil {
			s.log.Printf("failed to render reference for %s: %v", u, err)
			continue
		}
		refs.WriteString("\n\n")
		refs.WriteString(ref)
	}
	return refs.String()
}

// translate returns the title and content, each as the polished source plus
// its translation. If the combined call fails or its reply is unusable, it
// translates title and content separately, keeping the source wherever that
// fails too.
func (s *Service) translate(ctx context.Context, lang, title, content string) (bilingual, bilingual) {
	s.log.Printf("detecting language, polishing, and translating...")
	tr, err := s.llm.detectAndTranslate(ctx, title, content)
	if err == nil {
		tr = normalizeTranslateResult(tr, lang)
		if !isUsableTranslateResult(tr) {
			err = errors.New("invalid detect+translate payload")
		}
	}
	if err == nil {
		return inLang(lang, tr.PolishedTitle, tr.TranslatedTitle),
			inLang(lang, tr.PolishedContent, tr.TranslatedContent)
	}
	s.log.Printf("detect+translate unavailable, falling back to separate translation: %v", err)

	target := otherLang(lang)
	translatedTitle, err := s.llm.translateTitle(ctx, title, target)
	if err != nil {
		s.log.Printf("title translation fallback failed: %v", err)
		translatedTitle = title
	}
	translatedContent, err := s.llm.translateContent(ctx, content, target)
	if err != nil {
		s.log.Printf("content translation fallback failed: %v", err)
		translatedContent = content
	}
	return inLang(lang, title, translatedTitle), inLang(lang, content, translatedContent)
}

// slug asks the model for a permalink slug, falling back to slugify.
func (s *Service) slug(ctx context.Context, titleEn string) string {
	s.log.Printf("generating short slug...")
	slug, err := s.llm.generateSlug(ctx, titleEn)
	if err != nil {
		s.log.Printf("LLM slug generation failed, using fallback: %v", err)
		slug = slugify(titleEn)
	}
	s.log.Printf("slug: %s", slug)
	return slug
}

// augment returns the deep dive in both languages, and whether a model wrote
// it. It uses the author's deep dive if provided, and generates one otherwise.
// If a call fails, the post publishes without the deep dive or its
// translation.
func (s *Service) augment(ctx context.Context, lang, title, enriched, provided string) (bilingual, bool) {
	source := provided
	if source != "" {
		s.log.Printf("using provided augmented content for: %s", title)
	} else {
		s.log.Printf("augmenting idea: %s", title)
		generated, err := s.llm.augment(ctx, lang, title, enriched)
		if err != nil {
			s.log.Printf("LLM augmentation failed, publishing without augmentation: %v", err)
			return bilingual{}, false
		}
		source = generated
	}
	if source == "" {
		return bilingual{}, false
	}

	target := otherLang(lang)
	s.log.Printf("translating augmented content to %s...", target)
	translated, err := s.llm.translateContent(ctx, source, target)
	if err != nil {
		s.log.Printf("augmented translation failed: %v", err)
		translated = ""
	}
	return inLang(lang, source, translated), provided == ""
}

// Limits on what linked pages add to a prompt.
const (
	maxURLs      = 5
	maxPageBytes = 32 * 1024 // read from each response
	maxPageText  = 4096      // bytes kept after stripping tags
)

var (
	urlRe     = regexp.MustCompile(`https?://[^\s<>"{}|\\^` + "`" + `\[\]]+`)
	htmlTagRe = regexp.MustCompile(`<[^>]*>`)
)

// fetchURL returns the text of the page at url, with tags stripped and
// whitespace collapsed.
func fetchURL(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "ChangkunIdeasBot/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return "", err
	}
	text := strings.Join(strings.Fields(htmlTagRe.ReplaceAllString(string(body), " ")), " ")
	if len(text) > maxPageText {
		text = text[:maxPageText]
	}
	// Both limits count bytes; drop any character they split.
	return strings.ToValidUTF8(text, ""), nil
}
