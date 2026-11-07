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
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"latere.ai/x/pkg/sanitize"
)

// githubClient commits files through the GitHub contents API.
type githubClient struct {
	token   string
	owner   string
	repo    string
	name    string // committer
	email   string // committer
	baseURL string // API root; empty means https://api.github.com
}

type createFileRequest struct {
	Message   string           `json:"message"`
	Content   string           `json:"content"` // base64-encoded
	Committer *githubCommitter `json:"committer,omitempty"`
}

type githubCommitter struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// createFile commits content as a new file at path.
func (g *githubClient) createFile(ctx context.Context, path, content, commitMsg string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	reqBody := createFileRequest{
		Message: commitMsg,
		Content: base64.StdEncoding.EncodeToString([]byte(content)),
		Committer: &githubCommitter{
			Name:  g.name,
			Email: g.email,
		},
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s",
		cmp.Or(g.baseURL, "https://api.github.com"), g.owner, g.repo, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GitHub API returned %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
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
