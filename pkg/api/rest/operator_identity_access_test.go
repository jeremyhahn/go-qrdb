package rest

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// operatorPolicy is the isolation policy the operator gate reads: acme is an
// isolated tenant whose own policy the test sets.
type operatorPolicy struct{ acme tenant.TenantIsolationPolicy }

func (p *operatorPolicy) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	if id != "acme" {
		return nil, errors.New("tenant not found")
	}
	return &tenant.TenantInfo{ID: "acme", IsolationType: tenant.IsolationRaft, IsolationPolicy: p.acme}, nil
}

func (p *operatorPolicy) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

type operatorAuditSink struct {
	mu      sync.Mutex
	entries []*audit.Entry
}

func (s *operatorAuditSink) Log(_ context.Context, e *audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *operatorAuditSink) Close() error { return nil }

// operatorTrails is the set of trails holding an operator identity access
// record.
func (s *operatorAuditSink) operatorTrails() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]bool{}
	for _, e := range s.entries {
		if e.Action == authmw.OperatorIdentityAccessAction {
			out[e.TenantID] = true
		}
	}
	return out
}

// newOperatorServer is a REST server whose caller is the platform operator
// (a system-tenant admin), with the operator gate wired to policy.
func newOperatorServer(t *testing.T, policy tenant.TenantIsolationPolicy, opts ...ServerOption) (*Server, *operatorAuditSink) {
	t.Helper()
	sink := &operatorAuditSink{}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	mw := authmw.New(rbac.New(roles),
		authmw.WithRoleResolver(roles),
		authmw.WithAuthenticator(authtest.NewFixedAuthenticator(authtest.AdminIdentity(tenantid.System, "operator"))),
		authmw.WithAuditLogger(sink),
		authmw.WithIsolationPolicyReader(&operatorPolicy{acme: policy}))
	srv := NewServer(newDefaultKVSvc(), newDefaultAdminSvc(), "127.0.0.1:0", append([]ServerOption{WithAuthMiddleware(mw)}, opts...)...)
	return srv, sink
}

func serveOperator(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.server.Handler.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

// boundUserService records the tenant every call runs under.
func boundUserService(seen *[]string) *mockUserService {
	rec := func(ctx context.Context) { *seen = append(*seen, tenant.FromContext(ctx)) }
	u := &user.User{ID: 7, TenantID: "acme", Username: "u"}
	return &mockUserService{
		createUserFn: func(ctx context.Context, _ *user.CreateUserRequest) (*user.User, error) { rec(ctx); return u, nil },
		getUserFn:    func(ctx context.Context, _ uint64) (*user.User, error) { rec(ctx); return u, nil },
		updateUserFn: func(ctx context.Context, _ *user.UpdateUserRequest) (*user.User, error) { rec(ctx); return u, nil },
		deleteUserFn: func(ctx context.Context, _ uint64) error { rec(ctx); return nil },
		disableUserFn: func(ctx context.Context, _ uint64) error {
			rec(ctx)
			return nil
		},
		getUserByUsernameFn: func(ctx context.Context, _, _ string) (*user.User, error) { rec(ctx); return u, nil },
	}
}

// userOperations are the user routes beyond list that take the tenant
// qualifier: reads, record writes, lifecycle and certificate bindings.
var userOperations = []struct{ name, method, path, body string }{
	{"get", http.MethodGet, "/users/7?tenantID=acme", ""},
	{"create", http.MethodPost, "/users", `{"tenant_id":"acme","username":"u","without_email_login_key":true}`},
	{"update", http.MethodPut, "/users/7?tenantID=acme", `{"name":"n"}`},
	{"delete", http.MethodDelete, "/users/7?tenantID=acme", ""},
	{"disable", http.MethodPost, "/users/7/disable?tenantID=acme", ""},
	{"get by username", http.MethodGet, "/users/by-username/u?tenantID=acme", ""},
}

func TestOperatorIdentityAccess_UserOperationsUnderOperatorAccess(t *testing.T) {
	for _, op := range userOperations {
		var seen []string
		srv, sink := newOperatorServer(t, tenant.TenantIsolationPolicyInherit, WithUserService(boundUserService(&seen)))
		rec := serveOperator(srv, op.method, op.path, op.body)
		if rec.Code >= 300 {
			t.Fatalf("%s: operator under operator-access = %d %s, want served", op.name, rec.Code, rec.Body.String())
		}
		if len(seen) == 0 || seen[0] != "acme" {
			t.Fatalf("%s: user service ran under tenants %v, want acme", op.name, seen)
		}
		if trails := sink.operatorTrails(); !trails["acme"] || !trails[tenantid.Normalize(tenantid.System)] {
			t.Fatalf("%s: operator access recorded in trails %v, want the platform trail and acme's", op.name, trails)
		}
	}
}

func TestOperatorIdentityAccess_UserOperationsRefusedUnderStrict(t *testing.T) {
	for _, op := range userOperations {
		var seen []string
		srv, sink := newOperatorServer(t, tenant.TenantIsolationPolicyStrict, WithUserService(boundUserService(&seen)))
		rec := serveOperator(srv, op.method, op.path, op.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: operator on a strict tenant = %d %s, want 403", op.name, rec.Code, rec.Body.String())
		}
		if len(seen) != 0 {
			t.Fatalf("%s: user service ran (%v) after the refusal", op.name, seen)
		}
		if !sink.operatorTrails()["acme"] {
			t.Fatalf("%s: the refusal is not in acme's trail", op.name)
		}
	}
}

func TestOperatorIdentityAccess_CertThumbprintLookupGated(t *testing.T) {
	srv, sink := newOperatorServer(t, tenant.TenantIsolationPolicyStrict)
	rec := serveOperator(srv, http.MethodGet, "/users/by-cert-thumbprint/abc?tenantID=acme", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("thumbprint lookup on a strict tenant = %d %s, want 403", rec.Code, rec.Body.String())
	}
	// The built-in system Admin does not hold the route's
	// users:get_by_cert_thumbprint, so the route refuses before the gate and
	// nothing reaches either trail.
	if trails := sink.operatorTrails(); len(trails) != 0 {
		t.Fatalf("operator access recorded in %v before the route admitted it", trails)
	}
}

// boundRoleService records the tenant every call runs under.
func boundRoleService(seen *[]string) *mockRoleService {
	rec := func(ctx context.Context) { *seen = append(*seen, tenant.FromContext(ctx)) }
	rl := &role.Role{ID: "r1", TenantID: "acme", Name: "r"}
	return &mockRoleService{
		createRoleFn:    func(ctx context.Context, _ *role.CreateRoleRequest) (*role.Role, error) { rec(ctx); return rl, nil },
		getRoleFn:       func(ctx context.Context, _ string) (*role.Role, error) { rec(ctx); return rl, nil },
		getRoleByNameFn: func(ctx context.Context, _, _ string) (*role.Role, error) { rec(ctx); return rl, nil },
		updateRoleFn:    func(ctx context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) { rec(ctx); return rl, nil },
		deleteRoleFn:    func(ctx context.Context, _ string) error { rec(ctx); return nil },
	}
}

// roleOperations are the role routes beyond list that take the tenant
// qualifier.
var roleOperations = []struct{ name, method, path, body string }{
	{"get", http.MethodGet, "/roles/r1?tenantID=acme", ""},
	{"get by name", http.MethodGet, "/roles/by-name?name=r&tenant_id=acme", ""},
	{"create", http.MethodPost, "/roles", `{"tenant_id":"acme","name":"r","permissions":["kv:read"]}`},
	{"update", http.MethodPut, "/roles/r1?tenantID=acme", `{"name":"r2"}`},
	// Delete takes the qualifier too; the built-in system Admin does not hold
	// the route's roles:delete, so the route refuses it before the gate.
}

func TestOperatorIdentityAccess_RoleOperationsUnderOperatorAccess(t *testing.T) {
	for _, op := range roleOperations {
		var seen []string
		srv, sink := newOperatorServer(t, tenant.TenantIsolationPolicyInherit, WithRoleService(boundRoleService(&seen)))
		rec := serveOperator(srv, op.method, op.path, op.body)
		if rec.Code >= 300 {
			t.Fatalf("%s: operator under operator-access = %d %s, want served", op.name, rec.Code, rec.Body.String())
		}
		if len(seen) == 0 || seen[0] != "acme" {
			t.Fatalf("%s: role service ran under tenants %v, want acme", op.name, seen)
		}
		if trails := sink.operatorTrails(); !trails["acme"] || !trails[tenantid.Normalize(tenantid.System)] {
			t.Fatalf("%s: operator access recorded in trails %v, want the platform trail and acme's", op.name, trails)
		}
	}
}

func TestOperatorIdentityAccess_RoleOperationsRefusedUnderStrict(t *testing.T) {
	ops := append(roleOperations[:len(roleOperations):len(roleOperations)],
		struct{ name, method, path, body string }{"assign", http.MethodPost, "/roles/assign", `{"user_id":"7","role_id":"r1","tenant_id":"acme"}`},
		struct{ name, method, path, body string }{"unassign", http.MethodPost, "/roles/unassign", `{"user_id":"7","role_id":"r1","tenant_id":"acme"}`},
	)
	for _, op := range ops {
		var seen, userSeen []string
		srv, sink := newOperatorServer(t, tenant.TenantIsolationPolicyStrict,
			WithRoleService(boundRoleService(&seen)), WithUserService(boundUserService(&userSeen)))
		rec := serveOperator(srv, op.method, op.path, op.body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: operator on a strict tenant = %d %s, want 403", op.name, rec.Code, rec.Body.String())
		}
		if len(seen)+len(userSeen) != 0 {
			t.Fatalf("%s: identity services ran (%v %v) after the refusal", op.name, seen, userSeen)
		}
		if !sink.operatorTrails()["acme"] {
			t.Fatalf("%s: the refusal is not in acme's trail", op.name)
		}
	}
}
