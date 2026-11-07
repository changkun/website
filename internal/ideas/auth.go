// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"cmp"
	"log"
	"net/http"
	"os"
	"strings"

	"latere.ai/x/pkg/authkit/jwt"
)

// latereVerifier verifies latere access tokens against two allowlists.
//
// A valid token only proves who the caller is: any latere account can get one,
// for any latere client. A request is allowed only if its token was issued to
// an allowed client, by default the compose box, for an allowed principal.
type latereVerifier struct {
	auth    *jwt.Authenticator
	allowed map[string]bool // lowercase emails and principal ids
	clients map[string]bool // lowercase OAuth client ids
	log     *log.Logger
}

// defaultClient is the compose box's OAuth client, as in static/login-sdk.js.
const defaultClient = "changkun-blog"

// newLatereVerifier configures a verifier from the environment. It returns nil,
// which rejects every token, if AUTH_ALLOWED_PRINCIPALS is empty.
func newLatereVerifier(l *log.Logger) *latereVerifier {
	allowed := principalSet(os.Getenv("AUTH_ALLOWED_PRINCIPALS"))
	if len(allowed) == 0 {
		l.Println("AUTH_ALLOWED_PRINCIPALS is unset, latere tokens will be rejected")
		return nil
	}
	clients := principalSet(cmp.Or(os.Getenv("AUTH_ALLOWED_CLIENTS"), defaultClient))

	issuer := strings.TrimRight(cmp.Or(os.Getenv("AUTH_URL"), "https://auth.latere.ai"), "/")
	jwks := cmp.Or(os.Getenv("AUTH_JWKS_URL"), issuer+"/.well-known/jwks.json")
	l.Printf("latere auth enabled: issuer=%s principals=%d clients=%d", issuer, len(allowed), len(clients))

	return &latereVerifier{
		auth:    jwt.NewAuthenticator(jwt.New(jwt.Config{JWKSURL: jwks, Issuer: issuer})),
		allowed: allowed,
		clients: clients,
		log:     l,
	}
}

// principalSet parses a comma-separated list into a set of lowercase entries.
func principalSet(s string) map[string]bool {
	set := map[string]bool{}
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.ToLower(strings.TrimSpace(p)); p != "" {
			set[p] = true
		}
	}
	return set
}

// allow reports whether r carries a valid Bearer token issued to an allowed
// client for an allowed principal. A nil v allows nothing.
func (v *latereVerifier) allow(r *http.Request) bool {
	if v == nil {
		return false
	}
	id, err := v.auth.Authenticate(r)
	if err != nil {
		return false
	}
	if !v.clients[strings.ToLower(id.ClientID)] {
		v.log.Printf("latere client not allowed: sub=%s email=%s client=%s",
			id.Sub, id.Email, id.ClientID)
		return false
	}
	if v.allowed[strings.ToLower(id.Email)] || v.allowed[strings.ToLower(id.Sub)] {
		return true
	}
	v.log.Printf("latere principal not allowed: sub=%s email=%s client=%s",
		id.Sub, id.Email, id.ClientID)
	return false
}

// auth rejects requests that latere does not allow, except /ideas/ping.
func auth(latere *latereVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ideas/ping" {
			next.ServeHTTP(w, r)
			return
		}

		if latere.allow(r) {
			next.ServeHTTP(w, r)
			return
		}

		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}
