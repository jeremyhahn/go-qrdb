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
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
)

const barrierDelegatedPrincipal = "rest-barrier-deleg-test"

func init() {
	permission.RegisterRoleGrant(barrierDelegatedPrincipal, permission.TenantBarrierSeal, permission.TenantBarrierOfficerList)
}

// delegatedBarrierRequest dispatches method path on a tenant server whose gate
// verifies a #873 delegation for acme, as that delegation's identity.
func delegatedBarrierRequest(t *testing.T, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	d := authtest.NewDelegation(t, delegation.Grant{Tenant: "acme", Principal: barrierDelegatedPrincipal,
		Scope: []string{string(permission.TenantBarrierSeal), string(permission.TenantBarrierOfficerList)}})
	roles := authtest.BuiltInRoles(t, "acme", "bob-corp")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles),
		authmw.WithDelegationVerifier(d.Authenticator),
		authmw.WithAuditLogger(audit.NewDecisionLogger(audit.NewRingBuffer(8))))
	srv := newTenantServer(&mockTenantService{officerListFn: func(context.Context, string) ([]*ceremony.OfficerStatus, error) { return nil, nil }})
	WithAuthMiddleware(mw)(srv)

	req := httptest.NewRequest(method, path, nil)
	ctx, _, err := mw.AuthenticateDelegation(req.Context(), []*x509.Certificate{d.Certificate})
	if err != nil {
		t.Fatalf("AuthenticateDelegation: %v", err)
	}
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req.WithContext(ctx))
	return rec
}

// TestTenantBarrierGate_Delegated_SealAndOfficerList proves a #873
// delegation for acme passes the seal and officer_list gates in acme.
func TestTenantBarrierGate_Delegated_SealAndOfficerList(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, "/v1/tenants/acme/barrier/seal", http.StatusNoContent},
		{http.MethodGet, "/v1/tenants/acme/barrier/officers", http.StatusOK},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if rec := delegatedBarrierRequest(t, tc.method, tc.path); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// TestTenantBarrierGate_Delegated_CrossTenantDenied proves the same
// delegation is refused with 403 on another tenant's barrier.
func TestTenantBarrierGate_Delegated_CrossTenantDenied(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/tenants/bob-corp/barrier/seal"},
		{http.MethodGet, "/v1/tenants/bob-corp/barrier/officers"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			if rec := delegatedBarrierRequest(t, tc.method, tc.path); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
			}
		})
	}
}
