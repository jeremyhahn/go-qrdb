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
