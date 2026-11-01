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

package node_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/node"
)

// These tests are go-qrdb#1196's verification. They live in the external
// node_test package and name nothing but sdk/go and sdk/go/node, so they are
// the guarded multi-key commit exactly as an embedder outside this module
// reaches it: a standalone node, its Client's KVStore, and the sdk re-exports
// MultiGuardedBatchKVStore, KVGuard and KVWrite.

// guardedStandaloneStore boots a standalone node with no protocol listeners
// on backend and returns its Client's store as a MultiGuardedBatchKVStore.
func guardedStandaloneStore(t *testing.T, backend sdk.BackendType) sdk.MultiGuardedBatchKVStore {
	t.Helper()
	cfg := sdk.DefaultDaemonConfig()
	cfg.Auth.Enabled = sdk.BoolPtr(false) // not an authorization test
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []sdk.DaemonDatabaseConfig{{Name: "default", Backend: string(backend)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST = false
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false

	n, err := node.NewStandaloneNode(context.Background(), cfg, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	guarded, ok := n.Client().KVStore().(sdk.MultiGuardedBatchKVStore)
	require.True(t, ok, "a standalone node's KVStore must offer the guarded multi-key commit")
	return guarded
}

// TestStandaloneNode_GuardedMultiKeyCommit_RaceHasOneWinner races two writers
// on the same catalog entry, each committing the entry and its own index key
// guarded on the entry's current value. Whatever the interleaving, exactly
// one commits ALL of its keys, and the other is refused with nothing written.
func TestStandaloneNode_GuardedMultiKeyCommit_RaceHasOneWinner(t *testing.T) {
	for _, backend := range []sdk.BackendType{sdk.BackendPebble, sdk.BackendMemory} {
		t.Run(string(backend), func(t *testing.T) {
			store := guardedStandaloneStore(t, backend)
			ctx := context.Background()

			const rounds = 32
			for round := range rounds {
				entry := fmt.Sprintf("catalog/entry/%d", round)
				seed := []byte("v0")
				require.NoError(t, store.Put(ctx, entry, seed))

				var (
					start   = make(chan struct{})
					wg      sync.WaitGroup
					applied [2]bool
					errs    [2]error
				)
				for w := range 2 {
					wg.Go(func() {
						<-start
						applied[w], errs[w] = store.PutAllIfAllUnchanged(ctx,
							[]sdk.KVGuard{{Key: entry, Value: seed}},
							[]sdk.KVWrite{
								{Key: entry, Value: fmt.Appendf(nil, "writer-%d", w)},
								{Key: fmt.Sprintf("catalog/index/%d/%d", round, w), Value: []byte(entry)},
							})
					})
				}
				close(start)
				wg.Wait()

				require.NoError(t, errs[0])
				require.NoError(t, errs[1])
				require.NotEqual(t, applied[0], applied[1], "round %d: exactly one writer must commit", round)
				winner, loser := 0, 1
				if applied[1] {
					winner, loser = 1, 0
				}

				got, err := store.Get(ctx, entry)
				require.NoError(t, err)
				require.Equal(t, fmt.Sprintf("writer-%d", winner), string(got))

				idx, err := store.Get(ctx, fmt.Sprintf("catalog/index/%d/%d", round, winner))
				require.NoError(t, err)
				require.Equal(t, entry, string(idx))

				lost, err := store.Get(ctx, fmt.Sprintf("catalog/index/%d/%d", round, loser))
				require.NoError(t, err)
				require.Nil(t, lost, "round %d: the refused writer's index key must not be written", round)
			}
		})
	}
}

// TestStandaloneNode_GuardedMultiKeyCommit_EveryGuardMustHold proves the
// batch is conditional on ALL of its guards: one guard holding and one not
// leaves every key of the batch unwritten, and the same batch commits once
// both hold.
func TestStandaloneNode_GuardedMultiKeyCommit_EveryGuardMustHold(t *testing.T) {
	store := guardedStandaloneStore(t, sdk.BackendPebble)
	ctx := context.Background()

	require.NoError(t, store.Put(ctx, "catalog/entry/a", []byte("a1")))
	require.NoError(t, store.Put(ctx, "catalog/entry/b", []byte("b2")))

	guards := []sdk.KVGuard{
		{Key: "catalog/entry/a", Value: []byte("a1")},
		{Key: "catalog/entry/b", Value: []byte("b1")}, // stale
		{Key: "catalog/index/new", Value: nil},        // must be absent
	}
	writes := []sdk.KVWrite{
		{Key: "catalog/entry/a", Value: []byte("a2")},
		{Key: "catalog/entry/b", Delete: true},
		{Key: "catalog/index/new", Value: []byte("a")},
	}

	applied, err := store.PutAllIfAllUnchanged(ctx, guards, writes)
	require.NoError(t, err)
	require.False(t, applied, "a stale guard must refuse the whole batch")

	a, err := store.Get(ctx, "catalog/entry/a")
	require.NoError(t, err)
	require.Equal(t, "a1", string(a))
	b, err := store.Get(ctx, "catalog/entry/b")
	require.NoError(t, err)
	require.Equal(t, "b2", string(b))
	idx, err := store.Get(ctx, "catalog/index/new")
	require.NoError(t, err)
	require.Nil(t, idx)

	guards[1].Value = []byte("b2")
	applied, err = store.PutAllIfAllUnchanged(ctx, guards, writes)
	require.NoError(t, err)
	require.True(t, applied)

	a, err = store.Get(ctx, "catalog/entry/a")
	require.NoError(t, err)
	require.Equal(t, "a2", string(a))
	b, err = store.Get(ctx, "catalog/entry/b")
	require.NoError(t, err)
	require.Nil(t, b)
	idx, err = store.Get(ctx, "catalog/index/new")
	require.NoError(t, err)
	require.Equal(t, "a", string(idx))
}
