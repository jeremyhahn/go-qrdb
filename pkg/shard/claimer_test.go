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

package shard_test

import (
	"context"
	"errors"
	"math"
	"strconv"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

func newClaimer(t *testing.T, store shard.ClaimStore, seed shard.SeedFunc) *shard.Claimer {
	t.Helper()
	c, err := shard.NewClaimer(store, seed)
	if err != nil {
		t.Fatalf("NewClaimer: %v", err)
	}
	return c
}

func highWater(t *testing.T, store kvstore.KVStore) uint64 {
	t.Helper()
	raw, err := store.Get(context.Background(), shard.HighWaterKey)
	if err != nil || raw == nil {
		t.Fatalf("high-water mark: %q, %v", raw, err)
	}
	v, err := strconv.ParseUint(string(raw), 10, 64)
	if err != nil {
		t.Fatalf("high-water mark %q: %v", raw, err)
	}
	return v
}

func TestNewClaimer_RequiresStore(t *testing.T) {
	_, err := shard.NewClaimer(nil, nil)
	var cfg *shard.ClaimerConfigError
	if !errors.As(err, &cfg) || cfg.Field != "store" {
		t.Fatalf("NewClaimer(nil) = %v, want *ClaimerConfigError{store}", err)
	}
	if cfg.Error() == "" {
		t.Fatal("empty error text")
	}
}

func TestClaimShardID_FirstClaimStartsAtUserMin(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	c := newClaimer(t, store, nil)
	id, err := c.ClaimShardID(context.Background(), 0)
	if err != nil || id != shard.UserMin {
		t.Fatalf("first claim = %d, %v; want %d", id, err, shard.UserMin)
	}
	if hw := highWater(t, store); hw != shard.UserMin+1 {
		t.Fatalf("high-water after first claim = %d, want %d", hw, shard.UserMin+1)
	}
}

func TestClaimShardID_SeedCoversIDsAlreadyInUse(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	c := newClaimer(t, store, func(context.Context) (uint64, error) { return 1041, nil })
	id, err := c.ClaimShardID(context.Background(), 1003)
	if err != nil || id != 1042 {
		t.Fatalf("claim = %d, %v; want 1042, past the highest ID in use", id, err)
	}
	// The seed is consulted only to create the mark.
	id, err = c.ClaimShardID(context.Background(), 0)
	if err != nil || id != 1043 {
		t.Fatalf("second claim = %d, %v; want 1043", id, err)
	}
}

func TestClaimShardID_FloorWinsOverAnOlderMark(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	c := newClaimer(t, store, nil)
	if _, err := c.ClaimShardID(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	// A node whose own history is ahead of the mark never re-issues its IDs.
	id, err := c.ClaimShardID(context.Background(), 5000)
	if err != nil || id != 5000 {
		t.Fatalf("claim with floor 5000 = %d, %v; want 5000", id, err)
	}
	if hw := highWater(t, store); hw != 5001 {
		t.Fatalf("high-water = %d, want 5001", hw)
	}
}

func TestClaimShardID_ConcurrentClaimsAreUnique(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const nodes, perNode = 8, 25
	ids := make(chan uint64, nodes*perNode)
	errs := make(chan error, nodes*perNode)
	var wg sync.WaitGroup
	for range nodes {
		c := newClaimer(t, store, nil) // one claimer per node, one store
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perNode {
				id, err := c.ClaimShardID(context.Background(), 0)
				if err != nil {
					errs <- err
					return
				}
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		var contention *shard.ClaimContentionError
		if !errors.As(err, &contention) {
			t.Fatalf("claim: %v", err)
		}
	}
	seen := make(map[uint64]bool)
	for id := range ids {
		if seen[id] {
			t.Fatalf("shard ID %d issued twice", id)
		}
		seen[id] = true
	}
	if len(seen) == 0 {
		t.Fatal("no claim succeeded")
	}
}

// racingStore commits a competing claim between a claimer's read and its
// swap: the swap then genuinely loses, exactly as against another node.
type racingStore struct {
	*kvstore.MemoryKVStore
	races int
}

func (r *racingStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if r.races > 0 {
		r.races--
		cur, _ := strconv.ParseUint(string(expected), 10, 64)
		if _, err := r.MemoryKVStore.CompareAndSwap(ctx, key, expected, []byte(strconv.FormatUint(cur+1, 10))); err != nil {
			return false, err
		}
	}
	return r.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

func TestClaimShardID_RetriesALostRace(t *testing.T) {
	store := &racingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	c := newClaimer(t, store, nil)
	if _, err := c.ClaimShardID(context.Background(), 0); err != nil { // creates the mark: 1000
		t.Fatal(err)
	}
	store.races = 3
	id, err := c.ClaimShardID(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	// Three competing claims took 1001..1003.
	if id != 1004 {
		t.Fatalf("claim after three lost races = %d, want 1004", id)
	}
}

func TestClaimShardID_SustainedContentionIsTyped(t *testing.T) {
	store := &racingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	c := newClaimer(t, store, nil)
	if _, err := c.ClaimShardID(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	store.races = math.MaxInt
	_, err := c.ClaimShardID(context.Background(), 0)
	var contention *shard.ClaimContentionError
	if !errors.As(err, &contention) || contention.Attempts <= 0 {
		t.Fatalf("claim under sustained contention = %v, want *ClaimContentionError", err)
	}
	if contention.Error() == "" {
		t.Fatal("empty error text")
	}
}

// lostCreateStore loses the race to create the mark once: another node's
// PutIfAbsent lands first.
type lostCreateStore struct {
	*kvstore.MemoryKVStore
	lost bool
}

func (l *lostCreateStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if !l.lost {
		l.lost = true
		if _, err := l.MemoryKVStore.PutIfAbsent(ctx, key, []byte("2000")); err != nil {
			return false, err
		}
	}
	return l.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func TestClaimShardID_ClaimsFromAMarkAnotherNodeCreated(t *testing.T) {
	store := &lostCreateStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	c := newClaimer(t, store, nil)
	id, err := c.ClaimShardID(context.Background(), 0)
	if err != nil || id != 2000 {
		t.Fatalf("claim = %d, %v; want 2000 from the other node's mark", id, err)
	}
}

func TestClaimShardID_CorruptMarkRefuses(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	if err := store.Put(context.Background(), shard.HighWaterKey, []byte("not-a-number")); err != nil {
		t.Fatal(err)
	}
	_, err := newClaimer(t, store, nil).ClaimShardID(context.Background(), 0)
	var corrupt *shard.HighWaterCorruptError
	if !errors.As(err, &corrupt) || corrupt.Value != "not-a-number" {
		t.Fatalf("claim over a corrupt mark = %v, want *HighWaterCorruptError", err)
	}
	if corrupt.Error() == "" {
		t.Fatal("empty error text")
	}
}

func TestClaimShardID_ExhaustionIsTyped(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	if err := store.Put(context.Background(), shard.HighWaterKey, []byte(strconv.FormatUint(math.MaxUint64, 10))); err != nil {
		t.Fatal(err)
	}
	_, err := newClaimer(t, store, nil).ClaimShardID(context.Background(), 0)
	var exhausted *shard.ShardIDExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("claim at the end of the ID space = %v, want *ShardIDExhaustedError", err)
	}
	if exhausted.Error() == "" {
		t.Fatal("empty error text")
	}

	seeded := newClaimer(t, kvstore.NewMemoryKVStore(), func(context.Context) (uint64, error) { return math.MaxUint64, nil })
	if _, err := seeded.ClaimShardID(context.Background(), 0); !errors.As(err, &exhausted) {
		t.Fatalf("claim seeded at the end of the ID space = %v, want *ShardIDExhaustedError", err)
	}

	// The claim that would create the mark at the last ID cannot record what
	// follows it, so it refuses rather than issue an ID it cannot fence.
	if _, err := newClaimer(t, kvstore.NewMemoryKVStore(), nil).ClaimShardID(context.Background(), math.MaxUint64); !errors.As(err, &exhausted) {
		t.Fatalf("first claim at the end of the ID space = %v, want *ShardIDExhaustedError", err)
	}
}

// failingStore fails one operation with a store error.
type failingStore struct {
	*kvstore.MemoryKVStore
	failOp string
}

var errStore = errors.New("store unavailable")

func (f *failingStore) Get(ctx context.Context, key string) ([]byte, error) {
	if f.failOp == "read" {
		return nil, errStore
	}
	return f.MemoryKVStore.Get(ctx, key)
}

func (f *failingStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if f.failOp == "create" {
		return false, errStore
	}
	return f.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func (f *failingStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if f.failOp == "swap" {
		return false, errStore
	}
	return f.MemoryKVStore.CompareAndSwap(ctx, key, expected, next)
}

func TestClaimShardID_StoreFailuresAreTyped(t *testing.T) {
	for _, op := range []string{"read", "create", "swap"} {
		t.Run(op, func(t *testing.T) {
			store := &failingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
			if op == "swap" {
				if err := store.Put(context.Background(), shard.HighWaterKey, []byte("1000")); err != nil {
					t.Fatal(err)
				}
			}
			store.failOp = op
			_, err := newClaimer(t, store, nil).ClaimShardID(context.Background(), 0)
			var se *shard.ClaimStoreError
			if !errors.As(err, &se) || se.Op != op || !errors.Is(err, errStore) {
				t.Fatalf("claim with failing %s = %v, want *ClaimStoreError{%s} wrapping the store error", op, err, op)
			}
			if se.Error() == "" {
				t.Fatal("empty error text")
			}
		})
	}
}

func TestClaimShardID_SeedFailureIsTyped(t *testing.T) {
	c := newClaimer(t, kvstore.NewMemoryKVStore(), func(context.Context) (uint64, error) { return 0, errStore })
	_, err := c.ClaimShardID(context.Background(), 0)
	var se *shard.ClaimStoreError
	if !errors.As(err, &se) || se.Op != "seed" || !errors.Is(err, errStore) {
		t.Fatalf("claim with a failing seed = %v, want *ClaimStoreError{seed}", err)
	}
}

func TestClaimShardID_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newClaimer(t, kvstore.NewMemoryKVStore(), nil).ClaimShardID(ctx, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("claim on a cancelled context = %v, want context.Canceled", err)
	}
}
