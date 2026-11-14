// Copyright (c) 2026 Jeremy Hahn
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

package node

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// A seal-enabled standalone node whose first database is pebble reports no
// unencrypted store, and the report stays true: the node's database manager
// refuses an additional objstore database, with or without a barrier of its
// own, so no database writes plaintext to an object provider that neither
// unencryptedAtRest nor the startup warning names.
func TestStandaloneNode_SealedPebbleNodeRefusesAnObjstoreDatabase(t *testing.T) {
	cfg := &qrdbconfig.Config{
		Mode:      "standalone",
		Seal:      qrdbconfig.SealConfig{Enabled: true},
		Databases: []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}},
	}
	require.Empty(t, cfg.EffectiveCapabilities().UnencryptedAtRest, "a sealed pebble node reports no unencrypted store")

	n := &Node{logger: slog.New(slog.DiscardHandler)}
	dataDir := t.TempDir()
	mgr := n.newStandaloneDatabaseManager(dataDir, cfg)
	mgr.SetCatalog(databasetest.Catalog(dataDir))
	t.Cleanup(func() { _ = mgr.Close() })

	for _, dbCfg := range []database.DatabaseConfig{
		{Name: "blobs", Backend: database.BackendObjstore},
		{Name: "sealed-blobs", Backend: database.BackendObjstore, Barrier: &database.DatabaseBarrierConfig{Enabled: true}},
	} {
		_, err := mgr.Create(dbCfg)
		require.Errorf(t, err, "Create(%s) is refused", dbCfg.Name)
		if dbCfg.Barrier == nil {
			var sealed *database.SealedNodeBackendError
			require.ErrorAsf(t, err, &sealed, "Create(%s) = %v, want *database.SealedNodeBackendError", dbCfg.Name, err)
		} else {
			var shared *database.SharedStoreBarrierUnsupportedError
			require.ErrorAsf(t, err, &shared, "Create(%s) = %v, want *database.SharedStoreBarrierUnsupportedError", dbCfg.Name, err)
		}
		_, err = mgr.Get(dbCfg.Name)
		require.Errorf(t, err, "the refused %s database is not served", dbCfg.Name)
	}
}

// standaloneObjstoreManager builds an unsealed standalone node's database
// manager with opts applied as the constructor applies them.
func standaloneObjstoreManager(t *testing.T, sealed bool, opts ...NodeOption) *database.Manager {
	t.Helper()
	var o nodeOptions
	for _, opt := range opts {
		opt(&o)
	}
	cfg := &qrdbconfig.Config{
		Mode:      "standalone",
		Seal:      qrdbconfig.SealConfig{Enabled: sealed},
		Databases: []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}},
	}
	n := &Node{logger: slog.New(slog.DiscardHandler), objectStorage: o.objectStorage}
	dataDir := t.TempDir()
	mgr := n.newStandaloneDatabaseManager(dataDir, cfg)
	mgr.SetCatalog(databasetest.Catalog(dataDir))
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr
}

// fakeObjectStorage is a database.ObjectStorageOpener over an in-memory store
// that records the databases it opened.
type fakeObjectStorage struct{ opened []string }

func (f *fakeObjectStorage) open(cfg database.DatabaseConfig) (storage.Storage, error) {
	f.opened = append(f.opened, cfg.Name)
	return memory.NewStorage(), nil
}

func TestStandaloneNode_WithObjectStorageOpensAnObjstoreDatabase(t *testing.T) {
	fake := &fakeObjectStorage{}
	mgr := standaloneObjstoreManager(t, false, WithObjectStorage(fake.open))
	_, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	require.NoError(t, err)
	require.Equal(t, []string{"blobs"}, fake.opened, "the node's opener opened the database")
	_, err = mgr.Get("blobs")
	require.NoError(t, err, "the objstore database is served")
}

func TestStandaloneNode_WithoutObjectStorageRefusesAnObjstoreDatabase(t *testing.T) {
	mgr := standaloneObjstoreManager(t, false)
	_, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	var notWired *database.ObjectStorageNotWiredError
	require.ErrorAsf(t, err, &notWired, "Create(blobs) = %v, want *database.ObjectStorageNotWiredError", err)
	_, err = mgr.Get("blobs")
	require.Error(t, err, "the refused objstore database is not served")
}

func TestStandaloneNode_SealedNodeWithObjectStorageStillRefuses(t *testing.T) {
	fake := &fakeObjectStorage{}
	mgr := standaloneObjstoreManager(t, true, WithObjectStorage(fake.open))
	_, err := mgr.Create(database.DatabaseConfig{Name: "blobs", Backend: database.BackendObjstore})
	var sealed *database.SealedNodeBackendError
	require.ErrorAsf(t, err, &sealed, "Create(blobs) = %v, want *database.SealedNodeBackendError", err)
	require.Empty(t, fake.opened, "a sealed node never reaches the opener")
}
