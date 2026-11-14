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
			var invalid *database.InvalidBackendError
			require.ErrorAsf(t, err, &invalid, "Create(%s) = %v, want *database.InvalidBackendError", dbCfg.Name, err)
		} else {
			var shared *database.SharedStoreBarrierUnsupportedError
			require.ErrorAsf(t, err, &shared, "Create(%s) = %v, want *database.SharedStoreBarrierUnsupportedError", dbCfg.Name, err)
		}
		_, err = mgr.Get(dbCfg.Name)
		require.Errorf(t, err, "the refused %s database is not served", dbCfg.Name)
	}
}
