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
	branch := cmp.Or(g.branch, "main")
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/git/ref/heads/"+branch, nil, &ref); err != nil {
		return err
	}
	head := ref.Object.SHA

	code, err := g.call(ctx, http.MethodGet, "/contents/"+escapePath(root)+"?ref="+head, nil, nil)
	switch {
	case err == nil:
		return fmt.Errorf("%s: %w", root, ErrExists)
	case code != http.StatusNotFound:
		return err
	}

	var parent struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if _, err := g.call(ctx, http.MethodGet, "/git/commits/"+head, nil, &parent); err != nil {
		return err
	}

	type entry struct {
		Path    string  `json:"path"`
		Mode    string  `json:"mode"`
		Type    string  `json:"type"`
		SHA     *string `json:"sha,omitempty"`
		Content *string `json:"content,omitempty"`
	}
	tree := make([]entry, len(files))
	for i, f := range files {
		tree[i] = entry{Path: f.Path, Mode: "100644", Type: "blob"}
		if f.SHA != "" {
			tree[i].SHA = &f.SHA
		} else {
			tree[i].Content = &f.Text
		}
	}
	var made struct {
		SHA string `json:"sha"`
	}
	in := map[string]any{"base_tree": parent.Tree.SHA, "tree": tree}
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
	code, err = g.call(ctx, http.MethodPatch, "/git/refs/heads/"+branch, map[string]any{"sha": made.SHA}, nil)
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
