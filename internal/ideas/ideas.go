// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package ideas writes to the blog repository for the blog's editor. It
// turns an idea into a bilingual post with models behind the Lux gateway
// and commits it, and commits posts as they are written.
package ideas

import (
	"cmp"
	"context"
	"log"
	"os"
	"strings"

	"latere.ai/x/pkg/luxsdk"
)

// Default models. Lux matches names exactly, so they are spelled as the
// gateway's catalog lists them; any other spelling fails with model_not_found.
const (
	defaultModel      = "anthropic/claude-opus-5.5"
	defaultTitleModel = "anthropic/claude-haiku-5.5"
)

// Service writes to the blog repository. A nil *Service has no credentials.
type Service struct {
	log    *log.Logger
	llm    *llmClient
	github *githubClient
}

// New configures the service from the environment. It returns nil if LLM_API_KEY
// or GIT_TOKEN is unset, or GIT_REPO is not owner/repo, so a host without
// secrets still serves the site.
//
// The gateway URL is always passed to luxsdk, which would otherwise read
// LUX_BASE_URL.
func New(l *log.Logger) *Service {
	llmBaseURL := cmp.Or(os.Getenv("LLM_BASE_URL"), luxsdk.DefaultBaseURL)
	llmAPIKey := os.Getenv("LLM_API_KEY")
	gitToken := os.Getenv("GIT_TOKEN")
	if llmAPIKey == "" || gitToken == "" {
		l.Println("publishing is unconfigured, LLM_API_KEY and GIT_TOKEN are required")
		return nil
	}

	gitRepo := cmp.Or(os.Getenv("GIT_REPO"), "changkun/blog")
	owner, repo, ok := strings.Cut(gitRepo, "/")
	if !ok || owner == "" || repo == "" {
		l.Printf("publishing is unconfigured, GIT_REPO must be owner/repo, got %q", gitRepo)
		return nil
	}

	return &Service{
		log: l,
		llm: &llmClient{
			lux:        luxsdk.New(llmBaseURL, luxsdk.WithAPIKey(llmAPIKey)),
			model:      cmp.Or(os.Getenv("LLM_MODEL"), defaultModel),
			titleModel: cmp.Or(os.Getenv("LLM_TITLE_MODEL"), defaultTitleModel),
			log:        l,
		},
		github: &githubClient{
			token: gitToken,
			owner: owner,
			repo:  repo,
			name:  cmp.Or(os.Getenv("GIT_COMMITTER_NAME"), "Changkun Ou"),
			email: cmp.Or(os.Getenv("GIT_COMMITTER_EMAIL"), "hi+ideas@changkun.de"),
		},
	}
}

// idea is an idea to publish: its content, and its title if it has one.
type idea struct {
	Title   string
	Content string
}

// Upload stores data in the blog repository, to be committed with a page
// later, and returns its SHA.
func (s *Service) Upload(ctx context.Context, data []byte) (string, error) {
	return s.github.upload(ctx, data)
}

// Download returns a file stored with Upload.
func (s *Service) Download(ctx context.Context, sha string) ([]byte, error) {
	return s.github.download(ctx, sha)
}

// Create commits files as a new page at root, a file or a folder of the
// blog repository. It fails with ErrExists if root is taken.
func (s *Service) Create(ctx context.Context, root string, files []File, msg string) error {
	return s.github.create(ctx, root, files, sanitizeCommitMsg(msg))
}

// PublishIdea writes up and publishes an idea in the background, with files
// beside it.
func (s *Service) PublishIdea(title, content string, files []Attachment) {
	go s.processIdea(idea{Title: title, Content: content}, files)
}
