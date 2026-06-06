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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
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

func (f *fakeTenants) CreateTenant(_ context.Context, id, name string, isolationType tenant.IsolationType) (*tenant.TenantInfo, error) {
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
func (f *fakeTenants) TenantBarrierInit(_ context.Context, _, _ string, _, _ int) error { return nil }
func (f *fakeTenants) TenantBarrierInitWithStrategy(_ context.Context, _, _ string, _, _ int, _ seal.StrategyID) error {
	return nil
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
	nextID      uint64
}

func (f *fakeUsers) CreateUser(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
	f.createCalls = append(f.createCalls, *req)
	if f.exists {
		return nil, &user.UsernameExistsError{Username: req.Username, TenantID: req.TenantID}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.nextID++
	return &user.User{ID: f.nextID, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs}, nil
}

func (f *fakeUsers) GetUser(_ context.Context, _ uint64) (*user.User, error) { return nil, nil }
func (f *fakeUsers) GetUserByUsername(_ context.Context, _ string, _ string) (*user.User, error) {
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

// fakeRoles satisfies role.RoleServicer.
type fakeRoles struct {
	createCount  atomic.Int32
	createErr    error
	existsByName map[string]bool
	createCalls  []role.CreateRoleRequest
}

func (f *fakeRoles) CreateRole(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	f.createCalls = append(f.createCalls, *req)
	if f.existsByName[req.Name] {
		return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.createCount.Add(1)
	return &role.Role{ID: req.ID, Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions}, nil
}

func (f *fakeRoles) GetRole(_ context.Context, _ string) (*role.Role, error) { return nil, nil }
func (f *fakeRoles) GetRoleByName(_ context.Context, _, _ string) (*role.Role, error) {
	return nil, nil
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
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
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
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
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
		Tenants: tenants,
		Users:   users,
		Roles:   roles,
		CA:      ca,
		Logger:  discardLogger(),
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
	if tc.id != tenant.SystemTenantID {
		t.Errorf("CreateTenant id = %q, want %q", tc.id, tenant.SystemTenantID)
	}
	if tc.isolationType != tenant.IsolationPrefix {
		t.Errorf("CreateTenant isolation = %v, want IsolationPrefix (system tenant must be prefix)", tc.isolationType)
	}

	// Standard posture (default): the six canonical roles plus the Owner
	// convenience role.
	wantRoles := len(role.BuiltInRoles(tenant.SystemTenantID)) + 1
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
	if adminReq.TenantID != tenant.SystemTenantID {
		t.Errorf("admin TenantID = %q, want %q", adminReq.TenantID, tenant.SystemTenantID)
	}
	// Standard posture: admin holds Admin@system AND Owner@system.
	wantAdminRoleID := role.SystemRoleID(tenant.SystemTenantID, role.RoleNameAdmin)
	wantOwnerRoleID := role.SystemRoleID(tenant.SystemTenantID, role.RoleNameOwner)
	if len(adminReq.RoleIDs) != 2 || adminReq.RoleIDs[0] != wantAdminRoleID || adminReq.RoleIDs[1] != wantOwnerRoleID {
		t.Errorf("admin RoleIDs = %v, want [%q %q]", adminReq.RoleIDs, wantAdminRoleID, wantOwnerRoleID)
	}

	if ca.called.Load() != 1 {
		t.Errorf("CA called = %d, want 1", ca.called.Load())
	}
	if ca.lastID != tenant.SystemTenantID || ca.lastSubj != "admin" {
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
		Tenants:            tenants,
		Users:              users,
		Roles:              roles,
		Logger:             discardLogger(),
		SeparationOfDuties: true,
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	wantRoles := len(role.BuiltInRoles(tenant.SystemTenantID))
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
	wantAdminRoleID := role.SystemRoleID(tenant.SystemTenantID, role.RoleNameAdmin)
	adminReq := users.createCalls[0]
	if len(adminReq.RoleIDs) != 1 || adminReq.RoleIDs[0] != wantAdminRoleID {
		t.Errorf("admin RoleIDs = %v, want [%q] (no owner under strict)", adminReq.RoleIDs, wantAdminRoleID)
	}
}

// ---------------------------------------------------------------------------
// Idempotency
// ---------------------------------------------------------------------------

func TestInitialize_AllPresentReturnsIdempotent(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	users := &fakeUsers{exists: true}
	roles := &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO:        true,
		role.RoleNameAdmin:     true,
		role.RoleNameOperator:  true,
		role.RoleNameUser:      true,
		role.RoleNameAuditor:   true,
		role.RoleNameCustodian: true,
		role.RoleNameOwner:     true,
	}}
	ca := &fakeCA{cert: []byte("CERT"), key: []byte("KEY")}

	result, err := Initialize(context.Background(), Dependencies{
		Tenants: tenants,
		Users:   users,
		Roles:   roles,
		CA:      ca,
		Logger:  discardLogger(),
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

// ---------------------------------------------------------------------------
// Missing dependencies
// ---------------------------------------------------------------------------

func TestInitialize_MissingTenantsReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		Users:  &fakeUsers{},
		Roles:  &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *MissingTenantsError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingTenantsError", err)
	}
}

func TestInitialize_MissingUsersReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{},
		Roles:   &fakeRoles{},
		Logger:  discardLogger(),
	})
	var want *MissingUsersError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingUsersError", err)
	}
}

func TestInitialize_MissingRolesReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{},
		Users:   &fakeUsers{},
		Logger:  discardLogger(),
	})
	var want *MissingRolesError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingRolesError", err)
	}
}

func TestInitialize_MissingLoggerReturnsTypedError(t *testing.T) {
	_, err := Initialize(context.Background(), Dependencies{
		Tenants: &fakeTenants{},
		Users:   &fakeUsers{},
		Roles:   &fakeRoles{},
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
		Tenants: tenants,
		Users:   &fakeUsers{},
		Roles:   &fakeRoles{},
		Logger:  discardLogger(),
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
		Tenants: &fakeTenants{},
		Users:   &fakeUsers{},
		Roles:   roles,
		Logger:  discardLogger(),
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
		Tenants: &fakeTenants{},
		Users:   users,
		Roles:   &fakeRoles{},
		Logger:  discardLogger(),
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
		Tenants: &fakeTenants{},
		Users:   &fakeUsers{},
		Roles:   &fakeRoles{},
		CA:      ca,
		Logger:  discardLogger(),
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
		Tenants: tenants,
		Users:   users,
		Roles:   roles,
		Logger:  discardLogger(),
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
		role.RoleNameSO:        true,
		role.RoleNameAdmin:     true,
		role.RoleNameOperator:  true,
		role.RoleNameUser:      true,
		role.RoleNameAuditor:   true,
		role.RoleNameCustodian: true,
		role.RoleNameOwner:     true,
	}}

	result, err := Initialize(context.Background(), Dependencies{
		Tenants: tenants,
		Users:   users,
		Roles:   roles,
		Logger:  discardLogger(),
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
	store := newMemKVStore()
	tenantSvc, err := tenant.NewKVTenantService(store, slog.Default())
	if err != nil {
		t.Fatalf("NewKVTenantService: %v", err)
	}

	users := &fakeUsers{}
	roles := &fakeRoles{}

	result, err := Initialize(context.Background(), Dependencies{
		Tenants: tenantSvc,
		Users:   users,
		Roles:   roles,
		Logger:  discardLogger(),
	})
	if err != nil {
		t.Fatalf("Initialize: %v (would fail with ReservedTenantIDError if WithInternalCall not propagated)", err)
	}
	if !result.SystemTenantCreated {
		t.Errorf("SystemTenantCreated = false, want true")
	}

	// Round-trip: the real servicer should now expose the system tenant.
	got, err := tenantSvc.GetTenant(context.Background(), tenant.SystemTenantID)
	if err != nil {
		t.Fatalf("GetTenant(%q): %v", tenant.SystemTenantID, err)
	}
	if got.ID != tenant.SystemTenantID {
		t.Errorf("got.ID = %q, want %q", got.ID, tenant.SystemTenantID)
	}
}

// ---------------------------------------------------------------------------
// Integration helper: in-memory kvstore.KVStore
// ---------------------------------------------------------------------------

type memKVStore struct {
	data map[string][]byte
}

func newMemKVStore() *memKVStore { return &memKVStore{data: make(map[string][]byte)} }

func (m *memKVStore) Put(_ context.Context, key string, value []byte) error {
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	return nil
}

func (m *memKVStore) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, nil
}

func (m *memKVStore) Delete(_ context.Context, key string) error { delete(m.data, key); return nil }

func (m *memKVStore) List(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *memKVStore) Exists(_ context.Context, key string) (bool, error) {
	_, ok := m.data[key]
	return ok, nil
}

func (m *memKVStore) Scan(_ context.Context, prefix string, fn func(string, []byte) error) error {
	for k, v := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *memKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (m *memKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (m *memKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *memKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (m *memKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// Compile-time interface assertion.
var _ kvstore.KVStore = (*memKVStore)(nil)

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
		Tenants: &fakeTenants{},
		Users:   &fakeUsers{},
		Roles:   &fakeRoles{},
		CA:      &fakeCA{cert: certPEM, key: []byte("KEY")},
		Binder:  binder,
		Logger:  discardLogger(),
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
		Tenants:     &fakeTenants{},
		Users:       &fakeUsers{},
		Roles:       &fakeRoles{},
		Signer:      signer,
		AdminCSRPEM: []byte("MY-CSR"),
		Logger:      discardLogger(),
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
		Tenants: &fakeTenants{},
		Users:   &fakeUsers{},
		Roles:   &fakeRoles{},
		CA:      ca,
		Logger:  discardLogger(),
	})
	var wantErr *AdminCertGenerationError
	if !errors.As(err, &wantErr) {
		t.Fatalf("err = %v, want *AdminCertGenerationError (from thumbprint parse failure)", err)
	}
}
