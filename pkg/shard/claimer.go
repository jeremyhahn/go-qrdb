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

package shard

import (
	"context"
	"math"
	"strconv"
)

// HighWaterKey is the replicated record a Claimer allocates from: the next
// shard ID no node has issued, in decimal. It lives in the system store,
// outside every tenant, because a shard ID names a Raft group on a host every
// tenant shares.
const HighWaterKey = "__sys:shard/high-water"

// maxClaimAttempts bounds how many times one claim re-reads the high-water
// mark after losing a race to another node. Each lost race means another
// claim committed, so the bound is only reached under sustained contention
// from more concurrent creators than this.
const maxClaimAttempts = 64

// ClaimStore is the slice of the replicated system store a Claimer needs.
// PutIfAbsent and CompareAndSwap must be decided at the store's own log
// position -- kvstore.AtomicKVStore's contract -- or two nodes can both
// observe the same high-water mark and both issue it.
type ClaimStore interface {
	Get(ctx context.Context, key string) ([]byte, error)
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

// SeedFunc reports the highest shard ID already in use anywhere in the
// cluster, or zero when there is none. It is consulted once, when the
// high-water mark does not exist yet, so a cluster whose nodes allocated
// locally before claims existed never re-issues one of their IDs.
type SeedFunc func(ctx context.Context) (uint64, error)

// Claimer issues shard IDs that are unique across a cluster.
//
// An Allocator is unique per node only: every node's counter starts at
// UserMin and moves past the IDs THAT node issued, so two nodes creating a
// raft database each issue the same ID. A Claimer takes every ID from one
// replicated high-water mark with a compare-and-swap, so an ID is issued once
// in the cluster's lifetime -- dropped databases included, which is what a
// shard ID standing in for a database incarnation requires.
type Claimer struct {
	store ClaimStore
	seed  SeedFunc
}

// NewClaimer returns a Claimer over store. seed may be nil when the store
// is known to start empty.
func NewClaimer(store ClaimStore, seed SeedFunc) (*Claimer, error) {
	if store == nil {
		return nil, &ClaimerConfigError{Field: "store"}
	}
	return &Claimer{store: store, seed: seed}, nil
}

// ClaimShardID issues the next cluster-wide shard ID at or above floor.
//
// floor is the caller's own lower bound -- a node passes its allocator's
// next value, so a claim never issues an ID the node has issued itself even
// on a cluster whose high-water mark is newer than that node's history.
// UserMin is always a floor too, keeping claims out of the reserved ranges.
//
// The returned ID is recorded as issued before it is returned: a claim whose
// caller then fails leaves a gap, never a reuse.
func (c *Claimer) ClaimShardID(ctx context.Context, floor uint64) (uint64, error) {
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, &ClaimStoreError{Op: "claim", Err: err}
		}
		raw, err := c.store.Get(ctx, HighWaterKey)
		if err != nil {
			return 0, &ClaimStoreError{Op: "read", Err: err}
		}
		if raw == nil {
			id, err := c.firstID(ctx, floor)
			if err != nil {
				return 0, err
			}
			next, err := after(id)
			if err != nil {
				return 0, err
			}
			stored, err := c.store.PutIfAbsent(ctx, HighWaterKey, next)
			if err != nil {
				return 0, &ClaimStoreError{Op: "create", Err: err}
			}
			if stored {
				return id, nil
			}
			continue // another node created the mark first; claim from it
		}
		current, err := strconv.ParseUint(string(raw), 10, 64)
		if err != nil {
			return 0, &HighWaterCorruptError{Value: string(raw)}
		}
		id := max(current, floor, UserMin)
		next, err := after(id)
		if err != nil {
			return 0, err
		}
		swapped, err := c.store.CompareAndSwap(ctx, HighWaterKey, raw, next)
		if err != nil {
			return 0, &ClaimStoreError{Op: "swap", Err: err}
		}
		if swapped {
			return id, nil
		}
	}
	return 0, &ClaimContentionError{Attempts: maxClaimAttempts}
}

// firstID is the ID the claim that creates the high-water mark issues: past
// every ID the cluster already uses, the caller's floor and UserMin.
func (c *Claimer) firstID(ctx context.Context, floor uint64) (uint64, error) {
	id := max(floor, UserMin)
	if c.seed == nil {
		return id, nil
	}
	inUse, err := c.seed(ctx)
	if err != nil {
		return 0, &ClaimStoreError{Op: "seed", Err: err}
	}
	if inUse == math.MaxUint64 {
		return 0, &ShardIDExhaustedError{}
	}
	return max(id, inUse+1), nil
}

// after encodes the high-water mark that follows issuing id.
func after(id uint64) ([]byte, error) {
	if id == math.MaxUint64 {
		return nil, &ShardIDExhaustedError{}
	}
	return []byte(strconv.FormatUint(id+1, 10)), nil
}
