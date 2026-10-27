// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/delinoio/AIPRouter/packages/aiprouter/core"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"net/http"
	"time"
)

type outboundSocketDialer struct{}

func (outboundSocketDialer) Dial(ctx context.Context, endpoint string, headers http.Header, client *http.Client) (inference.Socket, error) {
	c, response, e := websocket.Dial(ctx, endpoint, &websocket.DialOptions{HTTPClient: client, HTTPHeader: headers, CompressionMode: websocket.CompressionDisabled})
	if e != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return nil, e
	}
	c.SetReadLimit(16 << 20)
	return webSocket{c}, nil
}

type storedLiveBinding struct {
	Binding           inference.LiveBinding
	CredentialVersion string
}

func (a *inferenceAdapter) SaveBinding(ctx context.Context, p inference.Principal, id string, binding inference.LiveBinding) error {
	if e := a.BeforeSend(ctx, p); e != nil {
		return e
	}
	if binding.AccountID == "" || !binding.ExpiresAt.After(time.Now()) || binding.ExpiresAt.After(time.Now().Add(24*time.Hour)) {
		return inference.ErrContinuation
	}
	_, version, e := a.s.GetPrivateVersion(ctx, scopeOf(p), string(providerKind), binding.AccountID)
	if e != nil {
		return inferenceError(e)
	}
	b, e := json.Marshal(storedLiveBinding{binding, version})
	if e != nil {
		return e
	}
	return a.s.PutState(ctx, scopeOf(p), core.Responses, "live/"+p.KeyID+"/"+id, b)
}
func (a *inferenceAdapter) ResolveBinding(ctx context.Context, p inference.Principal, id string) (inference.LiveBinding, inference.Credential, error) {
	var stored storedLiveBinding
	b, e := a.s.GetState(ctx, scopeOf(p), core.Responses, "live/"+p.KeyID+"/"+id)
	if e != nil {
		return stored.Binding, inference.Credential{}, inference.ErrContinuation
	}
	if json.Unmarshal(b, &stored) != nil || !stored.Binding.ExpiresAt.After(time.Now()) {
		return stored.Binding, inference.Credential{}, inference.ErrContinuation
	}
	_, version, e := a.s.GetPrivateVersion(ctx, scopeOf(p), string(providerKind), stored.Binding.AccountID)
	if e != nil || version != stored.CredentialVersion {
		return stored.Binding, inference.Credential{}, inference.ErrContinuation
	}
	credential, e := a.SelectBound(ctx, p, stored.Binding.Target, stored.Binding.AccountID)
	if e != nil || credential.AccountID != stored.Binding.AccountID {
		return stored.Binding, inference.Credential{}, inference.ErrContinuation
	}
	return stored.Binding, credential, nil
}
