// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const nativeControlLimit = 4096
const nativeControlWindow = 30 * time.Second

var nativeControlReplays = struct {
	sync.Mutex
	seen map[string]time.Time
}{seen: make(map[string]time.Time)}

func nativeControlMAC(token, domain string, fields ...string) string {
	b, _ := json.Marshal(append([]string{domain}, fields...))
	m := hmac.New(sha256.New, []byte(token))
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}

// NativeProxyPassword cannot be used to authenticate runtime control requests.
func NativeProxyPassword(token string) string {
	return nativeControlMAC(token, "aiprouter/native/proxy/v1")
}
func nativeControlProof(r *http.Request, token, domain string, extra ...string) string {
	return nativeControlMAC(token, domain, append([]string{r.Header.Get("X-Aiprouter-Nonce"), r.Header.Get("X-Aiprouter-Time"), r.Method, r.URL.RequestURI()}, extra...)...)
}
func nativeControlAuthorize(r *http.Request, token string) bool {
	nonce := r.Header.Get("X-Aiprouter-Nonce")
	b, e := hex.DecodeString(nonce)
	if e != nil || len(b) != 32 || len(token) != 64 || r.ContentLength != 0 {
		return false
	}
	stamp, e := strconv.ParseInt(r.Header.Get("X-Aiprouter-Time"), 10, 64)
	if e != nil {
		return false
	}
	now := time.Now()
	when := time.Unix(stamp, 0)
	if now.Sub(when) > nativeControlWindow || when.Sub(now) > nativeControlWindow {
		return false
	}
	want := nativeControlProof(r, token, "aiprouter/native/request/v1")
	if !hmac.Equal([]byte(want), []byte(r.Header.Get("X-Aiprouter-Proof"))) {
		return false
	}
	nativeControlReplays.Lock()
	defer nativeControlReplays.Unlock()
	for k, v := range nativeControlReplays.seen {
		if now.After(v) {
			delete(nativeControlReplays.seen, k)
		}
	}
	key := token + nonce
	if _, ok := nativeControlReplays.seen[key]; ok {
		return false
	}
	if len(nativeControlReplays.seen) >= 8192 {
		return false
	}
	nativeControlReplays.seen[key] = when.Add(nativeControlWindow)
	return true
}
func nativeControlRespond(w http.ResponseWriter, r *http.Request, token string, status int, body []byte) {
	if len(body) > nativeControlLimit {
		status = http.StatusInternalServerError
		body = []byte(`{"error":"control response too large"}`)
	}
	w.Header().Set("X-Aiprouter-Proof", nativeControlProof(r, token, "aiprouter/native/response/v1", strconv.Itoa(status), string(body)))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func nativeControlRequest(ctx context.Context, address, token, method, path string) ([]byte, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil || host != "127.0.0.1" {
		return nil, errors.New("invalid native control address")
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+address+path, nil)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	req.Header.Set("X-Aiprouter-Nonce", hex.EncodeToString(nonce))
	req.Header.Set("X-Aiprouter-Time", strconv.FormatInt(time.Now().Unix(), 10))
	req.Header.Set("X-Aiprouter-Proof", nativeControlProof(req, token, "aiprouter/native/request/v1"))
	client := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("native control redirect refused") }}
	defer client.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, nativeControlLimit+1))
	if err != nil {
		return nil, err
	}
	want := nativeControlProof(req, token, "aiprouter/native/response/v1", strconv.Itoa(res.StatusCode), string(body))
	if len(body) > nativeControlLimit || !hmac.Equal([]byte(want), []byte(res.Header.Get("X-Aiprouter-Proof"))) || res.StatusCode != http.StatusOK {
		return nil, errors.New("native control response authentication failed")
	}
	return body, nil
}
