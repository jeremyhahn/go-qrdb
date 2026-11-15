// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"errors"
	"net/http"
)

// serverClosedIdleText is the message of net/http's unexported
// errServerClosedIdle.
const serverClosedIdleText = "http: server closed idle connection"

// RetryServerClosedIdle wraps next so a request net/http fails with "http:
// server closed idle connection" is sent once more.
//
// net/http returns that error only when the connection's read loop saw the
// server close the connection while no response was expected on it: the
// server closed it as idle, before it read the request now being written,
// so the request was never applied and resending it cannot apply it twice.
// net/http itself resends only idempotent requests on it (GET, HEAD, or a
// request carrying an Idempotency-Key header); a POST -- every MCP call, and
// REST's guarded verbs -- comes back to the caller as a failure although the
// server never saw it.
//
// Only that error is retried, and only once. An Idempotency-Key header would
// also make net/http resend a request whose connection broke while the
// response was being read, after the server may have applied it, which a
// compare-and-swap must not be.
func RetryServerClosedIdle(next http.RoundTripper) http.RoundTripper {
	return closedIdleRetry{next: next}
}

type closedIdleRetry struct {
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (c closedIdleRetry) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	if err == nil || !IsServerClosedIdle(err) {
		return resp, err
	}
	retry, ok := rewind(req)
	if !ok {
		return resp, err
	}
	return c.next.RoundTrip(retry)
}

// IsServerClosedIdle reports whether err is, or wraps, net/http's "server
// closed idle connection" error.
func IsServerClosedIdle(err error) bool {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if e.Error() == serverClosedIdleText {
			return true
		}
	}
	return false
}

// rewind returns a copy of req with a fresh body, or false when the body
// cannot be produced again.
func rewind(req *http.Request) (*http.Request, bool) {
	retry := req.Clone(req.Context())
	if req.Body == nil || req.Body == http.NoBody {
		return retry, true
	}
	if req.GetBody == nil {
		return nil, false
	}
	body, err := req.GetBody()
	if err != nil {
		return nil, false
	}
	retry.Body = body
	return retry, true
}
