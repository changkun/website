// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"latere.ai/x/pkg/llmjson"
	"latere.ai/x/pkg/luxsdk"
	"latere.ai/x/pkg/sanitize"
)

// llmClient runs the prompts in prompts/ through the Lux gateway, whose own
// dialect gives every model the same request and response shape.
type llmClient struct {
	lux        luxsdk.Caller
	model      string // writes the deep dive
	titleModel string // does everything else: titles, slugs, polish, translation
	log        *log.Logger
}

// Reply limits in tokens. Every request sets one, since the gateway otherwise
// applies 4096, which cuts a long document off.
const (
	maxTokensShort = 1024  // a phrase, such as a title or a slug
	maxTokensLong  = 16384 // a whole document, or both languages of one
)

// augment writes a deep dive on an idea in sourceLang. It first lets the model
// search and fetch the web, so citations point at real pages, and retries
// without those tools if that fails.
func (c *llmClient) augment(ctx context.Context, sourceLang, title, content string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	prompt, err := renderPrompt(promptIdeaInput, ideaInputData{Title: title, Content: content})
	if err != nil {
		return "", err
	}

	sysPrompt, err := renderPrompt(promptAugment, augmentData{Lang: sourceLang, WebTools: true})
	if err != nil {
		return "", err
	}
	grounded := &luxsdk.Request{
		Model:     c.model,
		System:    []luxsdk.Block{{Type: luxsdk.BlockText, Text: sysPrompt}},
		Messages:  []luxsdk.Message{luxsdk.UserText(prompt)},
		MaxTokens: new(int64(maxTokensLong)),
		WebSearch: &luxsdk.WebSearch{ContextSize: "medium"},
		ServerTools: []luxsdk.ServerTool{{
			Type:   "web_fetch_20250910",
			Name:   "web_fetch",
			Config: json.RawMessage(`{"max_uses":5}`),
		}},
	}
	result, err := c.generate(ctx, grounded)
	if err == nil {
		return result, nil
	}
	c.log.Printf("augment with web search failed, falling back to plain: %v", err)

	plainPrompt, err := renderPrompt(promptAugment, augmentData{Lang: sourceLang})
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.model, plainPrompt, prompt, maxTokensLong)
}

func (c *llmClient) generateTitle(ctx context.Context, content string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptTitle, nil)
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, content, maxTokensShort)
}

// improveContent fixes typos, grammar, and flow in content.
func (c *llmClient) improveContent(ctx context.Context, content string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptImprove, nil)
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, content, maxTokensLong)
}

// translateResult is the JSON reply to detect_translate.tmpl.
type translateResult struct {
	Lang              string `json:"lang"`
	PolishedTitle     string `json:"polished_title"`
	PolishedContent   string `json:"polished_content"`
	TranslatedTitle   string `json:"translated_title"`
	TranslatedContent string `json:"translated_content"`
}

// detectAndTranslate polishes title and content, and translates both into the
// other language, in one call.
func (c *llmClient) detectAndTranslate(ctx context.Context, title, content string) (*translateResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	system, err := renderPrompt(promptDetectTranslate, nil)
	if err != nil {
		return nil, err
	}
	prompt, err := renderPrompt(promptIdeaInput, ideaInputData{Title: title, Content: content})
	if err != nil {
		return nil, err
	}
	raw, err := c.complete(ctx, c.titleModel, system, prompt, maxTokensLong)
	if err != nil {
		return nil, err
	}

	raw = llmjson.Unfence(raw)

	var result translateResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		// Models often get JSON escaping wrong; try repairing it.
		repaired := llmjson.Repair(raw)
		if err2 := json.Unmarshal([]byte(repaired), &result); err2 != nil {
			return nil, fmt.Errorf("parse translation response: %w (raw: %s)", err, raw)
		}
	}
	// Lang is otherwise unused, since the language is detected locally, but a
	// reply that names neither language is not trusted.
	if result.Lang != "en" && result.Lang != "zh" {
		return nil, fmt.Errorf("unexpected language: %q", result.Lang)
	}
	return &result, nil
}

// generateSlug returns a URL slug for an English title.
func (c *llmClient) generateSlug(ctx context.Context, titleEn string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptSlug, nil)
	if err != nil {
		return "", err
	}
	raw, err := c.complete(ctx, c.titleModel, system, titleEn, maxTokensShort)
	if err != nil {
		return "", err
	}

	// sanitize.Slug turns a reply with no letters or digits into a
	// placeholder. Reject it instead, so no post gets a slug the model did
	// not choose.
	if !hasSlugChar(raw) {
		return "", fmt.Errorf("no usable slug in model output %q", raw)
	}
	return sanitize.Slug(raw, slugMaxLen), nil
}

const slugMaxLen = 60

// hasSlugChar reports whether s has a letter or digit that sanitize.Slug keeps.
func hasSlugChar(s string) bool {
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func (c *llmClient) translateTitle(ctx context.Context, title, targetLang string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	system, err := renderPrompt(promptTranslateTitle, languageData{Language: languageName(targetLang)})
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, title, maxTokensShort)
}

func (c *llmClient) translateContent(ctx context.Context, content, targetLang string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	system, err := renderPrompt(promptTranslateContent, languageData{Language: languageName(targetLang)})
	if err != nil {
		return "", err
	}
	return c.complete(ctx, c.titleModel, system, content, maxTokensLong)
}

// complete sends a system prompt and one user message, and returns the reply.
func (c *llmClient) complete(ctx context.Context, model, system, user string, maxTokens int64) (string, error) {
	return c.generate(ctx, &luxsdk.Request{
		Model:     model,
		System:    []luxsdk.Block{{Type: luxsdk.BlockText, Text: system}},
		Messages:  []luxsdk.Message{luxsdk.UserText(user)},
		MaxTokens: new(maxTokens),
	})
}

// generate sends req and returns the reply text. It logs any part of the
// request the model's dialect could not carry, such as web search.
func (c *llmClient) generate(ctx context.Context, req *luxsdk.Request) (string, error) {
	res, err := c.lux.Generate(ctx, req)
	if err != nil {
		return "", fmt.Errorf("generate: %w", err)
	}
	if len(res.Loss) > 0 {
		c.log.Printf("gateway could not carry %v for model %s", res.Loss, req.Model)
	}
	return answerText(res.Blocks), nil
}

// answerText extracts the reply from a response. After a tool call, earlier
// text blocks are the model narrating its steps, so only the last one counts.
// Without tool calls, every text block is part of the reply.
func answerText(blocks []luxsdk.Block) string {
	usedTool := false
	for _, b := range blocks {
		if b.Type != luxsdk.BlockText {
			usedTool = true
			break
		}
	}

	var text strings.Builder
	for _, b := range blocks {
		if b.Type != luxsdk.BlockText {
			continue
		}
		if usedTool {
			text.Reset()
		}
		text.WriteString(b.Text)
	}
	return strings.TrimSpace(text.String())
}
