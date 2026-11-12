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

	// Placement is the decoded placement for an EventPut; nil for an
	// EventDelete (or when the prior value could not be decoded).
	Placement *Placement
}

// Store persists and retrieves Placement records in the replicated system KV
// under PlacementPrefix. It is stateless beyond its KV reference and therefore
// safe for concurrent use.
type Store struct {
	kv KV
}

// NewStore constructs a Store over the given KV backend, which must be non-nil.
func NewStore(kv KV) (*Store, error) {
	if kv == nil {
		return nil, ErrNilKV
	}
	return &Store{kv: kv}, nil
}

// placementKey returns the storage key for a (tenant, databaseID) placement.
func placementKey(tenantID, databaseID string) string {
	return PlacementPrefix + tenantID + "/" + databaseID
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
	data, err := json.Marshal(p)
	if err != nil {
		return &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "put", Err: err}
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
	var p Placement
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &StoreError{TenantID: tenantID, DatabaseID: databaseID, Op: "get", Err: err}
	}
	return &p, nil
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
	atomicKV, ok := s.kv.(AtomicKV)
	if !ok {
		return false, &StoreError{TenantID: next.TenantID, DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: &AtomicWriteUnsupportedError{}}
	}
	var expectedBytes []byte
	if expected != nil {
		b, err := json.Marshal(expected)
		if err != nil {
			return false, &StoreError{TenantID: next.TenantID, DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: err}
		}
		expectedBytes = b
	}
	nextBytes, err := json.Marshal(next)
	if err != nil {
		return false, &StoreError{TenantID: next.TenantID, DatabaseID: next.DatabaseID, Op: "compare-and-swap", Err: err}
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
	creator, ok := s.kv.(PutIfAbsentKV)
	if !ok {
		return false, nil, &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "claim", Err: &AtomicWriteUnsupportedError{}}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return false, nil, &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "claim", Err: err}
	}
	stored, err := creator.PutIfAbsent(ctx, placementKey(p.TenantID, p.DatabaseID), data)
	if err != nil {
		return false, nil, &StoreError{TenantID: p.TenantID, DatabaseID: p.DatabaseID, Op: "claim", Err: err}
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
		var p Placement
		if err := json.Unmarshal(value, &p); err != nil {
			return err
		}
		out = append(out, &p)
		return nil
	})
	if err != nil {
		return nil, &StoreError{Op: "list", Err: err}
	}
	return out, nil
}

// ListForTenant returns every placement owned by the given tenant.
func (s *Store) ListForTenant(ctx context.Context, tenantID string) ([]*Placement, error) {
	if tenantID == "" {
		return nil, &StoreError{Op: "list", Err: ErrEmptyTenant}
	}
	var out []*Placement
	err := s.kv.Scan(ctx, PlacementPrefix+tenantID+"/", func(_ string, value []byte) error {
		var p Placement
		if err := json.Unmarshal(value, &p); err != nil {
			return err
		}
		out = append(out, &p)
		return nil
	})
	if err != nil {
		return nil, &StoreError{TenantID: tenantID, Op: "list", Err: err}
	}
	return out, nil
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
				event, ok := translateEvent(ev)
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
func translateEvent(ev WatchEvent) (Event, bool) {
	if ev.Deleted {
		var p Placement
		if len(ev.PrevValue) > 0 && json.Unmarshal(ev.PrevValue, &p) == nil {
			return Event{Type: EventDelete, TenantID: p.TenantID, DatabaseID: p.DatabaseID, Database: p.Database}, true
		}
		tenantID, databaseID, ok := splitPlacementKey(ev.Key)
		if !ok {
			return Event{}, false
		}
		return Event{Type: EventDelete, TenantID: tenantID, DatabaseID: databaseID}, true
	}
	var p Placement
	if len(ev.Value) == 0 || json.Unmarshal(ev.Value, &p) != nil {
		return Event{}, false
	}
	return Event{Type: EventPut, TenantID: p.TenantID, DatabaseID: p.DatabaseID, Database: p.Database, Placement: &p}, true
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
