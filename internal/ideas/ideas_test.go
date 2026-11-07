// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ideasEnv sets the minimal environment New accepts.
func ideasEnv(t *testing.T) {
	t.Helper()
	t.Setenv("LLM_BASE_URL", "https://lux.example")
	t.Setenv("LLM_API_KEY", "key")
	t.Setenv("GIT_TOKEN", "token")
}

func discard() *log.Logger { return log.New(io.Discard, "", 0) }

// TestNewNeedsCredentials checks that New returns nil, rather than failing,
// when a credential is missing.
func TestNewNeedsCredentials(t *testing.T) {
	tests := []struct {
		name  string
		unset string
	}{
		{name: "no api key", unset: "LLM_API_KEY"},
		{name: "no git token", unset: "GIT_TOKEN"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ideasEnv(t)
			t.Setenv(tt.unset, "")
			if svc := New(discard()); svc != nil {
				t.Fatalf("New() = %v, want nil without %s", svc, tt.unset)
			}
		})
	}
}

func TestNewReadsRepo(t *testing.T) {
	tests := []struct {
		name        string
		repo        string
		wantOwner   string
		wantRepo    string
		wantService bool
	}{
		{name: "default", repo: "", wantOwner: "changkun", wantRepo: "blog", wantService: true},
		{name: "override", repo: "acme/notes", wantOwner: "acme", wantRepo: "notes", wantService: true},
		{name: "no slash", repo: "notes"},
		{name: "no owner", repo: "/notes"},
		{name: "no name", repo: "acme/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ideasEnv(t)
			t.Setenv("GIT_REPO", tt.repo)

			svc := New(discard())
			if (svc != nil) != tt.wantService {
				t.Fatalf("New() = %v, want service: %v", svc, tt.wantService)
			}
			if svc == nil {
				return
			}
			if svc.github.owner != tt.wantOwner || svc.github.repo != tt.wantRepo {
				t.Errorf("repo = %s/%s, want %s/%s",
					svc.github.owner, svc.github.repo, tt.wantOwner, tt.wantRepo)
			}
		})
	}
}

// TestHandlerUnconfigured checks that without a service every route answers
// 503, not the site's 404.
func TestHandlerUnconfigured(t *testing.T) {
	h := Handler(nil, discard())

	for _, path := range []string{"/ideas/ping", "/ideas/post", "/ideas/improve"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, nil))
		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s = %d, want %d", path, w.Code, http.StatusServiceUnavailable)
		}
	}
}

// TestHandlerAllowsComposeBox checks the CORS preflight from changkun.de, with
// and without a service.
func TestHandlerAllowsComposeBox(t *testing.T) {
	tests := []struct {
		name string
		svc  *Service
	}{
		{name: "configured", svc: &Service{log: discard()}},
		{name: "unconfigured", svc: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Handler(tt.svc, discard())

			r := httptest.NewRequest(http.MethodOptions, "/ideas/post", nil)
			r.Header.Set("Origin", "https://changkun.de")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)

			if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://changkun.de" {
				t.Errorf("Access-Control-Allow-Origin = %q, want the site origin", got)
			}
			if w.Code != http.StatusNoContent {
				t.Errorf("preflight status = %d, want %d", w.Code, http.StatusNoContent)
			}
		})
	}
}

// TestHandlerRejectsUnauthenticated checks that only /ideas/ping is public.
func TestHandlerRejectsUnauthenticated(t *testing.T) {
	h := Handler(&Service{log: discard()}, discard())

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/ideas/post", nil))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("POST /ideas/post = %d, want %d", w.Code, http.StatusUnauthorized)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ideas/ping", nil))
	if w.Code != http.StatusOK {
		t.Errorf("GET /ideas/ping = %d, want %d", w.Code, http.StatusOK)
	}
}

// TestHandleImprove checks the reply with and without a working gateway.
func TestHandleImprove(t *testing.T) {
	t.Run("rewritten", func(t *testing.T) {
		svc := &Service{log: discard(), llm: newScriptedGateway(t, "Polished.").client()}
		w := httptest.NewRecorder()
		svc.handleImprove(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"content":"rough"}`)))
		assertResponse(t, w, http.StatusOK, ideaResponse{OK: true, Content: "Polished."})
	})

	t.Run("gateway down", func(t *testing.T) {
		svc := &Service{log: discard(), llm: newScriptedGateway(t).client()}
		w := httptest.NewRecorder()
		svc.handleImprove(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"content":"rough"}`)))
		assertResponse(t, w, http.StatusInternalServerError, ideaResponse{Message: "content improvement failed"})
	})
}

func assertResponse(t *testing.T, w *httptest.ResponseRecorder, code int, want ideaResponse) {
	t.Helper()
	if w.Code != code {
		t.Errorf("status = %d, want %d", w.Code, code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got ideaResponse
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got != want {
		t.Errorf("response = %+v, want %+v", got, want)
	}
}
