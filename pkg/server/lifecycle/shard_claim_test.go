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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

// newPlacementStore is the real placement store over an in-memory KV, the
// same type the lifecycle builds over the replicated system store.
func newPlacementStore(t *testing.T) *placement.Store {
	t.Helper()
	store, err := placement.NewStore(kvstore.NewMemoryKVStore())
	if err != nil {
		t.Fatalf("placement.NewStore: %v", err)
	}
	return store
}

func TestPlacementShardSeed_HighestPlacedShard(t *testing.T) {
	store := newPlacementStore(t)
	for i, id := range []uint64{1004, 1311, 1002} {
		db := []string{"a", "b", "c"}[i]
		if err := store.Put(context.Background(), &placement.Placement{TenantID: "acme", Database: db, ShardID: id}); err != nil {
			t.Fatalf("put placement: %v", err)
		}
	}
	got, err := placementShardSeed(store)(context.Background())
	if err != nil || got != 1311 {
		t.Fatalf("seed = %d, %v; want 1311", got, err)
	}
}

func TestPlacementShardSeed_EmptyTableSeedsNothing(t *testing.T) {
	got, err := placementShardSeed(newPlacementStore(t))(context.Background())
	if err != nil || got != 0 {
		t.Fatalf("seed over an empty table = %d, %v; want 0", got, err)
	}
}

func TestPlacementShardSeed_NoStoreNoSeed(t *testing.T) {
	if placementShardSeed(nil) != nil {
		t.Fatal("a composition with no placement table must not get a seed")
	}
}

type failingLister struct{ err error }

func (f failingLister) List(context.Context) ([]*placement.Placement, error) { return nil, f.err }

func TestPlacementShardSeed_ListFailureIsReported(t *testing.T) {
	down := errors.New("placement unreadable")
	if _, err := seedFromPlacements(failingLister{err: down})(context.Background()); !errors.Is(err, down) {
		t.Fatalf("seed with an unreadable table = %v, want the list error", err)
	}
}

// TestPlacementShardSeed_ClaimStartsPastPlacedShards wires the seed into a
// real claimer: the first claim on a cluster that already placed shard 1311
// issues 1312, never an ID a node handed out before claims existed.
func TestPlacementShardSeed_ClaimStartsPastPlacedShards(t *testing.T) {
	placements := newPlacementStore(t)
	if err := placements.Put(context.Background(), &placement.Placement{TenantID: "acme", Database: "a", ShardID: 1311}); err != nil {
		t.Fatal(err)
	}
	claimer, err := shard.NewClaimer(kvstore.NewMemoryKVStore(), placementShardSeed(placements))
	if err != nil {
		t.Fatal(err)
	}
	id, err := claimer.ClaimShardID(context.Background(), shard.UserMin)
	if err != nil || id != 1312 {
		t.Fatalf("first claim = %d, %v; want 1312", id, err)
	}
}

func TestShardClaimerError(t *testing.T) {
	err := &ShardClaimerError{Err: kvstore.ErrAtomicWriteUnsupported}
	if !errors.Is(err, kvstore.ErrAtomicWriteUnsupported) || err.Error() == "" {
		t.Fatalf("ShardClaimerError = %v", err)
	}
}
