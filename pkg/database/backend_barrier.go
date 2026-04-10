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

package database

import (
	"context"
)

// BarrierEncryptor provides encrypt/decrypt/sealed-check for the barrier
// backend. This interface is satisfied by seal.StorageBarrier's
// EpochBarrier (go-quicraft Barrier) as well as the test mock.
type BarrierEncryptor interface {
	// Encrypt encrypts plaintext. dst may be nil.
	Encrypt(dst, plaintext []byte) ([]byte, error)

	// Decrypt decrypts ciphertext. dst may be nil.
	Decrypt(dst, ciphertext []byte) ([]byte, error)

	// IsSealed reports whether the barrier is sealed.
	IsSealed() bool
}

// BarrierBackend wraps a Backend with a BarrierEncryptor to provide
// transparent value encryption/decryption. Keys remain in plaintext.
// When the barrier is sealed, all operations return SealedError.
type BarrierBackend struct {
	inner   Backend
	barrier BarrierEncryptor
}

// Compile-time interface check.
var _ Backend = (*BarrierBackend)(nil)

// NewBarrierBackend creates a new BarrierBackend wrapping the given inner
// Backend with the provided BarrierEncryptor for transparent encryption.
func NewBarrierBackend(inner Backend, barrier BarrierEncryptor) *BarrierBackend {
	return &BarrierBackend{inner: inner, barrier: barrier}
}

// Get retrieves and decrypts the value for the given key.
func (b *BarrierBackend) Get(ctx context.Context, key []byte) ([]byte, error) {
	if b.barrier.IsSealed() {
		return nil, &SealedError{}
	}
	ciphertext, err := b.inner.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	if ciphertext == nil {
		return nil, nil
	}
	plaintext, err := b.barrier.Decrypt(nil, ciphertext)
	if err != nil {
		return nil, &BarrierDecryptError{Key: string(key), Err: err}
	}
	return plaintext, nil
}

// Put encrypts the value and stores it under the given key.
func (b *BarrierBackend) Put(ctx context.Context, key, value []byte) error {
	if b.barrier.IsSealed() {
		return &SealedError{}
	}
	encrypted, err := b.barrier.Encrypt(nil, value)
	if err != nil {
		return &BarrierEncryptError{Key: string(key), Err: err}
	}
	return b.inner.Put(ctx, key, encrypted)
}

// Delete removes the key. No decryption is needed for deletion.
func (b *BarrierBackend) Delete(ctx context.Context, key []byte) error {
	if b.barrier.IsSealed() {
		return &SealedError{}
	}
	return b.inner.Delete(ctx, key)
}

// List returns all keys matching the given prefix. Keys are not encrypted.
func (b *BarrierBackend) List(ctx context.Context, prefix string) ([]string, error) {
	if b.barrier.IsSealed() {
		return nil, &SealedError{}
	}
	return b.inner.List(ctx, prefix)
}

// Range scans keys in [start, end) and decrypts all values.
func (b *BarrierBackend) Range(ctx context.Context, start, end []byte, limit int) ([]KeyValue, error) {
	if b.barrier.IsSealed() {
		return nil, &SealedError{}
	}
	kvs, err := b.inner.Range(ctx, start, end, limit)
	if err != nil {
		return nil, err
	}
	result := make([]KeyValue, len(kvs))
	for i, kv := range kvs {
		plaintext, decErr := b.barrier.Decrypt(nil, kv.Value)
		if decErr != nil {
			return nil, &BarrierDecryptError{Key: string(kv.Key), Err: decErr}
		}
		result[i] = KeyValue{Key: kv.Key, Value: plaintext}
	}
	return result, nil
}

// Batch applies multiple operations atomically. Put values are encrypted
// before being passed to the inner backend. Delete operations pass through
// unchanged.
func (b *BarrierBackend) Batch(ctx context.Context, ops []BatchOp) error {
	if b.barrier.IsSealed() {
		return &SealedError{}
	}
	encrypted := make([]BatchOp, len(ops))
	for i, op := range ops {
		encrypted[i] = BatchOp{Op: op.Op, Key: op.Key}
		if op.Op == BatchOpPut && op.Value != nil {
			enc, err := b.barrier.Encrypt(nil, op.Value)
			if err != nil {
				return &BarrierEncryptError{Key: string(op.Key), Err: err}
			}
			encrypted[i].Value = enc
		}
	}
	return b.inner.Batch(ctx, encrypted)
}

// Close releases resources held by the inner backend.
func (b *BarrierBackend) Close() error {
	return b.inner.Close()
}
