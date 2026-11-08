// Copyright 2026 Changkun Ou. All rights reserved.
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
	"sync/atomic"
	"testing"

	"latere.ai/x/pkg/luxsdk"
)

// answeringGateway is a fake Lux gateway that answers each call from what
// its user message says, since the parts of a post are sent side by side.
func answeringGateway(t *testing.T, answer func(system, user string) string) (*Service, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		calls.Add(1)
		system, user := texts(req["system"]), texts(req["messages"])
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":  "m",
			"blocks": []map[string]any{{"type": "text", "text": answer(system, user)}},
		})
	}))
	t.Cleanup(srv.Close)
	return &Service{log: log.New(io.Discard, "", 0), llm: &llmClient{
		lux:        luxsdk.New(srv.URL, luxsdk.WithAPIKey("lux_test")),
		model:      "test/model",
		titleModel: "test/title-model",
		log:        log.New(io.Discard, "", 0),
	}}, &calls
}

// texts joins the "text" strings found anywhere in v.
func texts(v any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if s, ok := v["text"].(string); ok {
				b.WriteString(s)
			}
			for k, c := range v {
				if k != "text" {
					walk(c)
				}
			}
		case []any:
			for _, c := range v {
				walk(c)
			}
		}
	}
	walk(v)
	return b.String()
}

func TestSplitPost(t *testing.T) {
	code := "```go\nfunc main() {\n\n\tprintln(1)\n}\n```"
	math := "$$\na^2\n\n+ b^2\n$$"
	para := strings.Repeat("A sentence of the post that goes on. ", 20) // about 740 bytes
	body := "# One\n\n" + para + "\n\n" + code + "\n\n" + para + "\n\n\n" + math + "\n\n## Two\n\n" + para + "\n\n" + para
	parts := splitPost(body, 2000)
	if len(parts) < 2 {
		t.Fatalf("split into %d parts, want more", len(parts))
	}
	joined := strings.Join(parts, "\n\n")
	if want := strings.TrimSpace(strings.ReplaceAll(body, "\n\n\n", "\n\n")); joined != want {
		t.Errorf("the parts do not make the post again:\n%s", joined)
	}
	for _, p := range parts {
		if strings.Count(p, "```")%2 != 0 || strings.Count(p, "$$")%2 != 0 {
			t.Errorf("a part cuts a block of code or math:\n%s", p)
		}
	}
	if !strings.HasPrefix(parts[len(parts)-1], "## Two") && !strings.Contains(strings.Join(parts[1:], "|"), "|## Two") {
		t.Errorf("## Two does not start a part: %q", parts)
	}
}

func TestTranslatePost(t *testing.T) {
	para := func(tag string) string { return tag + " " + strings.Repeat("words of the post in English. ", 60) }
	// A block of code longer than a part is a part of its own, and is not
	// sent to the model.
	code := "```go\n" + strings.Repeat("func main() { println(\"hello, world\") }\n", 160) + "```"
	body := para("P1") + "\n\n" + code + "\n\n## Next\n\n" + para("P2") + "\n\n" + para("P3")

	var mu sync.Mutex
	var progress []int
	s, calls := answeringGateway(t, func(system, user string) string {
		if user == "A Title" {
			return "“一个标题”"
		}
		if strings.Contains(user, "func main") {
			t.Error("the code went to the model")
		}
		return strings.ReplaceAll(user, "words of the post in English. ", "这是翻译成中文的文章内容。")
	})
	p, err := s.TranslatePost(context.Background(), "A Title", "", body, func(done, total int) {
		mu.Lock()
		progress = append(progress, done)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "A Title" || p.TitleZh != "一个标题" {
		t.Errorf("titles = %q, %q", p.Title, p.TitleZh)
	}
	if p.En != strings.TrimSpace(body) {
		t.Errorf("the English is not the text as written")
	}
	if !strings.Contains(p.Zh, code) {
		t.Errorf("the code did not stay as it was:\n%s", p.Zh)
	}
	if i1, i2, i3 := strings.Index(p.Zh, "P1 "), strings.Index(p.Zh, "P2 "), strings.Index(p.Zh, "P3 "); !(i1 >= 0 && i1 < i2 && i2 < i3) {
		t.Errorf("the parts are out of order:\n%s", p.Zh)
	}
	parts := splitPost(body, postPart)
	if want := int32(len(parts) - 1 + 1); len(parts) != 3 || calls.Load() != want { // the code part is not sent
		t.Errorf("%d model calls for %d parts and a title", calls.Load(), len(parts))
	}
	if len(progress) == 0 || progress[len(progress)-1] != len(parts) {
		t.Errorf("progress = %v, want to end at %d", progress, len(parts))
	}
}

// A post written in Chinese, its title typed in the title field, keeps the
// title as its Chinese one, and gets an English one.
func TestTranslatePostFromChinese(t *testing.T) {
	body := strings.Repeat("这是一篇用中文写的文章，内容很长。", 30)
	s, _ := answeringGateway(t, func(system, user string) string {
		if user == "论信任" {
			return "On Trust"
		}
		return strings.Repeat("This is a post written in Chinese, and it is long. ", 20)
	})
	p, err := s.TranslatePost(context.Background(), "论信任", "", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "On Trust" || p.TitleZh != "论信任" || p.Zh != body || !strings.HasPrefix(p.En, "This is a post") {
		t.Errorf("post = %+v", p)
	}
}

// A model that sends the text back as it came, or in the wrong language,
// fails the whole post: nothing half translated is published.
func TestTranslatePostRejectsEcho(t *testing.T) {
	body := strings.Repeat("Words of the post in English, long enough to judge. ", 10)
	for name, answer := range map[string]func(string, string) string{
		"echo":  func(_, user string) string { return user },
		"wrong": func(_, user string) string { return "Still English, " + user },
		"empty": func(_, _ string) string { return "" },
	} {
		s, _ := answeringGateway(t, answer)
		if _, err := s.TranslatePost(context.Background(), "A Title", "一个标题", body, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestUnwrapFence(t *testing.T) {
	if got := unwrapFence("```markdown\n你好\n```", "Hello"); got != "你好" {
		t.Errorf("unwrapFence = %q", got)
	}
	if got := unwrapFence("```go\nx\n```", "```go\nx\n```"); got != "```go\nx\n```" {
		t.Errorf("unwrapFence took off a fence the part had: %q", got)
	}
}
