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

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Compile-time interface check.
var _ kvstore.KVStore = (*kvStoreAdapter)(nil)

// ErrNoTransactionService is returned by kvSealBackend.PutIfAbsent when the
// node was built without a TransactionServicer. Every lifecycle-built node
// wires one; this error only surfaces on a hand-rolled construction.
var ErrNoTransactionService = errors.New("lifecycle: transaction service required for PutIfAbsent")

// kvSealBackend adapts a kvstore.KVStore to a seal.StorageBackend by adding a
// no-op Close. The underlying KV is owned by the node lifecycle, so closing it
// here would be incorrect; the StorageBackend contract's Close is satisfied as
// a no-op. This lets KV-backed services (custodian groups, Shamir shares) reuse
// the replicated KV store through the seal.StorageBackend interface.
//
// tx provides the Raft-consensus compare-and-set used by PutIfAbsent. The
// wrapped KV is the replicated store, so an in-process lock cannot make a
// check-then-write atomic across nodes — the transaction service can.
type kvSealBackend struct {
	kvstore.KVStore
	tx service.TransactionServicer
}

// Close is a no-op: the wrapped KV store is owned by the node lifecycle.
func (kvSealBackend) Close() error { return nil }

// PutIfAbsent atomically stores data under key only if the key is absent,
// using a transaction applied through Raft consensus. The precondition
// "key version == 0" holds exactly when the key has never been written, so
// the Put executes only for the first writer; every concurrent writer on any
// node observes Succeeded=false. This gives linearizable cross-node
// uniqueness — the guarantee an in-process lock cannot provide.
func (b kvSealBackend) PutIfAbsent(ctx context.Context, key string, data []byte) (bool, error) {
	if b.tx == nil {
		return false, ErrNoTransactionService
	}
	resp, err := b.tx.Execute(ctx,
		[]service.Compare{{
			Key:     []byte(key),
			Target:  service.CompareVersion,
			Result:  service.CompareEqual,
			Version: 0, // version 0 ⇒ key has never been written (absent)
		}},
		[]service.Op{{
			Type:  service.OpPut,
			Key:   []byte(key),
			Value: data,
		}},
		nil,
	)
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected, expressed as the transaction service's own compare-and-execute:
// the comparison runs inside the state machine, which is the cross-node
// guarantee a read followed by a delete cannot give.
func (b kvSealBackend) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if b.tx == nil {
		return false, ErrNoTransactionService
	}
	resp, err := b.tx.Execute(ctx,
		[]service.Compare{{
			Key:    []byte(key),
			Target: service.CompareValue,
			Result: service.CompareEqual,
			Value:  expected,
		}},
		[]service.Op{{
			Type: service.OpDelete,
			Key:  []byte(key),
		}},
		nil,
	)
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// Get adapts the kvstore.KVStore missing-key convention (nil, nil) to the
// seal.StorageBackend contract, which signals a missing key with
// seal.ErrSecretNotFound. Services built on StorageBackend (custodian groups,
// shares) rely on a not-found error to distinguish "absent" from "empty value".
func (b kvSealBackend) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := b.KVStore.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, seal.ErrSecretNotFound
	}
	return data, nil
}

// NewKVStoreAdapter returns a kvstore.KVStore backed by the given KVServicer.
// It is the exported form of newKVStoreAdapter, used by daemon/composition-root
// wiring (cmd/qrdbd) that needs a replicated KV handle — e.g. binding the
// volume pool's replicated cursor store — without reaching into lifecycle
// internals. A missing key reads back as (nil, nil) per the kvstore contract.
// The returned store carries NO index subsystem, so its index operations
// return kvstore.ErrIndexUnsupported. That is correct for its callers — the
// volume pool's replicated cursor is a single cluster-wide scalar with
// nothing to index — and it is honest: a DAO built over it plans full scans
// rather than reading an index that was never maintained. Callers that need
// indexes take the store from Node.StoreResolver, which carries one.
func NewKVStoreAdapter(svc service.KVServicer) kvstore.KVStore {
	return newKVStoreAdapter(svc, nil)
}

// NewReplicatedSealBackend returns a seal.StorageBackend backed by the
// replicated system KV. Its PutIfAbsent uses a Raft transaction for
// cross-node-atomic compare-and-set, which the volume pool's cluster-wide
// slot-assignment record relies on (first writer wins). tx is required for
// PutIfAbsent; Get returns seal.ErrSecretNotFound for a missing key.
func NewReplicatedSealBackend(svc service.KVServicer, tx service.TransactionServicer) seal.StorageBackend {
	return kvSealBackend{KVStore: newKVStoreAdapter(svc, nil), tx: tx}
}

// kvStoreAdapter adapts service.KVServicer to the kvstore.KVStore interface
// so that internal stores (user, role, cert) — and any DAO a consumer builds
// over a StoreResolver-returned store — can use the replicated KV service for
// storage.
//
// idx carries the node's IndexServicer, which reaches the SAME KVEngine that
// backs svc on the same shard. That is what makes secondary indexes real
// here rather than a second index implementation layered on top: the engine
// already maintains every registered index inside the Raft-applied write
// batch (pkg/engine/kv_engine.go handleKVPut → UpdateIndexesBatch) and
// backfills on registration, so the adapter only has to route registration
// and lookups to it. Index entries are therefore replicated, crash-safe, and
// atomic with the data write — none of which a KV-level mirror-key scheme
// could offer.
//
// When idx is nil the adapter has no index subsystem to route to and every
// index method returns kvstore.ErrIndexUnsupported. It never returns an empty
// result set to mean "cannot": see kvstore.ErrIndexUnsupported.
type kvStoreAdapter struct {
	svc service.KVServicer
	idx service.IndexServicer
}

// Compile-time proof that the adapter offers the atomic capability. The
// engine has a native compare-and-set on every backend, so refusing here
// would be a lie in the opposite direction.
var _ kvstore.AtomicKVStore = (*kvStoreAdapter)(nil)

// PutIfAbsent atomically stores value under key only when key is absent,
// delegating to the KV service's own primitive. Every engine implements that
// natively — Raft evaluates the condition inside the state machine so
// replicas cannot diverge — which is the cross-node guarantee an in-process
// check-then-put cannot provide.
func (a *kvStoreAdapter) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return a.svc.PutIfAbsent(ctx, key, value)
}

// CompareAndDelete removes key only when its stored value still equals
// expected, delegating to the KV service's own guarded delete.
func (a *kvStoreAdapter) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return a.svc.CompareAndDelete(ctx, key, expected)
}

// Put stores a key-value pair through the KV service.
func (a *kvStoreAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.svc.Put(ctx, key, value)
}

// Get retrieves a value by key. Returns nil, nil when the key does not
// exist, matching the kvstore.KVStore contract.
func (a *kvStoreAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := a.svc.Get(ctx, key)
	if err != nil {
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// Delete removes a key through the KV service.
func (a *kvStoreAdapter) Delete(ctx context.Context, key string) error {
	return a.svc.Delete(ctx, key)
}

// Scan iterates over key-value pairs matching prefix, calling fn for each.
func (a *kvStoreAdapter) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	pairs, err := a.svc.Scan(ctx, prefix)
	if err != nil {
		return err
	}
	for k, v := range pairs {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

// List returns all keys matching the given prefix.
func (a *kvStoreAdapter) List(ctx context.Context, prefix string) ([]string, error) {
	return a.svc.List(ctx, prefix)
}

// Exists reports whether the given key exists.
func (a *kvStoreAdapter) Exists(ctx context.Context, key string) (bool, error) {
	data, err := a.svc.Get(ctx, key)
	if err != nil {
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			return false, nil
		}
		return false, err
	}
	return data != nil, nil
}

// RegisterEntityIndexes creates one engine-backed secondary index per
// definition, named "{entityType}.{FieldName}" to match both the DAO's
// lookup name (pkg/dao indexName) and the engine's own convention
// (KVEngine.buildIndexName). Creation goes through Raft, is idempotent, and
// backfills the index from the keys already under "{entityType}/".
//
// A compound definition is REFUSED with kvstore.ErrIndexUnsupported, not
// skipped. IndexServicer carries no compound creation verb, so this adapter
// can neither create nor query one — and skipping while returning nil would
// tell the caller its compound indexes exist. pkg/dao would then advertise
// them to the planner, route a query to QueryCompoundIndex, and get a refusal
// where a full scan would have been correct. Registration is the only point
// at which that can be reported honestly.
func (a *kvStoreAdapter) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	if a.idx == nil {
		return kvstore.ErrIndexUnsupported
	}
	ctx := context.Background()
	for _, ei := range indexes {
		if ei.IsCompound() {
			return kvstore.ErrIndexUnsupported
		}
		// A case-INSENSITIVE definition is refused for the same reason a
		// compound one is: this path cannot honour it.
		// service.IndexServicer.CreateIndex encodes its request with
		// protocol.EncodeIndexCreate, whose wire format carries name, field,
		// unique, sparse and numeric — and no case-sensitivity flag — so
		// KVEngine.handleIndexCreate builds every index with
		// CaseSensitive:true. Accepting the definition would create a
		// case-SENSITIVE index while telling the DAO it had a case-insensitive
		// one, and a lookup for "Alpha" over a stored "alpha" would come back
		// empty with a nil error.
		//
		// Carrying the flag through the Raft command encoding is the real fix
		// and is deliberately not done here: it changes a command format that
		// existing Raft logs replay through. Tracked as #34; until then the
		// DAO plans a correct full scan.
		//
		// This only fires for an explicit `index:"case_insensitive"` tag —
		// index.ParseIndexTags defaults CaseSensitive to true, so ordinary
		// `index:"true"` and `index:"unique"` fields keep their real index.
		if !ei.CaseSensitive {
			return kvstore.ErrIndexUnsupported
		}
		cfg := index.IndexConfig{Unique: ei.Unique, Sparse: ei.Sparse}
		if ei.Unique {
			cfg.Type = index.IndexTypeUnique
		}
		// The engine decides which indexes a write maintains by matching the
		// index NAME against the key's entity type: KVEngine.filterIndexes
		// accepts "{entityType}." and, when the engine has a database
		// namespace, "{engineDatabase}.{entityType}.". Nothing else is ever
		// updated.
		//
		// This adapter cannot see the engine's database setting, so it cannot
		// verify that a caller-qualified name — what a DAO built with
		// WithDatabase supplies — is one the engine will maintain. Creating
		// it anyway would succeed, leave the index permanently un-updated,
		// and answer every query on it empty with a nil error. That is the
		// exact failure this change exists to remove, so a name this adapter
		// cannot vouch for is refused and the DAO plans a correct full scan.
		//
		// Threading the engine's database scope through so the qualified form
		// can be honoured is tracked as #38.
		name := ei.ResolvedName(entityType)
		if name != entityType+"."+ei.FieldName {
			return kvstore.ErrIndexUnsupported
		}
		if err := a.idx.CreateIndex(ctx, name, ei.JSONFieldName, cfg); err != nil {
			return &kvstore.IndexRegistrationError{EntityType: entityType, Err: err}
		}
	}
	return nil
}

// QueryIndex performs an exact-match lookup through the engine's index
// subsystem and returns the matching entity keys.
func (a *kvStoreAdapter) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if a.idx == nil {
		return nil, kvstore.ErrIndexUnsupported
	}
	return a.idx.Query(ctx, indexName, value)
}

// ScanIndex performs a range scan over the named index for values in the
// half-open interval [start, end).
func (a *kvStoreAdapter) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if a.idx == nil {
		return nil, kvstore.ErrIndexUnsupported
	}
	return a.idx.Scan(ctx, indexName, start, end)
}

// QueryCompoundIndex refuses: service.IndexServicer exposes no compound
// index operations, so this adapter can neither create nor query one. It
// returns kvstore.ErrIndexUnsupported rather than an empty result, so a DAO
// plans a full scan instead of concluding the entity does not exist.
func (a *kvStoreAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanCompoundIndex refuses; see QueryCompoundIndex.
func (a *kvStoreAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
