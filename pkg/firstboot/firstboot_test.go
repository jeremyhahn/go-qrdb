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

package firstboot

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// realCertPEM generates a valid self-signed certificate PEM for tests that
// exercise the admin-cert issuance + SPKI-thumbprint binding path.
func realCertPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "admin"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ---------------------------------------------------------------------------
// In-memory test doubles
// ---------------------------------------------------------------------------

// fakeTenants satisfies tenant.TenantServicer for first-boot testing.
type fakeTenants struct {
	created          atomic.Int32
	createCalls      []createTenantCall
	createErr        error
	createAlreadyErr bool
}

type createTenantCall struct {
	id            string
	name          string
	isolationType tenant.IsolationType
}

func (f *fakeTenants) CreateTenant(_ context.Context, id, name string, isolationType tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	f.createCalls = append(f.createCalls, createTenantCall{
		id:            id,
		name:          name,
		isolationType: isolationType,
	})
	if f.createAlreadyErr {
		return nil, tenant.ErrTenantAlreadyExists
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.created.Add(1)
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: isolationType}, nil
}

func (f *fakeTenants) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (f *fakeTenants) ListTenants(_ context.Context) ([]tenant.TenantInfo, error) {
	return nil, nil
}
func (f *fakeTenants) DeleteTenant(_ context.Context, _ string) error { return nil }
func (f *fakeTenants) UpdateTenant(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (f *fakeTenants) UpdateQuotas(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (f *fakeTenants) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}
func (f *fakeTenants) TenantBarrierInit(_ context.Context, _ string, _ []byte, _, _ int) (*tenant.TenantBarrierInitResult, error) {
	return nil, nil
}
func (f *fakeTenants) TenantBarrierInitWithStrategy(_ context.Context, _ string, _ []byte, _, _ int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return nil, nil
}
func (f *fakeTenants) TenantBarrierUnseal(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (f *fakeTenants) TenantBarrierSeal(_ context.Context, _ string) error { return nil }
func (f *fakeTenants) TenantBarrierStatus(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
	return nil, nil
}

// fakeUsers satisfies user.UserServicer.
type fakeUsers struct {
	createCalls []user.CreateUserRequest
	createErr   error
	exists      bool
	// idExists models the OTHER guard a boot-time atomic create can lose on:
	// a concurrent attempt whose generated id ALSO collided, reported as
	// user.UserIDExistsError rather than UsernameExistsError (go-qrdb#555's
	// own failure shape — see TestInitialize_AdminIDCollisionIsIdempotent).
	idExists bool
	nextID   uint64
	// bound models an admin that already holds a live certificate binding
	// (go-qrdb#548 review: B6) -- a test proving a pure idempotent re-run
	// (nothing touches the CA/Signer/AdminCertPEM path a second time) sets
	// this so GetUserByUsername's answer reports a live binding, the same
	// way a REAL store would for an admin BindAdminCert already succeeded
	// for. Without it, every admin this fake reports has zero CertBindings
	// unconditionally, which Initialize's own needsBinding check would
	// read as "still needs repair" and attempt to issue a certificate --
	// exactly what these idempotency tests exist to prove does NOT happen.
	bound bool
}

func (f *fakeUsers) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	f.createCalls = append(f.createCalls, *req)
	if f.idExists {
		return nil, &user.UserIDExistsError{ID: 4242, TenantID: req.TenantID}
	}
	if f.exists {
		return nil, &user.UsernameExistsError{Username: req.Username, TenantID: req.TenantID}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	return &user.User{ID: f.nextID, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs, Lifecycle: user.LifecycleActive}, nil
}

func (f *fakeUsers) GetUser(_ context.Context, _ uint64) (*user.User, error) { return nil, nil }

// GetUserByUsername makes fakeUsers a firstboot.UserResolver, as qrdb's own
// user.UserServicer is. Whenever f.exists or f.idExists models an admin that
// is already persisted — or after a successful CreateUser in THIS test run —
// it answers with the record first boot itself writes: the requested roles,
// System false, lifecycle active. A lookup that answered with a bare record
// would contradict its own CreateUser, and first boot's provenance check
// rightly refuses such a record. Never (nil, nil) for a name it should know,
// which is the contract violation the resolution seam deliberately refuses.
func (f *fakeUsers) GetUserByUsername(_ context.Context, tenantID, username string) (*user.User, error) {
	for i := len(f.createCalls) - 1; i >= 0; i-- {
		if r := f.createCalls[i]; r.Username == username && r.TenantID == tenantID {
			u := &user.User{ID: f.nextID, Username: username, TenantID: tenantID, RoleIDs: r.RoleIDs, Lifecycle: user.LifecycleActive}
			if f.bound {
				u.CertBindings = []user.CertBinding{{Thumbprint: "fake-thumbprint", Kind: user.CertBindingSPKIThumbprint}}
			}
			return u, nil
		}
	}
	return nil, nil
}
func (f *fakeUsers) GetUserByEmail(_ context.Context, _ string, _ string) (*user.User, error) {
	return nil, nil
}
func (f *fakeUsers) UpdateUser(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
	return nil, nil
}
func (f *fakeUsers) DeleteUser(_ context.Context, _ uint64) error { return nil }
func (f *fakeUsers) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return nil, nil
}
func (f *fakeUsers) EnableUser(_ context.Context, _ uint64) error  { return nil }
func (f *fakeUsers) DisableUser(_ context.Context, _ uint64) error { return nil }
func (f *fakeUsers) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (f *fakeUsers) AddCertBinding(_ context.Context, _ uint64, _ string, _ int64) (*user.User, error) {
	return nil, nil
}

func (f *fakeUsers) RemoveCertBinding(_ context.Context, _ uint64, _ string) (*user.User, error) {
	return nil, nil
}

// fakeRoles satisfies role.RoleServicer.
type fakeRoles struct {
	createCount  atomic.Int32
	createErr    error
	existsByName map[string]bool
	// existsByID maps a role id to the record ALREADY persisted under it —
	// modelling go-qrdb#555's atomic-guard collision, where CreateRole
	// refuses with RoleIDExistsError before ever comparing names. The test
	// controls what that persisted record actually looks like, so it can
	// be identical to what the caller requested (the legitimate concurrent
	// first-boot case) or deliberately different (the genuine-conflict
	// case registerBuiltInRoles must still refuse).
	existsByID  map[string]role.Role
	createCalls []role.CreateRoleRequest
	seeded      []role.Role
	// existsByNameRecord, keyed like existsByName, models what is ACTUALLY
	// persisted under a name collision when it must differ from the incoming
	// request — the genuine-conflict case registerBuiltInRoles must refuse.
	// A name present in existsByName with no entry here is assumed identical
	// to the request, matching the legitimate "another attempt already
	// created this exact role" case.
	existsByNameRecord map[string]role.Role
}

func (f *fakeRoles) CreateRole(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	f.createCalls = append(f.createCalls, *req)
	if f.existsByName[req.Name] {
		// Already present. The record is still in the store, so
		// GetRoleByName must find it. Absent an explicit override, model it
		// as field-for-field identical to what this request would have
		// created, which is what a restart of the same release or another
		// node's own successful attempt looks like.
		if existing, ok := f.existsByNameRecord[req.Name]; ok {
			f.remember(existing)
		} else {
			f.remember(role.Role{
				ID:          req.ID,
				Name:        req.Name,
				TenantID:    req.TenantID,
				Permissions: req.Permissions,
				System:      role.IsSystemRoleName(req.Name),
			})
		}
		return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
	}
	if existing, ok := f.existsByID[req.ID]; ok {
		f.remember(existing)
		return nil, &role.RoleIDExistsError{ID: req.ID, TenantID: req.TenantID}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.createCount.Add(1)
	f.remember(role.Role{ID: req.ID, Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions})
	return &role.Role{ID: req.ID, Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions}, nil
}

// remember records a role as persisted, so the name lookup below answers as a
// real store would.
func (f *fakeRoles) remember(r role.Role) {
	for i := range f.seeded {
		if f.seeded[i].TenantID == r.TenantID && f.seeded[i].Name == r.Name {
			return
		}
	}
	f.seeded = append(f.seeded, r)
}

func (f *fakeRoles) GetRole(_ context.Context, _ string) (*role.Role, error) { return nil, nil }

// GetRoleByName makes fakeRoles a role.NameResolver, as qrdb's own
// role.RoleServicer is. It answers from what has been persisted and reports a
// typed not-found otherwise — never (nil, nil), which is a contract violation
// the resolution seam deliberately refuses.
func (f *fakeRoles) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	for i := range f.seeded {
		if f.seeded[i].TenantID == tenantID && f.seeded[i].Name == name {
			r := f.seeded[i]
			return &r, nil
		}
	}
	return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
}
func (f *fakeRoles) UpdateRole(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
	return nil, nil
}
func (f *fakeRoles) DeleteRole(_ context.Context, _ string) error { return nil }
func (f *fakeRoles) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return nil, nil
}
func (f *fakeRoles) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return nil, nil
}

// fakeCA implements CertProvider for tests.
type fakeCA struct {
	called   atomic.Int32
	cert     []byte
	key      []byte
	err      error
	lastID   string
	lastSubj string
}

func (f *fakeCA) GenerateAdminCert(_ context.Context, tenantID, subject string) ([]byte, []byte, error) {
	f.called.Add(1)
	f.lastID = tenantID
	f.lastSubj = subject
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.cert, f.key, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeBinder records CertBinder.BindAdminCert calls.
type fakeBinder struct {
	tenantID, thumbprint string
	userID               uint64
	calls                int
	err                  error
}

func (b *fakeBinder) BindAdminCert(_ context.Context, tenantID string, userID uint64, thumbprint string) error {
	b.calls++
	b.tenantID, b.userID, b.thumbprint = tenantID, userID, thumbprint
	return b.err
}

// fakeSigner implements CertSigner (Path B: CSR-sign).
type fakeSigner struct {
	called  atomic.Int32
	cert    []byte
	lastCSR []byte
	err     error
}

func (s *fakeSigner) SignAdminCSR(_ context.Context, _ /*tenantID*/, _ /*subject*/ string, csrPEM []byte) ([]byte, error) {
	s.called.Add(1)
	s.lastCSR = csrPEM
	if s.err != nil {
		return nil, s.err
	}
	return s.cert, nil
}

// TestInitialize_BindsAdminCertThumbprint verifies Path A binds the issued
// admin cert's SPKI thumbprint to the admin user via the CertBinder seam.
func TestInitialize_BindsAdminCertThumbprint(t *testing.T) {
	certPEM := realCertPEM(t)
	binder := &fakeBinder{}
	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		CA:     &fakeCA{cert: certPEM, key: []byte("KEY")},
		Binder: binder, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if binder.calls != 1 {
		t.Fatalf("BindAdminCert calls = %d, want 1", binder.calls)
	}
	if binder.thumbprint == "" || binder.thumbprint != result.AdminCertThumbprint {
		t.Fatalf("bound thumbprint %q != result thumbprint %q", binder.thumbprint, result.AdminCertThumbprint)
	}
	if binder.userID == 0 {
		t.Fatalf("bound userID = %d, want non-zero", binder.userID)
	}
}

// TestInitialize_PathB_CSRSign verifies the CSR-sign path: AdminCSRPEM + Signer
// signs the admin's CSR, no key is returned (admin keeps it), and the SPKI
// thumbprint is bound.
func TestInitialize_PathB_CSRSign(t *testing.T) {
	certPEM := realCertPEM(t)
	signer := &fakeSigner{cert: certPEM}
	binder := &fakeBinder{}
	ca := &fakeCA{cert: realCertPEM(t), key: []byte("CA-KEY")}
	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		CA:          ca, // present, but CSR path must take precedence
		Signer:      signer,
		AdminCSRPEM: []byte("THE-CSR"),
		Binder:      binder,
		Logger:      discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if signer.called.Load() != 1 {
		t.Fatalf("SignAdminCSR calls = %d, want 1 (CSR path must win over CA)", signer.called.Load())
	}
	if ca.called.Load() != 0 {
		t.Fatalf("CA.GenerateAdminCert called %d times; CSR path must not invoke the CA", ca.called.Load())
	}
	if string(signer.lastCSR) != "THE-CSR" {
		t.Fatalf("signer got CSR %q, want THE-CSR", signer.lastCSR)
	}
	if result.AdminKeyPEM != nil {
		t.Fatalf("AdminKeyPEM must be nil on the CSR-sign path (admin keeps their key)")
	}
	if result.AdminCertThumbprint == "" || binder.thumbprint != result.AdminCertThumbprint {
		t.Fatalf("CSR-signed cert must be bound by thumbprint")
	}
}

// ---------------------------------------------------------------------------
// Happy path
// ---------------------------------------------------------------------------

func TestInitialize_FreshBootCreatesEverything(t *testing.T) {
	tenants := &fakeTenants{}
	users := &fakeUsers{}
	roles := &fakeRoles{}
	certPEM := realCertPEM(t)
	ca := &fakeCA{cert: certPEM, key: []byte("KEY-PEM")}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		CA:                ca,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if !result.SystemTenantCreated {
		t.Errorf("SystemTenantCreated = false, want true")
	}
	if !result.RolesCreated {
		t.Errorf("RolesCreated = false, want true")
	}
	if !result.AdminCreated {
		t.Errorf("AdminCreated = false, want true")
	}
	if string(result.AdminCertPEM) != string(certPEM) {
		t.Errorf("AdminCertPEM mismatch; want the generated cert")
	}
	if string(result.AdminKeyPEM) != "KEY-PEM" {
		t.Errorf("AdminKeyPEM = %q, want KEY-PEM", result.AdminKeyPEM)
	}
	if result.AdminCertThumbprint == "" {
		t.Errorf("AdminCertThumbprint not set; the issued admin cert must yield an SPKI thumbprint")
	}

	if len(tenants.createCalls) != 1 {
		t.Fatalf("createCalls = %d, want 1", len(tenants.createCalls))
	}
	tc := tenants.createCalls[0]
	if tc.id != tenantid.System {
		t.Errorf("CreateTenant id = %q, want %q", tc.id, tenantid.System)
	}
	if tc.isolationType != tenant.IsolationPrefix {
		t.Errorf("CreateTenant isolation = %v, want IsolationPrefix (system tenant must be prefix)", tc.isolationType)
	}

	// Standard posture (default): the six canonical roles plus the Owner
	// convenience role.
	wantRoles := len(role.BuiltInRoles(tenantid.System)) + 1
	if got := roles.createCount.Load(); got != int32(wantRoles) {
		t.Errorf("roles created = %d, want %d (6 canonical + owner)", got, wantRoles)
	}

	if len(users.createCalls) != 1 {
		t.Fatalf("user createCalls = %d, want 1", len(users.createCalls))
	}
	adminReq := users.createCalls[0]
	if adminReq.Username != "admin" {
		t.Errorf("admin Username = %q, want admin", adminReq.Username)
	}
	if adminReq.TenantID != tenantid.System {
		t.Errorf("admin TenantID = %q, want %q", adminReq.TenantID, tenantid.System)
	}
	// Standard posture: admin holds Admin@system AND Owner@system.
	wantAdminRoleID := role.SystemRoleID(tenantid.System, role.RoleNameAdmin)
	wantOwnerRoleID := role.SystemRoleID(tenantid.System, role.RoleNameOwner)
	if len(adminReq.RoleIDs) != 2 || adminReq.RoleIDs[0] != wantAdminRoleID || adminReq.RoleIDs[1] != wantOwnerRoleID {
		t.Errorf("admin RoleIDs = %v, want [%q %q]", adminReq.RoleIDs, wantAdminRoleID, wantOwnerRoleID)
	}
	// go-qrdb#106: the system-tenant bootstrap admin is the one identity
	// createAdminUser marks immutable.
	if !adminReq.System {
		t.Error("admin CreateUserRequest.System = false, want true (go-qrdb#106)")
	}

	if ca.called.Load() != 1 {
		t.Errorf("CA called = %d, want 1", ca.called.Load())
	}
	if ca.lastID != tenantid.System || ca.lastSubj != "admin" {
		t.Errorf("CA invoked with (%q, %q), want (system, admin)", ca.lastID, ca.lastSubj)
	}
}

// TestInitialize_StrictSeparationOfDuties verifies that under the strict
// posture (FIPS 140-3 / auth.separation_of_duties) the Owner convenience
// role is neither seeded nor assigned: exactly the six canonical roles are
// created and the admin holds only the Admin@system grant.
func TestInitialize_StrictSeparationOfDuties(t *testing.T) {
	tenants := &fakeTenants{}
	users := &fakeUsers{}
	roles := &fakeRoles{}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy:  auth.MTLSBindingEnrolled,
		Tenants:            tenants,
		Users:              users,
		Roles:              roles,
		Logger:             discardLogger(),
		SeparationOfDuties: true,
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	wantRoles := len(role.BuiltInRoles(tenantid.System))
	if got := roles.createCount.Load(); got != int32(wantRoles) {
		t.Errorf("roles created = %d, want %d (6 canonical, no owner)", got, wantRoles)
	}
	// The Owner role must NOT have been created under strict posture.
	for _, r := range roles.createCalls {
		if r.Name == role.RoleNameOwner {
			t.Errorf("Owner role was created under strict separation of duties; want it omitted")
		}
	}

	if len(users.createCalls) != 1 {
		t.Fatalf("user createCalls = %d, want 1", len(users.createCalls))
	}
	wantAdminRoleID := role.SystemRoleID(tenantid.System, role.RoleNameAdmin)
	adminReq := users.createCalls[0]
	if len(adminReq.RoleIDs) != 1 || adminReq.RoleIDs[0] != wantAdminRoleID {
		t.Errorf("admin RoleIDs = %v, want [%q] (no owner under strict)", adminReq.RoleIDs, wantAdminRoleID)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

// TestReconcileExistingAdmin_UnsupportedUserCreatorIsANoOp asserts that a
// UserCreator without the systemPromoter capability -- exactly the shape a
// downstream composer's adapter to its own IAM store takes, per
// Dependencies' own doc comment -- is skipped rather than treated as an
// error. fakeUsers implements only UserCreator's required CreateUser plus
// the wider user.UserServicer surface some other tests need, but never
// PromoteToSystemUser, so it stands in for that adapter shape here.
func TestReconcileExistingAdmin_UnsupportedUserCreatorIsANoOp(t *testing.T) {
	got := reconcileExistingAdmin(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Users:             &fakeUsers{exists: true},
		Logger:            discardLogger(),
	}, tenantid.System)
	if got != nil {
		t.Errorf("reconcileExistingAdmin(unsupported UserCreator) = %+v, want nil", got)
	}
}

// fakeSystemPromoter implements systemPromoter (and the bare UserCreator it
// is asserted against) with fully controllable failure injection, for the
// two reconcileExistingAdmin branches fakeUsers cannot reach at all: it
// implements neither method at all.
type fakeSystemPromoter struct {
	getByUsernameResult *user.User
	getByUsernameErr    error
	promoteResult       *user.User
	promoteErr          error
}

func (f *fakeSystemPromoter) CreateUser(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	return nil, &user.UsernameExistsError{Username: adminUsername}
}

func (f *fakeSystemPromoter) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	return f.getByUsernameResult, f.getByUsernameErr
}

func (f *fakeSystemPromoter) PromoteToSystemUser(_ context.Context, _ uint64) (*user.User, error) {
	return f.promoteResult, f.promoteErr
}

// TestReconcileExistingAdmin_GetUserByUsernameErrorIsANoOp covers the
// best-effort contract when resolving the existing admin itself fails: the
// reconciliation gives up rather than propagating a transient store error
// out of an otherwise-successful first-boot.
func TestReconcileExistingAdmin_GetUserByUsernameErrorIsANoOp(t *testing.T) {
	got := reconcileExistingAdmin(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Users:             &fakeSystemPromoter{getByUsernameErr: errors.New("transient lookup failure")},
		Logger:            discardLogger(),
	}, tenantid.System)
	if got != nil {
		t.Errorf("reconcileExistingAdmin(GetUserByUsername error) = %+v, want nil", got)
	}
}

// TestReconcileExistingAdmin_PromoteErrorReturnsTheUnpromotedRecord covers
// the same best-effort contract for a promotion that fails after the
// existing record was successfully resolved: the caller gets back the
// record it already had (not System yet), not an error and not nil.
func TestReconcileExistingAdmin_PromoteErrorReturnsTheUnpromotedRecord(t *testing.T) {
	existing := &user.User{ID: 42, Username: adminUsername, TenantID: tenantid.System}
	got := reconcileExistingAdmin(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Users: &fakeSystemPromoter{
			getByUsernameResult: existing,
			promoteErr:          errors.New("sentinel already held"),
		},
		Logger: discardLogger(),
	}, tenantid.System)
	if got != existing {
		t.Errorf("reconcileExistingAdmin(promote error) = %+v, want the unpromoted %+v", got, existing)
	}
}

func TestInitialize_AllPresentReturnsIdempotent(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	// bound: this test's whole point is a pure idempotent re-run that
	// touches nothing new -- the bogus ca below would fail to parse if
	// Initialize attempted to use it, which it must not for an admin that
	// already holds a live binding (go-qrdb#548 review: B6's needsBinding).
	users := &fakeUsers{exists: true, bound: true}
	roles := &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO:       true,
		role.RoleNameAdmin:    true,
		role.RoleNameOperator: true,
		role.RoleNameUser:     true,
		role.RoleNameAuditor:  true,
		role.RoleNameOwner:    true,
	}}
	ca := &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		CA:                ca,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if result.SystemTenantCreated {
		t.Errorf("SystemTenantCreated = true, want false (idempotent re-run)")
	}
	if result.RolesCreated {
		t.Errorf("RolesCreated = true, want false (all roles existed)")
	}
	if result.AdminCreated {
		t.Errorf("AdminCreated = true, want false (admin existed)")
	}
	if result.AdminCertPEM != nil || result.AdminKeyPEM != nil {
		t.Errorf("AdminCertPEM/Key non-nil on idempotent re-run; want nil so we don't rotate platform key material")
	}
	if ca.called.Load() != 0 {
		t.Errorf("CA called = %d, want 0 on idempotent re-run", ca.called.Load())
	}
}

// TestInitialize_AdminIDCollisionIsIdempotent reproduces go-qrdb#555's actual
// bootstrap failure shape: a concurrent first-boot attempt races
// createAdminUser for the SAME fixed admin username, and the LOSING batch's
// generated id ALSO collides — the realistic case for independently seeded
// nodes, not merely a coincidental username clash — so CreateUser reports
// user.UserIDExistsError rather than UsernameExistsError. Before this fix,
// only UsernameExistsError was tolerated, so this exact race aborted the
// whole first-boot run even though the identical admin had already
// committed.
func TestInitialize_AdminIDCollisionIsIdempotent(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	// bound: see TestInitialize_AllPresentReturnsIdempotent -- the admin
	// this models already holds a live binding, so Initialize must not
	// attempt to reissue one.
	users := &fakeUsers{idExists: true, bound: true}
	roles := &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO:       true,
		role.RoleNameAdmin:    true,
		role.RoleNameOperator: true,
		role.RoleNameUser:     true,
		role.RoleNameAuditor:  true,
		role.RoleNameOwner:    true,
	}}
	ca := &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		CA:                ca,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.AdminCreated {
		t.Errorf("AdminCreated = true, want false (admin already existed under a colliding id)")
	}
	if ca.called.Load() != 0 {
		t.Errorf("CA called = %d, want 0: an id-collision loser must not re-issue the platform admin cert", ca.called.Load())
	}
}

// TestInitialize_AdminIDCollisionUnverifiableRefused is the control: an
// id-collision error with NO way to verify it names the admin (no
// UserResolver, and Users does not implement one either) must refuse the
// run rather than silently accept an unverifiable collision.
func TestInitialize_AdminIDCollisionUnverifiableRefused(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	users := &unresolvableIDCollisionUsers{}
	roles := &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO:       true,
		role.RoleNameAdmin:    true,
		role.RoleNameOperator: true,
		role.RoleNameUser:     true,
		role.RoleNameAuditor:  true,
		role.RoleNameOwner:    true,
	}}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		Logger:            discardLogger(),
	})
	var target *AdminUserCreationError
	if !errors.As(err, &target) {
		t.Fatalf("Initialize err = %v, want *AdminUserCreationError", err)
	}
}

// unresolvableIDCollisionUsers is a UserCreator (deliberately NOT a
// UserResolver) whose CreateUser always reports an id collision, modelling a
// consumer's adapter that has no username-lookup seam at all.
type unresolvableIDCollisionUsers struct{}

func (unresolvableIDCollisionUsers) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	return nil, &user.UserIDExistsError{ID: 4242, TenantID: req.TenantID}
}

// TestInitialize_IDCollisionVerifiedIdenticalIsIdempotent reproduces
// go-qrdb#555's actual failure mode: a concurrent first-boot attempt (this
// run's own retry, or another node's) already won CreateRole's atomic
// guard for a built-in role's deterministic id, so CreateRole refuses with
// RoleIDExistsError rather than RoleNameExistsError. registerBuiltInRoles
// must read the persisted record back, confirm it is field-for-field the
// same built-in role this run would have created, and proceed exactly as
// it does for a name collision.
func TestInitialize_IDCollisionVerifiedIdenticalIsIdempotent(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	// bound: see TestInitialize_AllPresentReturnsIdempotent -- the admin
	// this models already holds a live binding, so Initialize must not
	// attempt to reissue one.
	users := &fakeUsers{exists: true, bound: true}
	roles := &fakeRoles{existsByID: map[string]role.Role{
		role.SystemRoleID(tenantid.System, role.RoleNameSO): {
			ID:          role.SystemRoleID(tenantid.System, role.RoleNameSO),
			Name:        role.RoleNameSO,
			TenantID:    tenantid.System,
			Permissions: role.BuiltInRoles(tenantid.System)[0].Permissions,
			System:      true,
		},
	}}
	ca := &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		CA:                ca,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	// The SO role's id collision is tolerated as idempotent; every OTHER
	// built-in role has no existsByID/existsByName entry and is genuinely
	// created for the first time by this call, so RolesCreated is still
	// true overall — the assertion that matters is that Initialize did not
	// refuse the whole run over the SO collision.
	if !result.RolesCreated {
		t.Errorf("RolesCreated = false, want true (every role but SO is newly created)")
	}
	soCalls := 0
	for _, call := range roles.createCalls {
		if call.Name == role.RoleNameSO {
			soCalls++
		}
	}
	if soCalls != 1 {
		t.Errorf("CreateRole(so) calls = %d, want exactly 1 (no retry loop)", soCalls)
	}
}

// TestInitialize_IDCollisionDifferentDefinitionRefused is the control: an id
// collision whose persisted record does NOT match the built-in definition —
// wrong permissions here — is a genuine, unexplained conflict and must be
// refused, never silently accepted as "close enough". This is the
// provenance concern an earlier version of this fix got wrong: matching on
// name and tenant alone is not enough to prove the existing record is the
// canonical role rather than something else that happens to share its id.
func TestInitialize_IDCollisionDifferentDefinitionRefused(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	users := &fakeUsers{exists: true}
	soID := role.SystemRoleID(tenantid.System, role.RoleNameSO)
	roles := &fakeRoles{existsByID: map[string]role.Role{
		soID: {
			ID:       soID,
			Name:     role.RoleNameSO,
			TenantID: tenantid.System,
			// Deliberately wrong: not the SO role's canonical permission set.
			Permissions: []permission.Permission{permission.KVGet},
		},
	}}
	ca := &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		CA:                ca,
		Logger:            discardLogger(),
	})
	var target *RolesRegistrationError
	if !errors.As(err, &target) {
		t.Fatalf("Initialize err = %v, want *RolesRegistrationError", err)
	}
	if target.Role != role.RoleNameSO {
		t.Errorf("Role = %q, want %q", target.Role, role.RoleNameSO)
	}
}

// TestInitialize_NameCollisionVerifiedIdenticalIsIdempotent covers the OTHER
// tolerated collision registerBuiltInRoles handles: RoleNameExistsError, now
// verified with the same provenance check as the id-collision branch rather
// than accepted on the error type alone.
func TestInitialize_NameCollisionVerifiedIdenticalIsIdempotent(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	// bound: see TestInitialize_AllPresentReturnsIdempotent -- the admin
	// this models already holds a live binding, so Initialize must not
	// attempt to reissue one.
	users := &fakeUsers{exists: true, bound: true}
	roles := &fakeRoles{existsByName: map[string]bool{role.RoleNameSO: true}}
	ca := &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		CA:                ca,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if !result.RolesCreated {
		t.Errorf("RolesCreated = false, want true (every role but SO is newly created)")
	}
}

// TestInitialize_NameCollisionDifferentDefinitionRefused is the name-collision
// control for TestInitialize_IDCollisionDifferentDefinitionRefused: a name
// collision whose persisted record does not match the built-in definition —
// here, a System flag of false — is a genuine conflict and must be refused
// rather than accepted because the error was RoleNameExistsError.
func TestInitialize_NameCollisionDifferentDefinitionRefused(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	users := &fakeUsers{exists: true}
	roles := &fakeRoles{
		existsByName: map[string]bool{role.RoleNameSO: true},
		existsByNameRecord: map[string]role.Role{
			role.RoleNameSO: {
				ID:          role.SystemRoleID(tenantid.System, role.RoleNameSO),
				Name:        role.RoleNameSO,
				TenantID:    tenantid.System,
				Permissions: role.BuiltInRoles(tenantid.System)[0].Permissions,
				// Deliberately wrong: a "SO" role that is not marked System
				// would be mutable and deletable through the ordinary APIs.
				System: false,
			},
		},
	}
	ca := &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		CA:                ca,
		Logger:            discardLogger(),
	})
	var target *RolesRegistrationError
	if !errors.As(err, &target) {
		t.Fatalf("Initialize err = %v, want *RolesRegistrationError", err)
	}
	if target.Role != role.RoleNameSO {
		t.Errorf("Role = %q, want %q", target.Role, role.RoleNameSO)
	}
}

// TestBuiltInRoleAlreadySeeded covers builtInRoleAlreadySeeded's fail-closed
// branches directly: a nil resolver, a resolver that cannot find the role,
// and a resolver that finds one under a DIFFERENT id or tenant than the
// canonical definition names. Every one of these must answer false, since
// registerBuiltInRoles' only alternative on true is refusing the whole
// first-boot run.
func TestBuiltInRoleAlreadySeeded(t *testing.T) {
	want := &role.Role{
		ID:          role.SystemRoleID(tenantid.System, role.RoleNameSO),
		Name:        role.RoleNameSO,
		TenantID:    tenantid.System,
		Permissions: role.BuiltInRoles(tenantid.System)[0].Permissions,
		System:      true,
	}

	t.Run("nil resolver", func(t *testing.T) {
		if builtInRoleAlreadySeeded(context.Background(), nil, tenantid.System, want) {
			t.Error("expected false with a nil resolver")
		}
	})

	t.Run("resolver cannot find the role", func(t *testing.T) {
		roles := &fakeRoles{}
		if builtInRoleAlreadySeeded(context.Background(), roles, tenantid.System, want) {
			t.Error("expected false when GetRoleByName reports not-found")
		}
	})

	t.Run("resolver returns nil, nil", func(t *testing.T) {
		// A resolver that violates the (record, nil) / (nil, typed-error)
		// contract must not crash this function: it fails closed instead of
		// dereferencing a nil *role.Role.
		if builtInRoleAlreadySeeded(context.Background(), nilResultResolver{}, tenantid.System, want) {
			t.Error("expected false when the resolver returns (nil, nil)")
		}
	})

	t.Run("persisted id differs", func(t *testing.T) {
		// requireCanonicalSystemRole (pkg/role) refuses to let a reserved
		// name be created under any id but its canonical one, so a
		// persisted record under a different id is never a legitimate
		// idempotent re-attempt by this build — it is always the genuine,
		// unexplained conflict this function exists to catch, on EITHER
		// collision kind.
		roles := &fakeRoles{}
		roles.remember(role.Role{ID: "a-different-id", Name: want.Name, TenantID: want.TenantID, Permissions: want.Permissions, System: want.System})
		if builtInRoleAlreadySeeded(context.Background(), roles, tenantid.System, want) {
			t.Error("expected false when the persisted id differs from the canonical one")
		}
	})

	t.Run("persisted tenant differs", func(t *testing.T) {
		const otherTenant = "acme"
		roles := &fakeRoles{}
		roles.remember(role.Role{ID: want.ID, Name: want.Name, TenantID: otherTenant, Permissions: want.Permissions, System: want.System})
		// Looked up under otherTenant so the resolver finds it — the check
		// under test is the comparison against want.TenantID afterward, not
		// GetRoleByName's own tenant-scoped lookup.
		if builtInRoleAlreadySeeded(context.Background(), roles, otherTenant, want) {
			t.Error("expected false when the persisted tenant differs from the canonical one")
		}
	})

	t.Run("persisted name differs", func(t *testing.T) {
		// fakeRoles' own GetRoleByName can never answer with a mismatched
		// Name — it filters on name equality — so this uses a resolver that
		// can, modelling one that resolved the query to the wrong record by
		// some other means. The comparison this function makes must catch it
		// independently of GetRoleByName's own behavior.
		resolver := mismatchedRoleResolver{role: &role.Role{
			ID: want.ID, Name: "a-different-name", TenantID: want.TenantID, Permissions: want.Permissions, System: want.System,
		}}
		if builtInRoleAlreadySeeded(context.Background(), resolver, tenantid.System, want) {
			t.Error("expected false when the persisted name differs from the canonical one")
		}
	})

	t.Run("persisted System flag differs", func(t *testing.T) {
		// Same id, tenant, and permissions, but NOT marked System: true — a
		// record like this passes every other check while remaining
		// mutable and deletable through the ordinary role APIs, which
		// defeats a built-in role's immutability. Must be refused.
		roles := &fakeRoles{}
		roles.remember(role.Role{ID: want.ID, Name: want.Name, TenantID: want.TenantID, Permissions: want.Permissions, System: false})
		if builtInRoleAlreadySeeded(context.Background(), roles, tenantid.System, want) {
			t.Error("expected false when the persisted System flag differs from the canonical one")
		}
	})

	t.Run("identical", func(t *testing.T) {
		roles := &fakeRoles{}
		roles.remember(*want)
		if !builtInRoleAlreadySeeded(context.Background(), roles, tenantid.System, want) {
			t.Error("expected true when every field matches")
		}
	})
}

// nilResultResolver implements role.NameResolver by returning (nil, nil),
// modelling a resolver that violates the "record or typed not-found" contract
// fakeRoles otherwise upholds. builtInRoleAlreadySeeded must survive it.
type nilResultResolver struct{}

func (nilResultResolver) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	return nil, nil
}

// mismatchedRoleResolver implements role.NameResolver by always returning the
// same fixed record regardless of the name queried, modelling a resolver that
// answered the wrong record. Used only where the scenario needs a Name field
// that fakeRoles' own name-filtered lookup could never produce.
type mismatchedRoleResolver struct{ role *role.Role }

func (m mismatchedRoleResolver) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	return m.role, nil
}

// shrinkBuiltInRoleSeedRetry replaces the package's retry bound and interval
// with test-sized ones for the duration of the calling test, restoring the
// production values on cleanup. BuiltInRoleSeedRetryWindow and
// BuiltInRoleSeedRetryInterval are package-level variables rather than
// constants for exactly this: a test that needs to observe the retry loop's
// deadline or its sleep must not wait out the production 5-second window to
// do it.
func shrinkBuiltInRoleSeedRetry(t *testing.T, window, interval time.Duration) {
	t.Helper()
	origWindow, origInterval := BuiltInRoleSeedRetryWindow, BuiltInRoleSeedRetryInterval
	BuiltInRoleSeedRetryWindow, BuiltInRoleSeedRetryInterval = window, interval
	t.Cleanup(func() {
		BuiltInRoleSeedRetryWindow, BuiltInRoleSeedRetryInterval = origWindow, origInterval
	})
}

// countingNotFoundResolver implements role.NameResolver by reporting
// not-found a fixed number of times before answering with a persisted
// record, modelling the legitimate concurrent-seeder lag
// BuiltInRoleAlreadySeededWithin exists to ride out: a create collision is
// reported before the winner's own write has become visible to this read.
type countingNotFoundResolver struct {
	misses atomic.Int32
	role   *role.Role
}

func (c *countingNotFoundResolver) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	if c.misses.Add(-1) >= 0 {
		return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
	}
	return c.role, nil
}

// TestBuiltInRoleAlreadySeededWithin covers the exported retry wrapper's own
// branches, over and above builtInRoleAlreadySeeded's single-lookup
// comparisons (TestBuiltInRoleAlreadySeeded): the nil-resolver short circuit,
// a genuine mismatch answered at once without waiting out the window, a
// lookup that keeps missing until a legitimate concurrent winner's write
// becomes visible, retry exhaustion when it never does, and the ctx.Done
// path a caller's own cancellation takes.
func TestBuiltInRoleAlreadySeededWithin(t *testing.T) {
	want := &role.Role{
		ID:          role.SystemRoleID(tenantid.System, role.RoleNameSO),
		Name:        role.RoleNameSO,
		TenantID:    tenantid.System,
		Permissions: role.BuiltInRoles(tenantid.System)[0].Permissions,
		System:      true,
	}

	t.Run("nil resolver", func(t *testing.T) {
		if BuiltInRoleAlreadySeededWithin(context.Background(), nil, tenantid.System, want) {
			t.Error("expected false with a nil resolver")
		}
	})

	t.Run("a definitive mismatch is refused without retrying", func(t *testing.T) {
		shrinkBuiltInRoleSeedRetry(t, time.Hour, time.Hour)
		roles := &fakeRoles{}
		roles.remember(role.Role{ID: "a-different-id", Name: want.Name, TenantID: want.TenantID, Permissions: want.Permissions, System: want.System})
		start := time.Now()
		if BuiltInRoleAlreadySeededWithin(context.Background(), roles, tenantid.System, want) {
			t.Error("expected false when the persisted id differs from the canonical one")
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("a definitive mismatch waited %s; it must return at once, not retry out the window", elapsed)
		}
	})

	t.Run("a late-arriving winner is found before the window closes", func(t *testing.T) {
		shrinkBuiltInRoleSeedRetry(t, time.Second, time.Millisecond)
		resolver := &countingNotFoundResolver{role: want}
		resolver.misses.Store(3)
		if !BuiltInRoleAlreadySeededWithin(context.Background(), resolver, tenantid.System, want) {
			t.Error("expected true once the winner's write becomes visible within the window")
		}
	})

	t.Run("retry exhaustion when the record never resolves", func(t *testing.T) {
		shrinkBuiltInRoleSeedRetry(t, 30*time.Millisecond, 5*time.Millisecond)
		roles := &fakeRoles{}
		start := time.Now()
		if BuiltInRoleAlreadySeededWithin(context.Background(), roles, tenantid.System, want) {
			t.Error("expected false when the role never resolves before the window closes")
		}
		if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
			t.Errorf("returned after %s; expected to wait out close to the whole %s window", elapsed, 30*time.Millisecond)
		}
	})

	t.Run("ctx cancellation stops the wait early", func(t *testing.T) {
		shrinkBuiltInRoleSeedRetry(t, time.Hour, time.Hour)
		roles := &fakeRoles{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		if BuiltInRoleAlreadySeededWithin(ctx, roles, tenantid.System, want) {
			t.Error("expected false when the context is already cancelled")
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("a cancelled context waited %s; it must stop the retry loop immediately", elapsed)
		}
	})
}

// ---------------------------------------------------------------------------
// Missing dependencies
// ---------------------------------------------------------------------------

func TestInitialize_MissingTenantsReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Users:             &fakeUsers{},
		Roles:             &fakeRoles{},
		Logger:            discardLogger(),
	})
	var want *MissingTenantsError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingTenantsError", err)
	}
}

func TestInitialize_MissingUsersReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Roles:             &fakeRoles{},
		Logger:            discardLogger(),
	})
	var want *MissingUsersError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingUsersError", err)
	}
}

func TestInitialize_MissingRolesReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             &fakeUsers{},
		Logger:            discardLogger(),
	})
	var want *MissingRolesError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingRolesError", err)
	}
}

func TestInitialize_MissingLoggerReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             &fakeUsers{},
		Roles:             &fakeRoles{},
	})
	var want *MissingLoggerError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingLoggerError", err)
	}
}

// ---------------------------------------------------------------------------
// Error propagation
// ---------------------------------------------------------------------------

func TestInitialize_TenantCreationErrorWrapped(t *testing.T) {
	sentinel := errors.New("backend down")
	tenants := &fakeTenants{createErr: sentinel}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             &fakeUsers{},
		Roles:             &fakeRoles{},
		Logger:            discardLogger(),
	})
	var wantErr *SystemTenantCreationError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *SystemTenantCreationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

func TestInitialize_RoleCreationErrorWrapped(t *testing.T) {
	sentinel := errors.New("role write failed")
	roles := &fakeRoles{createErr: sentinel}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             &fakeUsers{},
		Roles:             roles,
		Logger:            discardLogger(),
	})
	var wantErr *RolesRegistrationError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *RolesRegistrationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

func TestInitialize_AdminUserCreationErrorWrapped(t *testing.T) {
	sentinel := errors.New("not leader")
	users := &fakeUsers{createErr: sentinel}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             users,
		Roles:             &fakeRoles{},
		Logger:            discardLogger(),
	})
	var wantErr *AdminUserCreationError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *AdminUserCreationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

func TestInitialize_AdminCertGenerationErrorWrapped(t *testing.T) {
	sentinel := errors.New("CA sealed")
	ca := &fakeCA{err: sentinel}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             &fakeUsers{},
		Roles:             &fakeRoles{},
		CA:                ca,
		Logger:            discardLogger(),
	})
	var wantErr *AdminCertGenerationError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *AdminCertGenerationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// ---------------------------------------------------------------------------
// CA-optional path
// ---------------------------------------------------------------------------

func TestInitialize_NoCAProvidedSkipsCertGeneration(t *testing.T) {
	tenants := &fakeTenants{}
	users := &fakeUsers{}
	roles := &fakeRoles{}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.AdminCertPEM != nil || result.AdminKeyPEM != nil {
		t.Errorf("AdminCertPEM/Key set without a CA; want nil")
	}
}

// ---------------------------------------------------------------------------
// Partial-state idempotency: tenant present but admin user missing
// ---------------------------------------------------------------------------

func TestInitialize_TenantExistsAdminMissingCreatesAdmin(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	users := &fakeUsers{}
	roles := &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO:       true,
		role.RoleNameAdmin:    true,
		role.RoleNameOperator: true,
		role.RoleNameUser:     true,
		role.RoleNameAuditor:  true,
		role.RoleNameOwner:    true,
	}}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenants,
		Users:             users,
		Roles:             roles,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.SystemTenantCreated {
		t.Errorf("SystemTenantCreated = true, want false")
	}
	if result.RolesCreated {
		t.Errorf("RolesCreated = true, want false")
	}
	if !result.AdminCreated {
		t.Errorf("AdminCreated = false, want true (admin user must be created in this run)")
	}
}

// ---------------------------------------------------------------------------
// Integration: real tenant servicer accepts the system tenant id
//
// The reserved-id rejection in pkg/tenant rejects "system" for external
// callers. firstboot.Initialize must propagate WithInternalCall(ctx) so
// that the CreateTenant call succeeds. This test wires up a real
// TenantServicer backed by an in-memory KV store and confirms the
// system tenant id is accepted (no ReservedTenantIDError).
// ---------------------------------------------------------------------------

func TestInitialize_RealTenantServicerAcceptsSystemTenantID(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	tenantSvc, err := tenant.NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	users := &fakeUsers{}
	roles := &fakeRoles{}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenantSvc,
		Users:             users,
		Roles:             roles,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v (would fail with ReservedTenantIDError if WithInternalCall not propagated)", err)
	}
	if !result.SystemTenantCreated {
		t.Errorf("SystemTenantCreated = false, want true")
	}

	// Round-trip: the real servicer should now expose the system tenant.
	got, err := tenantSvc.GetTenant(context.Background(), tenantid.System)
	if err != nil {
		t.Fatalf("GetTenant(%q): %v", tenantid.System, err)
	}
	if got.ID != tenantid.System {
		t.Errorf("got.ID = %q, want %q", got.ID, tenantid.System)
	}
}

// TestInitialize_ReconcilesPreExistingAdminToSystem is the go-qrdb#106
// review's Finding 1, closed against the REAL store rather than a double:
// an admin created by a build that predates the System flag (or one whose
// flag did not persist for any other reason) must be healed to System: true
// the next time Initialize runs, not left permanently unprotected because
// CreateUser's own already-exists response carries no information about
// which flags the existing record holds.
func TestInitialize_ReconcilesPreExistingAdminToSystem(t *testing.T) {
	kv := newGuardedMemoryKVStore()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(kv, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	tenantSvc, err := tenant.NewKVTenantService(kv, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	// Simulate a cluster whose admin was minted before go-qrdb#106 existed:
	// same username and tenant createAdminUser itself would use, but with no
	// System flag -- bypassing Initialize entirely for this seed, exactly as
	// an older release's own first-boot would have.
	pre, err := userSvc.CreateUser(context.Background(), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             adminUsername,
		TenantID:             tenantid.System,
		RoleIDs:              []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)},
	})
	if err != nil {
		t.Fatalf("seed CreateUser: %v", err)
	}
	if pre.System {
		t.Fatal("test setup: the seeded admin must not already be System")
	}

	roles := &fakeRoles{}
	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           tenantSvc,
		Users:             userSvc,
		Roles:             roles,
		Logger:            discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.AdminCreated {
		t.Error("AdminCreated = true, want false: the admin already existed")
	}

	got, gerr := userSvc.GetUserByUsername(context.Background(), tenantid.System, adminUsername)
	if gerr != nil {
		t.Fatalf("GetUserByUsername: %v", gerr)
	}
	if !got.System {
		t.Fatal("the pre-existing admin was not promoted to System")
	}
	if got.ID != pre.ID {
		t.Fatalf("promoted a different record: got ID %d, want the seeded admin's %d", got.ID, pre.ID)
	}

	// DeleteUser must now actually refuse it -- the point of the whole
	// reconciliation.
	var immutable *user.SystemUserImmutableError
	if derr := userSvc.DeleteUser(context.Background(), got.ID); !errors.As(derr, &immutable) {
		t.Fatalf("DeleteUser after reconciliation: err = %v, want *SystemUserImmutableError", derr)
	}
}

// TestInitialize_ReconciliationIsIdempotent asserts a SECOND Initialize call
// against an already-reconciled admin is a no-op success, not a repeated
// promotion attempt that trips the uniqueness sentinel against itself.
func TestInitialize_ReconciliationIsIdempotent(t *testing.T) {
	kv := newGuardedMemoryKVStore()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(kv, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	tenantSvc, err := tenant.NewKVTenantService(kv, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	deps := Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Tenants: tenantSvc, Users: userSvc, Roles: &fakeRoles{}, Logger: discardLogger()}

	if _, err := Initialize(context.Background(), deps); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}
	firstResult, err := userSvc.GetUserByUsername(context.Background(), tenantid.System, adminUsername)
	if err != nil {
		t.Fatalf("GetUserByUsername after first Initialize: %v", err)
	}
	if !firstResult.System {
		t.Fatal("fresh admin was not created with System: true")
	}

	if _, err := Initialize(context.Background(), deps); err != nil {
		t.Fatalf("second Initialize: %v", err)
	}
	secondResult, err := userSvc.GetUserByUsername(context.Background(), tenantid.System, adminUsername)
	if err != nil {
		t.Fatalf("GetUserByUsername after second Initialize: %v", err)
	}
	if !secondResult.System || secondResult.ID != firstResult.ID {
		t.Fatalf("second Initialize changed the record: got %+v, want unchanged %+v", secondResult, firstResult)
	}
}

// ---------------------------------------------------------------------------
// B6: create-but-never-bound crash recovery (go-qrdb#548 review)
// ---------------------------------------------------------------------------

// seedUnboundAdmin creates the system-tenant admin directly through userSvc,
// bypassing Initialize entirely -- simulating createAdminUser's OWN write
// having already committed on an earlier run, and deps.Binder.BindAdminCert
// never running (the process crashed, or the call failed, between the two).
// System: true matches what createAdminUser itself sets, so the record this
// test seeds is indistinguishable from one Initialize created.
func seedUnboundAdmin(t *testing.T, userSvc user.UserServicer) *user.User {
	t.Helper()
	admin, err := userSvc.CreateUser(context.Background(), &user.CreateUserRequest{
		WithoutEmailLoginKey: true,
		Username:             adminUsername,
		TenantID:             tenantid.System,
		RoleIDs:              []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)},
		System:               true,
	})
	if err != nil {
		t.Fatalf("seed CreateUser: %v", err)
	}
	if len(admin.CertBindings) != 0 {
		t.Fatal("test setup: the seeded admin must start with no certificate binding")
	}
	return admin
}

// TestInitialize_RepairsCreateButNeverBoundAdmin is the B6 crash test: an
// admin that already exists (System: true, exactly as createAdminUser left
// it) but holds no certificate binding -- the state a crash between
// createAdminUser's write and deps.Binder.BindAdminCert's write leaves --
// is repaired by a SUBSEQUENT Initialize call that has a credential path
// and a real Binder, rather than being skipped forever because the admin
// "already existed". The admin converges to fully bound without a second
// account ever being created, the same way the ceremony's own
// admin-credential issuance converges on a retry after a crash
// (ReissueAdminCredential) -- this is firstboot's side of that same
// guarantee.
func TestInitialize_RepairsCreateButNeverBoundAdmin(t *testing.T) {
	kv := newGuardedMemoryKVStore()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(kv, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	tenantSvc, err := tenant.NewKVTenantService(kv, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	seeded := seedUnboundAdmin(t, userSvc)

	certPEM := realCertPEM(t)
	ca := &fakeCA{cert: certPEM, key: []byte("KEY")}
	binder := UserCertBinder{Users: userSvc}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingRequired,
		Tenants:           tenantSvc, Users: userSvc, Roles: &fakeRoles{},
		CA:     ca,
		Binder: binder,
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize must repair the unbound admin, not refuse: %v", err)
	}
	if result.AdminCreated {
		t.Error("AdminCreated = true, want false: the admin already existed")
	}
	if ca.called.Load() != 1 {
		t.Fatalf("CA.GenerateAdminCert calls = %d, want 1", ca.called.Load())
	}
	if result.AdminCertThumbprint == "" {
		t.Fatal("result carries no AdminCertThumbprint after the repair")
	}

	got, gerr := userSvc.GetUserByUsername(context.Background(), tenantid.System, adminUsername)
	if gerr != nil {
		t.Fatalf("GetUserByUsername: %v", gerr)
	}
	if got.ID != seeded.ID {
		t.Fatalf("repaired a different record: got ID %d, want the seeded admin's %d", got.ID, seeded.ID)
	}
	if len(got.CertBindings) == 0 {
		t.Fatal("the seeded admin still has no certificate binding after Initialize")
	}

	// The SAME check Initialize's own pre-flight (checkBindingPosture) and
	// post-flight (verifyRequiredOutcome) apply must now report this admin
	// as able to log in under "required".
	if verr := VerifyAdminCertBinding(context.Background(), userSvc, time.Now().UTC()); verr != nil {
		t.Fatalf("VerifyAdminCertBinding after repair: %v", verr)
	}
}

// TestInitialize_RepairsCreateButNeverBoundAdmin_Idempotent proves a SECOND
// repair call, after the first one already bound the admin, is a no-op
// success -- the admin is not reissued or rebound a second time.
func TestInitialize_RepairsCreateButNeverBoundAdmin_Idempotent(t *testing.T) {
	kv := newGuardedMemoryKVStore()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(kv, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	tenantSvc, err := tenant.NewKVTenantService(kv, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	seedUnboundAdmin(t, userSvc)

	ca := &fakeCA{cert: realCertPEM(t), key: []byte("KEY")}
	deps := Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingRequired,
		Tenants:           tenantSvc, Users: userSvc, Roles: &fakeRoles{},
		CA:     ca,
		Binder: UserCertBinder{Users: userSvc},
		Logger: discardLogger(),
	}

	if _, err := Initialize(context.Background(), deps); err != nil {
		t.Fatalf("first (repair) Initialize: %v", err)
	}
	if ca.called.Load() != 1 {
		t.Fatalf("after the repair, CA.GenerateAdminCert calls = %d, want 1", ca.called.Load())
	}

	if _, err := Initialize(context.Background(), deps); err != nil {
		t.Fatalf("second Initialize after the repair: %v", err)
	}
	if ca.called.Load() != 1 {
		t.Fatalf("CA.GenerateAdminCert calls after a second call = %d, want still 1: an already-bound admin must not be reissued", ca.called.Load())
	}
}

// TestInitialize_UnboundAdminWithNoCredentialPathBootsUnbound guards the
// other half of B6, under the go-qrdb#560 review B2 ruling: an admin that
// exists with no live binding and NO credential path at all (no CA, no
// Signer+CSR, no AdminCertPEM) is NOT refused under "required" -- there was
// nothing this run could have bound, so it proceeds and leaves the admin
// unbound for the composer's AwaitingAdminCredentialProbe to gate, rather
// than refusing to boot at all. The repair test above still covers the case
// a credential path IS wired.
func TestInitialize_UnboundAdminWithNoCredentialPathBootsUnbound(t *testing.T) {
	kv := newGuardedMemoryKVStore()
	idgen, err := dao.NewSnowflakeGenerator(1)
	if err != nil {
		t.Fatalf("NewSnowflakeGenerator: %v", err)
	}
	userSvc, err := user.NewStore(kv, idgen)
	if err != nil {
		t.Fatalf("user.NewStore: %v", err)
	}
	tenantSvc, err := tenant.NewKVTenantService(kv, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}
	seedUnboundAdmin(t, userSvc)

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingRequired,
		Tenants:           tenantSvc, Users: userSvc, Roles: &fakeRoles{},
		Binder: UserCertBinder{Users: userSvc},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize must boot unbound, not refuse: %v", err)
	}
	if result.AdminCreated {
		t.Error("AdminCreated = true, want false: the admin already existed")
	}
	if verifyErr := VerifyAdminCertBinding(context.Background(), userSvc, time.Now().UTC()); verifyErr == nil {
		t.Fatal("admin reads as bound; the fixture seeded it unbound")
	}
}

// ---------------------------------------------------------------------------
// Error type string methods
// ---------------------------------------------------------------------------

// TestErrorMessages exercises the Error() method on every typed error in the
// package. This ensures the error message strings are correct and drives
// coverage of all the zero-coverage Error() methods reported by the coverage
// tool.
func TestErrorMessages(t *testing.T) {
	cause := errors.New("underlying cause")

	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "MissingTenantsError",
			err:  &MissingTenantsError{},
			want: "firstboot: missing tenant servicer",
		},
		{
			name: "MissingUsersError",
			err:  &MissingUsersError{},
			want: "firstboot: missing user servicer",
		},
		{
			name: "MissingRolesError",
			err:  &MissingRolesError{},
			want: "firstboot: missing role servicer",
		},
		{
			name: "MissingLoggerError",
			err:  &MissingLoggerError{},
			want: "firstboot: missing logger",
		},
		{
			name: "SystemTenantCreationError",
			err:  &SystemTenantCreationError{Cause: cause},
			want: "firstboot: system tenant creation failed: underlying cause",
		},
		{
			name: "RolesRegistrationError",
			err:  &RolesRegistrationError{Role: "Admin", Cause: cause},
			want: "firstboot: register role Admin failed: underlying cause",
		},
		{
			name: "AdminUserCreationError",
			err:  &AdminUserCreationError{Cause: cause},
			want: "firstboot: admin user creation failed: underlying cause",
		},
		{
			name: "AdminCertGenerationError",
			err:  &AdminCertGenerationError{Cause: cause},
			want: "firstboot: admin cert generation failed: underlying cause",
		},
		{
			name: "AdminCertBindError",
			err:  &AdminCertBindError{Cause: cause},
			want: "firstboot: admin cert binding failed: underlying cause",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAdminCertBindErrorUnwrap verifies AdminCertBindError.Unwrap() exposes
// the underlying cause so errors.Is / errors.As work correctly.
func TestAdminCertBindErrorUnwrap(t *testing.T) {
	sentinel := errors.New("bind backend down")
	wrapped := &AdminCertBindError{Cause: sentinel}
	if !errors.Is(wrapped, sentinel) {
		t.Errorf("errors.Is(wrapped, sentinel) = false; Unwrap must expose the cause")
	}
	if wrapped.Unwrap() != sentinel {
		t.Errorf("Unwrap() = %v, want sentinel", wrapped.Unwrap())
	}
}

// ---------------------------------------------------------------------------
// BindAdminCert error path
// ---------------------------------------------------------------------------

// TestInitialize_BindAdminCertErrorWrapped verifies that when the CertBinder
// returns an error Initialize propagates it as *AdminCertBindError.
func TestInitialize_BindAdminCertErrorWrapped(t *testing.T) {
	certPEM := realCertPEM(t)
	sentinel := errors.New("bind write failed")
	binder := &fakeBinder{err: sentinel}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             &fakeUsers{},
		Roles:             &fakeRoles{},
		CA:                &fakeCA{cert: certPEM, key: []byte("KEY")},
		Binder:            binder,
		Logger:            discardLogger(),
	})
	var wantErr *AdminCertBindError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *AdminCertBindError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// ---------------------------------------------------------------------------
// spkiThumbprintFromCertPEM edge cases
// ---------------------------------------------------------------------------

// TestSpkiThumbprintFromCertPEM_NoPEMBlock verifies that passing a byte slice
// with no PEM block returns a non-nil error (no panic, no silent zero value).
func TestSpkiThumbprintFromCertPEM_NoPEMBlock(t *testing.T) {
	_, err := spkiThumbprintFromCertPEM([]byte("not a pem block at all"))
	if err == nil {
		t.Fatal("expected error for non-PEM input, got nil")
	}
}

// TestSpkiThumbprintFromCertPEM_InvalidDER verifies that a PEM block whose
// body contains invalid DER bytes returns an x509.ParseCertificate error.
func TestSpkiThumbprintFromCertPEM_InvalidDER(t *testing.T) {
	invalidDERPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("these are not valid DER bytes"),
	})
	_, err := spkiThumbprintFromCertPEM(invalidDERPEM)
	if err == nil {
		t.Fatal("expected error for invalid DER, got nil")
	}
}

// TestSpkiThumbprintFromCertPEM_ValidCert verifies the happy path: a valid
// self-signed cert PEM yields a non-empty thumbprint.
func TestSpkiThumbprintFromCertPEM_ValidCert(t *testing.T) {
	certPEM := realCertPEM(t)
	thumbprint, err := spkiThumbprintFromCertPEM(certPEM)
	if err != nil {
		t.Fatalf("spkiThumbprintFromCertPEM: %v", err)
	}
	if thumbprint == "" {
		t.Error("thumbprint is empty, want non-empty SPKI hash")
	}
}

// ---------------------------------------------------------------------------
// Path B: CSR-sign error
// ---------------------------------------------------------------------------

// TestInitialize_PathB_SignerError verifies that when the CertSigner returns an
// error on the CSR-sign path Initialize propagates it as *AdminCertGenerationError.
func TestInitialize_PathB_SignerError(t *testing.T) {
	sentinel := errors.New("HSM not reachable")
	signer := &fakeSigner{err: sentinel}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             &fakeUsers{},
		Roles:             &fakeRoles{},
		Signer:            signer,
		AdminCSRPEM:       []byte("MY-CSR"),
		Logger:            discardLogger(),
	})
	var wantErr *AdminCertGenerationError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *AdminCertGenerationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// TestInitialize_SpkiThumbprintError verifies that when the CA returns a valid
// PEM that cannot be parsed as a certificate, Initialize wraps the error as
// *AdminCertGenerationError (the spkiThumbprintFromCertPEM failure path inside
// Initialize).
func TestInitialize_SpkiThumbprintError(t *testing.T) {
	// Return a PEM block with non-CERTIFICATE type or garbage bytes so that
	// x509.ParseCertificate fails after the pem.Decode succeeds.
	invalidDERPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("garbage DER that will not parse"),
	})
	ca := &fakeCA{cert: invalidDERPEM, key: []byte("KEY")}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{},
		Users:             &fakeUsers{},
		Roles:             &fakeRoles{},
		CA:                ca,
		Logger:            discardLogger(),
	})
	var wantErr *AdminCertGenerationError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *AdminCertGenerationError (from thumbprint parse failure)", err)
	}
}

// ---------------------------------------------------------------------------
// Path C — the admin presents a certificate they already hold
// ---------------------------------------------------------------------------

// issueCertFromCA mints a leaf signed by a freshly generated CA and returns the
// leaf PEM together with a pool containing that CA.
//
// A self-signed certificate cannot exercise Path C honestly: it chains to
// itself, so putting it in the pool would make the verification step pass for
// any input. A real issuer/leaf split is what distinguishes "this certificate
// chains to something we trust" from "this certificate exists".
func issueCertFromCA(t *testing.T, notBefore, notAfter time.Time) ([]byte, *x509.CertPool) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA GenerateKey: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(100),
		Subject:               pkix.Name{CommonName: "Presenting Org Root"},
		NotBefore:             time.Now().Add(-2 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate(CA): %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("ParseCertificate(CA): %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf GenerateKey: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(101),
		Subject:      pkix.Name{CommonName: "admin-on-a-piv-card"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate(leaf): %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), pool
}

// TestInitialize_PathC_AdoptsPresentedCert covers the enrolment route that lets
// an administrator authenticate with a credential they already hold — a
// certificate resident on a PIV card or a security key — instead of one this
// deployment mints for them.
//
// The properties that matter: the presented certificate is what gets bound, no
// private key is produced (it never left the operator's token), and the CA is
// not consulted even though one is configured. That last point is the reason
// Path C is tested before the others in issueAdminCert -- falling through to a
// configured CA would mint a second, unrelated credential and bind that instead,
// leaving the operator holding a certificate the server has never heard of.
func TestInitialize_PathC_AdoptsPresentedCert(t *testing.T) {
	presented, pool := issueCertFromCA(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	binder := &fakeBinder{}
	ca := &fakeCA{cert: realCertPEM(t), key: []byte("CA-KEY")}

	result, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		CA:            ca, // configured, and must NOT be used
		AdminCertPEM:  presented,
		AdminCertPool: pool,
		Binder:        binder,
		Logger:        discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if !bytes.Equal(result.AdminCertPEM, presented) {
		t.Fatal("the certificate the admin presented must be the one adopted")
	}
	if result.AdminKeyPEM != nil {
		t.Fatal("Path C must not produce a private key: the admin's key never leaves their token")
	}
	if n := ca.called.Load(); n != 0 {
		t.Fatalf("CA was called %d times; a presented certificate must not trigger issuance", n)
	}
	if binder.calls != 1 || binder.thumbprint != result.AdminCertThumbprint || binder.thumbprint == "" {
		t.Fatalf("presented cert thumbprint was not bound: calls=%d bound=%q result=%q",
			binder.calls, binder.thumbprint, result.AdminCertThumbprint)
	}
}

// TestInitialize_PathC_RejectsUntrustedCert is the counterweight: adoption must
// be a trust decision, not a formality. Without it, Path C would bind any
// syntactically valid certificate an attacker could generate for themselves.
func TestInitialize_PathC_RejectsUntrustedCert(t *testing.T) {
	presented, _ := issueCertFromCA(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	_, unrelatedPool := issueCertFromCA(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	binder := &fakeBinder{}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		AdminCertPEM:  presented,
		AdminCertPool: unrelatedPool, // does not contain the presenting CA
		Binder:        binder,
		Logger:        discardLogger(),
	})

	var rejected *AdminCertRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want AdminCertRejectedError for a cert that chains to nothing trusted", err)
	}
	if binder.calls != 0 {
		t.Fatal("a rejected certificate must never be bound")
	}
}

// TestInitialize_PathC_RejectsExpiredCert pins the validity-window check. A card
// outlives the certificates on it, so an operator presenting a stale one is an
// ordinary mistake rather than an attack — and it must still be refused, since
// binding it would enrol an identity that cannot authenticate.
func TestInitialize_PathC_RejectsExpiredCert(t *testing.T) {
	presented, pool := issueCertFromCA(t, time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))
	binder := &fakeBinder{}

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		AdminCertPEM:  presented,
		AdminCertPool: pool,
		Binder:        binder,
		Logger:        discardLogger(),
	})

	var rejected *AdminCertRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("err = %v, want AdminCertRejectedError for an expired certificate", err)
	}
	if binder.calls != 0 {
		t.Fatal("an expired certificate must never be bound")
	}
}

// TestInitialize_PathC_RequiresPool refuses a presented certificate when no
// trust anchors were configured, rather than treating an absent pool as
// "trust anything" -- the failure mode that would make the check above
// meaningless.
func TestInitialize_PathC_RequiresPool(t *testing.T) {
	presented, _ := issueCertFromCA(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		AdminCertPEM: presented,
		Logger:       discardLogger(),
	})

	var missing *MissingAdminCertPoolError
	if !errors.As(err, &missing) {
		t.Fatalf("err = %v, want MissingAdminCertPoolError", err)
	}
}

// TestInitialize_RejectsConflictingAdminCertInputs refuses a CSR and a
// certificate together.
//
// Either choice would be wrong in a way the operator could not see: signing the
// CSR leaves the presented certificate unbound, so the card they intended to
// authenticate with does not work; adopting the certificate silently discards a
// CSR they generated a key for.
func TestInitialize_RejectsConflictingAdminCertInputs(t *testing.T) {
	presented, pool := issueCertFromCA(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))

	_, err := Initialize(context.Background(), Dependencies{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Tenants:           &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer:        &fakeSigner{cert: realCertPEM(t)},
		AdminCSRPEM:   []byte("THE-CSR"),
		AdminCertPEM:  presented,
		AdminCertPool: pool,
		Logger:        discardLogger(),
	})

	var conflict *ConflictingAdminCertInputsError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want ConflictingAdminCertInputsError", err)
	}
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (f *fakeRoles) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}

