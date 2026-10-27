// SPDX-License-Identifier: Apache-2.0
package inference

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Handler struct{ c Config }

func New(c Config) (*Handler, error) {
	if c.Authorizer == nil || c.Router == nil || c.Credentials == nil || c.Usage == nil {
		return nil, errors.New("inference: authorizer, router, credentials and usage recorder are required")
	}
	if _, ok := c.Authorizer.(Dispatcher); !ok {
		return nil, errors.New("inference: authorizer must implement atomic Dispatcher")
	}
	if c.MaxRequestBytes > 512<<20 {
		return nil, errors.New("inference: request limit exceeds 512 MiB")
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.MaxRequestBytes <= 0 {
		c.MaxRequestBytes = 256 << 20
	}
	return &Handler{c}, nil
}
func newID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return "resp_" + hex.EncodeToString(b[:])
}
func (h *Handler) event(ctx context.Context, s string) {
	if h.c.Observer != nil {
		h.c.Observer.Event(ctx, s)
	}
}
func writeError(w http.ResponseWriter, err error) {
	e := publicError(err)
	if errors.Is(err, ErrAmbiguous) {
		w.Header().Del("Retry-After")
		w.Header().Set("x-should-retry", "false")
	} else if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(e.RetryAfter.Seconds())+1))
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(object{"error": object{"type": e.Code, "code": e.Code, "message": e.Code}})
}
func bearer(r *http.Request) string {
	s := r.Header.Get("Authorization")
	if strings.HasPrefix(s, "Bearer ") {
		return strings.TrimPrefix(s, "Bearer ")
	}
	return r.Header.Get("x-api-key")
}
func routePath(path string) (Wire, Operation, bool) {
	switch path {
	case "/v1/chat/completions":
		return Chat, Generate, true
	case "/v1/responses":
		return Responses, Generate, true
	case "/v1/messages":
		return Anthropic, Generate, true
	case "/v1/responses/compact":
		return Responses, Compact, true
	case "/v1/messages/count_tokens":
		return Anthropic, CountTokens, true
	case "/v1/images/generations":
		return Responses, ImageGeneration, true
	case "/v1/images/edits":
		return Responses, ImageEdit, true
	case "/v1/alpha/search":
		return Responses, Search, true
	}
	return "", "", false
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/backend-api/codex/") {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Path = "/v1/" + strings.TrimPrefix(u.Path, "/backend-api/codex/")
		copy.URL = &u
		r = copy
	}
	if h.serveExtra(w, r) {
		return
	}
	tracked := &trackedWriter{ResponseWriter: w}
	w = tracked
	wire, op, ok := routePath(r.URL.Path)
	models := r.URL.Path == "/v1/models"
	artifact := strings.HasPrefix(r.URL.Path, "/v1/aiprouter/artifacts/") || strings.HasPrefix(r.URL.Path, "/v1/opencodex/artifacts/")
	if !ok && !models && !artifact {
		writeError(w, &Error{404, "not_found", 0})
		return
	}
	if (models || artifact) && r.Method != "GET" || ok && r.Method != "POST" {
		writeError(w, &Error{405, "method_not_allowed", 0})
		return
	}
	p, release, err := h.c.Authorizer.Admit(r.Context(), bearer(r))
	if err != nil {
		writeError(w, err)
		return
	}
	if release != nil {
		defer release()
	}
	if p.OrganizationID == "" || p.UserID == "" || p.KeyID == "" {
		writeError(w, ErrUnauthorized)
		return
	}
	if models {
		m, e := h.c.Router.Models(r.Context(), p)
		if e != nil {
			writeError(w, e)
			return
		}
		if m == nil {
			m = []Model{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(object{"object": "list", "data": m})
		return
	}
	if artifact {
		if h.c.Artifacts == nil {
			writeError(w, ErrUnsupported)
			return
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		u, e := h.c.Artifacts.URL(r.Context(), p, id)
		if e != nil {
			writeError(w, e)
			return
		}
		http.Redirect(w, r, u, http.StatusSeeOther)
		return
	}
	raw, e := h.readRequest(r, h.c.MaxRequestBytes)
	if e != nil {
		writeError(w, e)
		return
	}
	if int64(len(raw)) > h.c.MaxRequestBytes {
		writeError(w, &Error{413, "request_too_large", 0})
		return
	}
	b, e := decode(raw)
	if e != nil {
		writeError(w, e)
		return
	}
	if str(b["model"]) == "" {
		writeError(w, &Error{400, "model_required", 0})
		return
	}
	if op == Generate && wire == Responses {
		var state *Continuation
		state, e = h.restore(r.Context(), p, b)
		if e != nil {
			writeError(w, e)
			return
		}
		if state != nil {
			r = r.WithContext(context.WithValue(r.Context(), continuationKey{}, *state))
		}
	}
	id := newID()
	w.Header().Set("X-Request-ID", id)
	rec := UsageRecord{RequestID: id, Model: str(b["model"])}
	defer func() {
		rec.Canceled = r.Context().Err() != nil
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer cancel()
		if e := h.c.Usage.Record(ctx, p, rec); e != nil {
			h.event(ctx, "usage_write_failed")
			h.c.Logger.ErrorContext(ctx, "inference usage write failed", "request_id", id)
		} else {
			h.event(ctx, "usage_recorded")
		}
	}()
	err = h.execute(r.Context(), w, p, wire, op, b, &rec)
	if err != nil {
		rec.Status = publicError(err).Status
		if !tracked.started {
			writeError(w, err)
		} else {
			h.event(r.Context(), "stream_failed")
			if strings.Contains(w.Header().Get("Content-Type"), "text/event-stream") {
				_ = emitSSE(w, "error", object{"type": "error", "error": object{"code": publicError(err).Code, "message": publicError(err).Code}})
			}
		}
	}
}
func (h *Handler) restore(ctx context.Context, p Principal, b object) (*Continuation, error) {
	id := str(b["previous_response_id"])
	if id == "" {
		return nil, nil
	}
	if h.c.Continuations == nil {
		return nil, ErrContinuation
	}
	c, e := h.c.Continuations.Load(ctx, p, id)
	if e != nil {
		return nil, ErrContinuation
	}
	if !c.ExpiresAt.After(time.Now()) || c.Model != str(b["model"]) {
		return nil, ErrContinuation
	}
	var old []any
	if json.Unmarshal(c.Input, &old) != nil {
		return nil, ErrContinuation
	}
	input := b["input"]
	if s, ok := input.(string); ok {
		input = []any{object{"role": "user", "content": s}}
	}
	b["input"] = append(old, arr(input)...)
	delete(b, "previous_response_id")
	return &c, nil
}

type continuationKey struct{}
type BoundCredentials interface {
	SelectBound(context.Context, Principal, Target, string) (Credential, error)
}

func (h *Handler) save(ctx context.Context, p Principal, b, out object, rec *UsageRecord) error {
	if b["store"] == false {
		return nil
	}
	if h.c.Continuations == nil {
		return nil
	}
	if e := h.c.Authorizer.BeforeSend(ctx, p); e != nil {
		return e
	}
	input := b["input"]
	if s, ok := input.(string); ok {
		input = []any{object{"role": "user", "content": s}}
	}
	history := append(append([]any{}, arr(input)...), arr(out["output"])...)
	data, e := json.Marshal(history)
	if e != nil {
		return e
	}
	if len(data) > 32<<20 {
		return &Error{502, "continuation_too_large", 0}
	}
	return h.c.Continuations.Save(ctx, p, str(out["id"]), Continuation{Provider: rec.Provider, AccountID: rec.AccountID, Model: str(b["model"]), Input: data, ExpiresAt: time.Now().Add(24 * time.Hour)})
}
func (h *Handler) guard(ctx context.Context, p Principal) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return h.c.Authorizer.BeforeSend(ctx, p)
}
func retryWait(value string, fallback time.Duration) time.Duration {
	if n, e := strconv.ParseInt(value, 10, 32); e == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if t, e := http.ParseTime(value); e == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return fallback
}
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func (h *Handler) execute(ctx context.Context, w http.ResponseWriter, p Principal, in Wire, op Operation, b object, rec *UsageRecord) error {
	route, e := h.c.Router.Resolve(ctx, p, str(b["model"]), op)
	if e != nil {
		return e
	}
	if len(route.Targets) == 0 {
		return ErrNoCredential
	}
	if route.MaxSends < 1 || route.MaxSends > 100 {
		return &Error{503, "invalid_send_budget", 0}
	}
	for ti, t := range route.Targets {
		adapter := h.c.Adapters[t.Wire]
		if adapter == nil && t.Wire != Chat && t.Wire != Responses && t.Wire != Anthropic {
			return ErrUnsupported
		}
		if op == Compact && t.Wire != Responses || op == CountTokens && t.Wire != Anthropic {
			return ErrUnsupported
		}
		client, local, e := h.c.Destinations.client(t.BaseURL)
		if e != nil {
			return e
		}
		if e = h.guard(ctx, p); e != nil {
			return e
		}
		var cred Credential
		if state, ok := ctx.Value(continuationKey{}).(Continuation); ok && state.Provider != "" {
			if t.Provider != state.Provider {
				return ErrContinuation
			}
			if bound, ok := h.c.Credentials.(BoundCredentials); ok {
				cred, e = bound.SelectBound(ctx, p, t, state.AccountID)
			} else {
				cred, e = h.c.Credentials.Select(ctx, p, t, "")
				if cred.AccountID != state.AccountID {
					return ErrContinuation
				}
			}
		} else {
			cred, e = h.c.Credentials.Select(ctx, p, t, "")
		}
		if e != nil {
			return e
		}
		refreshed := false
		rateRetries, transientSends := 0, 0
		visited := map[string]bool{cred.AccountID: true}
		for {
			if rec.Sends >= route.MaxSends {
				return &Error{429, "request_send_budget_exhausted", 0}
			}
			attemptCtx := ctx
			if prepare, ok := adapter.(ContextPreparer); ok {
				attemptCtx = prepare.PrepareContext(ctx)
			}
			attemptTimeout := t.AttemptTimeout
			if attemptTimeout == 0 && t.Wire == Wire("kiro") {
				attemptTimeout = 200 * time.Second
			}
			payload := clone(b)
			if op == ImageGeneration || op == ImageEdit {
				payload["response_format"] = "b64_json"
			}
			if op == Generate && adapter == nil {
				payload, e = translateRequest(b, in, t.Wire)
				if e != nil {
					return e
				}
			}
			if t.Model != "" {
				payload["model"] = t.Model
			}
			if t.Wire == Responses && op == Generate {
				payload["store"] = false
			}
			path := wirePath(t.Wire)
			switch op {
			case Compact:
				path = "/responses/compact"
			case CountTokens:
				path = "/messages/count_tokens"
			case ImageGeneration, ImageEdit, Search:
				path = "/" + string(op)
			}
			data, e := json.Marshal(payload)
			if e != nil {
				return e
			}
			responseTarget := t
			endpoint := strings.TrimRight(t.BaseURL, "/") + path
			var adapterHeaders http.Header
			var adapterInput AdapterInput
			if adapter != nil {
				adapterInput = AdapterInput{Principal: p, Target: t, Credential: cred, Operation: op, Wire: in, Body: data}
				if pre, ok := adapter.(PreparingAdapter); ok {
					prepared, err := pre.Prepare(attemptCtx, adapterInput, &requestExecutor{h: h, p: p, target: t, record: rec, maxSends: route.MaxSends})
					if err != nil {
						return err
					}
					adapterInput.Body = prepared
				}
				built, err := adapter.Build(attemptCtx, adapterInput)
				if err != nil {
					return err
				}
				u, err := url.Parse(built.URL)
				base, _ := url.Parse(t.BaseURL)
				if err != nil || u.User != nil || u.Fragment != "" || origin(u) != origin(base) {
					return ErrDestination
				}
				if built.AttemptTimeout > 0 {
					attemptTimeout = built.AttemptTimeout
				}
				endpoint = built.URL
				data = built.Body
				adapterHeaders = built.Headers
				responseTarget.Wire = built.ResponseWire
				if responseTarget.Wire != Chat && responseTarget.Wire != Responses && responseTarget.Wire != Anthropic {
					return ErrUnsupported
				}
			}
			sendCtx, cancel := context.WithCancel(attemptCtx)
			if attemptTimeout > 0 {
				cancel()
				sendCtx, cancel = context.WithTimeout(attemptCtx, attemptTimeout)
			}
			req, e := http.NewRequestWithContext(sendCtx, "POST", endpoint, bytes.NewReader(data))
			if e != nil {
				cancel()
				return ErrDestination
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if t.Wire == Anthropic {
				req.Header.Set("anthropic-version", "2023-06-01")
			}
			for k, vs := range t.Headers {
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			for k, vs := range cred.Headers {
				req.Header.Del(k)
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			if adapter == nil && cred.Secret != "" {
				if t.Wire == Anthropic && t.Auth != OAuth {
					req.Header.Set("x-api-key", cred.Secret)
				} else {
					req.Header.Set("Authorization", "Bearer "+cred.Secret)
				}
			} else if adapter == nil && t.Auth != Anonymous {
				cancel()
				return ErrNoCredential
			}
			for k, vs := range adapterHeaders {
				req.Header.Del(k)
				for _, v := range vs {
					req.Header.Add(k, v)
				}
			}
			if e = h.guard(ctx, p); e != nil {
				cancel()
				return e
			}
			transientSends++
			rec.Provider = t.Provider
			rec.Model = str(payload["model"])
			rec.AccountID = cred.AccountID
			client, local, e = h.c.Destinations.client(t.BaseURL)
			if e != nil {
				cancel()
				return e
			}
			sent := gateClient(client, sendCtx, p, h.c.Authorizer, func() { rec.Sends++; h.event(ctx, "upstream_send") })
			resp, e := client.Do(req)
			if e != nil {
				cancel()
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if !sent.Load() {
					return e
				}
				rec.UnmeasuredAttempts++
				return ErrAmbiguous
			}
			rec.Status = resp.StatusCode
			if resp.StatusCode >= 500 {
				rec.UnmeasuredAttempts++
			}
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if adapter != nil {
					original := resp
					var decoded *http.Response
					var err error
					if contextual, ok := adapter.(ContextualAdapter); ok {
						decoded, err = contextual.DecodeWithInput(sendCtx, adapterInput, resp)
					} else {
						decoded, err = adapter.Decode(sendCtx, resp)
					}
					if err != nil {
						original.Body.Close()
						cancel()
						return err
					}
					if decoded == nil || decoded.Body == nil {
						original.Body.Close()
						cancel()
						return &Error{502, "invalid_adapter_response", 0}
					}
					resp = decoded
				}
				err := h.deliver(sendCtx, w, p, in, op, b, responseTarget, local, resp, rec, cancel)
				resp.Body.Close()
				cancel()
				return err
			}
			// Read only bounded error data for replay markers; never log provider payloads.
			errorBody, readErr := readBody(resp.Body, 1<<20, 5*time.Second, 5*time.Second, cancel)
			resp.Body.Close()
			if readErr != nil {
				cancel()
				return &Error{resp.StatusCode, "upstream_refused", 0}
			}
			if m, err := decode(errorBody); err == nil {
				code := str(obj(m["error"])["code"])
				if code == "" {
					code = str(m["code"])
				}
				switch code {
				case "upstream_reset_replay_refused", "upstream_no_response", "upstream_closed_before_response":
					cancel()
					return ErrAmbiguous
				}
			}
			// These responses have not reached the client. Only explicit refusal policies permit replay.
			delay := retryWait(resp.Header.Get("Retry-After"), t.Retry.Interval)
			maxWait := t.Retry.MaxWait
			if maxWait <= 0 {
				maxWait = 60 * time.Second
			}
			retry := false
			nextTarget := false
			_, continued := ctx.Value(continuationKey{}).(Continuation)
			allowed := rec.Sends < route.MaxSends && op != ImageGeneration && op != ImageEdit
			if continued {
				t.Retry.RotateOn429 = false
				t.Retry.FallbackOn429 = false
				t.Retry.FallbackOn5xx = false
			}
			if reporter, ok := h.c.Credentials.(FailureReporter); ok && resp.StatusCode == 429 {
				if e = h.guard(ctx, p); e == nil {
					e = reporter.Failed(ctx, p, t, cred, resp.StatusCode, delay)
				}
				if e != nil {
					resp.Body.Close()
					cancel()
					return e
				}
			}
			if allowed && resp.StatusCode == 401 && t.Auth == OAuth && t.Retry.RefreshOn401 && !refreshed {
				if e = h.guard(ctx, p); e != nil {
					resp.Body.Close()
					cancel()
					return e
				}
				updated, err := h.c.Credentials.Refresh(ctx, p, t, cred)
				if err == nil {
					cred = updated
					retry = true
					refreshed = true
				}
			}
			if allowed && resp.StatusCode == 429 {
				if t.Auth == APIKey && rateRetries < t.Retry.RateLimitRetries && delay <= maxWait {
					retry = true
					rateRetries++
					if e = h.guard(ctx, p); e != nil {
						resp.Body.Close()
						cancel()
						return e
					}
					resp.Body.Close()
					cancel()
					if e = wait(ctx, delay); e != nil {
						return e
					}
					continue
				}
				if t.Retry.RotateOn429 {
					if e = h.guard(ctx, p); e != nil {
						resp.Body.Close()
						cancel()
						return e
					}
					next, err := h.c.Credentials.Select(ctx, p, t, cred.AccountID)
					if err == nil && !visited[next.AccountID] {
						cred = next
						visited[next.AccountID] = true
						retry = true
						rateRetries = 0
					}
				}
				if !retry && t.Retry.FallbackOn429 && ti+1 < len(route.Targets) {
					nextTarget = true
				}
			}
			if allowed && resp.StatusCode >= 500 && resp.StatusCode <= 599 {
				if t.Auth == APIKey && (t.Wire == Chat || t.Wire == Responses) && transientSends < t.Retry.TransientSends {
					retry = true
				} else if t.Retry.FallbackOn5xx && ti+1 < len(route.Targets) {
					nextTarget = true
				}
			}
			if retry || nextTarget {
				resp.Body.Close()
				cancel()
				if nextTarget {
					break
				}
				continue
			}
			// Never expose arbitrary upstream error bodies: these may contain credentials or internal URLs.
			resp.Body.Close()
			cancel()
			if resp.StatusCode >= 300 && resp.StatusCode < 400 {
				return ErrDestination
			}
			if resp.StatusCode == 429 {
				return &Error{429, "upstream_rate_limited", delay}
			}
			return &Error{resp.StatusCode, "upstream_refused", 0}
		}
	}
	return ErrNoCredential
}
func (h *Handler) deliver(ctx context.Context, w http.ResponseWriter, p Principal, in Wire, op Operation, b object, t Target, local bool, resp *http.Response, rec *UsageRecord, cancel context.CancelFunc) error {
	stall := 300 * time.Second
	if local {
		stall = 0
	}
	if t.StallTimeout != nil {
		stall = *t.StallTimeout
	}
	stream := strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
	if stream {
		return h.deliverStream(ctx, w, p, in, op, b, t, resp, rec, cancel, stall)
	}
	total := 180 * time.Second
	idle := 30 * time.Second
	if op == ImageGeneration || op == ImageEdit {
		total = 15 * time.Minute
		idle = stall
	}
	maxBody := int64(32 << 20)
	if op == ImageGeneration || op == ImageEdit {
		maxBody = 140 << 20
	}
	raw, e := readBody(resp.Body, maxBody, total, idle, cancel)
	if e != nil {
		return e
	}
	out, e := decode(raw)
	if e != nil {
		return &Error{502, "invalid_upstream_json", 0}
	}
	rec.Usage = readUsage(out, t.Wire)
	if op == Generate && in != t.Wire {
		r, e := parseResult(out, t.Wire)
		if e != nil {
			return e
		}
		if e = validateTools(r, b, in); e != nil {
			return e
		}
		out = r.response(in)
	}
	if op == ImageGeneration || op == ImageEdit {
		if e = h.images(ctx, p, out); e != nil {
			return e
		}
	}
	if op == Generate && in == Responses {
		if e = h.save(ctx, p, b, out, rec); e != nil {
			h.event(ctx, "continuation_write_failed")
			h.c.Logger.WarnContext(ctx, "inference continuation not saved", "request_id", rec.RequestID)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	return json.NewEncoder(w).Encode(out)
}
func readBody(r io.ReadCloser, max int64, total, idle time.Duration, cancel context.CancelFunc) ([]byte, error) {
	var deadline, inactivity *time.Timer
	if total > 0 {
		deadline = time.AfterFunc(total, func() { cancel(); r.Close() })
		defer deadline.Stop()
	}
	if idle > 0 {
		inactivity = time.AfterFunc(idle, func() { cancel(); r.Close() })
		defer inactivity.Stop()
	}
	var out bytes.Buffer
	buf := make([]byte, 32<<10)
	for {
		n, e := r.Read(buf)
		if n > 0 {
			if int64(out.Len()+n) > max {
				return nil, &Error{502, "upstream_body_too_large", 0}
			}
			out.Write(buf[:n])
			if inactivity != nil {
				inactivity.Reset(idle)
			}
		}
		if e == io.EOF {
			return out.Bytes(), nil
		}
		if e != nil {
			return nil, e
		}
	}
}
func (h *Handler) images(ctx context.Context, p Principal, out object) error {
	if h.c.Artifacts == nil {
		return ErrUnsupported
	}
	var total int
	var images []GeneratedImage
	rows := arr(out["data"])
	for _, v := range rows {
		m := obj(v)
		s := str(m["b64_json"])
		if s == "" {
			return &Error{502, "image_inline_data_required", 0}
		}
		if base64.StdEncoding.DecodedLen(len(s)) > 50<<20 {
			return &Error{502, "image_too_large", 0}
		}
		b, e := base64.StdEncoding.DecodeString(s)
		if e != nil {
			return &Error{502, "invalid_image", 0}
		}
		total += len(b)
		if len(b) > 50<<20 || total > 100<<20 {
			return &Error{502, "image_too_large", 0}
		}
		images = append(images, GeneratedImage{Data: b, ContentType: http.DetectContentType(b)})
	}
	if len(images) == 0 {
		return &Error{502, "image_result_empty", 0}
	}
	if e := h.guard(ctx, p); e != nil {
		return e
	}
	var urls []string
	if batch, ok := h.c.Artifacts.(ArtifactBatchStore); ok {
		var e error
		urls, e = batch.PutBatch(ctx, p, images)
		if e != nil {
			return e
		}
		if len(urls) != len(images) {
			return &Error{502, "invalid_artifact_batch", 0}
		}
	} else {
		for _, im := range images {
			if e := h.guard(ctx, p); e != nil {
				return e
			}
			u, e := h.c.Artifacts.Put(ctx, p, im.Data, im.ContentType)
			if e != nil {
				return e
			}
			urls = append(urls, u)
		}
	}
	for i, row := range rows {
		m := obj(row)
		delete(m, "b64_json")
		m["url"] = urls[i]
	}
	return nil
}

type trackedWriter struct {
	http.ResponseWriter
	started bool
}

func (w *trackedWriter) WriteHeader(status int) {
	w.started = true
	w.ResponseWriter.WriteHeader(status)
}
func (w *trackedWriter) Write(b []byte) (int, error) {
	w.started = true
	return w.ResponseWriter.Write(b)
}
func (w *trackedWriter) Flush() {
	w.started = true
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
