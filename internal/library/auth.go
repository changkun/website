// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package library

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"latere.ai/x/pkg/authkit/jwt"
)

// The owner logs in with latere, as the compose box does: the login page runs
// the PKCE flow in the browser with /login-sdk.js and posts the access token
// here once. The server checks it and sets its own session cookie, so the
// owner pages are plain links and forms.

const (
	cookieName  = "library_owner"
	sessionLife = 30 * 24 * time.Hour
)

// owner decides whether a request comes from the owner.
type owner struct {
	key      []byte // signs the session cookie
	verifier *verifier
	dev      bool // LIBRARY_DEV_OWNER: loopback requests are the owner
	log      *log.Logger
}

// verifier checks latere access tokens against the same allowlists as the
// ideas API.
type verifier struct {
	auth    *jwt.Authenticator
	allowed map[string]bool // lowercase emails and principal ids
	clients map[string]bool // lowercase OAuth client ids
}

// newVerifier configures a verifier from the environment, or returns nil if
// AUTH_ALLOWED_PRINCIPALS is empty, in which case nobody can log in.
func newVerifier(l *log.Logger) *verifier {
	allowed := set(os.Getenv("AUTH_ALLOWED_PRINCIPALS"))
	if len(allowed) == 0 {
		l.Println("library: AUTH_ALLOWED_PRINCIPALS is unset, nobody can log in")
		return nil
	}
	issuer := strings.TrimRight(cmp.Or(os.Getenv("AUTH_URL"), "https://auth.latere.ai"), "/")
	jwks := cmp.Or(os.Getenv("AUTH_JWKS_URL"), issuer+"/.well-known/jwks.json")
	return &verifier{
		auth:    jwt.NewAuthenticator(jwt.New(jwt.Config{JWKSURL: jwks, Issuer: issuer})),
		allowed: allowed,
		clients: set(cmp.Or(os.Getenv("AUTH_ALLOWED_CLIENTS"), "changkun-blog")),
	}
}

func set(s string) map[string]bool {
	out := map[string]bool{}
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			out[p] = true
		}
	}
	return out
}

// allow reports whether r carries a Bearer token of an allowed principal,
// issued to an allowed client.
func (v *verifier) allow(r *http.Request) bool {
	if v == nil {
		return false
	}
	id, err := v.auth.Authenticate(r)
	if err != nil {
		return false
	}
	return v.clients[strings.ToLower(id.ClientID)] &&
		(v.allowed[strings.ToLower(id.Email)] || v.allowed[strings.ToLower(id.Sub)])
}

// is reports whether r comes from the owner.
func (o *owner) is(r *http.Request) bool {
	if o.dev && loopback(r) {
		return true
	}
	c, err := r.Cookie(cookieName)
	if err != nil {
		return false
	}
	exp, sig, ok := strings.Cut(c.Value, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > unix {
		return false
	}
	want := o.sign(exp)
	return hmac.Equal([]byte(sig), []byte(want))
}

func (o *owner) sign(exp string) string {
	m := hmac.New(sha256.New, o.key)
	m.Write([]byte("owner|" + exp))
	return hex.EncodeToString(m.Sum(nil))
}

// login checks the Bearer token of a POST and sets the session cookie.
func (o *owner) login(w http.ResponseWriter, r *http.Request) {
	if !o.verifier.allow(r) {
		o.log.Printf("library: login refused from %s", r.RemoteAddr)
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	exp := strconv.FormatInt(time.Now().Add(sessionLife).Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    exp + "." + o.sign(exp),
		Path:     Prefix,
		MaxAge:   int(sessionLife.Seconds()),
		HttpOnly: true,
		Secure:   secure(r),
		SameSite: http.SameSiteLaxMode, // other sites cannot post owner forms
	})
	w.WriteHeader(http.StatusNoContent)
}

// logout clears the session cookie.
func (o *owner) logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: Prefix, MaxAge: -1,
		HttpOnly: true, Secure: secure(r), SameSite: http.SameSiteLaxMode})
	http.Redirect(w, r, Prefix+"login", http.StatusSeeOther)
}

// secure reports whether the client reached us over https, directly or
// through the proxy.
func secure(r *http.Request) bool {
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

// loopback reports whether r came straight from this machine, not through a
// proxy. Behind traefik the remote address is traefik's, so this is false.
func loopback(r *http.Request) bool {
	if r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("X-Real-Ip") != "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
