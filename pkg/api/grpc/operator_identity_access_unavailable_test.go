package grpc

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/rbac"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// unavailablePolicyReader is an isolation policy reader whose store is down.
type unavailablePolicyReader struct{}

func (unavailablePolicyReader) GetTenant(context.Context, string) (*tenant.TenantInfo, error) {
	return nil, errors.New("tenant store unavailable")
}

func (unavailablePolicyReader) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return "", errors.New("tenant store unavailable")
}

// unavailablePolicySink records the platform log and the tenant trail.
type unavailablePolicySink struct {
	mu      sync.Mutex
	entries []*audit.Entry
}

func (s *unavailablePolicySink) Log(_ context.Context, e *audit.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e)
	return nil
}

func (s *unavailablePolicySink) Close() error { return nil }

func (s *unavailablePolicySink) Emit(ctx context.Context, ev audit.AuditEvent) error {
	return s.Log(ctx, &audit.Entry{TenantID: ev.TenantID, Action: ev.Action, Subject: ev.Actor, Result: ev.Outcome})
}

func (s *unavailablePolicySink) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

// TestOperatorTenantContext_PolicyReadFailureIsUnavailable: an operator's
// cross-tenant identity-record RPC whose isolation policy read fails is
// refused with codes.Unavailable, which a client retries, not
// PermissionDenied.
func TestOperatorTenantContext_PolicyReadFailureIsUnavailable(t *testing.T) {
	sink := &unavailablePolicySink{}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	mw := authmw.New(rbac.New(roles),
		authmw.WithRoleResolver(roles),
		authmw.WithAuditLogger(sink), authmw.WithOperatorAccessTenantTrail(sink),
		authmw.WithIsolationPolicyReader(unavailablePolicyReader{}))
	srv := &qrdbServiceServer{roleSvc: roles, authMW: mw}
	ctx := auth.WithIdentity(tenant.WithTenantID(context.Background(), tenantid.System),
		authtest.AdminIdentity(tenantid.System, "operator"))
	for _, perm := range []permission.Permission{permission.UsersReadTenant, permission.UsersWriteTenant} {
		_, err := srv.operatorTenantContext(ctx, "acme", perm)
		if got := status.Code(err); got != codes.Unavailable {
			t.Fatalf("%s with a failing policy read: code = %v, want Unavailable (err %v)", perm, got, err)
		}
	}
}

// missingTenantPolicyReader answers an unknown tenant as the real reader
// does: tenant.ErrTenantNotFound.
type missingTenantPolicyReader struct{}

func (missingTenantPolicyReader) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	return nil, fmt.Errorf("get tenant %q: %w", id, tenant.ErrTenantNotFound)
}

func (missingTenantPolicyReader) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return tenant.ClusterIsolationPolicyOperatorAccess, nil
}

// TestOperatorTenantContext_MissingTenantIsNotFound: an operator call on a
// tenant that does not exist answers codes.NotFound, as on REST, MCP and
// QUIC, not PermissionDenied or Unavailable.
func TestOperatorTenantContext_MissingTenantIsNotFound(t *testing.T) {
	sink := &unavailablePolicySink{}
	roles := authtest.BuiltInRoles(t, tenantid.System, "acme")
	mw := authmw.New(rbac.New(roles),
		authmw.WithRoleResolver(roles),
		authmw.WithAuditLogger(sink), authmw.WithOperatorAccessTenantTrail(sink),
		authmw.WithIsolationPolicyReader(missingTenantPolicyReader{}))
	srv := &qrdbServiceServer{roleSvc: roles, authMW: mw}
	ctx := auth.WithIdentity(tenant.WithTenantID(context.Background(), tenantid.System),
		authtest.AdminIdentity(tenantid.System, "operator"))
	for _, perm := range []permission.Permission{permission.UsersReadTenant, permission.UsersWriteTenant} {
		_, err := srv.operatorTenantContext(ctx, "ghost", perm)
		if got := status.Code(err); got != codes.NotFound {
			t.Fatalf("%s on a missing tenant: code = %v, want NotFound (err %v)", perm, got, err)
		}
	}
}
