package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// wantNotActive fails the test unless err is *TenantNotActiveError for
// acme in status s.
func wantNotActive(t *testing.T, what string, err error, s tenant.Status) {
	t.Helper()
	var na *TenantNotActiveError
	if !errors.As(err, &na) || na.TenantID != "acme" || na.Status != string(s) {
		t.Fatalf("%s = %T(%v), want *TenantNotActiveError{acme, %s}", what, err, err, s)
	}
}

// An identity authenticated while its tenant was active -- an open SQL
// session, an embedded caller -- is refused on its next operation once the
// tenant is suspended: the gate is at every authorization, not only at
// authentication. Reactivation restores it.
func TestTenantStatus_IdentityAuthenticatedBeforeSuspensionRefusedPerOperation(t *testing.T) {
	tenants := statusTenants(t)
	m := newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(tokenAuthenticator{
		"Bearer acme-tok": {Subject: "bob", TenantID: "acme"},
	}), WithTenantStatusReader(tenants))
	ctx, id, err := m.Authenticate(auth.WithHTTPAuthorization(context.Background(), "Bearer acme-tok"))
	if err != nil {
		t.Fatalf("Authenticate while active: %v", err)
	}
	if err := m.AuthorizeTenant(ctx, "acme", permission.KVGet); err != nil {
		t.Fatalf("AuthorizeTenant while active: %v", err)
	}

	setStatus(t, tenants, "acme", tenant.StatusSuspended)

	wantNotActive(t, "AuthorizeTenant", m.AuthorizeTenant(ctx, "acme", permission.KVGet), tenant.StatusSuspended)
	wantNotActive(t, "AuthorizeTenantTarget", m.AuthorizeTenantTarget(ctx, "acme", permission.KVGet), tenant.StatusSuspended)
	wantNotActive(t, "AdmitIdentity", m.AdmitIdentity(context.Background(), id), tenant.StatusSuspended)

	setStatus(t, tenants, "acme", tenant.StatusActive)
	if err := m.AuthorizeTenant(ctx, "acme", permission.KVGet); err != nil {
		t.Fatalf("AuthorizeTenant after reactivation: %v", err)
	}
	if err := m.AdmitIdentity(context.Background(), id); err != nil {
		t.Fatalf("AdmitIdentity after reactivation: %v", err)
	}
}

// bootstrapStatusMiddleware is a middleware whose RBAC refuses everything, so
// every grant below comes from a bootstrap window, over a tenant status
// reader the test moves between states. It runs under the "enrolled" posture,
// which skips the first-admin-onboarding gate (first_init_gate_test.go) when
// the tenant has no pending-admin record.
func bootstrapStatusMiddleware(t *testing.T) (*AuthMiddleware, tenant.TenantServicer) {
	t.Helper()
	tenants := statusTenants(t)
	m := newTestMiddleware(&mockAuthorizer{err: errors.New("rbac: permission denied")},
		WithAuthenticator(tokenAuthenticator{"Bearer acme-tok": {Subject: "admin", TenantID: "acme"}}),
		WithTenantStatusReader(tenants), WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	return m, tenants
}

// TestAuthorizeTenantBarrierInit_TenantStatusRefusalSurvivesTheWindow: a
// context authenticated while acme was active is granted trust on first init;
// once acme is suspended or disabled the same context is refused with the
// tenant-status error, not granted by the window RBAC's refusal opens.
func TestAuthorizeTenantBarrierInit_TenantStatusRefusalSurvivesTheWindow(t *testing.T) {
	for _, s := range []tenant.Status{tenant.StatusSuspended, tenant.StatusDisabled} {
		t.Run(string(s), func(t *testing.T) {
			m, tenants := bootstrapStatusMiddleware(t)
			ctx, _, err := m.Authenticate(auth.WithHTTPAuthorization(context.Background(), "Bearer acme-tok"))
			if err != nil {
				t.Fatalf("Authenticate while active: %v", err)
			}
			if err := m.AuthorizeTenantBarrierInit(ctx, "acme", uninitialized()); err != nil {
				t.Fatalf("AuthorizeTenantBarrierInit while active = %v, want the window's grant", err)
			}

			setStatus(t, tenants, "acme", s)
			wantNotActive(t, "AuthorizeTenantBarrierInit", m.AuthorizeTenantBarrierInit(ctx, "acme", uninitialized()), s)

			setStatus(t, tenants, "acme", tenant.StatusActive)
			if err := m.AuthorizeTenantBarrierInit(ctx, "acme", uninitialized()); err != nil {
				t.Fatalf("AuthorizeTenantBarrierInit after reactivation = %v, want the window's grant", err)
			}
		})
	}
}

// TestAuthorizeTenantBarrierSealedOp_TenantStatusRefusalSurvivesTheWindow: the
// sealed window grants acme's sealed identity unseal while acme is active and
// refuses it with the tenant-status error once acme is suspended or disabled.
func TestAuthorizeTenantBarrierSealedOp_TenantStatusRefusalSurvivesTheWindow(t *testing.T) {
	for _, s := range []tenant.Status{tenant.StatusSuspended, tenant.StatusDisabled} {
		t.Run(string(s), func(t *testing.T) {
			m, tenants := bootstrapStatusMiddleware(t)
			ctx := sealedCtx("acme")
			if err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal); err != nil {
				t.Fatalf("AuthorizeTenantBarrierSealedOp while active = %v, want the window's grant", err)
			}

			setStatus(t, tenants, "acme", s)
			wantNotActive(t, "AuthorizeTenantBarrierSealedOp", m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal), s)

			setStatus(t, tenants, "acme", tenant.StatusActive)
			if err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal); err != nil {
				t.Fatalf("AuthorizeTenantBarrierSealedOp after reactivation = %v, want the window's grant", err)
			}
		})
	}
}

// statusReadFailures are the tenant record reads that establish nothing about
// the tenant's status: the node barrier sealed under the read, and the store
// not answering. Neither is the tenant-IAM-sealed signal, though both arrive
// as an UNAVAILABLE error just as it does.
var statusReadFailures = map[string]struct {
	err    error
	sealed bool
}{
	"barrier sealed":    {&tenant.StoreError{Op: "get", Err: seal.ErrBarrierSealed}, true},
	"store unavailable": {errStoreDown, false},
}

// wantStatusReadFailure asserts err is the typed status-read failure carrying
// the read error as classified, and so still maps to UNAVAILABLE.
func wantStatusReadFailure(t *testing.T, what string, err error, sealed bool) {
	t.Helper()
	var unreadable *TenantStatusUnavailableError
	if !errors.As(err, &unreadable) || unreadable.TenantID != "acme" {
		t.Fatalf("%s = %T(%v), want *TenantStatusUnavailableError{acme}", what, err, err)
	}
	var bs *BarrierSealedError
	var su *StoreUnavailableError
	if sealed && !errors.As(err, &bs) || !sealed && !errors.As(err, &su) {
		t.Fatalf("%s = %v, want the read failure classified (sealed=%v)", what, err, sealed)
	}
	if got := ClassifyAuthError(err); got != AuthFailureUnavailable {
		t.Fatalf("%s classified as %v, want AuthFailureUnavailable", what, got)
	}
}

// statusReadFixture is a middleware whose tenant record read can be failed
// after the bootstrap windows have been shown to open, with the decisions it
// records counted so a fallback grant -- which records one of its own -- is
// visible.
func statusReadFixture(t *testing.T) (*AuthMiddleware, *countingTenants, *mockAuditLogger) {
	t.Helper()
	reader := &countingTenants{rec: &tenant.TenantInfo{ID: "acme", Status: tenant.StatusActive}}
	audit := &mockAuditLogger{}
	m := newTestMiddleware(&mockAuthorizer{err: errors.New("rbac: permission denied")},
		WithTenantStatusReader(reader), WithAuditLogger(audit), WithMTLSBindingPolicy(auth.MTLSBindingEnrolled))
	return m, reader, audit
}

// A tenant status that could not be READ never opens the trust-on-first-init
// window: the tenant was not found active, so the window's grant would be one
// the status gate never answered. The refusal is the read failure itself.
func TestAuthorizeTenantBarrierInit_StatusReadFailureNeverOpensTheWindow(t *testing.T) {
	for name, tc := range statusReadFailures {
		t.Run(name, func(t *testing.T) {
			m, reader, audit := statusReadFixture(t)
			ctx := auth.WithIdentity(context.Background(), &auth.Identity{Subject: "admin", TenantID: "acme"})
			if err := m.AuthorizeTenantBarrierInit(ctx, "acme", uninitialized()); err != nil {
				t.Fatalf("AuthorizeTenantBarrierInit while readable = %v, want the window's grant", err)
			}
			granted := decisionRecords(audit.entries)

			reader.err = tc.err
			status := &countingStatusReader{}
			before := len(audit.entries)
			wantStatusReadFailure(t, "AuthorizeTenantBarrierInit", m.AuthorizeTenantBarrierInit(ctx, "acme", status), tc.sealed)
			if status.calls != 0 {
				t.Fatalf("the bootstrap window was consulted %d time(s) for a status read failure", status.calls)
			}
			if n := decisionRecords(audit.entries[before:]); n >= granted {
				t.Fatalf("decision records = %d, a grant records %d: the window's fallback grant ran", n, granted)
			}
		})
	}
}

// The sealed window likewise: an identity marked IAM-sealed whose tenant
// record cannot be read is refused with the read failure, while the
// IAM-sealed signal itself still takes the window.
func TestAuthorizeTenantBarrierSealedOp_StatusReadFailureNeverOpensTheWindow(t *testing.T) {
	for name, tc := range statusReadFailures {
		t.Run(name, func(t *testing.T) {
			m, reader, audit := statusReadFixture(t)
			ctx := sealedCtx("acme")
			if err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal); err != nil {
				t.Fatalf("AuthorizeTenantBarrierSealedOp while readable = %v, want the window's grant", err)
			}
			granted := decisionRecords(audit.entries)

			reader.err = tc.err
			before := len(audit.entries)
			wantStatusReadFailure(t, "AuthorizeTenantBarrierSealedOp",
				m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal), tc.sealed)
			if n := decisionRecords(audit.entries[before:]); n >= granted {
				t.Fatalf("decision records = %d, a grant records %d: the window's fallback grant ran", n, granted)
			}

			reader.err = nil
			if err := m.AuthorizeTenantBarrierSealedOp(ctx, "acme", permission.TenantBarrierUnseal); err != nil {
				t.Fatalf("AuthorizeTenantBarrierSealedOp once readable again = %v, want the window's grant", err)
			}
		})
	}
}

// A system administrator's session opened before acme was suspended keeps
// administering it: reading its status, reactivating it, deleting it.
func TestTenantStatus_SystemAdminSessionAdministersSuspendedTenant(t *testing.T) {
	tenants := statusTenants(t)
	m := builtInMiddleware(t,
		WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: authtest.AdminIdentity(tenantid.System, "root")}),
		WithTenantStatusReader(tenants))
	ctx, id, err := m.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	setStatus(t, tenants, "acme", tenant.StatusSuspended)

	if err := m.AdmitIdentity(context.Background(), id); err != nil {
		t.Fatalf("AdmitIdentity(system admin) = %v, want nil", err)
	}
	for _, perm := range []permission.Permission{permission.TenantsRead, permission.TenantsSetStatus, permission.TenantsDelete} {
		if err := m.AuthorizeTenantTarget(ctx, "acme", perm); err != nil {
			t.Fatalf("system admin %s on suspended acme = %v, want nil", perm, err)
		}
	}
	active := tenant.StatusActive
	if err := m.AuthorizeTenantUpdate(ctx, "acme", tenant.TenantPatch{Status: &active}); err != nil {
		t.Fatalf("system admin reactivating acme = %v, want nil", err)
	}
}

// AdmitIdentity refuses a nil identity rather than admitting it.
func TestAdmitIdentity_NilIdentityUnauthenticated(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{}, WithTenantStatusReader(statusTenants(t)))
	if err := m.AdmitIdentity(context.Background(), nil); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("AdmitIdentity(nil) = %v, want ErrUnauthenticated", err)
	}
}

// AdmitTenant answers for a tenant alone what AdmitIdentity answers for an
// identity of it: refused while the tenant is suspended, admitted again on
// reactivation, and never refused for another tenant's status.
func TestAdmitTenant_FollowsTheTenantsStatus(t *testing.T) {
	tenants := statusTenants(t)
	m := newTestMiddleware(&mockAuthorizer{}, WithTenantStatusReader(tenants))
	if err := m.AdmitTenant(context.Background(), "acme"); err != nil {
		t.Fatalf("AdmitTenant while active: %v", err)
	}

	setStatus(t, tenants, "acme", tenant.StatusSuspended)
	wantNotActive(t, "AdmitTenant", m.AdmitTenant(context.Background(), "acme"), tenant.StatusSuspended)
	if err := m.AdmitTenant(context.Background(), "globex"); err != nil {
		t.Fatalf("AdmitTenant(globex) while acme is suspended: %v", err)
	}

	setStatus(t, tenants, "acme", tenant.StatusActive)
	if err := m.AdmitTenant(context.Background(), "acme"); err != nil {
		t.Fatalf("AdmitTenant after reactivation: %v", err)
	}
}

// AdmitTenant admits the system tenant and a tenant with no record: neither
// has a status that could refuse it.
func TestAdmitTenant_SystemAndUnknownTenantsAdmitted(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{}, WithTenantStatusReader(statusTenants(t)))
	for _, id := range []string{"", "nonexistent"} {
		if err := m.AdmitTenant(context.Background(), id); err != nil {
			t.Fatalf("AdmitTenant(%q) = %v, want admitted", id, err)
		}
	}
}

// stepClock is a test clock advanced by hand.
type stepClock struct{ now time.Time }

func (c *stepClock) Now() time.Time { return c.now }

// The cache answers a tenant's status from one record read per TTL, and
// reads the record again once the TTL has passed.
func TestTenantStatusCache_ReadsOncePerTTL(t *testing.T) {
	rec := &countingTenants{rec: &tenant.TenantInfo{ID: "acme", Status: tenant.StatusActive}}
	clock := &stepClock{now: time.Unix(1000, 0)}
	c := newTenantStatusCache(rec, time.Second, clock.Now, TenantStatusCacheMaxEntries)

	for range 3 {
		info, err := c.GetTenant(context.Background(), "acme")
		if err != nil || info.Status != tenant.StatusActive {
			t.Fatalf("GetTenant = (%+v, %v), want active", info, err)
		}
	}
	if len(rec.calls) != 1 {
		t.Fatalf("record reads within the TTL = %d, want 1", len(rec.calls))
	}

	rec.rec = &tenant.TenantInfo{ID: "acme", Status: tenant.StatusSuspended}
	clock.now = clock.now.Add(time.Second)
	info, err := c.GetTenant(context.Background(), "acme")
	if err != nil || info.Status != tenant.StatusSuspended {
		t.Fatalf("GetTenant after the TTL = (%+v, %v), want suspended", info, err)
	}
	if len(rec.calls) != 2 {
		t.Fatalf("record reads after the TTL = %d, want 2", len(rec.calls))
	}
}

// A tenant with no record is remembered for the TTL like one with a record,
// so a deleted tenant's credentials cannot buy a store read per request; a
// failed read is never remembered.
func TestTenantStatusCache_NotFoundCachedErrorsNot(t *testing.T) {
	rec := &countingTenants{err: tenant.ErrTenantNotFound}
	clock := &stepClock{now: time.Unix(1000, 0)}
	c := newTenantStatusCache(rec, time.Second, clock.Now, TenantStatusCacheMaxEntries)
	for range 2 {
		if _, err := c.GetTenant(context.Background(), "gone"); !errors.Is(err, tenant.ErrTenantNotFound) {
			t.Fatalf("GetTenant(gone) = %v, want ErrTenantNotFound", err)
		}
	}
	if len(rec.calls) != 1 {
		t.Fatalf("reads of a missing tenant within the TTL = %d, want 1", len(rec.calls))
	}

	failing := &countingTenants{err: errStoreDown}
	c = newTenantStatusCache(failing, time.Second, clock.Now, TenantStatusCacheMaxEntries)
	for range 2 {
		if _, err := c.GetTenant(context.Background(), "acme"); !errors.Is(err, errStoreDown) {
			t.Fatalf("GetTenant = %v, want the store error", err)
		}
	}
	if len(failing.calls) != 2 {
		t.Fatalf("reads after a failure = %d, want 2: a failure is not cached", len(failing.calls))
	}
}

var errStoreDown = errors.New("store down")

// The cache hands out its own copy: a caller cannot change what the next
// caller is answered.
func TestTenantStatusCache_AnswersACopy(t *testing.T) {
	rec := &countingTenants{rec: &tenant.TenantInfo{ID: "acme", Status: tenant.StatusActive}}
	c := newTenantStatusCache(rec, time.Second, (&stepClock{now: time.Unix(1000, 0)}).Now, TenantStatusCacheMaxEntries)
	first, _ := c.GetTenant(context.Background(), "acme")
	first.Status = tenant.StatusDisabled
	second, _ := c.GetTenant(context.Background(), "acme")
	if second.Status != tenant.StatusActive {
		t.Fatalf("second answer = %s, want active", second.Status)
	}
}

// An entry of any other type is a miss, never an answer: the status is read
// again from the store and the entry replaced, so a suspended tenant is not
// assumed active.
func TestTenantStatusCache_WrongTypedEntryIsAMiss(t *testing.T) {
	for _, planted := range []any{"active", tenantStatusEntry{status: tenant.StatusActive}, (*tenantStatusEntry)(nil)} {
		rec := &countingTenants{rec: &tenant.TenantInfo{ID: "acme", Status: tenant.StatusSuspended}}
		c := newTenantStatusCache(rec, time.Second, (&stepClock{now: time.Unix(1000, 0)}).Now, TenantStatusCacheMaxEntries)
		c.entries.Store("acme", planted)

		info, err := c.GetTenant(context.Background(), "acme")
		if err != nil || info.Status != tenant.StatusSuspended {
			t.Fatalf("planted %T: GetTenant = (%+v, %v), want suspended from the store", planted, info, err)
		}
		if len(rec.calls) != 1 {
			t.Fatalf("planted %T: record reads = %d, want 1", planted, len(rec.calls))
		}
		if _, err := c.GetTenant(context.Background(), "acme"); err != nil || len(rec.calls) != 1 {
			t.Fatalf("planted %T: second read = %v, reads %d, want the replaced entry served", planted, err, len(rec.calls))
		}
	}
}

// NewTenantStatusCache with a non-positive TTL caches nothing: it returns
// the reader itself.
func TestNewTenantStatusCache_NonPositiveTTLIsTheReader(t *testing.T) {
	rec := &countingTenants{}
	for _, ttl := range []time.Duration{0, -time.Second} {
		if got := NewTenantStatusCache(rec, ttl); got != TenantStatusReader(rec) {
			t.Fatalf("NewTenantStatusCache(ttl=%v) = %T, want the reader", ttl, got)
		}
	}
	if _, ok := NewTenantStatusCache(rec, TenantStatusTTL).(*tenantStatusCache); !ok {
		t.Fatal("NewTenantStatusCache(TenantStatusTTL) did not cache")
	}
}
