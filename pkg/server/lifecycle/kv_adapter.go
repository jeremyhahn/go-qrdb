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

// kvStoreAdapter adapts service.KVServicer to the kvstore.KVStore interface
// so that internal stores (user, role, cert) can use the replicated KV
// service for storage. Index operations are no-ops: lifecycle-layer stores
// do not require secondary indexes.
type kvStoreAdapter struct {
	svc service.KVServicer
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

// RegisterEntityIndexes is a no-op: lifecycle-layer stores do not use
// secondary indexes.
func (a *kvStoreAdapter) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

// QueryIndex is a no-op: lifecycle-layer stores do not use secondary indexes.
func (a *kvStoreAdapter) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

// ScanIndex is a no-op: lifecycle-layer stores do not use secondary indexes.
func (a *kvStoreAdapter) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// QueryCompoundIndex is a no-op: lifecycle-layer stores do not use compound
// indexes.
func (a *kvStoreAdapter) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

// ScanCompoundIndex is a no-op: lifecycle-layer stores do not use compound
// indexes.
func (a *kvStoreAdapter) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
