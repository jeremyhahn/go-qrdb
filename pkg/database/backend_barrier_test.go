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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// xorBarrier is a trivial BarrierEncryptor for testing. It XORs every
// byte with a fixed mask, making encryption reversible and deterministic.
type xorBarrier struct {
	mask   byte
	sealed bool
}

func (x *xorBarrier) Encrypt(_, plaintext []byte) ([]byte, error) {
	out := make([]byte, len(plaintext))
	for i, b := range plaintext {
		out[i] = b ^ x.mask
	}
	return out, nil
}

func (x *xorBarrier) Decrypt(_, ciphertext []byte) ([]byte, error) {
	out := make([]byte, len(ciphertext))
	for i, b := range ciphertext {
		out[i] = b ^ x.mask
	}
	return out, nil
}

func (x *xorBarrier) IsSealed() bool {
	return x.sealed
}

// failBarrier is a BarrierEncryptor that always returns errors on
// Encrypt and Decrypt, for testing error paths.
type failBarrier struct {
	sealed bool
}

func (f *failBarrier) Encrypt(_, _ []byte) ([]byte, error) {
	return nil, errors.New("encrypt failed")
}

func (f *failBarrier) Decrypt(_, _ []byte) ([]byte, error) {
	return nil, errors.New("decrypt failed")
}

func (f *failBarrier) IsSealed() bool {
	return f.sealed
}

// newBarrierTestBackend creates a BarrierBackend backed by in-memory storage
// and the given barrier for use in tests.
func newBarrierTestBackend(t *testing.T, barrier *xorBarrier) *BarrierBackend {
	t.Helper()
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	inner := NewStandaloneBackend(store)
	return NewBarrierBackend(inner, barrier)
}

func TestBarrierBackend_InterfaceCompliance(t *testing.T) {
	var _ Backend = (*BarrierBackend)(nil)
}

func TestBarrierBackend_PutGet(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	key := []byte("mykey")
	value := []byte("hello world")

	if err := b.Put(ctx, key, value); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: unexpected error: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("Get: got %q, want %q", got, value)
	}
}

func TestBarrierBackend_PutGet_VerifyEncryption(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA}
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	inner := NewStandaloneBackend(store)
	b := NewBarrierBackend(inner, barrier)
	ctx := context.Background()

	key := []byte("secret")
	value := []byte("plaintext data")

	if err := b.Put(ctx, key, value); err != nil {
		t.Fatalf("Put: unexpected error: %v", err)
	}

	// Read raw from inner backend -- should be encrypted (XOR'd), not plaintext.
	raw, err := inner.Get(ctx, key)
	if err != nil {
		t.Fatalf("inner.Get: unexpected error: %v", err)
	}
	if bytes.Equal(raw, value) {
		t.Error("inner storage contains plaintext; expected encrypted data")
	}

	// Read through barrier -- should be plaintext.
	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("barrier.Get: unexpected error: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("barrier.Get: got %q, want %q", got, value)
	}
}

func TestBarrierBackend_Get_NonExistent(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	got, err := b.Get(ctx, []byte("missing"))
	if err != nil {
		t.Fatalf("Get non-existent: unexpected error: %v", err)
	}
	if got != nil {
		t.Errorf("Get non-existent: got %q, want nil", got)
	}
}

func TestBarrierBackend_Delete(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	key := []byte("delme")
	if err := b.Put(ctx, key, []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: unexpected error: %v", err)
	}

	got, err := b.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if got != nil {
		t.Errorf("Get after delete: got %q, want nil", got)
	}
}

func TestBarrierBackend_List(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	keys := []string{"prefix/a", "prefix/b", "other/c"}
	for _, k := range keys {
		if err := b.Put(ctx, []byte(k), []byte("v")); err != nil {
			t.Fatalf("Put %s: %v", k, err)
		}
	}

	got, err := b.List(ctx, "prefix/")
	if err != nil {
		t.Fatalf("List: unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("List: got %d keys, want 2", len(got))
	}
}

func TestBarrierBackend_Range(t *testing.T) {
	barrier := &xorBarrier{mask: 0xBB}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	pairs := []struct {
		key, value string
	}{
		{"a", "alpha"},
		{"b", "bravo"},
		{"c", "charlie"},
	}
	for _, p := range pairs {
		if err := b.Put(ctx, []byte(p.key), []byte(p.value)); err != nil {
			t.Fatalf("Put %s: %v", p.key, err)
		}
	}

	kvs, err := b.Range(ctx, []byte("a"), []byte("c"), 0)
	if err != nil {
		t.Fatalf("Range: unexpected error: %v", err)
	}
	if len(kvs) != 2 {
		t.Fatalf("Range: got %d results, want 2", len(kvs))
	}
	// Values must be decrypted.
	if string(kvs[0].Value) != "alpha" {
		t.Errorf("Range[0].Value = %q, want %q", kvs[0].Value, "alpha")
	}
	if string(kvs[1].Value) != "bravo" {
		t.Errorf("Range[1].Value = %q, want %q", kvs[1].Value, "bravo")
	}
}

func TestBarrierBackend_Batch_PutAndDelete(t *testing.T) {
	barrier := &xorBarrier{mask: 0xCC}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	// Seed a key to delete.
	if err := b.Put(ctx, []byte("del"), []byte("gone")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	ops := []BatchOp{
		{Op: BatchOpPut, Key: []byte("batch1"), Value: []byte("val1")},
		{Op: BatchOpPut, Key: []byte("batch2"), Value: []byte("val2")},
		{Op: BatchOpDelete, Key: []byte("del")},
	}
	if err := b.Batch(ctx, ops); err != nil {
		t.Fatalf("Batch: unexpected error: %v", err)
	}

	// Verify put values are readable and decrypted.
	v1, err := b.Get(ctx, []byte("batch1"))
	if err != nil {
		t.Fatalf("Get batch1: %v", err)
	}
	if string(v1) != "val1" {
		t.Errorf("batch1 = %q, want %q", v1, "val1")
	}

	v2, err := b.Get(ctx, []byte("batch2"))
	if err != nil {
		t.Fatalf("Get batch2: %v", err)
	}
	if string(v2) != "val2" {
		t.Errorf("batch2 = %q, want %q", v2, "val2")
	}

	// Verify delete worked.
	del, err := b.Get(ctx, []byte("del"))
	if err != nil {
		t.Fatalf("Get del: %v", err)
	}
	if del != nil {
		t.Errorf("del = %q, want nil", del)
	}
}

func TestBarrierBackend_Sealed_Get(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA, sealed: true}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	_, err := b.Get(ctx, []byte("key"))
	if err == nil {
		t.Fatal("Get: expected SealedError, got nil")
	}
	var sealErr *SealedError
	if !errors.As(err, &sealErr) {
		t.Errorf("Get: expected *SealedError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_Sealed_Put(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA, sealed: true}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	err := b.Put(ctx, []byte("key"), []byte("val"))
	if err == nil {
		t.Fatal("Put: expected SealedError, got nil")
	}
	var sealErr *SealedError
	if !errors.As(err, &sealErr) {
		t.Errorf("Put: expected *SealedError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_Sealed_Delete(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA, sealed: true}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	err := b.Delete(ctx, []byte("key"))
	if err == nil {
		t.Fatal("Delete: expected SealedError, got nil")
	}
	var sealErr *SealedError
	if !errors.As(err, &sealErr) {
		t.Errorf("Delete: expected *SealedError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_Sealed_List(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA, sealed: true}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	_, err := b.List(ctx, "prefix")
	if err == nil {
		t.Fatal("List: expected SealedError, got nil")
	}
	var sealErr *SealedError
	if !errors.As(err, &sealErr) {
		t.Errorf("List: expected *SealedError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_Sealed_Range(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA, sealed: true}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	_, err := b.Range(ctx, []byte("a"), []byte("z"), 0)
	if err == nil {
		t.Fatal("Range: expected SealedError, got nil")
	}
	var sealErr *SealedError
	if !errors.As(err, &sealErr) {
		t.Errorf("Range: expected *SealedError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_Sealed_Batch(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA, sealed: true}
	b := newBarrierTestBackend(t, barrier)
	ctx := context.Background()

	ops := []BatchOp{{Op: BatchOpPut, Key: []byte("k"), Value: []byte("v")}}
	err := b.Batch(ctx, ops)
	if err == nil {
		t.Fatal("Batch: expected SealedError, got nil")
	}
	var sealErr *SealedError
	if !errors.As(err, &sealErr) {
		t.Errorf("Batch: expected *SealedError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_EncryptError(t *testing.T) {
	barrier := &failBarrier{sealed: false}
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	inner := NewStandaloneBackend(store)
	b := NewBarrierBackend(inner, barrier)
	ctx := context.Background()

	err := b.Put(ctx, []byte("key"), []byte("value"))
	if err == nil {
		t.Fatal("Put: expected BarrierEncryptError, got nil")
	}
	var encErr *BarrierEncryptError
	if !errors.As(err, &encErr) {
		t.Errorf("Put: expected *BarrierEncryptError, got %T: %v", err, err)
	}
	if encErr.Key != "key" {
		t.Errorf("BarrierEncryptError.Key = %q, want %q", encErr.Key, "key")
	}
}

func TestBarrierBackend_DecryptError(t *testing.T) {
	// Write raw ciphertext directly, then try to read through a failing barrier.
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	inner := NewStandaloneBackend(store)

	ctx := context.Background()
	// Store raw data that the fail barrier cannot decrypt.
	if err := inner.Put(ctx, []byte("key"), []byte("raw")); err != nil {
		t.Fatalf("inner.Put: %v", err)
	}

	barrier := &failBarrier{sealed: false}
	b := NewBarrierBackend(inner, barrier)

	_, err := b.Get(ctx, []byte("key"))
	if err == nil {
		t.Fatal("Get: expected BarrierDecryptError, got nil")
	}
	var decErr *BarrierDecryptError
	if !errors.As(err, &decErr) {
		t.Errorf("Get: expected *BarrierDecryptError, got %T: %v", err, err)
	}
	if decErr.Key != "key" {
		t.Errorf("BarrierDecryptError.Key = %q, want %q", decErr.Key, "key")
	}
}

func TestBarrierBackend_Batch_EncryptError(t *testing.T) {
	barrier := &failBarrier{sealed: false}
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	inner := NewStandaloneBackend(store)
	b := NewBarrierBackend(inner, barrier)
	ctx := context.Background()

	ops := []BatchOp{
		{Op: BatchOpPut, Key: []byte("k"), Value: []byte("v")},
	}
	err := b.Batch(ctx, ops)
	if err == nil {
		t.Fatal("Batch: expected BarrierEncryptError, got nil")
	}
	var encErr *BarrierEncryptError
	if !errors.As(err, &encErr) {
		t.Errorf("Batch: expected *BarrierEncryptError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_Range_DecryptError(t *testing.T) {
	store := memory.NewStorage()
	t.Cleanup(func() { _ = store.Close() })
	inner := NewStandaloneBackend(store)
	ctx := context.Background()

	// Store raw data that the fail barrier cannot decrypt.
	if err := inner.Put(ctx, []byte("a"), []byte("raw")); err != nil {
		t.Fatalf("inner.Put: %v", err)
	}

	barrier := &failBarrier{sealed: false}
	b := NewBarrierBackend(inner, barrier)

	_, err := b.Range(ctx, []byte("a"), []byte("z"), 0)
	if err == nil {
		t.Fatal("Range: expected BarrierDecryptError, got nil")
	}
	var decErr *BarrierDecryptError
	if !errors.As(err, &decErr) {
		t.Errorf("Range: expected *BarrierDecryptError, got %T: %v", err, err)
	}
}

func TestBarrierBackend_Close(t *testing.T) {
	barrier := &xorBarrier{mask: 0xAA}
	store := memory.NewStorage()
	inner := NewStandaloneBackend(store)
	b := NewBarrierBackend(inner, barrier)

	if err := b.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}

// TestBarrierBackend_CompareAndDelete covers the guarded delete through the
// barrier, which had no test.
//
// The comparison it performs is on PLAINTEXT: the caller supplies the value it
// believes is stored, and the backend decrypts what is there before comparing.
// A backend that compared ciphertext instead would be wrong in a way nothing
// else would catch -- under a nondeterministic AEAD the same plaintext encrypts
// to a different ciphertext every time, so every guarded delete would decline
// even when the caller's expectation was exactly right.
func TestBarrierBackend_CompareAndDelete(t *testing.T) {
	ctx := context.Background()

	t.Run("deletes when the plaintext matches", func(t *testing.T) {
		b := newBarrierTestBackend(t, &xorBarrier{mask: 0xAA})
		key, value := []byte("k"), []byte("v1")
		if err := b.Put(ctx, key, value); err != nil {
			t.Fatalf("Put: %v", err)
		}
		ok, err := b.CompareAndDelete(ctx, key, value)
		if err != nil {
			t.Fatalf("CompareAndDelete: %v", err)
		}
		if !ok {
			t.Fatal("CompareAndDelete declined a matching plaintext")
		}
		got, err := b.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got != nil {
			t.Errorf("key survived the delete: %q", got)
		}
	})

	t.Run("declines when the plaintext differs, leaving the value intact", func(t *testing.T) {
		b := newBarrierTestBackend(t, &xorBarrier{mask: 0xAA})
		key := []byte("k")
		if err := b.Put(ctx, key, []byte("actual")); err != nil {
			t.Fatalf("Put: %v", err)
		}
		ok, err := b.CompareAndDelete(ctx, key, []byte("stale"))
		if err != nil {
			t.Fatalf("CompareAndDelete: %v", err)
		}
		if ok {
			t.Fatal("CompareAndDelete deleted on a stale expected value")
		}
		got, err := b.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got) != "actual" {
			t.Errorf("a declined delete must not write: value = %q, want %q", got, "actual")
		}
	})

	t.Run("reports false for an absent key rather than an error", func(t *testing.T) {
		b := newBarrierTestBackend(t, &xorBarrier{mask: 0xAA})
		ok, err := b.CompareAndDelete(ctx, []byte("missing"), []byte("v"))
		if err != nil {
			t.Fatalf("CompareAndDelete on an absent key: %v", err)
		}
		if ok {
			t.Fatal("CompareAndDelete reported a delete of an absent key")
		}
	})

	// A sealed barrier must refuse BEFORE touching storage: the guarded delete
	// cannot compare plaintext it is unable to decrypt, and answering "false"
	// would be indistinguishable from "your expected value was stale".
	t.Run("refuses while sealed", func(t *testing.T) {
		barrier := &xorBarrier{mask: 0xAA}
		b := newBarrierTestBackend(t, barrier)
		key, value := []byte("k"), []byte("v1")
		if err := b.Put(ctx, key, value); err != nil {
			t.Fatalf("Put: %v", err)
		}
		barrier.sealed = true

		ok, err := b.CompareAndDelete(ctx, key, value)
		if ok {
			t.Fatal("CompareAndDelete succeeded while sealed")
		}
		var sealed *SealedError
		if !errors.As(err, &sealed) {
			t.Fatalf("error = %v, want *SealedError", err)
		}

		barrier.sealed = false
		got, err := b.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get after unseal: %v", err)
		}
		if string(got) != "v1" {
			t.Errorf("a refusal while sealed must not delete: value = %q, want %q", got, "v1")
		}
	})
}
