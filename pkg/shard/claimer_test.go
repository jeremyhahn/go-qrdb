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
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/shard"
)

func newClaimer(t *testing.T, store shard.ClaimStore) *shard.Claimer {
	t.Helper()
	c, err := shard.NewClaimer(store)
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

func setMark(t *testing.T, store kvstore.KVStore, v string) {
	t.Helper()
	if err := store.Put(context.Background(), shard.HighWaterKey, []byte(v)); err != nil {
		t.Fatal(err)
	}
}

func TestNewClaimer_RequiresStore(t *testing.T) {
	_, err := shard.NewClaimer(nil)
	var cfg *shard.ClaimerConfigError
	if !errors.As(err, &cfg) || cfg.Field != "store" {
		t.Fatalf("NewClaimer(nil) = %v, want *ClaimerConfigError{store}", err)
	}
	if cfg.Error() == "" {
		t.Fatal("empty error text")
	}
}

func TestClaimShardID_FirstClaimIsUserMin(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	c := newClaimer(t, store)
	id, err := c.ClaimShardID(context.Background())
	if err != nil || id != shard.UserMin {
		t.Fatalf("first claim = %d, %v; want %d", id, err, shard.UserMin)
	}
	if hw := highWater(t, store); hw != shard.UserMin+1 {
		t.Fatalf("high-water after first claim = %d, want %d", hw, shard.UserMin+1)
	}
}

func TestClaimShardID_ClaimsAreSequential(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	c := newClaimer(t, store)
	for want := shard.UserMin; want < shard.UserMin+5; want++ {
		id, err := c.ClaimShardID(context.Background())
		if err != nil || id != want {
			t.Fatalf("claim = %d, %v; want %d", id, err, want)
		}
	}
}

func TestClaimShardID_ConcurrentClaimsAreUnique(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	const nodes, perNode = 8, 25
	ids := make(chan uint64, nodes*perNode)
	errs := make(chan error, nodes*perNode)
	var wg sync.WaitGroup
	for range nodes {
		c := newClaimer(t, store) // one claimer per node, one store
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range perNode {
				id, err := c.ClaimShardID(context.Background())
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
		if id < shard.UserMin {
			t.Fatalf("claim %d is below UserMin", id)
		}
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
	c := newClaimer(t, store)
	if _, err := c.ClaimShardID(context.Background()); err != nil { // creates the mark: 1000
		t.Fatal(err)
	}
	store.races = 3
	id, err := c.ClaimShardID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Three competing claims took 1001..1003.
	if id != shard.UserMin+4 {
		t.Fatalf("claim after three lost races = %d, want %d", id, shard.UserMin+4)
	}
}

func TestClaimShardID_SustainedContentionIsTyped(t *testing.T) {
	store := &racingStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	c := newClaimer(t, store)
	if _, err := c.ClaimShardID(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.races = math.MaxInt
	_, err := c.ClaimShardID(context.Background())
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
		if _, err := l.MemoryKVStore.PutIfAbsent(ctx, key, []byte(strconv.FormatUint(shard.UserMin+1, 10))); err != nil {
			return false, err
		}
	}
	return l.MemoryKVStore.PutIfAbsent(ctx, key, value)
}

func TestClaimShardID_ClaimsFromAMarkAnotherNodeCreated(t *testing.T) {
	store := &lostCreateStore{MemoryKVStore: kvstore.NewMemoryKVStore()}
	c := newClaimer(t, store)
	id, err := c.ClaimShardID(context.Background())
	if err != nil || id != shard.UserMin+1 {
		t.Fatalf("claim = %d, %v; want %d, after the other node's %d", id, err, shard.UserMin+1, shard.UserMin)
	}
}

func TestClaimShardID_CorruptMarkRefuses(t *testing.T) {
	for _, v := range []string{"not-a-number", "1", strconv.FormatUint(shard.UserMin, 10)} {
		store := kvstore.NewMemoryKVStore()
		setMark(t, store, v)
		_, err := newClaimer(t, store).ClaimShardID(context.Background())
		var corrupt *shard.HighWaterCorruptError
		if !errors.As(err, &corrupt) || corrupt.Value != v {
			t.Fatalf("claim over mark %q = %v, want *HighWaterCorruptError", v, err)
		}
		if corrupt.Error() == "" {
			t.Fatal("empty error text")
		}
	}
}

func TestClaimShardID_ExhaustionIsTyped(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	setMark(t, store, strconv.FormatUint(math.MaxUint64, 10))
	_, err := newClaimer(t, store).ClaimShardID(context.Background())
	var exhausted *shard.ShardIDExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("claim at the end of the ID space = %v, want *ShardIDExhaustedError", err)
	}
	if exhausted.Error() == "" {
		t.Fatal("empty error text")
	}
}

// A claimer with an activation refuses every claim, without touching the
// store, until the activation reports true; then it claims as usual.
func TestClaimShardID_WaitsForActivation(t *testing.T) {
	store := kvstore.NewMemoryKVStore()
	active := false
	c, err := shard.NewClaimer(store, shard.WithActivation(featuregate.ClusterShardIDs, func() bool { return active }))
	if err != nil {
		t.Fatalf("NewClaimer: %v", err)
	}
	_, err = c.ClaimShardID(context.Background())
	var inactive *feature.NotActiveError
	if !errors.As(err, &inactive) || inactive.Feature != featuregate.ClusterShardIDs {
		t.Fatalf("claim before activation = %v, want *feature.NotActiveError", err)
	}
	if msg := inactive.Error(); !strings.Contains(msg, `"cluster-shard-ids"`) {
		t.Fatalf("error text %q does not name the feature", msg)
	}
	if raw, _ := store.Get(context.Background(), shard.HighWaterKey); raw != nil {
		t.Fatalf("a refused claim wrote the mark: %q", raw)
	}
	active = true
	if id, err := c.ClaimShardID(context.Background()); err != nil || id != shard.UserMin {
		t.Fatalf("claim after activation = %d, %v; want %d", id, err, shard.UserMin)
	}
}

func TestNewClaimer_ActivationNeedsAFeature(t *testing.T) {
	_, err := shard.NewClaimer(kvstore.NewMemoryKVStore(), shard.WithActivation("", func() bool { return true }))
	var cfg *shard.ClaimerConfigError
	if !errors.As(err, &cfg) || cfg.Field != "activation feature" {
		t.Fatalf("NewClaimer with an unnamed activation = %v, want *ClaimerConfigError{activation feature}", err)
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
				setMark(t, store, strconv.FormatUint(shard.UserMin+1, 10))
			}
			store.failOp = op
			_, err := newClaimer(t, store).ClaimShardID(context.Background())
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

func TestClaimShardID_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newClaimer(t, kvstore.NewMemoryKVStore()).ClaimShardID(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("claim on a cancelled context = %v, want context.Canceled", err)
	}
}
