// SPDX-License-Identifier: Apache-2.0
package hosted

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/delinoio/AIPRouter/packages/aiprouter/inference"
)

type Adapter struct {
	inner  inference.Adapter
	config Config
}

func New(inner inference.Adapter, c Config) (*Adapter, error) {
	if c.Codec == nil {
		c.Codec = StandardCodec{}
	}
	if inner == nil || c.Resolver == nil {
		return nil, fault(500, "hosted_configuration_missing")
	}
	return &Adapter{inner: inner, config: defaults(c)}, nil
}
func (a *Adapter) Build(ctx context.Context, in inference.AdapterInput) (inference.AdapterRequest, error) {
	return a.inner.Build(ctx, in)
}
func (a *Adapter) Decode(ctx context.Context, r *http.Response) (*http.Response, error) {
	return a.inner.Decode(ctx, r)
}
func (a *Adapter) PrepareContext(ctx context.Context) context.Context {
	if p, ok := a.inner.(inference.ContextPreparer); ok {
		return p.PrepareContext(ctx)
	}
	return ctx
}
func (a *Adapter) RestoreState(ctx context.Context, in inference.AdapterInput, s inference.AdapterStateStore) (context.Context, error) {
	if p, ok := a.inner.(inference.StatefulAdapter); ok {
		return p.RestoreState(ctx, in, s)
	}
	return ctx, nil
}
func (a *Adapter) CaptureState(ctx context.Context, in inference.AdapterInput, id string, s inference.AdapterStateStore) error {
	if p, ok := a.inner.(inference.StatefulAdapter); ok {
		return p.CaptureState(ctx, in, id, s)
	}
	return nil
}

func (a *Adapter) model(ctx context.Context, in inference.AdapterInput, e inference.AdapterExecutor, s inference.AdapterStateStore) (*http.Response, inference.Wire, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if p, ok := a.inner.(inference.PreparingAdapter); ok {
		b, err := p.Prepare(ctx, in, e)
		if err != nil {
			return nil, "", err
		}
		in.Body = b
	}
	if p, ok := a.inner.(inference.ExecutingAdapter); ok {
		return p.Execute(ctx, in, e, s)
	}
	req, err := a.inner.Build(ctx, in)
	if err != nil {
		return nil, "", err
	}
	r, err := e.Do(ctx, req)
	if err != nil {
		return nil, "", err
	}
	if r == nil || r.Body == nil {
		return nil, "", fault(502, "hosted_invalid_response")
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return r, req.ResponseWire, nil
	}
	var out *http.Response
	if p, ok := a.inner.(inference.ContextualAdapter); ok {
		out, err = p.DecodeWithInput(ctx, in, r)
	} else {
		out, err = a.inner.Decode(ctx, r)
	}
	if err != nil {
		r.Body.Close()
	}
	return out, req.ResponseWire, err
}

type turn struct {
	a             *Adapter
	input         inference.AdapterInput
	counts        map[Kind]int
	failedQueries map[string]bool
	outputs       []any
}

func (a *Adapter) Execute(ctx context.Context, in inference.AdapterInput, e inference.AdapterExecutor, s inference.AdapterStateStore) (*http.Response, inference.Wire, error) {
	body, err := decode(in.Body)
	if err != nil {
		return nil, "", err
	}
	if in.Operation != inference.Generate {
		return a.model(ctx, in, e, s)
	}
	active, err := catalog(body, in.Wire, a.config.EnableVideo)
	if err != nil {
		return nil, "", err
	}
	if len(active) == 0 && !a.config.DescribeImages {
		return a.model(ctx, in, e, s)
	}
	raw, _ := json.Marshal(body)
	raw, err = a.config.Codec.Request(raw, in.Wire, inference.Chat)
	if err != nil {
		return nil, "", err
	}
	body, err = decode(raw)
	if err != nil {
		return nil, "", err
	}
	stream := body["stream"] == true
	body["stream"] = false
	delete(body, "stream_options")
	t := turn{a: a, input: in, counts: map[Kind]int{}, failedQueries: map[string]bool{}}
	if a.config.DescribeImages {
		if err = t.describe(ctx, body); err != nil {
			return nil, "", err
		}
	}
	for round := 0; round <= a.config.MaxRounds; round++ {
		if err = ctx.Err(); err != nil {
			return nil, "", err
		}
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, "", err
		}
		if int64(len(raw)) > a.config.MaxBytes {
			return nil, "", fault(502, "hosted_buffer_limit")
		}
		next := in
		next.Wire = inference.Chat
		next.Body = raw
		r, wire, sendErr := a.model(ctx, next, e, s)
		if sendErr != nil {
			return nil, "", sendErr
		}
		if r == nil || r.Body == nil {
			return nil, "", fault(502, "hosted_invalid_response")
		}
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			if round == 0 && len(t.outputs) == 0 {
				return r, wire, nil
			}
			r.Body.Close()
			return nil, "", fault(502, "hosted_followup_refused") // Never replay completed paid helpers.
		}
		raw, err = a.config.Codec.Collect(ctx, r, wire, a.config.MaxBytes)
		if err != nil {
			if partial, parseErr := decode(raw); parseErr == nil {
				if x, ok := e.(inference.AttemptUsage); ok {
					x.RecordUsage(readUsage(obj(partial["usage"])))
				}
			} else if x, ok := e.(inference.AttemptAccounting); ok {
				x.RecordUnmeasuredAttempt()
			}
			return nil, "", err
		}
		result, parseErr := decode(raw)
		if parseErr != nil {
			return nil, "", parseErr
		}
		choices := arr(result["choices"])
		if len(choices) != 1 {
			return nil, "", fault(502, "hosted_invalid_choices")
		}
		choice := obj(choices[0])
		message := obj(choice["message"])
		if message == nil {
			return nil, "", fault(502, "hosted_invalid_message")
		}
		calls := arr(message["tool_calls"])
		hostedCalls := 0
		for _, v := range calls {
			if _, ok := active[str(obj(obj(v)["function"])["name"])]; ok {
				hostedCalls++
			}
		}
		if hostedCalls == 0 {
			result["hosted_outputs"] = t.outputs
			raw, _ = json.Marshal(result)
			return finalResponse(raw, result, stream), inference.Chat, nil
		}
		// Record consumed model legs before any sidecar can fail or be canceled.
		if accounting, ok := e.(inference.AttemptUsage); ok {
			accounting.RecordUsage(readUsage(obj(result["usage"])))
		} else if x, ok := e.(inference.AttemptAccounting); ok {
			x.RecordUnmeasuredAttempt()
		}
		if round == a.config.MaxRounds {
			return nil, "", fault(502, "hosted_loop_limit")
		}

		seen := map[string]bool{}
		for _, v := range calls {
			call := obj(v)
			id := str(call["id"])
			if id == "" || seen[id] {
				return nil, "", fault(502, "hosted_invalid_call_id")
			}
			seen[id] = true
		}
		messages := arr(body["messages"])
		messages = append(messages, message)
		for _, v := range calls {
			call := obj(v)
			fn := obj(call["function"])
			tool, isHosted := active[str(fn["name"])]
			if !isHosted {
				continue
			}
			id := str(call["id"])
			args, parseErr := decode([]byte(str(fn["arguments"])))
			if parseErr != nil {
				return nil, "", fault(502, "hosted_invalid_arguments")
			}
			output, runErr := t.run(ctx, tool, id, args)
			if runErr != nil {
				return nil, "", runErr
			}
			data, _ := json.Marshal(output)
			messages = append(messages, object{"role": "tool", "tool_call_id": id, "content": string(data)})
		}
		if hostedCalls != len(calls) {
			clientCalls := []any{}
			for _, v := range calls {
				if _, ok := active[str(obj(obj(v)["function"])["name"])]; !ok {
					clientCalls = append(clientCalls, v)
				}
			}
			message["tool_calls"] = clientCalls
			result["hosted_outputs"] = t.outputs
			// This model leg has already been recorded as intermediate usage.
			delete(result, "usage")
			raw, _ = json.Marshal(result)
			return finalResponse(raw, result, stream), inference.Chat, nil
		}
		body["messages"] = messages
		// The last pass must answer from gathered results. It cannot request another
		// paid helper, even when the initial request forced a particular hosted tool.
		if round+1 == a.config.MaxRounds || t.exhausted(active) {
			body["tools"] = []any{}
			body["tool_choice"] = "none"
		} else {
			body["tool_choice"] = "auto"
		}
	}
	return nil, "", fault(502, "hosted_loop_limit")
}
func readUsage(m object) inference.Usage {
	number := func(k string) *int64 {
		f, ok := m[k].(float64)
		if !ok || f < 0 {
			return nil
		}
		n := int64(f)
		return &n
	}
	return inference.Usage{InputTokens: number("prompt_tokens"), OutputTokens: number("completion_tokens"), CacheReadTokens: number("cache_read_input_tokens"), CacheWriteTokens: number("cache_creation_input_tokens")}
}
func finalResponse(raw []byte, result object, stream bool) *http.Response {
	content := "application/json"
	if stream {
		var b bytes.Buffer
		for _, v := range arr(result["choices"]) {
			c := obj(v)
			chunk := object{"id": result["id"], "object": "chat.completion.chunk", "model": result["model"], "choices": []any{object{"index": 0, "delta": c["message"], "finish_reason": nil}}, "hosted_outputs": result["hosted_outputs"]}
			data, _ := json.Marshal(chunk)
			b.WriteString("data: ")
			b.Write(data)
			b.WriteString("\n\n")
			chunk["choices"] = []any{object{"index": 0, "delta": object{}, "finish_reason": c["finish_reason"]}}
			chunk["usage"] = result["usage"]
			delete(chunk, "hosted_outputs")
			data, _ = json.Marshal(chunk)
			b.WriteString("data: ")
			b.Write(data)
			b.WriteString("\n\n")
		}
		b.WriteString("data: [DONE]\n\n")
		raw = b.Bytes()
		content = "text/event-stream"
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{content}, "Cache-Control": []string{"no-store"}}, Body: io.NopCloser(bytes.NewReader(raw))}
}
func (t *turn) limit(k Kind) int {
	switch k {
	case Search:
		return t.a.config.MaxSearches
	case Vision:
		return t.a.config.MaxDescriptions
	case Image:
		return t.a.config.MaxImages
	case Video:
		return t.a.config.MaxVideos
	}
	return 0
}
func (t *turn) exhausted(active map[string]tool) bool {
	for _, v := range active {
		if t.counts[v.kind] < t.limit(v.kind) {
			return false
		}
	}
	return true
}
func (t *turn) execute(ctx context.Context, tool tool, id string, args object) (OperationResult, error) {
	if err := ctx.Err(); err != nil {
		return OperationResult{}, err
	}
	if t.counts[tool.kind] >= t.limit(tool.kind) {
		return OperationResult{Output: "Hosted tool limit reached; answer using existing results.", Failed: true}, nil
	}
	t.counts[tool.kind]++
	timeout := t.a.config.MediaTimeout
	if tool.kind == Search {
		timeout = t.a.config.SearchTimeout
	}
	if tool.kind == Vision {
		timeout = t.a.config.VisionTimeout
	}
	opctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, _ := json.Marshal(args)
	req := OperationRequest{Principal: t.input.Principal, RequestID: t.input.RequestID, CallID: id, Kind: tool.kind, Declaration: append(json.RawMessage(nil), tool.declaration...), Arguments: raw}
	executor, err := t.a.config.Resolver.Resolve(opctx, req)
	if err != nil {
		return OperationResult{}, err
	}
	if executor == nil {
		return OperationResult{}, inference.ErrUnavailable
	}
	result, err := executor.Execute(opctx, req)
	if err != nil {
		return OperationResult{}, err
	}
	if err = opctx.Err(); err != nil {
		return OperationResult{}, err
	}
	encoded, _ := json.Marshal(result)
	if int64(len(encoded)) > t.a.config.MaxBytes {
		return OperationResult{}, fault(502, "hosted_result_limit")
	}
	for _, a := range result.Artifacts {
		if a.ID == "" || !strings.HasPrefix(a.URL, "https://") {
			return OperationResult{}, fault(502, "hosted_invalid_artifact")
		}
	}
	return result, nil
}
func (t *turn) run(ctx context.Context, tool tool, id string, args object) (any, error) {
	if tool.kind == Search {
		qs, err := queries(args)
		if err != nil {
			return nil, err
		}
		results := []any{}
		sources := []Source{}
		success := false
		for _, q := range qs {
			key := strings.ToLower(q)
			var result OperationResult
			if t.failedQueries[key] {
				result = OperationResult{Output: "This query already failed; use other results.", Failed: true}
			} else {
				result, err = t.execute(ctx, tool, id, object{"query": q})
				if err != nil {
					return nil, err
				}
			}
			if result.Failed {
				t.failedQueries[key] = true
			} else {
				success = true
			}
			sources = append(sources, result.Sources...)
			results = append(results, object{"query": q, "result": result})
		}
		status := "failed"
		if success {
			status = "completed"
		}
		t.outputs = append(t.outputs, object{"type": "web_search_call", "id": id, "status": status, "action": object{"type": "search", "queries": qs}, "sources": sources})
		return results, nil
	}
	if strings.TrimSpace(str(args["prompt"])) == "" {
		return nil, fault(502, "hosted_missing_prompt")
	}
	result, err := t.execute(ctx, tool, id, args)
	if err != nil {
		return nil, err
	}
	status := "completed"
	if result.Failed {
		status = "failed"
	}
	outputType := "image_generation_call"
	if tool.kind == Video {
		outputType = "video_generation_call"
	}
	t.outputs = append(t.outputs, object{"type": outputType, "id": id, "status": status, "artifacts": result.Artifacts})
	return result, nil
}

// describe replaces only image parts; the surrounding ordered conversation stays
// intact. Descriptions are request-local, bounded, and never cached by content hash.
func (t *turn) describe(ctx context.Context, body object) error {
	for _, v := range arr(body["messages"]) {
		m := obj(v)
		parts := arr(m["content"])
		for i, p := range parts {
			part := obj(p)
			if part["type"] != "image_url" {
				continue
			}
			result, err := t.execute(ctx, tool{kind: Vision, declaration: json.RawMessage(`{"type":"vision"}`)}, "", object{"image_url": part["image_url"]})
			if err != nil {
				return err
			}
			if result.Failed {
				return fault(502, "hosted_vision_failed")
			}
			text := []rune(result.Output)
			if len(text) > 2000 {
				text = text[:2000]
			}
			parts[i] = object{"type": "text", "text": "[Image described by a vision model: " + string(text) + "]"}
		}
	}
	return nil
}

var _ inference.Adapter = (*Adapter)(nil)
var _ inference.ExecutingAdapter = (*Adapter)(nil)
var _ inference.StatefulAdapter = (*Adapter)(nil)
