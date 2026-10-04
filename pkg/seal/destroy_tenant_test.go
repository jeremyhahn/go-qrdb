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

package seal

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/jeremyhahn/go-objstore/pkg/local"
	objpool "github.com/jeremyhahn/go-objstore/pkg/pool"

	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	qrdbpool "github.com/jeremyhahn/go-qrdb/pkg/storage/pool"
)

// newPooledTestRegistry builds a BarrierRegistry over a real single-path
// local volume pool, a real Pebble-backed state store and a real per-tenant
// Pebble registry, with its system barrier initialized and unsealed.
//
// Everything here is the production type. DestroyTenant's whole job is to
// remove records from the system barrier store, close a real Pebble and
// unlink a real directory, so a double for any of the three would remove
// exactly the thing under test.
func newPooledTestRegistry(t *testing.T, extra ...Option) (*BarrierRegistry, *StorageBarrier) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	c := local.New()
	if err := c.Configure(map[string]string{"path": t.TempDir()}); err != nil {
		t.Fatalf("configure local backend: %v", err)
	}
	stateStorage, err := pebblestore.New(&pebblestore.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("open state-store Pebble: %v", err)
	}
	t.Cleanup(func() { _ = stateStorage.Close() })
	state, err := qrdbpool.NewPebbleStateStore(stateStorage)
	if err != nil {
		t.Fatalf("NewPebbleStateStore: %v", err)
	}
	mgr := objpool.NewManager()
	if err := mgr.Register("default", []common.Storage{c}, &objpool.RoundRobinStrategy{State: state}); err != nil {
		t.Fatalf("pool Register: %v", err)
	}

	strategy, err := NewSoftwareStrategy([]byte("destroy-tenant-system-pass"))
	if err != nil {
		t.Fatalf("NewSoftwareStrategy: %v", err)
	}
	system, err := NewStorageBarrier(logger, NewMemoryBackend(),
		BarrierConfig{RootKeyPath: "system/root-key"}, strategy)
	if err != nil {
		t.Fatalf("NewStorageBarrier: %v", err)
	}
	if err := system.Initialize(context.Background(), Credentials{}); err != nil {
		t.Fatalf("system Initialize: %v", err)
	}

	perTenantReg := pebblestore.NewPerTenantRegistry()
	t.Cleanup(func() { _ = perTenantReg.CloseAll(context.Background()) })

	opts := append([]Option{
		WithVolumePool(mgr, state, "default"),
		WithPerTenantRegistry(perTenantReg),
	}, extra...)
	reg, err := NewBarrierRegistry(logger, system, opts...)
	if err != nil {
		t.Fatalf("NewBarrierRegistry: %v", err)
	}
	return reg, system
}

// ---------------------------------------------------------------------------
// go-qrdb#413 -- UnregisterTenant is not a delete
//
// UnregisterTenant seals and forgets the IN-MEMORY barrier. Two durable
// records outlive it: the DataRoot at "__registry:tenant_root/{id}" and the
// volume-pool slot assignment. RestoreVolumeTenants reads the first at boot,
// so a tenant deleted through UnregisterTenant came back on the next restart,
// at the same directory, over the same store. DestroyTenant is the operation
// a deletion needs.
// ---------------------------------------------------------------------------

// TestDestroyTenant_RemovesTheRecordsThatWouldRestoreIt is the test that
// separates DestroyTenant from UnregisterTenant.
func TestDestroyTenant_RemovesTheRecordsThatWouldRestoreIt(t *testing.T) {
	ctx := context.Background()
	reg, system := newPooledTestRegistry(t)

	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := reg.InitializeTenant(ctx, "acme", Credentials{}); err != nil {
		t.Fatalf("InitializeTenant: %v", err)
	}
	if _, err := system.Get(ctx, tenantRootStorePrefix+"acme"); err != nil {
		t.Fatalf("the fixture did not persist a tenant-root record: %v", err)
	}

	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("DestroyTenant: %v", err)
	}

	if _, err := system.Get(ctx, tenantRootStorePrefix+"acme"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("the tenant-root record survived DestroyTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("GetTenant after destroy = %v, want ErrTenantNotFound", err)
	}
	if _, err := os.Stat(dataRoot); !os.IsNotExist(err) {
		t.Errorf("the DataRoot %s survived DestroyTenant (stat err %v)", dataRoot, err)
	}

	// The boot path must not bring it back.
	if err := reg.RestoreVolumeTenants(ctx); err != nil {
		t.Fatalf("RestoreVolumeTenants: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("a destroyed tenant was restored at boot: %v", err)
	}
}

// TestDestroyTenant_IsIdempotent: a destroy that is re-issued after an
// interrupted one must finish rather than fail, which is what makes
// DeleteTenant re-issuable.
func TestDestroyTenant_IsIdempotent(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)

	if _, _, err := reg.RegisterVolumeTenant(ctx, "acme", true); err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("first DestroyTenant: %v", err)
	}
	// The second call has nothing registered, which is reported as
	// ErrTenantNotFound so a caller can tell "was never here" from "failed".
	if err := reg.DestroyTenant(ctx, "acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("second DestroyTenant = %v, want ErrTenantNotFound", err)
	}
}

// TestDestroyTenant_EmptyIDIsRefused guards against a destroy with no scope.
func TestDestroyTenant_EmptyIDIsRefused(t *testing.T) {
	reg, _ := newPooledTestRegistry(t)
	if err := reg.DestroyTenant(context.Background(), ""); !errors.Is(err, ErrEmptyTenantID) {
		t.Errorf("DestroyTenant(\"\") = %v, want ErrEmptyTenantID", err)
	}
}

// TestDestroyTenant_KeyspaceModeTenantHasNoDataRoot: an IsolationBarrier
// tenant registered with an explicit in-memory backend has no dedicated
// directory, so the filesystem step is skipped rather than guessed at.
func TestDestroyTenant_KeyspaceModeTenantHasNoDataRoot(t *testing.T) {
	ctx := context.Background()
	reg, system := newPooledTestRegistry(t)

	if _, err := reg.RegisterTenantDefault("acme"); err != nil {
		t.Fatalf("RegisterTenantDefault: %v", err)
	}
	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("DestroyTenant: %v", err)
	}
	if _, err := reg.GetTenant("acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Errorf("GetTenant after destroy = %v, want ErrTenantNotFound", err)
	}
	if _, err := system.Get(ctx, tenantRootStorePrefix+"acme"); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("a keyspace-mode tenant must have no tenant-root record: %v", err)
	}
}

// TestDestroyTenant_LeavesOtherTenantsAlone: the destroy is scoped.
func TestDestroyTenant_LeavesOtherTenantsAlone(t *testing.T) {
	ctx := context.Background()
	reg, system := newPooledTestRegistry(t)

	rootA, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(acme): %v", err)
	}
	rootB, _, err := reg.RegisterVolumeTenant(ctx, "globex", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant(globex): %v", err)
	}
	if rootA == rootB {
		t.Fatalf("both tenants were given the same DataRoot %s", rootA)
	}

	if err := reg.DestroyTenant(ctx, "acme"); err != nil {
		t.Fatalf("DestroyTenant: %v", err)
	}

	if _, err := reg.GetTenant("globex"); err != nil {
		t.Errorf("the other tenant's barrier was removed: %v", err)
	}
	if _, err := system.Get(ctx, tenantRootStorePrefix+"globex"); err != nil {
		t.Errorf("the other tenant's root record was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootB, "barrier")); err != nil {
		t.Errorf("the other tenant's store was removed: %v", err)
	}
}

// TestDestroyTenantErrors_NameWhatFailed covers the three typed failures.
//
// They are constructed directly rather than provoked. Each one wraps an
// operation the operating system decides the outcome of -- closing a Pebble,
// deleting a barrier record, unlinking a directory -- and the only portable
// way to provoke the third is to make a directory unremovable, which root
// ignores. A test that passed by being skipped on half the machines that run
// it would be asserting nothing there; what is worth pinning is that each
// error names the tenant, names what failed, and carries the cause.
func TestDestroyTenantErrors_NameWhatFailed(t *testing.T) {
	cause := errors.New("disk on fire")

	closeErr := &TenantStoreCloseError{TenantID: "acme", Err: cause}
	if !strings.Contains(closeErr.Error(), "acme") ||
		!strings.Contains(closeErr.Error(), cause.Error()) {
		t.Errorf("TenantStoreCloseError = %q, want the tenant and the cause", closeErr)
	}
	if !errors.Is(closeErr, cause) {
		t.Error("TenantStoreCloseError must unwrap to its cause")
	}

	recErr := &TenantRecordDeleteError{TenantID: "acme", Record: "tenant-root", Err: cause}
	if !strings.Contains(recErr.Error(), "tenant-root") ||
		!strings.Contains(recErr.Error(), "acme") {
		t.Errorf("TenantRecordDeleteError = %q, want the record and the tenant", recErr)
	}
	if !errors.Is(recErr, cause) {
		t.Error("TenantRecordDeleteError must unwrap to its cause")
	}

	rmErr := &TenantDataRootRemoveError{TenantID: "acme", DataRoot: "/srv/acme", Err: cause}
	if !strings.Contains(rmErr.Error(), "/srv/acme") ||
		!strings.Contains(rmErr.Error(), "acme") {
		t.Errorf("TenantDataRootRemoveError = %q, want the directory and the tenant", rmErr)
	}
	if !errors.Is(rmErr, cause) {
		t.Error("TenantDataRootRemoveError must unwrap to its cause")
	}
}

// TestDestroyTenant_RecoversTheDataRootFromThePersistedRecord: a node that has
// not re-attached the tenant in this process still knows where its data is,
// because the record is the authority. Without this, a destroy issued on a
// follower would remove the records and leave the directory.
func TestDestroyTenant_RecoversTheDataRootFromThePersistedRecord(t *testing.T) {
	ctx := context.Background()
	reg, _ := newPooledTestRegistry(t)

	dataRoot, _, err := reg.RegisterVolumeTenant(ctx, "acme", true)
	if err != nil {
		t.Fatalf("RegisterVolumeTenant: %v", err)
	}
	// Simulate a process that never re-attached: drop the in-memory handle and
	// the cached DataRoot, leaving only the persisted record.
	if err := reg.RemoveTenant("acme"); err != nil {
		t.Fatalf("RemoveTenant: %v", err)
	}
	reg.tenantDataRoots.Delete("acme")

	if err := reg.DestroyTenant(ctx, "acme"); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("DestroyTenant = %v, want ErrTenantNotFound (nothing registered here)", err)
	}
	if _, err := os.Stat(dataRoot); !os.IsNotExist(err) {
		t.Errorf("the DataRoot %s survived a destroy driven by the persisted record (stat err %v)",
			dataRoot, err)
	}
}
