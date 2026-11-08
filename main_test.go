// Copyright 2020 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"changkun.de/x/website/internal/githubtest"
)

func TestReadIP(t *testing.T) {
	for _, tt := range []struct {
		name       string
		headers    map[string]string
		remoteAddr string
		want       string
	}{
		{
			name:       "first entry of the proxy chain wins",
			headers:    map[string]string{"X-Forwarded-For": " 203.0.113.7 , 198.51.100.1 "},
			remoteAddr: "10.0.0.1:1234",
			want:       "203.0.113.7",
		},
		{
			name:       "real ip is the fallback",
			headers:    map[string]string{"X-Forwarded-For": " , 198.51.100.1", "X-Real-Ip": " 203.0.113.8 "},
			remoteAddr: "10.0.0.1:1234",
			want:       "203.0.113.8",
		},
		{
			name:       "remote address when no proxy set a header",
			remoteAddr: "203.0.113.10:4321",
			want:       "203.0.113.10",
		},
		{
			name:       "ipv6 remote address",
			remoteAddr: "[2001:db8::1]:4321",
			want:       "2001:db8::1",
		},
		{
			name:       "unparsable remote address",
			remoteAddr: "not-an-address",
			want:       "unknown",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remoteAddr
			for k, v := range tt.headers {
				r.Header.Set(k, v)
			}
			if got := readIP(r); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestLogging(t *testing.T) {
	var out bytes.Buffer
	served := false
	h := logging(log.New(&out, "", 0))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served = true
		w.WriteHeader(http.StatusTeapot)
	}))

	r := httptest.NewRequest(http.MethodGet, "/books/", nil)
	r.RemoteAddr = "203.0.113.11:4321"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if !served {
		t.Fatal("the middleware did not call the next handler")
	}
	if w.Code != http.StatusTeapot {
		t.Errorf("got status %d, want %d", w.Code, http.StatusTeapot)
	}
	if got, want := strings.TrimSpace(out.String()), "203.0.113.11 GET /books/"; got != want {
		t.Errorf("got log line %q, want %q", got, want)
	}
}

// TestServe checks that serve answers over TCP and returns nil once ctx is
// cancelled.
func TestServe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	url := "http://" + ln.Addr().String()

	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, ln, log.New(&out, "", 0)) }()

	resp, err := http.Get(url + "/401.html")
	if err != nil {
		t.Fatalf("cannot reach the site: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("got status %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if !bytes.Equal(body, mustRead(t, "401.html")) {
		t.Error("the served body is not the embedded 401 page")
	}

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("serve reported %v, want a clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not return after its context was cancelled")
	}

	if _, err := http.Get(url + "/"); err == nil {
		t.Error("the site still answers after shutdown")
	}
	if got := out.String(); !strings.Contains(got, "shutting down") {
		t.Errorf("the shutdown was not logged, got %q", got)
	}
}

// TestNotifyShutdown checks both signals: containers stop with SIGTERM,
// terminals send SIGINT.
func TestNotifyShutdown(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			ctx, stop := notifyShutdown(context.Background())
			defer stop()

			if err := syscall.Kill(syscall.Getpid(), sig); err != nil {
				t.Fatalf("cannot send %v: %v", sig, err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(10 * time.Second):
				t.Fatalf("%v did not cancel the shutdown context", sig)
			}
		})
	}
}

// startSite builds and starts the binary with env added to the environment,
// and returns once it answers.
func startSite(t *testing.T, env ...string) (addr string, out *lockedBuffer, site *exec.Cmd) {
	t.Helper()

	bin := filepath.Join(t.TempDir(), "website")
	build := exec.Command("go", "build", "-o", bin, ".")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build the site: %v\n%s", err, b)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot reserve a port: %v", err)
	}
	addr = ln.Addr().String()
	ln.Close()

	out = &lockedBuffer{}
	site = exec.Command(bin)
	site.Env = append(append(os.Environ(), "WEBSITE_ADDR="+addr), env...)
	site.Stdout, site.Stderr = out, out
	if err := site.Start(); err != nil {
		t.Fatalf("cannot start the site: %v", err)
	}
	t.Cleanup(func() { site.Process.Kill() })

	for range 100 {
		var resp *http.Response
		if resp, err = http.Get("http://" + addr + "/"); err == nil {
			resp.Body.Close()
			return addr, out, site
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the site never came up: %v\n%s", err, out.String())
	return "", nil, nil
}

// get returns the status and body of GET path.
func get(t *testing.T, addr, path string) (int, []byte) {
	t.Helper()
	resp, err := http.Get("http://" + addr + path)
	if err != nil {
		t.Fatalf("cannot reach %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("cannot read %s: %v", path, err)
	}
	return resp.StatusCode, body
}

// TestBinaryEndToEnd runs the binary without credentials. It must still serve
// the site, and exit cleanly on SIGTERM.
func TestBinaryEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("the end to end test builds the binary")
	}

	addr, out, site := startSite(t)

	code, body := get(t, addr, "/")
	if code != http.StatusOK {
		t.Errorf("got status %d, want %d", code, http.StatusOK)
	}
	if !bytes.Equal(body, mustRead(t, "index.html")) {
		t.Error("the served body is not the embedded home page")
	}

	if err := site.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("cannot send SIGTERM: %v", err)
	}
	if err := site.Wait(); err != nil {
		t.Fatalf("the site exited with %v\n%s", err, out.String())
	}
	if got := out.String(); !strings.Contains(got, "goodbye!") {
		t.Errorf("the site did not shut down gracefully, got %q", got)
	}
}

// TestBinaryServesSiteWithCredentials runs the binary with the publishing
// credentials, and checks that it serves the site with publishing set up.
func TestBinaryServesSiteWithCredentials(t *testing.T) {
	if testing.Short() {
		t.Skip("the end to end test builds the binary")
	}

	addr, out, _ := startSite(t,
		"LLM_BASE_URL=https://lux.example",
		"LLM_API_KEY=key",
		"GIT_TOKEN=token",
	)

	code, body := get(t, addr, "/")
	if code != http.StatusOK {
		t.Errorf("GET / = %d, want %d", code, http.StatusOK)
	}
	if !bytes.Equal(body, mustRead(t, "index.html")) {
		t.Error("the served body is not the embedded home page")
	}

	if got := out.String(); strings.Contains(got, "publishing is unconfigured") {
		t.Errorf("the site did not set up publishing:\n%s", got)
	}
}

// lockedBuffer collects subprocess output, which the test reads while the
// process writes it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestServeSites checks that serve answers a site of WWW_SITES before
// changkun.de's routes, so neither /blog/ nor /library/ answer for it.
func TestServeSites(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("golang.design"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WWW_SITES", "golang.design="+dir)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cannot listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- serve(ctx, ln, log.New(io.Discard, "", 0)) }()
	defer func() {
		cancel()
		<-served
	}()

	for path, want := range map[string]string{
		"/":         "200 golang.design",
		"/blog/":    "404 404 page not found\n",
		"/library/": "404 404 page not found\n",
	} {
		r, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Host = "golang.design"
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatalf("cannot reach the site: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if got := strconv.Itoa(resp.StatusCode) + " " + string(body); got != want {
			t.Errorf("golang.design%s: got %q, want %q", path, got, want)
		}
	}
}

// TestBinaryEditsPost runs the binary on a blog checked out of a bare
// repository, with GitHub's API served from that repository, and edits a
// post as the owner, here anyone on this machine: the post is rewritten, a
// file added and one deleted in one commit to the repository, and an edit
// of the post as it was before is refused.
func TestBinaryEditsPost(t *testing.T) {
	if testing.Short() {
		t.Skip("the end to end test builds the binary")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the end to end test needs git")
	}

	tmp := t.TempDir()
	bare := filepath.Join(tmp, "blog.git")
	files := map[string][]byte{}
	err := filepath.WalkDir("internal/blog/testdata/site", func(name string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(name)
		files[filepath.ToSlash(strings.TrimPrefix(name, "internal/blog/testdata/site/"))] = b
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := githubtest.InitRepo(bare, files); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(tmp, "www", "blog")
	clone := exec.Command("git", "clone", "-q", bare, checkout)
	clone.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := clone.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	repo := func(args ...string) string {
		t.Helper()
		out, err := githubtest.Git(bare, args...)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	api := httptest.NewServer(githubtest.Handler(bare))
	t.Cleanup(api.Close)

	addr, out, _ := startSite(t,
		"WWW_DIR="+filepath.Join(tmp, "www"),
		"LIBRARY_DIR="+filepath.Join(tmp, "data"),
		"LIBRARY_DEV_OWNER=1",
		"GIT_TOKEN=t",
		"GIT_API_URL="+api.URL,
		"LLM_BASE_URL=https://lux.example",
		"LLM_API_KEY=k",
	)
	post := func(path, ctype string, body []byte) (int, []byte) {
		t.Helper()
		resp, err := http.Post("http://"+addr+path, ctype, bytes.NewReader(body))
		if err != nil {
			t.Fatalf("cannot reach %s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}

	code, body := get(t, addr, "/blog/posts/a-bundle/")
	if code != http.StatusOK || !bytes.Contains(body, []byte(`<a class="article-edit" href="/blog/edit/posts/a-bundle/"`)) {
		t.Fatalf("GET the post = %d, without the edit link\n%s", code, out.String())
	}
	code, body = get(t, addr, "/blog/edit/posts/a-bundle/")
	_, after, _ := bytes.Cut(body, []byte(`<script type="application/json" id="editor-entry">`))
	js, _, _ := bytes.Cut(after, []byte("</script>"))
	var entry struct {
		Base  string `json:"base"`
		Files []struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	if code != http.StatusOK || json.Unmarshal(js, &entry) != nil || len(entry.Files) != 2 {
		t.Fatalf("GET the editor = %d:\n%.500s", code, body)
	}
	if want := repo("rev-parse", "main:content/posts/2021-02-02-bundle/index.md"); entry.Base != want {
		t.Fatalf("the editor opened %s, the repository has %s", entry.Base, want)
	}

	code, body = post("/blog/new/files?name=two.png", "application/octet-stream", []byte("PNG2"))
	var up struct{ Name, SHA string }
	if code != http.StatusOK || json.Unmarshal(body, &up) != nil {
		t.Fatalf("upload = %d %s", code, body)
	}
	edit, _ := json.Marshal(map[string]any{
		"kind": "post", "title": "A Bundle, Edited", "tags": []string{"go"}, "body": "![two](two.png)\n\nEdited.\n",
		"files": []map[string]string{{"name": "figures.js"}, {"name": up.Name, "sha": up.SHA}}, "base": entry.Base,
	})
	if code, body := post("/blog/edit/posts/a-bundle/", "application/json", edit); code != http.StatusOK {
		t.Fatalf("save = %d %s\n%s", code, body, out.String())
	}

	md := repo("show", "main:content/posts/2021-02-02-bundle/index.md")
	for _, want := range []string{"\ndate: 2021-02-02T08:00:00+01:00\nlastmod: ", "\ntitle: A Bundle, Edited\nslug: /posts/a-bundle\n",
		"\n---\n\n![two](two.png)\n\nEdited."} {
		if !strings.Contains(md, want) {
			t.Errorf("the committed post lacks %q:\n%s", want, md)
		}
	}
	if got := repo("ls-tree", "--name-only", "main", "content/posts/2021-02-02-bundle/"); got !=
		"content/posts/2021-02-02-bundle/figures.js\ncontent/posts/2021-02-02-bundle/index.md\ncontent/posts/2021-02-02-bundle/two.png" {
		t.Errorf("the post's folder has\n%s", got)
	}
	if got := repo("show", "main:content/posts/2021-02-02-bundle/two.png"); got != "PNG2" {
		t.Errorf("two.png = %q", got)
	}
	if got := repo("log", "--format=%s", "main"); got != "posts: A Bundle, Edited\nseed" {
		t.Errorf("the log is\n%s", got)
	}

	// The checkout has not pulled, so an edit of the post as it was would
	// undo the one just saved; it is refused.
	if code, body := post("/blog/edit/posts/a-bundle/", "application/json", edit); code != http.StatusConflict ||
		!bytes.Contains(body, []byte("changed in the blog's repository")) {
		t.Errorf("a second save = %d %s", code, body)
	}
	if got := repo("rev-list", "--count", "main"); got != "2" {
		t.Errorf("%s commits, want 2", got)
	}
}
