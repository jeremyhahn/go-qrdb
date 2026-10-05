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

package sdk

import (
	"context"
	"errors"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// NilKVClientError is returned by NewKVStoreAdapter when the KV client is nil.
type NilKVClientError struct{}

// Error returns a human-readable description.
func (e *NilKVClientError) Error() string {
	return "sdk: kvstore adapter requires a non-nil transport.KVClient"
}

// NilIndexClientError is returned by NewKVStoreAdapter when the index client
// is nil.
type NilIndexClientError struct{}

// Error returns a human-readable description.
func (e *NilIndexClientError) Error() string {
	return "sdk: kvstore adapter requires a non-nil transport.IndexClient"
}

// NewKVStoreAdapter returns a kvstore.KVStore backed by the given transport
// clients, enabling DAOs to operate transparently over any transport
// (REST, gRPC, QUIC, MCP, Unix, embedded). KV operations delegate to kv and
// secondary-index operations delegate to index. Use Client.KVStore() when
// both clients come from the same connected Client; this constructor serves
// callers that compose the clients themselves. It returns a typed error
// (NilKVClientError or NilIndexClientError) when either dependency is nil.
func NewKVStoreAdapter(kv transport.KVClient, index transport.IndexClient) (KVStore, error) {
	if kv == nil {
		return nil, &NilKVClientError{}
	}
	if index == nil {
		return nil, &NilIndexClientError{}
	}
	return &kvStoreAdapter{kv: kv, index: index}, nil
}

// kvStoreAdapter bridges the SDK transport layer to the kvstore.KVStore
// interface, enabling DAOs to operate transparently over any transport
// (REST, gRPC, MCP, Unix, embedded).
type kvStoreAdapter struct {
	kv    transport.KVClient
	index transport.IndexClient
}

// Compile-time interface assertions. kvStoreAdapter satisfies
// kvstore.AtomicKVStore unconditionally -- see PutIfAbsent below for why
// that is correct even though not every transport.KVClient carries the
// capability it forwards to.
var _ kvstore.KVStore = (*kvStoreAdapter)(nil)
var _ kvstore.AtomicKVStore = (*kvStoreAdapter)(nil)
var _ kvstore.MultiGuardedBatchKVStore = (*kvStoreAdapter)(nil)

// Put stores a key-value pair by delegating to the transport's KVClient.
func (a *kvStoreAdapter) Put(ctx context.Context, key string, value []byte) error {
	return a.kv.Put(ctx, key, value)
}

// Get retrieves a value by key from the transport's KVClient.
// Returns nil, nil when the key does not exist.
//
// The translation is the point of this method rather than an aside. The two
// sides spell absence differently — kvstore.KVStore documents (nil, nil), the
// transports return *transport.KeyNotFoundError — and this adapter is the seam
// between them, so it is where the difference has to be resolved. Delegating
// verbatim, as it did, let a transport error escape through an interface whose
// contract says absence is not an error at all, and left every caller to
// recognise a transport type it should never have had to see (go-qrdb#58).
func (a *kvStoreAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := a.kv.Get(ctx, key)
	if err != nil {
		var notFound *transport.KeyNotFoundError
		if errors.As(err, &notFound) {
			return nil, nil
		}
		return nil, err
	}
	return value, nil
}

// Delete removes a key by delegating to the transport's KVClient.
func (a *kvStoreAdapter) Delete(ctx context.Context, key string) error {
	return a.kv.Delete(ctx, key)
}

// PutIfAbsent stores value under key only when key is absent, delegating to
// the transport's GuardedKVClient capability.
//
// Every production transport (REST, gRPC, QUIC, Unix, MCP, embedded)
// implements transport.GuardedKVClient today, so this reaches a real
// server-evaluated conditional write in practice. The type assertion exists
// for the transport that does not -- a future or a caller-supplied
// transport.KVClient this adapter did not originate -- and its failure
// returns kvstore.ErrAtomicWriteUnsupported rather than a panic.
//
// This is why kvStoreAdapter satisfies kvstore.AtomicKVStore
// UNCONDITIONALLY: the Go interface is about having the three METHODS, and
// kvstore.AtomicKVStore's own doc is explicit that a store which "has the
// method but not the capability" reports ErrAtomicWriteUnsupported from the
// call rather than refusing the type assertion -- pkg/dao.GenericDAO's
// SaveIfAbsent already reads that error as the identical refusal
// *AtomicWriteUnsupportedError gives when the assertion itself fails (go-qrdb#589).
func (a *kvStoreAdapter) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	guarded, ok := a.kv.(transport.GuardedKVClient)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	return guarded.PutIfAbsent(ctx, key, value)
}

// CompareAndDelete removes key only when its stored value still equals
// expected, delegating to the transport's GuardedKVClient capability. See
// PutIfAbsent for why the capability check cannot fail in practice and what
// it reports when it does.
func (a *kvStoreAdapter) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	guarded, ok := a.kv.(transport.GuardedKVClient)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	return guarded.CompareAndDelete(ctx, key, expected)
}

// CompareAndSwap replaces key's value with next only when its stored value
// still equals expected, delegating to the transport's GuardedKVClient
// capability. See PutIfAbsent for why the capability check cannot fail in
// practice and what it reports when it does.
func (a *kvStoreAdapter) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	guarded, ok := a.kv.(transport.GuardedKVClient)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	return guarded.CompareAndSwap(ctx, key, expected, next)
}

// PutAllIfUnchanged applies every write atomically, and only while guardKey
// still holds guardValue: PutAllIfAllUnchanged with a single guard. See
// PutAllIfAllUnchanged for the capability this delegates to.
func (a *kvStoreAdapter) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return a.PutAllIfAllUnchanged(ctx, []kvstore.Guard{{Key: guardKey, Value: guardValue}}, writes)
}

// PutAllIfAllUnchanged applies every write atomically, and only while EVERY
// guard's key still holds its expected value (go-qrdb#589), by delegating
// to the transport's TransactionClient capability -- the SAME
// ExecuteTransaction RPC every production transport (REST, gRPC, QUIC,
// Unix, MCP, embedded) already carries, translating guards into compares
// and writes into ops exactly as pkg/server/lifecycle.ExecuteGuardedBatch
// does on the node side of the same wire contract (guardCompares: a nil
// Guard.Value becomes a version-equals-zero compare, a non-nil one becomes
// an exists compare plus a value compare). Reusing ExecuteTransaction
// instead of adding a second, parallel wire verb that would carry the
// identical guarantee under a different name is deliberate: the RPC is
// already one atomic Raft/MVCC transaction end to end on every transport, so
// a dedicated PutAllIfAllUnchanged opcode would duplicate it rather than
// reach somewhere it cannot.
//
// A KV with Delete set becomes an OpDelete; otherwise an OpPut -- so a batch
// can remove one key and write others in the same atomic step a move/rename
// needs. See PutIfAbsent for why the capability type-assertion cannot fail
// in practice and what it reports when it does.
func (a *kvStoreAdapter) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	tx, ok := a.kv.(transport.TransactionClient)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}

	compares := make([]transport.Compare, 0, len(guards)*2)
	for _, g := range guards {
		if g.Value == nil {
			compares = append(compares, transport.Compare{
				Key:     []byte(g.Key),
				Target:  transport.CompareVersion,
				Result:  transport.CompareEqual,
				Version: 0,
			})
			continue
		}
		compares = append(compares,
			transport.Compare{
				Key:     []byte(g.Key),
				Target:  transport.CompareVersion,
				Result:  transport.CompareGreater,
				Version: 0,
			},
			transport.Compare{
				Key:    []byte(g.Key),
				Target: transport.CompareValue,
				Result: transport.CompareEqual,
				Value:  g.Value,
			},
		)
	}

	ops := make([]transport.Op, len(writes))
	for i, w := range writes {
		if w.Delete {
			ops[i] = transport.Op{Type: transport.OpDelete, Key: []byte(w.Key)}
			continue
		}
		ops[i] = transport.Op{Type: transport.OpPut, Key: []byte(w.Key), Value: w.Value}
	}

	resp, err := tx.ExecuteTransaction(ctx, &transport.TransactionRequest{Compares: compares, SuccessOps: ops})
	if err != nil {
		return false, err
	}
	return resp.Succeeded, nil
}

// Scan iterates over key-value pairs with the given prefix, invoking fn
// for each entry. The transport Scan returns the full map; this adapter
// iterates it and stops early if fn returns a non-nil error.
func (a *kvStoreAdapter) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	entries, err := a.kv.Scan(ctx, prefix)
	if err != nil {
		return err
	}
	for key, value := range entries {
		if err := fn(key, value); err != nil {
			return err
		}
	}
	return nil
}

// List returns all keys matching the given prefix from the transport's KVClient.
func (a *kvStoreAdapter) List(ctx context.Context, prefix string) ([]string, error) {
	return a.kv.List(ctx, prefix)
}

// Exists checks whether a key exists by delegating to the transport's KVClient.
func (a *kvStoreAdapter) Exists(ctx context.Context, key string) (bool, error) {
	return a.kv.Exists(ctx, key)
}

// RegisterEntityIndexes creates a secondary index for each provided
// EntityIndex definition, under the definition's resolved name. Registration
// is idempotent at the server level.
//
// A compound definition is REFUSED with kvstore.ErrIndexUnsupported:
// transport.IndexClient has no compound creation verb, and the compound query
// methods below cannot serve one either. Previously this composed
// entityType + "." + ei.FieldName for a compound definition, where FieldName
// is empty — creating a garbage index named "{entityType}." while telling the
// caller its compound index existed.
func (a *kvStoreAdapter) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	ctx := context.Background()
	for _, ei := range indexes {
		if ei.IsCompound() {
			return kvstore.ErrIndexUnsupported
		}
		// A case-INSENSITIVE definition used to be refused here:
		// transport.IndexConfig carried no case-sensitivity flag, so the
		// server created the index case-sensitive while this adapter reported
		// success -- a lookup for "Alpha" over a stored "alpha" coming back
		// empty with a nil error. #34 added the flag to transport.IndexConfig
		// and to every protocol underneath it, so the refusal is gone.
		//
		// The polarity is inverted here on purpose: transport.IndexConfig
		// marks the POSITIVE claim (CaseInsensitive) while kvstore.EntityIndex
		// names the default (CaseSensitive).
		cfg := transport.IndexConfig{
			Unique:          ei.Unique,
			Sparse:          ei.Sparse,
			CaseInsensitive: !ei.CaseSensitive,
		}
		if err := a.index.IndexCreate(ctx, ei.ResolvedName(entityType), ei.JSONFieldName, cfg); err != nil {
			return err
		}
	}
	return nil
}

// QueryIndex performs an exact-match query on the named index and returns
// the matching entity keys as byte slices. The string value is passed as
// the query term and the resulting string keys are converted to []byte.
func (a *kvStoreAdapter) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	resp, err := a.index.IndexQuery(ctx, indexName, string(value))
	if err != nil {
		return nil, err
	}
	keys := make([][]byte, len(resp.Keys))
	for i, k := range resp.Keys {
		keys[i] = []byte(k)
	}
	return keys, nil
}

// ScanIndex performs a range scan on the named index for values in the
// half-open interval [start, end). Returns a map of field values to the
// entity keys that share that value.
//
// A nil start or end is an OPEN bound on that side and is threaded through the
// transport as a nil *string, so a one-sided range (the planner leaves one
// side nil for Team >= "beta") returns its matches instead of an empty result
// (go-qrdb#36, which removed the earlier refusal here).
func (a *kvStoreAdapter) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	resp, err := a.index.IndexScan(ctx, indexName, transport.OptionalString(start), transport.OptionalString(end))
	if err != nil {
		return nil, err
	}
	result := make(map[string][][]byte, len(resp.Entries))
	for _, entry := range resp.Entries {
		keys := make([][]byte, len(entry.Keys))
		for i, k := range entry.Keys {
			keys[i] = []byte(k)
		}
		result[entry.Value] = keys
	}
	return result, nil
}

// QueryCompoundIndex refuses with kvstore.ErrIndexUnsupported:
// transport.IndexClient exposes no compound index operations, so this adapter
// can neither create nor query one.
//
// It previously returned (nil, nil), which a DAO could not tell apart from a
// compound index with no matches — so a compound query over any transport
// answered "no records" with no error. The typed refusal lets pkg/dao plan a
// full scan instead.
func (a *kvStoreAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}

// ScanCompoundIndex refuses; see QueryCompoundIndex.
func (a *kvStoreAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, kvstore.ErrIndexUnsupported
}
