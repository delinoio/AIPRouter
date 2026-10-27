// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"github.com/delinoio/AIPRouter/packages/aiprouter/pricing"
	"github.com/delinoio/AIPRouter/packages/aiprouter/store"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"strconv"
)

func usageOutcome(code string) v1.RequestOutcome {
	switch code {
	case "":
		return v1.RequestOutcome_REQUEST_OUTCOME_SUCCEEDED
	case "canceled", "cancelled":
		return v1.RequestOutcome_REQUEST_OUTCOME_CANCELED
	case "rate_limited":
		return v1.RequestOutcome_REQUEST_OUTCOME_RATE_LIMITED
	case "upstream_error", "upstream_status":
		return v1.RequestOutcome_REQUEST_OUTCOME_UPSTREAM_ERROR
	default:
		return v1.RequestOutcome_REQUEST_OUTCOME_GATEWAY_ERROR
	}
}
func appliedPrice(u store.Usage) *v1.AppliedPrice {
	var e pricing.Estimate
	if json.Unmarshal(u.Price, &e) != nil || e.Price.Model == "" {
		return nil
	}
	decimal := func(v float64) *string { s := strconv.FormatFloat(v, 'f', -1, 64); return &s }
	source := v1.PriceSource_PRICE_SOURCE_BUNDLED
	if e.Price.Source == pricing.Operator {
		source = v1.PriceSource_PRICE_SOURCE_OPERATOR_OVERRIDE
	}
	tier := ""
	if e.Tier.Response != nil {
		tier = *e.Tier.Response
	} else if e.Tier.Requested != nil {
		tier = *e.Tier.Requested
	} else if e.Tier.Configured != nil {
		tier = *e.Tier.Configured
	}
	return &v1.AppliedPrice{Rates: &v1.PriceRates{Currency: "USD", Input: decimal(e.EffectiveRates.Input), Output: decimal(e.EffectiveRates.Output), CacheRead: decimal(e.EffectiveRates.CacheRead), CacheWrite: decimal(e.EffectiveRates.CacheWrite)}, Source: source, SourceRevision: e.SnapshotID, ProviderId: e.Price.Provider, ModelId: e.Price.Model, ServiceTier: tier, AppliedAt: timestamp(e.RecordedAt)}
}
