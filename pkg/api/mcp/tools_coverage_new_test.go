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

//go:build codec_json && raft

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/permission"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/user"
)

// ---------------------------------------------------------------------------
// Mocks: barrierServicer (lightweight, no build-tag conflict)
// ---------------------------------------------------------------------------

type covMockBarrierSvc struct {
	initializeFn           func(ctx context.Context, secret []byte) error
	unsealFn               func(ctx context.Context, secret []byte) error
	sealFn                 func(ctx context.Context) error
	statusFn               func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn     func(ctx context.Context, secret []byte, threshold, totalShares int) (*service.ShamirInitInfo, error)
	unsealWithShareFn      func(ctx context.Context, share string) (*service.UnsealShareProgress, error)
	unsealWithSharesFn     func(ctx context.Context, shares []string) error
	rekeyFn                func(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error)
	generateRecoveryKeysFn func(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error)
	recoverWithKeysFn      func(ctx context.Context, keys []string) error
	deleteRecoveryKeysFn   func(ctx context.Context) error
	hasRecoveryKeysFn      func(ctx context.Context) (bool, error)
	generateRootTokenFn    func(ctx context.Context, shares []string) (*service.RootTokenInfo, error)
}

func newCovMockBarrierSvc() *covMockBarrierSvc { return &covMockBarrierSvc{} }

func (m *covMockBarrierSvc) BarrierInitialize(ctx context.Context, s []byte) error {
	if m.initializeFn != nil {
		return m.initializeFn(ctx, s)
	}
	return nil
}
func (m *covMockBarrierSvc) BarrierUnseal(ctx context.Context, s []byte) error {
	if m.unsealFn != nil {
		return m.unsealFn(ctx, s)
	}
	return nil
}
func (m *covMockBarrierSvc) BarrierSeal(ctx context.Context) error {
	if m.sealFn != nil {
		return m.sealFn(ctx)
	}
	return nil
}
func (m *covMockBarrierSvc) BarrierStatus(ctx context.Context) (*service.BarrierStatusInfo, error) {
	if m.statusFn != nil {
		return m.statusFn(ctx)
	}
	return &service.BarrierStatusInfo{Sealed: false, Strategy: "software"}, nil
}
func (m *covMockBarrierSvc) BarrierInitializeShamir(ctx context.Context, secret []byte, threshold, totalShares int) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, secret, threshold, totalShares)
	}
	return &service.ShamirInitInfo{Threshold: threshold, TotalShares: totalShares, ShareCount: totalShares}, nil
}
func (m *covMockBarrierSvc) BarrierUnsealWithShare(ctx context.Context, share string) (*service.UnsealShareProgress, error) {
	if m.unsealWithShareFn != nil {
		return m.unsealWithShareFn(ctx, share)
	}
	return &service.UnsealShareProgress{Required: 3, Submitted: 1, Complete: false}, nil
}
func (m *covMockBarrierSvc) BarrierUnsealWithShares(ctx context.Context, shares []string) error {
	if m.unsealWithSharesFn != nil {
		return m.unsealWithSharesFn(ctx, shares)
	}
	return nil
}
func (m *covMockBarrierSvc) BarrierRekey(ctx context.Context, threshold, total int) (*service.ShamirInitInfo, error) {
	if m.rekeyFn != nil {
		return m.rekeyFn(ctx, threshold, total)
	}
	return &service.ShamirInitInfo{Threshold: threshold, TotalShares: total, ShareCount: total}, nil
}
func (m *covMockBarrierSvc) BarrierGenerateRecoveryKeys(ctx context.Context, threshold, total int) (*service.RecoveryKeysInfo, error) {
	if m.generateRecoveryKeysFn != nil {
		return m.generateRecoveryKeysFn(ctx, threshold, total)
	}
	return &service.RecoveryKeysInfo{Threshold: threshold, Total: total, ShareCount: total}, nil
}
func (m *covMockBarrierSvc) BarrierRecoverWithKeys(ctx context.Context, keys []string) error {
	if m.recoverWithKeysFn != nil {
		return m.recoverWithKeysFn(ctx, keys)
	}
	return nil
}
func (m *covMockBarrierSvc) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryKeysFn != nil {
		return m.deleteRecoveryKeysFn(ctx)
	}
	return nil
}
func (m *covMockBarrierSvc) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return true, nil
}
func (m *covMockBarrierSvc) BarrierGenerateRootToken(ctx context.Context, shares []string) (*service.RootTokenInfo, error) {
	if m.generateRootTokenFn != nil {
		return m.generateRootTokenFn(ctx, shares)
	}
	return &service.RootTokenInfo{Token: "tok-abc", CreatedAt: time.Now()}, nil
}
func (m *covMockBarrierSvc) BarrierOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (m *covMockBarrierSvc) BarrierOfficerList(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (m *covMockBarrierSvc) BarrierOfficerClaimStatus(_ context.Context) ([]*service.BarrierOfficerStatus, error) {
	return nil, nil
}
func (m *covMockBarrierSvc) BarrierShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (m *covMockBarrierSvc) BarrierShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}
func (m *covMockBarrierSvc) BarrierShamirSealVote(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
	return &service.UnsealShareProgress{}, nil
}
func (m *covMockBarrierSvc) BarrierRecoveryOfficerRegister(_ context.Context, _ *service.BarrierOfficerRegisterRequest) (*service.BarrierOfficerRegisterResult, error) {
	return &service.BarrierOfficerRegisterResult{}, nil
}
func (m *covMockBarrierSvc) BarrierRecoveryShareClaimBegin(_ context.Context, _, _ string) (*service.BarrierShareClaimChallenge, error) {
	return &service.BarrierShareClaimChallenge{}, nil
}
func (m *covMockBarrierSvc) BarrierRecoveryShareClaimComplete(_ context.Context, _, _ string, _ []byte, _ string) (*service.BarrierShareClaimResult, error) {
	return &service.BarrierShareClaimResult{}, nil
}

// ---------------------------------------------------------------------------
// Mock: TenantServicer
// ---------------------------------------------------------------------------

type covMockTenantSvc struct {
	createFn        func(ctx context.Context, id, name string, iso tenant.IsolationType) (*tenant.TenantInfo, error)
	getFn           func(ctx context.Context, id string) (*tenant.TenantInfo, error)
	listFn          func(ctx context.Context) ([]tenant.TenantInfo, error)
	updateFn        func(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error)
	deleteFn        func(ctx context.Context, id string) error
	updateQuotasFn  func(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error)
	barrierInitFn   func(ctx context.Context, id string, passphrase []byte, threshold, total int) error
	barrierUnsealFn func(ctx context.Context, id string, key []byte) error
	barrierSealFn   func(ctx context.Context, id string) error
	barrierStatusFn func(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error)
}

var _ tenant.TenantServicer = (*covMockTenantSvc)(nil)

func newCovMockTenantSvc() *covMockTenantSvc { return &covMockTenantSvc{} }

// allowAllTenantScope is a tenantScopeAuthorizer stub that permits every
// target tenant, isolating handler-behavior tests from authorization. It
// lives here (minimal build-tag set) so every test compilation context that
// includes the tenant-barrier handlers can reference it.
func allowAllTenantScope(_ context.Context, _ string, _ permission.Permission) error {
	return nil
}

// allowAllTenantInit is the init-gate sibling of allowAllTenantScope: it
// permits every tenant barrier initialization, isolating handler-behavior
// tests from the trust-on-first-init authorization rule.
func allowAllTenantInit(_ context.Context, _ string) error { return nil }

// allowAllTenantBarrierAuthz builds a tenantBarrierAuthorizers whose three
// gates all permit every invocation, for registration/coverage tests that
// exercise handler behavior rather than authorization.
func allowAllTenantBarrierAuthz() tenantBarrierAuthorizers {
	return tenantBarrierAuthorizers{
		Op:     allowAllTenantScope,
		Init:   allowAllTenantInit,
		Sealed: allowAllTenantScope,
	}
}

func (m *covMockTenantSvc) CreateTenant(ctx context.Context, id, name string, iso tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, id, name, iso)
	}
	return &tenant.TenantInfo{ID: id, Name: name, IsolationType: iso, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}
func (m *covMockTenantSvc) GetTenant(ctx context.Context, id string) (*tenant.TenantInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &tenant.TenantInfo{ID: id, Name: "test"}, nil
}
func (m *covMockTenantSvc) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
func (m *covMockTenantSvc) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []tenant.TenantInfo{{ID: "t1", Name: "Tenant One"}}, nil
}
func (m *covMockTenantSvc) UpdateTenant(ctx context.Context, id string, p tenant.TenantPatch) (*tenant.TenantInfo, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, id, p)
	}
	return &tenant.TenantInfo{ID: id, Name: p.Name}, nil
}
func (m *covMockTenantSvc) DeleteTenant(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}
func (m *covMockTenantSvc) UpdateQuotas(ctx context.Context, id string, q *quota.Quotas) (*tenant.TenantInfo, error) {
	if m.updateQuotasFn != nil {
		return m.updateQuotasFn(ctx, id, q)
	}
	return &tenant.TenantInfo{ID: id, Quotas: q}, nil
}

func (m *covMockTenantSvc) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}
func (m *covMockTenantSvc) Quotas(ctx context.Context, id string) (*quota.Quotas, error) {
	return &quota.Quotas{}, nil
}
func (m *covMockTenantSvc) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *covMockTenantSvc) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *covMockTenantSvc) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}
func (m *covMockTenantSvc) TenantBarrierInit(ctx context.Context, id string, passphrase []byte, threshold, total int) (*tenant.TenantBarrierInitResult, error) {
	if m.barrierInitFn != nil {
		if err := m.barrierInitFn(ctx, id, passphrase, threshold, total); err != nil {
			return nil, err
		}
	}
	return &tenant.TenantBarrierInitResult{TenantID: id, Strategy: string(seal.StrategyPassphrase)}, nil
}
func (m *covMockTenantSvc) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}
func (m *covMockTenantSvc) TenantBarrierUnsealShare(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return nil, nil
}
func (m *covMockTenantSvc) TenantBarrierUnseal(ctx context.Context, id string, key []byte) error {
	if m.barrierUnsealFn != nil {
		return m.barrierUnsealFn(ctx, id, key)
	}
	return nil
}
func (m *covMockTenantSvc) TenantBarrierSeal(ctx context.Context, id string) error {
	if m.barrierSealFn != nil {
		return m.barrierSealFn(ctx, id)
	}
	return nil
}
func (m *covMockTenantSvc) TenantBarrierStatus(ctx context.Context, id string) (*tenant.TenantBarrierStatus, error) {
	if m.barrierStatusFn != nil {
		return m.barrierStatusFn(ctx, id)
	}
	return &tenant.TenantBarrierStatus{TenantID: id, Sealed: false, Initialized: true}, nil
}
func (m *covMockTenantSvc) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}
func (m *covMockTenantSvc) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *covMockTenantSvc) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}
func (m *covMockTenantSvc) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}
func (m *covMockTenantSvc) TenantBarrierOfficerClaimStatus(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}
func (m *covMockTenantSvc) TenantBarrierShamirSealVote(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return &tenant.TenantBarrierUnsealProgress{}, nil
}

// ---------------------------------------------------------------------------
// Mock: AuthFactorServicer
// ---------------------------------------------------------------------------

type covMockAuthFactorSvc struct {
	enrollFn    func(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error)
	challengeFn func(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error)
	verifyFn    func(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error
	listFn      func(ctx context.Context, subject string) ([]*authfactors.Enrollment, error)
	revokeFn    func(ctx context.Context, enrollmentID string) error
}

var _ service.AuthFactorServicer = (*covMockAuthFactorSvc)(nil)

func newCovMockAuthFactorSvc() *covMockAuthFactorSvc { return &covMockAuthFactorSvc{} }

func (m *covMockAuthFactorSvc) EnrollFactor(ctx context.Context, factorName, subject string, params authfactors.EnrollParams) (*authfactors.Enrollment, error) {
	if m.enrollFn != nil {
		return m.enrollFn(ctx, factorName, subject, params)
	}
	return &authfactors.Enrollment{
		EnrollmentID: "e-1",
		FactorName:   factorName,
		UserID:       subject,
		TenantID:     params.TenantID,
		EnrolledAt:   time.Now(),
	}, nil
}
func (m *covMockAuthFactorSvc) ChallengeFactor(ctx context.Context, enrollmentID string) (*authfactors.Challenge, error) {
	if m.challengeFn != nil {
		return m.challengeFn(ctx, enrollmentID)
	}
	return &authfactors.Challenge{
		ChallengeID:  "ch-1",
		EnrollmentID: enrollmentID,
		Nonce:        []byte("nonce"),
		ExpiresAt:    time.Now().Add(5 * time.Minute),
	}, nil
}
func (m *covMockAuthFactorSvc) VerifyFactor(ctx context.Context, factorName, challengeID string, response *authfactors.Response) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, factorName, challengeID, response)
	}
	return nil
}
func (m *covMockAuthFactorSvc) ListFactors(ctx context.Context, subject string) ([]*authfactors.Enrollment, error) {
	if m.listFn != nil {
		return m.listFn(ctx, subject)
	}
	return []*authfactors.Enrollment{
		{EnrollmentID: "e-1", FactorName: "pkcs11", UserID: subject, EnrolledAt: time.Now()},
	}, nil
}
func (m *covMockAuthFactorSvc) RevokeFactor(ctx context.Context, enrollmentID string) error {
	if m.revokeFn != nil {
		return m.revokeFn(ctx, enrollmentID)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mock: UserAuthFactorServicer
// ---------------------------------------------------------------------------

type covMockUserAuthFactorSvc struct {
	addFn      func(ctx context.Context, userID uint64, factor *auth.AuthFactor) error
	listFn     func(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error)
	getFn      func(ctx context.Context, factorID string) (*auth.AuthFactor, error)
	lookupFn   func(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error)
	removeFn   func(ctx context.Context, factorID string, force bool) error
	rotateFn   func(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error
	relabelFn  func(ctx context.Context, factorID, label string) error
	markUsedFn func(ctx context.Context, factorID string, ts time.Time) error
}

var _ service.UserAuthFactorServicer = (*covMockUserAuthFactorSvc)(nil)

func newCovMockUserAuthFactorSvc() *covMockUserAuthFactorSvc { return &covMockUserAuthFactorSvc{} }

func (m *covMockUserAuthFactorSvc) Add(ctx context.Context, userID uint64, factor *auth.AuthFactor) error {
	if m.addFn != nil {
		return m.addFn(ctx, userID, factor)
	}
	factor.ID = "f-001"
	factor.RegisteredAt = time.Now()
	return nil
}
func (m *covMockUserAuthFactorSvc) List(ctx context.Context, userID uint64) ([]*auth.AuthFactor, error) {
	if m.listFn != nil {
		return m.listFn(ctx, userID)
	}
	return []*auth.AuthFactor{
		{ID: "f-001", UserID: userID, Type: auth.AuthFactorMTLS, Credential: []byte("cred"), RegisteredAt: time.Now()},
	}, nil
}
func (m *covMockUserAuthFactorSvc) Get(ctx context.Context, factorID string) (*auth.AuthFactor, error) {
	if m.getFn != nil {
		return m.getFn(ctx, factorID)
	}
	return &auth.AuthFactor{ID: factorID, Type: auth.AuthFactorMTLS, Credential: []byte("cred"), RegisteredAt: time.Now()}, nil
}
func (m *covMockUserAuthFactorSvc) LookupByCredentialKey(ctx context.Context, t auth.AuthFactorType, key []byte) (*auth.AuthFactor, error) {
	if m.lookupFn != nil {
		return m.lookupFn(ctx, t, key)
	}
	return &auth.AuthFactor{ID: "f-001", UserID: 42, Type: t, Credential: key, RegisteredAt: time.Now()}, nil
}
func (m *covMockUserAuthFactorSvc) Remove(ctx context.Context, factorID string, force bool) error {
	if m.removeFn != nil {
		return m.removeFn(ctx, factorID, force)
	}
	return nil
}
func (m *covMockUserAuthFactorSvc) Rotate(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error {
	if m.rotateFn != nil {
		return m.rotateFn(ctx, oldID, newFactor)
	}
	newFactor.ID = "f-002"
	newFactor.RegisteredAt = time.Now()
	return nil
}
func (m *covMockUserAuthFactorSvc) Relabel(ctx context.Context, factorID, label string) error {
	if m.relabelFn != nil {
		return m.relabelFn(ctx, factorID, label)
	}
	return nil
}
func (m *covMockUserAuthFactorSvc) MarkUsed(ctx context.Context, factorID string, ts time.Time) error {
	if m.markUsedFn != nil {
		return m.markUsedFn(ctx, factorID, ts)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Mock: AuditLogger
// ---------------------------------------------------------------------------

type covMockAuditLogger struct {
	queryFn func(ctx context.Context, f audit.Filter) (*audit.Page, error)
}

func (m *covMockAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) error { return nil }
func (m *covMockAuditLogger) Query(ctx context.Context, f audit.Filter) (*audit.Page, error) {
	if m.queryFn != nil {
		return m.queryFn(ctx, f)
	}
	return &audit.Page{
		Events: []audit.AuditEvent{
			{Actor: "alice", Resource: "barrier", Action: "unseal", Outcome: "success", Timestamp: time.Now()},
		},
		Total: 1,
	}, nil
}
func (m *covMockAuditLogger) Close() error { return nil }

// ===========================================================================
// barrierTools registration
// ===========================================================================

func TestCovBarrierTools_Registration(t *testing.T) {
	svc := newCovMockBarrierSvc()
	tools := barrierTools(svc, nil, nil)
	if len(tools) != 22 {
		t.Fatalf("expected 22 tools, got %d", len(tools))
	}
	names := []string{
		"qrdb_barrier_initialize",
		"qrdb_barrier_unseal",
		"qrdb_barrier_seal",
		"qrdb_barrier_status",
		"qrdb_barrier_shamir_initialize",
		"qrdb_barrier_shamir_unseal_share",
		"qrdb_barrier_shamir_unseal_shares",
		"qrdb_barrier_rekey",
		"qrdb_barrier_recovery_generate",
		"qrdb_barrier_recovery_recover",
		"qrdb_barrier_recovery_delete",
		"qrdb_barrier_recovery_status",
		"qrdb_barrier_root_token",
		"qrdb_barrier_officer_register",
		"qrdb_barrier_officer_list",
		"qrdb_barrier_share_claim_begin",
		"qrdb_barrier_share_claim_complete",
		"qrdb_barrier_officer_claim_status",
		"qrdb_barrier_shamir_seal_vote",
		"qrdb_barrier_recovery_officer_register",
		"qrdb_barrier_recovery_share_claim_begin",
		"qrdb_barrier_recovery_share_claim_complete",
	}
	for i, n := range names {
		if tools[i].Tool.Name != n {
			t.Errorf("tool[%d]: got %q, want %q", i, tools[i].Tool.Name, n)
		}
	}
}

// ===========================================================================
// handleBarrierInitialize
// ===========================================================================

func TestCovHandleBarrierInitialize_Success(t *testing.T) {
	svc := newCovMockBarrierSvc()
	handler := handleBarrierInitialize(svc, nil)
	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{"secret": "s3cr3t"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
	if resultText(t, result) != "barrier initialized" {
		t.Errorf("unexpected text: %s", resultText(t, result))
	}
}

// TestCovHandleBarrierInitialize_MissingSecret proves an empty secret is
// passed through to the service rather than rejected at the handler --
// see tools_barrier_test.go's TestHandleBarrierInitialize_MissingSecret for
// the full rationale (parity with REST/gRPC/QUIC, required for `barrier
// init --barrier-strategy none` to work over MCP).
func TestCovHandleBarrierInitialize_MissingSecret(t *testing.T) {
	svc := newCovMockBarrierSvc()
	var gotSecret string
	gotCalled := false
	svc.initializeFn = func(_ context.Context, secret []byte) error {
		gotCalled = true
		gotSecret = string(secret)
		return nil
	}
	handler := handleBarrierInitialize(svc, nil)
	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Errorf("expected success (empty secret passed through), got error result: %v", result)
	}
	if !gotCalled {
		t.Fatal("expected the service's BarrierInitialize to be called with the empty secret")
	}
	if gotSecret != "" {
		t.Errorf("expected the empty secret to reach the service unchanged, got %q", gotSecret)
	}
}

func TestCovHandleBarrierInitialize_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.initializeFn = func(_ context.Context, _ []byte) error { return errors.New("already initialized") }
	handler := handleBarrierInitialize(svc, nil)
	req := buildCallToolRequest(t, "qrdb_barrier_initialize", map[string]any{"secret": "s"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "qrdb_barrier_initialize" {
		t.Errorf("expected ToolError for qrdb_barrier_initialize, got %v", err)
	}
}

// ===========================================================================
// handleBarrierUnseal
// ===========================================================================

func TestCovHandleBarrierUnseal_Success(t *testing.T) {
	handler := handleBarrierUnseal(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{"secret": "key"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resultText(t, result) != "barrier unsealed" {
		t.Errorf("unexpected: %s", resultText(t, result))
	}
}

func TestCovHandleBarrierUnseal_MissingSecret(t *testing.T) {
	handler := handleBarrierUnseal(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.IsError {
		t.Error("expected error result")
	}
}

func TestCovHandleBarrierUnseal_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.unsealFn = func(_ context.Context, _ []byte) error { return errors.New("bad key") }
	handler := handleBarrierUnseal(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_unseal", map[string]any{"secret": "bad"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierSeal
// ===========================================================================

func TestCovHandleBarrierSeal_Success(t *testing.T) {
	handler := handleBarrierSeal(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_seal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resultText(t, result) != "barrier sealed" {
		t.Errorf("unexpected: %s", resultText(t, result))
	}
}

func TestCovHandleBarrierSeal_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.sealFn = func(_ context.Context) error { return errors.New("seal failed") }
	handler := handleBarrierSeal(svc, nil)
	req := buildCallToolRequest(t, "qrdb_barrier_seal", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "qrdb_barrier_seal" {
		t.Errorf("expected ToolError qrdb_barrier_seal, got %v", err)
	}
}

// ===========================================================================
// handleBarrierStatus
// ===========================================================================

func TestCovHandleBarrierStatus_Success(t *testing.T) {
	handler := handleBarrierStatus(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info service.BarrierStatusInfo
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &info); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if info.Sealed {
		t.Error("expected sealed=false")
	}
}

func TestCovHandleBarrierStatus_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.statusFn = func(_ context.Context) (*service.BarrierStatusInfo, error) {
		return nil, errors.New("unavailable")
	}
	handler := handleBarrierStatus(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_status", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierShamirInitialize
// ===========================================================================

func TestCovHandleBarrierShamirInitialize_Success(t *testing.T) {
	handler := handleBarrierShamirInitialize(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret": "s", "threshold": float64(2), "total_shares": float64(3),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The handler marshals the canonical snake_case wire shape via
	// shamirInitInfoToWire, so decode with explicitly tagged fields.
	var info struct {
		Threshold   int `json:"threshold"`
		TotalShares int `json:"total_shares"`
		ShareCount  int `json:"share_count"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &info); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if info.Threshold != 2 || info.TotalShares != 3 {
		t.Errorf("unexpected info: %+v", info)
	}
}

func TestCovHandleBarrierShamirInitialize_MissingSecret(t *testing.T) {
	handler := handleBarrierShamirInitialize(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"threshold": float64(2), "total_shares": float64(3),
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result, got err=%v result.IsError=%v", err, result.IsError)
	}
}

func TestCovHandleBarrierShamirInitialize_MissingThreshold(t *testing.T) {
	handler := handleBarrierShamirInitialize(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret": "s", "total_shares": float64(3),
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result, got err=%v result.IsError=%v", err, result.IsError)
	}
}

func TestCovHandleBarrierShamirInitialize_MissingTotalShares(t *testing.T) {
	handler := handleBarrierShamirInitialize(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret": "s", "threshold": float64(2),
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result, got err=%v result.IsError=%v", err, result.IsError)
	}
}

func TestCovHandleBarrierShamirInitialize_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.initializeShamirFn = func(_ context.Context, _ []byte, _, _ int) (*service.ShamirInitInfo, error) {
		return nil, errors.New("failed")
	}
	handler := handleBarrierShamirInitialize(svc, nil)
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_initialize", map[string]any{
		"secret": "s", "threshold": float64(2), "total_shares": float64(3),
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierShamirUnsealShare
// ===========================================================================

func TestCovHandleBarrierShamirUnsealShare_Success(t *testing.T) {
	handler := handleBarrierShamirUnsealShare(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_share", map[string]any{"share": "s1"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var prog service.UnsealShareProgress
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &prog); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if prog.Required != 3 {
		t.Errorf("expected required=3, got %d", prog.Required)
	}
}

func TestCovHandleBarrierShamirUnsealShare_MissingShare(t *testing.T) {
	handler := handleBarrierShamirUnsealShare(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_share", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleBarrierShamirUnsealShare_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.unsealWithShareFn = func(_ context.Context, _ string) (*service.UnsealShareProgress, error) {
		return nil, errors.New("invalid share")
	}
	handler := handleBarrierShamirUnsealShare(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_share", map[string]any{"share": "bad"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierShamirUnsealShares
// ===========================================================================

func TestCovHandleBarrierShamirUnsealShares_Success(t *testing.T) {
	handler := handleBarrierShamirUnsealShares(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_shares", map[string]any{"shares": "s1,s2,s3"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resultText(t, result) != "barrier unsealed" {
		t.Errorf("unexpected text: %s", resultText(t, result))
	}
}

func TestCovHandleBarrierShamirUnsealShares_MissingShares(t *testing.T) {
	handler := handleBarrierShamirUnsealShares(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_shares", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleBarrierShamirUnsealShares_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.unsealWithSharesFn = func(_ context.Context, _ []string) error { return errors.New("err") }
	handler := handleBarrierShamirUnsealShares(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_shamir_unseal_shares", map[string]any{"shares": "a,b"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierRekey
// ===========================================================================

func TestCovHandleBarrierRekey_Success(t *testing.T) {
	handler := handleBarrierRekey(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{"threshold": float64(2), "total": float64(4)})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Rekey shares the snake_case wire shape produced by shamirInitInfoToWire.
	var info struct {
		Threshold   int `json:"threshold"`
		TotalShares int `json:"total_shares"`
		ShareCount  int `json:"share_count"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &info); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if info.Threshold != 2 || info.TotalShares != 4 {
		t.Errorf("unexpected: %+v", info)
	}
}

func TestCovHandleBarrierRekey_MissingThreshold(t *testing.T) {
	handler := handleBarrierRekey(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{"total": float64(4)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleBarrierRekey_MissingTotal(t *testing.T) {
	handler := handleBarrierRekey(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{"threshold": float64(2)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleBarrierRekey_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.rekeyFn = func(_ context.Context, _, _ int) (*service.ShamirInitInfo, error) {
		return nil, errors.New("rekey err")
	}
	handler := handleBarrierRekey(svc, nil)
	req := buildCallToolRequest(t, "qrdb_barrier_rekey", map[string]any{"threshold": float64(2), "total": float64(3)})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierRecoveryGenerate
// ===========================================================================

func TestCovHandleBarrierRecoveryGenerate_Success(t *testing.T) {
	handler := handleBarrierRecoveryGenerate(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{"threshold": float64(2), "total": float64(3)})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info service.RecoveryKeysInfo
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &info); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if info.Total != 3 {
		t.Errorf("expected total=3, got %d", info.Total)
	}
}

func TestCovHandleBarrierRecoveryGenerate_MissingThreshold(t *testing.T) {
	handler := handleBarrierRecoveryGenerate(newCovMockBarrierSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{"total": float64(3)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleBarrierRecoveryGenerate_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.generateRecoveryKeysFn = func(_ context.Context, _, _ int) (*service.RecoveryKeysInfo, error) {
		return nil, errors.New("err")
	}
	handler := handleBarrierRecoveryGenerate(svc, nil)
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_generate", map[string]any{"threshold": float64(2), "total": float64(3)})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierRecoveryRecover
// ===========================================================================

func TestCovHandleBarrierRecoveryRecover_Success(t *testing.T) {
	handler := handleBarrierRecoveryRecover(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_recover", map[string]any{"keys": "k1,k2"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resultText(t, result) != "barrier recovered" {
		t.Errorf("unexpected: %s", resultText(t, result))
	}
}

func TestCovHandleBarrierRecoveryRecover_MissingKeys(t *testing.T) {
	handler := handleBarrierRecoveryRecover(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_recover", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleBarrierRecoveryRecover_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.recoverWithKeysFn = func(_ context.Context, _ []string) error { return errors.New("bad keys") }
	handler := handleBarrierRecoveryRecover(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_recover", map[string]any{"keys": "k1"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierRecoveryDelete
// ===========================================================================

func TestCovHandleBarrierRecoveryDelete_Success(t *testing.T) {
	handler := handleBarrierRecoveryDelete(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_delete", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resultText(t, result) != "recovery keys deleted" {
		t.Errorf("unexpected: %s", resultText(t, result))
	}
}

func TestCovHandleBarrierRecoveryDelete_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.deleteRecoveryKeysFn = func(_ context.Context) error { return errors.New("err") }
	handler := handleBarrierRecoveryDelete(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_delete", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierRecoveryStatus
// ===========================================================================

func TestCovHandleBarrierRecoveryStatus_True(t *testing.T) {
	handler := handleBarrierRecoveryStatus(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp struct {
		HasRecoveryKeys bool `json:"has_recovery_keys"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if !resp.HasRecoveryKeys {
		t.Error("expected has_recovery_keys=true")
	}
}

func TestCovHandleBarrierRecoveryStatus_False(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.hasRecoveryKeysFn = func(_ context.Context) (bool, error) { return false, nil }
	handler := handleBarrierRecoveryStatus(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var resp struct {
		HasRecoveryKeys bool `json:"has_recovery_keys"`
	}
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &resp); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if resp.HasRecoveryKeys {
		t.Error("expected has_recovery_keys=false")
	}
}

func TestCovHandleBarrierRecoveryStatus_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.hasRecoveryKeysFn = func(_ context.Context) (bool, error) { return false, errors.New("err") }
	handler := handleBarrierRecoveryStatus(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_recovery_status", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleBarrierRootToken
// ===========================================================================

func TestCovHandleBarrierRootToken_Success(t *testing.T) {
	handler := handleBarrierRootToken(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_root_token", map[string]any{"shares": "s1,s2,s3"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info service.RootTokenInfo
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &info); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if info.Token == "" {
		t.Error("expected non-empty token")
	}
}

func TestCovHandleBarrierRootToken_MissingShares(t *testing.T) {
	handler := handleBarrierRootToken(newCovMockBarrierSvc())
	req := buildCallToolRequest(t, "qrdb_barrier_root_token", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleBarrierRootToken_ServiceError(t *testing.T) {
	svc := newCovMockBarrierSvc()
	svc.generateRootTokenFn = func(_ context.Context, _ []string) (*service.RootTokenInfo, error) {
		return nil, errors.New("err")
	}
	handler := handleBarrierRootToken(svc)
	req := buildCallToolRequest(t, "qrdb_barrier_root_token", map[string]any{"shares": "s1,s2"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// server.go With* options
// ===========================================================================

func TestCovWithLock(t *testing.T) {
	// WithLock: option sets the lockSvc field; no tools are registered
	// without a full lock service, but the option must be callable.
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithLock(nil))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestCovWithEmbedding(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithEmbedding(nil))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestCovWithRAG(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithRAG(nil))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestCovWithUserService(t *testing.T) {
	svc := &mockUserService{}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithUserService(svc))
	if s.userSvc != svc {
		t.Error("userSvc not set")
	}
	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_user_create"]; !ok {
		t.Error("qrdb_user_create not registered")
	}
}

func TestCovWithRoleService(t *testing.T) {
	userSvc := &mockUserService{}
	roleSvc := &mockRoleService{}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithUserService(userSvc), WithRoleService(roleSvc))
	if s.roleSvc != roleSvc {
		t.Error("roleSvc not set")
	}
	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_role_create"]; !ok {
		t.Error("qrdb_role_create not registered")
	}
}

func TestCovWithCertProvider(t *testing.T) {
	prov := &mockCertProvider{}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithCertProvider(prov))
	if s.certProv != prov {
		t.Error("certProv not set")
	}
}

func TestCovWithBarrierService(t *testing.T) {
	svc := newCovMockBarrierSvc()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithBarrierService(svc))
	if s.barrierSvc != svc {
		t.Error("barrierSvc not set")
	}
	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_barrier_initialize"]; !ok {
		t.Error("qrdb_barrier_initialize not registered")
	}
}

func TestCovWithPINBackend(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithPINBackend(nil))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestCovWithCeremonyService(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithCeremonyService(nil))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestCovWithCredentialService(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithCredentialService(nil))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestCovWithCeremonyCA(t *testing.T) {
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithCeremonyCA(nil))
	if s == nil {
		t.Fatal("NewServer returned nil")
	}
}

func TestCovWithTenantService(t *testing.T) {
	svc := newCovMockTenantSvc()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithTenantService(svc))
	if s.tenantSvc != svc {
		t.Error("tenantSvc not set")
	}
	tools := s.MCPServer().ListTools()
	for _, name := range []string{"qrdb_tenant_create", "qrdb_tenant_get", "qrdb_tenant_barrier_init"} {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %s not registered", name)
		}
	}
}

func TestCovWithAuthFactorService(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithAuthFactorService(svc))
	if s.authFactorSvc != svc {
		t.Error("authFactorSvc not set")
	}
	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_authfactor_enroll"]; !ok {
		t.Error("qrdb_authfactor_enroll not registered")
	}
}

func TestCovWithUserAuthFactorService(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithUserAuthFactorService(svc))
	if s.userAuthFactorSvc != svc {
		t.Error("userAuthFactorSvc not set")
	}
	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_user_authfactor_add"]; !ok {
		t.Error("qrdb_user_authfactor_add not registered")
	}
}

func TestCovWithAuditLogger(t *testing.T) {
	logger := &covMockAuditLogger{}
	s := NewServer(newMockKVService(), newMockAdminService(), ":0",
		WithAuditLogger(logger))
	if s.auditLogger != logger {
		t.Error("auditLogger not set")
	}
	tools := s.MCPServer().ListTools()
	if _, ok := tools["qrdb_audit_query"]; !ok {
		t.Error("qrdb_audit_query not registered")
	}
}

func TestCovWithKVService(t *testing.T) {
	kvSvc := newMockKVService()
	s := NewServer(nil, newMockAdminService(), ":0", WithKVService(kvSvc))
	if s.kvSvc != kvSvc {
		t.Error("kvSvc not set via WithKVService")
	}
}

func TestCovWithAdminService(t *testing.T) {
	adminSvc := newMockAdminService()
	s := NewServer(newMockKVService(), nil, ":0", WithAdminService(adminSvc))
	if s.adminSvc != adminSvc {
		t.Error("adminSvc not set via WithAdminService")
	}
}

// ===========================================================================
// errors.go Error() method
// ===========================================================================

func TestCovCeremonyCANotConfiguredError_Error(t *testing.T) {
	e := &CeremonyCANotConfiguredError{}
	if e.Error() != "mcp: ceremony CA not configured" {
		t.Errorf("unexpected: %s", e.Error())
	}
}

// ===========================================================================
// tenant tools
// ===========================================================================

func TestCovTenantTools_Registration(t *testing.T) {
	svc := newCovMockTenantSvc()
	tools := tenantTools(svc)
	if len(tools) != 7 {
		t.Fatalf("expected 7 tools, got %d", len(tools))
	}
}

func TestCovHandleTenantCreate_Success(t *testing.T) {
	svc := newCovMockTenantSvc()
	handler := handleTenantCreate(svc)
	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{
		"id": "acme", "name": "ACME Corp", "isolation_type": "barrier",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var info tenant.TenantInfo
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &info); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if info.ID != "acme" {
		t.Errorf("expected id 'acme', got %q", info.ID)
	}
}

func TestCovHandleTenantCreate_MissingID(t *testing.T) {
	handler := handleTenantCreate(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{"name": "X"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantCreate_MissingName(t *testing.T) {
	handler := handleTenantCreate(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{"id": "x"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantCreate_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.createFn = func(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantAlreadyExists
	}
	handler := handleTenantCreate(svc)
	req := buildCallToolRequest(t, "qrdb_tenant_create", map[string]any{"id": "x", "name": "X"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
	var te *ToolError
	if !errors.As(err, &te) || te.Tool != "qrdb_tenant_create" {
		t.Errorf("expected ToolError qrdb_tenant_create, got %v", err)
	}
}

func TestCovHandleTenantGet_Success(t *testing.T) {
	handler := handleTenantGet(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{"id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var info tenant.TenantInfo
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &info); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if info.ID != "acme" {
		t.Errorf("expected id 'acme', got %q", info.ID)
	}
}

func TestCovHandleTenantGet_MissingID(t *testing.T) {
	handler := handleTenantGet(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantGet_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.getFn = func(_ context.Context, _ string) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantGet(svc)
	req := buildCallToolRequest(t, "qrdb_tenant_get", map[string]any{"id": "missing"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleTenantList_Success(t *testing.T) {
	handler := handleTenantList(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var tenants []tenant.TenantInfo
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &tenants); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if len(tenants) == 0 {
		t.Error("expected at least one tenant")
	}
}

func TestCovHandleTenantList_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.listFn = func(_ context.Context) ([]tenant.TenantInfo, error) {
		return nil, errors.New("store err")
	}
	handler := handleTenantList(svc)
	req := buildCallToolRequest(t, "qrdb_tenant_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleTenantUpdate_Success(t *testing.T) {
	handler := handleTenantUpdate(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{"id": "acme", "name": "New Name"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleTenantUpdate_MissingID(t *testing.T) {
	handler := handleTenantUpdate(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{"name": "X"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantUpdate_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.updateFn = func(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantUpdate(svc)
	req := buildCallToolRequest(t, "qrdb_tenant_update", map[string]any{"id": "x", "name": "y"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleTenantDelete_Success(t *testing.T) {
	handler := handleTenantDelete(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_delete", map[string]any{"id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleTenantDelete_MissingID(t *testing.T) {
	handler := handleTenantDelete(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_delete", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantDelete_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.deleteFn = func(_ context.Context, _ string) error { return tenant.ErrTenantNotFound }
	handler := handleTenantDelete(svc)
	req := buildCallToolRequest(t, "qrdb_tenant_delete", map[string]any{"id": "missing"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleTenantUpdateQuotas_Success(t *testing.T) {
	handler := handleTenantUpdateQuotas(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_update_quotas", map[string]any{
		"id": "acme", "max_databases": float64(10), "max_keys": float64(1000),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleTenantUpdateQuotas_MissingID(t *testing.T) {
	handler := handleTenantUpdateQuotas(newCovMockTenantSvc())
	req := buildCallToolRequest(t, "qrdb_tenant_update_quotas", map[string]any{"max_databases": float64(5)})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantUpdateQuotas_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.updateQuotasFn = func(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantUpdateQuotas(svc)
	req := buildCallToolRequest(t, "qrdb_tenant_update_quotas", map[string]any{"id": "x"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// tenant barrier tools
// ===========================================================================

func TestCovTenantBarrierTools_Registration(t *testing.T) {
	svc := newCovMockTenantSvc()
	tools := tenantBarrierTools(svc, allowAllTenantBarrierAuthz())
	if len(tools) != 11 {
		t.Fatalf("expected 11 tools, got %d", len(tools))
	}
}

func TestCovHandleTenantBarrierInit_Success(t *testing.T) {
	handler := handleTenantBarrierInit(newCovMockTenantSvc(), allowAllTenantInit)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init", map[string]any{
		"tenant_id": "acme", "passphrase": "secret", "threshold": float64(2), "shares": float64(3),
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleTenantBarrierInit_MissingID(t *testing.T) {
	handler := handleTenantBarrierInit(newCovMockTenantSvc(), allowAllTenantInit)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantBarrierInit_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.barrierInitFn = func(_ context.Context, _ string, _ []byte, _, _ int) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierInit(svc, allowAllTenantInit)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_init", map[string]any{"tenant_id": "missing"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleTenantBarrierUnseal_SuccessWithKey(t *testing.T) {
	handler := handleTenantBarrierUnseal(newCovMockTenantSvc(), allowAllTenantScope)
	encoded := base64.StdEncoding.EncodeToString([]byte("keydata"))
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme", "key": encoded,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleTenantBarrierUnseal_NoKey(t *testing.T) {
	handler := handleTenantBarrierUnseal(newCovMockTenantSvc(), allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{"tenant_id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleTenantBarrierUnseal_MissingID(t *testing.T) {
	handler := handleTenantBarrierUnseal(newCovMockTenantSvc(), allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantBarrierUnseal_BadBase64(t *testing.T) {
	handler := handleTenantBarrierUnseal(newCovMockTenantSvc(), allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{
		"tenant_id": "acme", "key": "!!!bad!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad base64")
	}
}

func TestCovHandleTenantBarrierUnseal_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.barrierUnsealFn = func(_ context.Context, _ string, _ []byte) error {
		return tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierUnseal(svc, allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_unseal", map[string]any{"tenant_id": "missing"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleTenantBarrierSeal_Success(t *testing.T) {
	handler := handleTenantBarrierSeal(newCovMockTenantSvc(), allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{"tenant_id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleTenantBarrierSeal_MissingID(t *testing.T) {
	handler := handleTenantBarrierSeal(newCovMockTenantSvc(), allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantBarrierSeal_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.barrierSealFn = func(_ context.Context, _ string) error { return errors.New("seal err") }
	handler := handleTenantBarrierSeal(svc, allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_seal", map[string]any{"tenant_id": "acme"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleTenantBarrierStatus_Success(t *testing.T) {
	handler := handleTenantBarrierStatus(newCovMockTenantSvc(), allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{"tenant_id": "acme"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var status tenant.TenantBarrierStatus
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &status); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if status.TenantID != "acme" {
		t.Errorf("expected tenant_id 'acme', got %q", status.TenantID)
	}
}

func TestCovHandleTenantBarrierStatus_MissingID(t *testing.T) {
	handler := handleTenantBarrierStatus(newCovMockTenantSvc(), allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleTenantBarrierStatus_ServiceError(t *testing.T) {
	svc := newCovMockTenantSvc()
	svc.barrierStatusFn = func(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
		return nil, tenant.ErrTenantNotFound
	}
	handler := handleTenantBarrierStatus(svc, allowAllTenantScope)
	req := buildCallToolRequest(t, "qrdb_tenant_barrier_status", map[string]any{"tenant_id": "missing"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// authFactorTools
// ===========================================================================

func TestCovAuthFactorTools_Registration(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	tools := authFactorTools(svc)
	if len(tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools))
	}
}

func TestCovHandleAuthFactorEnroll_Success(t *testing.T) {
	handler := handleAuthFactorEnroll(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"factor_name": "pkcs11",
		"subject":     "user-001",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var m map[string]any
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &m); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if m["enrollment_id"] == "" {
		t.Error("expected enrollment_id")
	}
}

func TestCovHandleAuthFactorEnroll_MissingFactorName(t *testing.T) {
	handler := handleAuthFactorEnroll(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{"subject": "u"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleAuthFactorEnroll_MissingSubject(t *testing.T) {
	handler := handleAuthFactorEnroll(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{"factor_name": "pkcs11"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleAuthFactorEnroll_BadMethodParams(t *testing.T) {
	handler := handleAuthFactorEnroll(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"factor_name":   "pkcs11",
		"subject":       "u",
		"method_params": "{not-valid-json",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad JSON")
	}
}

func TestCovHandleAuthFactorEnroll_ServiceError(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	svc.enrollFn = func(_ context.Context, _, _ string, _ authfactors.EnrollParams) (*authfactors.Enrollment, error) {
		return nil, errors.New("enroll failed")
	}
	handler := handleAuthFactorEnroll(svc)
	req := buildCallToolRequest(t, "qrdb_authfactor_enroll", map[string]any{
		"factor_name": "pkcs11", "subject": "u",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleAuthFactorChallenge_Success(t *testing.T) {
	handler := handleAuthFactorChallenge(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_challenge", map[string]any{"enrollment_id": "e-1"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleAuthFactorChallenge_MissingID(t *testing.T) {
	handler := handleAuthFactorChallenge(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_challenge", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleAuthFactorChallenge_ServiceError(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	svc.challengeFn = func(_ context.Context, _ string) (*authfactors.Challenge, error) {
		return nil, errors.New("not found")
	}
	handler := handleAuthFactorChallenge(svc)
	req := buildCallToolRequest(t, "qrdb_authfactor_challenge", map[string]any{"enrollment_id": "e-bad"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleAuthFactorVerify_Success(t *testing.T) {
	handler := handleAuthFactorVerify(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"challenge_id":  "ch-1",
		"response_json": `{"value":"dGVzdA==","metadata":{}}`,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleAuthFactorVerify_MissingChallengeID(t *testing.T) {
	handler := handleAuthFactorVerify(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"response_json": `{"value":"dGVzdA=="}`,
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleAuthFactorVerify_MissingResponseJSON(t *testing.T) {
	handler := handleAuthFactorVerify(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{"challenge_id": "ch-1"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleAuthFactorVerify_BadResponseJSON(t *testing.T) {
	handler := handleAuthFactorVerify(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"challenge_id":  "ch-1",
		"response_json": "{bad",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad JSON")
	}
}

func TestCovHandleAuthFactorVerify_ServiceError(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	svc.verifyFn = func(_ context.Context, _, _ string, _ *authfactors.Response) error {
		return errors.New("verify failed")
	}
	handler := handleAuthFactorVerify(svc)
	req := buildCallToolRequest(t, "qrdb_authfactor_verify", map[string]any{
		"challenge_id":  "ch-1",
		"response_json": `{"value":"dGVzdA=="}`,
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleAuthFactorList_Success(t *testing.T) {
	handler := handleAuthFactorList(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_list", map[string]any{"subject": "user-001"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var m map[string]any
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &m); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if m["total"].(float64) < 1 {
		t.Error("expected at least one enrollment")
	}
}

func TestCovHandleAuthFactorList_WithRevokedAt(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	revokedAt := time.Now()
	svc.listFn = func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return []*authfactors.Enrollment{
			{EnrollmentID: "e-1", FactorName: "piv", UserID: "u", EnrolledAt: time.Now(), RevokedAt: revokedAt},
		}, nil
	}
	handler := handleAuthFactorList(svc)
	req := buildCallToolRequest(t, "qrdb_authfactor_list", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleAuthFactorList_ServiceError(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	svc.listFn = func(_ context.Context, _ string) ([]*authfactors.Enrollment, error) {
		return nil, errors.New("list err")
	}
	handler := handleAuthFactorList(svc)
	req := buildCallToolRequest(t, "qrdb_authfactor_list", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleAuthFactorRevoke_Success(t *testing.T) {
	handler := handleAuthFactorRevoke(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_revoke", map[string]any{"enrollment_id": "e-1"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleAuthFactorRevoke_MissingID(t *testing.T) {
	handler := handleAuthFactorRevoke(newCovMockAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_authfactor_revoke", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleAuthFactorRevoke_ServiceError(t *testing.T) {
	svc := newCovMockAuthFactorSvc()
	svc.revokeFn = func(_ context.Context, _ string) error { return errors.New("revoke err") }
	handler := handleAuthFactorRevoke(svc)
	req := buildCallToolRequest(t, "qrdb_authfactor_revoke", map[string]any{"enrollment_id": "e-1"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// userAuthFactorTools
// ===========================================================================

func TestCovUserAuthFactorTools_Registration(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	tools := userAuthFactorTools(svc, nil)
	if len(tools) != 7 {
		t.Fatalf("expected 7 tools, got %d", len(tools))
	}
}

func TestCovHandleUserAuthFactorAdd_Success(t *testing.T) {
	handler := handleUserAuthFactorAddTool(newCovMockUserAuthFactorSvc())
	cred := base64.StdEncoding.EncodeToString([]byte("creddata"))
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": cred,
		"label":          "my key",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", resultText(t, result))
	}
}

func TestCovHandleUserAuthFactorAdd_BadUserID(t *testing.T) {
	handler := handleUserAuthFactorAddTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{
		"user_id": "not-a-number",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad user_id")
	}
}

func TestCovHandleUserAuthFactorAdd_MissingTypeOrCred(t *testing.T) {
	handler := handleUserAuthFactorAddTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{"user_id": "42"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for missing type/cred")
	}
}

func TestCovHandleUserAuthFactorAdd_BadCredBase64(t *testing.T) {
	handler := handleUserAuthFactorAddTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": "!!!bad!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad base64")
	}
}

func TestCovHandleUserAuthFactorAdd_BadMetadataJSON(t *testing.T) {
	handler := handleUserAuthFactorAddTool(newCovMockUserAuthFactorSvc())
	cred := base64.StdEncoding.EncodeToString([]byte("cred"))
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": cred,
		"metadata_json":  "{bad",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad metadata JSON")
	}
}

func TestCovHandleUserAuthFactorAdd_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.addFn = func(_ context.Context, _ uint64, _ *auth.AuthFactor) error {
		return errors.New("add failed")
	}
	handler := handleUserAuthFactorAddTool(svc)
	cred := base64.StdEncoding.EncodeToString([]byte("cred"))
	req := buildCallToolRequest(t, "qrdb_user_authfactor_add", map[string]any{
		"user_id":        "42",
		"type":           "mtls",
		"credential_b64": cred,
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserAuthFactorList_Success(t *testing.T) {
	handler := handleUserAuthFactorListTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_list", map[string]any{"user_id": "42"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var m map[string]any
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &m); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
}

func TestCovHandleUserAuthFactorList_BadUserID(t *testing.T) {
	handler := handleUserAuthFactorListTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_list", map[string]any{"user_id": "abc"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad user_id")
	}
}

func TestCovHandleUserAuthFactorList_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.listFn = func(_ context.Context, _ uint64) ([]*auth.AuthFactor, error) {
		return nil, errors.New("list err")
	}
	handler := handleUserAuthFactorListTool(svc)
	req := buildCallToolRequest(t, "qrdb_user_authfactor_list", map[string]any{"user_id": "42"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserAuthFactorGet_Success(t *testing.T) {
	handler := handleUserAuthFactorGetTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_get", map[string]any{"factor_id": "f-001"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleUserAuthFactorGet_MissingID(t *testing.T) {
	handler := handleUserAuthFactorGetTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_get", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleUserAuthFactorGet_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.getFn = func(_ context.Context, _ string) (*auth.AuthFactor, error) {
		return nil, errors.New("not found")
	}
	handler := handleUserAuthFactorGetTool(svc)
	req := buildCallToolRequest(t, "qrdb_user_authfactor_get", map[string]any{"factor_id": "bad"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserAuthFactorRemove_Success(t *testing.T) {
	handler := handleUserAuthFactorRemoveTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_remove", map[string]any{
		"factor_id": "f-001", "force": true,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleUserAuthFactorRemove_MissingID(t *testing.T) {
	handler := handleUserAuthFactorRemoveTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_remove", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleUserAuthFactorRemove_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.removeFn = func(_ context.Context, _ string, _ bool) error { return errors.New("err") }
	handler := handleUserAuthFactorRemoveTool(svc)
	req := buildCallToolRequest(t, "qrdb_user_authfactor_remove", map[string]any{"factor_id": "f"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserAuthFactorRotate_Success(t *testing.T) {
	handler := handleUserAuthFactorRotateTool(newCovMockUserAuthFactorSvc())
	cred := base64.StdEncoding.EncodeToString([]byte("newcred"))
	req := buildCallToolRequest(t, "qrdb_user_authfactor_rotate", map[string]any{
		"old_factor_id":  "f-old",
		"type":           "mtls",
		"credential_b64": cred,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleUserAuthFactorRotate_MissingFields(t *testing.T) {
	handler := handleUserAuthFactorRotateTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_rotate", map[string]any{"old_factor_id": "f"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleUserAuthFactorRotate_BadBase64(t *testing.T) {
	handler := handleUserAuthFactorRotateTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_rotate", map[string]any{
		"old_factor_id":  "f",
		"type":           "mtls",
		"credential_b64": "!!!bad!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad base64")
	}
}

func TestCovHandleUserAuthFactorRotate_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.rotateFn = func(_ context.Context, _ string, _ *auth.AuthFactor) error { return errors.New("err") }
	handler := handleUserAuthFactorRotateTool(svc)
	cred := base64.StdEncoding.EncodeToString([]byte("cred"))
	req := buildCallToolRequest(t, "qrdb_user_authfactor_rotate", map[string]any{
		"old_factor_id":  "f",
		"type":           "mtls",
		"credential_b64": cred,
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserAuthFactorRelabel_Success(t *testing.T) {
	handler := handleUserAuthFactorRelabelTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{
		"factor_id": "f-001", "label": "new label",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleUserAuthFactorRelabel_MissingFields(t *testing.T) {
	handler := handleUserAuthFactorRelabelTool(newCovMockUserAuthFactorSvc())
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{"factor_id": "f"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleUserAuthFactorRelabel_RelabelError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.relabelFn = func(_ context.Context, _, _ string) error { return errors.New("relabel err") }
	handler := handleUserAuthFactorRelabelTool(svc)
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{
		"factor_id": "f", "label": "lbl",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserAuthFactorRelabel_GetError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.getFn = func(_ context.Context, _ string) (*auth.AuthFactor, error) {
		return nil, errors.New("get err")
	}
	handler := handleUserAuthFactorRelabelTool(svc)
	req := buildCallToolRequest(t, "qrdb_user_authfactor_relabel", map[string]any{
		"factor_id": "f", "label": "lbl",
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserAuthFactorLogin_Success(t *testing.T) {
	handler := handleUserAuthFactorLoginTool(newCovMockUserAuthFactorSvc(), nil)
	key := base64.StdEncoding.EncodeToString([]byte("lookupkey"))
	req := buildCallToolRequest(t, "qrdb_user_authfactor_login", map[string]any{
		"type": "mtls", "lookup_key_b64": key,
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var m map[string]any
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &m); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if m["user_id"] == nil {
		t.Error("expected user_id in response")
	}
}

func TestCovHandleUserAuthFactorLogin_MissingFields(t *testing.T) {
	handler := handleUserAuthFactorLoginTool(newCovMockUserAuthFactorSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_user_authfactor_login", map[string]any{"type": "mtls"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result")
	}
}

func TestCovHandleUserAuthFactorLogin_BadBase64(t *testing.T) {
	handler := handleUserAuthFactorLoginTool(newCovMockUserAuthFactorSvc(), nil)
	req := buildCallToolRequest(t, "qrdb_user_authfactor_login", map[string]any{
		"type": "mtls", "lookup_key_b64": "!!!bad!!!",
	})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad base64")
	}
}

func TestCovHandleUserAuthFactorLogin_ServiceError(t *testing.T) {
	svc := newCovMockUserAuthFactorSvc()
	svc.lookupFn = func(_ context.Context, _ auth.AuthFactorType, _ []byte) (*auth.AuthFactor, error) {
		return nil, errors.New("not found")
	}
	handler := handleUserAuthFactorLoginTool(svc, nil)
	key := base64.StdEncoding.EncodeToString([]byte("key"))
	req := buildCallToolRequest(t, "qrdb_user_authfactor_login", map[string]any{
		"type": "mtls", "lookup_key_b64": key,
	})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// factorToMCPMap nil guard
func TestCovFactorToMCPMap_Nil(t *testing.T) {
	m := factorToMCPMap(nil)
	if m != nil {
		t.Errorf("expected nil for nil input, got %v", m)
	}
}

// factorToMCPMap with LastUsedAt and RevokedAt set
func TestCovFactorToMCPMap_WithOptionalFields(t *testing.T) {
	now := time.Now()
	f := &auth.AuthFactor{
		ID:           "f-1",
		UserID:       42,
		Type:         auth.AuthFactorMTLS,
		Credential:   []byte("cred"),
		RegisteredAt: now,
		LastUsedAt:   &now,
		RevokedAt:    &now,
	}
	m := factorToMCPMap(f)
	if m == nil {
		t.Fatal("expected non-nil map")
	}
	if _, ok := m["last_used_at"]; !ok {
		t.Error("expected last_used_at in map")
	}
	if _, ok := m["revoked_at"]; !ok {
		t.Error("expected revoked_at in map")
	}
}

// parseUint64
func TestCovParseUint64_Valid(t *testing.T) {
	v, err := parseUint64("12345")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 12345 {
		t.Errorf("expected 12345, got %d", v)
	}
}

func TestCovParseUint64_Invalid(t *testing.T) {
	_, err := parseUint64("not-a-number")
	if err == nil {
		t.Fatal("expected error for invalid input")
	}
}

// ===========================================================================
// audit tools
// ===========================================================================

func TestCovAuditTools_Registration(t *testing.T) {
	logger := &covMockAuditLogger{}
	tools := auditTools(logger)
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Tool.Name != "qrdb_audit_query" {
		t.Errorf("expected qrdb_audit_query, got %s", tools[0].Tool.Name)
	}
}

func TestCovHandleAuditQuery_Success(t *testing.T) {
	logger := &covMockAuditLogger{}
	handler := handleAuditQuery(logger)
	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{
		"actor":    "alice",
		"resource": "barrier",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
	var page audit.Page
	if jerr := json.Unmarshal([]byte(resultText(t, result)), &page); jerr != nil {
		t.Fatalf("unmarshal: %v", jerr)
	}
	if page.Total != 1 {
		t.Errorf("expected total=1, got %d", page.Total)
	}
}

func TestCovHandleAuditQuery_WithTimeRange(t *testing.T) {
	logger := &covMockAuditLogger{}
	handler := handleAuditQuery(logger)
	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{
		"from": "2026-01-01T00:00:00Z",
		"to":   "2026-12-31T23:59:59Z",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleAuditQuery_WithLimit(t *testing.T) {
	logger := &covMockAuditLogger{}
	handler := handleAuditQuery(logger)
	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{"limit": "50"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleAuditQuery_BadFromTime(t *testing.T) {
	logger := &covMockAuditLogger{}
	handler := handleAuditQuery(logger)
	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{"from": "not-a-time"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad from time")
	}
}

func TestCovHandleAuditQuery_BadToTime(t *testing.T) {
	logger := &covMockAuditLogger{}
	handler := handleAuditQuery(logger)
	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{"to": "not-a-time"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad to time")
	}
}

func TestCovHandleAuditQuery_BadLimit(t *testing.T) {
	logger := &covMockAuditLogger{}
	handler := handleAuditQuery(logger)
	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{"limit": "not-a-number"})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for bad limit")
	}
}

func TestCovHandleAuditQuery_ServiceError(t *testing.T) {
	logger := &covMockAuditLogger{
		queryFn: func(_ context.Context, _ audit.Filter) (*audit.Page, error) {
			return nil, errors.New("query err")
		},
	}
	handler := handleAuditQuery(logger)
	req := buildCallToolRequest(t, "qrdb_audit_query", map[string]any{})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// handleUserGetByUsername / handleUserGetByCertThumbprint
// ===========================================================================

func TestCovHandleUserGetByUsername_Success(t *testing.T) {
	svc := &mockUserService{}
	handler := handleUserGetByUsername(svc)
	req := buildCallToolRequest(t, "qrdb_user_get_by_username", map[string]any{
		"username": "alice", "tenant_id": "system",
	})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}

func TestCovHandleUserGetByUsername_MissingUsername(t *testing.T) {
	handler := handleUserGetByUsername(&mockUserService{})
	req := buildCallToolRequest(t, "qrdb_user_get_by_username", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for missing username")
	}
}

func TestCovHandleUserGetByUsername_ServiceError(t *testing.T) {
	mock := &mockUserService{
		getUserByUsernameFn: func(_ context.Context, _, _ string) (*user.User, error) {
			return nil, errors.New("not found")
		},
	}
	handler := handleUserGetByUsername(mock)
	req := buildCallToolRequest(t, "qrdb_user_get_by_username", map[string]any{"username": "x"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ===========================================================================
// parseExactInt64 uncovered branches
// ===========================================================================

func TestCovParseExactInt64_Int64(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{"v": int64(42)})
	n, err := parseExactInt64(req, "v")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 42 {
		t.Errorf("expected 42, got %d", n)
	}
}

func TestCovParseExactInt64_Int(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{"v": int(99)})
	n, err := parseExactInt64(req, "v")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 99 {
		t.Errorf("expected 99, got %d", n)
	}
}

func TestCovParseExactInt64_Default(t *testing.T) {
	req := buildCallToolRequest(t, "test", map[string]any{"v": uint64(7)})
	n, err := parseExactInt64(req, "v")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 7 {
		t.Errorf("expected 7, got %d", n)
	}
}

func TestCovHandleUserGetByCertThumbprint_MissingThumbprint(t *testing.T) {
	handler := handleUserGetByCertThumbprint(&mockUserService{})
	req := buildCallToolRequest(t, "qrdb_user_get_by_cert_thumbprint", map[string]any{})
	result, err := handler(context.Background(), req)
	if err != nil || !result.IsError {
		t.Errorf("expected error result for missing thumbprint")
	}
}

func TestCovHandleUserGetByCertThumbprint_ServiceError(t *testing.T) {
	mock := &mockUserService{
		getUserByCertThumbprintFn: func(_ context.Context, _ string) (*user.User, error) {
			return nil, errors.New("not found")
		},
	}
	handler := handleUserGetByCertThumbprint(mock)
	req := buildCallToolRequest(t, "qrdb_user_get_by_cert_thumbprint", map[string]any{"thumbprint": "bad"})
	_, err := handler(context.Background(), req)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestCovHandleUserGetByCertThumbprint_Success(t *testing.T) {
	mock := &mockUserService{
		getUserByCertThumbprintFn: func(_ context.Context, _ string) (*user.User, error) {
			return &user.User{ID: 1, Username: "alice", Enabled: true}, nil
		},
	}
	handler := handleUserGetByCertThumbprint(mock)
	req := buildCallToolRequest(t, "qrdb_user_get_by_cert_thumbprint", map[string]any{"thumbprint": "sha256:abc"})
	result, err := handler(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result")
	}
}
