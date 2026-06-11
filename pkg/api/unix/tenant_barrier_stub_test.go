// Copyright 2026 Jeremy Hahn
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

package unix

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// stubTenantBarrierSvc satisfies tenant.TenantServicer for option-propagation
// and Start-cycle tests. The metadata methods are inert because the option
// tests only verify wiring; the four barrier methods are inspectable so
// downstream tests can assert they were called with the expected arguments.
type stubTenantBarrierSvc struct {
	initCalled    bool
	unsealCalled  bool
	sealCalled    bool
	statusCalled  bool
	lastInitID    string
	lastInitT     int
	lastInitS     int
	lastUnsealID  string
	lastUnsealKey []byte
	lastSealID    string
	lastStatusID  string
	statusReturn  *tenant.TenantBarrierStatus
	initErr       error
	unsealErr     error
	sealErr       error
	statusErr     error
}

// CreateTenant returns nil, nil; the option tests do not exercise it.
func (s *stubTenantBarrierSvc) CreateTenant(_ context.Context, _, _ string, _ tenant.IsolationType) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (s *stubTenantBarrierSvc) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (s *stubTenantBarrierSvc) ListTenants(_ context.Context) ([]tenant.TenantInfo, error) {
	return nil, nil
}

func (s *stubTenantBarrierSvc) DeleteTenant(_ context.Context, _ string) error { return nil }

func (s *stubTenantBarrierSvc) UpdateTenant(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (s *stubTenantBarrierSvc) UpdateQuotas(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
	return nil, nil
}

// TenantBarrierInit records arguments and returns the configured error.
func (s *stubTenantBarrierSvc) TenantBarrierInit(_ context.Context, tenantID, _ string, threshold, shares int) (*tenant.TenantBarrierInitResult, error) {
	s.initCalled = true
	s.lastInitID = tenantID
	s.lastInitT = threshold
	s.lastInitS = shares
	return nil, s.initErr
}

// TenantBarrierInitWithStrategy delegates to TenantBarrierInit for test simplicity.
func (s *stubTenantBarrierSvc) TenantBarrierInitWithStrategy(ctx context.Context, tenantID, passphrase string, threshold, shares int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return s.TenantBarrierInit(ctx, tenantID, passphrase, threshold, shares)
}

// TenantBarrierUnsealShare is a no-op stub.
func (s *stubTenantBarrierSvc) TenantBarrierUnsealShare(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return nil, nil
}

// TenantBarrierUnseal records arguments and returns the configured error.
func (s *stubTenantBarrierSvc) TenantBarrierUnseal(_ context.Context, tenantID string, key []byte) error {
	s.unsealCalled = true
	s.lastUnsealID = tenantID
	s.lastUnsealKey = key
	return s.unsealErr
}

// TenantBarrierSeal records the tenant ID and returns the configured error.
func (s *stubTenantBarrierSvc) TenantBarrierSeal(_ context.Context, tenantID string) error {
	s.sealCalled = true
	s.lastSealID = tenantID
	return s.sealErr
}

// TenantBarrierStatus records the tenant ID and returns the configured value.
func (s *stubTenantBarrierSvc) TenantBarrierStatus(_ context.Context, tenantID string) (*tenant.TenantBarrierStatus, error) {
	s.statusCalled = true
	s.lastStatusID = tenantID
	if s.statusErr != nil {
		return nil, s.statusErr
	}
	if s.statusReturn != nil {
		return s.statusReturn, nil
	}
	return &tenant.TenantBarrierStatus{
		TenantID:    tenantID,
		Sealed:      false,
		Initialized: true,
		Strategy:    "passphrase",
	}, nil
}

// IncrementQuotaUsage is a no-op stub.
func (s *stubTenantBarrierSvc) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}

// DecrementQuotaUsage is a no-op stub.
func (s *stubTenantBarrierSvc) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}

// GetQuotaUsage is a no-op stub.
func (s *stubTenantBarrierSvc) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

// TenantBarrierOfficerRegister is a no-op stub.
func (s *stubTenantBarrierSvc) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return &ceremony.RegisterOfficerResult{}, nil
}

// TenantBarrierOfficerList is a no-op stub.
func (s *stubTenantBarrierSvc) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}

// TenantBarrierShareClaimBegin is a no-op stub.
func (s *stubTenantBarrierSvc) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return &ceremony.ChallengeResponse{}, nil
}

// TenantBarrierShareClaimComplete is a no-op stub.
func (s *stubTenantBarrierSvc) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return &ceremony.ShareClaimResult{}, nil
}

// TenantBarrierOfficerClaimStatus is a no-op stub.
func (s *stubTenantBarrierSvc) TenantBarrierOfficerClaimStatus(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}

// TenantBarrierShamirSealVote is a no-op stub.
func (s *stubTenantBarrierSvc) TenantBarrierShamirSealVote(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return &tenant.TenantBarrierUnsealProgress{}, nil
}

// Compile-time interface check.
var _ tenant.TenantServicer = (*stubTenantBarrierSvc)(nil)
