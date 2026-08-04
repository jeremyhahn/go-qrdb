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
func (a *kvStoreAdapter) Get(ctx context.Context, key string) ([]byte, error) {
	return a.kv.Get(ctx, key)
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
// EntityIndex definition. The index name is composed as
// "entityType.FieldName". Registration is idempotent at the server level.
func (a *kvStoreAdapter) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	ctx := context.Background()
	for _, ei := range indexes {
		indexName := entityType + "." + ei.FieldName
		cfg := transport.IndexConfig{
			Unique: ei.Unique,
			Sparse: ei.Sparse,
		}
		if err := a.index.IndexCreate(ctx, indexName, ei.JSONFieldName, cfg); err != nil {
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
func (a *kvStoreAdapter) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
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

// QueryCompoundIndex performs an exact-match query on a compound index.
// Returns nil until the transport layer exposes compound index operations.
func (a *kvStoreAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

// ScanCompoundIndex performs a prefix + range scan on a compound index.
// Returns nil until the transport layer exposes compound index operations.
func (a *kvStoreAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
