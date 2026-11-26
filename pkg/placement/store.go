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

package placement

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// PlacementPrefix is the key prefix under which placement records live in the
// replicated system KV. It uses qrdb's "__sys:" system namespace so the entries
// are hidden from user-facing KV list/scan (which filter that prefix) and kept
// out of the tenant key space, exactly as the cluster-membership store does.
// Each database's placement is stored at
// "__sys:placement/{tenantID}/{databaseID}", keyed by the database's opaque
// id (Placement.DatabaseID), never by its name: the name <-> id mapping lives
// only in the tenant's catalog.
const PlacementPrefix = "__sys:placement/"

// KV is the minimal replicated system-KV substrate the placement store needs.
// The shared system KVStore (kvstore.KVStore) satisfies it directly, so the
// placement table is persisted on the same Raft-replicated store the IAM and
// membership subsystems use — every node reads a consistent view.
type KV interface {
	// Put stores a key-value pair.
	Put(ctx context.Context, key string, value []byte) error

	// Get retrieves a value by key, returning (nil, nil) for a missing key.
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete removes a key.
	Delete(ctx context.Context, key string) error

	// Scan iterates key-value pairs under prefix, stopping when fn returns a
	// non-nil error.
	Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error
}

// WatchEvent is a single change observed by a Watcher over the placement
// prefix. Deleted distinguishes a removal from a put; Value carries the current
// (put) value, PrevValue the prior value (used to recover the identity of a
// deleted entry).
type WatchEvent struct {
	// Deleted is true for a key removal, false for a put.
	Deleted bool

	// Key is the changed key.
	Key string

	// Value is the new value for a put; nil for a delete.
	Value []byte

	// PrevValue is the prior value, when the backend supplies it.
	PrevValue []byte
}

// Watcher is the optional change-feed capability a KV backend may provide. When
// the configured KV also implements Watcher, Store.Watch surfaces a typed
// placement event stream; otherwise Watch returns ErrWatchUnsupported. The
// per-node reconciler (Phase 4c-1d) consumes the stream to converge local
// replicas to the table.
type Watcher interface {
	// Watch returns a channel of change events for keys under prefix. The
	// channel is closed when ctx is cancelled or the feed ends.
	Watch(ctx context.Context, prefix string) (<-chan WatchEvent, error)
}

// AtomicKV is the optional guarded-write capability a KV backend may
// provide. When the configured KV also implements AtomicKV, every
// read-modify-write this package performs against a SINGLE placement entry
// -- SetDropping, and CompareAndSwapPlacement's callers (the scale and
// reconciler paths that read a Placement, derive an updated copy, and write
// it back) -- commits with a compare-and-swap against the exact bytes it
// read, instead of an unconditional Put. Without this, two concurrent
// mutations of the same entry (a scale-up admitting a replica, a
// reconciler promoting one, a Drop setting the fence) resolve by
// last-write-wins: whichever Put commits last silently discards the
// other's change, including a Drop's own fence (go-qrdb#482). The shared
// system KVStore this package is normally configured with
// (kvstore.KVStore, via lifecycle's kvStoreAdapter) already implements
// this; a KV that does not falls back to a bare Put, which is what every
// mutation in this package did before AtomicKV existed.
type AtomicKV interface {
	// CompareAndSwap replaces key's value with next only if it currently
	// equals expected. A nil/empty expected is not a supported "key must be
	// absent" case here (every placement mutation this package performs
	// targets an entry it just read), so it is not documented as one.
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
}

// PutIfAbsentKV is the optional atomic-create capability a KV backend may
// provide. When the configured KV also implements PutIfAbsentKV,
// Store.ClaimPlacement commits a BRAND NEW placement entry with a
// cluster-wide "first write wins" guarantee: two nodes racing to create the
// same (tenant, databaseID) -- the case a local binding's single owner must
// be decided by, not a Put each node could issue unconditionally -- settle
// on exactly one winner everywhere, never "whichever Put committed last on
// this entry," which is what an unconditional Put would give (go-qrdb Lane
// 2B). The shared system KVStore this package is normally configured with
// (kvstore.KVStore, via lifecycle's kvStoreAdapter) already implements this.
type PutIfAbsentKV interface {
	// PutIfAbsent stores value under key only if key is not already
	// present. It reports stored=true when this call created the key, and
	// stored=false with a nil error when the key already existed -- in
	// which case the store is unchanged.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)
}

// EventType distinguishes a placement put from a placement delete.
type EventType uint8

const (
	// EventPut indicates a placement was created or updated.
	EventPut EventType = iota

	// EventDelete indicates a placement was removed.
	EventDelete
)

// Event is a placement change surfaced by Store.Watch.
type Event struct {
	// Type is the kind of change.
	Type EventType

	// TenantID and DatabaseID identify the affected placement: the table's
	// key.
	TenantID   string
	DatabaseID string

	// Database is the database name the entry's value carries. Empty for
	// an EventDelete whose prior value was not delivered: the key holds
	// only the id.
	Database string

	// ShardID is the entry's immutable shard identity: the placement's
	// for an EventPut, and the prior value's for an EventDelete whose
	// prior value was delivered, so a delete can name the shard to tear
	// down even when its consumer never saw the entry. Zero when unknown.
	ShardID uint64

	// Placement is the decoded placement for an EventPut; nil for an
	// EventDelete (or when the prior value could not be decoded).
	Placement *Placement
}

// Store persists and retrieves Placement records in the replicated system KV
// under PlacementPrefix. It is stateless beyond its KV reference and therefore
// safe for concurrent use.
type Store struct {
	kv    KV
	fence *replicaAddFence

	// names caches id -> name for the entries this node has written or
	// resolved. A database id is never reused, so a cached name never goes
	// stale. The cache is node memory: no name is written to kv.
	names sync.Map

	// resolver resolves the names the cache misses, from the tenant's own
	// catalog (SetNameResolver).
	resolver atomic.Pointer[nameResolverRef]
}

// NameResolver resolves a tenant's database ids to their names inside the
// tenant's boundary: from the tenant's catalog, which a strict tenant keeps
// in its home store. The placement table never records a database name
// (docs/multi-tenancy/architecture.md decision S5), so the Store asks it for
// the name of every entry it reads.
type NameResolver interface {
	// DatabaseNames returns the names of tenantID's databases by id. A
	// tenant whose catalog cannot be read (sealed, or its home not yet open)
	// returns what it can; an id it does not return reads with an empty
	// Database and is resolved again on the next read.
	DatabaseNames(ctx context.Context, tenantID string) map[string]string
}

type nameResolverRef struct{ r NameResolver }

// SetNameResolver sets the resolver the Store reads database names from.
// Until it is set (and for an id it does not know), only the names this
// Store wrote or resolved before are known.
func (s *Store) SetNameResolver(r NameResolver) {
	if r == nil {
		s.resolver.Store(nil)
		return
	}
	s.resolver.Store(&nameResolverRef{r: r})
}

// encodePlacement is the system KV value of p: p without its database name,
// which stays inside the tenant's boundary. The name p carries is cached.
func (s *Store) encodePlacement(p *Placement) ([]byte, error) {
	if p.Database != "" {
		s.names.Store(nameCacheKey(p.TenantID, p.DatabaseID), p.Database)
	}
	stored := *p
	stored.Database = ""
	return json.Marshal(&stored)
}

// decodePlacement decodes a system KV value. Its Database is resolved by
// resolveNames.
func decodePlacement(value []byte) (*Placement, error) {
	var p Placement
	if err := json.Unmarshal(value, &p); err != nil {
		return nil, err
	}
	p.Database = ""
	return &p, nil
}

// resolveNames fills the Database of every entry of ps: from the cache, else
// from the resolver, asked once per tenant.
func (s *Store) resolveNames(ctx context.Context, ps ...*Placement) {
	var byTenant map[string]map[string]string
	for _, p := range ps {
		if p == nil || p.DatabaseID == "" {
			continue
		}
		if p.DatabaseID == tenant.HomeName {
			// The home group's id is its name. Resolving it through the
			// catalog would recurse: an isolated tenant's catalog lives in
			// its home store, which is located through this table.
			p.Database = tenant.HomeName
			continue
		}
		key := nameCacheKey(p.TenantID, p.DatabaseID)
		if name, ok := s.names.Load(key); ok {
			p.Database = name.(string) //nolint:errcheck // the cache only ever holds strings
			continue
		}
		ref := s.resolver.Load()
		if ref == nil || resolving(ctx) {
			continue
		}
		if byTenant == nil {
			byTenant = make(map[string]map[string]string)
		}
		names, ok := byTenant[p.TenantID]
		if !ok {
			names = ref.r.DatabaseNames(context.WithValue(ctx, resolvingKey{}, true), p.TenantID)
			byTenant[p.TenantID] = names
		}
		if name := names[p.DatabaseID]; name != "" {
			p.Database = name
			s.names.Store(key, name)
		}
	}
}

// resolvingKey marks a context inside a NameResolver call. The resolver
// reads the tenant's catalog, which for an isolated tenant first locates its
// home through this table; a read made there returns its entries without
// asking the resolver again, which would recurse without end.
type resolvingKey struct{}

func resolving(ctx context.Context) bool {
	v, _ := ctx.Value(resolvingKey{}).(bool)
	return v
}

func nameCacheKey(tenantID, databaseID string) string {
	return tenantID + "/" + databaseID
}

// GuardedBatchKV is the optional multi-guard capability a KV backend may
// provide: every write applied atomically, and only while every guard's key
// still holds its expected value (a nil guard value means "the key must be
// absent"). lifecycle's guardedKVStoreAdapter (kvstore.MultiGuardedBatchKVStore)
// implements it. A Store built WithReplicaAddFence commits every
// replica-adding write through it.
type GuardedBatchKV interface {
	PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (applied bool, err error)
}

// StoreOption configures a Store.
type StoreOption func(*Store)

// replicaAddFence is the WithReplicaAddFence configuration.
type replicaAddFence struct {
	key     func(tenantID string) string
	refusal func(tenantID string, fence []byte) error
}

// WithReplicaAddFence fences every placement write that ADDS a replica to a
// tenant (or creates a placement for it) on a key that must be absent: the
// write commits as one guarded batch comparing the placement entry (its
// expected bytes, or absent for a create) AND fenceKey(tenantID) absent, so
// a fence key committed at any moment before the write is seen by it, and
// one committed after it is ordered after it. lifecycle sets it with the
// volume pool's move journal key, so a scale-up or a rebalance that read
// "no move" before MoveTenant admitted the journal cannot commit a replica
// the move's resync would miss: either the write lands first (and the
// resync reads it) or it is refused. A refused write answers
// refusal(tenantID, storedFenceValue) unwrapped; the store never writes the
// fence key.
//
// Writes that add no replica (SetDropping, TryMarkDropping,
// MarkDropCommitted, RemoveReplica, a promotion or a removal through
// CompareAndSwapPlacement or Put) stay unfenced: a departure during a move
// is the move's own business (SyncMoveNodes). A replica is "added" when next
// names one whose (ReplicaID, NodeID, RaftAddr) no current replica has.
//
// A fenced write over a KV without GuardedBatchKV fails closed with a
// *StoreError wrapping *AtomicWriteUnsupportedError.
func WithReplicaAddFence(fenceKey func(tenantID string) string, refusal func(tenantID string, fence []byte) error) StoreOption {
	return func(s *Store) {
		if fenceKey == nil || refusal == nil {
			return
		}
		s.fence = &replicaAddFence{key: fenceKey, refusal: refusal}
	}
}

// addsReplica reports whether next names a replica current does not have.
// A nil current (a create) adds every replica next names.
func addsReplica(current, next *Placement) bool {
	if current == nil {
		return true
	}
	for _, r := range next.Replicas {
		found := false
		for _, c := range current.Replicas {
			if c.ReplicaID == r.ReplicaID && c.NodeID == r.NodeID && c.RaftAddr == r.RaftAddr {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}
	return false
}

// fencedWrite commits key=next as one guarded batch: the placement entry
// must hold expected (absent when expected is nil) and the tenant's fence
// key must be absent. applied=false with a nil error is a placement change
// (the caller declines or retries as it would without the fence); a fence
// key present answers the configured refusal.
func (s *Store) fencedWrite(ctx context.Context, op, tenantID, databaseID string, expected, next []byte) (bool, error) {
	guarded, ok := s.kv.(GuardedBatchKV)
	if !ok {
		return false, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: op, Err: &AtomicWriteUnsupportedError{}}
	}
	key := placementKey(tenantID, databaseID)
	fenceKey := s.fence.key(tenantID)
	applied, err := guarded.PutAllIfAllUnchanged(ctx,
		[]kvstore.Guard{{Key: key, Value: expected}, {Key: fenceKey, Value: nil}},
		[]kvstore.KV{{Key: key, Value: next}})
	if err != nil {
		return false, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: op, Err: err}
	}
	if applied {
		return true, nil
	}
	fence, err := s.kv.Get(ctx, fenceKey)
	if err != nil {
		return false, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: op, Err: err}
	}
	if fence != nil {
		return false, s.fence.refusal(tenantID, fence)
	}
	return false, nil
}

// NewStore constructs a Store over the given KV backend, which must be non-nil.
func NewStore(kv KV, opts ...StoreOption) (*Store, error) {
	if kv == nil {
		return nil, ErrNilKV
	}
	s := &Store{kv: kv}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// placementKey returns the storage key for a (tenant, databaseID) placement.
func placementKey(tenantID, databaseID string) string {
	return PlacementPrefix + tenantID + "/" + databaseID
}

// fencedPut is Put under WithReplicaAddFence: a Put that creates the
// placement or adds a replica to the stored one commits through
// fencedWrite against the bytes it read; any other Put stays a plain Put.
// A placement that changes between the read and the guarded write is
// re-read, at most setDroppingMaxAttempts times.
func (s *Store) fencedPut(ctx context.Context, p *Placement, data []byte) error {
	key := placementKey(p.TenantID, p.DatabaseID)
	for attempt := 0; attempt < setDroppingMaxAttempts; attempt++ {
		raw, err := s.kv.Get(ctx, key)
		if err != nil {
			return &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "put", Err: err}
		}
		var current *Placement
		if raw != nil {
			c, err := decodePlacement(raw)
			if err != nil {
				return &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "put", Err: err}
			}
			current = c
		}
		if !addsReplica(current, p) {
			if err := s.kv.Put(ctx, key, data); err != nil {
				return &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "put", Err: err}
			}
			return nil
		}
		applied, err := s.fencedWrite(ctx, "put", p.TenantID, p.DatabaseID, raw, data)
		if err != nil {
			return err
		}
		if applied {
			return nil
		}
	}
	return &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "put", Err: &ConflictError{}}
}

// Put stores (or replaces) the placement for its (TenantID, Database). The
// placement's TenantID and Database must be set.
func (s *Store) Put(ctx context.Context, p *Placement) error {
	if p == nil {
		return &StoreError{Op: "put", Err: ErrPlacementNotFound}
	}
	if p.TenantID == "" {
		return &StoreError{DatabaseID: p.DatabaseID, Op: "put", Err: ErrEmptyTenant}
	}
	if p.DatabaseID == "" {
		return &StoreError{TenantID: p.TenantID, Op: "put", Err: ErrEmptyDatabaseID}
	}
	data, err := s.encodePlacement(p)
	if err != nil {
		return &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "put", Err: err}
	}
	if s.fence != nil {
		return s.fencedPut(ctx, p, data)
	}
	if err := s.kv.Put(ctx, placementKey(p.TenantID, p.DatabaseID), data); err != nil {
		return &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "put", Err: err}
	}
	return nil
}

// Get returns the placement for (tenantID, databaseID). It returns a *StoreError
// wrapping ErrPlacementNotFound when no entry exists.
func (s *Store) Get(ctx context.Context, tenantID, databaseID string) (*Placement, error) {
	if tenantID == "" {
		return nil, &StoreError{DatabaseID: databaseID, Op: "get", Err: ErrEmptyTenant}
	}
	if databaseID == "" {
		return nil, &StoreError{TenantID: tenantID, Op: "get", Err: ErrEmptyDatabaseID}
	}
	raw, err := s.kv.Get(ctx, placementKey(tenantID, databaseID))
	if err != nil {
		return nil, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "get", Err: err}
	}
	if raw == nil {
		return nil, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "get", Err: ErrPlacementNotFound}
	}
	p, err := decodePlacement(raw)
	if err != nil {
		return nil, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "get", Err: err}
	}
	s.resolveNames(ctx, p)
	return p, nil
}

// Delete removes the placement for (tenantID, databaseID). Deleting a missing
// entry is a no-op (it mirrors the underlying KV delete semantics).
func (s *Store) Delete(ctx context.Context, tenantID, databaseID string) error {
	if tenantID == "" {
		return &StoreError{DatabaseID: databaseID, Op: "delete", Err: ErrEmptyTenant}
	}
	if databaseID == "" {
		return &StoreError{TenantID: tenantID, Op: "delete", Err: ErrEmptyDatabaseID}
	}
	if err := s.kv.Delete(ctx, placementKey(tenantID, databaseID)); err != nil {
		return &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "delete", Err: err}
	}
	return nil
}

// CompareAndSwapPlacement replaces the placement for (next.TenantID,
// next.DatabaseID) with next, but only if the entry currently marshals to
// the exact bytes expected does (nil expected is not supported -- every
// caller in this package holds a value it just read). It is the primitive
// a read-modify-write of an EXISTING placement entry should commit through
// instead of Put, so a caller's own copy -- read before some OTHER
// mutation (a Drop's fence, a concurrent scale, a reconciler promotion)
// committed -- can never silently overwrite it (go-qrdb#482); see AtomicKV.
//
// Returns (false, nil) -- not an error -- when the entry changed since
// expected was read: that is the compare-and-swap declining, not a failure
// to reach the store. A caller that must retry re-reads and re-derives its
// update from the fresh value, exactly as SetDropping does internally.
//
// Returns a *StoreError wrapping *AtomicWriteUnsupportedError when the
// backing KV does not implement AtomicKV. This is a capability refusal,
// not a silent fallback to Put: falling back would defeat the entire
// reason a caller chose this over Put.
func (s *Store) CompareAndSwapPlacement(ctx context.Context, expected, next *Placement) (bool, error) {
	if next == nil {
		return false, &StoreError{Op: "compare-and-swap", Err: ErrPlacementNotFound}
	}
	if next.TenantID == "" {
		return false, &StoreError{DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: ErrEmptyTenant}
	}
	if next.DatabaseID == "" {
		return false, &StoreError{TenantID: next.TenantID, Op: "compare-and-swap", Err: ErrEmptyDatabaseID}
	}
	fenced := s.fence != nil && addsReplica(expected, next)
	var atomicKV AtomicKV
	if !fenced {
		var ok bool
		atomicKV, ok = s.kv.(AtomicKV)
		if !ok {
			return false, &StoreError{TenantID: next.TenantID, DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: &AtomicWriteUnsupportedError{}}
		}
	}
	var expectedBytes []byte
	if expected != nil {
		b, err := s.encodePlacement(expected)
		if err != nil {
			return false, &StoreError{TenantID: next.TenantID, DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: err}
		}
		expectedBytes = b
	}
	nextBytes, err := s.encodePlacement(next)
	if err != nil {
		return false, &StoreError{TenantID: next.TenantID, DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: err}
	}
	if fenced {
		return s.fencedWrite(ctx, "compare-and-swap", next.TenantID, next.DatabaseID, expectedBytes, nextBytes)
	}
	swapped, err := atomicKV.CompareAndSwap(ctx, placementKey(next.TenantID, next.DatabaseID), expectedBytes, nextBytes)
	if err != nil {
		return false, &StoreError{TenantID: next.TenantID, DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: err}
	}
	return swapped, nil
}

// ClaimPlacement commits p as a brand-new placement entry for (p.TenantID,
// p.DatabaseID), atomically and cluster-wide: the first call to reach the
// store -- on this node or any other racing it for the same name -- wins,
// and every other call observes claimed=false together with the winner's
// own entry in existing, never a second write that silently overwrote it.
// This is the primitive "a database created on one node resolves to the
// same binding on every node" rests on (go-qrdb Lane 2B): a create that
// only Put its own choice of owner would let two nodes racing the same name
// each commit their own, and whichever Put lands last would silently win,
// stranding the data the first owner already started serving.
//
// It is Put's counterpart for an entry that must not already exist, exactly
// as CompareAndSwapPlacement is Put's counterpart for one that does:
// neither ever falls back to a bare Put.
//
// Returns a *StoreError wrapping *AtomicWriteUnsupportedError when the
// backing KV does not implement PutIfAbsentKV -- a capability refusal, not
// a fallback: falling back to Put would defeat the entire reason a caller
// chose this over Put, exactly as for CompareAndSwapPlacement.
//
// Returns existing as nil when claimed is true (there is nothing else to
// report) or when claimed is false only because reading the winner's entry
// itself failed -- that failure is returned as err instead.
func (s *Store) ClaimPlacement(ctx context.Context, p *Placement) (claimed bool, existing *Placement, err error) {
	if p == nil {
		return false, nil, &StoreError{Op: "claim", Err: ErrPlacementNotFound}
	}
	if p.TenantID == "" {
		return false, nil, &StoreError{DatabaseID: p.DatabaseID, Op: "claim", Err: ErrEmptyTenant}
	}
	if p.DatabaseID == "" {
		return false, nil, &StoreError{TenantID: p.TenantID, Op: "claim", Err: ErrEmptyDatabaseID}
	}
	data, err := s.encodePlacement(p)
	if err != nil {
		return false, nil, &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "claim", Err: err}
	}
	var stored bool
	if s.fence != nil {
		// A claim creates the placement: absent placement AND absent fence
		// key, in one guarded batch (WithReplicaAddFence).
		stored, err = s.fencedWrite(ctx, "claim", p.TenantID, p.DatabaseID, nil, data)
		if err != nil {
			return false, nil, err
		}
	} else {
		creator, ok := s.kv.(PutIfAbsentKV)
		if !ok {
			return false, nil, &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "claim", Err: &AtomicWriteUnsupportedError{}}
		}
		stored, err = creator.PutIfAbsent(ctx, placementKey(p.TenantID, p.DatabaseID), data)
		if err != nil {
			return false, nil, &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "claim", Err: err}
		}
	}
	if stored {
		return true, nil, nil
	}
	current, err := s.Get(ctx, p.TenantID, p.DatabaseID)
	if err != nil {
		return false, nil, err
	}
	return false, current, nil
}

// setDroppingMaxAttempts bounds SetDropping's optimistic-retry loop. Each
// attempt costs one Get plus one CompareAndSwap; a genuine conflict is
// resolved by the first attempt whose read observes the other mutation
// already committed, so this bounds against a pathologically hot entry
// rather than the expected case of zero or one retry.
const setDroppingMaxAttempts = 10

// SetDropping sets or clears the replicated drop fence (Placement.Dropping)
// on the placement for (tenantID, databaseID). A no-op write (the flag
// already holds the requested value) still succeeds without a store round
// trip beyond the read, matching Get's own idempotence contract rather
// than adding a special case a caller has to reason about.
//
// It commits through CompareAndSwapPlacement, retrying the whole
// read-modify-write when the entry changed since it was read -- a
// concurrent scale-up admitting a replica, or a reconciler promoting one,
// must never be silently discarded by a Drop's fence commit, and a Drop's
// fence must never be silently discarded by either of those (go-qrdb#482).
//
// Returns a *StoreError wrapping *AtomicWriteUnsupportedError when the backing
// KV cannot compare-and-swap at all: go-qrdb#482 round 3 found that
// degrading to an unconditional Put here was itself the exact race this
// fence exists to close -- two concurrent SetDropping(true) calls (or a
// racing scale/promotion) could both "succeed" against a backend with no
// compare-and-swap, silently overwriting one another. A backend that cannot
// offer atomic-mutation safety cannot host a dedicated-raft database's
// fence at all; every production KV (see AtomicKV) supports it.
//
// Returns a *StoreError wrapping ErrPlacementNotFound when no placement
// entry exists for (tenantID, databaseID) -- there is nothing to fence -- or
// *ConflictError when every attempt in the retry loop lost the race.
func (s *Store) SetDropping(ctx context.Context, tenantID, databaseID string, dropping bool) error {
	for attempt := 0; attempt < setDroppingMaxAttempts; attempt++ {
		p, err := s.Get(ctx, tenantID, databaseID)
		if err != nil {
			return err
		}
		if p.Dropping == dropping {
			return nil
		}
		next := *p
		next.Dropping = dropping
		swapped, err := s.CompareAndSwapPlacement(ctx, p, &next)
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
		// Lost the race: another mutation committed between Get and
		// CompareAndSwap. Retry with a fresh read.
	}
	return &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "set-dropping", Err: &ConflictError{}}
}

// TryMarkDropping attempts to transition the replicated drop fence for
// (tenantID, databaseID) from not-dropping to dropping, and reports whether
// THIS call performed that exact transition (owned=true), as opposed to
// finding the fence already set (owned=false, nil error).
//
// owned=false covers two situations a caller cannot tell apart from this
// return value alone, and must not treat differently: another drop request
// racing this one already claimed the fence, or an earlier attempt for the
// very same drop still owns it because this node restarted before clearing
// or completing it. Either way, THIS call did not just set the fence, and
// must never be the one to clear it afterward on a failure -- doing so is
// go-qrdb#482 round 3's finding that two concurrent drops (or a resumed
// retry racing a still-live earlier attempt) could roll back a fence they
// never owned. A caller that needs the fence cleared on failure gates that
// call on owned; a caller that only needs the whole entry removed once its
// own database-level drop succeeds (finishRaftPlacementTeardown) does not
// need to gate on it at all, since deletion is safe regardless of who most
// recently owned the fence.
//
// Commits through CompareAndSwapPlacement with the same bounded retry as
// SetDropping, and the same refusal -- not a fallback to Put -- when the
// backing KV cannot compare-and-swap: see SetDropping's doc comment for why
// a fallback here would let two concurrent callers both observe owned=true.
func (s *Store) TryMarkDropping(ctx context.Context, tenantID, databaseID string) (owned bool, err error) {
	for attempt := 0; attempt < setDroppingMaxAttempts; attempt++ {
		p, err := s.Get(ctx, tenantID, databaseID)
		if err != nil {
			return false, err
		}
		if p.Dropping {
			return false, nil
		}
		next := *p
		next.Dropping = true
		swapped, err := s.CompareAndSwapPlacement(ctx, p, &next)
		if err != nil {
			return false, err
		}
		if swapped {
			return true, nil
		}
		// Lost the race: another mutation committed between Get and
		// CompareAndSwap. Retry with a fresh read.
	}
	return false, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "try-mark-dropping", Err: &ConflictError{}}
}

// MarkDropCommitted sets Placement.DropCommitted for (tenantID, databaseID), a
// one-way marker (never cleared -- the whole entry is removed together with
// it once the drop finishes) recording that some node's database.Manager has
// completed the local half of this drop. See DropCommitted's doc comment for
// why a later retry landing on a different node needs this signal. A no-op
// when the flag is already set, or when the entry no longer exists at all
// (the drop already finished completely; nothing left to mark).
//
// Commits through CompareAndSwapPlacement with the same bounded retry as
// SetDropping, and the same refusal (not a Put fallback) when the backing
// KV cannot compare-and-swap.
func (s *Store) MarkDropCommitted(ctx context.Context, tenantID, databaseID string) error {
	for attempt := 0; attempt < setDroppingMaxAttempts; attempt++ {
		p, err := s.Get(ctx, tenantID, databaseID)
		if err != nil {
			if errors.Is(err, ErrPlacementNotFound) {
				return nil
			}
			return err
		}
		if p.DropCommitted {
			return nil
		}
		next := *p
		next.DropCommitted = true
		swapped, err := s.CompareAndSwapPlacement(ctx, p, &next)
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
		// Lost the race: retry with a fresh read.
	}
	return &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "mark-drop-committed", Err: &ConflictError{}}
}

// RemoveReplica drops replicaID from the entry for (target.TenantID,
// target.DatabaseID) and returns the entry as stored afterwards, with RF
// following the remaining replicas.
//
// Unlike a compare-and-swap against target, it applies to whatever the entry
// holds when it runs: it records a removal that has already happened
// elsewhere -- the replica's Raft eviction -- so a concurrent change to
// another field (a reconciler promoting a replica, the drop fence) is kept
// rather than refused. Removing a replica the entry does not list writes
// nothing and returns the entry unchanged.
//
// Commits through CompareAndSwapPlacement with the same bounded retry as
// SetDropping. Returns a *StoreError wrapping ErrPlacementNotFound when the
// entry is gone, or *ConflictError when every attempt lost the race.
func (s *Store) RemoveReplica(ctx context.Context, target *Placement, replicaID uint64) (*Placement, error) {
	if target == nil {
		return nil, &StoreError{Op: "remove-replica", Err: ErrPlacementNotFound}
	}
	for attempt := 0; attempt < setDroppingMaxAttempts; attempt++ {
		p, err := s.Get(ctx, target.TenantID, target.DatabaseID)
		if err != nil {
			return nil, err
		}
		replicas := make([]Replica, 0, len(p.Replicas))
		for _, r := range p.Replicas {
			if r.ReplicaID != replicaID {
				replicas = append(replicas, r)
			}
		}
		if len(replicas) == len(p.Replicas) {
			return p, nil
		}
		next := *p
		// The removed ID stays used: Raft never readmits it.
		next.ReplicaIDHighWater = MaxReplicaID(p)
		next.Replicas = replicas
		next.RF = len(replicas)
		swapped, err := s.CompareAndSwapPlacement(ctx, p, &next)
		if err != nil {
			return nil, err
		}
		if swapped {
			return &next, nil
		}
		// Lost the race: retry with a fresh read.
	}
	return nil, &StoreError{TenantID: target.TenantID, DatabaseID: target.DatabaseID, Op: "remove-replica", Err: &ConflictError{}}
}

// List returns every placement in the table, decoded from the values so the
// result is independent of key formatting. An undecodable record fails the
// whole listing rather than returning a partial view.
func (s *Store) List(ctx context.Context) ([]*Placement, error) {
	var out []*Placement
	err := s.kv.Scan(ctx, PlacementPrefix, func(_ string, value []byte) error {
		p, err := decodePlacement(value)
		if err != nil {
			return err
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, &StoreError{Op: "list", Err: err}
	}
	s.resolveNames(ctx, out...)
	return out, nil
}

// ListForTenant returns every placement owned by the given tenant.
func (s *Store) ListForTenant(ctx context.Context, tenantID string) ([]*Placement, error) {
	if tenantID == "" {
		return nil, &StoreError{Op: "list", Err: ErrEmptyTenant}
	}
	var out []*Placement
	err := s.kv.Scan(ctx, PlacementPrefix+tenantID+"/", func(_ string, value []byte) error {
		p, err := decodePlacement(value)
		if err != nil {
			return err
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, &StoreError{TenantID: tenantID, Op: "list", Err: err}
	}
	s.resolveNames(ctx, out...)
	return out, nil
}

// GetByName returns the tenant's placement entry for the database named
// name. The table is keyed by the opaque database id, so this scans the
// tenant's entries for the one whose record names the database; a caller
// that holds the id uses Get. It returns ErrPlacementNotFound when no entry
// names the database.
//
// A drop removes the catalog record before it deletes the placement entry,
// and that delete can fail, so a fenced (Dropping) entry can outlive its
// database and share its name with a re-created one. GetByName therefore
// prefers the live (not Dropping) entry; it returns a Dropping entry only
// when no live one names the database, so a resumed drop still finds its
// own entry. More than one live entry naming the database fails closed with
// ErrAmbiguousPlacement rather than picking one by scan order.
func (s *Store) GetByName(ctx context.Context, tenantID, name string) (*Placement, error) {
	if name == "" {
		return nil, &StoreError{TenantID: tenantID, Op: "get", Err: ErrEmptyDatabase}
	}
	if name == tenant.HomeName {
		// The home group's id is its name, so its entry is read by key.
		// Listing the tenant would resolve every other entry's name through
		// the tenant's catalog, which for an isolated tenant lives in this
		// very home: locating the home must not need the home.
		return s.Get(ctx, tenantID, tenant.HomeName)
	}
	placements, err := s.ListForTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var live, dropping *Placement
	fenced := 0
	for _, p := range placements {
		if p.Database != name {
			continue
		}
		if p.Dropping {
			fenced++
			if dropping == nil {
				dropping = p
			}
			continue
		}
		if live != nil {
			return nil, &StoreError{TenantID: tenantID, DatabaseID: name, Op: "get", Err: ErrAmbiguousPlacement}
		}
		live = p
	}
	if live != nil {
		return live, nil
	}
	// With no live entry, two fenced entries under the name leave no way to
	// tell which database's shard a caller would retire: refuse rather than
	// act on whichever the listing returned first.
	if fenced > 1 {
		return nil, &StoreError{TenantID: tenantID, DatabaseID: name, Op: "get", Err: ErrAmbiguousPlacement}
	}
	if dropping != nil {
		return dropping, nil
	}
	return nil, &StoreError{TenantID: tenantID, Op: "get", Err: ErrPlacementNotFound}
}

// GetBound returns the placement entry of the database a catalog record
// names: the entry keyed by its database id, never one found by the name. A
// dropped predecessor whose entry outlived its drop shares the name, so a
// lookup that must act on exactly this database -- a drop's fence above all --
// keys by the id. An entry under the id that records another database id or
// name is refused with *IdentityMismatchError; a missing entry is
// ErrPlacementNotFound, as from Get.
func (s *Store) GetBound(ctx context.Context, tenantID, databaseID, name string) (*Placement, error) {
	p, err := s.Get(ctx, tenantID, databaseID)
	if err != nil {
		return nil, err
	}
	if p.DatabaseID != databaseID || p.Database != name {
		return nil, &IdentityMismatchError{
			TenantID: tenantID, Database: name, DatabaseID: databaseID,
			GotDatabaseID: p.DatabaseID, GotDatabase: p.Database,
		}
	}
	return p, nil
}

// Watch returns a typed placement change stream when the backing KV implements
// Watcher; otherwise it returns ErrWatchUnsupported. The returned channel is
// closed when ctx is cancelled or the underlying feed ends.
func (s *Store) Watch(ctx context.Context) (<-chan Event, error) {
	w, ok := s.kv.(Watcher)
	if !ok {
		return nil, ErrWatchUnsupported
	}
	src, err := w.Watch(ctx, PlacementPrefix)
	if err != nil {
		return nil, &StoreError{Op: "watch", Err: err}
	}
	out := make(chan Event)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev, more := <-src:
				if !more {
					return
				}
				event, ok := s.translateEvent(ctx, ev)
				if !ok {
					continue
				}
				select {
				case <-ctx.Done():
					return
				case out <- event:
				}
			}
		}
	}()
	return out, nil
}

// translateEvent converts a backend WatchEvent into a placement Event. It
// reports false when the event carries no decodable placement identity.
func (s *Store) translateEvent(ctx context.Context, ev WatchEvent) (Event, bool) {
	if ev.Deleted {
		if len(ev.PrevValue) > 0 {
			if p, err := decodePlacement(ev.PrevValue); err == nil {
				s.resolveNames(ctx, p)
				return Event{Type: EventDelete, TenantID: p.TenantID, DatabaseID: p.DatabaseID, Database: p.Database, ShardID: p.ShardID}, true
			}
		}
		tenantID, databaseID, ok := splitPlacementKey(ev.Key)
		if !ok {
			return Event{}, false
		}
		return Event{Type: EventDelete, TenantID: tenantID, DatabaseID: databaseID}, true
	}
	if len(ev.Value) == 0 {
		return Event{}, false
	}
	p, err := decodePlacement(ev.Value)
	if err != nil {
		return Event{}, false
	}
	s.resolveNames(ctx, p)
	return Event{Type: EventPut, TenantID: p.TenantID, DatabaseID: p.DatabaseID, Database: p.Database, ShardID: p.ShardID, Placement: p}, true
}

// splitPlacementKey parses a placement key back into its (tenant, databaseID)
// components. It reports false when key is not a placement key or is malformed.
func splitPlacementKey(key string) (tenantID, databaseID string, ok bool) {
	if len(key) < len(PlacementPrefix) || key[:len(PlacementPrefix)] != PlacementPrefix {
		return "", "", false
	}
	rest := key[len(PlacementPrefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			return rest[:i], rest[i+1:], rest[:i] != "" && rest[i+1:] != ""
		}
	}
	return "", "", false
}
