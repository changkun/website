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
	"sync"
	"testing"

	"latere.ai/x/pkg/luxsdk"
)

// scriptedGateway is a fake Lux gateway. It answers each call with the next
// scripted reply and records the request. Past the end of the script it fails
// like the real gateway.
type scriptedGateway struct {
	srv *httptest.Server

	mu      sync.Mutex
	replies []string
	reqs    []map[string]any
}

func newScriptedGateway(t *testing.T, replies ...string) *scriptedGateway {
	t.Helper()

	g := &scriptedGateway{replies: replies}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}

		g.mu.Lock()
		n := len(g.reqs)
		g.reqs = append(g.reqs, req)
		g.mu.Unlock()

		if n >= len(g.replies) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"no reply scripted"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":  "m",
			"blocks": []map[string]any{{"type": "text", "text": g.replies[n]}},
		})
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *scriptedGateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.reqs)
}

// last is the most recent request the gateway received.
func (g *scriptedGateway) last() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reqs[len(g.reqs)-1]
}

func (g *scriptedGateway) client() *llmClient {
	return &llmClient{
		lux:        luxsdk.New(g.srv.URL, luxsdk.WithAPIKey("lux_test")),
		model:      "test/model",
		titleModel: "test/title-model",
		log:        log.New(io.Discard, "", 0),
	}
}

// TestAugmentAsksForGrounding checks that augment requests web search and
// fetch, so citations come from pages the model read.
func TestAugmentAsksForGrounding(t *testing.T) {
	g := newScriptedGateway(t, "augmented")

	got, err := g.client().augment(context.Background(), "en", "A Title", "some idea")
	if err != nil {
		t.Fatalf("augment() error: %v", err)
	}
	if got != "augmented" {
		t.Fatalf("augment() = %q", got)
	}

	tools, _ := g.last()["server_tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("server_tools = %#v, want the fetch tool", g.last()["server_tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "web_fetch_20250910" || tool["name"] != "web_fetch" {
		t.Fatalf("server_tools[0] = %#v", tool)
	}
	search, _ := g.last()["web_search"].(map[string]any)
	if search["context_size"] != "medium" {
		t.Fatalf("web_search = %#v, want context_size medium", g.last()["web_search"])
	}

	system, _ := g.last()["system"].([]any)
	b, _ := system[0].(map[string]any)
	if !strings.Contains(b["text"].(string), "web search and web fetch tools") {
		t.Fatalf("system prompt does not mention the tools it was given:\n%v", b["text"])
	}
}

// TestAugmentFallsBackUngrounded checks that augment retries without tools when
// the grounded call fails.
func TestAugmentFallsBackUngrounded(t *testing.T) {
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body)
		if _, grounded := body["server_tools"]; grounded {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"tools unsupported"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":  "m",
			"blocks": []map[string]any{{"type": "text", "text": "plain augmentation"}},
		})
	}))
	defer srv.Close()

	c := &llmClient{
		lux:   luxsdk.New(srv.URL, luxsdk.WithAPIKey("lux_test")),
		model: "m",
		log:   log.New(io.Discard, "", 0),
	}
	got, err := c.augment(context.Background(), "en", "T", "C")
	if err != nil {
		t.Fatalf("augment() error: %v", err)
	}
	if got != "plain augmentation" {
		t.Fatalf("augment() = %q", got)
	}
	if len(seen) != 2 {
		t.Fatalf("gateway saw %d requests, want a grounded attempt then a plain one", len(seen))
	}
	if _, grounded := seen[1]["server_tools"]; grounded {
		t.Fatal("the fallback request still asked for tools")
	}
}

// TestAnswerText checks which text blocks count as the reply.
func TestAnswerText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []luxsdk.Block
		want   string
	}{
		{
			name:   "single text block",
			blocks: []luxsdk.Block{{Type: luxsdk.BlockText, Text: " hello "}},
			want:   "hello",
		},
		{
			name: "no tool use joins every block",
			blocks: []luxsdk.Block{
				{Type: luxsdk.BlockText, Text: "part one "},
				{Type: luxsdk.BlockText, Text: "part two"},
			},
			want: "part one part two",
		},
		{
			name: "after a tool call only the last block is the answer",
			blocks: []luxsdk.Block{
				{Type: luxsdk.BlockText, Text: "I need to search for this."},
				{Type: luxsdk.BlockToolUse, ToolUse: &luxsdk.ToolUse{ID: "t1", Name: "web_fetch"}},
				{Type: luxsdk.BlockText, Text: "Let me verify."},
				{Type: luxsdk.BlockToolUse, ToolUse: &luxsdk.ToolUse{ID: "t2", Name: "web_fetch"}},
				{Type: luxsdk.BlockText, Text: "The final answer."},
			},
			want: "The final answer.",
		},
		{
			name: "thinking is not the answer",
			blocks: []luxsdk.Block{
				{Type: luxsdk.BlockThinking, Text: "hmm"},
				{Type: luxsdk.BlockText, Text: "the answer"},
			},
			want: "the answer",
		},
		{
			name:   "no blocks",
			blocks: nil,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := answerText(tt.blocks); got != tt.want {
				t.Fatalf("answerText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDetectAndTranslateRepairsReply decodes a fenced reply with raw newlines
// inside JSON strings.
func TestDetectAndTranslateRepairsReply(t *testing.T) {
	reply := "```json\n{\n  \"lang\": \"en\",\n  \"polished_title\": \"A Title\",\n" +
		"  \"polished_content\": \"Line one.\n\nLine two.\",\n" +
		"  \"translated_title\": \"\u6807\u9898\",\n" +
		"  \"translated_content\": \"\u7b2c\u4e00\u884c\u3002\n\n\u7b2c\u4e8c\u884c\u3002\"\n}\n```"
	g := newScriptedGateway(t, reply)

	got, err := g.client().detectAndTranslate(context.Background(), "T", "C")
	if err != nil {
		t.Fatalf("detectAndTranslate() error: %v", err)
	}
	want := translateResult{
		Lang:              "en",
		PolishedTitle:     "A Title",
		PolishedContent:   "Line one.\n\nLine two.",
		TranslatedTitle:   "\u6807\u9898",
		TranslatedContent: "\u7b2c\u4e00\u884c\u3002\n\n\u7b2c\u4e8c\u884c\u3002",
	}
	if *got != want {
		t.Fatalf("detectAndTranslate()\n got: %+v\nwant: %+v", *got, want)
	}
}

// TestDetectAndTranslateRejectsUnknownLanguage rejects a reply whose lang is
// neither en nor zh.
func TestDetectAndTranslateRejectsUnknownLanguage(t *testing.T) {
	g := newScriptedGateway(t, `{"lang":"fr","polished_title":"t","polished_content":"c","translated_title":"t","translated_content":"c"}`)

	if _, err := g.client().detectAndTranslate(context.Background(), "T", "C"); err == nil {
		t.Fatal("detectAndTranslate() = nil error, want one for an unsupported language")
	}
}

// TestHasSlugChar checks which replies sanitize.Slug can turn into a slug.
func TestHasSlugChar(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{in: "expertise-risk-control", want: true},
		{in: "PBO Methods", want: true},
		{in: "42", want: true},
		{in: "", want: false},
		{in: "   ", want: false},
		{in: "!!! ??? ---", want: false},
		{in: "中文标题", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := hasSlugChar(tt.in); got != tt.want {
				t.Fatalf("hasSlugChar(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// TestPromptTasks checks the prompt, model, and token limit of each
// single-prompt task. A wrong limit does not fail; it silently cuts the reply
// off.
func TestPromptTasks(t *testing.T) {
	const (
		bigModel   = "test/model"
		smallModel = "test/title-model"
	)

	tests := []struct {
		name      string
		reply     string
		call      func(*llmClient) (string, error)
		wantModel string
		wantMax   int
		want      string
		wantIn    string // a phrase the system prompt must carry
	}{
		{
			name:      "generateTitle",
			reply:     " A Short Title ",
			call:      func(c *llmClient) (string, error) { return c.generateTitle(context.Background(), "some idea") },
			wantModel: smallModel,
			wantMax:   maxTokensShort,
			want:      "A Short Title",
			wantIn:    "concise noun phrase",
		},
		{
			name:      "improveContent rewrites a whole body",
			reply:     "improved text",
			call:      func(c *llmClient) (string, error) { return c.improveContent(context.Background(), "improvd text") },
			wantModel: smallModel,
			wantMax:   maxTokensLong,
			want:      "improved text",
			wantIn:    "Fix typos",
		},
		{
			name:  "generateSlug",
			reply: "Expertise Risk Control",
			call: func(c *llmClient) (string, error) {
				return c.generateSlug(context.Background(), "Expertise as Risk Control")
			},
			wantModel: smallModel,
			wantMax:   maxTokensShort,
			want:      "expertise-risk-control",
			wantIn:    "URL slug",
		},
		{
			name:      "translateTitle",
			reply:     "标题",
			call:      func(c *llmClient) (string, error) { return c.translateTitle(context.Background(), "Title", "zh") },
			wantModel: smallModel,
			wantMax:   maxTokensShort,
			want:      "标题",
			wantIn:    "Translate the following title to Chinese",
		},
		{
			name:      "translateContent carries a whole document",
			reply:     "内容",
			call:      func(c *llmClient) (string, error) { return c.translateContent(context.Background(), "Content", "zh") },
			wantModel: smallModel,
			wantMax:   maxTokensLong,
			want:      "内容",
			wantIn:    "Translate the following text to Chinese",
		},
		{
			name:      "augment uses the capable model",
			reply:     "deep dive",
			call:      func(c *llmClient) (string, error) { return c.augment(context.Background(), "en", "T", "C") },
			wantModel: bigModel,
			wantMax:   maxTokensLong,
			want:      "deep dive",
			wantIn:    "intellectual companion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newScriptedGateway(t, tt.reply)

			got, err := tt.call(g.client())
			if err != nil {
				t.Fatalf("error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("= %q, want %q", got, tt.want)
			}
			if g.last()["model"] != tt.wantModel {
				t.Fatalf("model = %v, want %s", g.last()["model"], tt.wantModel)
			}
			if g.last()["max_tokens"] != float64(tt.wantMax) {
				t.Fatalf("max_tokens = %v, want %d", g.last()["max_tokens"], tt.wantMax)
			}
			system, _ := g.last()["system"].([]any)
			b, _ := system[0].(map[string]any)
			if !strings.Contains(b["text"].(string), tt.wantIn) {
				t.Fatalf("system prompt missing %q:\n%v", tt.wantIn, b["text"])
			}
		})
	}
}

// TestGenerateSlugRejectsUnusableAnswer checks that a reply with no letters or
// digits is an error, not a placeholder slug.
func TestGenerateSlugRejectsUnusableAnswer(t *testing.T) {
	g := newScriptedGateway(t, "!!! ???")

	if _, err := g.client().generateSlug(context.Background(), "A Title"); err == nil {
		t.Fatal("generateSlug() = nil error, want one")
	}
}
