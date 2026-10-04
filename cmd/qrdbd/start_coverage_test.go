// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/capabilities"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	tenantpkg "github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// fileExists tests
// ---------------------------------------------------------------------------

func TestFileExists_RegularFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "testfile.txt")
	require.NoError(t, os.WriteFile(tmpFile, []byte("data"), 0o600))
	assert.True(t, fileExists(tmpFile))
}

func TestFileExists_NonExistent(t *testing.T) {
	assert.False(t, fileExists(filepath.Join(t.TempDir(), "nofile.txt")))
}

func TestFileExists_Directory(t *testing.T) {
	dir := t.TempDir()
	assert.False(t, fileExists(dir))
}

// ---------------------------------------------------------------------------
// generateAdminClientCert tests
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// loadTLSFromFiles tests
// ---------------------------------------------------------------------------

func TestLoadTLSFromFiles_Success(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	require.NoError(t, os.WriteFile(caPath, bundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(certPath, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, bundle.KeyPEM, 0o600))

	result, err := loadTLSFromFiles(caPath, certPath, keyPath)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.NotNil(t, result.TLSConfig)
	assert.NotNil(t, result.CACertPool)
	assert.NotEmpty(t, result.CACertPEM)
	assert.NotEmpty(t, result.CertPEM)
	assert.NotEmpty(t, result.KeyPEM)
}

func TestLoadTLSFromFiles_MissingCAFile(t *testing.T) {
	dir := t.TempDir()
	_, err := loadTLSFromFiles(
		filepath.Join(dir, "missing-ca.pem"),
		filepath.Join(dir, "cert.pem"),
		filepath.Join(dir, "key.pem"),
	)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

func TestLoadTLSFromFiles_MissingCertFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, []byte("ca-data"), 0o600))

	_, err := loadTLSFromFiles(
		caPath,
		filepath.Join(dir, "missing-cert.pem"),
		filepath.Join(dir, "key.pem"),
	)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

func TestLoadTLSFromFiles_MissingKeyFile(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "cert.pem")
	require.NoError(t, os.WriteFile(caPath, []byte("ca-data"), 0o600))
	require.NoError(t, os.WriteFile(certPath, []byte("cert-data"), 0o600))

	_, err := loadTLSFromFiles(
		caPath,
		certPath,
		filepath.Join(dir, "missing-key.pem"),
	)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

func TestLoadTLSFromFiles_InvalidKeyPair(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// Write valid-looking PEM but mismatched cert/key.
	require.NoError(t, os.WriteFile(caPath, []byte("-----BEGIN CERTIFICATE-----\nfoo\n-----END CERTIFICATE-----\n"), 0o600))
	require.NoError(t, os.WriteFile(certPath, []byte("not-a-valid-cert"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("not-a-valid-key"), 0o600))

	_, err := loadTLSFromFiles(caPath, certPath, keyPath)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

func TestLoadTLSFromFiles_InvalidCACertPool(t *testing.T) {
	// Valid cert/key pair but CA PEM that contains no valid certificates.
	bundle, err := generateCATLS()
	require.NoError(t, err)

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// Write invalid CA PEM (not a valid certificate).
	require.NoError(t, os.WriteFile(caPath, []byte("not a valid PEM cert"), 0o600))
	require.NoError(t, os.WriteFile(certPath, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyPath, bundle.KeyPEM, 0o600))

	_, err = loadTLSFromFiles(caPath, certPath, keyPath)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

// ---------------------------------------------------------------------------
// loadCACertPool tests
// ---------------------------------------------------------------------------

func TestLoadCACertPool_EmptyPath_Coverage(t *testing.T) {
	pool, err := loadCACertPool("")
	require.NoError(t, err)
	assert.Nil(t, pool)
}

func TestLoadCACertPool_ValidCA(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	require.NoError(t, os.WriteFile(caPath, bundle.CACertPEM, 0o600))

	pool, err := loadCACertPool(caPath)
	require.NoError(t, err)
	require.NotNil(t, pool)
}

func TestLoadCACertPool_MissingFile(t *testing.T) {
	_, err := loadCACertPool(filepath.Join(t.TempDir(), "nonexistent.pem"))
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

func TestLoadCACertPool_InvalidPEM_Coverage(t *testing.T) {
	dir := t.TempDir()
	caPath := filepath.Join(dir, "bad-ca.pem")
	require.NoError(t, os.WriteFile(caPath, []byte("not valid PEM"), 0o600))

	_, err := loadCACertPool(caPath)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
	assert.True(t, errors.Is(loadErr.Err, errNoCertsInPEM))
}

// ---------------------------------------------------------------------------
// applyConfigToFlags TLS fields coverage
// ---------------------------------------------------------------------------

func TestApplyConfigToFlags_TLSFieldsFromConfig(t *testing.T) {
	cmd := newStartCmd()

	cfg := &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{Host: "0.0.0.0", AdminPort: 63002},
		TLS: qrdbconfig.TLSConfig{
			CAFile:   "/etc/tls/ca.pem",
			CertFile: "/etc/tls/cert.pem",
			KeyFile:  "/etc/tls/key.pem",
		},
	}

	f := &startFlags{host: testBindHost}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "/etc/tls/ca.pem", f.tlsCAFile)
	assert.Equal(t, "/etc/tls/cert.pem", f.tlsCertFile)
	assert.Equal(t, "/etc/tls/key.pem", f.tlsKeyFile)
}

func TestApplyConfigToFlags_TLSFieldsCLIPrecedence(t *testing.T) {
	cmd := newStartCmd()
	require.NoError(t, cmd.Flags().Set("tls-ca-file", "/cli/ca.pem"))
	require.NoError(t, cmd.Flags().Set("tls-cert-file", "/cli/cert.pem"))
	require.NoError(t, cmd.Flags().Set("tls-key-file", "/cli/key.pem"))

	cfg := &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{Host: "0.0.0.0", AdminPort: 63002},
		TLS: qrdbconfig.TLSConfig{
			CAFile:   "/yaml/ca.pem",
			CertFile: "/yaml/cert.pem",
			KeyFile:  "/yaml/key.pem",
		},
	}

	f := &startFlags{
		tlsCAFile:   "/cli/ca.pem",
		tlsCertFile: "/cli/cert.pem",
		tlsKeyFile:  "/cli/key.pem",
		host:        testBindHost,
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "/cli/ca.pem", f.tlsCAFile)
	assert.Equal(t, "/cli/cert.pem", f.tlsCertFile)
	assert.Equal(t, "/cli/key.pem", f.tlsKeyFile)
}

func TestApplyConfigToFlags_EmptyTLSFieldsNotOverridden(t *testing.T) {
	cmd := newStartCmd()

	cfg := &qrdbconfig.Config{
		Server: qrdbconfig.ServerConfig{Host: "0.0.0.0", AdminPort: 63002},
		TLS: qrdbconfig.TLSConfig{
			CAFile:   "",
			CertFile: "",
			KeyFile:  "",
		},
	}

	f := &startFlags{
		tlsCAFile:   "/existing/ca.pem",
		tlsCertFile: "/existing/cert.pem",
		tlsKeyFile:  "/existing/key.pem",
		host:        testBindHost,
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "/existing/ca.pem", f.tlsCAFile)
	assert.Equal(t, "/existing/cert.pem", f.tlsCertFile)
	assert.Equal(t, "/existing/key.pem", f.tlsKeyFile)
}

// ---------------------------------------------------------------------------
// runFirstBoot tests (mock-based)
// ---------------------------------------------------------------------------

// mockUserServicer satisfies user.UserServicer for bootstrap testing.
type mockUserServicer struct {
	getUserByUsernameFn func(ctx context.Context, tenantID, username string) (*user.User, error)
	getUserByEmailFn    func(ctx context.Context, tenantID, email string) (*user.User, error)
	createUserFn        func(ctx context.Context, req *user.CreateUserRequest) (*user.User, error)
	addCertBindingFn    func(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error)
}

func (m *mockUserServicer) CreateUser(ctx context.Context, req *user.CreateUserRequest) (*user.User, error) {
	if m.createUserFn != nil {
		return m.createUserFn(ctx, req)
	}
	return &user.User{ID: 1, Username: req.Username, TenantID: req.TenantID}, nil
}

func (m *mockUserServicer) GetUser(_ context.Context, _ uint64) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) GetUserByUsername(ctx context.Context, tenantID, username string) (*user.User, error) {
	if m.getUserByUsernameFn != nil {
		return m.getUserByUsernameFn(ctx, tenantID, username)
	}
	return nil, &user.UserNotFoundError{Lookup: username}
}

func (m *mockUserServicer) GetUserByEmail(ctx context.Context, tenantID, email string) (*user.User, error) {
	normalized := user.NormalizeEmail(email)
	if m.getUserByEmailFn != nil {
		return m.getUserByEmailFn(ctx, tenantID, normalized)
	}
	return nil, &user.UserNotFoundError{Lookup: normalized}
}

func (m *mockUserServicer) UpdateUser(_ context.Context, _ *user.UpdateUserRequest) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) DeleteUser(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockUserServicer) ListUsers(_ context.Context, _ *user.ListUsersRequest) (*user.ListUsersResponse, error) {
	return nil, nil
}

func (m *mockUserServicer) EnableUser(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockUserServicer) DisableUser(_ context.Context, _ uint64) error {
	return nil
}

func (m *mockUserServicer) UserByCertThumbprint(_ context.Context, _ string) (*user.User, error) {
	return nil, nil
}

func (m *mockUserServicer) AddCertBinding(ctx context.Context, userID uint64, thumbprint string, notAfterUnix int64) (*user.User, error) {
	if m.addCertBindingFn != nil {
		return m.addCertBindingFn(ctx, userID, thumbprint, notAfterUnix)
	}
	return nil, nil
}

func (m *mockUserServicer) RemoveCertBinding(_ context.Context, _ uint64, _ string) (*user.User, error) {
	return nil, nil
}

// mockRoleServicer satisfies role.RoleServicer for bootstrap testing.
type mockRoleServicer struct {
	createRoleFn    func(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error)
	getRoleByNameFn func(ctx context.Context, tenantID, name string) (*role.Role, error)
}

func (m *mockRoleServicer) CreateRole(ctx context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
	if m.createRoleFn != nil {
		return m.createRoleFn(ctx, req)
	}
	return &role.Role{ID: "r-" + req.Name, Name: req.Name, TenantID: req.TenantID}, nil
}

func (m *mockRoleServicer) GetRole(_ context.Context, _ string) (*role.Role, error) {
	return nil, nil
}

func (m *mockRoleServicer) GetRoleByName(ctx context.Context, tenantID, name string) (*role.Role, error) {
	if m.getRoleByNameFn != nil {
		return m.getRoleByNameFn(ctx, tenantID, name)
	}
	return &role.Role{ID: "r-" + name, Name: name, TenantID: tenantID}, nil
}

func (m *mockRoleServicer) UpdateRole(_ context.Context, _ *role.UpdateRoleRequest) (*role.Role, error) {
	return nil, nil
}

func (m *mockRoleServicer) DeleteRole(_ context.Context, _ string) error {
	return nil
}

func (m *mockRoleServicer) ListRoles(_ context.Context, _ *role.ListRolesRequest) (*role.ListRolesResponse, error) {
	return nil, nil
}

func (m *mockRoleServicer) ResolvePermissions(_ context.Context, _ []string) ([]permission.Permission, error) {
	return nil, nil
}

// mockTenantServicer satisfies tenant.TenantServicer for first-boot
// orchestration tests. CreateTenant defaults to a happy-path response
// that lets runFirstBoot proceed; tests override via createTenantFn when
// they need to inject specific behavior.
type mockTenantServicer struct {
	createTenantFn func(ctx context.Context, id, name string, isolationType tenantpkg.IsolationType) (*tenantpkg.TenantInfo, error)
}

func (m *mockTenantServicer) CreateTenant(ctx context.Context, id, name string, isolationType tenantpkg.IsolationType, _ ...tenantpkg.CreateTenantOption) (*tenantpkg.TenantInfo, error) {
	if m.createTenantFn != nil {
		return m.createTenantFn(ctx, id, name, isolationType)
	}
	return &tenantpkg.TenantInfo{ID: id, Name: name, IsolationType: isolationType}, nil
}

func (m *mockTenantServicer) GetTenant(_ context.Context, _ string) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantServicer) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenantpkg.DeletedTenantsPage, error) {
	return &tenantpkg.DeletedTenantsPage{}, nil
}

func (m *mockTenantServicer) ReconcileDeletedTenants(_ context.Context) (*tenantpkg.ReconcileReport, error) {
	return &tenantpkg.ReconcileReport{}, nil
}

func (m *mockTenantServicer) ListTenants(_ context.Context) ([]tenantpkg.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantServicer) DeleteTenant(_ context.Context, _ string) error { return nil }

func (m *mockTenantServicer) UpdateTenant(_ context.Context, _ string, _ tenantpkg.TenantPatch) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantServicer) UpdateQuotas(_ context.Context, _ string, _ *quota.Quotas) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantServicer) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenantpkg.TenantInfo, error) {
	return &tenantpkg.TenantInfo{ID: id, Entitlements: e}, nil
}

func (m *mockTenantServicer) TenantBarrierInit(_ context.Context, _ string, _ []byte, _, _ int) (*tenantpkg.TenantBarrierInitResult, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierInitWithStrategy(_ context.Context, _ string, _ []byte, _, _ int, _ seal.StrategyID) (*tenantpkg.TenantBarrierInitResult, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierUnseal(_ context.Context, _ string, _ []byte) error {
	return nil
}

func (m *mockTenantServicer) TenantBarrierSeal(_ context.Context, _ string) error { return nil }

func (m *mockTenantServicer) TenantBarrierStatus(_ context.Context, _ string) (*tenantpkg.TenantBarrierStatus, error) {
	return nil, nil
}
func (m *mockTenantServicer) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
func (m *mockTenantServicer) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantServicer) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenantpkg.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantServicer) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantServicer) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *mockTenantServicer) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *mockTenantServicer) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (m *mockTenantServicer) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}

// errHolderQuorumStub answers the share-holder quorum methods, which no test
// using this double drives.
var errHolderQuorumStub = errors.New("mockTenantServicer: share-holder quorum not modelled")

func (m *mockTenantServicer) TenantBarrierQuorumBegin(_ context.Context, _ string, _ seal.QuorumOp, _, _ string) (*ceremony.QuorumChallenge, error) {
	return nil, errHolderQuorumStub
}
func (m *mockTenantServicer) TenantBarrierQuorumSubmit(_ context.Context, _ string, _ *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	return nil, errHolderQuorumStub
}
func (m *mockTenantServicer) TenantBarrierRekeyStart(_ context.Context, _ string, _ int, _ seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	return nil, errHolderQuorumStub
}
func (m *mockTenantServicer) TenantBarrierOfficerRevoke(_ context.Context, _, _ string, _ int) (*seal.QuorumRoundInfo, error) {
	return nil, errHolderQuorumStub
}

// ---------------------------------------------------------------------------
// runFirstBoot: admin already exists
// ---------------------------------------------------------------------------

// TestRunFirstBoot_AdminAlreadyExists is the fast-path case: an admin that
// is already the System user AND already holds a live certificate binding
// is skipped entirely (go-qrdb#106/#192 round 2 narrowed this from "admin
// exists" to "admin exists AND is already System"; go-qrdb#548 review item
// 2 narrowed it again to also require a live binding -- an admin that
// exists, is System, but holds NO live binding falls through to repair
// instead; see TestRunFirstBoot_AdminSystemButUnbound_FallsThroughToRepair).
// An admin that exists but is not yet System falls through to the full
// bootstrap path too, so it gets reconciled; see
// TestRunFirstBoot_AdminExistsButNotSystem_FallsThroughToReconcile.
func TestRunFirstBoot_AdminAlreadyExists(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return &user.User{
				ID: 9001, Username: "admin", System: true, Lifecycle: user.LifecycleActive,
				CertBindings: []user.CertBinding{{Thumbprint: "abc", NotAfter: time.Now().Add(365 * 24 * time.Hour)}},
			}, nil
		},
	}
	roleSvc := &mockRoleServicer{}
	node := &mockRaftNode{} // default: leader ready

	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
}

// TestRunFirstBoot_AdminSystemButUnbound_FallsThroughToRepair is the
// go-qrdb#548 review's item 2 fix: an admin that already exists and is
// already the System user, but holds NO live certificate binding -- the
// B6 gap a crash between createAdminUser and BindAdminCert leaves on an
// earlier run -- must NOT be skipped by the fast path. runFirstBoot must
// fall through to the full bootstrap path so pkg/firstboot's own
// needsBinding repair gets a chance to run, exactly as the not-yet-System
// case already does. Asserted the same way its sibling is: CreateUser is
// called, proving the fall-through happened.
func TestRunFirstBoot_AdminSystemButUnbound_FallsThroughToRepair(t *testing.T) {
	createCalled := false
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			// System: true, but CertBindings is empty -- created, never bound.
			return &user.User{ID: 9001, Username: "admin", System: true}, nil
		},
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			createCalled = true
			return &user.User{ID: 9001, Username: req.Username, TenantID: req.TenantID, System: true}, nil
		},
	}
	roleSvc := &mockRoleServicer{}
	node := &mockRaftNode{} // default: leader ready

	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
	if !createCalled {
		t.Fatal("expected runFirstBoot to fall through to the bootstrap path (CreateUser) for a System admin with no live certificate binding")
	}
}

// selfSignedAdminCertPEM mints a self-signed "CN=admin" certificate and
// returns it PEM-encoded alongside the SPKI thumbprint a binding records.
// Self-signed so the SAME bytes can stand in for both the presented
// certificate and the trust anchor it chains to (clientCACertPEM) --
// adoptPresentedAdminCert's chain check is satisfied without a second CA.
func selfSignedAdminCertPEM(t *testing.T) (certPEM []byte, thumbprint string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "admin", OrganizationalUnit: []string{tenantid.System, "administrator"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), user.ComputeSPKIThumbprint(cert)
}

// TestRunFirstBoot_CrashRecovery_UnboundAdminPlusPendingRecord_Binds is the
// go-qrdb#548 review's item 2 qrdbd-level crash test: an admin created on an
// earlier run but never bound (System: true, no live CertBindings -- the
// B6 gap), PLUS a pending admin-certificate record left by that earlier
// run's ceremony-initialize, boots and gets bound: runFirstBoot's fast path
// must fall through to Phase 2.5 (which resolves the pending record) and
// Phase 3 (whose needsBinding repair binds it), not skip straight to
// "nothing to do" because the admin already carries System: true.
func TestRunFirstBoot_CrashRecovery_UnboundAdminPlusPendingRecord_Binds(t *testing.T) {
	certPEM, wantThumbprint := selfSignedAdminCertPEM(t)

	var boundUserID uint64
	var boundThumbprint string
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			// System: true, but CertBindings is empty -- created, never bound,
			// exactly the state a crash between createAdminUser and
			// BindAdminCert leaves on an earlier run.
			return &user.User{ID: 9001, Username: "admin", System: true}, nil
		},
		// The mock's CreateUser always "succeeds" rather than modelling a
		// real store's username-uniqueness constraint (UsernameExistsError
		// -> reconcileExistingAdmin), the same simplification
		// TestRunFirstBoot_AdminSystemButUnbound_FallsThroughToRepair makes:
		// returning the SAME id the lookup above reports stands in for "this
		// is the existing admin, not a new one" without needing the fuller
		// username-collision fixture pkg/firstboot/firstboot_test.go already
		// covers end to end with a faithful store double.
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			return &user.User{ID: 9001, Username: req.Username, TenantID: req.TenantID, System: true}, nil
		},
		addCertBindingFn: func(_ context.Context, userID uint64, thumbprint string, _ int64) (*user.User, error) {
			boundUserID = userID
			boundThumbprint = thumbprint
			return &user.User{ID: userID, Username: "admin", System: true}, nil
		},
	}
	roleSvc := &mockRoleServicer{}
	node := &mockRaftNode{} // default: leader ready

	// The pending record ceremony-initialize wrote on the earlier,
	// interrupted run: present now, under MTLSBindingEnrolled a single Get
	// resolves it (firstboot.ResolveAdminCertForFirstBoot), no replication
	// wait needed.
	pendingStore := &mockKVServicer{
		getFn: func(_ context.Context, key string) ([]byte, error) {
			return certPEM, nil
		},
	}

	err := runFirstBoot(
		context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{},
		false, nil, "", auth.MTLSBindingEnrolled,
		pendingStore, nil, certPEM, // ceremonyCA nil; clientCACertPEM is the self-signed cert's own trust anchor
	)
	require.NoError(t, err)
	assert.Equal(t, uint64(9001), boundUserID, "the existing, still-unbound admin must be the one bound, not a freshly minted user")
	assert.Equal(t, wantThumbprint, boundThumbprint, "the bound thumbprint must come from the pending record's certificate")
}

// TestRunFirstBoot_AdminExistsButNotSystem_FallsThroughToReconcile is the
// round-2 regression test: an admin that exists but is NOT yet the System
// user must NOT be skipped — runFirstBoot must fall through to the full,
// idempotent bootstrap path so pkg/firstboot's reconciliation can promote
// it. Asserted by observing that CreateUser IS called again (proving the
// fall-through happened) rather than by a full end-to-end promotion, which
// pkg/firstboot/firstboot_test.go and pkg/user/system_user_test.go already
// cover with doubles that implement PromoteToSystemUser.
func TestRunFirstBoot_AdminExistsButNotSystem_FallsThroughToReconcile(t *testing.T) {
	createCalled := false
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return &user.User{ID: 9001, Username: "admin", System: false}, nil
		},
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			createCalled = true
			return &user.User{ID: 9001, Username: req.Username, TenantID: req.TenantID}, nil
		},
	}
	roleSvc := &mockRoleServicer{}
	node := &mockRaftNode{} // default: leader ready

	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
	if !createCalled {
		t.Fatal("expected runFirstBoot to fall through to the bootstrap path (CreateUser) for a non-System existing admin")
	}
}

// ---------------------------------------------------------------------------
// runFirstBoot: fresh bootstrap (UserNotFound -> create roles + admin)
// ---------------------------------------------------------------------------

func TestRunFirstBoot_FreshBootstrap(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			return &user.User{ID: 9002, Username: req.Username, TenantID: req.TenantID}, nil
		},
	}

	var createdRoles []string
	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			createdRoles = append(createdRoles, req.Name)
			return &role.Role{ID: "r-" + req.Name, Name: req.Name, TenantID: req.TenantID}, nil
		},
	}

	node := &mockRaftNode{} // default: leader ready
	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
	// All built-in roles should have been created.
	assert.True(t, len(createdRoles) > 0, "expected at least one role to be created")
}

// ---------------------------------------------------------------------------
// runFirstBoot: role already exists (RoleNameExistsError tolerated)
// ---------------------------------------------------------------------------

func TestRunFirstBoot_RoleAlreadyExists(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			return &user.User{ID: 9001, Username: req.Username}, nil
		},
	}

	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
		},
		// The record read back must be field-for-field the canonical
		// built-in definition (go-qrdb#555's provenance check), id
		// included: pkg/role's requireCanonicalSystemRole refuses to let a
		// reserved name be created under any other id, so this models the
		// realistic case a name collision means -- a concurrent first-boot
		// attempt that won the create race -- not a cluster seeded under a
		// retired id scheme, which this build has no tolerance for at all
		// (see TestRunFirstBoot_RoleExistsUnexplainedIDRefused).
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			return canonicalRoleForTest(tenantID, name), nil
		},
	}

	node := &mockRaftNode{} // default: leader ready
	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
}

// TestRunFirstBoot_RoleExistsUnexplainedIDRefused is the regression test for
// go-qrdb's no-compat-cleanup lane: a name collision whose persisted record
// does NOT carry the canonical id is never a legitimate idempotent
// re-attempt -- pkg/role's requireCanonicalSystemRole refuses to let a
// reserved name be created under any other id, so no supported code path
// produces one -- and first boot must refuse it rather than silently
// granting the admin a role id that denotes no record at all.
func TestRunFirstBoot_RoleExistsUnexplainedIDRefused(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}
	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
		},
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			canonical := canonicalRoleForTest(tenantID, name)
			canonical.ID = "an-unexplained-id-" + name
			return canonical, nil
		},
	}

	node := &mockRaftNode{} // default: leader ready
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	err := runFirstBoot(ctx, node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "register role")
}

// canonicalRoleForTest returns the canonical built-in role definition for
// name (Admin, Owner, or one of role.BuiltInRoles' other names), matching
// what registerBuiltInRoles' provenance check (go-qrdb#555) requires a
// resolver's answer to equal: TenantID, Name, System, and the exact
// permission set. Callers that need to model a legacy or malformed record
// mutate the returned copy's ID or other fields afterward.
func canonicalRoleForTest(tenantID, name string) *role.Role {
	for _, r := range role.BuiltInRoles(tenantID) {
		if r.Name == name {
			cp := *r
			return &cp
		}
	}
	if owner := role.OwnerRole(tenantID); owner.Name == name {
		cp := *owner
		return &cp
	}
	panic("canonicalRoleForTest: unknown built-in role name " + name)
}

// ---------------------------------------------------------------------------
// runFirstBoot: role create fails with real error
// ---------------------------------------------------------------------------

func TestRunFirstBoot_RoleCreateError(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}

	sentinel := errors.New("role create failed")
	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, sentinel
		},
	}

	// A role-create failure is this node's own, not a NotLeader condition, so
	// it propagates with its cause rather than becoming a replication wait
	// (go-qrdb#86).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	node := &mockRaftNode{} // default: leader ready
	err := runFirstBoot(ctx, node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// runFirstBoot: admin user create fails
// ---------------------------------------------------------------------------

func TestRunFirstBoot_AdminCreateError(t *testing.T) {
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, errors.New("user create failed")
		},
	}

	roleSvc := &mockRoleServicer{}

	// An admin-create failure is this node's own, not a NotLeader condition,
	// so it propagates with its cause (go-qrdb#86).
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	node := &mockRaftNode{} // default: leader ready
	err := runFirstBoot(ctx, node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.False(t, errors.As(err, &abe),
		"a local create failure was reported as an admin-replication wait")
	assert.Contains(t, err.Error(), "user create failed")
}

// ---------------------------------------------------------------------------
// runFirstBoot: admin user create returns UsernameExistsError (tolerated)
// ---------------------------------------------------------------------------

func TestRunFirstBoot_AdminUsernameExists(t *testing.T) {
	userSvc := &mockUserServicer{
		// The resolver must agree with CreateUser that "admin" already
		// exists — go-qrdb#555's provenance check (createAdminUser) now
		// verifies a username collision by reading the record back, exactly
		// as a real user.UserServicer would let it, rather than trusting
		// UsernameExistsError alone. A resolver contradicting its own
		// CreateUser (not-found here, already-exists there) models a store
		// no real implementation is.
		// The record is the admin first boot itself would have made (its
		// Admin role, under the one id that role can ever be persisted
		// under -- role.SystemRoleID, per pkg/role's
		// requireCanonicalSystemRole -- and a live lifecycle), created
		// before the System flag existed: the fast path must not skip it,
		// and the idempotent path must accept it as the admin rather than a
		// squatter.
		getUserByUsernameFn: func(_ context.Context, tenantID, username string) (*user.User, error) {
			return &user.User{
				ID: 1, Username: username, TenantID: tenantID,
				RoleIDs:   []string{role.SystemRoleID(tenantID, role.RoleNameAdmin)},
				Lifecycle: user.LifecycleActive,
			}, nil
		},
		createUserFn: func(_ context.Context, _ *user.CreateUserRequest) (*user.User, error) {
			return nil, &user.UsernameExistsError{Username: "admin", TenantID: "system"}
		},
	}

	roleSvc := &mockRoleServicer{}

	node := &mockRaftNode{} // default: leader ready
	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// runFirstBoot: leader election wait succeeds after retries
// ---------------------------------------------------------------------------

func TestRunFirstBoot_LeaderElectionWait(t *testing.T) {
	// Simulate leader not ready for a few attempts, then ready.
	attempts := 0
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			attempts++
			if attempts <= 3 {
				return 0, false, nil
			}
			return 1, true, nil
		},
	}

	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			return &user.User{ID: 9001, Username: req.Username, TenantID: req.TenantID}, nil
		},
	}
	roleSvc := &mockRoleServicer{}

	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
	assert.Greater(t, attempts, 3)
}

// ---------------------------------------------------------------------------
// runFirstBoot: context canceled during leader wait
// ---------------------------------------------------------------------------

func TestRunFirstBoot_ContextCanceledDuringLeaderWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			attempts++
			if attempts >= 2 {
				cancel()
			}
			return 0, false, nil
		},
	}

	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}
	roleSvc := &mockRoleServicer{}

	err := runFirstBoot(ctx, node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for leader", abe.Step)
}

// ---------------------------------------------------------------------------
// runFirstBoot: GetUserByUsername returns unexpected error
// ---------------------------------------------------------------------------

func TestRunFirstBoot_UnexpectedStoreError(t *testing.T) {
	// When adminExists returns false (due to any error), tryCreate runs
	// and also fails with a storage error. The function falls back to
	// waitForAdminReplication which times out.
	sentinel := errors.New("storage corrupted")
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, sentinel
		},
	}
	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, sentinel
		},
	}
	node := &mockRaftNode{}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := runFirstBoot(ctx, node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	require.ErrorIs(t, err, sentinel,
		"a storage fault must surface as itself, not as a replication timeout")
}

// ---------------------------------------------------------------------------
// runFirstBoot: RoleNameExistsError still binds the admin to canonical IDs
// ---------------------------------------------------------------------------

// TestRunFirstBoot_RoleExistsUsesCanonicalIDs covers the RoleNameExistsError
// path specifically (as opposed to TestRunFirstBoot_UnseededRolesUseDerivedIDs'
// fresh-create path): every built-in role already exists -- a concurrent
// first-boot attempt won the create race -- and the admin grant must still
// bind to the canonical role.SystemRoleID derivation, because that is the
// ONLY id a role under a reserved name can ever be persisted under
// (pkg/role's requireCanonicalSystemRole). There is no alternate
// "persisted" id to prefer; see TestRunFirstBoot_RoleExistsUnexplainedIDRefused
// for what happens when the persisted record's id does not match.
func TestRunFirstBoot_RoleExistsUsesCanonicalIDs(t *testing.T) {
	var capturedRoleIDs []string
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			capturedRoleIDs = req.RoleIDs
			return &user.User{ID: 9001, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs}, nil
		},
	}

	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, req *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &role.RoleNameExistsError{Name: req.Name, TenantID: req.TenantID}
		},
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			return canonicalRoleForTest(tenantID, name), nil
		},
	}
	node := &mockRaftNode{} // default: leader ready

	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)

	// Under the standard posture (strictSoD=false) that is the Admin@system
	// grant plus the Owner@system convenience grant.
	require.Len(t, capturedRoleIDs, 2)
	assert.Equal(t, role.SystemRoleID(tenantid.System, role.RoleNameAdmin), capturedRoleIDs[0])
	assert.Equal(t, role.SystemRoleID(tenantid.System, role.RoleNameOwner), capturedRoleIDs[1])
}

// TestRunFirstBoot_UnseededRolesUseDerivedIDs is the companion case: when the
// store holds no role of that name, there is no persisted identifier to
// prefer and the deterministic derivation is what the grant uses.
func TestRunFirstBoot_UnseededRolesUseDerivedIDs(t *testing.T) {
	var capturedRoleIDs []string
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
		createUserFn: func(_ context.Context, req *user.CreateUserRequest) (*user.User, error) {
			capturedRoleIDs = req.RoleIDs
			return &user.User{ID: 9001, Username: req.Username, TenantID: req.TenantID, RoleIDs: req.RoleIDs}, nil
		},
	}
	roleSvc := &mockRoleServicer{
		getRoleByNameFn: func(_ context.Context, tenantID, name string) (*role.Role, error) {
			return nil, &role.RoleNotFoundError{ID: tenantID + "/" + name}
		},
	}

	err := runFirstBoot(context.Background(), &mockRaftNode{}, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)

	expectedAdminID := role.SystemRoleID(tenantid.System, role.RoleNameAdmin)
	expectedOwnerID := role.SystemRoleID(tenantid.System, role.RoleNameOwner)
	require.Len(t, capturedRoleIDs, 2)
	assert.Equal(t, expectedAdminID, capturedRoleIDs[0])
	assert.Equal(t, expectedOwnerID, capturedRoleIDs[1])
}

// ---------------------------------------------------------------------------
// runFirstBoot: follower falls back to replication wait
// ---------------------------------------------------------------------------

func TestRunFirstBoot_FollowerWaitsForReplication(t *testing.T) {
	// Simulate a follower: admin doesn't exist initially, role creation
	// fails (not leader), then admin appears via replication.
	calls := 0
	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			calls++
			if calls <= 2 {
				return nil, &user.UserNotFoundError{Lookup: "admin"}
			}
			// Admin replicated from leader.
			return &user.User{ID: 9001, Username: "admin"}, nil
		},
	}

	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, &service.NotLeaderError{ShardID: 1}
		},
	}
	node := &mockRaftNode{}

	err := runFirstBoot(context.Background(), node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
	assert.Greater(t, calls, 2, "should have polled for admin replication")
}

// ---------------------------------------------------------------------------
// runFirstBoot: no leader after max attempts
// ---------------------------------------------------------------------------

func TestRunFirstBoot_NoLeaderExhaustsRetries(t *testing.T) {
	node := &mockRaftNode{
		getLeaderFn: func(_ uint64) (uint64, bool, error) {
			return 0, false, nil // never returns a leader
		},
	}

	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}
	roleSvc := &mockRoleServicer{}

	// Use a short timeout so the test doesn't wait for all 30 attempts.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := runFirstBoot(ctx, node, 1, userSvc, roleSvc, &mockTenantServicer{}, false, nil, "", auth.MTLSBindingEnrolled, nil, nil, nil)
	require.Error(t, err)
	var abe *AuthBootstrapError
	require.True(t, errors.As(err, &abe))
	assert.Equal(t, "wait for leader", abe.Step)
}

// ---------------------------------------------------------------------------
// bootstrapUserRoleStores + bootstrapAuthEnforcement tests
// ---------------------------------------------------------------------------

func TestBootstrapAuth_Success(t *testing.T) {
	// Use a mock KVServicer that simulates a fresh cluster.
	// GetUserByUsername returns UserNotFound, so bootstrap creates roles + admin.
	store := make(map[string][]byte)
	mock := &mockKVServicer{
		putFn: func(_ context.Context, key string, value []byte) error {
			store[key] = value
			return nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if v, ok := store[key]; ok {
				return v, nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
		deleteFn: func(_ context.Context, key string) error {
			delete(store, key)
			return nil
		},
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			result := make(map[string][]byte)
			for k, v := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					result[k] = v
				}
			}
			return result, nil
		},
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			var keys []string
			for k := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					keys = append(keys, k)
				}
			}
			return keys, nil
		},
	}

	dataDir := t.TempDir()
	node := &mockRaftNode{} // default: leader ready
	tx := newMockTransactionServicer(store)
	mgmt, err := bootstrapUserRoleStores(context.Background(), mock, mock, tx, node, 1, 1, &mockTenantServicer{}, false, nil, "", nil, auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, mgmt)
	require.NotNil(t, mgmt.userSvc)
	require.NotNil(t, mgmt.roleSvc)

	authResult, err := bootstrapAuthEnforcement(context.Background(), mgmt, dataDir, node, audit.NewNoopLogger(), false, "", qrdbconfig.AuthABACConfig{}, nil, nil, "", "", "enrolled")
	require.NoError(t, err)
	require.NotNil(t, authResult)
	require.NotNil(t, authResult.auditLogger)
	require.NotNil(t, authResult.authMW)

	// Clean up.
	_ = authResult.auditLogger.Close()
}

// ---------------------------------------------------------------------------
// bootstrapAuth: per-decision audit rides the chained compliance logger
// ---------------------------------------------------------------------------

func TestBootstrapAuth_NoSideFile_DecisionAuditRidesChain(t *testing.T) {
	// The audit logger's file backend creates a file at
	// filepath.Join(dataDir, "audit.log"). If the dataDir does not exist
	// and cannot be created, os.OpenFile fails and bootstrapAuth wraps
	// the error in an AuthBootstrapError with Step "audit logger".
	store := make(map[string][]byte)
	mock := &mockKVServicer{
		putFn: func(_ context.Context, key string, value []byte) error {
			store[key] = value
			return nil
		},
		getFn: func(_ context.Context, key string) ([]byte, error) {
			if v, ok := store[key]; ok {
				return v, nil
			}
			return nil, &service.KeyNotFoundError{Key: key}
		},
		deleteFn: func(_ context.Context, key string) error {
			delete(store, key)
			return nil
		},
		scanFn: func(_ context.Context, prefix string) (map[string][]byte, error) {
			result := make(map[string][]byte)
			for k, v := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					result[k] = v
				}
			}
			return result, nil
		},
		listFn: func(_ context.Context, prefix string) ([]string, error) {
			var keys []string
			for k := range store {
				if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
					keys = append(keys, k)
				}
			}
			return keys, nil
		},
	}

	// The per-decision audit rides the chained compliance logger via the
	// decision bridge — there is no unchained side file to open — so a
	// nonexistent dataDir no longer fails auth bootstrap. Pin the
	// one-audit-plane behavior: construction succeeds and yields a
	// middleware.
	nonexistentDir := filepath.Join(t.TempDir(), "does", "not", "exist")
	node := &mockRaftNode{} // default: leader ready
	tx := newMockTransactionServicer(store)

	mgmt, err := bootstrapUserRoleStores(context.Background(), mock, mock, tx, node, 1, 1, &mockTenantServicer{}, false, nil, "", nil, auth.MTLSBindingEnrolled, nil, nil, nil)
	require.NoError(t, err)

	result, err := bootstrapAuthEnforcement(context.Background(), mgmt, nonexistentDir, node, audit.NewNoopLogger(), false, "", qrdbconfig.AuthABACConfig{}, nil, nil, "", "", "enrolled")
	require.NoError(t, err)
	require.NotNil(t, result)
}

// ---------------------------------------------------------------------------
// generateAdminClientCert: verify certificate CN and OU
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// generateAdminClientCert: write to unwritable directory
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// generateAdminClientCert: cert write succeeds but key write fails
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// resolveAutoConfig edge cases
// ---------------------------------------------------------------------------

func TestResolveAutoConfig_NonStaticWithAllExplicit(t *testing.T) {
	cmd := newStartCmd()
	// Mark every auto-gen flag as explicitly set.
	require.NoError(t, cmd.Flags().Set("replica-id", "99"))
	require.NoError(t, cmd.Flags().Set("listen", "0.0.0.0:40000"))
	require.NoError(t, cmd.Flags().Set("raft-address", "10.0.0.1:40000"))
	require.NoError(t, cmd.Flags().Set("admin-port", "40001"))
	require.NoError(t, cmd.Flags().Set("grpc-port", "40002"))
	require.NoError(t, cmd.Flags().Set("data-dir", "/explicit/data"))

	f := &startFlags{
		discoveryMethod: "dns",
		replicaID:       99,
		listen:          "0.0.0.0:40000",
		raftAddress:     "10.0.0.1:40000",
		adminPort:       40001,
		grpcPort:        40002,
		dataDir:         "/explicit/data",
		host:            testBindHost,
	}

	err := resolveAutoConfig(cmd, f)
	require.NoError(t, err)

	// All values should remain as explicitly set.
	assert.Equal(t, uint64(99), f.replicaID)
	assert.Equal(t, "0.0.0.0:40000", f.listen)
	assert.Equal(t, "10.0.0.1:40000", f.raftAddress)
	assert.Equal(t, 40001, f.adminPort)
	assert.Equal(t, 40002, f.grpcPort)
	assert.Equal(t, "/explicit/data", f.dataDir)
}

func TestResolveAutoConfig_DNSAutoGenerates(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{
		discoveryMethod: "dns",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		dataDir:         "./data",
		replicaID:       1,
		adminPort:       63002,
		grpcPort:        63003,
		host:            testBindHost,
	}

	err := resolveAutoConfig(cmd, f)
	require.NoError(t, err)

	// replicaID should have been auto-generated.
	assert.NotEqual(t, uint64(1), f.replicaID)
	assert.NotEqual(t, uint64(0), f.replicaID)
	// Ports should have changed.
	assert.NotEqual(t, "0.0.0.0:63001", f.listen)
	assert.NotEqual(t, 63002, f.adminPort)
	assert.NotEqual(t, 63003, f.grpcPort)
}

// ---------------------------------------------------------------------------
// resolveAutoConfig: partial explicit flags (only listen set)
// ---------------------------------------------------------------------------

func TestResolveAutoConfig_PartialExplicitFlags(t *testing.T) {
	cmd := newStartCmd()
	// Only set listen and raft-address, but not replica-id, admin-port, grpc-port, data-dir.
	require.NoError(t, cmd.Flags().Set("listen", "0.0.0.0:50000"))
	require.NoError(t, cmd.Flags().Set("raft-address", "10.0.0.1:50000"))

	f := &startFlags{
		discoveryMethod: "multicast",
		listen:          "0.0.0.0:50000",
		raftAddress:     "10.0.0.1:50000",
		dataDir:         "./data",
		replicaID:       1,
		adminPort:       63002,
		grpcPort:        63003,
	}

	err := resolveAutoConfig(cmd, f)
	require.NoError(t, err)

	// Listen should remain unchanged (explicitly set).
	assert.Equal(t, "0.0.0.0:50000", f.listen)
	assert.Equal(t, "10.0.0.1:50000", f.raftAddress)

	// replicaID, adminPort, grpcPort, dataDir should be auto-generated.
	assert.NotEqual(t, uint64(1), f.replicaID)
	assert.NotEqual(t, 63002, f.adminPort)
	assert.NotEqual(t, 63003, f.grpcPort)
}

// ---------------------------------------------------------------------------
// newKVServiceAdapter: ensure adapter creation and identity
// ---------------------------------------------------------------------------

func TestNewKVServiceAdapter_ReturnsNonNil(t *testing.T) {
	adapter := newKVServiceAdapter(&mockKVServicer{}, nil, nil)
	require.NotNil(t, adapter)
}

// ---------------------------------------------------------------------------
// generateAdminClientCert: valid CA cert but CA key is a different key than
// what signed the CA cert (will fail at CreateCertificate or earlier)
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// buildMulticastDiscovery: success path with valid secret
// ---------------------------------------------------------------------------

func TestBuildMulticastDiscovery_Success_Coverage(t *testing.T) {
	f := &startFlags{
		multicastSecret: "test-secret-fips16",
		replicaID:       42,
		raftAddress:     "127.0.0.1:63001",
		deploymentID:    1,
	}

	disc, err := buildMulticastDiscovery(f)
	require.NoError(t, err)
	require.NotNil(t, disc)
}

// ---------------------------------------------------------------------------
// buildMulticastDiscovery: NewMulticastDiscovery returns error
// ---------------------------------------------------------------------------

func TestBuildMulticastDiscovery_NewMulticastDiscoveryError(t *testing.T) {
	// The multicast discovery constructor validates that Address is non-empty.
	// Pass a valid secret but empty raftAddress to trigger the error from
	// discovery.NewMulticastDiscovery.
	f := &startFlags{
		multicastSecret: "test-secret-fips16",
		replicaID:       42,
		raftAddress:     "",
		deploymentID:    1,
	}

	disc, err := buildMulticastDiscovery(f)
	require.Error(t, err)
	assert.Nil(t, disc)
}

// ---------------------------------------------------------------------------
// newStatusCmd: invalid tls-ca-file triggers error in RunE
// ---------------------------------------------------------------------------

func TestNewStatusCmd_InvalidCAFile(t *testing.T) {
	cmd := newStatusCmd()
	// Point to a non-existent CA file so loadCACertPool returns an error.
	require.NoError(t, cmd.Flags().Set("tls-ca-file", filepath.Join(t.TempDir(), "nonexistent-ca.pem")))

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

// ---------------------------------------------------------------------------
// newPeersCmd: invalid tls-ca-file triggers error in RunE
// ---------------------------------------------------------------------------

func TestNewPeersCmd_InvalidCAFile(t *testing.T) {
	cmd := newPeersCmd()
	// Point to a non-existent CA file so loadCACertPool returns an error.
	require.NoError(t, cmd.Flags().Set("tls-ca-file", filepath.Join(t.TempDir(), "nonexistent-ca.pem")))

	err := cmd.RunE(cmd, nil)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

// ---------------------------------------------------------------------------
// generateAdminClientCert: read-only directory covers cert write error
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// runStart: TLS file load error path
//
// Exercises the loadTLSFromFiles branch inside runStart by providing all
// three TLS flags with nonexistent file paths. This covers the TLS file
// loading and early error return (start.go:380-382, 397-399).
// ---------------------------------------------------------------------------

func TestRunStart_TLSLoadError(t *testing.T) {
	dir := t.TempDir()
	f := &startFlags{
		tlsCAFile:   filepath.Join(dir, "nonexistent-ca.pem"),
		tlsCertFile: filepath.Join(dir, "nonexistent-cert.pem"),
		tlsKeyFile:  filepath.Join(dir, "nonexistent-key.pem"),
		dataDir:     dir,
		smType:      "memory",
		host:        testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr))
}

// ---------------------------------------------------------------------------
// runStart: the capability gate fires on CLI flags, not only --config
//
// resolveStartFlags already calls Config.ValidateCapabilities when --config
// names a YAML file; a protocol enabled by flag alone (no --config) went
// through runStart without that check, so --enable-mysql on a binary built
// without qrdb_mysql silently started with startMySQLFn staying nil instead
// of refusing. runStart now calls ValidateCapabilities itself, before TLS
// bootstrap or any other resource is touched, so this covers both outcomes
// this test binary's own build tags can produce: refused with the typed
// error naming the missing tag when the tag is absent, or -- as it is for
// this binary, built with the default tag set -- waved through to fail on
// the next thing runStart needs that this minimal *startFlags does not
// supply (the same TLSLoadError TestRunStart_TLSLoadError exercises).
// ---------------------------------------------------------------------------

func TestRunStart_MySQLCapabilityGate(t *testing.T) {
	dir := t.TempDir()
	f := &startFlags{
		tlsCAFile:   filepath.Join(dir, "nonexistent-ca.pem"),
		tlsCertFile: filepath.Join(dir, "nonexistent-cert.pem"),
		tlsKeyFile:  filepath.Join(dir, "nonexistent-key.pem"),
		dataDir:     dir,
		smType:      "memory",
		host:        testBindHost,
		enableMySQL: true,
	}

	err := runStart(f)
	require.Error(t, err)

	var verr *qrdbconfig.ValidationError
	if !capabilities.Has("mysql") {
		require.True(t, errors.As(err, &verr), "want *config.ValidationError, got %T: %v", err, err)
		assert.Contains(t, verr.Reason, "qrdb_mysql")
		return
	}
	if errors.As(err, &verr) {
		t.Fatalf("got a capability ValidationError (%v) on a binary built with qrdb_mysql -- "+
			"the gate must not refuse a protocol this binary actually has", verr)
	}
	var loadErr *TLSLoadError
	require.True(t, errors.As(err, &loadErr), "want *TLSLoadError once the capability gate passes, got %T: %v", err, err)
}

// ---------------------------------------------------------------------------
// runStart: CA cert directory creation failure
//
// Exercises the resolveTLS -> clusterenroll.Resolve persist path where
// MkdirAll fails because the data directory path traverses a regular file.
// ---------------------------------------------------------------------------

func TestRunStart_CACertMkdirAllError(t *testing.T) {
	dir := t.TempDir()
	// Create a file that blocks directory creation.
	blocker := filepath.Join(dir, "blocker")
	require.NoError(t, os.WriteFile(blocker, []byte("x"), 0o600))

	// Use the blocker file as a path component so MkdirAll fails.
	badDataDir := filepath.Join(blocker, "subdir")

	f := &startFlags{
		dataDir: badDataDir,
		smType:  "memory",
		host:    testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	var writeErr *BootstrapWriteError
	require.True(t, errors.As(err, &writeErr), "err = %v (%T)", err, err)
}

// ---------------------------------------------------------------------------
// runStart: CA cert file write failure
//
// Exercises the resolveTLS -> clusterenroll.Resolve persist path where
// MkdirAll succeeds but the file write fails because the data directory is
// read-only.
// ---------------------------------------------------------------------------

func TestRunStart_CACertWriteFileError(t *testing.T) {
	dataDir := t.TempDir()
	// A directory squatting at the ca-key.pem path fails the atomic persist
	// (rename onto a directory is EISDIR — for root too) while staying
	// invisible to the cached-material probe: LoadFromDir early-returns "no
	// cache" when ca.pem/node-cert.pem/node-key.pem are all absent, before
	// it ever consults ca-key.pem. A directory at ca.pem no longer reaches
	// the write branch at all: clustertls.fileExists deliberately reports
	// any existing entry, so the LOAD fails closed first.
	caKeyPath := filepath.Join(dataDir, "ca-key.pem")
	require.NoError(t, os.MkdirAll(caKeyPath, 0o700))

	f := &startFlags{
		dataDir: dataDir,
		smType:  "memory",
		host:    testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	var writeErr *BootstrapWriteError
	require.True(t, errors.As(err, &writeErr), "err = %v (%T)", err, err)
}

// ---------------------------------------------------------------------------
// initializeNode: peer parse error for static multi-node
//
// Exercises the parsePeers error path in initializeNode by providing
// static discovery with multiple peers that have invalid format. This
// covers start.go:560-563.
// ---------------------------------------------------------------------------

func TestInitializeNode_PeerParseError(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	f := &startFlags{
		smType:          "memory",
		discoveryMethod: "static",
		peers:           []string{"1=127.0.0.1:63001", "invalid-peer-format"},
		dataDir:         t.TempDir(),
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	_, err = initializeNode(context.Background(), f, bundle)
	require.Error(t, err)
	var peerErr *PeerParseError
	require.True(t, errors.As(err, &peerErr))
	assert.Equal(t, "invalid-peer-format", peerErr.Input)
}

// ---------------------------------------------------------------------------
// initializeNode: unknown SM type
//
// Exercises the resolveCreateFunc error path in initializeNode by
// providing an unknown state machine type. Covers start.go:552-554.
// ---------------------------------------------------------------------------

func TestInitializeNode_UnknownSMType(t *testing.T) {
	f := &startFlags{
		smType:          "nonexistent-sm",
		discoveryMethod: "static",
		dataDir:         t.TempDir(),
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	_, err := initializeNode(context.Background(), f, nil)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
	assert.Equal(t, "nonexistent-sm", smErr.Name)
}

// ---------------------------------------------------------------------------
// applyConfigToFlags: bootstrap fields applied from config
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// CertReloader watch: debounce Reload error path
//
// When the watched cert file is overwritten with invalid data, the
// debounce timer fires and Reload fails. This covers the error branch
// inside the debounce callback (cert_reload.go:192-194).
// ---------------------------------------------------------------------------

func TestCertReloader_FileWatch_DebounceReloadError(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	origLoaded := r.cert.Load()

	// Overwrite the cert file with invalid data to make the debounced
	// Reload fail. Write to both files to ensure the fsnotify event fires.
	require.NoError(t, os.WriteFile(certPath, []byte("invalid-cert"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("invalid-key"), 0o600))

	// Wait for debounce + a little margin. The debounce interval is 100ms.
	time.Sleep(500 * time.Millisecond)

	// The original certificate must still be served because the reload
	// failed and the old cert is preserved.
	current := r.cert.Load()
	assert.Equal(t, origLoaded.Certificate, current.Certificate,
		"failed debounce reload must preserve the original certificate")
}

// ---------------------------------------------------------------------------
// CertReloader watch: watcher.Errors path
//
// Sends an error through the fsnotify watcher's Errors channel to exercise
// the error logging branch in the watch goroutine (cert_reload.go:197-201).
// ---------------------------------------------------------------------------

func TestCertReloader_FileWatch_WatcherError(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := testCertPair(t)
	certPath, keyPath := writeCertFiles(t, dir, certPEM, keyPEM)

	r, err := NewCertReloader(certPath, keyPath, testLogger())
	require.NoError(t, err)
	t.Cleanup(func() { _ = r.Stop() })

	// Send a synthetic error through the watcher's error channel.
	r.watcher.Errors <- errors.New("synthetic watcher error")

	// Give the watch goroutine time to process the error.
	time.Sleep(100 * time.Millisecond)

	// The reloader should still be functional.
	cert, err := r.GetCertificate(nil)
	require.NoError(t, err)
	require.NotNil(t, cert)
}

func TestApplyConfigToFlags_BootstrapFields(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}
	cfg := &qrdbconfig.Config{
		Bootstrap: qrdbconfig.BootstrapConfig{
			NoiseKey:  "testnoisekey",
			NoiseAddr: "10.0.0.1:63006",
			NoisePort: 63007,
			SPKIPin:   "testspkipin",
			DANEDNS:   "8.8.8.8:53",
			DANEDoT:   true,
			DirectURL: "https://bootstrap.example.com",
			Addr:      "10.0.0.1:8443",
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "testnoisekey", f.bootstrapNoiseKey)
	assert.Equal(t, "10.0.0.1:63006", f.bootstrapNoiseAddr)
	assert.Equal(t, 63007, f.bootstrapNoisePort)
	assert.Equal(t, "testspkipin", f.bootstrapSPKIPin)
	assert.Equal(t, "8.8.8.8:53", f.bootstrapDANEDNS)
	assert.True(t, f.bootstrapDANEDoT)
	assert.Equal(t, "https://bootstrap.example.com", f.bootstrapDirectURL)
	assert.Equal(t, "10.0.0.1:8443", f.bootstrapAddr)
}

// ---------------------------------------------------------------------------
// applyConfigToFlags: CLI flags take precedence over bootstrap config
// ---------------------------------------------------------------------------

func TestApplyConfigToFlags_BootstrapCLIPrecedence(t *testing.T) {
	cmd := newStartCmd()
	require.NoError(t, cmd.Flags().Set("bootstrap-noise-key", "cli-key"))
	require.NoError(t, cmd.Flags().Set("bootstrap-noise-addr", "cli-addr"))
	require.NoError(t, cmd.Flags().Set("bootstrap-noise-port", "9999"))
	require.NoError(t, cmd.Flags().Set("bootstrap-spki-pin", "cli-pin"))
	require.NoError(t, cmd.Flags().Set("bootstrap-dane-dns", "cli-dns"))
	require.NoError(t, cmd.Flags().Set("bootstrap-dane-dot", "true"))
	require.NoError(t, cmd.Flags().Set("bootstrap-direct-url", "cli-url"))
	require.NoError(t, cmd.Flags().Set("bootstrap-addr", "cli-baddr"))

	cfg := &qrdbconfig.Config{
		Bootstrap: qrdbconfig.BootstrapConfig{
			NoiseKey:  "yaml-key",
			NoiseAddr: "yaml-addr",
			NoisePort: 1111,
			SPKIPin:   "yaml-pin",
			DANEDNS:   "yaml-dns",
			DANEDoT:   false,
			DirectURL: "yaml-url",
			Addr:      "yaml-baddr",
		},
	}

	f := &startFlags{
		bootstrapNoiseKey:  "cli-key",
		bootstrapNoiseAddr: "cli-addr",
		bootstrapNoisePort: 9999,
		bootstrapSPKIPin:   "cli-pin",
		bootstrapDANEDNS:   "cli-dns",
		bootstrapDANEDoT:   true,
		bootstrapDirectURL: "cli-url",
		bootstrapAddr:      "cli-baddr",
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "cli-key", f.bootstrapNoiseKey)
	assert.Equal(t, "cli-addr", f.bootstrapNoiseAddr)
	assert.Equal(t, 9999, f.bootstrapNoisePort)
	assert.Equal(t, "cli-pin", f.bootstrapSPKIPin)
	assert.Equal(t, "cli-dns", f.bootstrapDANEDNS)
	assert.True(t, f.bootstrapDANEDoT)
	assert.Equal(t, "cli-url", f.bootstrapDirectURL)
	assert.Equal(t, "cli-baddr", f.bootstrapAddr)
}

// ---------------------------------------------------------------------------
// newStartCmd + config file: bootstrap section loaded and applied
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// runStart: wireCertReloader fails when node-cert.pem/node-key.pem exist
// but are not valid TLS certificates. The error is logged as a warning
// and execution continues to the next stage.
// ---------------------------------------------------------------------------

func TestRunStart_WireCertReloaderWarnsOnInvalidCerts(t *testing.T) {
	dataDir := t.TempDir()

	// Generate valid TLS bundle and write CA/cert/key to separate files
	// that will be loaded via --tls-ca-file, --tls-cert-file, --tls-key-file.
	bundle, err := generateCATLS()
	require.NoError(t, err)

	caFile := filepath.Join(dataDir, "ca.pem")
	certFile := filepath.Join(dataDir, "cert.pem")
	keyFile := filepath.Join(dataDir, "key.pem")
	require.NoError(t, os.WriteFile(caFile, bundle.CACertPEM, 0o600))
	require.NoError(t, os.WriteFile(certFile, bundle.CertPEM, 0o600))
	require.NoError(t, os.WriteFile(keyFile, bundle.KeyPEM, 0o600))

	// Write invalid node-cert.pem and node-key.pem so that
	// wireCertReloader's tls.LoadX509KeyPair fails with an error.
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "node-cert.pem"), []byte("invalid-cert"), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "node-key.pem"), []byte("invalid-key"), 0o600))

	f := &startFlags{
		tlsCAFile:       caFile,
		tlsCertFile:     certFile,
		tlsKeyFile:      keyFile,
		dataDir:         dataDir,
		smType:          "nonexistent",
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err = runStart(f)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

func TestNewStartCmd_BootstrapConfigApplied(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	yaml := `
raft:
  sm_type: "memory"
  data_dir: "` + dir + `"
discovery:
  method: "static"
tls:
  enabled: false
bootstrap:
  noise_key: "aabbccdd"
  noise_addr: "10.0.0.1:63006"
  noise_port: 63007
  spki_pin: "sha256pin"
  dane_dns: "8.8.8.8:53"
  dane_dot: true
  direct_url: "https://bootstrap.example.com"
  addr: "10.0.0.1:8443"
`
	require.NoError(t, os.WriteFile(cfgFile, []byte(yaml), 0o600))

	// Read the resolved flags rather than forcing a startup failure and
	// asserting on its error type -- the same load sensitivity as
	// TestNewStartCmd_ValidConfigFileApplied, which is go-qrdb#84. Reaching
	// the unknown SM meant first binding the bootstrap Noise port that this
	// very config configures.
	cmd, f := newStartCmdWithFlags()
	require.NoError(t, cmd.ParseFlags([]string{"--config", cfgFile, "--sm", "nonexistent"}))
	require.NoError(t, resolveStartFlags(cmd, f))

	assert.Equal(t, "aabbccdd", f.bootstrapNoiseKey)
	assert.Equal(t, "10.0.0.1:63006", f.bootstrapNoiseAddr)
	assert.Equal(t, 63007, f.bootstrapNoisePort)
	assert.Equal(t, "sha256pin", f.bootstrapSPKIPin)
	assert.Equal(t, "8.8.8.8:53", f.bootstrapDANEDNS)
	assert.True(t, f.bootstrapDANEDoT)
	assert.Equal(t, "https://bootstrap.example.com", f.bootstrapDirectURL)
	assert.Equal(t, "10.0.0.1:8443", f.bootstrapAddr)

	// The CLI flag still overrides the file.
	assert.Equal(t, "nonexistent", f.smType)
}

// ---------------------------------------------------------------------------
// runStart: SQL database creation when sqlDatabase flag is set and mysql enabled
//
// The test sets smType to "nonexistent" so that initializeNode fails with
// UnknownSMError immediately after the SQL database setup code runs. This
// exercises the sqlDatabase + enableMySQL code path in runStart
// (start.go:706-738) without actually starting a Raft node.
// ---------------------------------------------------------------------------

func TestRunStart_SQLDatabaseCreated(t *testing.T) {
	dataDir := t.TempDir()

	f := &startFlags{
		dataDir:         dataDir,
		smType:          "nonexistent-sm",
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "testdb",
		enableMySQL:     true,
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	// On a binary without qrdb_mysql, the capability gate refuses
	// --enable-mysql before the SQL database creation path this test
	// targets is ever reached; on one with it, the SQL database creation
	// path is exercised (smType fails after).
	if !capabilities.Has("mysql") {
		var verr *qrdbconfig.ValidationError
		require.True(t, errors.As(err, &verr), "want *config.ValidationError, got %T: %v", err, err)
		return
	}
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

// ---------------------------------------------------------------------------
// runStart: SQL database "memory" backend when smType is "concurrent"
//
// Exercises the backend == memory branch (start.go:712-713) when the
// sql database does not yet exist.
// ---------------------------------------------------------------------------

func TestRunStart_SQLDatabaseConcurrentMemoryBackend(t *testing.T) {
	dataDir := t.TempDir()

	f := &startFlags{
		dataDir:         dataDir,
		smType:          "nonexistent-sm",
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		sqlDatabase:     "testdb-concurrent",
		enablePgSQL:     true,
		host:            testBindHost,
	}

	err := runStart(f)
	require.Error(t, err)
	// On a binary without qrdb_pgsql, the capability gate refuses
	// --enable-pgsql before the SQL database creation path this test
	// targets is ever reached; on one with it, the SQL database creation
	// path is exercised (smType fails after).
	if !capabilities.Has("pgsql") {
		var verr *qrdbconfig.ValidationError
		require.True(t, errors.As(err, &verr), "want *config.ValidationError, got %T: %v", err, err)
		return
	}
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

// ---------------------------------------------------------------------------
// runStart: database already exists - Open path exercised
//
// Write a pebble database directory so the default database already exists
// from a prior run. runStart must succeed in opening it and then fail
// at initializeNode (unknown SM type).
// ---------------------------------------------------------------------------

func TestRunStart_DatabaseAlreadyExistsOpensIt(t *testing.T) {
	dataDir := t.TempDir()

	// First run creates the database and fails at initializeNode.
	f1 := &startFlags{
		dataDir:         dataDir,
		smType:          "nonexistent-sm",
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err := runStart(f1)
	require.Error(t, err)

	// Second run: database exists -> AlreadyExistsError -> Open path.
	f2 := &startFlags{
		dataDir:         dataDir,
		smType:          "nonexistent-sm",
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	err = runStart(f2)
	require.Error(t, err)
	var smErr *UnknownSMError
	require.True(t, errors.As(err, &smErr))
}

// ---------------------------------------------------------------------------
// runStart: pebble SM type registers a factory (exercises switch case)
//
// The pebble case in the switch at start.go:744-757 assigns f.createFn.
// Setting smType to "pebble" enters that branch; initializeNode then
// proceeds and fails at the shard start step (no real cluster available),
// which is a non-UnknownSMError.
// ---------------------------------------------------------------------------

func TestRunStart_PebbleSMTypeExercisesSwitch(t *testing.T) {
	dataDir := t.TempDir()

	f := &startFlags{
		dataDir:         dataDir,
		smType:          "pebble",
		discoveryMethod: "static",
		listen:          "0.0.0.0:63001",
		raftAddress:     "127.0.0.1:63001",
		shardID:         1,
		replicaID:       1,
		host:            testBindHost,
	}

	// pebble SM type is valid so initializeNode proceeds past the factory
	// assignment. Any error from the host/shard layer is acceptable; the
	// key assertion is that UnknownSMError is NOT returned.
	err := runStart(f)
	if err != nil {
		var smErr *UnknownSMError
		assert.False(t, errors.As(err, &smErr),
			"pebble SM should not trigger UnknownSMError: %v", err)
	}
}

// ---------------------------------------------------------------------------
// localSANs: the SAN set is exactly the two loopbacks plus every non-loopback
// interface address — no more, no less, on a host with any number of NICs
// ---------------------------------------------------------------------------

func TestLocalSANs_ReturnsLocalhostAndBaseIPs(t *testing.T) {
	dnsNames, ips := localSANs()

	assert.Contains(t, dnsNames, "localhost")
	if hostname, err := os.Hostname(); err == nil && hostname != "" {
		assert.Contains(t, dnsNames, hostname, "hostname must be a DNS SAN")
	}

	// The contract is a set equality, so measure the other half of it rather
	// than asserting a count: a count is a claim about how many NICs the host
	// happens to have, which is true on a workstation and false in a
	// single-interface container.
	want := map[string]bool{"127.0.0.1": true, "::1": true}
	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, addr := range addrs {
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			want[ipNet.IP.String()] = true
		}
	}

	got := make(map[string]bool, len(ips))
	for _, ip := range ips {
		got[ip.String()] = true
	}

	// Set equality in both directions. Missing entries catch a dropped
	// loopback or a dropped interface address; extra entries catch the
	// unspecified addresses 0.0.0.0 and ::, which LocalSANs deliberately
	// excludes because nothing dials them (pkg/cluster.AdvertisableHost
	// refuses to advertise them for the same reason).
	assert.Equal(t, want, got, "IP SANs must be the loopbacks plus every non-loopback interface address")
}

// ---------------------------------------------------------------------------
// applyTLSClientAuth: explicit operator-selected client-auth modes
// ---------------------------------------------------------------------------

func TestApplyTLSClientAuth_RequestAttachesClientCAs(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	f := &startFlags{tlsClientAuth: "request", tlsClientAuthSet: true}
	applyTLSClientAuth(f, bundle)

	assert.Equal(t, tls.RequestClientCert, bundle.TLSConfig.ClientAuth)
	assert.Same(t, bundle.CACertPool, bundle.TLSConfig.ClientCAs)
}

func TestApplyTLSClientAuth_NoneLeavesClientCAsUnset(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	f := &startFlags{tlsClientAuth: "none", tlsClientAuthSet: true}
	applyTLSClientAuth(f, bundle)

	assert.Equal(t, tls.NoClientCert, bundle.TLSConfig.ClientAuth)
	assert.Nil(t, bundle.TLSConfig.ClientCAs)
}

func TestApplyTLSClientAuth_RequireAndVerify(t *testing.T) {
	bundle, err := generateCATLS()
	require.NoError(t, err)

	f := &startFlags{tlsClientAuth: "require_and_verify", tlsClientAuthSet: true, host: testBindHost}
	applyTLSClientAuth(f, bundle)

	assert.Equal(t, tls.RequireAndVerifyClientCert, bundle.TLSConfig.ClientAuth)
	assert.Same(t, bundle.CACertPool, bundle.TLSConfig.ClientCAs)
}

// ---------------------------------------------------------------------------
// applyConfigToFlags: YAML tls.client_auth marks the mode explicitly set
// ---------------------------------------------------------------------------

func TestApplyConfigToFlags_TLSClientAuthFromYAML(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}

	cfg := &qrdbconfig.Config{
		TLS: qrdbconfig.TLSConfig{ClientAuth: "request"},
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "request", f.tlsClientAuth)
	assert.True(t, f.tlsClientAuthSet)
}

func TestApplyConfigToFlags_TLSClientAuthUnsetStaysImplicit(t *testing.T) {
	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}

	applyConfigToFlags(cmd, f, &qrdbconfig.Config{})

	assert.False(t, f.tlsClientAuthSet)
}

// ---------------------------------------------------------------------------
// applyConfigToFlags: ClusterTrustDomain from config
//
// When the --cluster-trust-domain flag has not been set on the command line,
// a non-empty Auth.ClusterTrustDomain in the config file must be applied to
// f.clusterTrustDomain (start.go:611-613).
// ---------------------------------------------------------------------------

func TestApplyConfigToFlags_ClusterTrustDomainFromConfig(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}

	cfg := &qrdbconfig.Config{
		Auth: qrdbconfig.AuthConfig{
			ClusterTrustDomain: "test.example.cluster",
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "test.example.cluster", f.clusterTrustDomain)
}

// TestApplyConfigToFlags_ClusterTrustDomainCLIPrecedence verifies that an
// explicit --cluster-trust-domain flag is not overwritten by the config value.
func TestApplyConfigToFlags_ClusterTrustDomainCLIPrecedence(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	require.NoError(t, cmd.Flags().Set("cluster-trust-domain", "cli.cluster"))

	f := &startFlags{clusterTrustDomain: "cli.cluster", host: testBindHost}
	cfg := &qrdbconfig.Config{
		Auth: qrdbconfig.AuthConfig{
			ClusterTrustDomain: "yaml.cluster",
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	assert.Equal(t, "cli.cluster", f.clusterTrustDomain)
}

// ---------------------------------------------------------------------------
// applyConfigToFlags: PeerAdminURLs from config
//
// A non-empty Auth.PeerAdminURLs slice in the config is parsed into the
// f.peerAdminURLs map (start.go:614-624). Entries with empty RaftAddr or
// AdminURL are silently skipped.
// ---------------------------------------------------------------------------

func TestApplyConfigToFlags_PeerAdminURLsFromConfig(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	f := &startFlags{host: testBindHost}

	cfg := &qrdbconfig.Config{
		Auth: qrdbconfig.AuthConfig{
			PeerAdminURLs: []qrdbconfig.PeerAdminEntry{
				{RaftAddr: "10.0.0.1:7946", AdminURL: "https://10.0.0.1:8443"},
				{RaftAddr: "10.0.0.2:7946", AdminURL: "https://10.0.0.2:8443"},
			},
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	require.NotNil(t, f.peerAdminURLs)
	assert.Equal(t, "https://10.0.0.1:8443", f.peerAdminURLs["10.0.0.1:7946"])
	assert.Equal(t, "https://10.0.0.2:8443", f.peerAdminURLs["10.0.0.2:7946"])
}

// TestApplyConfigToFlags_PeerAdminURLsSkipsIncomplete verifies that entries
// with missing RaftAddr or AdminURL are not written to f.peerAdminURLs, and
// that the entire map is nil when no valid entries remain.
func TestApplyConfigToFlags_PeerAdminURLsSkipsIncomplete(t *testing.T) {
	t.Parallel()

	cmd := newStartCmd()
	f := &startFlags{}

	cfg := &qrdbconfig.Config{
		Auth: qrdbconfig.AuthConfig{
			PeerAdminURLs: []qrdbconfig.PeerAdminEntry{
				{RaftAddr: "", AdminURL: "https://10.0.0.1:8443"}, // missing RaftAddr
				{RaftAddr: "10.0.0.2:7946", AdminURL: ""},         // missing AdminURL
			},
		},
	}
	applyConfigToFlags(cmd, f, cfg)

	// No valid entries → peerAdminURLs must remain nil (the map is not
	// assigned when len(m) == 0).
	assert.Nil(t, f.peerAdminURLs)
}

// ReconcileBuiltInRoles satisfies role.RoleServicer; this double never drifts.
func (m *mockRoleServicer) ReconcileBuiltInRoles(_ context.Context, tenantID string) (*role.ReconcileReport, error) {
	return &role.ReconcileReport{TenantID: tenantID}, nil
}
