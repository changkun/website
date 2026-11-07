// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"io/fs"
	"strings"
	"testing"
)

// sampleData is valid data for each template in prompts/.
var sampleData = map[string]any{
	promptAugment:          augmentData{Lang: "en"},
	promptTitle:            nil,
	promptImprove:          nil,
	promptDetectTranslate:  nil,
	promptSlug:             nil,
	promptTranslateTitle:   languageData{Language: "Chinese"},
	promptTranslateContent: languageData{Language: "Chinese"},
	promptIdeaInput:        ideaInputData{Title: "T", Content: "C"},
	promptReference:        referenceData{URL: "https://example.com", Text: "body"},
}

// TestRenderPromptAll renders every template, catching a field renamed in Go
// but not in the template.
func TestRenderPromptAll(t *testing.T) {
	files, err := fs.Glob(promptFS, "prompts/*.tmpl")
	if err != nil {
		t.Fatalf("glob embedded prompts: %v", err)
	}
	if len(files) != len(sampleData) {
		t.Errorf("prompts/ holds %d templates, sampleData covers %d", len(files), len(sampleData))
	}
	for _, f := range files {
		name := strings.TrimPrefix(f, "prompts/")
		t.Run(name, func(t *testing.T) {
			data, ok := sampleData[name]
			if !ok {
				t.Fatal("no entry in sampleData")
			}
			got, err := renderPrompt(name, data)
			if err != nil {
				t.Fatalf("renderPrompt() error: %v", err)
			}
			if strings.TrimSpace(got) == "" {
				t.Fatal("renderPrompt() = empty")
			}
			if strings.Contains(got, "{{") || strings.Contains(got, "}}") {
				t.Fatalf("renderPrompt() left template syntax unrendered:\n%s", got)
			}
			if strings.HasSuffix(got, "\n") {
				t.Fatal("renderPrompt() kept the file's trailing newline")
			}
		})
	}
}

// TestAugmentPrompt checks all four combinations of Lang and WebTools.
func TestAugmentPrompt(t *testing.T) {
	const (
		zhDirective = "You MUST write your ENTIRE response in Chinese"
		enDirective = "Write in the same language as the original content."
		webBlock    = "You have access to web search and web fetch tools"
		verifyWords = "search for and verify link to the canonical source"
		plainWords  = "For other references, link to the canonical source"
	)

	tests := []struct {
		name    string
		data    augmentData
		want    []string
		notWant []string
	}{
		{
			name:    "english without web tools",
			data:    augmentData{Lang: "en"},
			want:    []string{enDirective, plainWords},
			notWant: []string{zhDirective, webBlock, verifyWords},
		},
		{
			name:    "english with web tools",
			data:    augmentData{Lang: "en", WebTools: true},
			want:    []string{enDirective, webBlock, verifyWords},
			notWant: []string{zhDirective, plainWords},
		},
		{
			name:    "chinese without web tools",
			data:    augmentData{Lang: "zh"},
			want:    []string{zhDirective, plainWords},
			notWant: []string{enDirective, webBlock, verifyWords},
		},
		{
			name:    "chinese with web tools",
			data:    augmentData{Lang: "zh", WebTools: true},
			want:    []string{zhDirective, webBlock, verifyWords},
			notWant: []string{enDirective, plainWords},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := renderPrompt(promptAugment, tt.data)
			if err != nil {
				t.Fatalf("renderPrompt() error: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(got, w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, w := range tt.notWant {
				if strings.Contains(got, w) {
					t.Errorf("unexpectedly present: %q", w)
				}
			}
		})
	}
}

// TestRenderPromptKeepsContentWhitespace checks that trailing blank lines in
// user content survive rendering.
func TestRenderPromptKeepsContentWhitespace(t *testing.T) {
	got, err := renderPrompt(promptIdeaInput, ideaInputData{Title: "T", Content: "body\n\n"})
	if err != nil {
		t.Fatalf("renderPrompt() error: %v", err)
	}
	if want := "Title: T\n\nContent:\nbody\n\n"; got != want {
		t.Fatalf("renderPrompt() = %q, want %q", got, want)
	}

	got, err = renderPrompt(promptReference, referenceData{URL: "https://example.com", Text: "page\n\n"})
	if err != nil {
		t.Fatalf("renderPrompt() error: %v", err)
	}
	if !strings.HasSuffix(got, "page\n\n") {
		t.Fatalf("renderPrompt() = %q, want it to end with the fetched text unchanged", got)
	}
}
