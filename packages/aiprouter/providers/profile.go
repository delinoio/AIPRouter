// SPDX-License-Identifier: Apache-2.0
// Protocol constants adapted from pinned upstream; see snapshot/NOTICE.
package providers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"strings"
	"time"
)

// AccountProfile is PRIVATE, bearer-bound provider evidence. It is not a service
// principal and must never be used to merge personal account slots automatically.
type AccountProfile struct {
	Provider, AccountID, Email, Plan, BearerSHA256 string
	ObservedAt                                     time.Time
}

func (p AccountProfile) Matches(c Credential) bool {
	h := sha256.Sum256([]byte(c.AccessToken))
	return p.Provider == c.Provider && p.BearerSHA256 == hex.EncodeToString(h[:])
}
func (a *Auth) Profile(ctx context.Context, s core.Scope, c Credential) (AccountProfile, error) {
	if !validScope(s) || c.Scope != s {
		return AccountProfile{}, ErrScope
	}
	if !safeString(c.AccessToken) {
		return AccountProfile{}, ErrReauthenticate
	}
	h := bearer(c.AccessToken)
	endpoint := ""
	switch c.Provider {
	case "anthropic", "anthropic2":
		endpoint = "https://api.anthropic.com/api/oauth/profile"
		h.Set("Anthropic-Beta", "oauth-2025-04-20")
		h.Set("Cache-Control", "no-cache")
		h.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
	case "google-antigravity":
		endpoint = "https://www.googleapis.com/oauth2/v2/userinfo"
	default:
		return AccountProfile{}, ErrProfileUnavailable
	}
	m, err := a.send(ctx, s, "GET", endpoint, nil, h)
	if err != nil {
		return AccountProfile{}, err
	}
	p := AccountProfile{Provider: c.Provider, ObservedAt: a.now()}
	switch c.Provider {
	case "anthropic", "anthropic2":
		account := obj(m, "account")
		p.AccountID = str(account, "uuid")
		p.Email = str(account, "email")
		org := obj(m, "organization")
		p.Plan = str(org, "rate_limit_tier")
		if p.Plan == "" {
			p.Plan = str(org, "organization_type")
		}
		p.Plan = strings.TrimPrefix(strings.TrimPrefix(p.Plan, "default_claude_"), "claude_")
	case "google-antigravity":
		p.AccountID = str(m, "id")
		p.Email = str(m, "email")
	}
	if !modelID(p.AccountID) || len(p.AccountID) > 128 {
		return AccountProfile{}, ErrResponse
	}
	if c.AccountID != "" && c.AccountID != p.AccountID {
		return AccountProfile{}, ErrReauthenticate
	}
	if !safeString(p.Email) || len(p.Email) > 320 {
		p.Email = ""
	}
	if !safeString(p.Plan) || len(p.Plan) > 128 {
		p.Plan = ""
	}
	digest := sha256.Sum256([]byte(c.AccessToken))
	p.BearerSHA256 = hex.EncodeToString(digest[:])
	return p, nil
}
