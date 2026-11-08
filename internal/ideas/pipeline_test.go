// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func newPipeline(t *testing.T, g *scriptedGateway) (*Service, *fakeGitHub) {
	t.Helper()

	gh := newFakeGitHub(t)
	return &Service{
		log: log.New(io.Discard, "", 0),
		llm: g.client(),
		github: &githubClient{
			token:   "t",
			owner:   "changkun",
			repo:    "blog",
			name:    "test",
			email:   "test@changkun.de",
			baseURL: gh.srv.URL,
		},
	}, gh
}

const zhIdea = "最近整理自己的经历，推断出下面几条适用于目前阶段的公理。"

// TestProcessIdeaWithholdsOnGatewayOutage reproduces the 2026-09-01 incident:
// every model call failed, each fallback kept the source text, and a post
// titled "Untitled" with Chinese in its English block was published.
func TestProcessIdeaWithholdsOnGatewayOutage(t *testing.T) {
	g := newScriptedGateway(t) // no replies: every call answers 500
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{Content: zhIdea}, nil)

	if got := gh.got(); len(got) != 0 {
		t.Fatalf("committed %d file(s) during an outage, want none: %+v", len(got), got)
	}
}

// TestProcessIdeaWithholdsOnEchoedTranslation covers a model that echoes the
// source as its translation, which isUsableTranslateResult accepts.
func TestProcessIdeaWithholdsOnEchoedTranslation(t *testing.T) {
	echoed, err := json.Marshal(translateResult{
		Lang:              "zh",
		PolishedTitle:     "公理",
		PolishedContent:   zhIdea,
		TranslatedTitle:   "公理",
		TranslatedContent: zhIdea,
	})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}

	g := newScriptedGateway(t, string(echoed))
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{Title: "公理", Content: zhIdea}, nil)

	if got := gh.got(); len(got) != 0 {
		t.Fatalf("committed %d untranslated file(s), want none: %+v", len(got), got)
	}
}

// TestProcessIdeaWithholdsWithoutTitle checks that the run stops when no title
// can be generated.
func TestProcessIdeaWithholdsWithoutTitle(t *testing.T) {
	g := newScriptedGateway(t) // no replies: the title call fails
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{Content: zhIdea}, nil)

	if got := g.count(); got != 1 {
		t.Errorf("made %d gateway calls, want 1: the run must stop at the title", got)
	}
	if got := gh.got(); len(got) != 0 {
		t.Fatalf("committed %d file(s) without a title, want none: %+v", len(got), got)
	}
}

// TestProcessIdeaPublishesBilingual runs the combined translation path for a
// Chinese idea and checks the committed post.
func TestProcessIdeaPublishesBilingual(t *testing.T) {
	translated, err := json.Marshal(translateResult{
		Lang:              "zh",
		PolishedTitle:     "三条公理",
		PolishedContent:   zhIdea,
		TranslatedTitle:   "Three Axioms",
		TranslatedContent: "Three axioms drawn from my own experience.",
	})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}

	g := newScriptedGateway(t,
		string(translated),                  // detect, polish and translate
		"three-axioms",                      // slug
		"An English rendering of the note.", // the augmented text, translated
	)
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{
		Title:     "三条公理",
		Content:   zhIdea,
		Augmented: "这是补充说明。",
	}, nil)

	got := gh.got()
	if len(got) != 1 {
		t.Fatalf("committed %d file(s), want 1: %+v", len(got), got)
	}
	if !strings.HasSuffix(got[0].path, "-three-axioms.md") {
		t.Errorf("path = %q, want the generated slug", got[0].path)
	}
	if got[0].message != "ideas: Three Axioms" {
		t.Errorf("message = %q, want the English title", got[0].message)
	}
	en, zh := splitLanguages(t, got[0].content)
	assertContains(t, "front matter", got[0].content, `title: "Three Axioms"`, `title_zh: "三条公理"`)
	assertContains(t, "the en block", en, "Three axioms drawn", "An English rendering of the note.")
	assertContains(t, "the zh block", zh, zhIdea, "这是补充说明。")
}

// TestProcessIdeaFallbackPlacesEnglishSource runs the fallback translation path
// for an English idea and checks that each language lands in its own block.
func TestProcessIdeaFallbackPlacesEnglishSource(t *testing.T) {
	const enIdea = "Three axioms drawn from my own experience."

	g := newScriptedGateway(t,
		"not json", // detect, polish and translate: unusable
		"三条公理",     // title, translated
		"从我自己的经历中总结出的三条公理。", // content, translated
		"three-axioms", // slug
		"这是对补充说明的中文翻译。", // the augmented text, translated
	)
	svc, gh := newPipeline(t, g)

	svc.processIdea(ideaRequest{
		Title:     "Three Axioms",
		Content:   enIdea,
		Augmented: "A note added by hand.",
	}, nil)

	got := gh.got()
	if len(got) != 1 {
		t.Fatalf("committed %d file(s), want 1: %+v", len(got), got)
	}
	if got[0].message != "ideas: Three Axioms" {
		t.Errorf("message = %q, want the English title", got[0].message)
	}
	en, zh := splitLanguages(t, got[0].content)
	assertContains(t, "front matter", got[0].content, `title: "Three Axioms"`, `title_zh: "三条公理"`)
	assertContains(t, "the en block", en, enIdea, "A note added by hand.")
	assertContains(t, "the zh block", zh, "从我自己的经历中总结出的三条公理。", "这是对补充说明的中文翻译。")
	if strings.Contains(got[0].content, "generated by LLMs") {
		t.Error("a hand-written augmentation carries the generated-content disclaimer")
	}
}

// splitLanguages returns the en and zh blocks of a post.
func splitLanguages(t *testing.T, md string) (en, zh string) {
	t.Helper()
	_, rest, ok := strings.Cut(md, "{{% en %}}")
	if !ok {
		t.Fatalf("no en block in:\n%s", md)
	}
	en, zh, ok = strings.Cut(rest, "{{% zh %}}")
	if !ok {
		t.Fatalf("no zh block in:\n%s", md)
	}
	return en, zh
}

func assertContains(t *testing.T, where, s string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(s, w) {
			t.Errorf("%s is missing %q:\n%s", where, w, s)
		}
	}
}

// TestFetchURLKeepsWholeRunes checks that neither byte limit on a page splits a
// character.
func TestFetchURLKeepsWholeRunes(t *testing.T) {
	const han = "标" // 3 bytes, so a limit not divisible by 3 splits one

	tests := []struct {
		name string
		page string
		want string
	}{
		{
			// 4096 bytes is 1365 characters and one byte of the next.
			name: "cut at maxPageText",
			page: strings.Repeat(han, 2000),
			want: strings.Repeat(han, maxPageText/3),
		},
		{
			// Markup fills the read limit, leaving 4061 bytes of text: 1353
			// characters and two bytes. maxPageText does not cut.
			name: "cut at the read limit",
			page: strings.Repeat("<i></i>", 4101) + strings.Repeat(han, 2000),
			want: strings.Repeat(han, 1353),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, tt.page)
			}))
			defer srv.Close()

			got, err := fetchURL(context.Background(), srv.URL)
			if err != nil {
				t.Fatalf("fetchURL() error: %v", err)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("fetchURL() returned invalid UTF-8, ending %q", got[len(got)-4:])
			}
			if got != tt.want {
				t.Fatalf("fetchURL() = %d bytes, want %d", len(got), len(tt.want))
			}
		})
	}
}

// TestProcessIdeaWithFiles checks that an idea with files goes into a folder
// beside them, and that a file the translation dropped is still shown.
func TestProcessIdeaWithFiles(t *testing.T) {
	const withFig = zhIdea + "\n\n![](fig.png)"
	translated, err := json.Marshal(translateResult{
		Lang:              "zh",
		PolishedTitle:     "三条公理",
		PolishedContent:   withFig,
		TranslatedTitle:   "Three Axioms",
		TranslatedContent: "Three axioms drawn from my own experience.", // the figure is gone
	})
	if err != nil {
		t.Fatalf("marshal reply: %v", err)
	}
	g := newScriptedGateway(t, string(translated), "three-axioms", "An English rendering of the note.")
	svc, gh := newPipeline(t, g)
	sha, err := svc.Upload(context.Background(), []byte("PNG"))
	if err != nil {
		t.Fatal(err)
	}

	svc.processIdea(ideaRequest{Title: "三条公理", Content: withFig, Augmented: "这是补充说明。"},
		[]Attachment{{Name: "fig.png", SHA: sha}})

	got := gh.got()
	if len(got) != 2 {
		t.Fatalf("committed %d file(s), want 2: %+v", len(got), got)
	}
	if !strings.HasSuffix(got[0].path, "-three-axioms/index.md") || !strings.HasSuffix(got[1].path, "-three-axioms/fig.png") {
		t.Errorf("paths = %q, %q, want a folder with index.md and the file", got[0].path, got[1].path)
	}
	if got[1].content != "PNG" {
		t.Errorf("file = %q, want the upload", got[1].content)
	}
	en, zh := splitLanguages(t, got[0].content)
	assertContains(t, "the en block", en, "Three axioms drawn", "![](fig.png)")
	assertContains(t, "the zh block", zh, "![](fig.png)")
	if strings.Count(zh, "fig.png") != 1 {
		t.Errorf("the zh block shows the figure %d times, want once", strings.Count(zh, "fig.png"))
	}
}
