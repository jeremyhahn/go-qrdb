// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/api/mcp/wire"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// maxRefusalBodyBytes bounds how much of a refusal's body the client reads:
// the server's JSON error is a sentence, and a peer that is not a qrdb node
// must not make the client buffer an unbounded reply.
const maxRefusalBodyBytes = 64 << 10

// refusalCodeByStatus names the wire code each auth-chain refusal status
// means: the server's verdict on the caller's credential, which the other
// transports return as the same typed refusal.
var refusalCodeByStatus = map[int]wire.Code{
	http.StatusUnauthorized: wire.CodeUnauthenticated,
	http.StatusForbidden:    wire.CodePermissionDenied,
	// The pre-authentication client-address limiter (go-qrdb#1197)
	// refuses before the JSON-RPC layer runs, so its 429 is an HTTP status.
	http.StatusTooManyRequests: wire.CodeResourceExhausted,
}

// refusalCapture keeps the auth chain's HTTP refusal of a request whose
// context carries a refusalSlot. mcp-go reports a 401 as an
// authorization-required error and a 403 on initialize as a legacy SSE
// server, dropping the body that says why the caller was refused, so the
// caller reads the refusal from its slot instead. The refusal can come on
// any request, not only initialize: a server that serves no-login tools
// (pkg/api/publicverbs) answers initialize to a caller whose credential it
// refused, and refuses that credential at the first tools/call that needs
// an identity.
type refusalCapture struct {
	next http.RoundTripper
}

// RoundTrip sends r and, when r's context carries a refusalSlot, keeps the
// first 401 or 403 reply in it as the typed refusal. The body is handed on
// unchanged.
func (c *refusalCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil {
		return resp, err
	}
	slot, ok := r.Context().Value(refusalSlotKey{}).(*refusalSlot)
	if !ok {
		return resp, nil
	}
	code, ok := refusalCodeByStatus[resp.StatusCode]
	if !ok {
		return resp, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRefusalBodyBytes))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(body))
	if readErr != nil {
		return resp, nil
	}
	slot.refusal.CompareAndSwap(nil, httpRefusal(slot.method, code, resp, body))
	return resp, nil
}

// refusalSlotKey is the context key of a call's refusalSlot.
type refusalSlotKey struct{}

// refusalSlot holds the typed refusal of one call: initialize or one
// tools/call. Each call has its own, so concurrent calls never read each
// other's refusal.
type refusalSlot struct {
	method  string
	refusal atomic.Pointer[capturedRefusal]
}

// withRefusalSlot returns ctx carrying a new slot for the call named method,
// and the slot.
func withRefusalSlot(ctx context.Context, method string) (context.Context, *refusalSlot) {
	slot := &refusalSlot{method: method}
	return context.WithValue(ctx, refusalSlotKey{}, slot), slot
}

// Refusal returns the typed refusal (a *transport.RequestError) the call
// was answered with, or nil when none came.
func (s *refusalSlot) Refusal() error {
	if r := s.refusal.Load(); r != nil {
		return r.err
	}
	return nil
}

// httpRefusal builds the typed refusal of the call named method from the
// server's JSON error body (transport.ErrorResponse, the body REST's auth
// chain refuses with too) and its Retry-After header, through the mapping
// every MCP tool refusal takes.
func httpRefusal(method string, code wire.Code, resp *http.Response, body []byte) *capturedRefusal {
	var errResp transport.ErrorResponse
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		msg = errResp.Error
	}
	if msg == "" {
		msg = resp.Status
	}
	return &capturedRefusal{err: mapToolError(method, &wire.ErrorPayload{
		Code:              code,
		Message:           msg,
		RetryAfterSeconds: retryAfterSeconds(resp.Header.Get("Retry-After")),
		Reason:            errResp.Code,
		Metadata:          errResp.Metadata,
	})}
}

// capturedRefusal holds the typed refusal a slot kept.
type capturedRefusal struct {
	err error
}

// retryAfterSeconds reads a Retry-After header (RFC 9110 §10.2.3) in either
// form, delay-seconds or HTTP-date, as whole seconds; zero when absent,
// malformed or not in the future.
func retryAfterSeconds(v string) int64 {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		return max(secs, 0)
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return int64((d + time.Second - 1) / time.Second)
		}
	}
	return 0
}
