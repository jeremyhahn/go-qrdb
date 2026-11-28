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

package lifecycle_test

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/atrest"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newMigrationVolumeManager returns a manager whose LocalBindingFactory
// resolves every tenant as volume-isolated under root, encrypted under
// tenantEnc, with storage as the node's storage configuration.
func newMigrationVolumeManager(t *testing.T, raftMode bool, storage qrdbconfig.StorageConfig, tenantEnc *atrest.Encryptor, root string) *database.Manager {
	t.Helper()
	dir := t.TempDir()
	svc := &isolationTenantService{isolation: tenant.IsolationVolume}
	f := lifecycle.NewLocalBindingFactory(database.BindingNode{DataDir: dir, RaftMode: raftMode}, nil, storage,
		lifecycle.TenantServiceBindingLookup(
			func() tenant.TenantServicer { return svc },
			func() lifecycle.TenantDataRootResolver { return staticDataRoot(root) },
		)).WithTenantEncryptors(func(string) (*atrest.Encryptor, error) { return tenantEnc, nil })
	mgr := database.NewManager(slog.Default(), dir, database.WithBackendFactory(f))
	cat, err := database.NewKVCatalog(kvstore.NewMemoryKVStore(), nil)
	if err != nil {
		t.Fatalf("NewKVCatalog: %v", err)
	}
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// TestTenantEncryptedStoreDirs_ListsEveryVolumeDatabaseStore checks the
// tenant encryption migration's store list holds the store of every one of
// a volume tenant's on-disk databases, opened or closed, in standalone and
// raft modes, and none of another tenant's.
func TestTenantEncryptedStoreDirs_ListsEveryVolumeDatabaseStore(t *testing.T) {
	ctx := context.Background()
	tenantEnc := atrest.New(newUnsealedBarrier(t))
	for _, raftMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "raft"}[raftMode], func(t *testing.T) {
			root := t.TempDir()
			mgr := newMigrationVolumeManager(t, raftMode, qrdbconfig.StorageConfig{}, tenantEnc, root)
			for _, cfg := range []database.DatabaseConfig{
				{TenantID: bindingTenant, Name: "orders", Backend: database.BackendPebble},
				{TenantID: bindingTenant, Name: "ledger", Backend: database.BackendPebble},
				{TenantID: bindingTenant, Name: "cache", Backend: database.BackendMemory},
			} {
				if _, err := mgr.Create(cfg); err != nil {
					t.Fatalf("Create %s: %v", cfg.Name, err)
				}
			}
			want := []string{
				filepath.Join(root, "databases", "ledger", "pebble"),
				filepath.Join(root, "databases", "orders", "pebble"),
			}
			check := func(stage string) {
				t.Helper()
				pebbleDirs, fileDirs, err := database.TenantEncryptedStoreDirs(ctx, mgr, bindingTenant)
				if err != nil {
					t.Fatalf("TenantEncryptedStoreDirs (%s): %v", stage, err)
				}
				slices.Sort(pebbleDirs)
				if !slices.Equal(pebbleDirs, want) || len(fileDirs) != 0 {
					t.Fatalf("TenantEncryptedStoreDirs (%s) = %v, %v; want %v, []", stage, pebbleDirs, fileDirs, want)
				}
			}
			check("open")
			// The migration closes the tenant's databases before it recodes
			// their stores: a closed database's store is still listed.
			if err := mgr.CloseTenant(bindingTenant); err != nil {
				t.Fatalf("CloseTenant: %v", err)
			}
			check("closed")
			pebbleDirs, fileDirs, err := database.TenantEncryptedStoreDirs(ctx, mgr, "other-tenant")
			if err != nil || len(pebbleDirs) != 0 || len(fileDirs) != 0 {
				t.Fatalf("TenantEncryptedStoreDirs(other-tenant) = %v, %v, %v; want none", pebbleDirs, fileDirs, err)
			}
		})
	}
}

// TestTenantEncryptedStoreDirs_RefusesExternalWAL checks a volume tenant's
// pebble database whose write-ahead log sits under storage.wal_dir refuses
// the tenant encryption migration, which moves the store directory alone.
func TestTenantEncryptedStoreDirs_RefusesExternalWAL(t *testing.T) {
	ctx := context.Background()
	tenantEnc := atrest.New(newUnsealedBarrier(t))
	for _, raftMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "raft"}[raftMode], func(t *testing.T) {
			storage := qrdbconfig.StorageConfig{WALDir: t.TempDir()}
			mgr := newMigrationVolumeManager(t, raftMode, storage, tenantEnc, t.TempDir())
			if _, err := mgr.Create(database.DatabaseConfig{TenantID: bindingTenant, Name: "orders", Backend: database.BackendPebble}); err != nil {
				t.Fatalf("Create: %v", err)
			}
			_, _, err := database.TenantEncryptedStoreDirs(ctx, mgr, bindingTenant)
			var unsupported *database.StoreMigrationUnsupportedError
			if !errors.As(err, &unsupported) || unsupported.Database != "orders" {
				t.Fatalf("TenantEncryptedStoreDirs = %v, want *StoreMigrationUnsupportedError for orders", err)
			}
		})
	}
}
