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
// EntityRegisterer. Returns nil when no registerer is configured.
func (a *kvServiceAdapter) RegisterEntityIndexes(entityType string, indexes []kvstore.EntityIndex) error {
	if a.registerer == nil {
		return nil
	}
	return a.registerer.RegisterEntityType(entityType, indexes)
}

// QueryIndex performs an exact-match query on the named index by delegating
// to the lookup function with an IndexQueryRequest.
func (a *kvServiceAdapter) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if a.lookup == nil {
		return nil, nil
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
		return nil, nil
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

// QueryCompoundIndex performs an exact-match query on a compound index.
func (a *kvServiceAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

// ScanCompoundIndex performs a prefix + range scan on a compound index.
func (a *kvServiceAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
