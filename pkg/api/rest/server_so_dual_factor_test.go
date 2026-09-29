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
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSOVerifier is a deterministic soPINVerifier for tests.
type fakeSOVerifier struct {
	err error
}

func (f *fakeSOVerifier) VerifySOPIN(_ context.Context, _ string, _ []byte) error {
	return f.err
}

// TestSODualFactor_Disabled_NoGate verifies the default posture: without
// WithSODualFactor, an SO-permission route (POST /v1/barrier/rekey/start) is
// NOT gated by the SO middleware even when a verifier is wired — the
// request reaches the normal handler chain.
func TestSODualFactor_Disabled_NoGate(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0", WithSOAuthenticator(&fakeSOVerifier{}))

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey/start", nil)
	// Plain HTTP, no SO PIN header: with the gate absent this must NOT
	// produce the SO middleware's 401/403 envelopes.
	srv.server.Handler.ServeHTTP(rr, r)

	if strings.Contains(rr.Body.String(), "SO PIN") || strings.Contains(rr.Body.String(), "SO role") {
		t.Fatalf("SO middleware engaged while disabled: body = %q", rr.Body.String())
	}
}

// TestSODualFactor_EnabledWithoutVerifier_NoGate verifies that the flag
// alone does not install the middleware — a verifier is required, so a
// misconfigured deployment cannot silently lock out SO routes with an
// unverifiable PIN check.
func TestSODualFactor_EnabledWithoutVerifier_NoGate(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0", WithSODualFactor())

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey/start", nil)
	srv.server.Handler.ServeHTTP(rr, r)

	if strings.Contains(rr.Body.String(), "SO PIN") || strings.Contains(rr.Body.String(), "SO role") {
		t.Fatalf("SO middleware engaged without a verifier: body = %q", rr.Body.String())
	}
}

// TestSODualFactor_Enabled_PlainHTTP_Unauthorized verifies the enabled
// posture rejects SO-gated routes over plain HTTP with 401 before the
// PIN header is read.
func TestSODualFactor_Enabled_PlainHTTP_Unauthorized(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithSOAuthenticator(&fakeSOVerifier{}),
		WithSODualFactor(),
	)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey/start", nil) // r.TLS == nil
	srv.server.Handler.ServeHTTP(rr, r)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "SO PIN requires TLS") {
		t.Errorf("body = %q, want TLS-required envelope", rr.Body.String())
	}
}

// TestSODualFactor_Enabled_NoSOIdentity_Forbidden verifies the enabled
// posture rejects an SO-gated route over TLS when the caller has no SO
// identity in context.
func TestSODualFactor_Enabled_NoSOIdentity_Forbidden(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithSOAuthenticator(&fakeSOVerifier{}),
		WithSODualFactor(),
	)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/barrier/rekey/start", nil)
	r.TLS = &tls.ConnectionState{}
	srv.server.Handler.ServeHTTP(rr, r)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "SO role required") {
		t.Errorf("body = %q, want SO-role envelope", rr.Body.String())
	}
}

// TestSODualFactor_Enabled_NonSORoute_Unaffected verifies the enabled
// posture leaves non-SO routes untouched (fail-open only outside the SO
// permission surface).
func TestSODualFactor_Enabled_NonSORoute_Unaffected(t *testing.T) {
	srv := NewServer(nil, nil, "127.0.0.1:0",
		WithSOAuthenticator(&fakeSOVerifier{}),
		WithSODualFactor(),
	)

	rr := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/barrier/status", nil) // not in SOPermissions
	srv.server.Handler.ServeHTTP(rr, r)

	if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
		if strings.Contains(rr.Body.String(), "SO PIN") || strings.Contains(rr.Body.String(), "SO role") {
			t.Fatalf("SO middleware gated a non-SO route: status=%d body=%q", rr.Code, rr.Body.String())
		}
	}
}
