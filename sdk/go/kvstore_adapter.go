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

// Compile-time interface assertion.
var _ kvstore.KVStore = (*kvStoreAdapter)(nil)

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
		// transport.IndexConfig carries Unique, Sparse and Numeric and no
		// case-sensitivity flag, so a case-INSENSITIVE definition would be
		// created case-sensitive on the server while this adapter reported
		// success — and a lookup for "Alpha" over a stored "alpha" would come
		// back empty with a nil error. Refuse instead; the DAO then plans a
		// correct full scan. Tracked as #34.
		//
		// This only fires for an explicit `index:"case_insensitive"` tag —
		// index.ParseIndexTags defaults CaseSensitive to true, so ordinary
		// `index:"true"` and `index:"unique"` fields keep their real index.
		if !ei.CaseSensitive {
			return kvstore.ErrIndexUnsupported
		}
		cfg := transport.IndexConfig{
			Unique: ei.Unique,
			Sparse: ei.Sparse,
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
// An OPEN bound is refused with kvstore.ErrIndexUnsupported.
// transport.IndexClient.IndexScan takes plain strings, so there is no way to
// express "unbounded" over it — a nil bound would arrive as "", which every
// index implementation reads as an explicit empty bound rather than an open
// one, and REST rejects it as a missing parameter outright.
//
// That matters because the planner leaves one side nil for every one-sided
// range: Team >= "beta" would become ["beta", "") and return an empty result
// with a nil error over an index holding matches. Refusing makes pkg/dao's
// caller see the incapability instead of a wrong answer. Carrying optional
// bounds through the transport contract is the real fix and is tracked as
// go-qrdb#36.
func (a *kvStoreAdapter) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if start == nil || end == nil {
		return nil, kvstore.ErrIndexUnsupported
	}
	resp, err := a.index.IndexScan(ctx, indexName, string(start), string(end))
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
