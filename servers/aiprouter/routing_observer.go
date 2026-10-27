// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"net/http"
)

// Core invokes this before repair/recovery for each physical model response.
// Support-token/profile calls do not produce model quota evidence.
func (a *inferenceAdapter) ObserveResponse(ctx context.Context, p inference.Principal, t inference.Target, c inference.Credential, status int, headers http.Header) error {
	return a.observeNativeResponse(ctx, p, t, c, status, headers)
}

var _ inference.PhysicalResponseObserver = (*inferenceAdapter)(nil)
