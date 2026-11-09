// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package owner

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestOwner(t *testing.T, keyFile string) *Owner {
	t.Helper()
	t.Setenv("LIBRARY_DEV_OWNER", "")
	t.Setenv("AUTH_ALLOWED_PRINCIPALS", "")
	return New(keyFile, log.New(io.Discard, "", 0))
}

func TestSessionCookie(t *testing.T) {
	o := newTestOwner(t, filepath.Join(t.TempDir(), "state", "session.key"))
	exp := "99999999999"
	for _, tc := range []struct {
		c    *http.Cookie
		want bool
	}{
		{&http.Cookie{Name: cookieName, Value: exp + "." + o.sign(exp)}, true},
		{&http.Cookie{Name: cookieName, Value: exp + "." + strings.Repeat("0", 64)}, false},
		{&http.Cookie{Name: cookieName, Value: "1." + o.sign("1")}, false}, // expired
		{&http.Cookie{Name: oldCookie, Value: exp + "." + o.sign(exp)}, false},
	} {
		r := httptest.NewRequest("GET", "/blog/new", nil)
		r.AddCookie(tc.c)
		if got := o.Is(r); got != tc.want {
			t.Errorf("Is(%s=%s) = %v, want %v", tc.c.Name, tc.c.Value, got, tc.want)
		}
	}

	// Without an allowlist, login is refused.
	rec := httptest.NewRecorder()
	o.Login(rec, httptest.NewRequest("POST", "/login", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("login without allowlist = %d, want 403", rec.Code)
	}

	// Logging out clears the cookie for the whole site.
	rec = httptest.NewRecorder()
	o.Logout(rec, httptest.NewRequest("POST", "/logout", nil))
	if c := rec.Result().Cookies(); len(c) == 0 || c[0].Name != cookieName || c[0].Path != "/" || c[0].MaxAge >= 0 {
		t.Fatalf("logout cookies = %v", c)
	}
	// And the cookie that tells the pages' scripts, which they can read.
	var marked bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == signedIn {
			marked = true
			if c.Path != "/" || c.MaxAge >= 0 || c.HttpOnly {
				t.Errorf("logout %s cookie = %+v", signedIn, c)
			}
		}
	}
	if !marked {
		t.Errorf("logout does not clear the %s cookie", signedIn)
	}
	if c := marker(httptest.NewRequest("POST", "/login", nil), "1", 60); c.Name != signedIn || c.Value != "1" || c.HttpOnly || c.Path != "/" {
		t.Errorf("marker = %+v", c)
	}

	var nobody *Owner
	if nobody.Is(httptest.NewRequest("GET", "/", nil)) {
		t.Fatal("a nil Owner allowed a request")
	}
}

func TestKeyKept(t *testing.T) {
	name := filepath.Join(t.TempDir(), "state", "session.key")
	a := newTestOwner(t, name)
	b := newTestOwner(t, name)
	if a.sign("1") != b.sign("1") {
		t.Fatal("the key changed across runs")
	}
	if fi, err := os.Stat(name); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v %v", fi, err)
	}
}

func TestDev(t *testing.T) {
	o := newTestOwner(t, filepath.Join(t.TempDir(), "session.key"))
	o.Dev = true
	r := httptest.NewRequest("GET", "/blog/new", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if !o.Is(r) {
		t.Fatal("a loopback request is not the owner's in dev")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if o.Is(r) {
		t.Fatal("a proxied loopback request is the owner's")
	}
}
