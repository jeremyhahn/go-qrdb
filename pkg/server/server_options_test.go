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

//go:build codec_json && raft && qrdb_ts && qrdb_vector && qrdb_mysql && qrdb_pgsql && qrdb_etcd

package server

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/custodian"
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Stubs for zero-covered server option tests
// ---------------------------------------------------------------------------

// stubLockService satisfies service.LockServicer via embedding.
type stubLockService struct{ service.LockServicer }

// stubAuditLogger satisfies audit.AuditLogger.
type stubAuditLogger struct{}

func (s *stubAuditLogger) Emit(_ context.Context, _ audit.AuditEvent) {}
func (s *stubAuditLogger) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return nil, nil
}
func (s *stubAuditLogger) Close() error { return nil }

var _ audit.AuditLogger = (*stubAuditLogger)(nil)

// stubAuthFactorService satisfies service.AuthFactorServicer via embedding.
type stubAuthFactorService struct{ service.AuthFactorServicer }

// stubCustodianServicer satisfies custodian.Servicer via embedding.
type stubCustodianServicer struct{ custodian.Servicer }

// stubLockImpl implements service.LockServicer with real method stubs
// so the embedding type assertion passes.
type stubLockImpl struct{}

func (s *stubLockImpl) Lock(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
	return nil, nil
}
func (s *stubLockImpl) TryLock(_ context.Context, _, _ string, _ time.Duration, _ bool) (*lock.Lock, error) {
	return nil, nil
}
func (s *stubLockImpl) Unlock(_ context.Context, _ string, _ int64) error { return nil }
func (s *stubLockImpl) Extend(_ context.Context, _ string, _ int64) (*lock.Lock, error) {
	return nil, nil
}
func (s *stubLockImpl) Status(_ context.Context, _ string) (*lock.Lock, error) { return nil, nil }
func (s *stubLockImpl) List(_ context.Context, _ string) ([]*lock.Lock, error) { return nil, nil }

var _ service.LockServicer = (*stubLockImpl)(nil)

// stubWebUI satisfies http.Handler.
type stubWebUI struct{}

func (s *stubWebUI) ServeHTTP(_ http.ResponseWriter, _ *http.Request) {}

var _ http.Handler = (*stubWebUI)(nil)

// stubTenantServicerForRegistry satisfies tenant.TenantServicer.
type stubTenantServicerForRegistry struct{}

func (s *stubTenantServicerForRegistry) CreateTenant(_ context.Context, _, _ string, _ tenant.IsolationType, _ ...tenant.CreateTenantOption) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) GetTenant(_ context.Context, _ string) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) ListTenants(_ context.Context) ([]tenant.TenantInfo, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) DeleteTenant(_ context.Context, _ string) error { return nil }
func (s *stubTenantServicerForRegistry) UpdateTenant(_ context.Context, _ string, _ tenant.TenantPatch) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) TenantBarrierInit(_ context.Context, _ string, _ []byte, _, _ int) (*tenant.TenantBarrierInitResult, error) {
	return nil, nil
}

func (s *stubTenantServicerForRegistry) TenantBarrierInitWithStrategy(_ context.Context, _ string, _ []byte, _, _ int, _ seal.StrategyID) (*tenant.TenantBarrierInitResult, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) TenantBarrierUnsealShare(_ context.Context, _, _ string) (*tenant.TenantBarrierUnsealProgress, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) TenantBarrierUnseal(_ context.Context, _ string, _ []byte) error {
	return nil
}
func (s *stubTenantServicerForRegistry) TenantBarrierSeal(_ context.Context, _ string) error {
	return nil
}
func (s *stubTenantServicerForRegistry) TenantBarrierStatus(_ context.Context, _ string) (*tenant.TenantBarrierStatus, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) UpdateQuotas(_ context.Context, _ string, _ *quota.Quotas) (*tenant.TenantInfo, error) {
	return nil, nil
}

func (s *stubTenantServicerForRegistry) UpdateEntitlements(ctx context.Context, id string, e *quota.Entitlements) (*tenant.TenantInfo, error) {
	return &tenant.TenantInfo{ID: id, Entitlements: e}, nil
}
func (s *stubTenantServicerForRegistry) Quotas(_ context.Context, _ string) (*quota.Quotas, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) IncrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) DecrementQuotaUsage(_ context.Context, _ string, _ quota.Resource, _ int64) (*tenant.TenantInfo, error) {
	return nil, nil
}
func (s *stubTenantServicerForRegistry) GetQuotaUsage(_ context.Context, _ string, _ quota.Resource) (int64, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// WithLockService
// ---------------------------------------------------------------------------

// TestWithLockService verifies that WithLockService stores the provided
// LockServicer on the server options struct.
func TestWithLockService(t *testing.T) {
	t.Parallel()
	svc := &stubLockImpl{}
	var opts serverOptions
	WithLockService(svc)(&opts)
	if opts.lockSvc != svc {
		t.Error("WithLockService did not store the provided service")
	}
}

// ---------------------------------------------------------------------------
// WithKVService (option-based override)
// ---------------------------------------------------------------------------

// TestWithKVService_OptionOverridesPositionalParam verifies that when
// WithKVService is passed as an option, the server's kvSvc field is updated
// to the option value rather than the positional nil.
func TestWithKVService_OptionOverridesPositionalParam(t *testing.T) {
	t.Parallel()
	kvSvc := newMockKVService()
	cfg := testConfig(t, false, false)

	srv := New(cfg, nil, &mockAdminService{}, WithKVService(kvSvc))
	if srv.kvSvc != kvSvc {
		t.Error("WithKVService: kvSvc should be the option value")
	}
}

// TestWithKVService_StoresOnOpts verifies the option closure sets opts.kvSvc.
func TestWithKVService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	svc := newMockKVService()
	var opts serverOptions
	WithKVService(svc)(&opts)
	if opts.kvSvc != svc {
		t.Error("WithKVService did not store the provided KVServicer on opts")
	}
}

// ---------------------------------------------------------------------------
// WithAdminService (option-based override)
// ---------------------------------------------------------------------------

// TestWithAdminService_OptionOverridesPositionalParam verifies that when
// WithAdminService is passed as an option, the server's adminSvc field is updated.
func TestWithAdminService_OptionOverridesPositionalParam(t *testing.T) {
	t.Parallel()
	adminSvc := &mockAdminService{}
	cfg := testConfig(t, false, false)

	srv := New(cfg, newMockKVService(), nil, WithAdminService(adminSvc))
	if srv.adminSvc != adminSvc {
		t.Error("WithAdminService: adminSvc should be the option value")
	}
}

// TestWithAdminService_StoresOnOpts verifies the option closure sets opts.adminSvc.
func TestWithAdminService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	svc := &mockAdminService{}
	var opts serverOptions
	WithAdminService(svc)(&opts)
	if opts.adminSvc != svc {
		t.Error("WithAdminService did not store the provided AdminServicer on opts")
	}
}

// ---------------------------------------------------------------------------
// WithWebUI
// ---------------------------------------------------------------------------

// TestWithWebUI verifies that WithWebUI stores the provided http.Handler.
func TestWithWebUI(t *testing.T) {
	t.Parallel()
	ui := &stubWebUI{}
	var opts serverOptions
	WithWebUI(ui)(&opts)
	if opts.webUI != ui {
		t.Error("WithWebUI did not store the provided http.Handler")
	}
}

// TestWithWebUI_NilHandlerAllowed verifies that a nil WebUI handler is
// stored as-is (callers check for nil before registering the route).
func TestWithWebUI_NilHandlerAllowed(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithWebUI(nil)(&opts)
	if opts.webUI != nil {
		t.Errorf("expected nil webUI, got %v", opts.webUI)
	}
}

// ---------------------------------------------------------------------------
// WithAuditLogger
// ---------------------------------------------------------------------------

// TestWithAuditLogger verifies that WithAuditLogger stores the provided logger.
func TestWithAuditLogger(t *testing.T) {
	t.Parallel()
	logger := &stubAuditLogger{}
	var opts serverOptions
	WithAuditLogger(logger)(&opts)
	if opts.auditLogger != logger {
		t.Error("WithAuditLogger did not store the provided AuditLogger")
	}
}

// TestWithAuditLogger_ViaNewServer verifies the option propagates through New().
func TestWithAuditLogger_ViaNewServer(t *testing.T) {
	logger := &stubAuditLogger{}
	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithAuditLogger(logger))
	if srv.opts.auditLogger != logger {
		t.Error("WithAuditLogger not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// WithAuthFactorService
// ---------------------------------------------------------------------------

// TestWithAuthFactorService verifies that WithAuthFactorService stores the
// provided AuthFactorServicer on serverOptions.
func TestWithAuthFactorService(t *testing.T) {
	t.Parallel()
	svc := &stubAuthFactorService{}
	var opts serverOptions
	WithAuthFactorService(svc)(&opts)
	if opts.authFactorSvc != svc {
		t.Error("WithAuthFactorService did not store the provided service")
	}
}

// TestWithAuthFactorService_ViaNewServer verifies the option propagates through New().
func TestWithAuthFactorService_ViaNewServer(t *testing.T) {
	svc := &stubAuthFactorService{}
	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithAuthFactorService(svc))
	if srv.opts.authFactorSvc != svc {
		t.Error("WithAuthFactorService not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// WithCustodianService
// ---------------------------------------------------------------------------

// TestWithCustodianService verifies that WithCustodianService stores the
// provided custodian.Servicer on serverOptions.
func TestWithCustodianService(t *testing.T) {
	t.Parallel()
	svc := &stubCustodianServicer{}
	var opts serverOptions
	WithCustodianService(svc)(&opts)
	if opts.custodianSvc != svc {
		t.Error("WithCustodianService did not store the provided service")
	}
}

// TestWithCustodianService_ViaNewServer verifies the option propagates through New().
func TestWithCustodianService_ViaNewServer(t *testing.T) {
	svc := &stubCustodianServicer{}
	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCustodianService(svc))
	if srv.opts.custodianSvc != svc {
		t.Error("WithCustodianService not propagated via New()")
	}
}

// ---------------------------------------------------------------------------
// WithSOAuthenticator
// ---------------------------------------------------------------------------

// TestWithSOAuthenticator verifies that WithSOAuthenticator stores the
// provided SOAuthenticator pointer on serverOptions. A nil pointer is valid
// (callers check for nil before using the authenticator).
func TestWithSOAuthenticator_NilIsAccepted(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithSOAuthenticator(nil)(&opts)
	if opts.soAuthenticator != nil {
		t.Errorf("expected nil soAuthenticator, got %v", opts.soAuthenticator)
	}
}

// TestWithSOAuthenticator_ViaNewServer verifies nil SOAuthenticator propagates
// cleanly through New() without panicking.
func TestWithSOAuthenticator_ViaNewServer(t *testing.T) {
	cfg := testConfig(t, false, false)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithSOAuthenticator(nil))
	if srv.opts.soAuthenticator != nil {
		t.Error("expected nil soAuthenticator after nil WithSOAuthenticator")
	}
}

// ---------------------------------------------------------------------------
// WithTenantServicer (RESTRegistryOption)
// ---------------------------------------------------------------------------

// TestWithTenantServicer verifies that the RESTRegistryOption stores the
// tenant service on the restServiceRegistry.
func TestWithTenantServicer_StoresService(t *testing.T) {
	t.Parallel()
	svc := &mockTenantServicer{}
	reg := &restServiceRegistry{}
	WithTenantServicer(svc)(reg)
	if reg.tenantSvc != svc {
		t.Error("WithTenantServicer did not store the provided TenantServicer")
	}
}

// TestWithTenantServicer_NilIsAccepted verifies that a nil TenantServicer
// is accepted (restServiceRegistry.resolveTenant skips the lookup when nil).
func TestWithTenantServicer_NilIsAccepted(t *testing.T) {
	t.Parallel()
	reg := &restServiceRegistry{}
	WithTenantServicer(nil)(reg)
	if reg.tenantSvc != nil {
		t.Errorf("expected nil tenantSvc, got %v", reg.tenantSvc)
	}
}
