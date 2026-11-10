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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// listFnServicer satisfies tenant.TenantServicer by delegating all calls to
// the embedded TenantServicer except ListTenants, which uses the provided
// closure. The embedded interface may be nil when the test only calls
// ListTenants (calling any other method on a nil interface panics).
type listFnServicer struct {
	tenant.TenantServicer
	fn func(context.Context) ([]tenant.TenantInfo, error)
}

func (s *listFnServicer) ListTenants(ctx context.Context) ([]tenant.TenantInfo, error) {
	return s.fn(ctx)
}

// TestReattachVolumeTenants_ListError verifies the ListTenants error is
// propagated unchanged (boot must abort).
func TestReattachVolumeTenants_ListError(t *testing.T) {
	wantErr := errors.New("list failed")
	svc := &listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) {
		return nil, wantErr
	}}

	err := reattachVolumeTenants(context.Background(), svc, nil, nil, reattachTestLogger())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected propagated list error, got %v", err)
	}
}

// TestReattachVolumeTenants_SkipsUnprovisioned verifies that a volume tenant
// whose barrier is not present in the local registry (this node holds no key
// material for it) is skipped rather than re-registered with the foreign
// replicated DataRoot.
func TestReattachVolumeTenants_SkipsUnprovisioned(t *testing.T) {
	_, reg := newReattachFixture(t)
	svc := &listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) {
		return []tenant.TenantInfo{
			// A non-volume tenant must be skipped before the registry lookup.
			{ID: "prefix-t", IsolationType: tenant.IsolationPrefix},
			// Not registered in reg: must be skipped (deferred to lazy attach).
			{ID: "vol-t", IsolationType: tenant.IsolationVolume, VolumePool: "default"},
		}, nil
	}}

	if err := reattachVolumeTenants(context.Background(), svc, reg, nil, reattachTestLogger()); err != nil {
		t.Fatalf("reattachVolumeTenants: expected nil (unprovisioned tenant skipped), got %v", err)
	}
	if _, gerr := reg.GetTenant("vol-t"); gerr == nil {
		t.Error("unprovisioned volume tenant must not be registered from the replicated DataRoot")
	}
}

// TestReattachRaftTenants_ListError verifies the ListTenants error is
// propagated unchanged.
func TestReattachRaftTenants_ListError(t *testing.T) {
	wantErr := errors.New("list failed")
	svc := &listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) {
		return nil, wantErr
	}}

	err := reattachRaftTenants(context.Background(), svc, nil, nil, nil, reattachTestLogger())
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected propagated list error, got %v", err)
	}
}

// TestReattachRaftTenants_SkipsUnprovisioned verifies that a raft tenant whose
// barrier is not present in the local registry is skipped and deferred to the
// reconciler rather than re-registered with the foreign replicated DataRoot.
func TestReattachRaftTenants_SkipsUnprovisioned(t *testing.T) {
	_, reg := newReattachFixture(t)
	svc := &listFnServicer{fn: func(context.Context) ([]tenant.TenantInfo, error) {
		return []tenant.TenantInfo{
			{ID: "prefix-t", IsolationType: tenant.IsolationPrefix},
			// Not registered in reg: must be skipped (deferred to reconciler).
			{ID: "raft-t", IsolationType: tenant.IsolationRaft, VolumePool: "default"},
		}, nil
	}}

	if err := reattachRaftTenants(context.Background(), svc, reg, nil, nil, reattachTestLogger()); err != nil {
		t.Fatalf("reattachRaftTenants: expected nil (unprovisioned tenant skipped), got %v", err)
	}
	if _, gerr := reg.GetTenant("raft-t"); gerr == nil {
		t.Error("unprovisioned raft tenant must not be registered from the replicated DataRoot")
	}
}

// TestReattachRaftTenants_SkipsNonRaftDatabase verifies a raft tenant's
// non-raft database is never attached: with a nil raft factory, an Attach
// would panic.
func TestReattachRaftTenants_SkipsNonRaftDatabase(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "rnoskip", "No Skip Raft", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(rnoskip): %v", err)
	}

	mgr := claimingManager(t, reattachTestLogger(), t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })

	tenantCtx := tenant.WithTenantID(ctx, "rnoskip")
	_, err := mgr.CreateForTenant(tenantCtx, database.DatabaseConfig{
		Name:    "memdb",
		Backend: database.BackendMemory,
	})
	require.NoError(t, err, "CreateForTenant(memdb): unexpected error")

	require.NoError(t, reg.SealTenant("rnoskip"))
	require.NoError(t, reattachRaftTenants(ctx, svc, reg, nil, mgr, reattachTestLogger()))
}

// TestReattachVolumeTenants_UnsealTenantError verifies a failed fallback
// unseal of an auto-unseal volume tenant is returned as *VolumeReattachError.
func TestReattachVolumeTenants_UnsealTenantError(t *testing.T) {
	svc, reg := newReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "vunseal-err", "UnsealErr", tenant.IsolationVolume,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(vunseal-err): %v", err)
	}
	require.NoError(t, reg.SealTenant("vunseal-err"))
	// Seal the system barrier so UnsealTenant fails.
	require.NoError(t, reg.SystemBarrier().Seal())

	rerr := reattachVolumeTenants(ctx, svc, reg, nil, reattachTestLogger())
	var vrErr *VolumeReattachError
	require.True(t, errors.As(rerr, &vrErr),
		"expected *VolumeReattachError from UnsealTenant failure, got %T: %v", rerr, rerr)
}

// TestReattachRaftTenants_UnsealTenantError verifies a failed fallback unseal
// of an auto-unseal raft tenant is returned as *RaftReattachError.
func TestReattachRaftTenants_UnsealTenantError(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "runseal-err", "RUnsealErr", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(runseal-err): %v", err)
	}
	require.NoError(t, reg.SealTenant("runseal-err"))
	// Seal the system barrier so UnsealTenant fails.
	require.NoError(t, reg.SystemBarrier().Seal())

	// nil raftReg and manager: the function returns before any Attach.
	rerr := reattachRaftTenants(ctx, svc, reg, nil, nil, reattachTestLogger())
	var rrErr *RaftReattachError
	require.True(t, errors.As(rerr, &rrErr),
		"expected *RaftReattachError from UnsealTenant failure, got %T: %v", rerr, rerr)
}
