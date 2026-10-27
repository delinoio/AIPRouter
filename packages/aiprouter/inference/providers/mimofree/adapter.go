// SPDX-License-Identifier: Apache-2.0
// Protocol behavior adapted from opencodex; see LICENSE.upstream and NOTICE.
package mimofree

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
)

const (
	Wire         inference.Wire = "mimo-free"
	ChatURL                     = "https://api.xiaomimimo.com/api/free-ai/openai/chat"
	BootstrapURL                = "https://api.xiaomimimo.com/api/free-ai/bootstrap"
	SystemMarker                = "You are MiMoCode, an interactive CLI tool that helps users with software engineering tasks."
	userAgent                   = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

type Adapter struct{}

func New() *Adapter { return &Adapter{} }

var _ inference.Adapter = (*Adapter)(nil)
var _ inference.ExecutingAdapter = (*Adapter)(nil)

func canonical(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	return u.Scheme == "https" && strings.EqualFold(u.Host, "api.xiaomimimo.com") && strings.TrimRight(u.Path, "/") == "/api/free-ai/openai/chat"
}

// Build constructs the public body only. Execute installs the scoped bootstrap token.
func (a *Adapter) Build(ctx context.Context, in inference.AdapterInput) (inference.AdapterRequest, error) {
	if !canonical(in.Target.BaseURL) {
		return inference.AdapterRequest{}, inference.ErrDestination
	}
	if in.Operation != inference.Generate {
		return inference.AdapterRequest{}, inference.ErrUnsupported
	}
	raw, err := inference.TranslateRequestJSON(in.Body, in.Wire, inference.Chat)
	if err != nil {
		return inference.AdapterRequest{}, err
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || body == nil {
		return inference.AdapterRequest{}, invalid("invalid_mimo_request")
	}
	messages, ok := body["messages"].([]any)
	if !ok {
		return inference.AdapterRequest{}, invalid("invalid_mimo_messages")
	}
	marked := false
	for _, item := range messages {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, _ := m["content"].(string)
		if m["role"] == "system" && strings.Contains(text, SystemMarker) {
			marked = true
		}
	}
	if !marked {
		body["messages"] = append([]any{map[string]any{"role": "system", "content": SystemMarker}}, messages...)
	}
	body["model"] = in.Target.Model
	raw, err = json.Marshal(body)
	if err != nil {
		return inference.AdapterRequest{}, err
	}
	accept := "application/json"
	if body["stream"] == true {
		accept = "text/event-stream"
	}
	return inference.AdapterRequest{Method: http.MethodPost, URL: ChatURL, Body: raw, ResponseWire: inference.Chat, Headers: http.Header{
		"Content-Type": {"application/json"}, "Accept": {accept}, "User-Agent": {userAgent}, "X-Mimo-Source": {"mimocode-cli-free"},
	}}, nil
}
func (*Adapter) Decode(_ context.Context, r *http.Response) (*http.Response, error) { return r, nil }
func invalid(code string) error                                                     { return &inference.Error{Status: 502, Code: code} }
