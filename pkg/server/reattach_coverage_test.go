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
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// reattachRaftTenants: non-raft database skipped in inner loop
//
// The inner loop iterates manager.List() looking for databases owned by the
// current raft tenant whose backend is BackendRaft. An entry with a
// non-BackendRaft backend must hit continue (skip) and return nil.
// ---------------------------------------------------------------------------

// TestReattachRaftTenants_SkipsNonRaftDatabase verifies that the inner loop
// of reattachRaftTenants skips a database whose backend is not BackendRaft
// when a BackendMemory database is registered for the tenant.
func TestReattachRaftTenants_SkipsNonRaftDatabase(t *testing.T) {
	svc, reg := newRaftReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "rnoskip", "No Skip Raft", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(rnoskip): %v", err)
	}

	mgr := database.NewManager(reattachTestLogger(), t.TempDir(),
		database.WithShardIDClaimer(testShardClaimer(t)))
	t.Cleanup(func() { _ = mgr.Close() })

	tenantCtx := tenant.WithTenantID(ctx, "rnoskip")
	_, err := mgr.CreateForTenant(tenantCtx, database.DatabaseConfig{
		Name:    "memdb",
		Engine:  database.EngineKV,
		Backend: database.BackendMemory,
	})
	require.NoError(t, err, "CreateForTenant(memdb): unexpected error")

	require.NoError(t, reg.SealTenant("rnoskip"))

	// nil raftReg is safe: BackendRaft is never encountered so Attach is
	// never called.
	require.NoError(t, reattachRaftTenants(ctx, svc, reg, nil, mgr, reattachTestLogger()))
}

// ---------------------------------------------------------------------------
// reattachVolumeTenants: tenant absent from the local registry is skipped
//
// A tenant whose barrier is not in the local registry must be skipped
// (GetTenant returns not-found → continue). The function returns nil and
// the bare registry remains free of the tenant.
// ---------------------------------------------------------------------------

// TestReattachVolumeTenants_SkipsTenantAbsentFromRegistry verifies that
// reattachVolumeTenants SKIPS a volume tenant whose barrier is not present
// in the local registry — it must NOT re-register it from the replicated
// DataRoot (which belongs to the creating node).
func TestReattachVolumeTenants_SkipsTenantAbsentFromRegistry(t *testing.T) {
	svc, _ := newReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "vreg-skip", "RegSkip", tenant.IsolationVolume,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(vreg-skip): %v", err)
	}

	// A registry that never had this tenant registered.
	sysStrat, err := seal.NewSoftwareStrategy([]byte("bare-vol-pass"))
	require.NoError(t, err)
	bareSys, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "bare-vol/root-key"}, sysStrat)
	require.NoError(t, err)
	require.NoError(t, bareSys.Initialize(ctx, seal.Credentials{}))
	bareReg, err := seal.NewBarrierRegistry(reattachTestLogger(), bareSys)
	require.NoError(t, err)

	// hostReg is nil: a skipped tenant never reaches Attach.
	require.NoError(t, reattachVolumeTenants(ctx, svc, bareReg, nil, reattachTestLogger()))
	_, gerr := bareReg.GetTenant("vreg-skip")
	require.Error(t, gerr, "tenant must not be registered from the replicated DataRoot")
}

// ---------------------------------------------------------------------------
// reattachVolumeTenants: UnsealTenant error
//
// When the system barrier is sealed, SystemBarrierStrategy.UnsealRootKey
// returns ErrSystemBarrierUnavailable. reattachVolumeTenants wraps this in
// *VolumeReattachError and returns.
// ---------------------------------------------------------------------------

// TestReattachVolumeTenants_UnsealTenantError verifies that
// reattachVolumeTenants returns *VolumeReattachError when UnsealTenant fails
// because the system barrier is sealed.
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
	require.Error(t, rerr)
	var vrErr *VolumeReattachError
	require.True(t, errors.As(rerr, &vrErr),
		"expected *VolumeReattachError from UnsealTenant failure, got %T: %v", rerr, rerr)
}

// ---------------------------------------------------------------------------
// reattachRaftTenants: tenant absent from the local registry is skipped
// ---------------------------------------------------------------------------

// TestReattachRaftTenants_SkipsTenantAbsentFromRegistry verifies that
// reattachRaftTenants SKIPS a raft tenant whose barrier is not present in
// the local registry.
func TestReattachRaftTenants_SkipsTenantAbsentFromRegistry(t *testing.T) {
	svc, _ := newRaftReattachFixture(t)
	ctx := context.Background()

	if _, err := svc.CreateTenant(ctx, "rreg-skip", "RRegSkip", tenant.IsolationRaft,
		tenant.WithBarrierAutoUnseal(true)); err != nil {
		t.Fatalf("CreateTenant(rreg-skip): %v", err)
	}

	sysStrat, err := seal.NewSoftwareStrategy([]byte("bare-raft-pass"))
	require.NoError(t, err)
	bareSys, err := seal.NewStorageBarrier(reattachTestLogger(), seal.NewMemoryBackend(),
		seal.BarrierConfig{RootKeyPath: "bare-raft/root-key"}, sysStrat)
	require.NoError(t, err)
	require.NoError(t, bareSys.Initialize(ctx, seal.Credentials{}))
	bareReg, err := seal.NewBarrierRegistry(reattachTestLogger(), bareSys)
	require.NoError(t, err)

	// nil raftReg and manager: a skipped tenant never reaches Attach.
	require.NoError(t, reattachRaftTenants(ctx, svc, bareReg, nil, nil, reattachTestLogger()))
	_, gerr := bareReg.GetTenant("rreg-skip")
	require.Error(t, gerr, "tenant must not be registered from the replicated DataRoot")
}

// ---------------------------------------------------------------------------
// reattachRaftTenants: UnsealTenant error
// ---------------------------------------------------------------------------

// TestReattachRaftTenants_UnsealTenantError verifies that
// reattachRaftTenants returns *RaftReattachError when UnsealTenant fails
// because the system barrier is sealed.
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

	// nil raftReg and manager: the function returns before reaching
	// manager.List().
	rerr := reattachRaftTenants(ctx, svc, reg, nil, nil, reattachTestLogger())
	require.Error(t, rerr)
	var rrErr *RaftReattachError
	require.True(t, errors.As(rerr, &rrErr),
		"expected *RaftReattachError from UnsealTenant failure, got %T: %v", rerr, rerr)
}
