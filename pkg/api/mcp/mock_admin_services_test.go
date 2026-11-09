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

// This file holds the barrier, tenant, auth-factor and user-auth-factor
// service doubles and the allow-all authorization helpers shared by the
// package's handler tests.

package mcp

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	authmw "github.com/jeremyhahn/go-qrdb/pkg/auth/middleware"
	"github.com/jeremyhahn/go-qrdb/pkg/authfactors"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterseal"
	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Mocks: barrierServicer (lightweight, no build-tag conflict)
// ---------------------------------------------------------------------------

type covMockBarrierSvc struct {
	service.BarrierServicer
	initializeFn           func(ctx context.Context, secret []byte) error
	unsealFn               func(ctx context.Context, secret []byte) error
	sealFn                 func(ctx context.Context) error
	statusFn               func(ctx context.Context) (*service.BarrierStatusInfo, error)
	initializeShamirFn     func(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error)
	generateRecoveryKeysFn func(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error)
	deleteRecoveryKeysFn   func(ctx context.Context) error
	hasRecoveryKeysFn      func(ctx context.Context) (bool, error)
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
func (m *covMockBarrierSvc) BarrierInitializeShamir(ctx context.Context, threshold, totalShares int, policy seal.FactorPolicy) (*service.ShamirInitInfo, error) {
	if m.initializeShamirFn != nil {
		return m.initializeShamirFn(ctx, threshold, totalShares, policy)
	}
	return &service.ShamirInitInfo{Threshold: threshold, TotalShares: totalShares, ShareCount: totalShares}, nil
}
func (m *covMockBarrierSvc) BarrierGenerateRecoveryKeys(ctx context.Context, threshold int, policy seal.FactorPolicy) (*service.RecoveryKeysInfo, error) {
	if m.generateRecoveryKeysFn != nil {
		return m.generateRecoveryKeysFn(ctx, threshold, policy)
	}
	return &service.RecoveryKeysInfo{Threshold: threshold, Total: threshold + 1, ShareCount: threshold + 1}, nil
}
func (m *covMockBarrierSvc) BarrierDeleteRecoveryKeys(ctx context.Context) error {
	if m.deleteRecoveryKeysFn != nil {
		return m.deleteRecoveryKeysFn(ctx)
	}
	return nil
}

// BarrierInitializePrecondition admits every initialize: the double's
// initialize paths answer for themselves through their canned results.
func (m *covMockBarrierSvc) BarrierInitializePrecondition(_ context.Context) error { return nil }

// BarrierClusterUnseal answers as a node without cluster sealing does.
func (m *covMockBarrierSvc) BarrierClusterUnseal(context.Context) (*clusterseal.Report, error) {
	return nil, clusterseal.ErrInactive
}

func (m *covMockBarrierSvc) BarrierHasRecoveryKeys(ctx context.Context) (bool, error) {
	if m.hasRecoveryKeysFn != nil {
		return m.hasRecoveryKeysFn(ctx)
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Mock: TenantServicer
// ---------------------------------------------------------------------------

type covMockTenantSvc struct {
	tenant.TenantServicer
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

// allowAllTenantInit is the init-gate sibling of allowAllTenantScope: it
// permits every tenant barrier initialization, proof or none, isolating
// handler-behavior tests from the trust-on-first-init authorization rule.
func allowAllTenantInit(_ context.Context, _ string, _ *authmw.TenantBarrierInitProof) error {
	return nil
}

// allowAllTenantInitBegin is init/begin's sibling of allowAllTenantInit: it
// admits every caller and asks no proof, as an unauthenticated node does.
func allowAllTenantInitBegin(_ context.Context, _ string) (*initproof.Challenge, error) {
	return &initproof.Challenge{}, nil
}

// allowAllTenantBarrierAuthz builds a tenantBarrierAuthorizers whose gates
// all permit every invocation, for registration/coverage tests that exercise
// handler behavior rather than authorization.
func allowAllTenantBarrierAuthz() tenantBarrierAuthorizers {
	return tenantBarrierAuthorizers{
		Op:        allowAllTenantScope,
		InitBegin: allowAllTenantInitBegin,
		Init:      allowAllTenantInit,
		Sealed:    allowAllTenantScope,
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
func (m *covMockTenantSvc) ListDeletedTenants(_ context.Context, _ string, _ int) (*tenant.DeletedTenantsPage, error) {
	return &tenant.DeletedTenantsPage{}, nil
}

func (m *covMockTenantSvc) ReconcileDeletedTenants(_ context.Context) (*tenant.ReconcileReport, error) {
	return &tenant.ReconcileReport{}, nil
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

// UndoCreateTenant undoes a create the way this double deletes one.
func (m *covMockTenantSvc) UndoCreateTenant(ctx context.Context, tenantID string) error {
	return m.DeleteTenant(ctx, tenantID)
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
func (m *covMockTenantSvc) TenantBarrierInitWithStrategy(ctx context.Context, id string, passphrase []byte, threshold, total int, _ seal.StrategyID, _ *tenant.TenantBarrierShamirRoster, _ ...tenant.TenantBarrierInitOption) (*tenant.TenantBarrierInitResult, error) {
	return m.TenantBarrierInit(ctx, id, passphrase, threshold, total)
}

func (m *covMockTenantSvc) TenantBarrierInitBegin(context.Context, *tenant.TenantBarrierInitBeginRequest) (*tenant.TenantBarrierInitBeginResult, error) {
	return nil, tenant.ErrBarrierNotEnabled
}

func (m *covMockTenantSvc) TenantBarrierInitComplete(context.Context, *tenant.TenantBarrierInitCompleteRequest) (*tenant.TenantBarrierInitResult, error) {
	return nil, tenant.ErrBarrierNotEnabled
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
	deleteFn   func(ctx context.Context, factorID string, force bool) error
	rotateFn   func(ctx context.Context, oldID string, newFactor *auth.AuthFactor) error
	relabelFn  func(ctx context.Context, factorID, label string) error
	markUsedFn func(ctx context.Context, factorID string, ts time.Time) error
	condFn     func(ctx context.Context, userID uint64, factor *auth.AuthFactor, cond auth.FactorAddCondition) (uint64, error)
	versionFn  func(ctx context.Context, userID uint64) (uint64, error)
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
func (m *covMockUserAuthFactorSvc) Delete(ctx context.Context, factorID string, force bool) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, factorID, force)
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

func (m *covMockUserAuthFactorSvc) AddConditional(ctx context.Context, userID uint64, factor *auth.AuthFactor, cond auth.FactorAddCondition) (uint64, error) {
	if m.condFn != nil {
		return m.condFn(ctx, userID, factor, cond)
	}
	factor.ID = "f-001"
	factor.RegisteredAt = time.Now()
	return 1, nil
}

func (m *covMockUserAuthFactorSvc) FactorSetVersion(ctx context.Context, userID uint64) (uint64, error) {
	if m.versionFn != nil {
		return m.versionFn(ctx, userID)
	}
	return 0, nil
}

// ---------------------------------------------------------------------------
// Mock: AuditLogger
// ---------------------------------------------------------------------------
