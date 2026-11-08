// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"changkun.de/x/website/internal/githubtest"
)

// TestSanitizeCommitMsg checks that control characters and surrounding space
// are dropped, and everything else is kept.
func TestSanitizeCommitMsg(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text is untouched", in: "ideas: A Short Title", want: "ideas: A Short Title"},
		{name: "control characters are dropped", in: "ideas: a\x00b\nc\td", want: "ideas: abcd"},
		{name: "surrounding space is trimmed", in: "  ideas: title  ", want: "ideas: title"},
		{name: "chinese is preserved", in: "ideas: 中文标题", want: "ideas: 中文标题"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeCommitMsg(tt.in); got != tt.want {
				t.Fatalf("sanitizeCommitMsg(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestSanitizeCommitMsgTruncatesOnRunes checks that a long Chinese subject is
// cut on a character boundary.
func TestSanitizeCommitMsgTruncatesOnRunes(t *testing.T) {
	long := "ideas: " + strings.Repeat("标", 300)

	got := sanitizeCommitMsg(long)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitizeCommitMsg() produced invalid UTF-8: %q", got)
	}
	// The cut is marked with one ellipsis.
	if n := utf8.RuneCountInString(got); n != commitMsgMaxRunes+1 {
		t.Fatalf("rune count = %d, want %d", n, commitMsgMaxRunes+1)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("sanitizeCommitMsg() = %q, want a trailing ellipsis to mark the cut", got)
	}
}

// commit is a file the pipeline committed.
type commit struct {
	path    string
	message string
	content string
}

// fakeGitHub is a repository behind the parts of the Git Data API that
// githubClient uses. It records each committed file.
type fakeGitHub struct {
	srv *httptest.Server

	mu      sync.Mutex
	head    int
	blobs   map[string][]byte
	trees   map[string][]treeEntry
	commits map[string]fakeCommit
	files   map[string]bool // paths in the repository
	moves   int             // how many ref updates to refuse, as if the branch moved
	done    []commit
}

type treeEntry struct {
	Path    string  `json:"path"`
	SHA     *string `json:"sha"`
	Content *string `json:"content"`
}

type fakeCommit struct {
	message string
	tree    string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	f := &fakeGitHub{
		blobs:   map[string][]byte{},
		trees:   map[string][]treeEntry{},
		commits: map[string]fakeCommit{},
		files:   map[string]bool{},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve(t)))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeGitHub) serve(t *testing.T) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		p, ok := strings.CutPrefix(r.URL.Path, "/repos/changkun/blog")
		if !ok || r.Header.Get("Authorization") != "Bearer t" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		reply := func(v any) { json.NewEncoder(w).Encode(v) }
		var in map[string]json.RawMessage
		if r.Body != nil {
			json.NewDecoder(r.Body).Decode(&in)
		}
		str := func(k string) string {
			var s string
			json.Unmarshal(in[k], &s)
			return s
		}
		switch {
		case r.Method == "POST" && p == "/git/blobs":
			data, err := base64.StdEncoding.DecodeString(str("content"))
			if err != nil {
				t.Errorf("blob: %v", err)
			}
			sha := fmt.Sprintf("blob%d", len(f.blobs))
			f.blobs[sha] = data
			reply(map[string]string{"sha": sha})
		case r.Method == "GET" && strings.HasPrefix(p, "/git/blobs/"):
			data, ok := f.blobs[strings.TrimPrefix(p, "/git/blobs/")]
			if !ok {
				http.NotFound(w, r)
				return
			}
			reply(map[string]string{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"})
		case r.Method == "GET" && p == "/git/ref/heads/main":
			reply(map[string]any{"object": map[string]string{"sha": fmt.Sprint("head", f.head)}})
		case r.Method == "GET" && strings.HasPrefix(p, "/contents/"):
			at := strings.TrimPrefix(p, "/contents/")
			for name := range f.files {
				if name == at || strings.HasPrefix(name, at+"/") {
					reply(map[string]string{})
					return
				}
			}
			http.NotFound(w, r)
		case r.Method == "GET" && strings.HasPrefix(p, "/git/commits/"):
			reply(map[string]any{"tree": map[string]string{"sha": "base"}})
		case r.Method == "POST" && p == "/git/trees":
			var entries []treeEntry
			json.Unmarshal(in["tree"], &entries)
			sha := fmt.Sprintf("tree%d", len(f.trees))
			f.trees[sha] = entries
			reply(map[string]string{"sha": sha})
		case r.Method == "POST" && p == "/git/commits":
			sha := fmt.Sprintf("commit%d", len(f.commits))
			f.commits[sha] = fakeCommit{message: str("message"), tree: str("tree")}
			reply(map[string]string{"sha": sha})
		case r.Method == "PATCH" && p == "/git/refs/heads/main":
			if f.moves > 0 {
				f.moves--
				f.head++
				http.Error(w, `{"message":"Update is not a fast forward"}`, http.StatusUnprocessableEntity)
				return
			}
			c := f.commits[str("sha")]
			for _, e := range f.trees[c.tree] {
				content := ""
				if e.Content != nil {
					content = *e.Content
				} else if e.SHA != nil {
					content = string(f.blobs[*e.SHA])
				}
				f.files[e.Path] = true
				f.done = append(f.done, commit{path: e.Path, message: c.message, content: content})
			}
			f.head++
			reply(map[string]any{})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

// got returns the committed files, in the order they were committed.
func (f *fakeGitHub) got() []commit {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]commit(nil), f.done...)
}

func newTestGitHub(t *testing.T) (*githubClient, *fakeGitHub) {
	t.Helper()
	gh := newFakeGitHub(t)
	return &githubClient{token: "t", owner: "changkun", repo: "blog", name: "test",
		email: "test@changkun.de", baseURL: gh.srv.URL}, gh
}

func TestCreate(t *testing.T) {
	g, gh := newTestGitHub(t)
	ctx := context.Background()
	sha, err := g.upload(ctx, []byte("PNG"))
	if err != nil {
		t.Fatal(err)
	}
	if data, err := g.download(ctx, sha); err != nil || string(data) != "PNG" {
		t.Fatalf("download = %q, %v", data, err)
	}

	gh.moves = 1 // the first try finds the branch moved on
	files := []File{{Path: "content/posts/x/index.md", Text: "# X"}, {Path: "content/posts/x/a.png", SHA: sha}}
	if err := g.create(ctx, "content/posts/x", files, "posts: X"); err != nil {
		t.Fatal(err)
	}
	got := gh.got()
	if len(got) != 2 || got[0].content != "# X" || got[1].content != "PNG" || got[1].message != "posts: X" {
		t.Fatalf("committed %+v", got)
	}

	// A second page at the same place is refused, and nothing is committed.
	if err := g.create(ctx, "content/posts/x", files, "posts: X"); !errors.Is(err, ErrExists) {
		t.Fatalf("create over a page = %v, want ErrExists", err)
	}
	if err := g.create(ctx, "content/posts/y", files, "posts: Y"); err == nil {
		t.Fatal("create took files outside the page")
	}
	if n := len(gh.got()); n != 2 {
		t.Fatalf("committed %d files, want 2", n)
	}
}

// TestUpdate edits pages of a bare repository on disk: the markdown is
// rewritten, a file added and one deleted, in one commit; an edit of a page
// that changed since it was read is refused; and a page of one file that
// gains a file moves into a folder of its name.
func TestUpdate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("needs git")
	}
	dir := filepath.Join(t.TempDir(), "blog.git")
	err := githubtest.InitRepo(dir, map[string][]byte{
		"content/posts/x/index.md": []byte("# X\n"),
		"content/posts/x/a.png":    []byte("A"),
		"content/posts/y.md":       []byte("# Y\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(githubtest.Handler(dir))
	t.Cleanup(srv.Close)
	g := &githubClient{token: "t", owner: "changkun", repo: "blog", name: "test",
		email: "test@changkun.de", baseURL: srv.URL}
	git := func(args ...string) string {
		t.Helper()
		out, err := githubtest.Git(dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	ctx := context.Background()

	base := git("rev-parse", "main:content/posts/x/index.md")
	sha, err := g.upload(ctx, []byte("B"))
	if err != nil {
		t.Fatal(err)
	}
	files := []File{{Path: "content/posts/x/index.md", Text: "# X, edited\n"}, {Path: "content/posts/x/b.png", SHA: sha}}
	remove := []string{"content/posts/x/a.png", "content/posts/x/gone.png"}
	if err := g.update(ctx, "content/posts/x/index.md", base, files, remove, "posts: X, edited"); err != nil {
		t.Fatal(err)
	}
	if got := git("show", "main:content/posts/x/index.md"); got != "# X, edited" {
		t.Errorf("index.md = %q", got)
	}
	if got := git("ls-tree", "--name-only", "main", "content/posts/x/"); got != "content/posts/x/b.png\ncontent/posts/x/index.md" {
		t.Errorf("the folder has %q", got)
	}
	if got := git("log", "--format=%s", "main"); got != "posts: X, edited\nseed" {
		t.Errorf("log = %q", got)
	}

	// The same edit again finds the page changed, and commits nothing.
	if err := g.update(ctx, "content/posts/x/index.md", base, files, nil, "posts: again"); !errors.Is(err, ErrChanged) {
		t.Fatalf("an edit of a changed page = %v, want ErrChanged", err)
	}

	base = git("rev-parse", "main:content/posts/y.md")
	files = []File{{Path: "content/posts/y/index.md", Text: "# Y\n"}, {Path: "content/posts/y/c.png", SHA: sha}}
	if err := g.update(ctx, "content/posts/y.md", base, files, []string{"content/posts/y.md"}, "posts: Y"); err != nil {
		t.Fatal(err)
	}
	if got := git("ls-tree", "-r", "--name-only", "main", "content/posts/"); got != "content/posts/x/b.png\ncontent/posts/x/index.md\ncontent/posts/y/c.png\ncontent/posts/y/index.md" {
		t.Errorf("the posts are %q", got)
	}

	// Nothing outside the page is written.
	bad := []File{{Path: "content/posts/z.md", Text: "# Z\n"}}
	if err := g.update(ctx, "content/posts/y/index.md", git("rev-parse", "main:content/posts/y/index.md"), bad, nil, "posts: Z"); err == nil {
		t.Fatal("update wrote outside the page")
	}
	if got := git("rev-list", "--count", "main"); got != "3" {
		t.Errorf("%s commits, want 3", got)
	}
}
