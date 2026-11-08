// Copyright 2026 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

// Package owner recognizes requests from the person who runs changkun.de.
//
// Logging in goes through latere: the library's login page runs the PKCE
// flow in the browser with /login-sdk.js and posts the access token to
// Login once. Login checks it against AUTH_ALLOWED_PRINCIPALS and
// AUTH_ALLOWED_CLIENTS and sets a cookie for the whole site, signed with a key kept on disk, so
// that the library's private pages and the blog's editor are plain links
// and forms after.
package owner

import (
	"cmp"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"latere.ai/x/pkg/authkit/jwt"
)

const (
	cookieName  = "owner"
	oldCookie   = "library_owner" // the library's own cookie, from before the blog's editor shared it
	sessionLife = 30 * 24 * time.Hour
)

// An Owner decides whether a request comes from the site's owner. A nil
// *Owner refuses every request.
type Owner struct {
	// Dev makes requests from this machine the owner's, for running the
	// site locally without logging in.
	Dev bool

	key      []byte // signs the session cookie
	verifier *verifier
	log      *log.Logger
}

// New configures an Owner from the environment. keyFile holds the key that
// signs the session cookie, and is created if missing; if it cannot be read
// or written, a key for this run alone is used, and sessions end with it.
//
//	AUTH_ALLOWED_PRINCIPALS  who may log in; nobody without it
//	AUTH_ALLOWED_CLIENTS     default changkun-blog
//	AUTH_URL, AUTH_JWKS_URL  default https://auth.latere.ai and its JWKS
//	LIBRARY_DEV_OWNER=1      requests from this machine are the owner's
func New(keyFile string, l *log.Logger) *Owner {
	o := &Owner{Dev: os.Getenv("LIBRARY_DEV_OWNER") == "1", verifier: newVerifier(l), log: l}
	if o.Dev {
		l.Println("owner: LIBRARY_DEV_OWNER is set, requests from this machine are the owner's")
	}
	key, err := loadKey(keyFile)
	if err != nil {
		l.Printf("owner: %v; logins last until the site restarts", err)
		key = make([]byte, 32)
		rand.Read(key)
	}
	o.key = key
	return o
}

// loadKey reads the key in name, or makes one and writes it there.
func loadKey(name string) ([]byte, error) {
	raw, err := os.ReadFile(name)
	if err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(key) < 32 {
			return nil, errors.New(name + " is not 32 hex-encoded bytes")
		}
		return key, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	rand.Read(key)
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(name, []byte(hex.EncodeToString(key)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// Is reports whether r comes from the owner.
func (o *Owner) Is(r *http.Request) bool {
	if o == nil {
		return false
	}
	if o.Dev && loopback(r) {
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
	return hmac.Equal([]byte(sig), []byte(o.sign(exp)))
}

func (o *Owner) sign(exp string) string {
	m := hmac.New(sha256.New, o.key)
	m.Write([]byte("owner|" + exp))
	return hex.EncodeToString(m.Sum(nil))
}

// Login checks the Bearer token of a POST and sets the session cookie.
func (o *Owner) Login(w http.ResponseWriter, r *http.Request) {
	if o == nil || !o.verifier.allow(r) {
		if o != nil {
			o.log.Printf("owner: login refused from %s", r.RemoteAddr)
		}
		http.Error(w, "not allowed", http.StatusForbidden)
		return
	}
	exp := strconv.FormatInt(time.Now().Add(sessionLife).Unix(), 10)
	http.SetCookie(w, cookie(r, cookieName, "/", exp+"."+o.sign(exp), int(sessionLife.Seconds())))
	http.SetCookie(w, cookie(r, oldCookie, "/library/", "", -1))
	w.WriteHeader(http.StatusNoContent)
}

// Logout clears the session cookie.
func (o *Owner) Logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, cookie(r, cookieName, "/", "", -1))
	http.SetCookie(w, cookie(r, oldCookie, "/library/", "", -1))
}

func cookie(r *http.Request, name, path, value string, age int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   age,
		HttpOnly: true,
		Secure:   Secure(r),
		SameSite: http.SameSiteLaxMode, // other sites cannot post the owner's forms
	}
}

// Secure reports whether the client reached the site over https, directly
// or through the proxy.
func Secure(r *http.Request) bool {
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

// verifier checks latere access tokens against the allowlists of
// AUTH_ALLOWED_PRINCIPALS and AUTH_ALLOWED_CLIENTS.
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
		l.Println("owner: AUTH_ALLOWED_PRINCIPALS is unset, nobody can log in")
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
