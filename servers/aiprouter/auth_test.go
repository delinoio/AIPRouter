// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"crypto/rand"
	"crypto/rsa"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

type authFixture struct {
	state              map[string]LoginState
	logged             bool
	issuer, sub, email string
}

func (f *authFixture) SaveLogin(_ context.Context, k string, v LoginState) error {
	f.state[k] = v
	return nil
}
func (f *authFixture) ConsumeLogin(_ context.Context, k string, binding string) (LoginState, error) {
	v, ok := f.state[k]
	delete(f.state, k)
	if !ok || v.Binding != binding {
		return v, errors.New("missing")
	}
	return v, nil
}
func (f *authFixture) Login(_ context.Context, i, s, e string) (string, error) {
	f.logged = true
	f.issuer = i
	f.sub = s
	f.email = e
	return "session-secret", nil
}
func (f *authFixture) Session(context.Context, string) (Identity, error) {
	return Identity{}, errAuthentication
}
func (f *authFixture) Logout(context.Context, string) error { return nil }
func TestOIDCCodeFlow(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	jwk := jose.JSONWebKey{Key: key, KeyID: "test", Algorithm: "RS256", Use: "sig"}
	signer, e := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: jwk}, nil)
	if e != nil {
		t.Fatal(e)
	}
	var issuer, nonce, challenge string
	verified := true
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
		case "/keys":
			json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{jwk.Public()}})
		case "/token":
			r.ParseForm()
			if hashToken(r.Form.Get("code_verifier")) != challenge {
				t.Error("PKCE verifier mismatch")
				http.Error(w, "invalid verifier", 400)
				return
			}
			raw, e := jwt.Signed(signer).Claims(map[string]any{"iss": issuer, "sub": "subject-1", "aud": "client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce, "email": "member@example.test", "email_verified": verified}).Serialize()
			if e != nil {
				t.Error(e)
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "token_type": "Bearer", "id_token": raw})
		default:
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()
	issuer = mock.URL
	fixture := &authFixture{state: map[string]LoginState{}}
	a, e := newAuth(context.Background(), Config{PublicURL: "http://localhost:8080", OIDCIssuer: issuer, OIDCClientID: "client"}, fixture)
	if e != nil {
		t.Fatal(e)
	}
	for _, valid := range []bool{true, false} {
		verified = valid
		fixture.logged = false
		start := httptest.NewRecorder()
		a.login(start, httptest.NewRequest("GET", "/auth/login", nil))
		location, _ := url.Parse(start.Header().Get("Location"))
		q := location.Query()
		nonce = q.Get("nonce")
		challenge = q.Get("code_challenge")
		if q.Get("code_challenge_method") != "S256" || nonce == "" {
			t.Fatal("missing PKCE or nonce")
		}
		req := httptest.NewRequest("GET", "/auth/callback?code=code&state="+url.QueryEscape(q.Get("state")), nil)
		req.AddCookie(start.Result().Cookies()[0])
		out := httptest.NewRecorder()
		a.callback(out, req)
		if valid {
			if out.Code != 303 || !fixture.logged || fixture.issuer != issuer || fixture.sub != "subject-1" {
				t.Fatalf("login failed: %d", out.Code)
			}
		} else if out.Code != 403 || fixture.logged {
			t.Fatalf("unverified email accepted: %d", out.Code)
		}
		replay := httptest.NewRecorder()
		a.callback(replay, req)
		if replay.Code != 400 {
			t.Fatal("state replay accepted")
		}
	}
}
func TestOriginGuard(t *testing.T) {
	a := Auth{config: Config{PublicURL: "https://router.example"}}
	for _, origin := range []string{"", "https://evil.example", "https://router.example.evil", "null"} {
		r := httptest.NewRequest("POST", "https://router.example/auth/logout", strings.NewReader(""))
		r.Header.Set("Origin", origin)
		if a.sameOrigin(r) {
			t.Fatalf("accepted %s", origin)
		}
	}
}
func TestCallbackRequiresBrowserState(t *testing.T) {
	a := Auth{}
	w := httptest.NewRecorder()
	a.callback(w, httptest.NewRequest("GET", "/auth/callback?state=x&code=x", nil))
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
