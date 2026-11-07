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

// processIdea turns one submitted idea into a bilingual post and commits it.
//
// It runs after the request has been answered, so it reports only to the log.
// The steps run in the order of their model calls, which the pipeline tests
// script by position.
func (s *Service) processIdea(req ideaRequest) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// The linked pages inform the title and the augmentation. Translation and
	// the published body see only what the author wrote.
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

	// Every step above degrades rather than fails, and the floor sits here
	// because the ways they degrade all converge on one artifact: the source
	// text in both language blocks. A gateway outage makes each fallback
	// substitute the input, and a model that echoes the input into both
	// fields passes isUsableTranslateResult on the primary path, so a check
	// inside either branch would miss the other. Publishing that is worse
	// than publishing nothing: it is permanent, dated, and public.
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

// references fetches the pages an idea links to and renders each as context
// for the model. A page that cannot be fetched is skipped.
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

// translate returns the title and content in both languages, the source
// polished. It asks for everything in one call and falls back to translating
// title and content separately when that answer is missing or malformed. The
// fallback copies what it cannot translate; checkBilingual decides whether the
// result is fit to publish.
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

// slug names the permalink, falling back to the slugified title when the model
// gives nothing usable.
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

// augment returns the deep dive in both languages and whether a model wrote
// it. The author may provide one; otherwise it is generated in the idea's
// language. Either way it is then translated. A post publishes without a deep
// dive, or with one in a single language, when these calls fail.
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

// Bounds on what linked pages add to a prompt.
const (
	maxURLs      = 5
	maxPageBytes = 32 * 1024 // read from the response
	maxPageText  = 4096      // kept after stripping tags, in bytes
)

var (
	urlRe     = regexp.MustCompile(`https?://[^\s<>"{}|\\^` + "`" + `\[\]]+`)
	htmlTagRe = regexp.MustCompile(`<[^>]*>`)
)

// fetchURL returns a rough plain-text rendering of the page at url.
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
	// Both limits count bytes, so either can split a multi-byte character.
	// Drop the partial one rather than send the model half of it.
	return strings.ToValidUTF8(text, ""), nil
}
