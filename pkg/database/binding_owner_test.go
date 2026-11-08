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

package database

import (
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/placement"
)

const (
	ownerNodeA = "10.0.0.1:7001"
	ownerNodeB = "10.0.0.2:7001"
)

// ownedPair returns two managers sharing one catalog, as two nodes of one
// cluster do: a known as ownerNodeA, b as ownerNodeB, each with its own data
// directory, the second of which is returned.
func ownedPair(t *testing.T) (a, b *Manager, cat *hookCatalog, dirB string) {
	t.Helper()
	cat = newHookCatalog(t)
	a = NewManager(nil, t.TempDir())
	a.SetLocalNode(ownerNodeA)
	a.SetCatalog(cat)
	dirB = t.TempDir()
	b = NewManager(nil, dirB)
	b.SetLocalNode(ownerNodeB)
	b.SetCatalog(cat)
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b, cat, dirB
}

func fileConfig(name string) DatabaseConfig {
	return DatabaseConfig{Name: name, Backend: BackendFile}
}

// requireEmptyDir fails when dir holds anything: nothing was opened there.
func requireEmptyDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Empty(t, entries, "a local store was created under %s", dir)
}

func TestManager_Create_RecordsCreatingNodeAsOwner(t *testing.T) {
	a, _, cat, _ := ownedPair(t)
	ctx := tenantContext("tenant-a")

	db, err := a.CreateForTenant(ctx, fileConfig("orders"))
	require.NoError(t, err)
	assert.Equal(t, ownerNodeA, db.Info().OwnerNode)

	meta, err := cat.Get(ctx, "tenant-a", "orders")
	require.NoError(t, err)
	assert.Equal(t, ownerNodeA, meta.OwnerNode)
}

func TestManager_Create_StandaloneRecordsNoOwner(t *testing.T) {
	cat := newHookCatalog(t)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetCatalog(cat)
	defer func() { _ = mgr.Close() }()
	ctx := tenantContext("tenant-a")

	_, err := mgr.CreateForTenant(ctx, fileConfig("orders"))
	require.NoError(t, err)
	meta, err := cat.Get(ctx, "tenant-a", "orders")
	require.NoError(t, err)
	assert.Empty(t, meta.OwnerNode)
}

func TestManager_GetOwned_OtherNodeRefusesToOwnerAndOpensNothing(t *testing.T) {
	a, b, _, dirB := ownedPair(t)
	_, err := a.CreateForTenant(tenantContext("tenant-a"), fileConfig("orders"))
	require.NoError(t, err)

	_, err = b.GetOwned("orders", "tenant-a")
	var refusal *placement.NotAReplicaError
	require.ErrorAs(t, err, &refusal)
	assert.True(t, refusal.LocalBinding())
	assert.Equal(t, "tenant-a", refusal.TenantID)
	assert.Equal(t, "orders", refusal.Database)
	assert.Equal(t, ownerNodeB, refusal.NodeID)
	require.Len(t, refusal.Replicas, 1)
	assert.Equal(t, ownerNodeA, refusal.Replicas[0].NodeID)
	assert.Equal(t, ownerNodeA, refusal.Replicas[0].RaftAddr)
	requireEmptyDir(t, dirB)
}

func TestManager_GetOwned_OwnerOpensItsOwnRecord(t *testing.T) {
	a, _, _, _ := ownedPair(t)
	_, err := a.CreateForTenant(tenantContext("tenant-a"), fileConfig("orders"))
	require.NoError(t, err)

	db, err := a.GetOwned("orders", "tenant-a")
	require.NoError(t, err)
	assert.Equal(t, ownerNodeA, db.Info().OwnerNode)
}

func TestManager_Provision_RecordsNoOwnerAndOpensOnEveryNode(t *testing.T) {
	a, b, cat, _ := ownedPair(t)
	ctx := tenantContext("tenant-a")
	cfg := fileConfig("defaults")
	cfg.TenantID = "tenant-a"
	_, err := a.Provision(ctx, cfg)
	require.NoError(t, err)

	meta, err := cat.Get(ctx, "tenant-a", "defaults")
	require.NoError(t, err)
	assert.Empty(t, meta.OwnerNode)
	db, err := b.Provision(ctx, cfg)
	require.NoError(t, err)
	assert.Empty(t, db.Info().OwnerNode)
}

func TestManager_OwnerFor_RaftAndProvisionedHaveNoOwner(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	mgr.SetLocalNode(ownerNodeA)
	defer func() { _ = mgr.Close() }()

	assert.Empty(t, mgr.ownerFor(DatabaseConfig{Backend: BackendRaft}, false))
	assert.Empty(t, mgr.ownerFor(fileConfig("x"), true))
	assert.Empty(t, mgr.ownerFor(DatabaseConfig{Backend: BackendPebble, Raft: validRaftConfig(0)}, false))
	assert.Equal(t, ownerNodeA, mgr.ownerFor(fileConfig("x"), false))
	assert.Equal(t, ownerNodeA, mgr.ownerFor(DatabaseConfig{Backend: BackendPebble}, false))
}

func TestManager_OwnerRefusal_RecordWithoutOwnerOpensAnywhere(t *testing.T) {
	mgr := NewManager(nil, t.TempDir())
	mgr.SetLocalNode(ownerNodeB)
	defer func() { _ = mgr.Close() }()
	key := dbKey{tenant: "tenant-a", name: "raftdb"}

	_, held := mgr.ownerRefusal(key, DatabaseMeta{Backend: BackendRaft})
	assert.False(t, held)
	_, held = mgr.ownerRefusal(key, DatabaseMeta{OwnerNode: ownerNodeB})
	assert.False(t, held)
	refusal, held := mgr.ownerRefusal(key, DatabaseMeta{OwnerNode: ownerNodeA})
	require.True(t, held)
	assert.Equal(t, ownerNodeA, refusal.Replicas[0].NodeID)
}

func TestManager_ListForTenant_OtherNodeListsRemoteWithoutOpening(t *testing.T) {
	a, b, _, dirB := ownedPair(t)
	ctx := tenantContext("tenant-a")
	_, err := a.CreateForTenant(ctx, fileConfig("orders"))
	require.NoError(t, err)

	infos, err := b.ListForTenant(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.Equal(t, "orders", infos[0].Name)
	assert.Equal(t, StatusRemote, infos[0].Status)
	assert.Equal(t, ownerNodeA, infos[0].OwnerNode)
	requireEmptyDir(t, dirB)

	infos, err = a.ListForTenant(ctx)
	require.NoError(t, err)
	require.Len(t, infos, 1)
	assert.NotEqual(t, StatusRemote, infos[0].Status)
}

func TestManager_OpenAll_OtherNodeSkipsRecordsHeldElsewhere(t *testing.T) {
	a, b, _, dirB := ownedPair(t)
	_, err := a.CreateForTenant(tenantContext("tenant-a"), fileConfig("orders"))
	require.NoError(t, err)

	require.NoError(t, b.OpenAll(t.Context()))
	assert.Empty(t, b.List())
	requireEmptyDir(t, dirB)
}

func TestRemoteInfo_DroppingRecordReportsDropping(t *testing.T) {
	info := remoteInfo(DatabaseMeta{Name: "orders", OwnerNode: ownerNodeA, Dropping: true,
		Raft: &DatabaseRaftConfig{ShardID: 9}})
	assert.Equal(t, StatusDropping, info.Status)
	assert.Equal(t, uint64(9), info.ShardID)
	assert.Equal(t, StatusRemote, remoteInfo(DatabaseMeta{Name: "orders"}).Status)
}

func TestBindingOwnerUnavailableError_NamesOwnerAndUnwraps(t *testing.T) {
	cause := errors.New("connection refused")
	err := &BindingOwnerUnavailableError{TenantID: "t", Name: "orders", Owner: ownerNodeA, Err: cause}
	assert.Contains(t, err.Error(), `"orders"`)
	assert.Contains(t, err.Error(), ownerNodeA)
	assert.Contains(t, err.Error(), "connection refused")
	assert.ErrorIs(t, err, cause)

	bare := &BindingOwnerUnavailableError{Name: "orders", Owner: ownerNodeA}
	assert.NotContains(t, bare.Error(), ": <nil>")
	assert.NoError(t, bare.Unwrap())
}
