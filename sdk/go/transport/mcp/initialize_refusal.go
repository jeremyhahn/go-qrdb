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
}

// initializeRefusalCapture keeps the auth chain's refusal of a request sent
// while it is armed -- the initialize POST. mcp-go reports a 401 on
// initialize as an authorization-required error and a 403 as a legacy SSE
// server, dropping the body that says why the caller was refused, so the
// dial reads the refusal from here instead.
type initializeRefusalCapture struct {
	next    http.RoundTripper
	armed   atomic.Bool
	refusal atomic.Pointer[capturedRefusal]
}

// newInitializeRefusalCapture returns a capture over next, armed.
func newInitializeRefusalCapture(next http.RoundTripper) *initializeRefusalCapture {
	c := &initializeRefusalCapture{next: next}
	c.armed.Store(true)
	return c
}

// RoundTrip sends r and, while armed, keeps the first 401 or 403 reply as
// the typed refusal. The body is handed on unchanged.
func (c *initializeRefusalCapture) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(r)
	if err != nil || !c.armed.Load() {
		return resp, err
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
	c.refusal.CompareAndSwap(nil, initializeRefusal(code, resp, body))
	return resp, nil
}

// disarm stops the capture: requests after initialize are answered through
// the MCP envelope, not the auth chain's HTTP refusal.
func (c *initializeRefusalCapture) disarm() {
	c.armed.Store(false)
}

// Refusal returns the typed refusal (a *transport.RequestError) kept while
// armed, or nil when none came.
func (c *initializeRefusalCapture) Refusal() error {
	if r := c.refusal.Load(); r != nil {
		return r.err
	}
	return nil
}

// initializeRefusal builds the typed refusal of the initialize request from
// the server's JSON error body (transport.ErrorResponse, the body REST's auth
// chain refuses with too) and its Retry-After header, through the mapping
// every MCP tool refusal takes.
func initializeRefusal(code wire.Code, resp *http.Response, body []byte) *capturedRefusal {
	var errResp transport.ErrorResponse
	msg := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		msg = errResp.Error
	}
	if msg == "" {
		msg = resp.Status
	}
	return &capturedRefusal{err: mapToolError("initialize", &wire.ErrorPayload{
		Code:              code,
		Message:           msg,
		RetryAfterSeconds: retryAfterSeconds(resp.Header.Get("Retry-After")),
		Reason:            errResp.Code,
		Metadata:          errResp.Metadata,
	})}
}

// capturedRefusal holds the typed refusal a capture kept.
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
