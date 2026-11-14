package middleware

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

type fakeIsolationPolicy struct {
	floor   tenant.ClusterIsolationPolicy
	tenants map[string]*tenant.TenantInfo
}

func (f *fakeIsolationPolicy) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	info, ok := f.tenants[id]
	if !ok {
		return nil, errors.New("tenant not found")
	}
	return info, nil
}

func (f *fakeIsolationPolicy) ClusterIsolationPolicy(context.Context) (tenant.ClusterIsolationPolicy, error) {
	return f.floor, nil
}

func policyFixture(floor tenant.ClusterIsolationPolicy, acmePolicy tenant.TenantIsolationPolicy) *fakeIsolationPolicy {
	return &fakeIsolationPolicy{
		floor: floor,
		tenants: map[string]*tenant.TenantInfo{
			"acme":   {ID: "acme", IsolationType: tenant.IsolationRaft, IsolationPolicy: acmePolicy},
			"globex": {ID: "globex", IsolationType: tenant.IsolationPrefix},
		},
	}
}

// sinkTenantTrail is a tenant trail recording into a mockAuditLogger, so a
// test reads both trails' records in one place; it fails as the sink does.
type sinkTenantTrail struct{ sink *mockAuditLogger }

func (s sinkTenantTrail) Emit(ctx context.Context, ev audit.AuditEvent) error {
	md := map[string]string{}
	for k, v := range ev.Attributes {
		md[k], _ = v.(string)
	}
	return s.sink.Log(ctx, &audit.Entry{
		Timestamp: ev.Timestamp, Subject: ev.Actor, TenantID: ev.TenantID,
		Action: ev.Action, Resource: ev.Resource, Result: ev.Outcome, Metadata: md,
	})
}

func (sinkTenantTrail) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}

func (sinkTenantTrail) Close() error { return nil }

// withOperatorTrails wires sink as the platform trail and, through
// sinkTenantTrail, as the tenant trail.
func withOperatorTrails(sink *mockAuditLogger) Option {
	return func(m *AuthMiddleware) {
		WithAuditLogger(sink)(m)
		WithOperatorAccessTenantTrail(sinkTenantTrail{sink: sink})(m)
	}
}

// failingTenantTrail is a tenant trail that refuses every write, as a
// sealed or unavailable home group does.
type failingTenantTrail struct{ emits int }

func (f *failingTenantTrail) Emit(context.Context, audit.AuditEvent) error {
	f.emits++
	return errors.New("tenant home group sealed")
}

func (*failingTenantTrail) Query(context.Context, audit.Filter) (*audit.Page, error) {
	return nil, errors.New("tenant home group sealed")
}

func (*failingTenantTrail) Close() error { return nil }

// The platform trail writing is not enough: an operator access the target
// tenant's own trail refuses is refused, and nothing of it is written into
// the platform decision log under the tenant's name.
func TestOperatorIdentityAccess_TenantTrailFailureRefuses(t *testing.T) {
	platform := &mockAuditLogger{}
	trail := &failingTenantTrail{}
	m := builtInMiddleware(t, WithAuditLogger(platform), WithOperatorAccessTenantTrail(trail),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)))
	_, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.UsersReadTenant)
	var unaudited *OperatorAccessUnauditedError
	if !errors.As(err, &unaudited) || unaudited.Trail != "tenant" {
		t.Fatalf("access whose tenant trail refuses = %v, want *OperatorAccessUnauditedError for the tenant trail", err)
	}
	if trail.emits != 1 {
		t.Fatalf("tenant trail took %d writes, want 1", trail.emits)
	}
	if len(platform.entries) != 1 || platform.entries[0].TenantID != tenantid.Normalize(tenantid.System) {
		t.Fatalf("platform log = %+v, want the one platform record", platform.entries)
	}
}

// A middleware given no tenant trail refuses operator access rather than
// recording it in the platform log alone.
func TestOperatorIdentityAccess_NoTenantTrailRefuses(t *testing.T) {
	m := builtInMiddleware(t, WithAuditLogger(&mockAuditLogger{}),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)))
	_, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.UsersReadTenant)
	var unaudited *OperatorAccessUnauditedError
	if !errors.As(err, &unaudited) || unaudited.Trail != "tenant" {
		t.Fatalf("access with no tenant trail = %v, want *OperatorAccessUnauditedError for the tenant trail", err)
	}
}

func operatorCtx() context.Context {
	return as(authtest.AdminIdentity(tenantid.System, "operator"))
}

// wantBothTrails asserts one record in the platform trail and one in acme's.
func wantBothTrails(t *testing.T, sink *mockAuditLogger, target, result string) {
	t.Helper()
	if len(sink.entries) != 2 {
		t.Fatalf("wrote %d audit records, want 2 (platform + tenant)", len(sink.entries))
	}
	trails := map[string]bool{}
	for _, e := range sink.entries {
		if e.Action != OperatorIdentityAccessAction || e.Result != result || e.Subject != "operator" {
			t.Fatalf("record %+v, want action %q result %q subject operator", e, OperatorIdentityAccessAction, result)
		}
		if e.Metadata[OperatorAccessTargetMetadataKey] != target {
			t.Fatalf("record names target %q, want %q", e.Metadata[OperatorAccessTargetMetadataKey], target)
		}
		trails[e.TenantID] = true
	}
	if !trails[tenantid.Normalize(tenantid.System)] || !trails[target] {
		t.Fatalf("records went to trails %v, want the platform trail and %q", trails, target)
	}
}

func TestOperatorIdentityAccess_OperatorAccessAllowsAndAuditsBothTrails(t *testing.T) {
	for _, perm := range []permission.Permission{
		permission.UsersReadTenant, permission.UsersWriteTenant,
		permission.RolesReadTenant, permission.RolesWriteTenant,
	} {
		sink := &mockAuditLogger{}
		m := builtInMiddleware(t, withOperatorTrails(sink),
			WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)))
		ctx, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", perm)
		if err != nil {
			t.Fatalf("%s under operator-access = %v, want admitted", perm, err)
		}
		if got := tenant.FromContext(ctx); got != "acme" {
			t.Fatalf("%s: context bound to %q, want acme", perm, got)
		}
		wantBothTrails(t, sink, "acme", audit.ResultSuccess)
	}
}

func TestOperatorIdentityAccess_StrictTenantRefusedAndAudited(t *testing.T) {
	sink := &mockAuditLogger{}
	m := builtInMiddleware(t, withOperatorTrails(sink),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyStrict)))
	_, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.UsersReadTenant)
	var refused *OperatorAccessRefusedError
	if !errors.As(err, &refused) || refused.TenantID != "acme" {
		t.Fatalf("read of a strict tenant = %v, want *OperatorAccessRefusedError for acme", err)
	}
	if ClassifyAuthError(err) != AuthFailureUnauthorized {
		t.Fatalf("refusal classified %v, want unauthorized", ClassifyAuthError(err))
	}
	wantBothTrails(t, sink, "acme", audit.ResultDenied)
}

func TestOperatorIdentityAccess_StrictFloorRefusesInheritingTenant(t *testing.T) {
	sink := &mockAuditLogger{}
	m := builtInMiddleware(t, withOperatorTrails(sink),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyStrict, tenant.TenantIsolationPolicyInherit)))
	_, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.UsersWriteTenant)
	var refused *OperatorAccessRefusedError
	if !errors.As(err, &refused) {
		t.Fatalf("write under a strict floor = %v, want *OperatorAccessRefusedError", err)
	}
}

func TestOperatorIdentityAccess_SharedTenantNeverRefused(t *testing.T) {
	sink := &mockAuditLogger{}
	m := builtInMiddleware(t, withOperatorTrails(sink),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyStrict, tenant.TenantIsolationPolicyStrict)))
	if _, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "globex", permission.UsersReadTenant); err != nil {
		t.Fatalf("read of a shared tenant under a strict floor = %v, want admitted", err)
	}
	wantBothTrails(t, sink, "globex", audit.ResultSuccess)
}

func TestOperatorIdentityAccess_TenantAdminRefused(t *testing.T) {
	sink := &mockAuditLogger{}
	m := builtInMiddleware(t, withOperatorTrails(sink),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)))
	ctx := as(authtest.AdminIdentity("globex", "globex-admin"))
	if _, err := m.AuthorizeOperatorIdentityAccess(ctx, "acme", permission.UsersReadTenant); err == nil {
		t.Fatal("a tenant admin reached another tenant's users, want refused (ScopeCross)")
	}
	if len(sink.entries) != 2 {
		t.Fatalf("wrote %d audit records, want 2", len(sink.entries))
	}
}

func TestOperatorIdentityAccess_OwnTenantIsNotOperatorAccess(t *testing.T) {
	sink := &mockAuditLogger{}
	m := builtInMiddleware(t, withOperatorTrails(sink))
	in := as(authtest.AdminIdentity("acme", "acme-admin"))
	for _, target := range []string{"", "acme"} {
		ctx, err := m.AuthorizeOperatorIdentityAccess(in, target, permission.UsersReadTenant)
		if err != nil || ctx != in {
			t.Fatalf("own tenant %q = (%v, %v), want the context unchanged", target, ctx, err)
		}
	}
	if len(sink.entries) != 0 {
		t.Fatalf("own-tenant access wrote %d records, want 0", len(sink.entries))
	}
}

func TestOperatorIdentityAccess_UnrecordedAccessRefused(t *testing.T) {
	sink := &mockAuditLogger{err: errors.New("tenant trail unavailable")}
	m := builtInMiddleware(t, withOperatorTrails(sink), WithAuditFailurePolicy(audit.FailOpen),
		WithIsolationPolicyReader(policyFixture(tenant.ClusterIsolationPolicyOperatorAccess, tenant.TenantIsolationPolicyInherit)))
	_, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.UsersReadTenant)
	var unaudited *OperatorAccessUnauditedError
	if !errors.As(err, &unaudited) {
		t.Fatalf("unrecordable access under fail-open = %v, want *OperatorAccessUnauditedError", err)
	}
}

func TestOperatorIdentityAccess_NoPolicyReaderRefuses(t *testing.T) {
	sink := &mockAuditLogger{}
	m := builtInMiddleware(t, withOperatorTrails(sink))
	_, err := m.AuthorizeOperatorIdentityAccess(operatorCtx(), "acme", permission.UsersReadTenant)
	var unresolved *OperatorAccessUnresolvedError
	if !errors.As(err, &unresolved) {
		t.Fatalf("access without a policy reader = %v, want *OperatorAccessUnresolvedError", err)
	}
}
