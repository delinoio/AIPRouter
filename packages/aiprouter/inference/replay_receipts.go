// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type replayScopeKey struct{}

// signedReplayTokens traverses protocol fields, never tool arguments, schemas or
// ordinary text. Envelopes are unpacked so a wire translation cannot confer trust.
func signedReplayTokens(v any) ([]string, error) {
	tokens := map[string]bool{}
	var walk func(any, int) error
	add := func(s string) error {
		if s == "" {
			return nil
		}
		if len(s) > 2<<20 || len(tokens) >= 256 {
			return &Error{400, "reasoning_receipt_limit", 0}
		}
		sum := sha256.Sum256([]byte(s))
		tokens[hex.EncodeToString(sum[:])] = true
		return nil
	}
	walk = func(v any, depth int) error {
		if depth > 64 {
			return &Error{400, "reasoning_nesting_limit", 0}
		}
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				if e := walk(item, depth+1); e != nil {
					return e
				}
			}
		case map[string]any:
			for k, value := range x {
				if k == "arguments" || k == "parameters" || k == "input_schema" || k == "tools" || k == "input" && x["type"] == "tool_use" {
					continue
				}
				if s, ok := value.(string); ok {
					if k == "encrypted_content" && strings.HasPrefix(s, reasoningPrefix) {
						envelope, e := decodeReasoning(s)
						if e != nil {
							return e
						}
						for _, token := range append([]string{envelope.Signature, envelope.Kiro}, envelope.Redacted...) {
							if e = add(token); e != nil {
								return e
							}
						}
						if e = walk(envelope.Native, depth+1); e != nil {
							return e
						}
						continue
					}
					switch k {
					case "signature", "thoughtSignature", "thought_signature", "reasoning_signature", "kiro_reasoning":
						if e := add(s); e != nil {
							return e
						}
					case "data":
						if x["type"] == "redacted_thinking" {
							if e := add(s); e != nil {
								return e
							}
						}
					}
				} else if e := walk(value, depth+1); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if e := walk(v, 0); e != nil {
		return nil, e
	}
	out := make([]string, 0, len(tokens))
	for t := range tokens {
		out = append(out, t)
	}
	return out, nil
}
func (h *Handler) validateReplay(ctx context.Context, b object, scope scopedAdapterState) error {
	tokens, e := signedReplayTokens(b)
	if e != nil {
		return e
	}
	for _, token := range tokens {
		receipt, err := scope.Load(ctx, "signed-replay-v1:"+token)
		if err != nil || string(receipt) != token {
			return &Error{400, "unbound_signed_reasoning", 0}
		}
	}
	return nil
}

// CaptureReplayReceipts is called with the successful public response before terminal
// output. The context must be the prepared execution context supplied by Handler.
// Provider snapshots still validate canonical tool arguments and provider ordering.
func (h *Handler) CaptureReplayReceipts(ctx context.Context, out any) error {
	scope, ok := ctx.Value(replayScopeKey{}).(scopedAdapterState)
	if !ok {
		return nil
	}
	tokens, e := signedReplayTokens(out)
	if e != nil {
		return e
	}
	for _, token := range tokens {
		if e = scope.Save(ctx, "signed-replay-v1:"+token, []byte(token), time.Hour); e != nil {
			return e
		}
	}
	return nil
}

// CaptureReplayJSON is a convenience hook for bounded finalized stream JSON.
func (h *Handler) CaptureReplayJSON(ctx context.Context, raw json.RawMessage) error {
	var out any
	if e := json.Unmarshal(raw, &out); e != nil {
		return e
	}
	return h.CaptureReplayReceipts(ctx, out)
}
