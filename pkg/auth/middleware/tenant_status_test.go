package middleware

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/authtest"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// These tests hold the tenant status gate against the REAL tenant service
// and, for certificates, the REAL mTLS authenticator: the gate is a statement
// about what the production authentication path admits.

// statusTenants is a real tenant service holding acme and globex, both
// created active.
func statusTenants(t *testing.T) tenant.TenantServicer {
	t.Helper()
	svc, err := tenant.NewKVTenantService(kvstore.NewMemoryKVStore(), slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	for _, id := range []string{"acme", "globex"} {
		if _, err := svc.CreateTenant(context.Background(), id, id, tenant.IsolationPrefix); err != nil {
			t.Fatalf("CreateTenant(%s): %v", id, err)
		}
	}
	return svc
}

// setStatus moves tenantID to s through the service, as the system
// administrator's update does.
func setStatus(t *testing.T, svc tenant.TenantServicer, tenantID string, s tenant.Status) {
	t.Helper()
	if _, err := svc.UpdateTenant(context.Background(), tenantID, tenant.TenantPatch{Status: &s}); err != nil {
		t.Fatalf("UpdateTenant(%s, %s): %v", tenantID, s, err)
	}
}

// tokenAuthenticator is an embedder's bearer validator: each token names the
// identity it was issued to.
type tokenAuthenticator map[string]*auth.Identity

func (a tokenAuthenticator) Authenticate(ctx context.Context) (*auth.Identity, error) {
	id, ok := a[auth.HTTPAuthorizationFromContext(ctx)]
	if !ok {
		return nil, auth.ErrUnauthenticated
	}
	cp := *id
	return &cp, nil
}

func (tokenAuthenticator) Name() string { return "token-stub" }

// credential is one way a principal of a tenant presents itself.
type credential struct {
	name string
	m    func(tenants TenantStatusReader) *AuthMiddleware
	ctx  func(t *testing.T, tenantID string) context.Context
}

func statusCredentials() []credential {
	tokens := tokenAuthenticator{
		"Bearer acme-tok":   {Subject: "bob", TenantID: "acme"},
		"Bearer globex-tok": {Subject: "gil", TenantID: "globex"},
	}
	return []credential{
		{
			name: "mtls",
			m: func(r TenantStatusReader) *AuthMiddleware {
				// The binding policy is not what this test judges; under the
				// default the unenrolled certificate is refused before status.
				return newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(auth.NewMTLSAuthenticator()),
					WithMTLSBindingPolicy(auth.MTLSBindingEnrolled), WithTenantStatusReader(r))
			},
			ctx: func(t *testing.T, tenantID string) context.Context {
				return mtlsContext(mintCert(t, "bob", tenantID, nil, ""))
			},
		},
		{
			name: "bearer",
			m: func(r TenantStatusReader) *AuthMiddleware {
				return newTestMiddleware(&mockAuthorizer{}, WithAuthenticator(tokens), WithTenantStatusReader(r))
			},
			ctx: func(_ *testing.T, tenantID string) context.Context {
				return auth.WithHTTPAuthorization(context.Background(), "Bearer "+tenantID+"-tok")
			},
		},
	}
}

// A principal of a suspended and of a disabled tenant is refused, over mTLS
// and over a bearer token; the credential it held before the change is the
// one refused after it, and reactivating the tenant restores it. A principal
// of another, active tenant is unaffected throughout.
func TestTenantStatus_NonActiveTenantRefusedOnEveryCredential(t *testing.T) {
	for _, c := range statusCredentials() {
		for _, s := range []tenant.Status{tenant.StatusSuspended, tenant.StatusDisabled} {
			t.Run(c.name+"/"+string(s), func(t *testing.T) {
				tenants := statusTenants(t)
				m := c.m(tenants)
				acme, globex := c.ctx(t, "acme"), c.ctx(t, "globex")

				if _, _, err := m.Authenticate(acme); err != nil {
					t.Fatalf("active acme before the change: %v", err)
				}
				setStatus(t, tenants, "acme", s)

				_, _, err := m.Authenticate(acme)
				var na *TenantNotActiveError
				if !errors.As(err, &na) || na.TenantID != "acme" || na.Status != string(s) {
					t.Fatalf("%s acme = %T(%v), want *TenantNotActiveError{acme, %s}", s, err, err, s)
				}
				assertMappedAsDisabledAccount(t, err)
				if _, id, err := m.Authenticate(globex); err != nil || id.TenantID != "globex" {
					t.Fatalf("globex while acme is %s = (%+v, %v), want admitted", s, id, err)
				}

				setStatus(t, tenants, "acme", tenant.StatusActive)
				if _, id, err := m.Authenticate(acme); err != nil || id.TenantID != "acme" {
					t.Fatalf("reactivated acme = (%+v, %v), want admitted", id, err)
				}
			})
		}
	}
}

// assertMappedAsDisabledAccount checks err gets, on HTTP and on gRPC, exactly
// the answer a disabled account gets.
func assertMappedAsDisabledAccount(t *testing.T, err error) {
	t.Helper()
	disabled := &UserDisabledError{Username: "bob", TenantID: "acme"}
	if got, want := ClassifyAuthError(err), ClassifyAuthError(disabled); got != want || got != AuthFailureUnauthorized {
		t.Fatalf("class = %v, want %v", got, want)
	}
	if got := HTTPStatus(err, http.StatusUnauthorized); got != http.StatusForbidden {
		t.Fatalf("HTTP status = %d, want %d", got, http.StatusForbidden)
	}
	if got := status.Code(GRPCError(err, codes.Unauthenticated)); got != codes.PermissionDenied {
		t.Fatalf("gRPC code = %v, want %v", got, codes.PermissionDenied)
	}
}

// The REST transport answers the refusal with the status a disabled account
// gets.
func TestTenantStatus_RESTAnswersAsForADisabledAccount(t *testing.T) {
	tenants := statusTenants(t)
	setStatus(t, tenants, "acme", tenant.StatusSuspended)
	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(tokenAuthenticator{"Bearer acme-tok": {Subject: "bob", TenantID: "acme"}}),
		WithTenantStatusReader(tenants))
	handler := m.RESTMiddleware(bearerCarrierRoutes(), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("handler reached for a principal of a suspended tenant")
	}))
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	req.Header.Set("Authorization", "Bearer acme-tok")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// A delegated identity acts inside its tenant, so it is gated the same way.
func TestTenantStatus_DelegationIntoNonActiveTenantRefused(t *testing.T) {
	tenants := statusTenants(t)
	ctx, a := dlgPresented(t)
	m := dlgMiddleware(t, a, WithAuditLogger(&mockAuditLogger{}), WithTenantStatusReader(tenants))
	if _, _, err := m.Authenticate(ctx); err != nil {
		t.Fatalf("delegation into active acme: %v", err)
	}
	setStatus(t, tenants, "acme", tenant.StatusDisabled)
	var na *TenantNotActiveError
	if _, _, err := m.Authenticate(ctx); !errors.As(err, &na) || na.TenantID != "acme" {
		t.Fatalf("delegation into disabled acme = %v, want *TenantNotActiveError", err)
	}
}

// countingTenants records every lookup and answers with rec or err.
type countingTenants struct {
	calls []string
	rec   *tenant.TenantInfo
	err   error
}

func (c *countingTenants) GetTenant(_ context.Context, id string) (*tenant.TenantInfo, error) {
	c.calls = append(c.calls, id)
	return c.rec, c.err
}

// The system tenant is never gated -- its administrators are the principals
// who reactivate every other tenant -- and is never even looked up.
func TestTenantStatus_SystemTenantNeverGated(t *testing.T) {
	r := &countingTenants{err: errors.New("must not be read")}
	for _, id := range []string{tenantid.System, ""} {
		m := newTestMiddleware(&mockAuthorizer{},
			WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: &auth.Identity{Subject: "root", TenantID: id}}),
			WithTenantStatusReader(r))
		if _, _, err := m.Authenticate(context.Background()); err != nil {
			t.Fatalf("system principal (tenant %q) = %v, want admitted", id, err)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("system tenant looked up: %v", r.calls)
	}
}

// The system administrator's control-plane calls on a suspended tenant pass:
// its own tenant is the one the gate reads, and the tenant it acts on is the
// target of the call.
func TestTenantStatus_SystemAdminControlPlaneOnNonActiveTenantPasses(t *testing.T) {
	tenants := statusTenants(t)
	setStatus(t, tenants, "acme", tenant.StatusSuspended)
	m := builtInMiddleware(t,
		WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: authtest.AdminIdentity(tenantid.System, "root")}),
		WithTenantStatusReader(tenants))
	ctx, _, err := m.Authenticate(context.Background())
	if err != nil {
		t.Fatalf("system admin while acme is suspended: %v", err)
	}
	active := tenant.StatusActive
	if err := m.AuthorizeTenantUpdate(ctx, "acme", tenant.TenantPatch{Status: &active}); err != nil {
		t.Fatalf("system admin reactivating acme = %v, want nil", err)
	}
	if err := m.AuthorizeTenantTarget(ctx, "acme", permission.TenantsRead); err != nil {
		t.Fatalf("system admin reading suspended acme = %v, want nil", err)
	}
}

// A tenant admin holds tenants:update on its own tenant, and that is not
// tenants:set_status: a status change may not ride the update baseline, alone
// or beside a field the admin may change.
func TestTenantStatus_TenantAdminCannotChangeOwnStatus(t *testing.T) {
	m := builtInMiddleware(t)
	admin := as(authtest.AdminIdentity("acme", "acme-admin"))
	for _, s := range []tenant.Status{tenant.StatusActive, tenant.StatusSuspended, tenant.StatusDisabled} {
		for _, patch := range []tenant.TenantPatch{{Status: &s}, {Name: "Acme", Status: &s}} {
			if err := m.AuthorizeTenantUpdate(admin, "acme", patch); err == nil {
				t.Errorf("acme admin setting its own status %s (%+v) = nil, want a refusal", s, patch)
			}
		}
	}
	if err := m.AuthorizeTenantUpdate(admin, "acme", tenant.TenantPatch{Name: "Acme"}); err != nil {
		t.Fatalf("acme admin renaming acme = %v, want nil", err)
	}
	if err := m.AuthorizeTenant(admin, "acme", permission.TenantsSetStatus); err == nil {
		t.Fatal("acme admin holds tenants:set_status on its own tenant")
	}
}

// A tenant with no record was never registered and is served as the store
// resolver serves it; a record with no known status fails closed; a failed
// read is a refusal, never an admission.
func TestTenantStatus_LookupOutcomes(t *testing.T) {
	sealedErr := &tenant.StoreError{Op: "get", Err: seal.ErrBarrierSealed}
	cases := []struct {
		name   string
		reader *countingTenants
		check  func(error) bool
	}{
		{"unregistered", &countingTenants{err: tenant.ErrTenantNotFound}, func(err error) bool { return err == nil }},
		{"no status", &countingTenants{rec: &tenant.TenantInfo{ID: "acme"}}, func(err error) bool {
			var na *TenantNotActiveError
			return errors.As(err, &na) && na.Status == ""
		}},
		{"unknown status", &countingTenants{rec: &tenant.TenantInfo{ID: "acme", Status: "paused"}}, func(err error) bool {
			var na *TenantNotActiveError
			return errors.As(err, &na) && na.Status == "paused"
		}},
		{"sealed", &countingTenants{err: sealedErr}, func(err error) bool {
			var bs *BarrierSealedError
			return errors.As(err, &bs)
		}},
		{"store down", &countingTenants{err: errors.New("io")}, func(err error) bool {
			var su *StoreUnavailableError
			return errors.As(err, &su)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestMiddleware(&mockAuthorizer{},
				WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: &auth.Identity{Subject: "bob", TenantID: "acme"}}),
				WithTenantStatusReader(tc.reader))
			if _, _, err := m.Authenticate(context.Background()); !tc.check(err) {
				t.Fatalf("err = %T(%v)", err, err)
			}
			if len(tc.reader.calls) != 1 || tc.reader.calls[0] != "acme" {
				t.Fatalf("lookups = %v, want exactly acme", tc.reader.calls)
			}
		})
	}
}

// Without a reader no tenant status is enforced.
func TestTenantStatus_NoReaderEnforcesNothing(t *testing.T) {
	m := newTestMiddleware(&mockAuthorizer{},
		WithAuthenticator(&mockAuthenticator{name: "test-authn", identity: &auth.Identity{Subject: "bob", TenantID: "acme"}}))
	if _, _, err := m.Authenticate(context.Background()); err != nil {
		t.Fatalf("no reader: %v", err)
	}
}

func TestTenantNotActiveError_Message(t *testing.T) {
	if got := (&TenantNotActiveError{TenantID: "acme", Status: "suspended"}).Error(); !strings.Contains(got, "acme") || !strings.Contains(got, "status suspended") {
		t.Fatalf("message = %q", got)
	}
	if got := (&TenantNotActiveError{TenantID: "acme"}).Error(); strings.Contains(got, "status") || !strings.HasSuffix(got, "acme") {
		t.Fatalf("message without a status = %q", got)
	}
}
