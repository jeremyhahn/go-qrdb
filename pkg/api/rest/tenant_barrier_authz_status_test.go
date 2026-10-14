// Copyright (c) 2026 Jeremy Hahn
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

// What status the tenant-barrier authorization gate answers (go-qrdb#335).
//
// No build constraint: newTenantServer and mockTenantService
// (handlers_tenants_test.go) are unconstrained, so nothing here straddles a
// tag, and the mapping these pin ships in every build.

package rest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/holderprofile"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// grantingAuthorizer permits everything, so these tests isolate the AUDIT
// failure from any authorization decision. Without it a denial and an
// audit-write failure would be indistinguishable in the response.
type grantingAuthorizer struct{}

func (grantingAuthorizer) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	return nil
}

// brokenAuditSink returns a decision logger over a REAL ChainFileLogger whose
// file has been closed, so the append fails at os.File.Write and the sink
// returns a real *audit.WriteError -- the fault a full audit disk produces.
func brokenAuditSink(t *testing.T) audit.Logger {
	t.Helper()
	chain, err := audit.NewFileLogger(filepath.Join(t.TempDir(), "audit-events.ndjson"))
	if err != nil {
		t.Fatalf("NewFileLogger: %v", err)
	}
	if err := chain.Close(); err != nil {
		t.Fatalf("close (the injected fault): %v", err)
	}
	return audit.NewDecisionLogger(chain)
}

// barrierRequest drives one tenant-barrier route through the real dispatcher
// with an authenticated identity bound the way the auth middleware would.
func barrierRequest(t *testing.T, srv *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	ctx := auth.WithIdentity(req.Context(), &auth.Identity{Subject: "acme-admin", TenantID: "acme"})
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req.WithContext(ctx))
	return rec
}

// TestTenantBarrier_AuditFailure_Is503NotForbidden is the regression test for
// the mis-mapping that made this surface dangerous.
//
// Eleven handlers here hard-coded http.StatusForbidden for whatever the gate
// returned. Once the middleware began refusing decisions it could not record,
// an operator recovering a node with a full audit disk was told by the UNSEAL
// endpoint that they were not permitted -- and 403 is terminal: no client
// retries it, and it sends an entitled caller off to fix permissions that were
// never the problem.
//
// Init, unseal and status take three different routes through the gate
// (trust-on-first-init, sealed-window, sealed-window), and all three reach the
// fallback's own auditDecision, which fails for the same reason the first one
// did. That is why *AuditError escapes this surface reliably rather than
// rarely.
func TestTenantBarrier_AuditFailure_Is503NotForbidden(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})
	WithAuthMiddleware(authmw.New(grantingAuthorizer{},
		authmw.WithAuditLogger(brokenAuditSink(t))))(srv)

	for _, tc := range []struct {
		name, method, path string
	}{
		{"init-begin", http.MethodPost, "/v1/tenants/acme/barrier/init/begin"},
		{"init-complete", http.MethodPost, "/v1/tenants/acme/barrier/init/complete"},
		{"unseal", http.MethodPost, "/v1/tenants/acme/barrier/unseal"},
		{"quorum-submit", http.MethodPost, "/v1/tenants/acme/barrier/quorum/submit"},
		{"rekey-start", http.MethodPost, "/v1/tenants/acme/barrier/rekey/start"},
		{"officer-revoke", http.MethodPost, "/v1/tenants/acme/barrier/officers/revoke"},
		{"seal", http.MethodPost, "/v1/tenants/acme/barrier/seal"},
		{"status", http.MethodGet, "/v1/tenants/acme/barrier/status"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := barrierRequest(t, srv, tc.method, tc.path)

			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503; 403 is terminal and no client retries it", rec.Code)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !strings.Contains(body.Error, "audit log failed") {
				t.Errorf("the response must name the cause, got %q", body.Error)
			}
		})
	}
}

// TestTenantBarrier_HealthyAuditIsNotRefused is the control: the same wiring
// with a working sink is not answered 503. Without it, a gate that refused
// everything would pass the test above.
func TestTenantBarrier_HealthyAuditIsNotRefused(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})
	WithAuthMiddleware(authmw.New(grantingAuthorizer{},
		authmw.WithAuditLogger(audit.NewDecisionLogger(audit.NewRingBuffer(8)))))(srv)

	rec := barrierRequest(t, srv, http.MethodGet, "/v1/tenants/acme/barrier/status")
	if rec.Code == http.StatusServiceUnavailable || rec.Code == http.StatusForbidden {
		t.Fatalf("a recordable decision must not be refused by the gate, got %d", rec.Code)
	}
}

// TestTenantBarrier_OrdinaryDenialIsStill403 is the other control. Routing the
// gate through the classifier must not turn a real denial into something a
// client will retry forever.
func TestTenantBarrier_OrdinaryDenialIsStill403(t *testing.T) {
	srv := newTenantServer(&mockTenantService{})
	WithAuthMiddleware(authmw.New(denyingAuthorizer{}))(srv)

	rec := barrierRequest(t, srv, http.MethodPost, "/v1/tenants/acme/barrier/seal")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 for an ordinary denial", rec.Code)
	}
}

// denyingAuthorizer refuses with the canonical RBAC sentinel.
type denyingAuthorizer struct{}

func (denyingAuthorizer) Authorize(_ context.Context, _ *auth.Identity, _, _ string) error {
	return auth.ErrUnauthorized
}

// holderBarrierRequest drives one tenant-barrier route through the real
// dispatcher with a holder (custodian/SO) identity bound the way the mTLS
// authenticator binds one -- auth.HolderProfileMetadataKey set to profile,
// never enriched from the user store.
func holderBarrierRequest(t *testing.T, srv *Server, method, path, profile string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	id := &auth.Identity{
		Subject:  "custodian",
		TenantID: "acme",
		Metadata: map[string]string{auth.HolderProfileMetadataKey: profile},
	}
	ctx := auth.WithIdentity(req.Context(), id)
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req.WithContext(ctx))
	return rec
}

// TestTenantBarrierInit_HolderIdentityRefusedEvenWhenUninitialized is the
// REST half of go-qrdb#560 review item 4: a custodian identity for tenant
// acme, RBAC denying, the barrier never initialized -- exactly the state
// that admits an ORDINARY tenant identity through trust-on-first-init --
// must still be refused. The reviewer's probe got nil (200 OK); this test
// requires 403.
func TestTenantBarrierInit_HolderIdentityRefusedEvenWhenUninitialized(t *testing.T) {
	svc := &mockTenantService{
		tenantBarrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{TenantID: tenantID, Initialized: false}, nil
		},
		tenantBarrierInitFn: func(_ context.Context, _ string, _ []byte, _, _ int) error {
			t.Fatal("the tenant service must never be reached: the holder identity should be refused before the business logic runs")
			return nil
		},
	}
	srv := newTenantServer(svc)
	WithAuthMiddleware(authmw.New(denyingAuthorizer{}))(srv)

	for _, profile := range []string{holderprofile.Custodian, holderprofile.SecurityOfficer} {
		t.Run(profile, func(t *testing.T) {
			rec := holderBarrierRequest(t, srv, http.MethodPost, "/v1/tenants/acme/barrier/init/complete", profile)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: a share-holder certificate must never initialize a tenant barrier", rec.Code)
			}
		})
	}
}

// TestWriteAuthzErrorJSON_FallbackStaysForbidden pins the fallback choice.
// These gates run AFTER authentication, so an error the middleware cannot
// classify means "the gate refused you" and must stay 403 -- the REST auth
// middleware's own 401 default would be a lie here, and a regression on what
// these sites answered before the helper existed.
func TestWriteAuthzErrorJSON_FallbackStaysForbidden(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAuthzErrorJSON(rec, errors.New("something the middleware does not classify"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
