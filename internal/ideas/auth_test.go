// Copyright 2025 Changkun Ou. All rights reserved.
// Use of this source code is governed by a MIT
// license that can be found in the LICENSE file.

package ideas

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"maps"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"latere.ai/x/pkg/authkit/jwt"
)

const testKid = "test-key"

// authFixture is an in-process latere auth server: an RSA key, its JWKS
// endpoint, and a token minter.
type authFixture struct {
	key    *rsa.PrivateKey
	issuer string
}

func newAuthFixture(t *testing.T) *authFixture {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("cannot generate key: %v", err)
	}

	f := &authFixture{key: key}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA",
			"kid": testKid,
			"alg": "RS256",
			"use": "sig",
			"n":   b64(key.N.Bytes()),
			"e":   b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	f.issuer = srv.URL
	return f
}

// token mints an access token like those auth.latere.ai issues to the compose
// box. claims override the defaults.
func (f *authFixture) token(t *testing.T, claims map[string]any) string {
	t.Helper()

	payload := map[string]any{
		"sub":            "principal-1",
		"iss":            f.issuer,
		"exp":            time.Now().Add(time.Hour).Unix(),
		"principal_type": "user",
		"client_id":      "changkun-blog",
	}
	maps.Copy(payload, claims)
	// A nil value removes the claim.
	maps.DeleteFunc(payload, func(_ string, v any) bool { return v == nil })

	header, err := json.Marshal(map[string]string{"alg": "RS256", "kid": testKid, "typ": "JWT"})
	if err != nil {
		t.Fatalf("cannot encode header: %v", err)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("cannot encode payload: %v", err)
	}

	signing := b64(header) + "." + b64(body)
	sum := crypto.SHA256.New()
	sum.Write([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum.Sum(nil))
	if err != nil {
		t.Fatalf("cannot sign token: %v", err)
	}
	return signing + "." + b64(sig)
}

func (f *authFixture) verifier(allowed string) *latereVerifier {
	return &latereVerifier{
		auth: jwt.NewAuthenticator(jwt.New(jwt.Config{
			JWKSURL: f.issuer + "/.well-known/jwks.json",
			Issuer:  f.issuer,
		})),
		allowed: principalSet(allowed),
		clients: principalSet(defaultClient),
		log:     log.New(io.Discard, "", 0),
	}
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func doAuth(v *latereVerifier, bearer string) *httptest.ResponseRecorder {
	h := auth(v, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	r := httptest.NewRequest(http.MethodPost, "/ideas/post", nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestAuthLatereToken checks the principal allowlist and token validation.
func TestAuthLatereToken(t *testing.T) {
	f := newAuthFixture(t)

	tests := []struct {
		name    string
		allowed string
		claims  map[string]any
		want    int
	}{
		{
			name:    "allowlisted email",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "hi@changkun.de"},
			want:    http.StatusOK,
		},
		{
			name:    "allowlisted email is case-insensitive",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "Hi@Changkun.de"},
			want:    http.StatusOK,
		},
		{
			name:    "allowlisted principal id",
			allowed: "principal-1",
			claims:  map[string]any{"email": "someone@example.com"},
			want:    http.StatusOK,
		},
		{
			name:    "valid signature but foreign principal",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "stranger@example.com", "sub": "principal-2"},
			want:    http.StatusUnauthorized,
		},
		{
			name:    "expired token",
			allowed: "hi@changkun.de",
			claims: map[string]any{
				"email": "hi@changkun.de",
				"exp":   time.Now().Add(-time.Minute).Unix(),
			},
			want: http.StatusUnauthorized,
		},
		{
			name:    "foreign issuer",
			allowed: "hi@changkun.de",
			claims:  map[string]any{"email": "hi@changkun.de", "iss": "https://evil.example.com"},
			want:    http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doAuth(f.verifier(tt.allowed), f.token(t, tt.claims)).Code
			if got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAuthForgedSignature rejects a token signed by a key the JWKS does not
// publish.
func TestAuthForgedSignature(t *testing.T) {
	f := newAuthFixture(t)

	forger := newAuthFixture(t)
	forger.issuer = f.issuer // claim the real issuer, sign with the wrong key

	tok := forger.token(t, map[string]any{"email": "hi@changkun.de"})
	if got := doAuth(f.verifier("hi@changkun.de"), tok).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
	}
}

// TestAuthForeignClient rejects an allowed person's token issued to another
// latere client.
func TestAuthForeignClient(t *testing.T) {
	f := newAuthFixture(t)

	tests := []struct {
		name   string
		claims map[string]any
		want   int
	}{
		{
			name:   "compose box client",
			claims: map[string]any{"client_id": "changkun-blog"},
			want:   http.StatusOK,
		},
		{
			name:   "client named by azp alone",
			claims: map[string]any{"client_id": nil, "azp": "changkun-blog"},
			want:   http.StatusOK,
		},
		{
			name:   "another latere client",
			claims: map[string]any{"client_id": "some-other-client"},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "no client",
			claims: map[string]any{"client_id": nil},
			want:   http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claims := map[string]any{"email": "hi@changkun.de"}
			maps.Copy(claims, tt.claims)
			got := doAuth(f.verifier("hi@changkun.de"), f.token(t, claims)).Code
			if got != tt.want {
				t.Fatalf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestAuthDisabledLatere checks that every token is rejected without an
// allowlist.
func TestAuthDisabledLatere(t *testing.T) {
	f := newAuthFixture(t)
	t.Setenv("AUTH_ALLOWED_PRINCIPALS", "")

	v := newLatereVerifier(log.New(io.Discard, "", 0))
	if v != nil {
		t.Fatalf("newLatereVerifier() = %v, want nil without an allowlist", v)
	}
	tok := f.token(t, map[string]any{"email": "hi@changkun.de"})
	if got := doAuth(v, tok).Code; got != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", got, http.StatusUnauthorized)
	}
}

// TestNewLatereVerifier checks the allowlists parsed from the environment.
func TestNewLatereVerifier(t *testing.T) {
	t.Setenv("AUTH_ALLOWED_PRINCIPALS", " HI@changkun.de , principal-1 ,")
	t.Setenv("AUTH_URL", "https://auth.latere.ai/")

	v := newLatereVerifier(log.New(io.Discard, "", 0))
	if v == nil {
		t.Fatal("newLatereVerifier() = nil, want a verifier")
	}
	if !v.allowed["hi@changkun.de"] || !v.allowed["principal-1"] || len(v.allowed) != 2 {
		t.Fatalf("allowed = %v", v.allowed)
	}
	if !v.clients["changkun-blog"] || len(v.clients) != 1 {
		t.Fatalf("clients = %v, want the compose box client by default", v.clients)
	}

	t.Setenv("AUTH_ALLOWED_CLIENTS", "changkun-blog, latere-cli")
	v = newLatereVerifier(log.New(io.Discard, "", 0))
	if !v.clients["changkun-blog"] || !v.clients["latere-cli"] || len(v.clients) != 2 {
		t.Fatalf("clients = %v", v.clients)
	}
}
