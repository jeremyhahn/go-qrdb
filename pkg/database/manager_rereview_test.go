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
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// A database dropped through a manager with a reclaimer leaves a tombstone
// in its store. A later create of the name through a manager with NO
// reclaimer -- a restart that provisions configured databases before the
// data plane is up -- cannot mark a new incarnation live, so the database
// it created would refuse every write forever. It must be refused instead.
func TestManager_Create_WithoutAReclaimerRefusesADroppedName(t *testing.T) {
	dir := t.TempDir()
	mgr := NewManager(nil, dir)
	mgr.SetKeyspaceReclaimer(&spyReclaimer{})
	_, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	require.NoError(t, mgr.Drop(context.Background(), "orders"))
	require.NoError(t, mgr.Close())

	restarted := NewManager(nil, dir)
	t.Cleanup(func() { _ = restarted.Close() })
	require.NoError(t, restarted.OpenAll())
	_, err = restarted.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.Error(t, err, "a dropped name was created with no way to mark it live")
	var required *ReclaimerRequiredError
	require.ErrorAs(t, err, &required)
	assert.Contains(t, required.Error(), "orders")
	_, getErr := restarted.Get("orders")
	assert.Error(t, getErr, "the refused create must not leave a database behind")

	// Once the data plane is attached the create succeeds, and the record of
	// the drop is cleared with it.
	restarted.SetKeyspaceReclaimer(&spyReclaimer{})
	_, err = restarted.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
	dropped, err := restarted.droppedHere("orders")
	require.NoError(t, err)
	assert.False(t, dropped, "a re-created name is no longer a dropped one")
}

// A name never dropped here is created normally without a reclaimer.
func TestManager_Create_WithoutAReclaimerCreatesANeverDroppedName(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	_, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	require.NoError(t, err)
}

// DropRolledBack is what lets a caller decide whether fences of its own may
// be rolled back after a failed drop.
func TestDropRolledBack(t *testing.T) {
	boom := os.ErrClosed
	assert.True(t, DropRolledBack(&LocalFenceError{Name: "o", Err: boom}))
	assert.True(t, DropRolledBack(&KeyspaceFenceError{Name: "o", Err: boom, RolledBack: true}))
	assert.False(t, DropRolledBack(&KeyspaceFenceError{Name: "o", Err: boom}))
	assert.True(t, DropRolledBack(&KeyspaceReclaimError{Name: "o", Err: boom, RolledBack: true}))
	assert.False(t, DropRolledBack(&KeyspaceReclaimError{Name: "o", Err: boom}))
	assert.False(t, DropRolledBack(&DropError{Name: "o", Err: boom}))
	assert.False(t, DropRolledBack(boom))
	le := &LocalFenceError{Name: "o", Err: boom}
	assert.ErrorIs(t, le, boom)
	assert.Contains(t, le.Error(), "o")
}

// GetIgnoringFence reaches a database its own drop fence refuses to Get.
func TestManager_GetIgnoringFence(t *testing.T) {
	mgr, _ := newReclaimManager(t, "orders")
	db, err := mgr.Get("orders")
	require.NoError(t, err)
	db.SetDropping(true)

	var dropping *DroppingError
	_, err = mgr.Get("orders")
	require.ErrorAs(t, err, &dropping)
	got, err := mgr.GetIgnoringFence("orders")
	require.NoError(t, err)
	assert.Same(t, db, got)

	var notFound *NotFoundError
	_, err = mgr.GetIgnoringFence("missing")
	assert.ErrorAs(t, err, &notFound)
}

// A dedicated-raft database's shard ID must never repeat, across drops AND
// restarts: the shard stands in for the database's incarnation, so a
// re-created database that reused the dropped one's shard ID would be the
// same Raft group to every handle still pointing at it.
func TestManager_ShardIDsNeverRepeatAcrossDropAndRestart(t *testing.T) {
	dir := t.TempDir()
	raftCfg := func(name string) DatabaseConfig {
		return DatabaseConfig{Name: name, Engine: EngineKV, Backend: BackendMemory, Raft: &DatabaseRaftConfig{ClusterMode: "none"}}
	}

	replicated := kvstore.NewMemoryKVStore()
	mgr := NewManager(nil, dir)
	mgr.SetShardIDClaimer(claimerOver(t, replicated))
	first, err := mgr.Create(raftCfg("a"))
	require.NoError(t, err)
	top, err := mgr.Create(raftCfg("b"))
	require.NoError(t, err)
	require.Greater(t, top.ShardID(), first.ShardID())
	dropped := top.ShardID()
	require.NoError(t, mgr.Drop(context.Background(), "b"))
	require.NoError(t, mgr.Close())

	restarted := NewManager(nil, dir)
	restarted.SetShardIDClaimer(claimerOver(t, replicated))
	t.Cleanup(func() { _ = restarted.Close() })
	require.NoError(t, restarted.OpenAll())
	again, err := restarted.Create(raftCfg("b"))
	require.NoError(t, err)
	assert.NotEqual(t, dropped, again.ShardID(), "a dropped database's shard ID was issued again after a restart")
	assert.Greater(t, again.ShardID(), dropped)
}

// A create that fails leaves nothing of itself behind: not its catalog
// directory (meta.json included), and not an external data directory this
// call created.
func TestManager_Create_FailureRemovesWhatItCreated(t *testing.T) {
	failing := &failingBackendFactory{BackendFactory: NewEmbeddedBackendFactory()}
	root := t.TempDir()
	mgr := NewManager(nil, filepath.Join(root, "catalog"), WithBackendFactory(failing))
	t.Cleanup(func() { _ = mgr.Close() })
	external := filepath.Join(root, "external", "orders-data")
	failing.err = os.ErrClosed

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendFile, DataDir: external})
	require.Error(t, err)

	_, statErr := os.Stat(mgr.dbDir("orders"))
	assert.True(t, os.IsNotExist(statErr), "the failed create left its catalog directory (and meta.json) behind")
	_, statErr = os.Stat(external)
	assert.True(t, os.IsNotExist(statErr), "the failed create left the external data directory it created")
}

// An external data directory that existed before the create is the
// operator's, not the create's, and survives a failed create.
func TestManager_Create_FailureKeepsAPreexistingDataDir(t *testing.T) {
	failing := &failingBackendFactory{BackendFactory: NewEmbeddedBackendFactory(), err: os.ErrClosed}
	root := t.TempDir()
	mgr := NewManager(nil, filepath.Join(root, "catalog"), WithBackendFactory(failing))
	t.Cleanup(func() { _ = mgr.Close() })
	external := filepath.Join(root, "external", "orders-data")
	require.NoError(t, os.MkdirAll(external, 0o750))

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendFile, DataDir: external})
	require.Error(t, err)
	_, statErr := os.Stat(external)
	assert.NoError(t, statErr, "a pre-existing data directory must survive a failed create")
}

// Boot provisioning of a configured name this node dropped earlier clears
// the tombstone the drop left, marking the name live for the shared empty
// incarnation every node provisions it with.
func TestManager_Provision_ClearsTheTombstoneOfADroppedName(t *testing.T) {
	dir := t.TempDir()
	spy := &spyReclaimer{}
	mgr := NewManager(nil, dir)
	mgr.SetKeyspaceReclaimer(spy)
	cfg := DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory}
	_, err := mgr.Create(cfg)
	require.NoError(t, err)
	require.NoError(t, mgr.Drop(context.Background(), "orders"))
	require.NoError(t, mgr.Close())
	require.Equal(t, spyTombstone, spy.markerOf("/orders"))

	restarted := NewManager(nil, dir)
	t.Cleanup(func() { _ = restarted.Close() })
	require.NoError(t, restarted.OpenAll())
	restarted.SetKeyspaceReclaimer(spy)
	db, err := restarted.Provision(context.Background(), cfg)
	require.NoError(t, err)
	assert.Empty(t, db.Incarnation(), "a provisioned database carries the shared empty incarnation")
	assert.Empty(t, spy.markerOf("/orders"), "the tombstone must be cleared to absent, which is live for the empty incarnation")
	dropped, err := restarted.droppedHere("orders")
	require.NoError(t, err)
	assert.False(t, dropped)
}

// Provisioning is idempotent: a name already in the catalog -- even one a
// drop is fencing -- is returned as is, and one on disk but not yet loaded
// is opened rather than created.
func TestManager_Provision_ReturnsOrOpensAnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory}
	mgr := NewManager(nil, dir)
	created, err := mgr.Provision(context.Background(), cfg)
	require.NoError(t, err)
	again, err := mgr.Provision(context.Background(), cfg)
	require.NoError(t, err)
	assert.Same(t, created, again)
	created.SetDropping(true)
	fenced, err := mgr.Provision(context.Background(), cfg)
	require.NoError(t, err, "a database a drop is fencing must not fail boot")
	assert.Same(t, created, fenced)
	created.SetDropping(false)
	require.NoError(t, mgr.Close())

	reopened := NewManager(nil, dir)
	t.Cleanup(func() { _ = reopened.Close() })
	db, err := reopened.Provision(context.Background(), cfg)
	require.NoError(t, err)
	assert.Equal(t, "orders", db.Name())
	got, err := reopened.Get("orders")
	require.NoError(t, err)
	assert.Same(t, db, got)
}

// A configured name some other node created through the API holds that
// database's live incarnation in the replicated marker. Provisioning it
// here must not take the marker over; it is refused as already existing
// elsewhere and leaves nothing behind.
func TestManager_Provision_RefusesANameLiveElsewhere(t *testing.T) {
	spy := &spyReclaimer{marker: map[string]string{"/orders": "incarnation-on-node-b"}}
	mgr := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	mgr.SetKeyspaceReclaimer(spy)

	_, err := mgr.Provision(context.Background(), DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	var exists *AlreadyExistsError
	require.ErrorAs(t, err, &exists)
	assert.True(t, exists.Elsewhere)
	assert.Contains(t, exists.Error(), "orders")
	assert.Equal(t, "incarnation-on-node-b", spy.markerOf("/orders"))
	_, getErr := mgr.Get("orders")
	assert.Error(t, getErr)
	_, statErr := os.Stat(mgr.dbDir("orders"))
	assert.True(t, os.IsNotExist(statErr), "the refused provisioning left its catalog directory behind")
}

// Create through the API returns the elsewhere refusal unwrapped too, so
// every transport classifies it as an already-exists conflict.
func TestManager_Create_RefusesANameLiveElsewhere(t *testing.T) {
	spy := &spyReclaimer{marker: map[string]string{"/orders": "incarnation-on-node-b"}}
	mgr := NewManager(nil, t.TempDir())
	t.Cleanup(func() { _ = mgr.Close() })
	mgr.SetKeyspaceReclaimer(spy)

	_, err := mgr.Create(DatabaseConfig{Name: "orders", Engine: EngineKV, Backend: BackendMemory})
	exists, ok := err.(*AlreadyExistsError)
	require.True(t, ok, "want *AlreadyExistsError unwrapped, got %T", err)
	assert.True(t, exists.Elsewhere)
	assert.NotEqual(t, (&AlreadyExistsError{Name: "orders"}).Error(), exists.Error())
}
