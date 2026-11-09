// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package owner

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoginPage checks the login page: who it serves, and where it goes on to.
func TestLoginPage(t *testing.T) {
	o := newTestOwner(t, filepath.Join(t.TempDir(), "session.key"))
	h := o.Handler()
	serve := func(method, target string, owner bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		if owner {
			exp := "99999999999"
			r.AddCookie(&http.Cookie{Name: cookieName, Value: exp + "." + o.sign(exp)})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	// A visitor gets the page, which carries where to go on to, and is
	// never kept.
	rec := serve("GET", "/login?next="+url.QueryEscape("/blog/new"), false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `var next = "/blog/new";`) ||
		!strings.Contains(rec.Body.String(), `fetch('/login'`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("GET /login = %d, Cache-Control %q:\n%.600s", rec.Code, rec.Header().Get("Cache-Control"), rec.Body.String())
	}

	// The owner goes straight on, to the home page, the library or the
	// blog, and nowhere else.
	for next, want := range map[string]string{
		"":                     "/",
		"/":                    "/",
		"/blog/new":            "/blog/new",
		"/blog/edit/posts/x/":  "/blog/edit/posts/x/",
		"/library/t/SYS":       "/library/t/SYS",
		"https://example.com/": "/",
		"//example.com/":       "/",
		"/\\example.com":       "/",
		"/elsewhere":           "/",
		"/libraryx/":           "/",
	} {
		rec := serve("GET", "/login?next="+url.QueryEscape(next), true)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("owner GET /login?next=%s = %d to %q, want %q", next, rec.Code, rec.Header().Get("Location"), want)
		}
	}

	// A token nobody allowed is refused.
	if rec := serve("POST", "/login", false); rec.Code != http.StatusForbidden {
		t.Errorf("POST /login without a token = %d, want 403", rec.Code)
	}

	// Logging out clears the session and goes to the page, which logs out
	// of latere too.
	rec = serve("POST", "/logout", true)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?out=1" {
		t.Fatalf("POST /logout = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	cleared := map[string]bool{}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			cleared[c.Name] = true
		}
	}
	if !cleared[cookieName] || !cleared[signedIn] {
		t.Errorf("POST /logout cleared %v, want %s and %s", cleared, cookieName, signedIn)
	}
	if !strings.Contains(loginHTML, "latereAuth.logout()") {
		t.Error("the login page does not log out of latere on ?out=1")
	}

	// The page names no other way out than the one it was built for.
	if rec := serve("GET", "/logout", true); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /logout = %d, want 405", rec.Code)
	}
}
