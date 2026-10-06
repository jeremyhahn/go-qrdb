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
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock tenant onboarding service
// ---------------------------------------------------------------------------

// mockTenantOnboarding implements service.TenantOnboardingServicer. The
// embedded transport's CreateTenant delegates to exactly this interface
// (not tenant.TenantServicer), so this is the double "tenant create"
// CLI/binding tests exercise.
type mockTenantOnboarding struct {
	onboardFn func(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error)
}

var _ service.TenantOnboardingServicer = (*mockTenantOnboarding)(nil)

func (m *mockTenantOnboarding) OnboardTenant(ctx context.Context, req *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
	if m.onboardFn != nil {
		return m.onboardFn(ctx, req)
	}
	now := time.Now()
	return &service.OnboardTenantResult{
		Tenant: &tenant.TenantInfo{ID: req.ID, Name: req.Name, CreatedAt: now, UpdatedAt: now},
		Admin: service.TenantAdminCredential{
			UserID:     1001,
			Username:   req.AdminUsername,
			CertPEM:    []byte("-----BEGIN CERTIFICATE-----\ntest\n-----END CERTIFICATE-----\n"),
			Serial:     "SERIAL-ADMIN",
			Thumbprint: "mock-admin-thumbprint",
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Mock tenant service
// ---------------------------------------------------------------------------

// mockTenantService implements tenant.TenantServicer with configurable
// function fields for per-test behavior injection.
type mockTenantService struct {
	createFn        func(ctx context.Context, id, name string, isolationType tenant.IsolationType) (*tenant.TenantInfo, error)
	getFn           func(ctx context.Context, tenantID string) (*tenant.TenantInfo, error)
	listFn          func(ctx context.Context) ([]tenant.TenantInfo, error)
	deleteFn        func(ctx context.Context, tenantID string) error
	updateFn        func(ctx context.Context, tenantID string, patch tenant.TenantPatch) (*tenant.TenantInfo, error)
	barrierInitFn   func(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) error
	barrierUnsealFn func(ctx context.Context, tenantID string, key []byte) error
	barrierStatusFn func(ctx context.Context, tenantID string) (*tenant.TenantBarrierStatus, error)
	barrierSealFn   func(ctx context.Context, tenantID string) error

	officerRegisterFn    func(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error)
	officerListFn        func(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error)
	shareClaimBeginFn    func(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error)
	shareClaimCompleteFn func(ctx context.Context, tenantID, username, nonceHex string, signature []byte, thumbprint string) (*ceremony.ShareClaimResult, error)
	quorumBeginFn        func(ctx context.Context, tenantID string, op seal.QuorumOp, username, factor string) (*ceremony.QuorumChallenge, error)
	quorumSubmitFn       func(ctx context.Context, tenantID string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error)
	rekeyStartFn         func(ctx context.Context, tenantID string, threshold int, policy seal.FactorPolicy) (*seal.QuorumRoundInfo, error)
	officerRevokeFn      func(ctx context.Context, tenantID, username string, threshold int) (*seal.QuorumRoundInfo, error)
}

func (m *mockTenantService) CreateTenant(ctx context.Context, id, name string, isolationType tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, id, name, isolationType)
	}
	now := time.Now()
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: isolationType, CreatedAt: now, UpdatedAt: now}, nil
}

func (m *mockTenantService) UpdateTenant(ctx context.Context, tenantID string, patch tenant.TenantPatch) (*tenant.TenantInfo, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, tenantID, patch)
	}
	now := time.Now()
	return &tenant.TenantInfo{ID: tenantID, Name: patch.Name, IsolationType: patch.IsolationType, CreatedAt: now, UpdatedAt: now}, nil
}

func (m *mockTenantService) GetTenant(ctx context.Context, tenantID string) (*tenant.TenantInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, tenantID)
	}
	if tenantID == "notfound" {
		return nil, tenant.ErrTenantNotFound
	}
	now := time.Now()
	return &tenant.TenantInfo{ID: tenantID, Name: "Test Tenant", CreatedAt: now, UpdatedAt: now}, nil
}

func (m *mockTenantService) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenant.DeletedTenantsPage, error) {
	return &tenant.DeletedTenantsPage{}, nil
}

func (m *mockTenantService) ReconcileDeletedTenants(_ context.Context) (*tenant.ReconcileReport, error) {
	return &tenant.ReconcileReport{}, nil
}

func (m *mockTenantService) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	now := time.Now()
	return []tenant.TenantInfo{
		{ID: "tenant-001", Name: "Alpha", CreatedAt: now, UpdatedAt: now},
		{ID: "tenant-002", Name: "Beta", CreatedAt: now, UpdatedAt: now},
	}, nil
}

func (m *mockTenantService) DeleteTenant(ctx context.Context, tenantID string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, tenantID)
	}
	if tenantID == "notfound" {
		return tenant.ErrTenantNotFound
	}
	return nil
}

func (m *mockTenantService) TenantBarrierInit(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int) (*tenant.TenantBarrierInitResult, error) {
	if m.barrierInitFn != nil {
		return nil, m.barrierInitFn(ctx, tenantID, passphrase, threshold, shares)
	}
	return nil, nil
}

func (m *mockTenantService) TenantBarrierInitWithStrategy(ctx context.Context, tenantID string, passphrase []byte, threshold, shares int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, tenantID, passphrase, threshold, shares)
}

func (m *mockTenantService) TenantBarrierUnseal(ctx context.Context, tenantID string, key []byte) error {
	if m.barrierUnsealFn != nil {
		return m.barrierUnsealFn(ctx, tenantID, key)
	}
	return nil
}

func (m *mockTenantService) UpdateQuotas(_ context.Context, tenantID string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	now := time.Now()
	return &tenant.TenantInfo{ID: tenantID, Name: "Updated", Quotas: q, CreatedAt: now, UpdatedAt: now}, nil
}

func (m *mockTenantService) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}

func (m *mockTenantService) Quotas(_ context.Context, _ string) (*quota.Quotas, error) {
	return &quota.Quotas{}, nil
}
func (m *mockTenantService) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantService) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantService) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantService) TenantBarrierOfficerRegister(ctx context.Context, tenantID string, req *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	if m.officerRegisterFn != nil {
		return m.officerRegisterFn(ctx, tenantID, req)
	}
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *mockTenantService) TenantBarrierOfficerList(ctx context.Context, tenantID string) ([]*ceremony.OfficerStatus, error) {
	if m.officerListFn != nil {
		return m.officerListFn(ctx, tenantID)
	}
	return nil, nil
}
func (m *mockTenantService) TenantBarrierShareClaimBegin(ctx context.Context, tenantID, username, thumbprint string) (*ceremony.ChallengeResponse, error) {
	if m.shareClaimBeginFn != nil {
		return m.shareClaimBeginFn(ctx, tenantID, username, thumbprint)
	}
	return &ceremony.ChallengeResponse{}, nil
}
func (m *mockTenantService) TenantBarrierShareClaimComplete(ctx context.Context, tenantID, username, nonceHex string, signature []byte, thumbprint string) (*ceremony.ShareClaimResult, error) {
	if m.shareClaimCompleteFn != nil {
		return m.shareClaimCompleteFn(ctx, tenantID, username, nonceHex, signature, thumbprint)
	}
	return &ceremony.ShareClaimResult{}, nil
}

// errTenantHolderUnset answers a holder method the test did not configure.
var errTenantHolderUnset = errors.New("mockTenantService: holder method not configured by this test")

func (m *mockTenantService) TenantBarrierQuorumBegin(ctx context.Context, tenantID string, op seal.QuorumOp, username, factor string) (*ceremony.QuorumChallenge, error) {
	if m.quorumBeginFn != nil {
		return m.quorumBeginFn(ctx, tenantID, op, username, factor)
	}
	return nil, errTenantHolderUnset
}
func (m *mockTenantService) TenantBarrierQuorumSubmit(ctx context.Context, tenantID string, sub *seal.HolderSubmission) (*seal.QuorumOutcome, error) {
	if m.quorumSubmitFn != nil {
		return m.quorumSubmitFn(ctx, tenantID, sub)
	}
	return nil, errTenantHolderUnset
}
func (m *mockTenantService) TenantBarrierRekeyStart(ctx context.Context, tenantID string, threshold int, policy seal.FactorPolicy) (*seal.QuorumRoundInfo, error) {
	if m.rekeyStartFn != nil {
		return m.rekeyStartFn(ctx, tenantID, threshold, policy)
	}
	return nil, errTenantHolderUnset
}
func (m *mockTenantService) TenantBarrierOfficerRevoke(ctx context.Context, tenantID, username string, threshold int) (*seal.QuorumRoundInfo, error) {
	if m.officerRevokeFn != nil {
		return m.officerRevokeFn(ctx, tenantID, username, threshold)
	}
	return nil, errTenantHolderUnset
}

// CheckBarrierMode lives HERE, with the rest of the method set. It used to sit
// in bindings_officer_test.go, which is `!integration`, while the type is
// unconstrained: under BUILD_TAGS+integration the double still compiled and had
// silently stopped implementing tenant.TenantServicer, failing at
// newTestTenantBinding's call site rather than anywhere near the cause (#203).
// A type's method set must not straddle a build constraint.
func (m *mockTenantService) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}

// newTestTenantBinding creates a TenantBinding wired to the given mock service.
func newTestTenantBinding(t *testing.T, svc tenant.TenantServicer) *TenantBinding {
	t.Helper()
	conn := newTestConnectionBinding(
		t,
		&noopKVService{},
		newMockAdminService(),
		embedded.WithTenantService(svc),
	)
	return NewTenantBinding(testLogger(t), conn)
}

// newTestTenantBindingWithOnboarding wires the embedded transport's
// CreateTenant path, which delegates to service.TenantOnboardingServicer
// rather than tenant.TenantServicer (sdk/go/transport/embedded/tenant.go).
func newTestTenantBindingWithOnboarding(t *testing.T, onboarding service.TenantOnboardingServicer) *TenantBinding {
	t.Helper()
	conn := newTestConnectionBinding(
		t,
		&noopKVService{},
		newMockAdminService(),
		embedded.WithTenantOnboarding(onboarding),
	)
	return NewTenantBinding(testLogger(t), conn)
}

// ---------------------------------------------------------------------------
// TenantBinding.Create tests
// ---------------------------------------------------------------------------

func TestTenantBinding_Create_Success(t *testing.T) {
	b := newTestTenantBindingWithOnboarding(t, &mockTenantOnboarding{})

	resp, err := b.Create(&transport.CreateTenantRequest{ID: "tenant-001", Name: "Alpha", AdminUsername: "admin1", AdminCSRPEM: "csr"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if resp.Tenant.ID != "tenant-001" {
		t.Fatalf("expected tenant ID tenant-001, got %s", resp.Tenant.ID)
	}
	if resp.Tenant.Name != "Alpha" {
		t.Fatalf("expected tenant name Alpha, got %s", resp.Tenant.Name)
	}
	if resp.Admin.Username != "admin1" {
		t.Fatalf("expected admin username admin1, got %s", resp.Admin.Username)
	}
}

func TestTenantBinding_Create_ServiceError(t *testing.T) {
	onboarding := &mockTenantOnboarding{
		onboardFn: func(_ context.Context, _ *service.OnboardTenantRequest) (*service.OnboardTenantResult, error) {
			return nil, tenant.ErrTenantAlreadyExists
		},
	}
	b := newTestTenantBindingWithOnboarding(t, onboarding)

	_, err := b.Create(&transport.CreateTenantRequest{ID: "dup", Name: "Duplicate", AdminUsername: "admin1", AdminCSRPEM: "csr"})
	if err == nil {
		t.Fatal("expected error for duplicate tenant")
	}
	if !errors.Is(err, tenant.ErrTenantAlreadyExists) {
		t.Fatalf("expected ErrTenantAlreadyExists, got %T: %v", err, err)
	}
}

func TestTenantBinding_Create_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTenantBinding(testLogger(t), conn)

	_, err := b.Create(&transport.CreateTenantRequest{ID: "t1", Name: "T1"})
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// TenantBinding.List tests
// ---------------------------------------------------------------------------

func TestTenantBinding_List_Success(t *testing.T) {
	svc := &mockTenantService{}
	b := newTestTenantBinding(t, svc)

	resp, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(resp.Tenants) != 2 {
		t.Fatalf("expected 2 tenants, got %d", len(resp.Tenants))
	}
}

func TestTenantBinding_List_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		listFn: func(_ context.Context) ([]tenant.TenantInfo, error) {
			return nil, errors.New("storage unavailable")
		},
	}
	b := newTestTenantBinding(t, svc)

	_, err := b.List()
	if err == nil {
		t.Fatal("expected error from service")
	}
}

func TestTenantBinding_List_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTenantBinding(testLogger(t), conn)

	_, err := b.List()
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBinding.Get tests
// ---------------------------------------------------------------------------

func TestTenantBinding_Get_Success(t *testing.T) {
	svc := &mockTenantService{}
	b := newTestTenantBinding(t, svc)

	resp, err := b.Get("tenant-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if resp.Tenant.ID != "tenant-001" {
		t.Fatalf("expected ID tenant-001, got %s", resp.Tenant.ID)
	}
}

func TestTenantBinding_Get_NotFound(t *testing.T) {
	svc := &mockTenantService{}
	b := newTestTenantBinding(t, svc)

	_, err := b.Get("notfound")
	if err == nil {
		t.Fatal("expected error for unknown tenant")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %T: %v", err, err)
	}
}

func TestTenantBinding_Get_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTenantBinding(testLogger(t), conn)

	_, err := b.Get("tenant-001")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBinding.Delete tests
// ---------------------------------------------------------------------------

func TestTenantBinding_Delete_Success(t *testing.T) {
	svc := &mockTenantService{}
	b := newTestTenantBinding(t, svc)

	if err := b.Delete("tenant-001"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestTenantBinding_Delete_NotFound(t *testing.T) {
	svc := &mockTenantService{}
	b := newTestTenantBinding(t, svc)

	err := b.Delete("notfound")
	if err == nil {
		t.Fatal("expected error for unknown tenant")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %T: %v", err, err)
	}
}

func TestTenantBinding_Delete_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTenantBinding(testLogger(t), conn)

	if err := b.Delete("tenant-001"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBinding.BarrierInit tests
// ---------------------------------------------------------------------------

func TestTenantBinding_BarrierInit_Success(t *testing.T) {
	svc := &mockTenantService{}
	b := newTestTenantBinding(t, svc)

	err := b.BarrierInit(&transport.TenantBarrierInitRequest{
		TenantID:  "tenant-001",
		Threshold: 2,
		Shares:    3,
	})
	if err != nil {
		t.Fatalf("BarrierInit: %v", err)
	}
}

func TestTenantBinding_BarrierInit_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierInitFn: func(_ context.Context, _ string, _ []byte, _, _ int) error {
			return &tenant.BarrierError{TenantID: "tenant-001", Op: "init", Err: errors.New("already initialized")}
		},
	}
	b := newTestTenantBinding(t, svc)

	err := b.BarrierInit(&transport.TenantBarrierInitRequest{TenantID: "tenant-001"})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var barrierErr *tenant.BarrierError
	if !errors.As(err, &barrierErr) {
		t.Fatalf("expected BarrierError, got %T: %v", err, err)
	}
}

func TestTenantBinding_BarrierInit_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTenantBinding(testLogger(t), conn)

	err := b.BarrierInit(&transport.TenantBarrierInitRequest{TenantID: "tenant-001"})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBinding.BarrierUnseal tests
// ---------------------------------------------------------------------------

func TestTenantBinding_BarrierUnseal_Success(t *testing.T) {
	svc := &mockTenantService{}
	b := newTestTenantBinding(t, svc)

	err := b.BarrierUnseal(&transport.TenantBarrierUnsealRequest{
		TenantID: "tenant-001",
		Key:      []byte("supersecretkey32bytes_____________"),
	})
	if err != nil {
		t.Fatalf("BarrierUnseal: %v", err)
	}
}

func TestTenantBinding_BarrierUnseal_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierUnsealFn: func(_ context.Context, _ string, _ []byte) error {
			return &tenant.BarrierError{TenantID: "tenant-001", Op: "unseal", Err: errors.New("invalid key")}
		},
	}
	b := newTestTenantBinding(t, svc)

	err := b.BarrierUnseal(&transport.TenantBarrierUnsealRequest{
		TenantID: "tenant-001",
		Key:      []byte("badkey"),
	})
	if err == nil {
		t.Fatal("expected error from service")
	}
	var barrierErr *tenant.BarrierError
	if !errors.As(err, &barrierErr) {
		t.Fatalf("expected BarrierError, got %T: %v", err, err)
	}
}

func TestTenantBinding_BarrierUnseal_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTenantBinding(testLogger(t), conn)

	err := b.BarrierUnseal(&transport.TenantBarrierUnsealRequest{TenantID: "tenant-001", Key: []byte("key")})
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// TenantBinding.BarrierStatus tests
// ---------------------------------------------------------------------------

func TestTenantBinding_BarrierStatus_Success(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
			return &tenant.TenantBarrierStatus{
				TenantID:    tenantID,
				Sealed:      true,
				Initialized: true,
				Strategy:    "shamir",
			}, nil
		},
	}
	b := newTestTenantBinding(t, svc)

	status, err := b.BarrierStatus("tenant-001")
	if err != nil {
		t.Fatalf("BarrierStatus: %v", err)
	}
	if status.TenantID != "tenant-001" {
		t.Fatalf("expected tenant tenant-001, got %s", status.TenantID)
	}
	if !status.Sealed || !status.Initialized {
		t.Fatalf("expected sealed+initialized, got sealed=%v initialized=%v", status.Sealed, status.Initialized)
	}
	if status.Strategy != "shamir" {
		t.Fatalf("expected strategy shamir, got %s", status.Strategy)
	}
}

func TestTenantBinding_BarrierStatus_ServiceError(t *testing.T) {
	svc := &mockTenantService{
		barrierStatusFn: func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
			return nil, tenant.ErrTenantNotFound
		},
	}
	b := newTestTenantBinding(t, svc)

	_, err := b.BarrierStatus("notfound")
	if err == nil {
		t.Fatal("expected error for unknown tenant")
	}
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("expected ErrTenantNotFound, got %T: %v", err, err)
	}
}

func TestTenantBinding_BarrierStatus_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewTenantBinding(testLogger(t), conn)

	_, err := b.BarrierStatus("tenant-001")
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}
