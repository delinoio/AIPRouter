// SPDX-License-Identifier: Apache-2.0
package hosted

import (
	"context"
	"encoding/json"
	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
	"io"
	"net/http"
)

// StandardCodec uses the central translator, including opaque reasoning and tool
// metadata. The wrapper never implements a second provider signature codec.
type StandardCodec struct{}

func (StandardCodec) Request(b json.RawMessage, from, to inference.Wire) (json.RawMessage, error) {
	return inference.TranslateRequestJSON(b, from, to)
}
func (StandardCodec) Collect(ctx context.Context, r *http.Response, w inference.Wire, max int64) (json.RawMessage, error) {
	if r == nil || r.Body == nil {
		return nil, fault(502, "hosted_invalid_response")
	}
	reader := &limitedBody{ReadCloser: r.Body, remaining: max}
	r.Body = reader
	b, err := inference.AggregateResponse(ctx, r, w)
	if err == nil && int64(len(b)) > max {
		return nil, fault(502, "hosted_buffer_limit")
	}
	return b, err
}

type limitedBody struct {
	io.ReadCloser
	remaining int64
}

func (r *limitedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > r.remaining+1 {
		p = p[:r.remaining+1]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return 0, fault(502, "hosted_buffer_limit")
	}
	return n, err
}
