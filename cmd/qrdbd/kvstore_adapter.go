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

package main

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// Compile-time interface checks. The adapter also carries the optional atomic
// conditional write, so callers that need exactly-once semantics — custodian
// registration claiming a username, invite redemption — get the Raft-native
// guarantee rather than being refused.
var (
	_ kvstore.KVStore       = (*kvServiceAdapter)(nil)
	_ kvstore.AtomicKVStore = (*kvServiceAdapter)(nil)
)

// kvServiceAdapter adapts service.KVServicer to the kvstore.KVStore interface
// so that internal stores (user, role, audit) can use Raft-replicated storage.
// It also implements the index methods of KVStore by delegating to an
// EntityRegisterer and LookupFunc when provided.
type kvServiceAdapter struct {
	svc        service.KVServicer
	registerer kvstore.EntityRegisterer
	lookup     kvstore.LookupFunc
}

// newKVServiceAdapter creates a kvstore.KVStore backed by a Raft-replicated
// KVServicer. All writes are proposed through Raft consensus; all reads are
// linearizable. The optional registerer and lookup enable index operations.
func newKVServiceAdapter(svc service.KVServicer, registerer kvstore.EntityRegisterer, lookup kvstore.LookupFunc) kvstore.KVStore {
	return &kvServiceAdapter{
		svc:        svc,
		registerer: registerer,
		lookup:     lookup,
	}
}

// newGuardedKVServiceAdapter is newKVServiceAdapter plus the transaction plane,
// which is what lets the returned store satisfy kvstore.GuardedBatchKVStore.
//
// Separate constructor rather than a fourth argument on the existing one: every
// other call site builds a store that has no use for multi-key guarding, and a
// nil transaction servicer threaded through them all would be a capability that
// looks present at the type level and is absent at run time.
func newGuardedKVServiceAdapter(svc service.KVServicer, txSvc service.TransactionServicer, registerer kvstore.EntityRegisterer, lookup kvstore.LookupFunc) kvstore.KVStore {
	if txSvc == nil {
		// Fail CLOSED at construction: without a transaction plane this store
		// cannot honour the multi-key guarantee, so it must not advertise it.
		// Returning the plain adapter makes the capability assertion answer
		// honestly, and a caller that requires the guarantee — the custodian
		// service does — refuses at startup rather than discovering the gap at
		// the first enrolment.
		return newKVServiceAdapter(svc, registerer, lookup)
	}
	return &guardedKVServiceAdapter{
		kvServiceAdapter: &kvServiceAdapter{
			svc:        svc,
			registerer: registerer,
			lookup:     lookup,
		},
		txSvc: txSvc,
	}
}

// guardedKVServiceAdapter is the plain adapter plus the transaction plane.
//
// It is a DISTINCT type, and that is the whole point: PutAllIfUnchanged lives
// here rather than on kvServiceAdapter, so a store built without a transaction
// servicer does not satisfy kvstore.GuardedBatchKVStore at all. Hanging the
// method on the shared type would make every adapter answer the capability
// assertion truthfully at compile time and falsely at run time — a capability
// that is present in the method set and absent in fact, which is exactly the
// failure the assertion exists to prevent.
type guardedKVServiceAdapter struct {
	*kvServiceAdapter
	txSvc service.TransactionServicer
}

// PutAllIfUnchanged applies every write atomically, and only while guardKey
// still holds guardValue, by proposing ONE transaction: a value compare on the
// guard key, with the writes as its success operations.
//
// The compare and the writes are evaluated together inside the state machine,
// in a single revision, which is the whole point. A caller doing this with the
// single-key verbs — check ownership, write, check, write — still loses if the
// guard changes between one check and its write: the write lands anyway, on
// top of whoever now owns the key (go-qrdb#21).
//
// A transaction whose compare fails is a RESULT, not an error: the guard did
// not hold, nothing was written, and applied=false says so.
func (a *guardedKVServiceAdapter) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	if a.txSvc == nil {
		// Only reachable if someone constructs this type with a nil servicer.
		// A store that cannot honour the guarantee must say so rather than
		// panic or, worse, appear to have applied the write.
		return false, &service.SMNotConfiguredError{Message: "transaction service not configured"}
	}

	ops := make([]service.Op, 0, len(writes))
	for _, w := range writes {
		ops = append(ops, service.Op{
			Type:  service.OpPut,
			Key:   []byte(w.Key),
			Value: w.Value,
		})
	}

	resp, err := a.txSvc.Execute(ctx, []service.Compare{{
		Key:    []byte(guardKey),
		Target: service.CompareValue,
		Result: service.CompareEqual,
		Value:  guardValue,
	}}, ops, nil)
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// Put stores a key-value pair through the Raft-replicated KV service.
func (a *kvServiceAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.svc.Put(ctx, key, value)
}

// PutIfAbsent stores value under key only when the key is absent, reporting
// whether this call created it.
//
// The condition is evaluated by the KV service, not here: KVServicer.PutIfAbsent
// proposes a conditional command that every engine resolves natively — Raft
// inside the state machine, so replicas cannot diverge. A Get followed by a Put
// in this adapter would reintroduce exactly the window the operation exists to
// close.
func (a *kvServiceAdapter) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	return a.svc.PutIfAbsent(ctx, key, value)
}

// CompareAndDelete removes key only when its stored value still equals
// expected. As with PutIfAbsent the condition is evaluated by the KV service
// inside the state machine, never by a read and a delete in this adapter.
func (a *kvServiceAdapter) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	return a.svc.CompareAndDelete(ctx, key, expected)
}

// CompareAndSwap replaces key's value only when it still equals expected.
func (a *kvServiceAdapter) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	return a.svc.CompareAndSwap(ctx, key, expected, next)
}

// Get retrieves a value by key. Returns nil, nil when the key does not
// exist, matching the kvstore.KVStore contract.
func (a *kvServiceAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := a.svc.Get(ctx, key)
	if err != nil {
		// Translate KeyNotFoundError to nil, nil per kvstore contract.
		var knf *service.KeyNotFoundError
		if errors.As(err, &knf) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// Delete removes a key through the Raft-replicated KV service.
func (a *kvServiceAdapter) Delete(ctx context.Context, key string) error {
	return a.svc.Delete(ctx, key)
}

// Scan iterates over all key-value pairs matching the given prefix,
// calling fn for each pair. Return a non-nil error from fn to stop
// iteration.
func (a *kvServiceAdapter) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
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
func (a *kvServiceAdapter) List(ctx context.Context, prefix string) ([]string, error) {
	return a.svc.List(ctx, prefix)
}

// Exists reports whether the given key exists in the Raft-replicated store.
func (a *kvServiceAdapter) Exists(ctx context.Context, key string) (bool, error) {
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

// RegisterEntityIndexes delegates index registration to the engine-level
// EntityRegisterer.
//
// With no registerer configured this adapter maintains no indexes, so it
// returns kvstore.ErrIndexUnsupported rather than nil. Reporting success
// would tell a DAO its indexes exist, and every subsequent index query would
// then answer empty with no error — the silent "no records" the KVStore
// contract now forbids.
func (a *kvServiceAdapter) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	if a.registerer == nil {
		return kvstore.ErrIndexUnsupported
	}
	// A registerer without a lookup can create indexes it can never answer
	// from: QueryIndex and ScanIndex below both refuse when lookup is nil.
	// Reporting successful registration in that state is a false capability
	// statement — the caller would plan against an index it cannot read.
	if a.lookup == nil {
		return kvstore.ErrIndexUnsupported
	}
	for _, ei := range indexes {
		// Compound definitions are refused, because QueryCompoundIndex and
		// ScanCompoundIndex below refuse unconditionally — this adapter
		// exposes no compound operations at all. Forwarding the definition
		// and returning nil would tell the caller a compound index exists
		// that it can never query, which is the same false success in a
		// different place.
		if ei.IsCompound() {
			return kvstore.ErrIndexUnsupported
		}
	}
	return a.registerer.RegisterEntityType(entityType, indexes)
}

// QueryIndex performs an exact-match query on the named index by delegating
// to the lookup function with an IndexQueryRequest.
func (a *kvServiceAdapter) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if a.lookup == nil {
		return nil, kvstore.ErrIndexUnsupported
	}
	result, err := a.lookup(ctx, index.IndexQueryRequest{
		IndexName: indexName,
		Value:     value,
	})
	if err != nil {
		return nil, err
	}
	keys, ok := result.([][]byte)
	if !ok {
		return nil, &kvstore.IndexQueryError{IndexName: indexName, Reason: "unexpected result type"}
	}
	return keys, nil
}

// ScanIndex performs a range scan on the named index by delegating to the
// lookup function with an IndexScanRequest.
func (a *kvServiceAdapter) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if a.lookup == nil {
		return nil, kvstore.ErrIndexUnsupported
	}
	result, err := a.lookup(ctx, index.IndexScanRequest{
		IndexName: indexName,
		Start:     start,
		End:       end,
	})
	if err != nil {
		return nil, err
	}
	entries, ok := result.(map[string][][]byte)
	if !ok {
		return nil, &kvstore.IndexScanError{IndexName: indexName, Reason: "unexpected result type"}
	}
	return entries, nil
}

// QueryCompoundIndex refuses with kvstore.ErrIndexUnsupported: this adapter
// exposes no compound index operations. Returning (nil, nil) would be
// indistinguishable from a compound index with no matches, so a DAO would
// report "no records" for a query it never performed.
func (a *kvServiceAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanCompoundIndex refuses; see QueryCompoundIndex.
func (a *kvServiceAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// Only the guarded adapter carries the multi-key capability.
var _ kvstore.GuardedBatchKVStore = (*guardedKVServiceAdapter)(nil)
