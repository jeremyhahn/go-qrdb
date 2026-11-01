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

	"github.com/jeremyhahn/go-qrdb/pkg/feature"
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

// Claimer issues shard IDs that are unique across a cluster.
//
// A Claimer takes every ID from one replicated high-water mark with a
// compare-and-swap, so an ID is issued once in the cluster's lifetime --
// dropped databases included, which is what a shard ID standing in for a
// database incarnation requires. The record is the only state: nothing on a
// node's disk takes part, so a restart, a restore into a fresh data
// directory, and standalone and raft mode all behave the same.
//
// The guarantee rests on the Claimer being the only issuer of user-range
// shard IDs in a cluster: a database create cannot name its own ID
// (database.CallerShardIDError) and cannot be provisioned with a Raft
// configuration (database.ProvisionRaftError).
type Claimer struct {
	store ClaimStore

	// feature and active gate every claim; see WithActivation.
	feature feature.Feature
	active  func() bool
}

// ClaimerOption configures a Claimer.
type ClaimerOption func(*Claimer)

// WithActivation makes every claim wait for feature: while active reports
// false the claim is refused with *feature.NotActiveError and the store is
// not touched. A cluster whose nodes do not all claim -- a rolling upgrade
// from a binary that allocated shard IDs per node -- could otherwise issue
// one ID twice, from the old node's counter and from the mark. A Claimer
// with no activation serves a host whose groups hold only this binary.
func WithActivation(feature feature.Feature, active func() bool) ClaimerOption {
	return func(c *Claimer) {
		c.feature = feature
		c.active = active
	}
}

// NewClaimer returns a Claimer over store.
func NewClaimer(store ClaimStore, opts ...ClaimerOption) (*Claimer, error) {
	if store == nil {
		return nil, &ClaimerConfigError{Field: "store"}
	}
	c := &Claimer{store: store}
	for _, opt := range opts {
		opt(c)
	}
	if c.active != nil && c.feature == "" {
		return nil, &ClaimerConfigError{Field: "activation feature"}
	}
	return c, nil
}

// ClaimShardID issues the next cluster-wide shard ID. The first ID a cluster
// issues is UserMin, which keeps claims out of the reserved ranges.
//
// The returned ID is recorded as issued before it is returned: a claim whose
// caller then fails leaves a gap, never a reuse.
func (c *Claimer) ClaimShardID(ctx context.Context) (uint64, error) {
	if c.active != nil && !c.active() {
		return 0, &feature.NotActiveError{Feature: c.feature}
	}
	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, &ClaimStoreError{Op: "claim", Err: err}
		}
		raw, err := c.store.Get(ctx, HighWaterKey)
		if err != nil {
			return 0, &ClaimStoreError{Op: "read", Err: err}
		}
		if raw == nil {
			stored, err := c.store.PutIfAbsent(ctx, HighWaterKey, []byte(strconv.FormatUint(UserMin+1, 10)))
			if err != nil {
				return 0, &ClaimStoreError{Op: "create", Err: err}
			}
			if stored {
				return UserMin, nil
			}
			continue // another node created the mark first; claim from it
		}
		id, err := strconv.ParseUint(string(raw), 10, 64)
		// The mark is only ever written past an issued ID, so it is never
		// at or below UserMin. Anything else is not a mark this code wrote,
		// and a claim refuses rather than guess.
		if err != nil || id <= UserMin {
			return 0, &HighWaterCorruptError{Value: string(raw)}
		}
		if id == math.MaxUint64 {
			return 0, &ShardIDExhaustedError{}
		}
		swapped, err := c.store.CompareAndSwap(ctx, HighWaterKey, raw, []byte(strconv.FormatUint(id+1, 10)))
		if err != nil {
			return 0, &ClaimStoreError{Op: "swap", Err: err}
		}
		if swapped {
			return id, nil
		}
	}
	return 0, &ClaimContentionError{Attempts: maxClaimAttempts}
}
