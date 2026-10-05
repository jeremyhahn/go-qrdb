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
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
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

// PutIfAllAbsent stores data under key only while key and every fence key are
// absent, as ONE transaction on the consensus plane; see putIfAllAbsent.
func (b kvSealBackend) PutIfAllAbsent(ctx context.Context, key string, data []byte, fences ...string) (bool, error) {
	return putIfAllAbsent(ctx, b.tx, key, data, fences)
}

// putIfAllAbsent is kvstore.FencedCreator on the transaction plane: one
// "version == 0" compare per key -- the created key and each fence -- guarding
// a single put. Every compare and the put are evaluated inside the state
// machine in one revision, so no writer on any node can land between the
// absence checks and the write, and every replica decides identically.
func putIfAllAbsent(ctx context.Context, tx service.TransactionServicer, key string, data []byte, fences []string) (bool, error) {
	if tx == nil {
		return false, ErrNoTransactionService
	}
	compares := make([]service.Compare, 0, len(fences)+1)
	for _, k := range append([]string{key}, fences...) {
		compares = append(compares, service.Compare{
			Key:     []byte(k),
			Target:  service.CompareVersion,
			Result:  service.CompareEqual,
			Version: 0, // version 0 ⇒ the key is absent
		})
	}
	resp, err := tx.Execute(ctx, compares,
		[]service.Op{{Type: service.OpPut, Key: []byte(key), Value: data}},
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
//
// Guarded on EXISTENCE as well as value — see existsGuard for why a value
// compare alone is not enough.
func (b kvSealBackend) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if b.tx == nil {
		return false, ErrNoTransactionService
	}
	resp, err := b.tx.Execute(ctx,
		[]service.Compare{existsGuard(key), {
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

// CompareAndSwap replaces key's value only when its stored value still equals
// expected, decided on the transaction plane so every replica agrees.
//
// The guard is the same pair the guarded delete uses; only the success op
// differs. Both halves are required: see existsGuard.
func (b kvSealBackend) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if b.tx == nil {
		return false, ErrNoTransactionService
	}
	resp, err := b.tx.Execute(ctx,
		[]service.Compare{existsGuard(key), {
			Key:    []byte(key),
			Target: service.CompareValue,
			Result: service.CompareEqual,
			Value:  expected,
		}},
		[]service.Op{{
			Type:  service.OpPut,
			Key:   []byte(key),
			Value: next,
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

// fencedKVStoreAdapter is kvStoreAdapter plus the transaction plane, which is
// what lets it offer kvstore.FencedCreator. It is a DISTINCT type rather than
// a field on kvStoreAdapter so a store built without a transaction servicer
// does not satisfy the capability at all: a capability present in the method
// set and absent in fact is the failure capability assertions exist to catch.
//
// The seal-disabled tenant service is built over it, so creating a tenant
// commits its metadata only while the tenant's tombstone is also absent,
// exactly as the seal-enabled service does through kvSealBackend.
type fencedKVStoreAdapter struct {
	*kvStoreAdapter
	tx service.TransactionServicer
}

// Compile-time proofs of the capabilities the fenced adapter offers.
var (
	_ kvstore.AtomicKVStore = (*fencedKVStoreAdapter)(nil)
	_ kvstore.FencedCreator = (*fencedKVStoreAdapter)(nil)
	_ kvstore.FencedCreator = kvSealBackend{}
)

// PutIfAllAbsent stores value under key only while key and every fence key
// are absent, as one transaction; see putIfAllAbsent.
func (a *fencedKVStoreAdapter) PutIfAllAbsent(ctx context.Context, key string, value []byte, fences ...string) (bool, error) {
	return putIfAllAbsent(ctx, a.tx, key, value, fences)
}

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

// CompareAndSwap replaces key's value only when it still equals expected.
func (a *kvStoreAdapter) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return a.svc.CompareAndSwap(ctx, key, expected, next)
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
		// A case-INSENSITIVE definition used to be refused here: the
		// OpIndexCreate wire format carried no case-sensitivity flag, so
		// KVEngine.handleIndexCreate built every index with
		// CaseSensitive:true and accepting the definition would have produced
		// a case-SENSITIVE index while telling the DAO it had a
		// case-insensitive one -- a lookup for "Alpha" over a stored "alpha"
		// coming back empty with a nil error. #34 added
		// protocol.IndexFlagCaseInsensitive, so the definition now reaches
		// the engine intact and the refusal is gone.
		//
		// CaseSensitive is passed through explicitly rather than left zero:
		// index.IndexConfig names the field for the DEFAULT, so its zero
		// value is case-INSENSITIVE, which the engine now honours.
		name := ei.ResolvedName(entityType)
		// The engine maintains an index for exactly the keys under its SCOPE,
		// which it composes as engine.IndexNamespace(name) followed by the
		// key prefix declared at creation (engine.ComposeIndexScope). The
		// store contract, on the other hand, declares the ABSOLUTE prefix the
		// caller's keys carry (kvstore.EntityIndex.KeyPrefix): a namespaced
		// tenant store declares "{tenant}/{entityType}/" and names the index
		// "{tenant}/{entityType}.{Field}".
		//
		// So the prefix handed to the engine is the absolute one made
		// RELATIVE to whatever namespace the name carries, and the scope the
		// engine then maintains is precisely the absolute prefix -- for a
		// shared-prefix tenant, a per-(tenant, database) name, a DAO built
		// WithDatabase, and a bare entity type alike. Nothing about the
		// tenant or database is assumed here; both are read off what the
		// caller declared.
		//
		// A name whose namespace the keys do NOT live under is refused: the
		// engine would maintain the index over a keyspace no write of this
		// entity type reaches, create it successfully, and answer every query
		// on it empty with a nil error -- the silent "no records" the store
		// contract forbids.
		absolute := ei.ResolvedPrefix(entityType)
		ns := engine.IndexNamespace(name)
		if !strings.HasPrefix(absolute, ns) {
			return kvstore.ErrIndexUnsupported
		}
		cfg := index.IndexConfig{
			Unique:        ei.Unique,
			Sparse:        ei.Sparse,
			CaseSensitive: ei.CaseSensitive,
			// The key prefix this index covers, relative to the name's
			// namespace; see above (go-qrdb#410, go-qrdb#40).
			KeyPrefix: absolute[len(ns):],
		}
		if ei.Unique {
			cfg.Type = index.IndexTypeUnique
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

// existsGuard refuses a key that is absent, as a compare the state machine
// evaluates alongside the value compare.
//
// A value compare ALONE does not do it, and the reason is easy to miss: an
// absent key's value is nil, and nil compares equal to an empty slice. So
// CompareAndSwap(key, []byte{}, next) on an absent key satisfies the value
// guard and executes its OpPut — turning a swap into a create, which the
// AtomicKVStore contract forbids. CompareAndDelete has the same hole, where it
// reports having deleted a key that was never there.
//
// pkg/store's evaluator states the behaviour outright ("Key does not exist:
// only equal-to-zero-length succeeds"), so this was documented as true while a
// comment here claimed the opposite (go-qrdb#56).
//
// Version is the target because both state machines carry it for an absent
// key: the MVCC store reports the key's real version, and the pebble store
// degrades honestly to 0 absent and 1 present. Create/mod revision would not
// work — pebble keeps no revisions and rejects those compares up front.
func existsGuard(key string) service.Compare {
	return service.Compare{
		Key:     []byte(key),
		Target:  service.CompareVersion,
		Result:  service.CompareGreater,
		Version: 0,
	}
}

// ErrTransactionServiceRequired is returned by NewGuardedKVStoreAdapter when
// asked to build a guarded store with no transaction plane to back it.
//
// It is a construction-time refusal, not a runtime fallback — see
// cmd/qrdbd/kvstore_adapter.go's identical sentinel and reasoning, which
// this package's guarded adapter exists to match: every composer that
// builds role/user stores (cmd/qrdbd AND, through this package,
// sdk/go/node) gets the SAME guarded-adapter capability, so a caller that
// needs kvstore.GuardedBatchKVStore refuses at startup here exactly as it
// does there, rather than discovering the gap silently.
var ErrTransactionServiceRequired = errors.New("lifecycle: transaction service required for a guarded kvstore")

// guardedKVStoreAdapter is kvStoreAdapter plus the transaction plane, which
// is what lets the returned store satisfy kvstore.GuardedBatchKVStore.
//
// Separate type from kvStoreAdapter for the same reason
// cmd/qrdbd/kvstore_adapter.go's guardedKVServiceAdapter is separate from
// its plain sibling: the GuardedBatchKVStore assertion a caller performs
// must answer honestly, which means the method exists on the type ONLY
// when a transaction plane actually backs it.
type guardedKVStoreAdapter struct {
	*kvStoreAdapter
	tx service.TransactionServicer
}

// Compile-time interface check.
var _ kvstore.MultiGuardedBatchKVStore = (*guardedKVStoreAdapter)(nil)

// NewGuardedKVStoreAdapter returns a kvstore.KVStore backed by svc (and idx,
// for index operations — see newKVStoreAdapter), with the additional
// kvstore.MultiGuardedBatchKVStore capability tx's transaction plane
// provides -- the static return type documents the capability directly,
// rather than making every caller type-assert for what construction already
// guarantees.
//
// Every node BuildServices constructs wires a TransactionServicer
// unconditionally (n.Transaction, above), so tx is never unavailable for a
// production caller — this exists so a store that DOES need atomic
// multi-key commits (go-qrdb#555's atomic role/user creation; cakey's
// split key/certificate records) can get one, without every OTHER caller of
// newKVStoreAdapter paying for a capability it has no use for.
func NewGuardedKVStoreAdapter(svc service.KVServicer, idx service.IndexServicer, tx service.TransactionServicer) (kvstore.MultiGuardedBatchKVStore, error) {
	if tx == nil {
		return nil, ErrTransactionServiceRequired
	}
	return &guardedKVStoreAdapter{
		kvStoreAdapter: &kvStoreAdapter{svc: svc, idx: idx},
		tx:             tx,
	}, nil
}

// PutAllIfUnchanged applies every write atomically, and only while guardKey
// still holds guardValue: PutAllIfAllUnchanged with a single guard.
func (a *guardedKVStoreAdapter) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return a.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged applies every write atomically, and only while EVERY
// guard's key still holds its expected value. See ExecuteGuardedBatch, which
// this delegates to.
func (a *guardedKVStoreAdapter) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	return ExecuteGuardedBatch(ctx, a.tx, guards, writes)
}

// ExecuteGuardedBatch is the one implementation of
// kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged over a
// service.TransactionServicer. Every guarded adapter in the tree delegates to
// it — this package's guardedKVStoreAdapter and cmd/qrdbd's
// guardedKVServiceAdapter — so the guard encoding and the error vocabulary
// cannot drift between composers.
//
// It proposes ONE transaction: the compares guardCompares derives from
// guards, with writes as its success operations -- a put for each KV, or a
// delete when KV.Delete is set, so a batch can remove one key and write
// others in the same atomic step a move/rename needs (go-qrdb#589). The
// compares and the writes are evaluated together inside the state machine,
// in a single revision, which is the whole point: a caller doing this with
// single-key verbs — check, write, check, write — still loses if a guard
// changes between one check and its write (go-qrdb#21, go-qrdb#555).
//
// A transaction whose compare fails is a RESULT, not an error: applied=false
// with a nil error, and the store is unchanged. A non-nil error means the
// outcome is unknown, except for the refusals that PROVE nothing was
// written, which are translated into kvstore's vocabulary for callers that
// cannot import the service layer:
//
//   - a nil tx (wrapping ErrTransactionServiceRequired) and a state machine
//     built without a transaction subsystem become
//     kvstore.GuardedBatchUnsupportedError, which kvstore.ProbeGuardedBatch
//     reads as "this node cannot";
//   - any other error the service layer marks service.ErrNotStored — a
//     propose-time NotLeader, most commonly — becomes kvstore.NotAppliedError.
//
// Both match kvstore.ErrNotApplied, which is what lets an ambiguous-outcome
// reconciler return at once instead of waiting for a write that cannot
// appear.
func ExecuteGuardedBatch(ctx context.Context, tx service.TransactionServicer, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	if tx == nil {
		return false, &kvstore.GuardedBatchUnsupportedError{Err: ErrTransactionServiceRequired}
	}

	// tx.Execute refuses an empty key too (service.InvalidKeyError, wrapped
	// in service.EncodeRequestError) -- but a caller that holds this
	// function only through its kvstore.MultiGuardedBatchKVStore view (every
	// server-side composition that hands out a kvstore.KVStore rather than a
	// service.TransactionServicer) cannot import pkg/service to name that
	// refusal. Checking here first gives it the kvstore-layer type instead.
	for i, g := range guards {
		if g.Key == "" {
			return false, &kvstore.EmptyKeyError{Field: "guard", Index: i}
		}
	}
	for i, w := range writes {
		if w.Key == "" {
			return false, &kvstore.EmptyKeyError{Field: "write", Index: i}
		}
	}

	ops := make([]service.Op, 0, len(writes))
	for _, w := range writes {
		if w.Delete {
			ops = append(ops, service.Op{Type: service.OpDelete, Key: []byte(w.Key)})
			continue
		}
		ops = append(ops, service.Op{
			Type:  service.OpPut,
			Key:   []byte(w.Key),
			Value: w.Value,
		})
	}
	resp, err := tx.Execute(ctx, guardCompares(guards), ops, nil)
	if err != nil {
		var unsupported *service.CapabilityUnsupportedError
		if errors.As(err, &unsupported) {
			return false, &kvstore.GuardedBatchUnsupportedError{Err: err}
		}
		if errors.Is(err, service.ErrNotStored) {
			return false, &kvstore.NotAppliedError{Err: err}
		}
		return false, err
	}
	return resp.Succeeded, nil
}

// guardCompares translates each kvstore.Guard into the service.Compare the
// state machine actually needs to enforce it.
//
// A nil Guard.Value ("must be absent") is deliberately NOT expressed as a
// value compare against nil/empty. Every engine here degrades an absent
// key's value to nil for a value comparison (pebbleEvaluateCompare: "Key
// does not exist: only equal-to-zero-length succeeds"), which means
// bytes.Compare(nil, nil) == 0 succeeds for an ABSENT key — but so does a
// PRESENT key whose stored value happens to be an empty slice. That is a
// real, reachable state for a malformed or pre-migration record, and a
// value compare cannot tell the two apart. Version can: every engine here
// reports version/revision 0 only for a key that has never been written
// (pkg/mvcc's RaftCompare.evaluate: "missing key means version...0"; pebble
// degrades the same way), so a nil guard is expressed as a version compare
// instead, matching the existsGuard/PutIfAbsent pattern already used
// elsewhere in this file.
//
// A non-nil Guard.Value carries the SAME hole in the opposite direction, and
// it is not hypothetical: both engines' value compare treats an absent key's
// value as equal to an empty slice, exactly as they do for a nil guard above.
// Guard{Value: []byte{}} means "the key must be PRESENT and hold an empty
// value" — but a value compare alone cannot refuse an absent key, because
// bytes.Compare(nil, []byte{}) == 0 there too. So every non-nil-Value guard
// gets existsGuard's version compare ADDED ahead of its value compare, not
// substituted for it: the transaction only proposes when every compare in the
// slice holds, so requiring both closes the gap for the empty-expected case
// without changing anything for a guard expecting a non-empty value, which
// existence already implied.
func guardCompares(guards []kvstore.Guard) []service.Compare {
	compares := make([]service.Compare, 0, len(guards)*2)
	for _, g := range guards {
		if g.Value == nil {
			compares = append(compares, service.Compare{
				Key:     []byte(g.Key),
				Target:  service.CompareVersion,
				Result:  service.CompareEqual,
				Version: 0,
			})
			continue
		}
		compares = append(compares, existsGuard(g.Key), service.Compare{
			Key:    []byte(g.Key),
			Target: service.CompareValue,
			Result: service.CompareEqual,
			Value:  g.Value,
		})
	}
	return compares
}
