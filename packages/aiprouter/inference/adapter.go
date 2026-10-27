// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"encoding/json"
	"net/http"
)

// Adapter builds provider-specific HTTP requests and exposes one supported response wire.
// It must not perform network I/O. All sends belong to the central executor so retries,
// destination policy and authorization cannot be bypassed by a provider adapter.
type Adapter interface {
	Build(context.Context, AdapterInput) (AdapterRequest, error)
	Decode(context.Context, *http.Response) (*http.Response, error)
}
type AdapterInput struct {
	Principal  Principal
	Target     Target
	Credential Credential
	Operation  Operation
	Wire       Wire
	Body       json.RawMessage
}
type AdapterRequest struct {
	URL          string
	Headers      http.Header
	Body         []byte
	ResponseWire Wire
}
