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

// Which tenant may call GET /v1/audit/verify.
//
// pkg/audit.Verify walks the node's WHOLE trail as one hash chain -- there is
// no per-tenant slice of it -- so audit:verify is registered ScopeCross
// (pkg/permission/audit_verify_scope_test.go pins the registration itself).
// This is the REST leg: a real rbac.RBACAuthorizer and a real permission
// registry drive the full middleware chain, proving a tenant-local holder of
// audit:verify is refused and a system-tenant holder is admitted -- the
// property ScopeCross exists to guarantee, not merely the registration.
//
// No build constraint: certScopeAuthenticator, mockRoleService, and
// stubVerifyingAuditLogger are unconstrained.

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func newAuditVerifyScopeFixture(t *testing.T) (*Server, *certScopeAuthenticator) {
	t.Helper()

	roles := &mockRoleService{
		resolvePermissionsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return []permission.Permission{permission.AuditVerify}, nil
		},
	}

	caller := &certScopeAuthenticator{}
	caller.as(certScopeTenantA, "a-auditor")

	al := &stubVerifyingAuditLogger{
		verifyFn: func(context.Context) (*audit.ChainVerification, error) {
			return &audit.ChainVerification{Files: 1, Records: 1, Genesis: true}, nil
		},
	}
	mw := authmw.New(rbac.New(roles), authmw.WithAuthenticator(caller))
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithAuditLogger(al),
		WithAuthMiddleware(mw),
	)

	return srv, caller
}

// TestHandleAuditVerify_TenantLocalIsRefused proves a tenant-local caller
// holding audit:verify cannot trigger a full-trail scan: the permission is
// ScopeCross, so only the system tenant is admitted regardless of grant.
func TestHandleAuditVerify_TenantLocalIsRefused(t *testing.T) {
	srv, caller := newAuditVerifyScopeFixture(t)
	caller.as(certScopeTenantA, "a-auditor")

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/verify", nil)
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a tenant-local caller must not verify the whole trail (body %q)",
			rec.Code, rec.Body.String())
	}
}

// TestHandleAuditVerify_SystemTenantStillSucceeds is the control that keeps
// the fix from being "deny everything": the system tenant is the one
// identity ScopeCross admits.
func TestHandleAuditVerify_SystemTenantStillSucceeds(t *testing.T) {
	srv, caller := newAuditVerifyScopeFixture(t)
	caller.as(tenantid.System, "sys-auditor")

	req := httptest.NewRequest(http.MethodGet, "/v1/audit/verify", nil)
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
}
