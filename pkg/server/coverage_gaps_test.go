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
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// ---------------------------------------------------------------------------
// RESTRegistryOption setters — withTenantHostRegistry / withRaftBackendFactory
// ---------------------------------------------------------------------------

// TestWithTenantHostRegistry_RESTOption verifies that withTenantHostRegistry
// stores the registry on the restServiceRegistry.
func TestWithTenantHostRegistry_RESTOption(t *testing.T) {
	t.Parallel()
	r := &restServiceRegistry{}
	withTenantHostRegistry(nil)(r)
	if r.tenantHosts != nil {
		t.Error("expected nil tenantHosts after withTenantHostRegistry(nil)")
	}
}

// TestWithTenantHostRegistry_RESTOption_NonNil verifies non-nil value is stored.
func TestWithTenantHostRegistry_RESTOption_NonNil(t *testing.T) {
	t.Parallel()
	reg := &TenantHostRegistry{}
	r := &restServiceRegistry{}
	withTenantHostRegistry(reg)(r)
	if r.tenantHosts != reg {
		t.Error("withTenantHostRegistry did not store provided registry")
	}
}

// TestWithRaftBackendFactory_RESTOption verifies that withRaftBackendFactory
// stores the factory on the restServiceRegistry.
func TestWithRaftBackendFactory_RESTOption(t *testing.T) {
	t.Parallel()
	r := &restServiceRegistry{}
	withRaftBackendFactory(nil)(r)
	if r.raftBackend != nil {
		t.Error("expected nil raftBackend after withRaftBackendFactory(nil)")
	}
}

// TestWithRaftBackendFactory_RESTOption_NonNil verifies a non-nil factory is stored.
func TestWithRaftBackendFactory_RESTOption_NonNil(t *testing.T) {
	t.Parallel()
	f := &RaftBackendFactory{}
	r := &restServiceRegistry{}
	withRaftBackendFactory(f)(r)
	if r.raftBackend != f {
		t.Error("withRaftBackendFactory did not store provided factory")
	}
}

// ---------------------------------------------------------------------------
// TenantHostRegistry — NewTenantHostRegistryFromServicer, Attach, Close
// ---------------------------------------------------------------------------

// TestNewTenantHostRegistryFromServicer verifies that the factory wraps the
// servicer correctly and returns a non-nil registry.
func TestNewTenantHostRegistryFromServicer(t *testing.T) {
	t.Parallel()
	svc := &mockTenantServicer{}
	reg := NewTenantHostRegistryFromServicer(nil, svc, nil)
	if reg == nil {
		t.Fatal("expected non-nil TenantHostRegistry")
	}
}

// TestNewTenantHostRegistryFromServicer_NilLogger verifies nil logger is accepted
// (slog.Default() is used as fallback).
func TestNewTenantHostRegistryFromServicer_NilLogger(t *testing.T) {
	t.Parallel()
	svc := &mockTenantServicer{}
	reg := NewTenantHostRegistryFromServicer(nil, svc, nil)
	if reg == nil {
		t.Fatal("expected non-nil TenantHostRegistry with nil logger")
	}
}

// TestTenantHostRegistry_Attach_NilRegistry verifies Attach returns an error
// when the registry itself is nil (both barrierReg and tenants nil).
func TestTenantHostRegistry_Attach_NilRegistry(t *testing.T) {
	t.Parallel()
	// A registry with nil barrierReg returns an error from Services, which
	// Attach propagates directly.
	reg := &TenantHostRegistry{hosts: make(map[string]*tenantHost)}
	ctx := context.Background()
	if err := reg.Attach(ctx, "t1"); err == nil {
		t.Fatal("expected error from Attach on registry with nil barrierReg")
	}
}

// TestTenantHostRegistry_Attach_ReturnsError propagates Services error.
func TestTenantHostRegistry_Attach_ReturnsError(t *testing.T) {
	t.Parallel()
	// A registry with nil barrierReg/tenants returns TenantHostError.
	reg := NewTenantHostRegistry(nil, nil, nil)
	ctx := context.Background()
	err := reg.Attach(ctx, "missing-tenant")
	if err == nil {
		t.Fatal("expected error from Attach when registry has no barrier/tenants")
	}
}

// TestTenantHostRegistry_Close_AbsentTenant verifies that Close is a no-op
// for a tenant that was never attached.
func TestTenantHostRegistry_Close_AbsentTenant(t *testing.T) {
	t.Parallel()
	reg := NewTenantHostRegistry(nil, nil, nil)
	if err := reg.Close("nonexistent"); err != nil {
		t.Errorf("Close of absent tenant: %v", err)
	}
}

// TestTenantHostRegistry_Close_AbsentTenantEmpty verifies Close on an empty
// registry returns nil.
func TestTenantHostRegistry_Close_AbsentTenantEmpty(t *testing.T) {
	t.Parallel()
	reg := &TenantHostRegistry{hosts: make(map[string]*tenantHost)}
	if err := reg.Close("nope"); err != nil {
		t.Errorf("Close on empty registry: %v", err)
	}
}

// ---------------------------------------------------------------------------
// checkTenantBarrier — remaining branches (TenantNotFound, BarrierSealed)
// ---------------------------------------------------------------------------

// TestCheckTenantBarrier_TenantNotFound verifies that a tenant not registered
// in the barrier registry yields a TenantNotFoundError.
func TestCheckTenantBarrier_TenantNotFound(t *testing.T) {
	t.Parallel()
	system := seal.NewMemoryBackend()
	strategy, err := seal.NewSoftwareStrategy([]byte("check-barrier-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	sysBarrier, err := seal.NewStorageBarrier(nil, system,
		seal.BarrierConfig{RootKeyPath: "sys/root"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	reg, err := seal.NewBarrierRegistry(nil, sysBarrier)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}

	r := &restServiceRegistry{
		barrierReg:   reg,
		systemTenant: "system",
	}

	// "ghost-tenant" was never registered — GetTenant returns ErrTenantNotFound.
	err = r.checkTenantBarrier("ghost-tenant")
	if err == nil {
		t.Fatal("expected TenantNotFoundError for unregistered tenant, got nil")
	}
}

// ---------------------------------------------------------------------------
// quota_transaction.go — entityBranchPutBytes, entityOpPutBytes,
//                        ExecuteWithMode, ExecuteEntity
// ---------------------------------------------------------------------------

// countingTxService is a TransactionServicer stub that records reach-through.
type countingTxService struct {
	service.TransactionServicer
	calls int
	err   error
}

func (m *countingTxService) Execute(_ context.Context, _ []service.Compare, _, _ []service.Op) (*service.TransactionResponse, error) {
	m.calls++
	return &service.TransactionResponse{}, m.err
}

func (m *countingTxService) ExecuteWithMode(_ context.Context, _ []service.Compare, _, _ []service.Op, _ writemode.Mode) (*service.TransactionResponse, error) {
	m.calls++
	return &service.TransactionResponse{}, m.err
}

func (m *countingTxService) ExecuteEntity(_ context.Context, _ *service.TransactionEntityRequest) (*service.TransactionEntityResponse, error) {
	m.calls++
	return &service.TransactionEntityResponse{}, m.err
}

// TestEntityBranchPutBytes_SelectsLarger verifies that entityBranchPutBytes
// returns the larger of the success/failure put-byte totals.
func TestEntityBranchPutBytes_SelectsLarger(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		success []service.TransactionEntityOp
		failure []service.TransactionEntityOp
		want    int
	}{
		{
			name:    "success larger",
			success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 100)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 50)}},
			want:    100,
		},
		{
			name:    "failure larger",
			success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 30)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 80)}},
			want:    80,
		},
		{
			name:    "equal",
			success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 40)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 40)}},
			want:    40,
		},
		{
			name:    "delete ops not counted",
			success: []service.TransactionEntityOp{{Type: "delete", Data: make([]byte, 200)}},
			failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 10)}},
			want:    10,
		},
		{
			name:    "both empty",
			success: nil,
			failure: nil,
			want:    0,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := entityBranchPutBytes(tc.success, tc.failure)
			if got != tc.want {
				t.Errorf("entityBranchPutBytes = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestEntityOpPutBytes_SumsPuts verifies that entityOpPutBytes sums only
// the "put" operations and ignores deletes.
func TestEntityOpPutBytes_SumsPuts(t *testing.T) {
	t.Parallel()
	ops := []service.TransactionEntityOp{
		{Type: "put", Data: make([]byte, 20)},
		{Type: "delete", Data: make([]byte, 100)}, // must not be counted
		{Type: "put", Data: make([]byte, 30)},
	}
	got := entityOpPutBytes(ops)
	if got != 50 {
		t.Errorf("entityOpPutBytes = %d, want 50", got)
	}
}

// TestEntityOpPutBytes_Empty verifies that an empty slice returns 0.
func TestEntityOpPutBytes_Empty(t *testing.T) {
	t.Parallel()
	if got := entityOpPutBytes(nil); got != 0 {
		t.Errorf("entityOpPutBytes(nil) = %d, want 0", got)
	}
}

// TestQuotaTransactionService_ExecuteWithMode_AccountsAndEnforces verifies that
// ExecuteWithMode reserves the larger branch's put bytes, forwards on success,
// and releases on inner error.
func TestQuotaTransactionService_ExecuteWithMode_AccountsAndEnforces(t *testing.T) {
	inner := &countingTxService{}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 100)
	ctx := context.Background()

	// success branch has 60 bytes — admitted.
	successOps := []service.Op{{Type: service.OpPut, Value: make([]byte, 60)}}
	_, err := tx.ExecuteWithMode(ctx, nil, successOps, nil, writemode.Mode(0))
	if err != nil {
		t.Fatalf("ExecuteWithMode within limit: %v", err)
	}
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage = %d, want 60", got)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}

	// A second batch of 50 bytes would exceed the 100-byte limit → rejected.
	bigOps := []service.Op{{Type: service.OpPut, Value: make([]byte, 50)}}
	_, err = tx.ExecuteWithMode(ctx, nil, bigOps, nil, writemode.Mode(0))
	if err == nil {
		t.Fatal("expected ExceededError, got nil")
	}
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Errorf("expected *quota.ExceededError, got %T: %v", err, err)
	}
	if inner.calls != 1 {
		t.Fatalf("rejected ExecuteWithMode must not reach inner (calls=%d)", inner.calls)
	}
}

// TestQuotaTransactionService_ExecuteWithMode_InnerErrorReleases verifies
// that reserved bytes are released when the inner service returns an error.
func TestQuotaTransactionService_ExecuteWithMode_InnerErrorReleases(t *testing.T) {
	inner := &countingTxService{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 1000)
	ctx := context.Background()

	ops := []service.Op{{Type: service.OpPut, Value: make([]byte, 50)}}
	if _, err := tx.ExecuteWithMode(ctx, nil, ops, nil, writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Fatalf("ExecuteWithMode err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after inner error = %d, want 0 (released)", acct.Usage("t1"))
	}
}

// TestQuotaTransactionService_ExecuteEntity_AccountsAndEnforces verifies that
// ExecuteEntity reserves the larger entity-branch put bytes.
func TestQuotaTransactionService_ExecuteEntity_AccountsAndEnforces(t *testing.T) {
	inner := &countingTxService{}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 100)
	ctx := context.Background()

	req := &service.TransactionEntityRequest{
		Success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 40)}},
		Failure: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 60)}},
	}
	_, err := tx.ExecuteEntity(ctx, req)
	if err != nil {
		t.Fatalf("ExecuteEntity within limit: %v", err)
	}
	// failure branch is larger (60 bytes) → that's what was reserved.
	if got := acct.Usage("t1"); got != 60 {
		t.Fatalf("usage = %d, want 60 (larger branch)", got)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
}

// TestQuotaTransactionService_ExecuteEntity_NilReqZeroBytes verifies that
// a nil request reserves zero bytes and still forwards to the inner service.
func TestQuotaTransactionService_ExecuteEntity_NilReqZeroBytes(t *testing.T) {
	inner := &countingTxService{}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 10)
	ctx := context.Background()

	if _, err := tx.ExecuteEntity(ctx, nil); err != nil {
		t.Fatalf("ExecuteEntity(nil): %v", err)
	}
	if got := acct.Usage("t1"); got != 0 {
		t.Fatalf("usage for nil req = %d, want 0", got)
	}
	if inner.calls != 1 {
		t.Fatalf("inner calls = %d, want 1", inner.calls)
	}
}

// TestQuotaTransactionService_ExecuteEntity_InnerErrorReleases verifies that
// reserved bytes are released on inner error.
func TestQuotaTransactionService_ExecuteEntity_InnerErrorReleases(t *testing.T) {
	inner := &countingTxService{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	tx := newQuotaTransactionService(inner, acct, "t1", 1000)
	ctx := context.Background()

	req := &service.TransactionEntityRequest{
		Success: []service.TransactionEntityOp{{Type: "put", Data: make([]byte, 30)}},
	}
	if _, err := tx.ExecuteEntity(ctx, req); !errors.Is(err, errBoom) {
		t.Fatalf("ExecuteEntity err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after inner error = %d, want 0 (released)", acct.Usage("t1"))
	}
}

// ---------------------------------------------------------------------------
// quota_batch.go — ExecuteWithMode, ExecuteEntity, Import
// ---------------------------------------------------------------------------

// cgCountingBatch is an inner BatchServicer that records reach-through and
// implements Import so the passthrough can be verified. (Named distinctly from
// the cgCountingBatch in quota_engines_test.go, which has a different shape.)
type cgCountingBatch struct {
	service.BatchServicer
	execCalls   int
	importCalls int
	err         error
}

func (m *cgCountingBatch) Execute(_ context.Context, ops []service.Op) ([]service.OpResponse, error) {
	m.execCalls++
	return make([]service.OpResponse, len(ops)), m.err
}

func (m *cgCountingBatch) ExecuteWithMode(_ context.Context, ops []service.Op, _ writemode.Mode) ([]service.OpResponse, error) {
	m.execCalls++
	return make([]service.OpResponse, len(ops)), m.err
}

func (m *cgCountingBatch) ExecuteEntity(_ context.Context, _ *service.BatchEntityRequest) (*service.BatchEntityResponse, error) {
	m.execCalls++
	return &service.BatchEntityResponse{}, m.err
}

func (m *cgCountingBatch) Import(_ context.Context, _ io.Reader, _ service.ImportOptions) (*service.ImportResult, error) {
	m.importCalls++
	return &service.ImportResult{}, m.err
}

// TestQuotaBatchService_ExecuteWithMode_AccountsAndEnforces verifies that
// ExecuteWithMode reserves the batch's put bytes before forwarding.
func TestQuotaBatchService_ExecuteWithMode_AccountsAndEnforces(t *testing.T) {
	inner := &cgCountingBatch{}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 100)
	ctx := context.Background()

	ops := []service.Op{
		{Type: service.OpPut, Value: make([]byte, 70)},
	}
	if _, err := batch.ExecuteWithMode(ctx, ops, writemode.Mode(0)); err != nil {
		t.Fatalf("ExecuteWithMode within limit: %v", err)
	}
	if got := acct.Usage("t1"); got != 70 {
		t.Fatalf("usage = %d, want 70", got)
	}
	if inner.execCalls != 1 {
		t.Fatalf("inner execCalls = %d, want 1", inner.execCalls)
	}

	// A second batch that would exceed the limit is rejected.
	bigOps := []service.Op{{Type: service.OpPut, Value: make([]byte, 40)}}
	_, err := batch.ExecuteWithMode(ctx, bigOps, writemode.Mode(0))
	var ee *quota.ExceededError
	if !errors.As(err, &ee) {
		t.Fatalf("over-limit ExecuteWithMode err = %v, want *quota.ExceededError", err)
	}
	if inner.execCalls != 1 {
		t.Fatalf("rejected call must not reach inner (execCalls=%d)", inner.execCalls)
	}
}

// TestQuotaBatchService_ExecuteWithMode_InnerErrorReleases verifies the
// release-on-error path for ExecuteWithMode.
func TestQuotaBatchService_ExecuteWithMode_InnerErrorReleases(t *testing.T) {
	inner := &cgCountingBatch{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 1000)
	ctx := context.Background()

	ops := []service.Op{{Type: service.OpPut, Value: make([]byte, 50)}}
	if _, err := batch.ExecuteWithMode(ctx, ops, writemode.Mode(0)); !errors.Is(err, errBoom) {
		t.Fatalf("ExecuteWithMode err = %v, want errBoom", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage after error = %d, want 0 (released)", acct.Usage("t1"))
	}
}

// TestQuotaBatchService_ExecuteEntity_NilReq verifies nil request reserves
// zero bytes and forwards to the inner service.
func TestQuotaBatchService_ExecuteEntity_NilReq(t *testing.T) {
	inner := &cgCountingBatch{}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 10)
	ctx := context.Background()

	if _, err := batch.ExecuteEntity(ctx, nil); err != nil {
		t.Fatalf("ExecuteEntity(nil): %v", err)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("usage for nil req = %d, want 0", acct.Usage("t1"))
	}
	if inner.execCalls != 1 {
		t.Fatalf("inner execCalls = %d, want 1", inner.execCalls)
	}
}

// TestQuotaBatchService_Import_Passthrough verifies that Import is forwarded
// to the inner service without quota accounting.
func TestQuotaBatchService_Import_Passthrough(t *testing.T) {
	inner := &cgCountingBatch{}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 100)
	ctx := context.Background()

	if _, err := batch.Import(ctx, nil, service.ImportOptions{}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if inner.importCalls != 1 {
		t.Fatalf("inner importCalls = %d, want 1", inner.importCalls)
	}
	if acct.Usage("t1") != 0 {
		t.Fatalf("Import must not account bytes (usage=%d)", acct.Usage("t1"))
	}
}

// TestQuotaBatchService_Import_PropagatesError verifies that inner errors
// are propagated correctly from Import.
func TestQuotaBatchService_Import_PropagatesError(t *testing.T) {
	inner := &cgCountingBatch{err: errBoom}
	acct, _ := newTestAccountant(t, nil)
	batch := newQuotaBatchService(inner, acct, "t1", 100)
	ctx := context.Background()

	if _, err := batch.Import(ctx, nil, service.ImportOptions{}); !errors.Is(err, errBoom) {
		t.Fatalf("Import error = %v, want errBoom", err)
	}
}

// ---------------------------------------------------------------------------
// Server option setters — WithClusterKeyReceiver, WithBackupService,
//                         WithRebalanceService, WithTenantHostRegistry,
//                         WithRaftBackendFactory, WithQuotaAccountant
// ---------------------------------------------------------------------------

// TestWithClusterKeyReceiver_StoresOnOpts verifies the option closure stores
// the provided ClusterKeyReceiver (nil accepted — routes not registered).
func TestWithClusterKeyReceiver_StoresOnOpts(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithClusterKeyReceiver(nil)(&opts)
	if opts.clusterKeyReceiver != nil {
		t.Error("expected nil clusterKeyReceiver after WithClusterKeyReceiver(nil)")
	}
}

// TestWithClusterKeyReceiver_NonNil verifies a non-nil value is stored.
func TestWithClusterKeyReceiver_NonNil(t *testing.T) {
	t.Parallel()
	// stubClusterKeyReceiver satisfies rest.ClusterKeyReceiver via embedding.
	rcv := &stubClusterKeyReceiver{}
	var opts serverOptions
	WithClusterKeyReceiver(rcv)(&opts)
	if opts.clusterKeyReceiver != rcv {
		t.Error("WithClusterKeyReceiver did not store provided receiver")
	}
}

// TestWithBackupService_StoresOnOpts verifies the option closure stores
// the provided BackupServicer.
func TestWithBackupService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithBackupService(nil)(&opts)
	if opts.backupSvc != nil {
		t.Error("expected nil backupSvc after WithBackupService(nil)")
	}
}

// TestWithBackupService_NonNil verifies a non-nil value is stored.
func TestWithBackupService_NonNil(t *testing.T) {
	t.Parallel()
	svc := &stubBackupService{}
	var opts serverOptions
	WithBackupService(svc)(&opts)
	if opts.backupSvc != svc {
		t.Error("WithBackupService did not store provided service")
	}
}

// TestWithRebalanceService_StoresOnOpts verifies the option closure stores
// the provided RebalanceServicer.
func TestWithRebalanceService_StoresOnOpts(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithRebalanceService(nil)(&opts)
	if opts.rebalanceSvc != nil {
		t.Error("expected nil rebalanceSvc after WithRebalanceService(nil)")
	}
}

// TestWithRebalanceService_NonNil verifies a non-nil value is stored.
func TestWithRebalanceService_NonNil(t *testing.T) {
	t.Parallel()
	svc := &stubRebalanceService{}
	var opts serverOptions
	WithRebalanceService(svc)(&opts)
	if opts.rebalanceSvc != svc {
		t.Error("WithRebalanceService did not store provided service")
	}
}

// TestWithTenantHostRegistry_StoresOnOpts verifies the option closure stores
// the TenantHostRegistry.
func TestWithTenantHostRegistry_StoresOnOpts(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithTenantHostRegistry(nil)(&opts)
	if opts.tenantHosts != nil {
		t.Error("expected nil tenantHosts after WithTenantHostRegistry(nil)")
	}
}

// TestWithTenantHostRegistry_NonNil verifies a non-nil registry is stored.
func TestWithTenantHostRegistry_NonNil(t *testing.T) {
	t.Parallel()
	reg := &TenantHostRegistry{}
	var opts serverOptions
	WithTenantHostRegistry(reg)(&opts)
	if opts.tenantHosts != reg {
		t.Error("WithTenantHostRegistry did not store provided registry")
	}
}

// TestWithRaftBackendFactory_StoresOnOpts verifies the option closure stores
// the RaftBackendFactory.
func TestWithRaftBackendFactory_StoresOnOpts(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithRaftBackendFactory(nil)(&opts)
	if opts.raftBackend != nil {
		t.Error("expected nil raftBackend after WithRaftBackendFactory(nil)")
	}
}

// TestWithRaftBackendFactory_NonNil verifies a non-nil factory is stored.
func TestWithRaftBackendFactory_NonNil(t *testing.T) {
	t.Parallel()
	f := &RaftBackendFactory{}
	var opts serverOptions
	WithRaftBackendFactory(f)(&opts)
	if opts.raftBackend != f {
		t.Error("WithRaftBackendFactory did not store provided factory")
	}
}

// TestWithQuotaAccountant_StoresOnOpts verifies the option closure stores
// the Accountant.
func TestWithQuotaAccountant_StoresOnOpts(t *testing.T) {
	t.Parallel()
	var opts serverOptions
	WithQuotaAccountant(nil)(&opts)
	if opts.quotaAccountant != nil {
		t.Error("expected nil quotaAccountant after WithQuotaAccountant(nil)")
	}
}

// TestWithQuotaAccountant_NonNil verifies a non-nil accountant is stored.
func TestWithQuotaAccountant_NonNil(t *testing.T) {
	t.Parallel()
	acct, _ := newTestAccountant(t, nil)
	var opts serverOptions
	WithQuotaAccountant(acct)(&opts)
	if opts.quotaAccountant != acct {
		t.Error("WithQuotaAccountant did not store provided accountant")
	}
}

// ---------------------------------------------------------------------------
// Stub types for option setter tests
// ---------------------------------------------------------------------------

// stubClusterKeyReceiver satisfies rest.ClusterKeyReceiver (Handler +
// NodeKeyHandler returning http.HandlerFunc).
type stubClusterKeyReceiver struct{}

func (s *stubClusterKeyReceiver) Handler() http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}
func (s *stubClusterKeyReceiver) NodeKeyHandler() http.HandlerFunc {
	return func(http.ResponseWriter, *http.Request) {}
}

// stubBackupService satisfies service.BackupServicer.
type stubBackupService struct{ service.BackupServicer }

// stubRebalanceService satisfies service.RebalanceServicer.
type stubRebalanceService struct{ service.RebalanceServicer }
