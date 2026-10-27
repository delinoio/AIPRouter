// SPDX-License-Identifier: Apache-2.0
package main

import (
	"encoding/json"
	"errors"
	"github.com/delinoio/AIPRouter/packages/aiprouter/pricing"
	v1 "github.com/delinoio/AIPRouter/protos/aiprouter/v1"
	"strconv"
)

func priceOverrides(raw json.RawMessage) ([]pricing.Override, error) {
	if len(raw) == 0 || string(raw) == "{}" || string(raw) == "null" {
		return nil, nil
	}
	var list []*v1.PriceOverride
	if e := json.Unmarshal(raw, &list); e != nil {
		return nil, e
	}
	out := make([]pricing.Override, 0, len(list))
	for _, p := range list {
		if p == nil || p.Rates == nil || p.Rates.Currency != "USD" || p.ServiceTier != "" {
			return nil, errors.New("unsupported price override")
		}
		rate := func(value *string) (float64, error) {
			if value == nil {
				return 0, errors.New("complete price rates required")
			}
			return strconv.ParseFloat(*value, 64)
		}
		input, e := rate(p.Rates.Input)
		if e != nil {
			return nil, e
		}
		output, e := rate(p.Rates.Output)
		if e != nil {
			return nil, e
		}
		read, e := rate(p.Rates.CacheRead)
		if e != nil {
			return nil, e
		}
		write, e := rate(p.Rates.CacheWrite)
		if e != nil {
			return nil, e
		}
		out = append(out, pricing.Override{Provider: p.ProviderId, Model: p.ModelId, Rates: pricing.Rates{Input: input, Output: output, CacheRead: read, CacheWrite: write}, SourceRef: p.Id})
	}
	return out, nil
}

func (s *server) priceSnapshot(raw json.RawMessage) (*pricing.Snapshot, error) {
	s.priceMu.Lock()
	defer s.priceMu.Unlock()
	if s.priceCache != nil && s.priceJSON == string(raw) {
		return s.priceCache, nil
	}
	overrides, e := priceOverrides(raw)
	if e != nil {
		return nil, e
	}
	snapshot, e := pricing.NewSnapshot(overrides)
	if e != nil {
		return nil, e
	}
	s.priceCache = snapshot
	s.priceJSON = string(raw)
	return snapshot, nil
}
