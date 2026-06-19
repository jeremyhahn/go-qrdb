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
)

// PlacementPrefix is the key prefix under which placement records live in the
// replicated system KV. It uses qrdb's "__sys:" system namespace so the entries
// are hidden from user-facing KV list/scan (which filter that prefix) and kept
// out of the tenant key space, exactly as the cluster-membership store does.
// Each database's placement is stored at
// "__sys:placement/{tenantID}/{database}".
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

	// TenantID and Database identify the affected placement.
	TenantID string
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

// placementKey returns the storage key for a (tenant, database) placement.
func placementKey(tenantID, database string) string {
	return PlacementPrefix + tenantID + "/" + database
}

// Put stores (or replaces) the placement for its (TenantID, Database). The
// placement's TenantID and Database must be set.
func (s *Store) Put(ctx context.Context, p *Placement) error {
	if p == nil {
		return &StoreError{Op: "put", Err: ErrPlacementNotFound}
	}
	if p.TenantID == "" {
		return &StoreError{Database: p.Database, Op: "put", Err: ErrEmptyTenant}
	}
	if p.Database == "" {
		return &StoreError{TenantID: p.TenantID, Op: "put", Err: ErrEmptyDatabase}
	}
	data, err := json.Marshal(p)
	if err != nil {
		return &StoreError{TenantID: p.TenantID, Database: p.Database, Op: "put", Err: err}
	}
	if err := s.kv.Put(ctx, placementKey(p.TenantID, p.Database), data); err != nil {
		return &StoreError{TenantID: p.TenantID, Database: p.Database, Op: "put", Err: err}
	}
	return nil
}

// Get returns the placement for (tenantID, database). It returns a *StoreError
// wrapping ErrPlacementNotFound when no entry exists.
func (s *Store) Get(ctx context.Context, tenantID, database string) (*Placement, error) {
	if tenantID == "" {
		return nil, &StoreError{Database: database, Op: "get", Err: ErrEmptyTenant}
	}
	if database == "" {
		return nil, &StoreError{TenantID: tenantID, Op: "get", Err: ErrEmptyDatabase}
	}
	raw, err := s.kv.Get(ctx, placementKey(tenantID, database))
	if err != nil {
		return nil, &StoreError{TenantID: tenantID, Database: database, Op: "get", Err: err}
	}
	if raw == nil {
		return nil, &StoreError{TenantID: tenantID, Database: database, Op: "get", Err: ErrPlacementNotFound}
	}
	var p Placement
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &StoreError{TenantID: tenantID, Database: database, Op: "get", Err: err}
	}
	return &p, nil
}

// Delete removes the placement for (tenantID, database). Deleting a missing
// entry is a no-op (it mirrors the underlying KV delete semantics).
func (s *Store) Delete(ctx context.Context, tenantID, database string) error {
	if tenantID == "" {
		return &StoreError{Database: database, Op: "delete", Err: ErrEmptyTenant}
	}
	if database == "" {
		return &StoreError{TenantID: tenantID, Op: "delete", Err: ErrEmptyDatabase}
	}
	if err := s.kv.Delete(ctx, placementKey(tenantID, database)); err != nil {
		return &StoreError{TenantID: tenantID, Database: database, Op: "delete", Err: err}
	}
	return nil
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
			return Event{Type: EventDelete, TenantID: p.TenantID, Database: p.Database}, true
		}
		tenantID, database, ok := splitPlacementKey(ev.Key)
		if !ok {
			return Event{}, false
		}
		return Event{Type: EventDelete, TenantID: tenantID, Database: database}, true
	}
	var p Placement
	if len(ev.Value) == 0 || json.Unmarshal(ev.Value, &p) != nil {
		return Event{}, false
	}
	return Event{Type: EventPut, TenantID: p.TenantID, Database: p.Database, Placement: &p}, true
}

// splitPlacementKey parses a placement key back into its (tenant, database)
// components. It reports false when key is not a placement key or is malformed.
func splitPlacementKey(key string) (tenantID, database string, ok bool) {
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
