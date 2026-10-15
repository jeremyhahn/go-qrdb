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

package bootstrap

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

	"github.com/jeremyhahn/go-enclave"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/cert/certtest"
	"github.com/jeremyhahn/go-qrdb/pkg/firstboot"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/seal/sealtest"
	"github.com/jeremyhahn/go-qrdb/pkg/secure"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantadmin"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// passphraseBuffer builds the *secure.Buffer SystemConfig.Passphrase takes.
//
// The buffer is destroyed by the test, not by BootstrapSystem: the caller
// owns what it allocated, and a bootstrap that destroyed a caller's buffer
// would be a surprise for the one production caller that reuses it.
func passphraseBuffer(t *testing.T, pp string) *secure.Buffer {
	t.Helper()
	buf, err := secure.NewBufferFromBytes([]byte(pp))
	if err != nil {
		t.Fatalf("secure.NewBufferFromBytes: %v", err)
	}
	t.Cleanup(buf.Destroy)
	return buf
}

// validAdminCSR returns a syntactically and cryptographically valid CSR PEM
// -- planAdmin now parses and signature-checks the CSR (cert.ParseCSR)
// before anything is written, so every cert-mode ProvisionTenant test needs
// a real one rather than a placeholder string.
func validAdminCSR(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := certtest.NewCSR("irrelevant-csr-subject")
	if err != nil {
		t.Fatalf("certtest.NewCSR: %v", err)
	}
	return csrPEM
}

// realCertPEM generates a valid self-signed certificate PEM for cert-binding
// tests.
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

// newRegistry builds a BarrierRegistry whose system barrier is sealed with the
// supplied strategies (already constructed, sealed, ready for Initialize).
func newRegistry(t *testing.T, strategies ...seal.SealingStrategy) *seal.BarrierRegistry {
	t.Helper()
	system, err := seal.NewStorageBarrier(discardLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, strategies...)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(discardLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg
}

// newPooledRegistry builds a BarrierRegistry with a durable local volume
// pool (via sealtest.PooledRegistry: a real "dir"-provider volume plus an
// in-memory replicated catalog) and a per-tenant Pebble registry. Used to
// prove InitializeTenantBarrier registers tenants against a DURABLE
// backend (not seal.NewMemoryBackend()) when the registry is configured
// for it.
func newPooledRegistry(t *testing.T) (*seal.BarrierRegistry, pebblestore.PerTenantRegistry) {
	t.Helper()

	system, err := seal.NewStorageBarrier(discardLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, seal.NewNoneStrategy())
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}

	reg, _, _ := sealtest.PooledRegistry(t, system)
	perTenantReg := reg.PerTenantPebbleRegistry()
	return reg, perTenantReg
}

// ---------------------------------------------------------------------------
// In-memory store doubles (mirror pkg/firstboot/firstboot_test.go)
// ---------------------------------------------------------------------------

type fakeTenants struct {
	createCalls      []createTenantCall
	createErr        error
	createAlreadyErr bool
	deleteCalls      []string
	deleteErr        error

	// autoUnseal makes CreateTenant's returned *tenant.TenantInfo report
	// BarrierAutoUnseal: true, as the real TenantService does when the
	// caller's CreateOptions included tenant.WithBarrierAutoUnseal(true) --
	// this double ignores opts entirely, so a test that needs the
	// auto-unseal path (ProvisionTenant reporting the admin completion due)
	// sets this field directly instead.
	autoUnseal bool
}

// DeleteTenant implements tenantDeleter so an Exclusive ProvisionTenant's
// undo stack can be exercised against this double.
func (f *fakeTenants) DeleteTenant(_ context.Context, id string) error {
	f.deleteCalls = append(f.deleteCalls, id)
	return f.deleteErr
}

type createTenantCall struct {
	id            string
	name          string
	isolationType tenant.IsolationType
}

func (f *fakeTenants) CreateTenant(_ context.Context, id, name string, isolationType tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	f.createCalls = append(f.createCalls, createTenantCall{id: id, name: name, isolationType: isolationType})
	if f.createAlreadyErr {
		return nil, tenant.ErrTenantAlreadyExists
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: isolationType, BarrierAutoUnseal: f.autoUnseal}, nil
}

type fakeUsers struct {
	createCalls []user.CreateUserRequest
	createErr   error
	exists      bool
	nextID      uint64
	deleteCalls []uint64
	deleteErr   error
}

// DeleteUser implements userDeleter so an Exclusive ProvisionTenant's undo
// stack can be exercised against this double.
func (f *fakeUsers) DeleteUser(_ context.Context, id uint64) error {
	f.deleteCalls = append(f.deleteCalls, id)
	return f.deleteErr
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
	// Resolve the initial state the way the real store does, so a record the
	// double returns carries the lifecycle a real create would have written.
	lc := req.Lifecycle
	switch {
	case lc != "":
	case req.Pending:
		lc = user.LifecyclePending
	default:
		lc = user.LifecycleActive
	}
	return &user.User{ID: f.nextID, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs, Lifecycle: lc}, nil
}

type fakeRoles struct {
	createCount  atomic.Int32
	createErr    error
	existsByName map[string]bool
	// existsByID models a RoleIDExistsError collision — a losing racer whose
	// id, not its name, is what the store reports as already claimed.
	existsByID map[string]bool
	// mismatch, when true, makes a reported collision's "already persisted"
	// record deliberately disagree with the request (a different id) —
	// modelling a genuine, unrelated conflict rather than this call's own
	// idempotent re-attempt. False (the default) models the ordinary
	// idempotent case: the collision is this exact canonical definition,
	// seeded field-for-field identical to the request.
	mismatch    bool
	createCalls []role.CreateRoleRequest
	seeded      []role.Role
}

func (f *fakeRoles) CreateRole(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	f.createCalls = append(f.createCalls, *req)
	switch {
	case f.existsByName[req.Name]:
		f.recordCollision(req)
		return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
	case f.existsByID[req.ID]:
		f.recordCollision(req)
		return nil, &role.RoleIDExistsError{ID: req.ID, TenantID: req.TenantID}
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	f.createCount.Add(1)
	f.seeded = append(f.seeded, role.Role{ID: req.ID, Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions})
	return &role.Role{ID: req.ID, Name: req.Name, TenantID: req.TenantID, Permissions: req.Permissions}, nil
}

// recordCollision records what is "already persisted" for a collision
// CreateRole is about to report, so GetRoleByName below can find it. The
// provenance check (firstboot.BuiltInRoleAlreadySeededWithin,
// go-qrdb#555) reads this back and refuses the collision as unverifiable
// if it cannot: a fakeRoles that reported "already exists" without ever
// remembering a matching record would make every idempotent re-run fail,
// not merely fail to prove.
func (f *fakeRoles) recordCollision(req *role.CreateRoleRequest) {
	id := req.ID
	if f.mismatch {
		id = "some-other-tenants-role-id"
	}
	f.seeded = append(f.seeded, role.Role{
		ID: id, Name: req.Name, TenantID: req.TenantID,
		Permissions: req.Permissions, System: role.IsSystemRoleName(req.Name),
	})
}

// GetRoleByName makes fakeRoles a role.NameResolver, as qrdb's own
// role.RoleServicer is — every production wiring passes a store that can
// answer lookups by name, and provisioning consults it so grants bind to the
// identifier a role is actually persisted under.
func (f *fakeRoles) GetRoleByName(_ context.Context, tenantID, name string) (*role.Role, error) {
	for i := range f.seeded {
		if f.seeded[i].TenantID == tenantID && f.seeded[i].Name == name {
			r := f.seeded[i]
			return &r, nil
		}
	}
	return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
}

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
	f.lastID, f.lastSubj = tenantID, subject
	if f.err != nil {
		return nil, nil, f.err
	}
	return f.cert, f.key, nil
}

type fakeSigner struct {
	called  atomic.Int32
	cert    []byte
	lastCSR []byte
}

func (s *fakeSigner) SignAdminCSR(_ context.Context, _, _ string, csrPEM []byte) ([]byte, error) {
	s.called.Add(1)
	s.lastCSR = csrPEM
	return s.cert, nil
}

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

// ---------------------------------------------------------------------------
// BootstrapSystem
// ---------------------------------------------------------------------------

func TestBootstrapSystem_NoneStrategy(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	tenants, users, roles := &fakeTenants{}, &fakeUsers{}, &fakeRoles{}

	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyNone}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem: %v", err)
	}
	if result.BarrierStrategy != StrategyNone {
		t.Errorf("BarrierStrategy = %q, want none", result.BarrierStrategy)
	}
	if reg.SystemBarrier().IsSealed() {
		t.Error("system barrier still sealed after bootstrap")
	}
	if result.FirstBoot == nil || !result.FirstBoot.SystemTenantCreated {
		t.Error("first-boot did not create the system tenant")
	}
	if len(tenants.createCalls) != 1 || tenants.createCalls[0].id != tenantid.System {
		t.Errorf("system tenant not created via first-boot: %+v", tenants.createCalls)
	}
}

func TestBootstrapSystem_PassphraseStrategy(t *testing.T) {
	reg := newRegistry(t, seal.NewDeferredSoftwareStrategy())
	tenants, users, roles := &fakeTenants{}, &fakeUsers{}, &fakeRoles{}

	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Strategy:          StrategyPassphrase,
		Passphrase:        passphraseBuffer(t, "correct horse battery staple"),
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem: %v", err)
	}
	if result.BarrierStrategy != StrategyPassphrase {
		t.Errorf("BarrierStrategy = %q, want passphrase/software", result.BarrierStrategy)
	}
	if reg.SystemBarrier().IsSealed() {
		t.Error("system barrier still sealed after passphrase bootstrap")
	}
}

func TestBootstrapSystem_PassphraseStrategy_EmptyPassphraseRejected(t *testing.T) {
	reg := newRegistry(t, seal.NewDeferredSoftwareStrategy())

	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Strategy:          StrategyPassphrase,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	var want *MissingPassphraseError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingPassphraseError", err)
	}
	if !reg.SystemBarrier().IsSealed() {
		t.Error("system barrier must remain sealed when passphrase is rejected")
	}
}

// TestBootstrapSystem_PassphraseEmptyButAlreadyUnsealed exercises the locked
// deferred-init flow: for the passphrase strategy the system barrier is
// initialized+unsealed out-of-band via the admin barrier API BEFORE
// BootstrapSystem runs (during awaitUnseal). By that point cmd/qrdbd passes an
// EMPTY passphrase to BootstrapSystem because the barrier is already live.
// BootstrapSystem must NOT demand a passphrase in that case — it must detect
// the unsealed barrier, set BarrierAlreadyInitialized, and proceed to
// first-boot. This is the regression guard for the P4-2 wiring.
func TestBootstrapSystem_PassphraseEmptyButAlreadyUnsealed(t *testing.T) {
	reg := newRegistry(t, seal.NewDeferredSoftwareStrategy())
	// Simulate awaitUnseal: the admin initialized+unsealed the barrier with
	// their passphrase out-of-band before daemon first-boot is reachable.
	if err := reg.SystemBarrier().Initialize(context.Background(),
		seal.MustCredentials(context.Background(), []byte("operator-passphrase"))); err != nil {
		t.Fatalf("pre-initialize passphrase barrier: %v", err)
	}
	if reg.SystemBarrier().IsSealed() {
		t.Fatal("setup: passphrase barrier should be unsealed after Initialize")
	}

	// Empty passphrase — exactly what cmd/qrdbd supplies post-unseal.
	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Strategy:          StrategyPassphrase,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem with empty passphrase + unsealed barrier: %v", err)
	}
	if !result.BarrierAlreadyInitialized {
		t.Error("BarrierAlreadyInitialized = false, want true for an already-unsealed passphrase barrier")
	}
	if result.FirstBoot == nil || !result.FirstBoot.SystemTenantCreated {
		t.Error("first-boot did not run on the post-unseal passphrase path")
	}
}

// TestBootstrapSystem_ShamirSealedDefersToCeremony asserts the locked
// deferred-init architecture for shamir: a still-SEALED shamir system barrier
// is NOT initialized inline by BootstrapSystem (initialization is the
// ceremony's job, driven by multiple SOs out-of-band). BootstrapSystem must
// neither demand a passphrase nor initialize the barrier; it proceeds to
// first-boot and leaves the barrier untouched (sealed, no strategy recorded).
func TestBootstrapSystem_ShamirSealedDefersToCeremony(t *testing.T) {
	shamir, err := seal.NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	reg := newRegistry(t, shamir)
	tenants, users, roles := &fakeTenants{}, &fakeUsers{}, &fakeRoles{}

	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyShamir}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem (shamir): %v", err)
	}
	// Barrier init is deferred to the ceremony: not initialized here.
	if result.BarrierStrategy != "" {
		t.Errorf("BarrierStrategy = %q, want empty (deferred to ceremony)", result.BarrierStrategy)
	}
	if result.BarrierAlreadyInitialized {
		t.Error("BarrierAlreadyInitialized = true, want false for a sealed shamir barrier")
	}
	if !reg.SystemBarrier().IsSealed() {
		t.Error("shamir system barrier must remain sealed; BootstrapSystem must not inline-initialize it")
	}
	// First-boot still runs once reachable.
	if result.FirstBoot == nil || !result.FirstBoot.SystemTenantCreated {
		t.Error("first-boot did not run for the shamir deferred path")
	}
}

// TestBootstrapSystem_ShamirAlreadyUnsealed asserts that once the ceremony has
// unsealed the shamir barrier out-of-band, BootstrapSystem treats it as
// already-initialized (BarrierAlreadyInitialized) and proceeds to first-boot
// without demanding any credential.
func TestBootstrapSystem_ShamirAlreadyUnsealed(t *testing.T) {
	shamir, err := seal.NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	reg := newRegistry(t, shamir)
	// The ceremony has already initialized (and so unsealed) the barrier
	// over its share holders.
	system := reg.SystemBarrier()
	system.SetShamirConfig(&seal.ShamirConfig{Threshold: 2, TotalShares: 3})
	sealtest.NewCustody("alice", "bob", "carol").Install(system)
	if _, err := system.InitializeShamir(context.Background(), ""); err != nil {
		t.Fatalf("pre-initialize shamir barrier: %v", err)
	}
	if reg.SystemBarrier().IsSealed() {
		t.Fatal("setup: shamir barrier should be unsealed after Initialize")
	}

	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyShamir}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem (shamir, already unsealed): %v", err)
	}
	if !result.BarrierAlreadyInitialized {
		t.Error("BarrierAlreadyInitialized = false, want true for an already-unsealed shamir barrier")
	}
	if result.FirstBoot == nil || !result.FirstBoot.SystemTenantCreated {
		t.Error("first-boot did not run for the already-unsealed shamir path")
	}
}

func TestBootstrapSystem_AdminCertViaCA(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	certPEM := realCertPEM(t)
	ca := &fakeCA{cert: certPEM, key: []byte("KEY")}
	binder := &fakeBinder{}

	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyNone}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		CA: ca, Binder: binder, Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem: %v", err)
	}
	if ca.called.Load() != 1 {
		t.Errorf("CA called %d times, want 1", ca.called.Load())
	}
	if result.FirstBoot.AdminCertThumbprint == "" || binder.thumbprint != result.FirstBoot.AdminCertThumbprint {
		t.Error("admin cert thumbprint not bound through first-boot")
	}
}

func TestBootstrapSystem_AlreadyInitializedIsIdempotent(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	deps := Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	}
	if _, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyNone}, deps); err != nil {
		t.Fatalf("first BootstrapSystem: %v", err)
	}

	// Second run: barrier already initialized, first-boot idempotent. Users
	// is a fakeUsersWithLookup, not plain fakeUsers: go-qrdb#555's
	// provenance check (createAdminUser) now verifies a username collision
	// by reading the record back, exactly as a real user.UserServicer would
	// let it, rather than trusting UsernameExistsError alone.
	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyNone}, Dependencies{
		Tenants: &fakeTenants{createAlreadyErr: true},
		Users: &fakeUsersWithLookup{
			fakeUsers: fakeUsers{exists: true},
			lookupUser: &user.User{
				ID: 1, Username: "admin", TenantID: tenantid.System,
				RoleIDs:   []string{role.SystemRoleID(tenantid.System, role.RoleNameAdmin)},
				Lifecycle: user.LifecycleActive,
			},
		},
		Roles: &fakeRoles{existsByName: map[string]bool{
			role.RoleNameSO: true, role.RoleNameAdmin: true, role.RoleNameOperator: true,
			role.RoleNameUser: true, role.RoleNameAuditor: true,
			role.RoleNameOwner: true,
		}},
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("second BootstrapSystem: %v", err)
	}
	if !result.BarrierAlreadyInitialized {
		t.Error("BarrierAlreadyInitialized = false, want true on re-run")
	}
}

func TestBootstrapSystem_MissingBarriersRejected(t *testing.T) {
	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *MissingBarriersError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingBarriersError", err)
	}
}

func TestBootstrapSystem_UnsupportedStrategyRejected(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: seal.StrategyID("tpm2")}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	var want *UnsupportedStrategyError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *UnsupportedStrategyError", err)
	}
}

func TestBootstrapSystem_FirstBootErrorWrapped(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	sentinel := errors.New("backend down")
	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyNone}, Dependencies{
		Tenants: &fakeTenants{createErr: sentinel}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	var want *FirstBootError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *FirstBootError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ProvisionTenant
// ---------------------------------------------------------------------------

func TestProvisionTenant_CreatesTenantAdminAndBindsCert(t *testing.T) {
	tenants, users, roles := &fakeTenants{}, &fakeUsers{}, &fakeRoles{}
	certPEM := realCertPEM(t)
	signer := &fakeSigner{cert: certPEM}
	binder := &fakeBinder{}

	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		DisplayName: "Acme Corp",
		// Isolation pinned to prefix: barrier/volume/raft isolation keeps IAM
		// behind the tenant's own barrier (tenant.IAMBehindTenantBarrier), so
		// cert-mode provisioning for one of those takes the pending-admin
		// path (provisionPendingAdmin) and creates no role or user at all --
		// covered by its own tests. This test is about the direct path.
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Signer: signer, Binder: binder, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}

	// Tenant record created with the requested isolation.
	if len(tenants.createCalls) != 1 {
		t.Fatalf("tenant createCalls = %d, want 1", len(tenants.createCalls))
	}
	tc := tenants.createCalls[0]
	if tc.id != "acme" || tc.name != "Acme Corp" || tc.isolationType != tenant.IsolationPrefix {
		t.Errorf("tenant create = %+v, want acme/Acme Corp/prefix", tc)
	}

	// Tenant-scoped roles seeded.
	if roles.createCount.Load() != int32(len(role.BuiltInRoles("acme"))) {
		t.Errorf("roles created = %d, want %d", roles.createCount.Load(), len(role.BuiltInRoles("acme")))
	}
	for _, r := range roles.createCalls {
		if r.TenantID != "acme" {
			t.Errorf("role %q seeded for tenant %q, want acme", r.Name, r.TenantID)
		}
	}

	// Tenant-admin user bound to the tenant-scoped admin role.
	if len(users.createCalls) != 1 {
		t.Fatalf("user createCalls = %d, want 1", len(users.createCalls))
	}
	adminReq := users.createCalls[0]
	wantRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	if adminReq.Username != "admin" || adminReq.TenantID != "acme" {
		t.Errorf("admin user = %+v, want admin@acme", adminReq)
	}
	if len(adminReq.RoleIDs) != 1 || adminReq.RoleIDs[0] != wantRoleID {
		t.Errorf("admin RoleIDs = %v, want [%q]", adminReq.RoleIDs, wantRoleID)
	}
	// go-qrdb#106: a per-tenant admin minted by ProvisionTenant must stay
	// deletable, so it must NOT carry the immutable System flag reserved for
	// the single system-tenant bootstrap admin.
	if adminReq.System {
		t.Error("per-tenant admin CreateUserRequest.System = true, want false (go-qrdb#106)")
	}

	// Cert enrolled + bound.
	if signer.called.Load() != 1 {
		t.Errorf("SignAdminCSR called %d times, want 1", signer.called.Load())
	}
	if result.AdminCertThumbprint == "" || binder.thumbprint != result.AdminCertThumbprint {
		t.Error("tenant-admin cert thumbprint not bound")
	}
	if binder.userID == 0 {
		t.Errorf("bound userID = %d, want non-zero", binder.userID)
	}
	if result.AdminUserID == 0 {
		t.Errorf("AdminUserID = %d, want non-zero", result.AdminUserID)
	}
}

// TestProvisionTenant_DoesNotInitTenantBarrier asserts the system-admin
// provisioning path never touches the barrier registry: no tenant barrier is
// registered and none is initialized. The default isolation (barrier) keeps
// IAM behind the tenant's own barrier, so this exercises the pending-admin
// path (deps.PendingAdmins) rather than direct role/user creation -- which
// is exactly the case most likely to tempt a caller into touching Barriers.
func TestProvisionTenant_DoesNotInitTenantBarrier(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	signer := &fakeSigner{cert: realCertPEM(t)}
	pending := tenantadmin.NewKVStore(kvstore.NewMemoryKVStore())

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: signer, Binder: &fakeBinder{}, PendingAdmins: pending,
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if got := reg.ListTenants(); len(got) != 0 {
		t.Errorf("tenant barriers registered = %v, want none (system admin holds no tenant key)", got)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, seal.ErrTenantNotFound) {
		t.Errorf("GetTenant(acme) err = %v, want ErrTenantNotFound", err)
	}
}

func TestProvisionTenant_CSRSignPathKeepsAdminKey(t *testing.T) {
	certPEM := realCertPEM(t)
	signer := &fakeSigner{cert: certPEM}
	binder := &fakeBinder{}

	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: signer, Binder: binder, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if signer.called.Load() != 1 {
		t.Errorf("SignAdminCSR called %d times, want 1", signer.called.Load())
	}
	// BREAKING: ProvisionTenant never generates or returns an admin private
	// key -- the admin keeps its own, whether the CSR or the BYOC path was
	// used. TenantProvisionResult has no AdminKeyPEM field at all.
	if result.AdminCertPEM == nil {
		t.Error("AdminCertPEM must be set on the CSR-sign path")
	}
	if result.AdminCertThumbprint == "" || binder.thumbprint != result.AdminCertThumbprint {
		t.Error("CSR-signed cert not bound by thumbprint")
	}
}

// TestProvisionTenant_DeferredModeCreatesPendingAdmin asserts deferred
// enrollment provisions the control plane (tenant + seeded roles + admin
// user) but establishes NO credential: the admin is created pending, the
// cert seams are never invoked even though they are configured, and the
// result carries the user + role ids a consumer needs to attach an
// invitation.
func TestProvisionTenant_DeferredModeCreatesPendingAdmin(t *testing.T) {
	tenants, users, roles := &fakeTenants{}, &fakeUsers{}, &fakeRoles{}
	signer := &fakeSigner{cert: realCertPEM(t)}
	binder := &fakeBinder{}

	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Signer: signer, Binder: binder,
		AdminFactors: &fakeFactorChecker{}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}

	// Control plane provisioned exactly as in cert mode.
	if len(tenants.createCalls) != 1 {
		t.Fatalf("tenant createCalls = %d, want 1", len(tenants.createCalls))
	}
	if roles.createCount.Load() != int32(len(role.BuiltInRoles("acme"))) {
		t.Errorf("roles created = %d, want %d", roles.createCount.Load(), len(role.BuiltInRoles("acme")))
	}

	// Admin user created PENDING, bound to the tenant-scoped Admin role.
	if len(users.createCalls) != 1 {
		t.Fatalf("user createCalls = %d, want 1", len(users.createCalls))
	}
	adminReq := users.createCalls[0]
	if !adminReq.Pending {
		t.Error("deferred admin created with Pending=false, want pending")
	}
	wantRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	if len(adminReq.RoleIDs) != 1 || adminReq.RoleIDs[0] != wantRoleID {
		t.Errorf("admin RoleIDs = %v, want [%q]", adminReq.RoleIDs, wantRoleID)
	}

	// No credential exists: the signer is untouched, nothing bound.
	if signer.called.Load() != 0 {
		t.Errorf("SignAdminCSR invoked %d times, want none in deferred mode", signer.called.Load())
	}
	if binder.thumbprint != "" || binder.userID != 0 {
		t.Errorf("cert bound (thumbprint=%q userID=%d), want no binding", binder.thumbprint, binder.userID)
	}
	if result.AdminCertPEM != nil || result.AdminCertThumbprint != "" {
		t.Error("result carries cert material in deferred mode, want none")
	}

	// The consumer gets what an invitation needs.
	if !result.AdminPending {
		t.Error("result.AdminPending = false, want true")
	}
	if result.AdminUserID == 0 {
		t.Error("result.AdminUserID = 0, want the pending admin's id")
	}
	if result.AdminRoleID != wantRoleID {
		t.Errorf("result.AdminRoleID = %q, want %q", result.AdminRoleID, wantRoleID)
	}
}

// TestProvisionTenant_CertModeUnchanged asserts the default (empty) mode
// still enrolls by certificate: the admin is created enabled and the result
// reports a non-pending admin with the role id populated.
func TestProvisionTenant_CertModeUnchanged(t *testing.T) {
	users := &fakeUsers{}
	signer := &fakeSigner{cert: realCertPEM(t)}

	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users, Roles: &fakeRoles{},
		Signer: signer, Binder: &fakeBinder{}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if len(users.createCalls) != 1 || users.createCalls[0].Pending {
		t.Error("cert-mode admin must be created non-pending")
	}
	if result.AdminPending {
		t.Error("result.AdminPending = true in cert mode, want false")
	}
	if want := role.SystemRoleID("acme", role.RoleNameAdmin); result.AdminRoleID != want {
		t.Errorf("result.AdminRoleID = %q, want %q", result.AdminRoleID, want)
	}
	if result.AdminCertThumbprint == "" {
		t.Error("cert mode enrolled no cert")
	}
}

// fakeFactorChecker models the canonical auth-factor query seam.
type fakeFactorChecker struct {
	hasActive bool
	err       error
}

func (f *fakeFactorChecker) HasActiveFactor(_ context.Context, _ uint64) (bool, error) {
	return f.hasActive, f.err
}

// fakeUsersWithLookup extends fakeUsers with the AdminUserLookup seam so
// deferred re-run resolution can be exercised; plain fakeUsers deliberately
// lacks it to model adapters without lookup.
type fakeUsersWithLookup struct {
	fakeUsers
	lookupUser *user.User
	lookupErr  error
}

func (f *fakeUsersWithLookup) GetUserByUsername(_ context.Context, _, _ string) (*user.User, error) {
	if f.lookupErr != nil {
		return nil, f.lookupErr
	}
	return f.lookupUser, nil
}

// TestProvisionTenant_DeferredModeRejectedForSystemTenant asserts the
// system tenant can never take the deferred path: system-level enrollment
// is cert-only, always.
func TestProvisionTenant_DeferredModeRejectedForSystemTenant(t *testing.T) {
	tenants := &fakeTenants{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        tenantid.System,
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *SystemTenantDeferredEnrollmentError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *SystemTenantDeferredEnrollmentError", err)
	}
	if len(tenants.createCalls) != 0 {
		t.Error("tenant created despite rejected config; validation must precede writes")
	}
}

// TestProvisionTenant_DeferredReRunRecoversAdminID asserts an idempotent
// deferred re-run resolves a TRULY PENDING preexisting admin (lifecycle
// pending, no credential bound) so the consumer still receives the user id an
// invitation attaches to.
func TestProvisionTenant_DeferredReRunRecoversAdminID(t *testing.T) {
	wantRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	users := &fakeUsersWithLookup{
		fakeUsers: fakeUsers{exists: true},
		lookupUser: &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecyclePending,
		},
	}
	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: &fakeTenants{createAlreadyErr: true}, Users: users,
		Roles: &fakeRoles{}, AdminFactors: &fakeFactorChecker{},
		Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant: %v", err)
	}
	if result.AdminUserID != 42 {
		t.Errorf("AdminUserID = %d, want 42 (recovered by lookup)", result.AdminUserID)
	}
	if !result.AdminPending {
		t.Error("AdminPending = false, want true (resolved admin is pending)")
	}
}

// TestProvisionTenant_DeferredReRunFailsClosedOnNonPendingAdmin asserts a
// deferred re-run refuses to convert any non-pending preexisting admin into
// an invitation target: activated admins, disabled admins that still hold a
// bound certificate (e.g. disabled for incident response), and
// credentialless admins in any lifecycle other than pending — absent and
// unrecognized included — all fail with PendingAdminUnresolvableError.
// Enrolled auth factors are a separate, canonical-store check
// (ensureNoActiveFactors / AdminFactorChecker), covered by its own tests.
func TestProvisionTenant_DeferredReRunFailsClosedOnNonPendingAdmin(t *testing.T) {
	wantRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	for _, tc := range []struct {
		name  string
		admin *user.User
	}{
		{"already activated", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecycleActive,
		}},
		{"disabled with bound certificate", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecycleDisabled,
			CertBindings: []user.CertBinding{{Thumbprint: "aa"}},
		}},
		// Credentialless records that are NOT pending: only an explicit
		// pending lifecycle is an invitation target. An absent or unknown
		// state fails closed like every other non-pending one.
		{"absent lifecycle", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID},
		}},
		{"unrecognized lifecycle", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: "quarantined",
		}},
		{"disabled without credentials", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecycleDisabled,
		}},
		{"suspended without credentials", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecycleSuspended,
		}},
		{"locked without credentials", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecycleLocked,
		}},
		{"deleting without credentials", &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecycleDeleting,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := &fakeUsersWithLookup{
				fakeUsers:  fakeUsers{exists: true},
				lookupUser: tc.admin,
			}
			_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
				TenantID:        "acme",
				AdminEnrollment: AdminEnrollDeferred,
			}, Dependencies{
				Tenants: &fakeTenants{createAlreadyErr: true}, Users: users,
				Roles: &fakeRoles{}, AdminFactors: &fakeFactorChecker{},
				Logger: discardLogger(),
			})
			var want *PendingAdminUnresolvableError
			if !errors.As(err, &want) {
				t.Fatalf("err = %v, want *PendingAdminUnresolvableError", err)
			}
		})
	}
}

// TestProvisionTenant_DeferredReRunWithoutLookupFails asserts a deferred
// re-run against a Users seam without lookup fails loudly instead of
// reporting user id 0.
func TestProvisionTenant_DeferredReRunWithoutLookupFails(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{exists: true},
		Roles: &fakeRoles{}, AdminFactors: &fakeFactorChecker{},
		Logger: discardLogger(),
	})
	var want *PendingAdminUnresolvableError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *PendingAdminUnresolvableError", err)
	}
}

// TestProvisionTenant_DeferredReRunNilLookupFails asserts a lookup adapter
// answering (nil, nil) fails closed with a typed error instead of panicking.
func TestProvisionTenant_DeferredReRunNilLookupFails(t *testing.T) {
	users := &fakeUsersWithLookup{fakeUsers: fakeUsers{exists: true}}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users,
		Roles: &fakeRoles{}, AdminFactors: &fakeFactorChecker{},
		Logger: discardLogger(),
	})
	var want *PendingAdminUnresolvableError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *PendingAdminUnresolvableError", err)
	}
}

// TestProvisionTenant_DeferredReRunRoleMismatchFails asserts an existing
// user under the admin username that does NOT hold the tenant-scoped Admin
// role is refused the invitation handoff.
func TestProvisionTenant_DeferredReRunRoleMismatchFails(t *testing.T) {
	users := &fakeUsersWithLookup{
		fakeUsers: fakeUsers{exists: true},
		lookupUser: &user.User{
			ID: 42, Username: "admin", TenantID: "acme",
			RoleIDs: []string{"some-other-role"},
		},
	}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: users,
		Roles: &fakeRoles{}, AdminFactors: &fakeFactorChecker{},
		Logger: discardLogger(),
	})
	var want *PendingAdminUnresolvableError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *PendingAdminUnresolvableError", err)
	}
}

// fakeUsersReturning models a UserCreator adapter that answers CreateUser
// with a canned record — e.g. an IAM stack that ignored
// CreateUserRequest.Pending.
type fakeUsersReturning struct {
	ret *user.User
}

func (f *fakeUsersReturning) CreateUser(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
	return f.ret, nil
}

// TestProvisionTenant_DeferredCreateFailsClosedOnNonPendingRecord asserts
// the freshly created record passes the same truly-pending validation as a
// re-run lookup: an adapter that ignores Pending (or returns a wrong or
// credentialed record) must not yield an invitation target.
func TestProvisionTenant_DeferredCreateFailsClosedOnNonPendingRecord(t *testing.T) {
	wantRoleID := role.SystemRoleID("acme", role.RoleNameAdmin)
	for _, tc := range []struct {
		name string
		ret  *user.User
	}{
		{"enabled record (Pending ignored)", &user.User{
			ID: 7, Username: "admin", TenantID: "acme",
			RoleIDs: []string{wantRoleID}, Lifecycle: user.LifecycleActive,
		}},
		{"zero user id", &user.User{
			Username: "admin", TenantID: "acme", RoleIDs: []string{wantRoleID},
		}},
		{"wrong tenant", &user.User{
			ID: 7, Username: "admin", TenantID: "other",
			RoleIDs: []string{wantRoleID},
		}},
		{"wrong username", &user.User{
			ID: 7, Username: "someone-else", TenantID: "acme",
			RoleIDs: []string{wantRoleID},
		}},
		{"missing admin role", &user.User{
			ID: 7, Username: "admin", TenantID: "acme",
		}},
		{"credentialed record", &user.User{
			ID: 7, Username: "admin", TenantID: "acme",
			RoleIDs:      []string{wantRoleID},
			CertBindings: []user.CertBinding{{Thumbprint: "aa"}},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
				TenantID:        "acme",
				AdminEnrollment: AdminEnrollDeferred,
			}, Dependencies{
				Tenants: &fakeTenants{}, Users: &fakeUsersReturning{ret: tc.ret},
				Roles: &fakeRoles{}, AdminFactors: &fakeFactorChecker{},
				Logger: discardLogger(),
			})
			var want *PendingAdminUnresolvableError
			if !errors.As(err, &want) {
				t.Fatalf("err = %v, want *PendingAdminUnresolvableError", err)
			}
		})
	}
}

// TestProvisionTenant_DeferredModeRequiresFactorSeam asserts deferred mode
// refuses to run without the canonical auth-factor store seam, before any
// write.
func TestProvisionTenant_DeferredModeRequiresFactorSeam(t *testing.T) {
	tenants := &fakeTenants{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *MissingAdminFactorsError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingAdminFactorsError", err)
	}
	if len(tenants.createCalls) != 0 {
		t.Error("tenant created despite rejected config; validation must precede writes")
	}
}

// TestProvisionTenant_DeferredModeFailsClosedOnCanonicalActiveFactor asserts
// a record that looks pending is still refused when the canonical factor
// store reports an active factor for it (enrolled elsewhere, or a
// redemption that bound a factor and failed before activation).
func TestProvisionTenant_DeferredModeFailsClosedOnCanonicalActiveFactor(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		AdminFactors: &fakeFactorChecker{hasActive: true},
		Logger:       discardLogger(),
	})
	var want *PendingAdminUnresolvableError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *PendingAdminUnresolvableError", err)
	}
}

// TestProvisionTenant_DeferredModeFactorCheckErrorWrapped asserts a canonical
// factor-store query failure is surfaced as a typed error, never treated as
// "no factors".
func TestProvisionTenant_DeferredModeFactorCheckErrorWrapped(t *testing.T) {
	sentinel := errors.New("kv unavailable")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		AdminFactors: &fakeFactorChecker{err: sentinel},
		Logger:       discardLogger(),
	})
	var want *TenantAdminUserCreationError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminUserCreationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// TestProvisionTenant_DeferredModeRejectsCSR asserts a CSR supplied alongside
// deferred enrollment is refused rather than silently discarded.
func TestProvisionTenant_DeferredModeRejectsCSR(t *testing.T) {
	tenants := &fakeTenants{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollDeferred,
		AdminCSRPEM:     []byte("THE-CSR"),
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *ConflictingAdminEnrollmentError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *ConflictingAdminEnrollmentError", err)
	}
	if len(tenants.createCalls) != 0 {
		t.Error("tenant created despite rejected config; validation must precede writes")
	}
}

// TestProvisionTenant_InvalidEnrollmentModeRejected asserts an undeclared
// mode value is refused with the offending value named.
func TestProvisionTenant_InvalidEnrollmentModeRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:        "acme",
		AdminEnrollment: AdminEnrollmentMode("bogus"),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *InvalidAdminEnrollmentModeError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *InvalidAdminEnrollmentModeError", err)
	}
	if want.Mode != "bogus" {
		t.Errorf("Mode = %q, want %q", want.Mode, "bogus")
	}
}

// TestProvisionTenant_NoCredentialRejected asserts cert-mode provisioning
// with neither an admin CSR nor an admin certificate is refused before
// anything is written: a tenant is onboarded with its first admin's login
// credential, or not at all. This is the retired Path A (generate-and-
// distribute via Dependencies.CA) made unreachable, not merely unused:
// ProvisionTenant no longer references CA at all.
func TestProvisionTenant_NoCredentialRejected(t *testing.T) {
	tenants := &fakeTenants{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *MissingAdminCredentialError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingAdminCredentialError", err)
	}
	if len(tenants.createCalls) != 0 {
		t.Error("tenant created despite a missing admin credential; validation must precede writes")
	}
}

// TestProvisionTenant_ConflictingCredentialRejected asserts supplying both
// an admin CSR and an admin certificate is refused before anything is
// written: exactly one names the admin's key.
func TestProvisionTenant_ConflictingCredentialRejected(t *testing.T) {
	tenants := &fakeTenants{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:     "acme",
		AdminCSRPEM:  validAdminCSR(t),
		AdminCertPEM: realCertPEM(t),
	}, Dependencies{
		Tenants: tenants, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *ConflictingAdminCredentialError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *ConflictingAdminCredentialError", err)
	}
	if len(tenants.createCalls) != 0 {
		t.Error("tenant created despite a conflicting admin credential; validation must precede writes")
	}
}

func TestProvisionTenant_EmptyTenantIDRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: ""}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Logger: discardLogger(),
	})
	var want *EmptyTenantIDError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *EmptyTenantIDError", err)
	}
}

func TestProvisionTenant_TenantCreateErrorWrapped(t *testing.T) {
	sentinel := errors.New("not leader")
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{createErr: sentinel}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{},
		Logger: discardLogger(),
	})
	var want *TenantRecordCreationError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantRecordCreationError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

func TestProvisionTenant_IdempotentReRun(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	roles := &fakeRoles{existsByName: map[string]bool{
		role.RoleNameSO: true, role.RoleNameAdmin: true, role.RoleNameOperator: true,
		role.RoleNameUser: true, role.RoleNameAuditor: true,
	}}
	users := &fakeUsers{exists: true}
	signer := &fakeSigner{cert: realCertPEM(t)}

	result, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Signer: signer, Binder: &fakeBinder{}, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("ProvisionTenant (idempotent): %v", err)
	}
	// Admin already existed -> no cert rotation.
	if signer.called.Load() != 0 {
		t.Errorf("SignAdminCSR called %d times on idempotent re-run, want 0", signer.called.Load())
	}
	if result.AdminCertPEM != nil {
		t.Error("AdminCertPEM set on idempotent re-run; want nil (no rotation)")
	}
}

// TestProvisionTenant_RoleIDCollisionVerifiedIdempotent covers the OTHER
// collision CreateRole can report for a built-in role:
// role.RoleIDExistsError, not only role.RoleNameExistsError.
// seedTenantRoles must verify this collision the SAME way, via
// firstboot.BuiltInRoleAlreadySeededWithin, and accept it as this run's own
// idempotent re-attempt — not merely the name-collision case
// TestProvisionTenant_IdempotentReRun already covers.
func TestProvisionTenant_RoleIDCollisionVerifiedIdempotent(t *testing.T) {
	tenants := &fakeTenants{createAlreadyErr: true}
	roles := &fakeRoles{existsByID: map[string]bool{
		role.SystemRoleID("acme", role.RoleNameSO):       true,
		role.SystemRoleID("acme", role.RoleNameAdmin):    true,
		role.SystemRoleID("acme", role.RoleNameOperator): true,
		role.SystemRoleID("acme", role.RoleNameUser):     true,
		role.SystemRoleID("acme", role.RoleNameAuditor):  true,
	}}
	users := &fakeUsers{exists: true}

	if _, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: roles,
		Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{}, Logger: discardLogger(),
	}); err != nil {
		t.Fatalf("ProvisionTenant (id-collision, verified identical): %v", err)
	}
}

// TestProvisionTenant_RoleCollisionMismatchFails proves the OTHER half of
// the verification: a collision whose persisted record does NOT match the
// canonical definition is a genuine, unexplained conflict, never accepted
// as an idempotent re-attempt — on either collision kind, and no amount of
// the bounded retry changes that.
func TestProvisionTenant_RoleCollisionMismatchFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		roles *fakeRoles
	}{
		{"name collision", &fakeRoles{mismatch: true, existsByName: map[string]bool{role.RoleNameSO: true}}},
		{"id collision", &fakeRoles{mismatch: true, existsByID: map[string]bool{role.SystemRoleID("acme", role.RoleNameSO): true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
				TenantID:    "acme",
				Isolation:   tenant.IsolationPrefix,
				AdminCSRPEM: validAdminCSR(t),
			}, Dependencies{
				Tenants: &fakeTenants{createAlreadyErr: true}, Users: &fakeUsers{exists: true}, Roles: tc.roles,
				Signer: &fakeSigner{cert: realCertPEM(t)}, Binder: &fakeBinder{}, Logger: discardLogger(),
			})
			var want *TenantAdminRoleCreationError
			if !errors.As(err, &want) {
				t.Fatalf("err = %v, want *TenantAdminRoleCreationError", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// InitializeTenantBarrier
// ---------------------------------------------------------------------------

func TestInitializeTenantBarrier_None(t *testing.T) {
	reg, perTenantReg := newPooledRegistry(t)

	err := InitializeTenantBarrier(context.Background(), "acme", StrategyNone, seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("InitializeTenantBarrier: %v", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant(acme): %v", err)
	}
	if tb.IsSealed() {
		t.Error("tenant barrier still sealed after init")
	}

	// Regression guard for the memory-backend footgun: the tenant must have
	// been registered against the registry's DURABLE per-tenant Pebble pool
	// (proving InitializeTenantBarrier did not fall back to
	// seal.NewMemoryBackend()), not merely succeed.
	if _, err := perTenantReg.Get("acme"); err != nil {
		t.Fatalf("expected a durable per-tenant Pebble opened for acme, got: %v", err)
	}
}

func TestInitializeTenantBarrier_Passphrase(t *testing.T) {
	reg, perTenantReg := newPooledRegistry(t)

	err := InitializeTenantBarrier(context.Background(), "acme", StrategyPassphrase,
		seal.MustCredentials(context.Background(), []byte("tenant-passphrase")), Dependencies{
			Barriers: reg, Logger: discardLogger(),
		})
	if err != nil {
		t.Fatalf("InitializeTenantBarrier (passphrase): %v", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant(acme): %v", err)
	}
	if tb.IsSealed() {
		t.Error("tenant barrier still sealed after passphrase init")
	}
	if _, err := perTenantReg.Get("acme"); err != nil {
		t.Fatalf("expected a durable per-tenant Pebble opened for acme, got: %v", err)
	}

	// Round-trip: seal then unseal with the same passphrase proves the
	// passphrase strategy actually protects the root key.
	if err := reg.SealTenant("acme"); err != nil {
		t.Fatalf("SealTenant: %v", err)
	}
	if err := reg.UnsealTenant(context.Background(), "acme", seal.MustCredentials(context.Background(), []byte("tenant-passphrase"))); err != nil {
		t.Fatalf("UnsealTenant with correct passphrase: %v", err)
	}
}

// TestInitializeTenantBarrier_FailsClosedWithoutDurablePool is the direct
// regression test for the memory-backend footgun: when the registry is not
// configured with a durable volume pool / per-tenant registry (see
// seal.WithVolumeAllocator / WithPerTenantRegistry), InitializeTenantBarrier must
// fail closed rather than silently falling back to a non-durable
// seal.NewMemoryBackend(), whose wrapped-root-key/epoch bookkeeping would be
// lost on restart and make the tenant's barrier-encrypted IAM unrecoverable.
func TestInitializeTenantBarrier_FailsClosedWithoutDurablePool(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())

	err := InitializeTenantBarrier(context.Background(), "acme", StrategyNone, seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	var want *TenantBarrierRegistrationError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantBarrierRegistrationError (registry has no durable volume pool configured)", err)
	}
	if got := reg.ListTenants(); len(got) != 0 {
		t.Errorf("tenant barrier registered despite no durable pool configured: %v", got)
	}
}

func TestInitializeTenantBarrier_PassphraseEmptyRejected(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())

	err := InitializeTenantBarrier(context.Background(), "acme", StrategyPassphrase, seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	var want *MissingPassphraseError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingPassphraseError", err)
	}
	if got := reg.ListTenants(); len(got) != 0 {
		t.Errorf("tenant barrier registered despite rejected passphrase: %v", got)
	}
}

func TestInitializeTenantBarrier_EmptyTenantIDRejected(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	err := InitializeTenantBarrier(context.Background(), "", StrategyNone, seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	var want *EmptyTenantIDError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *EmptyTenantIDError", err)
	}
}

func TestInitializeTenantBarrier_MissingBarriersRejected(t *testing.T) {
	err := InitializeTenantBarrier(context.Background(), "acme", StrategyNone, seal.Credentials{}, Dependencies{
		Logger: discardLogger(),
	})
	var want *MissingBarriersError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingBarriersError", err)
	}
}

func TestInitializeTenantBarrier_ShamirRequiresPreRegistration(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	// Without a pre-registered shamir tenant barrier, the Credentials-only
	// self-service signature cannot express threshold/total.
	err := InitializeTenantBarrier(context.Background(), "acme", StrategyShamir, seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	var want *MissingShamirConfigError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingShamirConfigError", err)
	}
}

// TestInitializeTenantBarrier_UsesPreRegisteredStrategy verifies that when a
// tenant barrier is already registered, InitializeTenantBarrier initializes
// the existing barrier rather than building a new strategy.
func TestInitializeTenantBarrier_UsesPreRegisteredStrategy(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	if _, err := reg.RegisterTenant(context.Background(), "acme", seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "tenant/acme/root-key"}, seal.NewNoneStrategy()); err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}

	// The barrier is already registered, so no new strategy is built; the
	// pre-registered barrier is initialized.
	if err := InitializeTenantBarrier(context.Background(), "acme", StrategyNone, seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	}); err != nil {
		t.Fatalf("InitializeTenantBarrier (pre-registered): %v", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant(acme): %v", err)
	}
	if tb.IsSealed() {
		t.Error("pre-registered tenant barrier still sealed after init")
	}
}

// TestInitializeTenantBarrier_RefusesPreRegisteredShamir proves the
// credential-only self-service call cannot initialize a Shamir tenant
// barrier: it would leave one with no wrapped key and no holder roster,
// which could never be unsealed once sealed. A Shamir tenant barrier is
// initialized over its enrolled holders by the tenant service.
func TestInitializeTenantBarrier_RefusesPreRegisteredShamir(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	shamir, err := seal.NewShamirStrategy(2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	if _, err := reg.RegisterTenant(context.Background(), "acme", seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "tenant/acme/root-key"}, shamir); err != nil {
		t.Fatalf("RegisterTenant: %v", err)
	}
	err = InitializeTenantBarrier(context.Background(), "acme", StrategyShamir, seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	if !errors.Is(err, seal.ErrShamirInitializeRequiresHolders) {
		t.Fatalf("err = %v, want seal.ErrShamirInitializeRequiresHolders", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant(acme): %v", err)
	}
	if !tb.IsSealed() {
		t.Error("a refused Shamir tenant barrier was left unsealed")
	}
}

// ---------------------------------------------------------------------------
// BootstrapSystem – additional branch coverage
// ---------------------------------------------------------------------------

// TestBootstrapSystem_MissingLoggerRejected covers the logger nil-guard on
// the BootstrapSystem entry point.
func TestBootstrapSystem_MissingLoggerRejected(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyNone}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg,
	})
	var want *MissingLoggerError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingLoggerError", err)
	}
}

// TestBootstrapSystem_EmptyStrategyDefaultsToNone exercises the empty-strategy
// → StrategyNone default in BootstrapSystem.
func TestBootstrapSystem_EmptyStrategyDefaultsToNone(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		// Strategy deliberately left empty; should default to none.
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem (empty strategy): %v", err)
	}
	if result.BarrierStrategy != StrategyNone {
		t.Errorf("BarrierStrategy = %q, want %q", result.BarrierStrategy, StrategyNone)
	}
	if reg.SystemBarrier().IsSealed() {
		t.Error("system barrier still sealed after empty-strategy bootstrap")
	}
}

// TestBootstrapSystem_InitializeReturnsAlreadyInitialized exercises the
// errors.Is(initErr, seal.ErrAlreadyInitialized) branch inside the
// system.Initialize error path. This can occur when two concurrent
// BootstrapSystem calls race on the same sealed barrier: the second caller
// sees ErrAlreadyInitialized and must treat it as idempotent success.
func TestBootstrapSystem_InitializeReturnsAlreadyInitialized(t *testing.T) {
	// Pre-initialize the system barrier so Initialize returns ErrAlreadyInitialized,
	// then re-seal it so IsSealed() is true when BootstrapSystem is called —
	// this is the only way to reach the Initialize error branch without a race.
	none := seal.NewNoneStrategy()
	system, err := seal.NewStorageBarrier(discardLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "system/root-key"}, none)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	// Initialize once so the barrier has persistent root-key state.
	if err := system.Initialize(context.Background(), seal.Credentials{}); err != nil {
		t.Fatalf("pre-initialize: %v", err)
	}
	// Seal the barrier: now IsSealed() == true but the root key exists in
	// storage — Initialize will return ErrAlreadyInitialized on the next call.
	if err := system.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !system.IsSealed() {
		t.Fatal("setup: barrier must be sealed after Seal()")
	}

	reg, err := seal.NewBarrierRegistry(discardLogger(), system)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	// BootstrapSystem sees a sealed barrier for StrategyNone → calls
	// credentialsForStrategy (empty creds OK) → calls system.Initialize →
	// receives ErrAlreadyInitialized → sets BarrierAlreadyInitialized = true.
	result, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled, Strategy: StrategyNone}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem (ErrAlreadyInitialized path): %v", err)
	}
	if !result.BarrierAlreadyInitialized {
		t.Error("BarrierAlreadyInitialized = false, want true when Initialize returns ErrAlreadyInitialized")
	}
	if result.FirstBoot == nil || !result.FirstBoot.SystemTenantCreated {
		t.Error("first-boot did not run after ErrAlreadyInitialized barrier path")
	}
}

// ---------------------------------------------------------------------------
// ProvisionTenant – nil deps guards
// ---------------------------------------------------------------------------

// TestProvisionTenant_NilTenantsRejected covers the Tenants == nil guard.
func TestProvisionTenant_NilTenantsRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Users: &fakeUsers{}, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *firstboot.MissingTenantsError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *firstboot.MissingTenantsError", err)
	}
}

// TestProvisionTenant_NilUsersRejected covers the Users == nil guard.
func TestProvisionTenant_NilUsersRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: &fakeTenants{}, Roles: &fakeRoles{}, Logger: discardLogger(),
	})
	var want *firstboot.MissingUsersError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *firstboot.MissingUsersError", err)
	}
}

// TestProvisionTenant_NilRolesRejected covers the Roles == nil guard.
func TestProvisionTenant_NilRolesRejected(t *testing.T) {
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{TenantID: "acme"}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Logger: discardLogger(),
	})
	var want *firstboot.MissingRolesError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *firstboot.MissingRolesError", err)
	}
}

// ---------------------------------------------------------------------------
// InitializeTenantBarrier – additional branch coverage
// ---------------------------------------------------------------------------

// TestInitializeTenantBarrier_MissingLoggerRejected covers the logger nil-guard.
func TestInitializeTenantBarrier_MissingLoggerRejected(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	err := InitializeTenantBarrier(context.Background(), "acme", StrategyNone, seal.Credentials{}, Dependencies{
		Barriers: reg,
	})
	var want *MissingLoggerError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingLoggerError", err)
	}
}

// TestInitializeTenantBarrier_EmptyStrategyDefaultsToNone covers the empty
// strategy → StrategyNone default in InitializeTenantBarrier.
func TestInitializeTenantBarrier_EmptyStrategyDefaultsToNone(t *testing.T) {
	reg, perTenantReg := newPooledRegistry(t)
	err := InitializeTenantBarrier(context.Background(), "acme", "", seal.Credentials{}, Dependencies{
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("InitializeTenantBarrier (empty strategy): %v", err)
	}
	tb, err := reg.GetTenant("acme")
	if err != nil {
		t.Fatalf("GetTenant(acme): %v", err)
	}
	if tb.IsSealed() {
		t.Error("tenant barrier still sealed after empty-strategy init")
	}
	if _, err := perTenantReg.Get("acme"); err != nil {
		t.Fatalf("expected a durable per-tenant Pebble opened for acme, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// enrollAdminCert – additional branch coverage
// ---------------------------------------------------------------------------

// TestEnrollAdminCert_SignerError covers the SignAdminCSR error path.
func TestEnrollAdminCert_SignerError(t *testing.T) {
	sentinel := errors.New("signer unavailable")
	signer := &fakeSignerErr{err: sentinel}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: signer, Binder: &fakeBinder{}, Logger: discardLogger(),
	})
	var want *TenantAdminCertError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminCertError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// TestEnrollAdminCert_SignerReturnsEmptyCert covers issue()'s propagation of
// trustanchor.ParseCertificates' *EmptyBundleError when the Signer reports
// success with no certificate: a defective Signer must fail provisioning
// rather than silently proceed with no credential bound.
func TestEnrollAdminCert_SignerReturnsEmptyCert(t *testing.T) {
	signer := &fakeSignerEmpty{}
	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: signer, Binder: &fakeBinder{}, Logger: discardLogger(),
	})
	var want *TenantAdminCertError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminCertError", err)
	}
}

// TestEnrollAdminCert_BinderError covers the BindAdminCert error path.
func TestEnrollAdminCert_BinderError(t *testing.T) {
	sentinel := errors.New("bind store down")
	certPEM := realCertPEM(t)
	signer := &fakeSigner{cert: certPEM}
	binder := &fakeBinder{err: sentinel}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: signer, Binder: binder, Logger: discardLogger(),
	})
	var want *TenantAdminCertError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminCertError", err)
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("err does not unwrap to sentinel: %v", err)
	}
}

// ---------------------------------------------------------------------------
// spkiThumbprintFromCertPEM – bad DER path
// ---------------------------------------------------------------------------

// TestSPKIThumbprintFromCertPEM_BadDER covers the x509.ParseCertificate error
// path in spkiThumbprintFromCertPEM: a valid CERTIFICATE PEM block whose DER
// bytes are syntactically invalid.
func TestSPKIThumbprintFromCertPEM_BadDER(t *testing.T) {
	badDER := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not valid DER")})
	signer := &fakeSigner{cert: badDER}

	_, err := ProvisionTenant(context.Background(), TenantProvisionConfig{
		TenantID:    "acme",
		Isolation:   tenant.IsolationPrefix,
		AdminCSRPEM: validAdminCSR(t),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Signer: signer, Binder: &fakeBinder{}, Logger: discardLogger(),
	})
	var want *TenantAdminCertError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *TenantAdminCertError", err)
	}
}

// ---------------------------------------------------------------------------
// Additional fake helpers needed by the new tests
// ---------------------------------------------------------------------------

// fakeSignerErr is a CertSigner whose SignAdminCSR always returns an error.
type fakeSignerErr struct {
	err error
}

func (s *fakeSignerErr) SignAdminCSR(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
	return nil, s.err
}

// fakeSignerEmpty is a CertSigner whose SignAdminCSR returns an empty cert slice.
type fakeSignerEmpty struct{}

func (s *fakeSignerEmpty) SignAdminCSR(_ context.Context, _, _ string, _ []byte) ([]byte, error) {
	return []byte{}, nil
}

// ---------------------------------------------------------------------------
// Internal function branch coverage (same package — white-box tests)
// ---------------------------------------------------------------------------

// TestCredentialsForStrategy_InvalidStrategyReturnsError exercises the
// validateStrategy error guard inside credentialsForStrategy directly. This
// branch is unreachable through the public API (BootstrapSystem calls
// validateStrategy first), but it is a defensive check that must be covered.
func TestCredentialsForStrategy_InvalidStrategyReturnsError(t *testing.T) {
	_, err := credentialsForStrategy(t.Context(), seal.StrategyID("tpm2"), passphraseBuffer(t, "ignored"))
	var want *UnsupportedStrategyError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *UnsupportedStrategyError", err)
	}
}

// ---------------------------------------------------------------------------
// The credential is destroyed when its lifetime ends (go-qrdb#429)
//
// credentialsForStrategy copies the passphrase out of the *secure.Buffer and
// moves it straight into a sealed enclave; the credential BootstrapSystem
// builds is the only holder of that material, and BootstrapSystem destroys it
// when its lifetime ends. Before go-quicraft#41 the secret was an ORDINARY,
// PAGEABLE HEAP SLICE and this section asserted it was zeroized; the
// property is the same — nothing of the passphrase survives BootstrapSystem —
// and the observation is now "the enclave is dead" rather than "the bytes are
// zero".
//
// retainingStrategy is what makes that observable. It embeds a REAL strategy —
// so the barrier genuinely initializes and the test exercises the production
// flow — and overrides SealRootKey to keep the credential's enclave rather
// than open it. After BootstrapSystem returns, that enclave must be destroyed.
//
// Delete the `defer seal.DestroyCredentials(creds)` and this fails.
// ---------------------------------------------------------------------------

type retainingStrategy struct {
	seal.SealingStrategy
	tb       testing.TB
	retained *enclave.SealedEnclave
	sawBytes int
}

func (r *retainingStrategy) SealRootKey(ctx context.Context, rootKey []byte, creds seal.Credentials) (*seal.SealedRootKey, error) {
	// The enclave itself, deliberately not a copy of its content. The whole
	// mechanism is that the barrier borrows the credential and the caller
	// destroys it after.
	r.retained = creds.Secret
	r.sawBytes = len(sealtest.Plaintext(r.tb, creds))
	return r.SealingStrategy.SealRootKey(ctx, rootKey, creds)
}

// assertDestroyed fails unless the retained credential enclave is dead. A nil
// enclave is a failure too: it means the strategy was never handed a
// credential, so the test would be proving nothing.
func assertDestroyed(t *testing.T, what string, e *enclave.SealedEnclave) {
	t.Helper()
	if e == nil {
		t.Fatalf("%s: the strategy was never handed a credential; this test proves nothing", what)
	}
	if e.IsAlive() {
		t.Fatalf("%s: the credential enclave is still alive — the passphrase was not destroyed", what)
	}
}

// TestBootstrapSystem_WipesTheCredentialCopy asserts the SUCCESS path.
func TestBootstrapSystem_WipesTheCredentialCopy(t *testing.T) {
	const passphrase = "correct horse battery staple"
	strategy := &retainingStrategy{SealingStrategy: seal.NewDeferredSoftwareStrategy(), tb: t}
	reg := newRegistry(t, strategy)

	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Strategy:          StrategyPassphrase,
		Passphrase:        passphraseBuffer(t, passphrase),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	if err != nil {
		t.Fatalf("BootstrapSystem: %v", err)
	}

	if strategy.sawBytes != len(passphrase) {
		t.Fatalf("the strategy saw a %d-byte credential, want %d — the enclave is not "+
			"the one credentialsForStrategy made", strategy.sawBytes, len(passphrase))
	}
	assertDestroyed(t, "BootstrapSystem credential", strategy.retained)
}

// TestBootstrapSystem_WipesTheCredentialCopyOnInitFailure covers the path a
// wipe placed after Initialize instead of deferred would miss. The strategy
// retains the credential and THEN fails, so the copy exists and
// BootstrapSystem returns through an error path.
func TestBootstrapSystem_WipesTheCredentialCopyOnInitFailure(t *testing.T) {
	const passphrase = "a passphrase the barrier will refuse"
	strategy := &failingRetainingStrategy{
		retainingStrategy: retainingStrategy{SealingStrategy: seal.NewDeferredSoftwareStrategy(), tb: t},
	}
	reg := newRegistry(t, strategy)

	_, err := BootstrapSystem(context.Background(), SystemConfig{
		MTLSBindingPolicy: auth.MTLSBindingEnrolled,
		Strategy:          StrategyPassphrase,
		Passphrase:        passphraseBuffer(t, passphrase),
	}, Dependencies{
		Tenants: &fakeTenants{}, Users: &fakeUsers{}, Roles: &fakeRoles{},
		Barriers: reg, Logger: discardLogger(),
	})
	if err == nil {
		t.Fatal("expected BootstrapSystem to fail when the strategy refuses the seal")
	}

	assertDestroyed(t, "BootstrapSystem credential on the error path", strategy.retained)
}

// failingRetainingStrategy retains the credential and then refuses to seal, so
// BootstrapSystem leaves through SystemBarrierInitError with the copy already
// made.
type failingRetainingStrategy struct {
	retainingStrategy
}

func (f *failingRetainingStrategy) SealRootKey(_ context.Context, _ []byte, creds seal.Credentials) (*seal.SealedRootKey, error) {
	f.retained = creds.Secret
	return nil, errRefusedSeal
}

var errRefusedSeal = errors.New("bootstrap_test: strategy refuses to seal")

// TestCredentialsForStrategy_DestroyedBufferIsAMissingPassphrase pins the
// branch the *secure.Buffer conversion introduced (go-qrdb#429). A destroyed
// buffer is indistinguishable from no buffer at this call site, and both must
// surface as MissingPassphraseError rather than as a barrier-init failure —
// an operator who supplied no passphrase should not be told the barrier
// broke.
func TestCredentialsForStrategy_DestroyedBufferIsAMissingPassphrase(t *testing.T) {
	buf, err := secure.NewBufferFromBytes([]byte("gone in a moment"))
	if err != nil {
		t.Fatalf("secure.NewBufferFromBytes: %v", err)
	}
	buf.Destroy()

	_, err = credentialsForStrategy(t.Context(), StrategyPassphrase, buf)
	var want *MissingPassphraseError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *MissingPassphraseError", err)
	}
}

// TestCredentialsForStrategy_CopiesOutOfTheBuffer verifies the borrow yields
// the material and that the returned Secret is a COPY: destroying the buffer
// afterwards must not blank the credentials already handed to the barrier.
func TestCredentialsForStrategy_CopiesOutOfTheBuffer(t *testing.T) {
	buf := passphraseBuffer(t, "correct horse battery staple")

	creds, err := credentialsForStrategy(t.Context(), StrategyPassphrase, buf)
	if err != nil {
		t.Fatalf("credentialsForStrategy: %v", err)
	}
	defer seal.DestroyCredentials(creds)
	if got := string(sealtest.Plaintext(t, creds)); got != "correct horse battery staple" {
		t.Fatalf("Secret = %q, want the supplied passphrase", got)
	}
	buf.Destroy()
	if got := string(sealtest.Plaintext(t, creds)); got != "correct horse battery staple" {
		t.Errorf("Secret aliased the enclave: destroying the buffer changed it to %q", got)
	}
}

// TestCredentialsForStrategy_NonPassphraseStrategyNeedsNoBuffer confirms that
// none and shamir demand no credential at all, so a nil buffer is not an
// error for them.
func TestCredentialsForStrategy_NonPassphraseStrategyNeedsNoBuffer(t *testing.T) {
	for _, strategy := range []seal.StrategyID{StrategyNone, StrategyShamir} {
		creds, err := credentialsForStrategy(t.Context(), strategy, nil)
		if err != nil {
			t.Fatalf("credentialsForStrategy(%q, nil): %v", strategy, err)
		}
		if creds.Secret != nil {
			t.Errorf("credentialsForStrategy(%q, nil) returned a secret", strategy)
		}
	}
}

// TestBuildTenantStrategy_DefaultCaseReturnsError exercises the default
// (unknown strategy) branch of buildTenantStrategy directly. This is a
// defensive branch unreachable through the public API because
// InitializeTenantBarrier calls validateStrategy first.
func TestBuildTenantStrategy_DefaultCaseReturnsError(t *testing.T) {
	_, err := buildTenantStrategy(seal.StrategyID("vault"))
	var want *UnsupportedStrategyError
	if !errors.As(err, &want) {
		t.Fatalf("err = %v, want *UnsupportedStrategyError", err)
	}
}

// TestBootstrapSystem_ForwardsMTLSBindingPolicy pins that the system bootstrap
// hands auth.mtls_binding_policy to first-boot: under "required" with no
// CertBinder, first-boot refuses before creating the system tenant, and the
// refusal reaches the caller wrapped as a FirstBootError.
func TestBootstrapSystem_ForwardsMTLSBindingPolicy(t *testing.T) {
	reg := newRegistry(t, seal.NewNoneStrategy())
	tenants := &fakeTenants{}
	users := &fakeUsersWithLookup{lookupErr: &user.UserNotFoundError{Lookup: "admin"}}

	_, err := BootstrapSystem(context.Background(), SystemConfig{
		Strategy:          StrategyNone,
		MTLSBindingPolicy: auth.MTLSBindingRequired,
	}, Dependencies{
		Tenants: tenants, Users: users, Roles: &fakeRoles{},
		CA:       &fakeCA{cert: realCertPEM(t), key: []byte("KEY")},
		Barriers: reg, Logger: discardLogger(),
	})
	var fbErr *FirstBootError
	var refusal *firstboot.AdminCertBindingRequiredError
	if !errors.As(err, &fbErr) || !errors.As(err, &refusal) {
		t.Fatalf("err = %v, want a FirstBootError wrapping *firstboot.AdminCertBindingRequiredError", err)
	}
	if refusal.Reason != firstboot.AdminBindingNoBinder {
		t.Fatalf("reason = %q, want %q", refusal.Reason, firstboot.AdminBindingNoBinder)
	}
	if len(tenants.createCalls) != 0 || len(users.createCalls) != 0 {
		t.Fatalf("refused first boot wrote: tenants=%d users=%d", len(tenants.createCalls), len(users.createCalls))
	}
}
