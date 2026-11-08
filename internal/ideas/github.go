// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
	"unicode"

	"latere.ai/x/pkg/sanitize"
)

// githubClient writes to the blog repository through GitHub's Git Data API,
// so that a page and its files land in one commit.
type githubClient struct {
	token   string
	owner   string
	repo    string
	branch  string // empty means main
	name    string // committer
	email   string // committer
	baseURL string // API root; empty means https://api.github.com
}

// A File is a file of a new page.
type File struct {
	Path string // in the repository
	SHA  string // of a blob made with Upload; empty to commit Text
	Text string
}

// ErrExists reports that a page is already where a new one would go.
var ErrExists = errors.New("a page is already there")

// ErrChanged reports that a page changed in the repository since it was
// read, so that committing an edit of it would undo the change.
var ErrChanged = errors.New("the page changed since it was opened")

// errMoved reports that the branch moved on while a commit was being made.
var errMoved = errors.New("the branch moved on")

// call sends a request to the API, with in as its JSON body unless nil, and
// decodes the reply into out unless nil. It reports the status for replies
// it does not take as success.
func (g *githubClient) call(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	u := cmp.Or(g.baseURL, "https://api.github.com") + "/repos/" + g.owner + "/" + g.repo + path
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return resp.StatusCode, fmt.Errorf("GitHub %s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("GitHub %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// upload stores data as a blob, to be committed later, and returns its SHA.
func (g *githubClient) upload(ctx context.Context, data []byte) (string, error) {
	var out struct {
		SHA string `json:"sha"`
	}
	in := map[string]string{"content": base64.StdEncoding.EncodeToString(data), "encoding": "base64"}
	if _, err := g.call(ctx, http.MethodPost, "/git/blobs", in, &out); err != nil {
		return "", err
	}
	return out.SHA, nil
}

// download returns the blob sha.
func (g *githubClient) download(ctx context.Context, sha string) ([]byte, error) {
	var out struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/git/blobs/"+url.PathEscape(sha), nil, &out); err != nil {
		return nil, err
	}
	if out.Encoding != "base64" {
		return []byte(out.Content), nil
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(out.Content, "\n", ""))
}

// create commits files, which make up a new page at root, a file or a
// folder. It fails with ErrExists if root is in the repository already,
// and tries again if the branch moves on while it commits.
func (g *githubClient) create(ctx context.Context, root string, files []File, msg string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for _, f := range files {
		if f.Path != root && !strings.HasPrefix(f.Path, root+"/") {
			return fmt.Errorf("%s is not in %s", f.Path, root)
		}
	}
	var err error
	for range 3 {
		if err = g.commit(ctx, root, files, msg); !errors.Is(err, errMoved) {
			return err
		}
	}
	return err
}

func (g *githubClient) commit(ctx context.Context, root string, files []File, msg string) error {
	head, tree, err := g.head(ctx)
	if err != nil {
		return err
	}
	code, err := g.call(ctx, http.MethodGet, "/contents/"+escapePath(root)+"?ref="+head, nil, nil)
	switch {
	case err == nil:
		return fmt.Errorf("%s: %w", root, ErrExists)
	case code != http.StatusNotFound:
		return err
	}
	return g.commitTree(ctx, head, tree, treeEntries(files, nil), msg)
}

// update commits files over the page whose markdown is file, and deletes
// the paths of remove, in one commit. It fails with ErrChanged if file is
// no longer the blob base, and tries again if the branch moves on while it
// commits. Every path must be the page's: its file, or one in its folder,
// which for a page of one file is the folder of that name it moves to.
func (g *githubClient) update(ctx context.Context, file, base string, files []File, remove []string, msg string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	root := strings.TrimSuffix(file, ".md")
	if path.Base(file) == "index.md" {
		root = path.Dir(file)
	}
	paths := slices.Clone(remove)
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	for _, p := range paths {
		if path.Clean(p) != p || p != file && !strings.HasPrefix(p, root+"/") {
			return fmt.Errorf("%s is not in %s", p, root)
		}
	}
	var err error
	for range 3 {
		if err = g.replace(ctx, file, base, files, remove, msg); !errors.Is(err, errMoved) {
			return err
		}
	}
	return err
}

func (g *githubClient) replace(ctx context.Context, file, base string, files []File, remove []string, msg string) error {
	head, tree, err := g.head(ctx)
	if err != nil {
		return err
	}
	dir, name := path.Dir(file), path.Base(file)
	have, err := g.blobs(ctx, tree, dir, name == "index.md")
	if err != nil {
		return err
	}
	if base == "" || have[name] != base {
		return fmt.Errorf("%s: %w", file, ErrChanged)
	}
	// A file that is gone already is not deleted again.
	var gone []string
	for _, p := range remove {
		if _, ok := have[strings.TrimPrefix(p, dir+"/")]; ok {
			gone = append(gone, p)
		}
	}
	return g.commitTree(ctx, head, tree, treeEntries(files, gone), msg)
}

// head returns the commit the branch is at, and its tree.
func (g *githubClient) head(ctx context.Context) (commit, tree string, err error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/git/ref/heads/"+cmp.Or(g.branch, "main"), nil, &ref); err != nil {
		return "", "", err
	}
	var c struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/git/commits/"+ref.Object.SHA, nil, &c); err != nil {
		return "", "", err
	}
	return ref.Object.SHA, c.Tree.SHA, nil
}

// blobs returns the files of the folder dir in tree, by their path in it,
// and those of its folders too if deep. It is empty if dir is not there.
func (g *githubClient) blobs(ctx context.Context, tree, dir string, deep bool) (map[string]string, error) {
	type entry struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	list := func(sha string, deep bool) ([]entry, error) {
		var out struct {
			Tree      []entry `json:"tree"`
			Truncated bool    `json:"truncated"`
		}
		p := "/git/trees/" + url.PathEscape(sha)
		if deep {
			p += "?recursive=1"
		}
		if _, err := g.call(ctx, http.MethodGet, p, nil, &out); err != nil {
			return nil, err
		}
		if out.Truncated {
			return nil, fmt.Errorf("GitHub listed only part of the tree %s", sha)
		}
		return out.Tree, nil
	}
	m := make(map[string]string)
	for seg := range strings.SplitSeq(dir, "/") {
		es, err := list(tree, false)
		if err != nil {
			return nil, err
		}
		tree = ""
		for _, e := range es {
			if e.Path == seg && e.Type == "tree" {
				tree = e.SHA
			}
		}
		if tree == "" {
			return m, nil
		}
	}
	es, err := list(tree, deep)
	if err != nil {
		return nil, err
	}
	for _, e := range es {
		if e.Type == "blob" {
			m[e.Path] = e.SHA
		}
	}
	return m, nil
}

// treeEntries are the entries of a tree, over a base tree, that write files
// and delete the paths of remove.
func treeEntries(files []File, remove []string) []map[string]any {
	tree := make([]map[string]any, 0, len(files)+len(remove))
	for _, f := range files {
		e := map[string]any{"path": f.Path, "mode": "100644", "type": "blob"}
		if f.SHA != "" {
			e["sha"] = f.SHA
		} else {
			e["content"] = f.Text
		}
		tree = append(tree, e)
	}
	for _, p := range remove {
		// A null sha, which has to be there, deletes the path.
		tree = append(tree, map[string]any{"path": p, "mode": "100644", "type": "blob", "sha": nil})
	}
	return tree
}

// commitTree commits tree, over base, the tree of head, and moves the
// branch from head to the commit. It fails with errMoved if the branch is
// no longer at head.
func (g *githubClient) commitTree(ctx context.Context, head, base string, tree []map[string]any, msg string) error {
	var made struct {
		SHA string `json:"sha"`
	}
	in := map[string]any{"base_tree": base, "tree": tree}
	if _, err := g.call(ctx, http.MethodPost, "/git/trees", in, &made); err != nil {
		return err
	}
	in = map[string]any{
		"message":   msg,
		"tree":      made.SHA,
		"parents":   []string{head},
		"committer": map[string]string{"name": g.name, "email": g.email},
	}
	if _, err := g.call(ctx, http.MethodPost, "/git/commits", in, &made); err != nil {
		return err
	}
	code, err := g.call(ctx, http.MethodPatch, "/git/refs/heads/"+cmp.Or(g.branch, "main"), map[string]any{"sha": made.SHA}, nil)
	if code == http.StatusUnprocessableEntity || code == http.StatusConflict {
		return errMoved
	}
	return err
}

// escapePath escapes each segment of a path in the repository.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// commitMsgMaxRunes bounds the commit subject. It counts runes, so the cut
// never splits a character.
const commitMsgMaxRunes = 200

// sanitizeCommitMsg drops control characters, trims space, and truncates s.
func sanitizeCommitMsg(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return sanitize.Truncate(strings.TrimSpace(b.String()), commitMsgMaxRunes)
}
