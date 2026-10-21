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

package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/clustermig"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// plainKV is a store with the KVStore surface and none of the atomic verbs: a
// replicated store that cannot compare-and-set.
type plainKV struct{ kvstore.KVStore }

func TestNewShardClaimer_RefusesAStoreWithoutAtomicVerbs(t *testing.T) {
	_, err := newShardClaimer(plainKV{KVStore: kvstore.NewMemoryKVStore()}, nil)
	var claimerErr *ShardClaimerError
	if !errors.As(err, &claimerErr) || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("newShardClaimer over a non-atomic store = %v, want *ShardClaimerError wrapping ErrAtomicWriteUnsupported", err)
	}
	if claimerErr.Error() == "" {
		t.Fatal("empty error text")
	}
}

func TestNewShardClaimer_ClaimsOverTheReplicatedStore(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	claimer, err := newShardClaimer(store, nil)
	if err != nil {
		t.Fatalf("newShardClaimer: %v", err)
	}
	id, err := claimer.ClaimShardID(context.Background())
	if err != nil || id != shard.UserMin {
		t.Fatalf("first claim = %d, %v; want %d", id, err, shard.UserMin)
	}
	if raw, _ := store.Get(context.Background(), shard.HighWaterKey); raw == nil {
		t.Fatal("the claim was not recorded in the store it was made over")
	}
}

func TestNewClusterMigStore_RefusesAStoreWithoutAtomicVerbs(t *testing.T) {
	s, err := newClusterMigStore(plainKV{KVStore: kvstore.NewMemoryKVStore()})
	var migErr *ClusterMigStoreError
	if s != nil || !errors.As(err, &migErr) || !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
		t.Fatalf("newClusterMigStore over a non-atomic store = %v, %v; want nil, *ClusterMigStoreError wrapping ErrAtomicWriteUnsupported", s, err)
	}
	if !strings.Contains(migErr.Error(), "cluster migration store") || !strings.Contains(migErr.Error(), kvstore.ErrAtomicWriteUnsupported.Error()) {
		t.Fatalf("unexpected message: %q", migErr.Error())
	}
}

func TestNewClusterMigStore_WritesOverTheReplicatedStore(t *testing.T) {
	ctx := context.Background()
	store := kvstore.NewMemoryKVStore()
	s, err := newClusterMigStore(store)
	if err != nil {
		t.Fatalf("newClusterMigStore: %v", err)
	}
	created, err := s.CompareAndSwapMigration(ctx, clustermig.MigrationVersion{}, &clustermig.ClusterMigration{
		TenantID: "t", Database: "d", Target: seal.StrategyPassphrase, Generation: 1,
	})
	if err != nil || !created {
		t.Fatalf("CompareAndSwapMigration(create) = %v, %v; want true, nil", created, err)
	}
	if m, err := s.GetMigration(ctx, "t", "d"); err != nil || m == nil || m.Generation != 1 {
		t.Fatalf("GetMigration = %+v, %v; want generation 1", m, err)
	}
}
