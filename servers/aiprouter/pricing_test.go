// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"github.com/delinoio/AIPRouter/packages/aiprouter/pricing"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"testing"
)

func TestOverrideValidationPreservesUnknownRates(t *testing.T) {
	raw, e := json.Marshal([]*v1.PriceOverride{{ProviderId: "custom", ModelId: "fixture", Rates: &v1.PriceRates{Currency: "USD", Input: ptr("0"), Output: ptr("0"), CacheRead: ptr("0"), CacheWrite: ptr("0")}}})
	if e != nil {
		t.Fatal(e)
	}
	overrides, e := priceOverrides(raw)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = pricing.NewSnapshot(overrides); e != nil {
		t.Fatal(e)
	}
	raw, _ = json.Marshal([]*v1.PriceOverride{{ProviderId: "custom", ModelId: "fixture", Rates: &v1.PriceRates{Currency: "USD", Input: ptr("0")}}})
	if _, e = priceOverrides(raw); e == nil {
		t.Fatal("missing rates became known zero")
	}
}
