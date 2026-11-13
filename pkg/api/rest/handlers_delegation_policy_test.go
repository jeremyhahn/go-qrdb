// Copyright (c) 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0

package rest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/delegation"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore/kvstoretest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

func doPolicy(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.dispatch(rec, req)
	return rec
}

// policyTenant is the tenant the authenticated caller of these tests is in.
const policyTenant = "acme"

// newPolicyAuthServer serves ce behind a real AuthMiddleware whose caller is
// an account of tenantID holding exactly perms.
func newPolicyAuthServer(ce service.CertEnrollmentServicer, tenantID string, perms ...permission.Permission) *Server {
	roles := &mockRoleService{
		resolvePermissionsFn: func(context.Context, []string) ([]permission.Permission, error) {
			return perms, nil
		},
	}
	caller := &certScopeAuthenticator{}
	caller.as(tenantID, "policy-admin")
	mw := authmw.New(rbac.New(roles), authmw.WithRoleResolver(roles), authmw.WithAuthenticator(caller))
	return NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0",
		WithCertEnrollment(ce), WithAuthMiddleware(mw))
}

// servePolicy sends one request through the server's full handler chain,
// authentication included.
func servePolicy(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, req)
	return rec
}

// recordingPolicyEnroll records the tenant every policy verb receives and
// answers with that tenant, never a fixed one.
type recordingPolicyEnroll struct {
	fakeCertEnroll
	calls   int
	tenants []string
}

func newRecordingPolicyEnroll() *recordingPolicyEnroll {
	r := &recordingPolicyEnroll{}
	r.policyPutFn = func(_ context.Context, req *service.DelegationPolicyRequest) (*delegation.Policy, error) {
		r.calls++
		r.tenants = append(r.tenants, req.TenantID)
		return &delegation.Policy{Tenant: req.TenantID, Principal: req.Principal, Scope: req.Scope, TTL: req.TTL}, nil
	}
	r.policyListFn = func(_ context.Context, tenantID string) ([]delegation.Policy, error) {
		r.calls++
		r.tenants = append(r.tenants, tenantID)
		return []delegation.Policy{{Tenant: tenantID, Principal: "billing", Scope: []string{"kv:get"}}}, nil
	}
	r.policyDeleteFn = func(_ context.Context, tenantID, _ string) error {
		r.calls++
		r.tenants = append(r.tenants, tenantID)
		return nil
	}
	return r
}

// policyVerbs is each delegation-policy verb as a request.
var policyVerbs = []struct {
	name, method, path, body string
	ok                       int
}{
	{"put", http.MethodPost, "/certs/delegate/policies", `{"principal":"billing","scope":["kv:get"],"ttl":"30m"}`, http.StatusCreated},
	{"list", http.MethodGet, "/certs/delegate/policies", "", http.StatusOK},
	{"delete", http.MethodDelete, "/certs/delegate/policies/billing", "", http.StatusNoContent},
}

// TestDelegationPolicy_AuthenticatedCallersTenantReachesService: every verb
// hands the service the authenticated caller's own tenant.
func TestDelegationPolicy_AuthenticatedCallersTenantReachesService(t *testing.T) {
	for _, v := range policyVerbs {
		t.Run(v.name, func(t *testing.T) {
			ce := newRecordingPolicyEnroll()
			rec := servePolicy(newPolicyAuthServer(ce, policyTenant, permission.CertsDelegate), v.method, v.path, v.body)
			assertStatus(t, rec, v.ok)
			if !slices.Equal(ce.tenants, []string{policyTenant}) {
				t.Fatalf("service received tenants %q, want [%s]", ce.tenants, policyTenant)
			}
		})
	}
}

// TestDelegationPolicy_MissingCertsDelegateIsRefused: a caller without
// certs:delegate is refused and the service is never called.
func TestDelegationPolicy_MissingCertsDelegateIsRefused(t *testing.T) {
	for _, v := range policyVerbs {
		t.Run(v.name, func(t *testing.T) {
			ce := newRecordingPolicyEnroll()
			srv := newPolicyAuthServer(ce, policyTenant, permission.CertsIssue, permission.CertsList)
			rec := servePolicy(srv, v.method, v.path, v.body)
			assertStatus(t, rec, http.StatusForbidden)
			if ce.calls != 0 {
				t.Fatalf("service called %d times for a caller without certs:delegate", ce.calls)
			}
		})
	}
}

// newRealPolicyEnrollment is the real enrollment service over a real CA
// that keeps delegation policies.
func newRealPolicyEnrollment(t *testing.T) *service.CertEnrollmentService {
	t.Helper()
	p := cert.NewDefaultProvider(kvstoretest.NewGuardedMemoryStore(), sealtest.UnsealedBarrier(t),
		cert.WithTrustDomain("db.example.com"))
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize CA: %v", err)
	}
	store, ok := any(p).(cert.DelegationPolicyStore)
	if !ok {
		t.Fatalf("%T keeps no delegation policies", p)
	}
	return service.NewCertEnrollmentService(p, nil, nil, service.WithDelegationPolicyStore(store))
}

// TestDelegationPolicy_RealServiceKeepsTheCallersTenant: a policy written
// through the full chain is the caller's tenant's, and lists back as such.
func TestDelegationPolicy_RealServiceKeepsTheCallersTenant(t *testing.T) {
	srv := newPolicyAuthServer(newRealPolicyEnrollment(t), policyTenant, permission.CertsDelegate)
	put := policyVerbs[0]
	rec := servePolicy(srv, put.method, put.path, put.body)
	assertStatus(t, rec, http.StatusCreated)
	var resp delegationPolicyResponse
	decodeJSON(t, rec, &resp)
	if resp.TenantID != policyTenant || resp.Principal != "billing" || resp.TTL != "30m0s" {
		t.Fatalf("put response = %+v", resp)
	}
	rec = servePolicy(srv, http.MethodGet, "/certs/delegate/policies", "")
	assertStatus(t, rec, http.StatusOK)
	var list struct {
		Policies []delegationPolicyResponse `json:"policies"`
	}
	decodeJSON(t, rec, &list)
	if len(list.Policies) != 1 || list.Policies[0].TenantID != policyTenant {
		t.Fatalf("list response = %+v", list)
	}
}

// TestDelegationPolicy_SystemCallerIsRefusedByTheService: the system tenant
// holds certs:delegate yet the real service refuses it every verb.
func TestDelegationPolicy_SystemCallerIsRefusedByTheService(t *testing.T) {
	srv := newPolicyAuthServer(newRealPolicyEnrollment(t), tenantid.System, permission.CertsDelegate)
	for _, v := range policyVerbs {
		t.Run(v.name, func(t *testing.T) {
			assertStatus(t, servePolicy(srv, v.method, v.path, v.body), http.StatusForbidden)
		})
	}
}

// TestDelegationPolicy_DelegatedCallerIsRefusedByTheService: a caller acting
// under a delegation reaches the handler with its tenant, and the real service
// refuses it every verb.
func TestDelegationPolicy_DelegatedCallerIsRefusedByTheService(t *testing.T) {
	id := authtest.NewDelegation(t, delegation.Grant{Tenant: policyTenant, Principal: "billing", Scope: []string{"certs:delegate"}}).Identity(t)
	ctx := tenant.WithTenantID(auth.WithIdentity(context.Background(), id), policyTenant)
	srv := newEnrollAuthServer(newRealPolicyEnrollment(t))
	for _, v := range []struct {
		name   string
		handle http.HandlerFunc
		req    *http.Request
	}{
		{"put", srv.handlePutDelegationPolicy, httptest.NewRequest(http.MethodPost, "/certs/delegate/policies", strings.NewReader(policyVerbs[0].body))},
		{"list", srv.handleListDelegationPolicies, httptest.NewRequest(http.MethodGet, "/certs/delegate/policies", nil)},
		{"delete", srv.handleDeleteDelegationPolicy, httptest.NewRequest(http.MethodDelete, "/certs/delegate/policies/billing", nil)},
	} {
		t.Run(v.name, func(t *testing.T) {
			v.req.SetPathValue("principal", "billing")
			rec := httptest.NewRecorder()
			v.handle(rec, v.req.WithContext(ctx))
			assertStatus(t, rec, http.StatusForbidden)
		})
	}
}

func TestHandlePutDelegationPolicy_RefusesBadRequests(t *testing.T) {
	srv := newEnrollAuthServer(&fakeCertEnroll{})
	for name, body := range map[string]string{
		"invalid json": "{bad",
		"invalid ttl":  `{"principal":"billing","scope":["kv:get"],"ttl":"soon"}`,
	} {
		t.Run(name, func(t *testing.T) {
			assertStatus(t, doPolicy(srv, http.MethodPost, "/certs/delegate/policies", body), http.StatusBadRequest)
		})
	}
}

func TestHandlePutDelegationPolicy_MapsServiceErrors(t *testing.T) {
	ce := &fakeCertEnroll{
		policyPutFn: func(context.Context, *service.DelegationPolicyRequest) (*delegation.Policy, error) {
			return nil, &service.DelegationDeniedError{Reason: "a delegated caller cannot delegate"}
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodPost, "/certs/delegate/policies", `{"principal":"billing","scope":["kv:get"]}`)
	assertStatus(t, rec, http.StatusForbidden)
}

func TestHandlePutDelegationPolicy_NilServiceDirect(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(nil, nil, nil).handlePutDelegationPolicy(rec,
		httptest.NewRequest(http.MethodPost, "/certs/delegate/policies", strings.NewReader("{}")))
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleListDelegationPolicies_Success(t *testing.T) {
	ce := &fakeCertEnroll{
		policyListFn: func(context.Context, string) ([]delegation.Policy, error) {
			return []delegation.Policy{{Tenant: "acme", Principal: "billing", Scope: []string{"kv:get"}}}, nil
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodGet, "/certs/delegate/policies", "")

	assertStatus(t, rec, http.StatusOK)
	var resp struct {
		Policies []delegationPolicyResponse `json:"policies"`
	}
	decodeJSON(t, rec, &resp)
	if len(resp.Policies) != 1 || resp.Policies[0].Principal != "billing" ||
		resp.Policies[0].TTL != delegation.DefaultLifetime.String() {
		t.Fatalf("response = %+v", resp)
	}
}

func TestHandleListDelegationPolicies_MapsServiceErrors(t *testing.T) {
	rec := doPolicy(newEnrollAuthServer(&fakeCertEnroll{}), http.MethodGet, "/certs/delegate/policies", "")
	if rec.Code < 400 {
		t.Fatalf("status = %d, want an error", rec.Code)
	}
}

func TestHandleListDelegationPolicies_NilServiceDirect(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(nil, nil, nil).handleListDelegationPolicies(rec,
		httptest.NewRequest(http.MethodGet, "/certs/delegate/policies", nil))
	assertStatus(t, rec, http.StatusNotImplemented)
}

func TestHandleDeleteDelegationPolicy_Success(t *testing.T) {
	var gotPrincipal string
	ce := &fakeCertEnroll{
		policyDeleteFn: func(_ context.Context, _, principal string) error {
			gotPrincipal = principal
			return nil
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodDelete, "/certs/delegate/policies/billing", "")

	assertStatus(t, rec, http.StatusNoContent)
	if gotPrincipal != "billing" {
		t.Fatalf("principal = %q, want billing", gotPrincipal)
	}
}

func TestHandleDeleteDelegationPolicy_MapsServiceErrors(t *testing.T) {
	ce := &fakeCertEnroll{
		policyDeleteFn: func(context.Context, string, string) error {
			return &service.DelegationRequestError{Field: "principal", Reason: "no policy"}
		},
	}
	rec := doPolicy(newEnrollAuthServer(ce), http.MethodDelete, "/certs/delegate/policies/billing", "")
	assertStatus(t, rec, http.StatusBadRequest)
}

func TestHandleDeleteDelegationPolicy_NilServiceDirect(t *testing.T) {
	rec := httptest.NewRecorder()
	newAuthServer(nil, nil, nil).handleDeleteDelegationPolicy(rec,
		httptest.NewRequest(http.MethodDelete, "/certs/delegate/policies/billing", nil))
	assertStatus(t, rec, http.StatusNotImplemented)
}
