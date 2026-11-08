// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"io"
	"log"
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
