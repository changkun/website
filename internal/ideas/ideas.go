// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package ideas writes to the blog repository. It turns an idea into a
// bilingual post with models behind the Lux gateway and commits it, both
// for its API and for the blog's editor, which also commits posts through
// it.
package ideas

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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

// Prefix is the path the API is mounted at. The mux does not strip it, so the
// routes include it.
const Prefix = "/ideas/"

// Service is the configured API. A nil *Service has no credentials, and
// Handler answers it with 503.
type Service struct {
	log    *log.Logger
	llm    *llmClient
	github *githubClient
}

// New configures the API from the environment. It returns nil if LLM_API_KEY
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
		l.Println("ideas API is unconfigured, LLM_API_KEY and GIT_TOKEN are required")
		return nil
	}

	gitRepo := cmp.Or(os.Getenv("GIT_REPO"), "changkun/blog")
	owner, repo, ok := strings.Cut(gitRepo, "/")
	if !ok || owner == "" || repo == "" {
		l.Printf("ideas API is unconfigured, GIT_REPO must be owner/repo, got %q", gitRepo)
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
			name:  cmp.Or(os.Getenv("GIT_COMMITTER_NAME"), "Changkun Ideas API Server"),
			email: cmp.Or(os.Getenv("GIT_COMMITTER_EMAIL"), "hi+ideas@changkun.de"),
		},
	}
}

// Handler serves the API. If svc is nil, every route answers 503 rather than
// falling through to the site's 404 page. Both cases send CORS headers, so the
// compose box sees the real status instead of a network error.
func Handler(svc *Service, l *log.Logger) http.Handler {
	r := http.NewServeMux()
	if svc == nil {
		r.HandleFunc(Prefix, func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "ideas API is not configured", http.StatusServiceUnavailable)
		})
		return cors(r)
	}

	r.HandleFunc("GET /ideas/ping", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "pong")
	})
	r.HandleFunc("POST /ideas/post", svc.handlePost)
	r.HandleFunc("POST /ideas/improve", svc.handleImprove)
	return cors(auth(newLatereVerifier(l), r))
}

// ideaRequest is the body of /ideas/post and /ideas/improve.
type ideaRequest struct {
	Title     string `json:"title"`
	Content   string `json:"content"`
	Augmented string `json:"augmented"`
}

// ideaResponse is the reply of /ideas/post and /ideas/improve.
type ideaResponse struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Content string `json:"content,omitempty"`
}

// handlePost replies as soon as the idea is valid and publishes it in the
// background.
func (s *Service) handlePost(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeIdea(w, r)
	if !ok {
		return
	}
	go s.processIdea(req, nil)
	writeJSON(w, http.StatusOK, ideaResponse{OK: true, Message: "idea accepted, publishing in background"})
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

// PublishIdea writes up and publishes an idea in the background, as
// /ideas/post does, with files beside it.
func (s *Service) PublishIdea(title, content string, files []Attachment) {
	go s.processIdea(ideaRequest{Title: title, Content: content}, files)
}

// handleImprove replies with the content polished by the model, without
// publishing it.
func (s *Service) handleImprove(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeIdea(w, r)
	if !ok {
		return
	}
	improved, err := s.llm.improveContent(r.Context(), req.Content)
	if err != nil {
		s.log.Printf("content improvement failed: %v", err)
		writeJSON(w, http.StatusInternalServerError, ideaResponse{Message: "content improvement failed"})
		return
	}
	writeJSON(w, http.StatusOK, ideaResponse{OK: true, Content: improved})
}

// decodeIdea decodes the request body. It writes a 400 and reports false if
// the body is invalid or has no content.
func decodeIdea(w http.ResponseWriter, r *http.Request) (ideaRequest, bool) {
	var req ideaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, ideaResponse{Message: "invalid request body"})
		return req, false
	}
	if req.Content == "" {
		writeJSON(w, http.StatusBadRequest, ideaResponse{Message: "content is required"})
		return req, false
	}
	return req, true
}

func writeJSON(w http.ResponseWriter, code int, v ideaResponse) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

// cors lets pages on changkun.de call the API, and answers preflight requests.
func cors(next http.Handler) http.Handler {
	allowed := map[string]bool{
		"https://changkun.de":     true,
		"https://www.changkun.de": true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
