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

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// Drops of one database overlapping across nodes: drop A fenced and failed,
// drop B took its claim over and re-fenced, and A's rollback reaches the
// store only then. The rollback must be refused by the store, where the two
// drops' writes are ordered, and leave B's fence -- and B's reclamation --
// intact (go-qrdb#893).
func TestKeyspaceReclaim_RollbackAfterTakeoverKeepsTheTakeoversFence(t *testing.T) {
	ctx := context.Background()
	ns := tenant.ComposeNamespace("acme", "reports")
	kv := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	key := DropFenceKey("acme", "reports")

	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", ""), "the create")
	require.NoError(t, kv.Put(ctx, ns+"k", []byte("v")))
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"), "drop A fences")
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-b"), "drop B takes it over")

	var superseded *database.DropSupersededError
	require.ErrorAs(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"), &superseded,
		"drop A's rollback must not clear the fence drop B is reclaiming under")
	marker, err := kv.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, dropFenceTombstoneFor("claim-b"), marker)

	require.ErrorAs(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"), &superseded,
		"a drop taken over deletes nothing")
	_, err = kv.Get(ctx, ns+"k")
	require.NoError(t, err)
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-b"), "drop B finishes")
	_, err = kv.Get(ctx, ns+"k")
	var notFound *KeyNotFoundError
	assert.ErrorAs(t, err, &notFound)
}

// The other order: drop A's rollback lands first, then drop B fences. B's
// fence is a takeover of a live marker and lands; A's rollback is idempotent
// for A's own claim and no longer clears anything once B has fenced.
func TestKeyspaceReclaim_TakeoverAfterRollbackFencesAgain(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})
	key := DropFenceKey("acme", "reports")

	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", ""))
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"))
	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"), "drop A rolls back")
	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"), "a retried rollback is idempotent")
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-b"), "drop B fences")

	var superseded *database.DropSupersededError
	require.ErrorAs(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"), &superseded)
	marker, err := kv.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, dropFenceTombstoneFor("claim-b"), marker)
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-b"), "a retried fence is idempotent")
}

// A dedicated-raft database's rollback clears two markers, the one in its own
// shard and the one in the store its keyspace is reclaimed from. A takeover
// re-stamps both, and the rollback clears neither.
func TestKeyspaceReclaim_DedicatedRollbackAfterTakeoverKeepsBothFences(t *testing.T) {
	ctx := context.Background()
	system := newMemoryKVSvc()
	shard := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: system})
	svc.SetStoreResolver(&dedicatedRaftResolver{shard: shard})
	key := DropFenceKey("acme", "reports")

	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", ""))
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"))
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-b"))

	var superseded *database.DropSupersededError
	require.ErrorAs(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"), &superseded)
	for name, kv := range map[string]*memoryKVSvc{"system": system, "shard": shard} {
		marker, err := kv.Get(ctx, key)
		require.NoError(t, err, name)
		assert.Equal(t, dropFenceTombstoneFor("claim-b"), marker, name)
	}

	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-b"), "drop B may roll back")
	_, err := shard.Get(ctx, key)
	var notFound *KeyNotFoundError
	assert.ErrorAs(t, err, &notFound, "the shard is live while its marker is absent")
	marker, err := system.Get(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, "inc-a", string(marker))
}

// A create claims the marker out of any drop's tombstone: the catalog, not
// the store, refuses a create while a drop is in flight.
func TestKeyspaceReclaim_CreateClaimsAnyDropsTombstone(t *testing.T) {
	ctx := context.Background()
	kv := newMemoryKVSvc()
	svc := NewKeyspaceReclaimService(ReclaimTarget{KV: kv})

	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", ""))
	require.NoError(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"))
	require.NoError(t, svc.ReclaimDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-a"))
	require.NoError(t, svc.UnfenceDatabaseKeyspace(ctx, "acme", "reports", "inc-b", ""), "the successor's create")
	marker, err := kv.Get(ctx, DropFenceKey("acme", "reports"))
	require.NoError(t, err)
	assert.Equal(t, "inc-b", string(marker))

	var superseded *database.DropSupersededError
	require.ErrorAs(t, svc.FenceDatabaseKeyspace(ctx, "acme", "reports", "inc-a", "claim-c"), &superseded,
		"a fence of the dropped incarnation must not tombstone the successor")
}

func TestDropFenceTombstoneFor(t *testing.T) {
	assert.Equal(t, dropFenceTombstone, dropFenceTombstoneFor(""))
	stamped := dropFenceTombstoneFor("claim-a")
	assert.True(t, isDropFenceTombstone(stamped))
	assert.NotEqual(t, stamped, dropFenceTombstoneFor("claim-b"))
	assert.False(t, isDropFenceTombstone([]byte("inc-a")))
	assert.False(t, isDropFenceTombstone(nil))
}
