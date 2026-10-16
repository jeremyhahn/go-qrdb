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

package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestWithSealedWindowProbe_SealedReturns503 verifies that when the
// REST server is constructed with WithSealedWindowProbe and the probe
// reports sealed, requests outside the canonical allowlist receive
// 503 with the documented JSON envelope, while allowlisted paths
// flow through to the per-handler nil-service guards.
func TestWithSealedWindowProbe_SealedReturns503(t *testing.T) {
	probe := newFakeSealed(true)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithSealedWindowProbe(probe))

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/kv/foo", nil)
	srv.server.Handler.ServeHTTP(rr, r)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-allowlist path: status = %d, want 503", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "5" {
		t.Errorf("Retry-After header = %q, want %q", got, "5")
	}
	if !strings.Contains(rr.Body.String(), `"code":"SEALED"`) {
		t.Errorf("body = %q, want SEALED envelope", rr.Body.String())
	}
}

// TestWithSealedWindowProbe_AllowlistPasses verifies that allowlisted
// paths bypass the 503 envelope and reach the underlying handler
// chain when the probe reports sealed. The barrier status handler is
// nil-guarded internally, so we expect 503 from the handler itself
// (with the unrelated "barrier service not configured" envelope) —
// the point is that the request was NOT short-circuited by the
// sealed-window middleware.
func TestWithSealedWindowProbe_AllowlistPasses(t *testing.T) {
	probe := newFakeSealed(true)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithSealedWindowProbe(probe))

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/barrier/unseal", nil)
	srv.server.Handler.ServeHTTP(rr, r)

	// The path is allowlisted, so the sealed-window middleware passes
	// it through. The handler then nil-guards on s.barrierSvc and
	// returns its own error envelope. Either way, we must NOT see the
	// sealed-window's "SEALED" envelope.
	if strings.Contains(rr.Body.String(), `"code":"SEALED"`) {
		t.Errorf("allowlisted path was rejected by sealed-window middleware: body = %q", rr.Body.String())
	}
}

// TestWithSealedWindowProbe_UnsealedPassthrough verifies that when the
// probe reports unsealed, every path flows through the middleware
// untouched — non-allowlisted paths reach the dispatcher and get
// handled by their normal routes (404 here because no service is
// wired, but emphatically NOT a 503 SEALED envelope).
func TestWithSealedWindowProbe_UnsealedPassthrough(t *testing.T) {
	probe := newFakeSealed(false)
	srv := NewServer(nil, nil, "127.0.0.1:0", WithSealedWindowProbe(probe))

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/kv/foo", nil)
	srv.server.Handler.ServeHTTP(rr, r)

	if strings.Contains(rr.Body.String(), `"code":"SEALED"`) {
		t.Errorf("unsealed probe rejected request as sealed: body = %q", rr.Body.String())
	}
}

// TestNoSealedWindowProbe_Passthrough verifies that when the option
// is not supplied, the middleware is not installed and requests flow
// through unchanged. The server is constructed without the option;
// the body must never contain the sealed envelope regardless of
// whether the path is on the allowlist.
func TestNoSealedWindowProbe_Passthrough(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0")

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/kv/foo", nil)
	srv.server.Handler.ServeHTTP(rr, r)

	if strings.Contains(rr.Body.String(), `"code":"SEALED"`) {
		t.Errorf("no probe configured but middleware engaged: body = %q", rr.Body.String())
	}
}
