// SPDX-License-Identifier: Apache-2.0
package main

import (
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference/providers/cursor"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference/providers/devin"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference/providers/google"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference/providers/kiro"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference/providers/zed"
)

func providerAdapters() map[inference.Wire]inference.Adapter {
	out := map[inference.Wire]inference.Adapter{google.WireAPI: google.NewAPI(google.Options{}), google.WireAntigravity: google.NewAntigravity(google.Options{}), google.WireGeminiCLI: google.NewGeminiCLI(google.Options{}), kiro.Wire: kiro.New(), cursor.Wire: cursor.New(), devin.Wire: devin.New()}
	for wire, family := range map[inference.Wire]zed.Provider{zed.WireAnthropic: zed.Anthropic, zed.WireOpenAI: zed.OpenAI, zed.WireGoogle: zed.Google, zed.WireXAI: zed.XAI} {
		a, e := zed.New(family)
		if e == nil {
			out[wire] = a
		}
	}
	return out
}

// Reuse the same adapter instance so request recovery and decoding share policy.
func providerRecoveryPolicies(adapters map[inference.Wire]inference.Adapter) map[inference.Wire]inference.HTTPRecoveryPolicy {
	out := map[inference.Wire]inference.HTTPRecoveryPolicy{devin.Wire: devin.RecoveryPolicy{}}
	for wire, adapter := range adapters {
		if policy, ok := adapter.(inference.HTTPRecoveryPolicy); ok {
			out[wire] = policy
		}
	}
	return out
}
