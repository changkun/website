// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package githubtest serves, from a bare git repository on disk, the parts
// of GitHub's REST API that the blog's publishing uses: blobs, trees,
// commits, the branch, and whether a path is there. Tests point the
// publisher at it, so they commit to a repository they can look into, and
// never to GitHub.
package githubtest

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Handler serves the API at /repos/<owner>/<repo>/ for the bare repository
// dir, whatever the owner, repository and token.
func Handler(dir string) http.Handler {
	s := &server{dir: dir}
	return http.HandlerFunc(s.serve)
}

// Git runs git on the bare repository dir, with neither the system's nor
// the user's configuration, and returns what it prints.
func Git(dir string, args ...string) ([]byte, error) {
	return run(dir, nil, nil, args...)
}

// InitRepo makes a bare repository at dir, with branch main at one commit
// of files, by their paths.
func InitRepo(dir string, files map[string][]byte) error {
	// Not init -b, which git before 2.28 does not know.
	if _, err := run("", nil, nil, "init", "--bare", dir); err != nil {
		return err
	}
	if _, err := run(dir, nil, nil, "symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	index := filepath.Join(dir, "seed.index")
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sha, err := run(dir, files[name], nil, "hash-object", "-w", "--stdin")
		if err != nil {
			return err
		}
		if _, err := run(dir, nil, env, "update-index", "--add", "--cacheinfo", "100644,"+trim(sha)+","+name); err != nil {
			return err
		}
	}
	tree, err := run(dir, nil, env, "write-tree")
	if err != nil {
		return err
	}
	commit, err := run(dir, nil, author("test", "test@changkun.de"), "commit-tree", "--no-gpg-sign", trim(tree), "-m", "seed")
	if err != nil {
		return err
	}
	_, err = run(dir, nil, nil, "update-ref", "refs/heads/main", trim(commit))
	return err
}

func run(dir string, stdin []byte, env []string, args ...string) ([]byte, error) {
	if dir != "" {
		args = append([]string{"--git-dir", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return out, nil
}

func trim(b []byte) string { return strings.TrimSpace(string(b)) }

func author(name, email string) []string {
	return []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
}

type server struct {
	dir string
	mu  sync.Mutex
}

// An entry is an entry of a tree, as GitHub lists it.
type entry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reply := func(code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	fail := func(code int, msg string) { reply(code, map[string]string{"message": msg}) }
	git := func(stdin []byte, env []string, args ...string) (string, error) {
		out, err := run(s.dir, stdin, env, args...)
		return trim(out), err
	}

	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/repos/"), "/", 3)
	if !strings.HasPrefix(r.URL.Path, "/repos/") || len(parts) != 3 {
		fail(http.StatusNotFound, "Not Found")
		return
	}
	p := "/" + parts[2]
	var in map[string]json.RawMessage
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&in)
	}
	str := func(k string) string {
		var v string
		json.Unmarshal(in[k], &v)
		return v
	}

	switch {
	case r.Method == http.MethodPost && p == "/git/blobs":
		data := []byte(str("content"))
		if str("encoding") == "base64" {
			var err error
			if data, err = base64.StdEncoding.DecodeString(str("content")); err != nil {
				fail(http.StatusUnprocessableEntity, err.Error())
				return
			}
		}
		sha, err := git(data, nil, "hash-object", "-w", "--stdin")
		if err != nil {
			fail(http.StatusInternalServerError, err.Error())
			return
		}
		reply(http.StatusCreated, map[string]string{"sha": sha})

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/blobs/"):
		data, err := run(s.dir, nil, nil, "cat-file", "blob", strings.TrimPrefix(p, "/git/blobs/"))
		if err != nil {
			fail(http.StatusNotFound, "Not Found")
			return
		}
		reply(http.StatusOK, map[string]any{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64", "size": len(data)})

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/ref/heads/"):
		ref := "refs/heads/" + strings.TrimPrefix(p, "/git/ref/heads/")
		sha, err := git(nil, nil, "rev-parse", "--verify", ref)
		if err != nil {
			fail(http.StatusNotFound, "Not Found")
			return
		}
		reply(http.StatusOK, map[string]any{"ref": ref, "object": map[string]string{"type": "commit", "sha": sha}})

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/commits/"):
		sha := strings.TrimPrefix(p, "/git/commits/")
		tree, err := git(nil, nil, "rev-parse", "--verify", sha+"^{tree}")
		if err != nil {
			fail(http.StatusNotFound, "Not Found")
			return
		}
		reply(http.StatusOK, map[string]any{"sha": sha, "tree": map[string]string{"sha": tree}})

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/git/trees/"):
		sha := strings.TrimPrefix(p, "/git/trees/")
		args := []string{"ls-tree", "-z", sha}
		if r.URL.Query().Get("recursive") != "" {
			args = []string{"ls-tree", "-z", "-r", "-t", sha}
		}
		out, err := run(s.dir, nil, nil, args...)
		if err != nil {
			fail(http.StatusNotFound, "Not Found")
			return
		}
		reply(http.StatusOK, map[string]any{"sha": sha, "tree": entries(out), "truncated": false})

	case r.Method == http.MethodGet && strings.HasPrefix(p, "/contents/"):
		name := strings.TrimPrefix(p, "/contents/")
		ref := r.URL.Query().Get("ref")
		if ref == "" {
			ref = "refs/heads/main"
		}
		kind, err := git(nil, nil, "cat-file", "-t", ref+":"+name)
		if err != nil {
			fail(http.StatusNotFound, "Not Found")
			return
		}
		sha, err := git(nil, nil, "rev-parse", ref+":"+name)
		if err != nil {
			fail(http.StatusInternalServerError, err.Error())
			return
		}
		if kind == "tree" {
			out, err := run(s.dir, nil, nil, "ls-tree", "-z", sha)
			if err != nil {
				fail(http.StatusInternalServerError, err.Error())
				return
			}
			reply(http.StatusOK, entries(out))
			return
		}
		reply(http.StatusOK, map[string]string{"type": "file", "path": name, "sha": sha})

	case r.Method == http.MethodPost && p == "/git/trees":
		var tree []map[string]json.RawMessage
		if err := json.Unmarshal(in["tree"], &tree); err != nil {
			fail(http.StatusUnprocessableEntity, "tree: "+err.Error())
			return
		}
		index, err := os.CreateTemp("", "githubtest-index-*")
		if err != nil {
			fail(http.StatusInternalServerError, err.Error())
			return
		}
		index.Close()
		os.Remove(index.Name()) // git makes it; an empty file is no index
		defer os.Remove(index.Name())
		env := []string{"GIT_INDEX_FILE=" + index.Name()}
		if base := str("base_tree"); base != "" {
			if _, err := git(nil, env, "read-tree", base); err != nil {
				fail(http.StatusUnprocessableEntity, err.Error())
				return
			}
		}
		for _, e := range tree {
			var name, mode string
			json.Unmarshal(e["path"], &name)
			json.Unmarshal(e["mode"], &mode)
			if mode == "" {
				mode = "100644"
			}
			sha, hasSHA := e["sha"]
			content, hasContent := e["content"]
			switch {
			case hasSHA && hasContent:
				fail(http.StatusUnprocessableEntity, name+": both sha and content")
				return
			case hasContent:
				var text string
				json.Unmarshal(content, &text)
				blob, err := git([]byte(text), nil, "hash-object", "-w", "--stdin")
				if err == nil {
					_, err = git(nil, env, "update-index", "--add", "--cacheinfo", mode+","+blob+","+name)
				}
				if err != nil {
					fail(http.StatusUnprocessableEntity, err.Error())
					return
				}
			case hasSHA && string(sha) == "null":
				// Mode 0 takes the path out; --force-remove wants a work tree.
				zero := "0 " + strings.Repeat("0", 40) + "\t" + name + "\n"
				if _, err := git([]byte(zero), env, "update-index", "--index-info"); err != nil {
					fail(http.StatusUnprocessableEntity, err.Error())
					return
				}
			case hasSHA:
				var blob string
				json.Unmarshal(sha, &blob)
				if _, err := git(nil, nil, "cat-file", "-e", blob); err != nil {
					fail(http.StatusUnprocessableEntity, name+": no blob "+blob)
					return
				}
				if _, err := git(nil, env, "update-index", "--add", "--cacheinfo", mode+","+blob+","+name); err != nil {
					fail(http.StatusUnprocessableEntity, err.Error())
					return
				}
			default:
				fail(http.StatusUnprocessableEntity, name+": neither sha nor content")
				return
			}
		}
		sha, err := git(nil, env, "write-tree")
		if err != nil {
			fail(http.StatusInternalServerError, err.Error())
			return
		}
		reply(http.StatusCreated, map[string]string{"sha": sha})

	case r.Method == http.MethodPost && p == "/git/commits":
		var parents []string
		json.Unmarshal(in["parents"], &parents)
		var who struct{ Name, Email string }
		json.Unmarshal(in["committer"], &who)
		if who.Name == "" {
			who.Name, who.Email = "test", "test@changkun.de"
		}
		args := []string{"commit-tree", "--no-gpg-sign", str("tree"), "-m", str("message")}
		for _, p := range parents {
			args = append(args, "-p", p)
		}
		sha, err := git(nil, author(who.Name, who.Email), args...)
		if err != nil {
			fail(http.StatusUnprocessableEntity, err.Error())
			return
		}
		reply(http.StatusCreated, map[string]string{"sha": sha})

	case r.Method == http.MethodPatch && strings.HasPrefix(p, "/git/refs/heads/"):
		ref := "refs/heads/" + strings.TrimPrefix(p, "/git/refs/heads/")
		old, err := git(nil, nil, "rev-parse", "--verify", ref)
		if err != nil {
			fail(http.StatusUnprocessableEntity, "Reference does not exist")
			return
		}
		var force bool
		json.Unmarshal(in["force"], &force)
		if _, err := git(nil, nil, "merge-base", "--is-ancestor", old, str("sha")); err != nil && !force {
			fail(http.StatusUnprocessableEntity, "Update is not a fast forward")
			return
		}
		if _, err := git(nil, nil, "update-ref", ref, str("sha"), old); err != nil {
			fail(http.StatusUnprocessableEntity, err.Error())
			return
		}
		reply(http.StatusOK, map[string]any{"ref": ref, "object": map[string]string{"type": "commit", "sha": str("sha")}})

	default:
		fail(http.StatusNotFound, "Not Found: "+r.Method+" "+p)
	}
}

// entries reads the output of git ls-tree -z.
func entries(out []byte) []entry {
	es := []entry{}
	for line := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 3 {
			continue
		}
		es = append(es, entry{Path: name, Mode: f[0], Type: f[1], SHA: f[2]})
	}
	return es
}
