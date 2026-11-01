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

package database

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestManager_CreateReleasesAnEntryDroppedThroughAnotherNode proves a create
// does not take an entry this node still holds for a name another node
// dropped as a duplicate: the catalog no longer records it, so the entry is
// released and the name created again, with no lookup in between.
func TestManager_CreateReleasesAnEntryDroppedThroughAnotherNode(t *testing.T) {
	_, a, b := newTwoNodes(t)
	ctx := tenantContext("tenant-a")
	_, err := a.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err)
	old, err := b.GetForTenant(ctx, "orders")
	require.NoError(t, err)
	require.NoError(t, a.DropForTenant(ctx, "orders"))

	created, err := b.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err, "B refused a name the catalog no longer records")
	assert.NotSame(t, old, created)
	local, ok := b.Local("orders", "tenant-a")
	require.True(t, ok)
	assert.Same(t, created, local)
}

// TestManager_CreateRefusesANameTheCatalogStillRecords proves the
// reconciliation keeps refusing a name whose entry the catalog confirms.
func TestManager_CreateRefusesANameTheCatalogStillRecords(t *testing.T) {
	_, a, b := newTwoNodes(t)
	ctx := tenantContext("tenant-a")
	_, err := a.CreateForTenant(ctx, validMemoryConfig("orders"))
	require.NoError(t, err)
	held, err := b.GetForTenant(ctx, "orders")
	require.NoError(t, err)

	_, err = b.CreateForTenant(ctx, validMemoryConfig("orders"))
	var exists *AlreadyExistsError
	require.ErrorAs(t, err, &exists)
	local, ok := b.Local("orders", "tenant-a")
	require.True(t, ok)
	assert.Same(t, held, local)
}

// TestManager_CreateFailsClosedWhenTheCatalogCannotConfirmAnEntry proves a
// create whose release of an obsolete entry cannot re-read the catalog
// returns that failure and keeps the entry, rather than creating over it.
func TestManager_CreateFailsClosedWhenTheCatalogCannotConfirmAnEntry(t *testing.T) {
	mgr, cat, old := newStaleEntry(t)
	readErr := errors.New("catalog unreachable")
	cat.err = readErr

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Backend: BackendMemory})
	require.ErrorIs(t, err, readErr)
	local, ok := mgr.Local("orders", "")
	require.True(t, ok)
	assert.Same(t, old, local)
}

// writeLegacyRecord writes a meta.json, the record a version before the
// catalog kept, into dir.
func writeLegacyRecord(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, legacyMetaFile)
	require.NoError(t, os.WriteFile(path, []byte(`{"name":"orders","backend":"memory"}`), 0o600))
	return path
}

// TestManager_OpenAllRefusesRecordsThatPredateTheCatalog proves a node whose
// data directory still holds pre-catalog records, for the system tenant or
// any other, refuses to open rather than starting with those databases
// unlisted, and names every record.
func TestManager_OpenAllRefusesRecordsThatPredateTheCatalog(t *testing.T) {
	mgr := newTestManager(t)
	t.Cleanup(func() { _ = mgr.Close() })
	root := filepath.Join(mgr.DataDir(), databasesDir)
	system := writeLegacyRecord(t, tenantScopedPath(root, "", "orders"))
	tenanted := writeLegacyRecord(t, tenantScopedPath(root, "tenant-a", "invoices"))

	err := mgr.OpenAll(t.Context())
	var unmigrated *UnmigratedDatabasesError
	require.ErrorAs(t, err, &unmigrated)
	assert.ElementsMatch(t, []string{system, tenanted}, unmigrated.Records)
	assert.Contains(t, err.Error(), "2 database record(s) predate the catalog")
}

// TestManager_OpenAllOpensWithDatabaseDirectoriesButNoLegacyRecords proves the
// refusal is about the old record alone: a database directory holding data
// but no meta.json, and a data directory with no databases at all, open.
func TestManager_OpenAllOpensWithDatabaseDirectoriesButNoLegacyRecords(t *testing.T) {
	empty := newTestManager(t)
	t.Cleanup(func() { _ = empty.Close() })
	require.NoError(t, empty.OpenAll(t.Context()))

	mgr := newTestManager(t)
	t.Cleanup(func() { _ = mgr.Close() })
	root := filepath.Join(mgr.DataDir(), databasesDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tenantScopedPath(root, "", "orders"), dataSubDir), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tenantScopedPath(root, "tenant-a", "invoices"), dataSubDir), 0o700))
	require.NoError(t, mgr.OpenAll(t.Context()))
}

// TestManager_OpenAllFailsWhenADatabaseDirectoryCannotBeRead proves a scan
// that cannot read the databases directory fails the open instead of
// reporting no records.
func TestManager_OpenAllFailsWhenADatabaseDirectoryCannotBeRead(t *testing.T) {
	mgr := newTestManager(t)
	t.Cleanup(func() { _ = mgr.Close() })
	// A file where the databases directory belongs cannot be listed.
	require.NoError(t, os.MkdirAll(mgr.DataDir(), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mgr.DataDir(), databasesDir), nil, 0o600))

	var openErr *OpenError
	require.ErrorAs(t, mgr.OpenAll(t.Context()), &openErr)
}
