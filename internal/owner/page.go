// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package owner

import (
	_ "embed"
	"html/template"
	"net/http"
	"regexp"
)

//go:embed login.html
var loginHTML string

var loginPage = template.Must(template.New("login").Parse(loginHTML))

// next matches the pages the login page may go on to: the home page, the
// library and the blog. The page's script checks the same.
var next = regexp.MustCompile(`^/((library|blog)/.*)?$`)

// Handler serves the site's login page at GET /login, takes the latere token
// at POST /login, and logs out at POST /logout.
func (o *Owner) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /login", o.page)
	m.HandleFunc("POST /login", o.Login)
	m.HandleFunc("POST /logout", o.logout)
	return m
}

// page logs the owner in and goes on to ?next=, the home page if it names
// none or a page elsewhere.
func (o *Owner) page(w http.ResponseWriter, r *http.Request) {
	to := r.URL.Query().Get("next")
	if !next.MatchString(to) {
		to = "/"
	}
	if o.Is(r) {
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	if err := loginPage.Execute(w, to); err != nil && o != nil {
		o.log.Printf("owner: login page: %v", err)
	}
}

// logout ends the owner's session and shows the login page, which logs out
// of latere too and does not log straight back in.
func (o *Owner) logout(w http.ResponseWriter, r *http.Request) {
	o.Logout(w, r)
	http.Redirect(w, r, "/login?out=1", http.StatusSeeOther)
}
