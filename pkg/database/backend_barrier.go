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
	"bytes"
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
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
// transparent value encryption/decryption. Keys remain in plaintext (so
// range scans and prefix listing stay usable without exposing key barrier
// material); values are AES-256-GCM sealed under the owning database's
// barrier-derived DEK before they ever reach the underlying store. When the
// barrier is sealed, all operations fail closed with SealedError.
//
// This is Manager's at-rest encryption mechanism for every per-database
// encryption barrier (see Manager.CreateWithCredentials / Manager.Open):
// unlike the system/tenant barrier's FS-level pkg/atrest.Encryptor (which
// wraps a filesys.FS and therefore only applies to stores that write
// through one), a per-database store may be memory-, file-, or Pebble-backed, so
// encryption is applied uniformly at the value layer instead.
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

// PutIfAbsent encrypts value and stores it only when key is absent, forwarding
// the conditional write to the inner backend so the atomicity guarantee is the
// inner store's rather than something this wrapper invents.
//
// It satisfies AtomicBackend only when the inner backend does. A barrier around
// a non-atomic store cannot manufacture atomicity — encrypting first and then
// doing a check-then-write here would reintroduce exactly the window the
// operation exists to close — so the capability is reported honestly by
// returning kvstore.ErrAtomicWriteUnsupported instead. StandaloneBackend is one, so the
// barrier-over-standalone composition the dedicated-store path builds is atomic
// end to end (go-qrdb#466).
func (b *BarrierBackend) PutIfAbsent(ctx context.Context, key, value []byte) (bool, error) {
	if b.barrier.IsSealed() {
		return false, &SealedError{}
	}
	atomicInner, ok := b.inner.(AtomicBackend)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	encrypted, err := b.barrier.Encrypt(nil, value)
	if err != nil {
		return false, &BarrierEncryptError{Key: string(key), Err: err}
	}
	return atomicInner.PutIfAbsent(ctx, key, encrypted)
}

// CompareAndDelete removes key only when the value stored there still
// decrypts to expected. Like StorageBarrier's, it guards on the ciphertext
// this call read: encryption is randomised, so a re-encryption of expected
// would never match.
//
// The Get below is deliberately OUTSIDE the inner backend's write lock. It
// establishes only which ciphertext corresponds to expected; the decision is
// the inner CompareAndDelete's, which re-reads and compares that same
// ciphertext while holding the lock. A writer that lands between the two
// changes the stored ciphertext, so the guarded call declines — the outcome is
// still defined, and no value this caller never observed is destroyed.
func (b *BarrierBackend) CompareAndDelete(ctx context.Context, key, expected []byte) (bool, error) {
	if b.barrier.IsSealed() {
		return false, &SealedError{}
	}
	atomicInner, ok := b.inner.(AtomicBackend)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	stored, err := b.inner.Get(ctx, key)
	if err != nil {
		return false, err
	}
	if stored == nil {
		return false, nil
	}
	plaintext, derr := b.barrier.Decrypt(nil, stored)
	if derr != nil {
		return false, &BarrierDecryptError{Key: string(key), Err: derr}
	}
	if !bytes.Equal(plaintext, expected) {
		return false, nil
	}
	return atomicInner.CompareAndDelete(ctx, key, stored)
}

// CompareAndSwap replaces key's value with next only when the value stored
// there still decrypts to expected.
//
// Like CompareAndDelete it guards on the CIPHERTEXT this call read, because
// encryption is randomised and a re-encryption of expected would never match
// what the inner backend holds.
//
// The Get below is outside the inner backend's write lock for the same reason
// CompareAndDelete's is: it resolves expected to a ciphertext, and the inner
// CompareAndSwap re-compares that ciphertext under the lock, so a concurrent
// writer turns this into a declined swap rather than a lost update.
//
// Its absence was a breaking defect, not merely a missing feature: widening
// AtomicBackend without implementing it here meant BarrierBackend stopped
// satisfying the interface, so BackendKVService's type assertion failed for
// every barrier-backed database and the PRE-EXISTING PutIfAbsent and
// CompareAndDelete began returning kvstore.ErrAtomicWriteUnsupported — silently
// disabling uniqueness and single-use claims on encrypted databases. The
// compile-time assertion below exists so that cannot recur (go-qrdb#56).
func (b *BarrierBackend) CompareAndSwap(ctx context.Context, key, expected, next []byte) (bool, error) {
	if b.barrier.IsSealed() {
		return false, &SealedError{}
	}
	atomicInner, ok := b.inner.(AtomicBackend)
	if !ok {
		return false, kvstore.ErrAtomicWriteUnsupported
	}
	stored, err := b.inner.Get(ctx, key)
	if err != nil {
		return false, err
	}
	if stored == nil {
		// A swap does not create.
		return false, nil
	}
	plaintext, derr := b.barrier.Decrypt(nil, stored)
	if derr != nil {
		return false, &BarrierDecryptError{Key: string(key), Err: derr}
	}
	if !bytes.Equal(plaintext, expected) {
		return false, nil
	}
	encrypted, eerr := b.barrier.Encrypt(nil, next)
	if eerr != nil {
		return false, &BarrierEncryptError{Key: string(key), Err: eerr}
	}
	return atomicInner.CompareAndSwap(ctx, key, stored, encrypted)
}

// Compile-time proof that BarrierBackend still satisfies AtomicBackend.
//
// Without it, widening the interface degrades this type silently: the code
// keeps compiling, the runtime type assertion in BackendKVService starts
// failing, and every guarded write on an encrypted database reports
// "unsupported" (go-qrdb#56).
var _ AtomicBackend = (*BarrierBackend)(nil)

// Delete removes the key. No decryption is needed for deletion.
func (b *BarrierBackend) Delete(ctx context.Context, key []byte) error {
	if b.barrier.IsSealed() {
		return &SealedError{}
	}
	return b.inner.Delete(ctx, key)
}

func (b *BarrierBackend) DeleteRange(ctx context.Context, start, end []byte) error {
	if b.barrier.IsSealed() {
		return &SealedError{}
	}
	return b.inner.DeleteRange(ctx, start, end)
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
