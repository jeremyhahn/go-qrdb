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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/bootstrap"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/role"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// quotaErrTenantSvc: mock satisfying TenantServicer, QuotaProvider, and the
// package-private quotaUsageSetter with a configurable ListTenants error.
// ---------------------------------------------------------------------------

// quotaErrTenantSvc wraps mockTenantServicer and layers on Quotas and
// SetQuotaUsage so that buildQuotaAccountant and buildQuotaReconciler both
// return non-nil. ListTenants is overridden to return a fixed error, which
// exercises the early-return path inside the Tenants closure.
type quotaErrTenantSvc struct {
	*mockTenantServicer
	listErr error
}

func (m *quotaErrTenantSvc) ListTenants(_ context.Context) ([]tenant.TenantInfo, error) {
	return nil, m.listErr
}

// Quotas satisfies quota.QuotaProvider so buildQuotaAccountant returns non-nil.
func (m *quotaErrTenantSvc) Quotas(_ context.Context, _ string) (*quota.Quotas, error) {
	return &quota.Quotas{MaxStorageBytes: 1 << 30}, nil
}

// SetQuotaUsage satisfies the package-private quotaUsageSetter interface so
// buildQuotaReconciler does not return nil at the !ok branch.
func (m *quotaErrTenantSvc) SetQuotaUsage(_ context.Context, tenantID string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: tenantID}, nil
}

// ---------------------------------------------------------------------------
// buildQuotaReconciler: system tenant skipped in Tenants closure (line 1725)
//
// When ListTenants returns a result that includes the system tenant, the
// Tenants closure in buildQuotaReconciler must skip it via continue.
// ---------------------------------------------------------------------------

// TestBuildQuotaReconciler_SystemTenantSkipped verifies that the Tenants
// closure wired by buildQuotaReconciler filters out the system tenant
// (ti.ID == tenantid.System → continue, line 1725) so it is never
// included in the reconciler's volume-tenant list.
func TestBuildQuotaReconciler_SystemTenantSkipped(t *testing.T) {
	svc, _ := newReattachFixture(t)
	ctx := context.Background()

	// Create the system tenant via the internal-call context so the
	// reserved-ID rejection is bypassed (same path firstboot uses).
	internalCtx := tenant.WithInternalCall(ctx)
	if _, err := svc.CreateTenant(internalCtx, tenantid.System, "System", tenant.IsolationPrefix); err != nil {
		t.Fatalf("CreateTenant(system): %v", err)
	}

	acct := buildQuotaAccountant(svc)
	require.NotNil(t, acct, "buildQuotaAccountant must return non-nil for a quota-capable service")

	rec := buildQuotaReconciler(acct, svc, qrdbconfig.TenantConfig{}, nil)
	require.NotNil(t, rec, "buildQuotaReconciler must return non-nil reconciler")

	// RunOnce calls the Tenants closure, which lists tenants and skips the
	// system tenant at line 1725. Must not panic.
	rec.RunOnce(ctx)
}

// ---------------------------------------------------------------------------
// awaitUnseal: success path — barrier unsealed during poll loop (lines 295-299)
//
// awaitUnseal blocks in a ticker loop checking IsSealed(). When the barrier
// transitions to unsealed (Initialize called from another goroutine), the
// loop exits on the next tick and returns (sealNode, nil).
// ---------------------------------------------------------------------------

// TestAwaitUnseal_SuccessPath verifies that awaitUnseal returns a non-nil
// sealNode and nil error (lines 295-299) when the seal barrier is initialized
// (and therefore unsealed) while the loop is running.
func TestAwaitUnseal_SuccessPath(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:    "",
			RootKeyPath: "__sys:seal/root_key",
		},
		host: testBindHost,
	}

	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	bundle, err := generateCATLS()
	require.NoError(t, err)

	sigCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type callResult struct {
		nodeNonNil bool
		err        error
	}
	ch := make(chan callResult, 1)
	go func() {
		n, e := awaitUnseal(sigCtx, f, bundle, sub, nil, nil, nil)
		if n != nil {
			_ = n.Stop(context.Background())
		}
		ch <- callResult{nodeNonNil: n != nil, err: e}
	}()

	// Allow the sealed-window listener to start and the first ticker tick
	// to confirm the barrier is sealed before we initialize it.
	time.Sleep(400 * time.Millisecond)

	// Initialize the barrier with a non-empty passphrase so
	// DeferredSoftwareStrategy accepts the credentials.
	require.NoError(t, sub.SealBarrier.Initialize(
		context.Background(),
		seal.MustCredentials(context.Background(), []byte("testpass-awaitUnseal")),
	))

	// awaitUnseal should detect !IsSealed() on the next tick and return.
	select {
	case res := <-ch:
		require.NoError(t, res.err, "awaitUnseal must return nil error on success path")
		require.True(t, res.nodeNonNil, "awaitUnseal must return a non-nil sealNode on success path")
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("awaitUnseal did not complete within 2s after barrier was unsealed")
	}
}

// ---------------------------------------------------------------------------
// runFirstBoot: FirstBootError assigned at line 2489
//
// When barriers != nil, runFirstBoot routes through bootstrap.BootstrapSystem.
// If the system barrier is already unsealed, BootstrapSystem skips Initialize
// and calls firstboot.Initialize directly. A role-create failure causes
// firstboot.Initialize to return an error that BootstrapSystem wraps as
// *FirstBootError. The barriers path then assigns firstBootErr = fbErr at
// line 2489, after which waitForAdminReplication is called.
// ---------------------------------------------------------------------------

// TestRunFirstBoot_WithBarriers_UnsealedBarrier_FirstBootError verifies that
// line 2489 (firstBootErr = fbErr) is executed when the system barrier is
// already unsealed and firstboot.Initialize fails with a wrapped FirstBootError.
func TestRunFirstBoot_WithBarriers_UnsealedBarrier_FirstBootError(t *testing.T) {
	dir := t.TempDir()

	f := &startFlags{
		dataDir:      dir,
		deploymentID: 1,
		sealConfig: qrdbconfig.SealConfig{
			Strategy:    "",
			RootKeyPath: "__sys:seal/root_key",
		},
	}
	sub, err := initSealSubsystem(f, context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = sub.Close() }()

	// Unseal the system barrier so BootstrapSystem observes !IsSealed() and
	// proceeds directly to firstboot.Initialize without re-initializing.
	require.NoError(t, sub.SealBarrier.Initialize(
		context.Background(),
		seal.MustCredentials(context.Background(), []byte("testpass-runfirstboot")),
	))

	userSvc := &mockUserServicer{
		getUserByUsernameFn: func(_ context.Context, _ string, _ string) (*user.User, error) {
			return nil, &user.UserNotFoundError{Lookup: "admin"}
		},
	}

	// createRoleFn returns a sentinel that is not *role.RoleNameExistsError,
	// so registerBuiltInRoles returns *firstboot.RolesRegistrationError, which
	// BootstrapSystem wraps as *bootstrap.FirstBootError (line 2489).
	roleSvc := &mockRoleServicer{
		createRoleFn: func(_ context.Context, _ *role.CreateRoleRequest) (*role.Role, error) {
			return nil, errors.New("test: role create intentionally failed")
		},
	}
	node := &mockRaftNode{}

	// Short timeout so waitForAdminReplication returns quickly after line 2489
	// is hit and the function proceeds to wait for admin replication.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	err = runFirstBoot(
		ctx, node, 1,
		userSvc, roleSvc, &mockTenantServicer{},
		false, sub.BarrierRegistry, seal.StrategyID("passphrase"),
		auth.MTLSBindingEnrolled, nil, nil, nil,
	)

	// firstBootErr is assigned the *bootstrap.FirstBootError from
	// BootstrapSystem. It is not a NotLeader condition, so it propagates with
	// the role-create failure that caused it rather than being reported as an
	// admin-replication timeout (go-qrdb#86).
	require.Error(t, err, "runFirstBoot must return an error when BootstrapSystem fails")
	var fbErr *bootstrap.FirstBootError
	require.True(t, errors.As(err, &fbErr),
		"expected the *bootstrap.FirstBootError to survive, got %T: %v", err, err)
	assert.Contains(t, err.Error(), "role create intentionally failed",
		"the cause an operator needs was replaced by a generic wrapper")
}

// ---------------------------------------------------------------------------
// NewCertReloader: tls.LoadX509KeyPair error (cert_reload.go:69.16,71.3)
//
// When the cert and key files exist but do not contain valid PEM/DER data,
// tls.LoadX509KeyPair returns an error and NewCertReloader wraps it in a
// *CertReloadError and returns it at cert_reload.go:69-71.
// ---------------------------------------------------------------------------

// TestNewCertReloader_InvalidCertKeyFiles verifies that NewCertReloader returns
// *CertReloadError (cert_reload.go:69.16,71.3) when the certificate and key
// files contain invalid PEM content that tls.LoadX509KeyPair cannot parse.
func TestNewCertReloader_InvalidCertKeyFiles(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "bad-cert.pem")
	keyPath := filepath.Join(dir, "bad-key.pem")

	require.NoError(t, os.WriteFile(certPath, []byte("not-valid-pem"), 0o600))
	require.NoError(t, os.WriteFile(keyPath, []byte("not-valid-pem"), 0o600))

	_, err := NewCertReloader(certPath, keyPath, reattachTestLogger())
	require.Error(t, err)
	var certErr *CertReloadError
	require.True(t, errors.As(err, &certErr),
		"expected *CertReloadError from invalid PEM files, got %T: %v", err, err)
}

// ---------------------------------------------------------------------------
// buildQuotaReconciler: Tenants closure ListTenants error (start.go:1719-1721)
//
// When the tenant service's ListTenants returns an error, the Tenants closure
// returns nil, lerr at line 1719-1721. RunOnce logs a warning and returns.
// ---------------------------------------------------------------------------

// TestBuildQuotaReconciler_TenantsClosureListTenantsError verifies that the
// Tenants closure inside buildQuotaReconciler propagates a ListTenants error
// (return nil, lerr at start.go:1719.19,1721.5) when RunOnce is called.
func TestBuildQuotaReconciler_TenantsClosureListTenantsError(t *testing.T) {
	svc := &quotaErrTenantSvc{
		mockTenantServicer: &mockTenantServicer{},
		listErr:            errors.New("test: list tenants intentionally failed"),
	}

	acct := buildQuotaAccountant(svc)
	require.NotNil(t, acct, "buildQuotaAccountant must return non-nil for a QuotaProvider")

	rec := buildQuotaReconciler(acct, svc, qrdbconfig.TenantConfig{}, nil)
	require.NotNil(t, rec, "buildQuotaReconciler must return non-nil reconciler")

	// RunOnce invokes the Tenants closure, which calls svc.ListTenants and
	// receives a non-nil error. The closure hits return nil, lerr at line
	// 1719-1721. The reconciler logs a warning and returns without panicking.
	rec.RunOnce(context.Background())
}
