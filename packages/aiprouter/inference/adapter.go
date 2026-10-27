// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Adapter builds provider-specific HTTP requests and exposes one supported response wire.
// It must not perform network I/O. All sends belong to the central executor so retries,
// destination policy and authorization cannot be bypassed by a provider adapter.
type Adapter interface {
	Build(context.Context, AdapterInput) (AdapterRequest, error)
	Decode(context.Context, *http.Response) (*http.Response, error)
}
type AdapterInput struct {
	RequestID          string
	PreviousResponseID string
	ClientHeaders      http.Header
	Principal          Principal
	Target             Target
	Credential         Credential
	Operation          Operation
	Wire               Wire
	Body               json.RawMessage
}
type AdapterRequest struct {
	AttemptTimeout time.Duration
	Method         string
	URL            string
	Headers        http.Header
	Body           []byte
	ResponseWire   Wire
}

// ContextualAdapter receives the immutable input used for this attempt. Use it for
// request-scoped tool-name restoration and signature ordering, never adapter-global state.
type ContextualAdapter interface {
	DecodeWithInput(context.Context, AdapterInput, *http.Response) (*http.Response, error)
}

// AdapterExecutor owns every preparatory or model HTTP exchange. It applies the
// enclosing logical request's send budget and current authorization at the write gate.
// The request URL must remain inside the selected credential's origin.
type AdapterExecutor interface {
	Do(context.Context, AdapterRequest) (*http.Response, error)
}

// PreparingAdapter performs bounded account/catalog preflight with the central executor.
// It may change the request body, but not the selected target or credential.
type PreparingAdapter interface {
	Prepare(context.Context, AdapterInput, AdapterExecutor) (json.RawMessage, error)
}

// ContextPreparer creates private request state once per physical attempt before Build.
type ContextPreparer interface {
	PrepareContext(context.Context) context.Context
}

// ExecutingAdapter orchestrates protocols with multiple exchanges through the central
// executor (for example Cursor RunSSE/BidiAppend). It owns decoding into ResponseWire,
// cancellation and closing every response. It must not create HTTP clients or sockets.
// Returning a stream does not release its state: Close must cancel/join all producers.
type ExecutingAdapter interface {
	Execute(context.Context, AdapterInput, AdapterExecutor, AdapterStateStore) (*http.Response, Wire, error)
}

// StatefulAdapter binds provider continuation metadata to this exact request/account.
// RestoreState must use the supplied scoped store; it cannot fall back to another owner.
// CaptureState runs only after a successful response and current authorization check.
type StatefulAdapter interface {
	RestoreState(context.Context, AdapterInput, AdapterStateStore) (context.Context, error)
	CaptureState(context.Context, AdapterInput, string, AdapterStateStore) error
}
