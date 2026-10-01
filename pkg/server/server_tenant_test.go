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

package server

import (
	"context"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/dek"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// mockTenantServicer is a minimal TenantServicer for option tests.
type mockTenantServicer struct{}

func (m *mockTenantServicer) CreateTenant(_ context.Context, _, _ string, _ tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: "test", Name: "test", IsolationType: tenant.IsolationBarrier, CreatedAt: time.Now(), UpdatedAt: time.Now()}, nil
}

func (m *mockTenantServicer) UpdateTenant(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantServicer) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantServicer) ListTenants(_ context.Context) ([]tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantServicer) DeleteTenant(_ context.Context, _ string) error { return nil }
func (m *mockTenantServicer) TenantBarrierInit(_ context.Context, _ string, _ []byte, _, _ int) (*tenant.TenantBarrierInitResult, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierInitWithStrategy(_ context.Context, _ string, _ []byte, _, _ int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierUnsealShare(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return nil, nil
}
func (m *mockTenantServicer) TenantBarrierUnseal(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (m *mockTenantServicer) UpdateQuotas(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (m *mockTenantServicer) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}
func (m *mockTenantServicer) Quotas(_ context.Context, _ string) (*quota.Quotas, error) {
	return nil, nil
}
func (m *mockTenantServicer) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantServicer) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (m *mockTenantServicer) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

func (m *mockTenantServicer) TenantBarrierOfficerRegister(_ context.Context, _ string, _ *ceremony.RegisterOfficerRequest) (*ceremony.RegisterOfficerResult, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierOfficerList(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierShareClaimBegin(_ context.Context, _, _, _ string) (*ceremony.ChallengeResponse, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierShareClaimComplete(_ context.Context, _, _, _ string, _ []byte, _ string) (*ceremony.ShareClaimResult, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierOfficerClaimStatus(_ context.Context, _ string) ([]*ceremony.OfficerStatus, error) {
	return nil, nil
}

func (m *mockTenantServicer) TenantBarrierShamirSealVote(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return nil, nil
}

// Compile-time interface check.
var _ tenant.TenantServicer = (*mockTenantServicer)(nil)

// TestWithTenantService verifies that WithTenantService correctly sets
// the tenantSvc field on serverOptions and that the nil case leaves the
// field unset.
func TestWithTenantService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		svc     tenant.TenantServicer
		wantNil bool
	}{
		{
			name:    "non-nil service is stored",
			svc:     &mockTenantServicer{},
			wantNil: false,
		},
		{
			name:    "nil service is stored as-is",
			svc:     nil,
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var opts serverOptions
			WithTenantService(tc.svc)(&opts)

			if tc.wantNil && opts.tenantSvc != nil {
				t.Errorf("tenantSvc: got non-nil, want nil")
			}
			if !tc.wantNil && opts.tenantSvc == nil {
				t.Errorf("tenantSvc: got nil, want non-nil")
			}
			if !tc.wantNil && opts.tenantSvc != tc.svc {
				t.Errorf("tenantSvc: got %v, want %v", opts.tenantSvc, tc.svc)
			}
		})
	}
}

// TestWithBarrierRegistry verifies that WithBarrierRegistry correctly
// sets the barrierRegistry field on serverOptions. The nil case is also
// exercised to ensure the option setter is safe with nil input.
func TestWithBarrierRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantNil bool
	}{
		{
			name:    "nil registry is stored as-is",
			wantNil: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var opts serverOptions
			WithBarrierRegistry(nil)(&opts)

			if tc.wantNil && opts.barrierRegistry != nil {
				t.Errorf("barrierRegistry: got non-nil, want nil")
			}
		})
	}
}

// TestTenantOptionsCombined verifies that multiple tenant-related options can
// be applied together to the same serverOptions without interference.
func TestTenantOptionsCombined(t *testing.T) {
	t.Parallel()

	dekSvc := newTestDEKService(t)
	svc := &mockTenantServicer{}

	var opts serverOptions
	WithDEKService(dekSvc)(&opts)
	WithTenantService(svc)(&opts)
	WithBarrierRegistry(nil)(&opts)

	if opts.dekSvc == nil {
		t.Error("dekSvc: got nil after WithDEKService")
	}
	if opts.tenantSvc == nil {
		t.Error("tenantSvc: got nil after WithTenantService")
	}
	if opts.barrierRegistry != nil {
		t.Error("barrierRegistry: got non-nil after WithBarrierRegistry(nil)")
	}
}

// TestWithDEKService_ServerIntegration verifies that the keyring service
// option survives server.New, which is what threads it to every protocol
// server.
func TestWithDEKService_ServerIntegration(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, false, false)
	dekSvc := newTestDEKService(t)

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithDEKService(dekSvc),
	)

	if srv.opts.dekSvc != dekSvc {
		t.Fatalf("dekSvc: got %v, want the injected service", srv.opts.dekSvc)
	}
}

// newTestDEKService is the production keyring service over in-memory stores.
func newTestDEKService(t *testing.T) service.DEKServicer {
	t.Helper()
	keyring, err := dek.NewKeyring(memTenantStores{})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.NewDEKService(keyring)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// memTenantStores resolves one in-memory store per tenant.
type memTenantStores map[string]*kvstore.MemoryKVStore

func (m memTenantStores) Resolve(_ context.Context, tenantID string) (kvstore.KVStore, error) {
	s, ok := m[tenantID]
	if !ok {
		s = kvstore.NewMemoryKVStore()
		m[tenantID] = s
	}
	return s, nil
}

// TestWithTenantService_ServerIntegration verifies that the option is
// correctly applied when passed to server.New.
func TestWithTenantService_ServerIntegration(t *testing.T) {
	t.Parallel()

	cfg := testConfig(t, false, false)
	svc := &mockTenantServicer{}

	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTenantService(svc),
	)

	if srv.opts.tenantSvc == nil {
		t.Fatal("tenantSvc: got nil, want non-nil after New()")
	}
	if srv.opts.tenantSvc != svc {
		t.Errorf("tenantSvc: got %v, want %v", srv.opts.tenantSvc, svc)
	}
}

func (m *mockTenantServicer) CheckBarrierMode(_ context.Context, _ string, _ seal.StrategyID) error {
	return nil
}
