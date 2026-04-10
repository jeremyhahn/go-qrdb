// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir"
)

// failingBackend is a StorageBackend that returns errors on demand.
// Each method error can be set independently. When nil, the operation
// delegates to an in-memory fallback.
type failingBackend struct {
	inner    *MemoryBackend
	getErr   error
	putErr   error
	delErr   error
	listErr  error
	existErr error
}

func newFailingBackend() *failingBackend {
	return &failingBackend{inner: NewMemoryBackend()}
}

func (f *failingBackend) Get(ctx context.Context, key string) ([]byte, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	return f.inner.Get(ctx, key)
}

func (f *failingBackend) Put(ctx context.Context, key string, data []byte) error {
	if f.putErr != nil {
		return f.putErr
	}
	return f.inner.Put(ctx, key, data)
}

func (f *failingBackend) Delete(ctx context.Context, key string) error {
	if f.delErr != nil {
		return f.delErr
	}
	return f.inner.Delete(ctx, key)
}

func (f *failingBackend) List(ctx context.Context, prefix string) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.inner.List(ctx, prefix)
}

func (f *failingBackend) Exists(ctx context.Context, key string) (bool, error) {
	if f.existErr != nil {
		return false, f.existErr
	}
	return f.inner.Exists(ctx, key)
}

func (f *failingBackend) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	return f.inner.Scan(ctx, prefix, fn)
}

func (f *failingBackend) Close() error { return nil }

// ---------- NewShamirStrategy tests ----------

func TestStrategy_Shamir_NewShamirStrategy_Success(t *testing.T) {
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	if s.ID() != StrategyShamir {
		t.Errorf("expected ID=%s, got %s", StrategyShamir, s.ID())
	}
	if !s.Available() {
		t.Error("expected Available()=true")
	}
	if s.HardwareBacked() {
		t.Error("expected HardwareBacked()=false")
	}
	if s.Threshold() != 2 {
		t.Errorf("expected Threshold=2, got %d", s.Threshold())
	}
	if s.TotalShares() != 3 {
		t.Errorf("expected TotalShares=3, got %d", s.TotalShares())
	}
	if s.Storage() != store {
		t.Error("expected Storage() to return the provided store")
	}
}

func TestStrategy_Shamir_NewShamirStrategy_NilStorage(t *testing.T) {
	_, err := NewShamirStrategy(nil, 2, 3)
	if err != ErrShamirNilStorage {
		t.Errorf("expected ErrShamirNilStorage, got %v", err)
	}
}

func TestStrategy_Shamir_NewShamirStrategy_InvalidThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
		total     int
	}{
		{"threshold_below_2", 1, 3},
		{"threshold_exceeds_total", 5, 3},
		{"threshold_zero", 0, 3},
		{"negative_threshold", -1, 3},
	}

	store := NewMemoryBackend()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewShamirStrategy(store, tc.threshold, tc.total)
			if err != ErrShamirThresholdInvalid {
				t.Errorf("expected ErrShamirThresholdInvalid, got %v", err)
			}
		})
	}
}

// ---------- SealRootKey / UnsealRootKey tests ----------

func TestStrategy_Shamir_SealUnsealRoundTrip(t *testing.T) {
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef") // 32 bytes
	ctx := context.Background()

	sealed, err := s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	if sealed.Strategy != StrategyShamir {
		t.Errorf("expected Strategy=%s, got %s", StrategyShamir, sealed.Strategy)
	}
	if sealed.ShamirThreshold != 2 {
		t.Errorf("expected ShamirThreshold=2, got %d", sealed.ShamirThreshold)
	}
	if sealed.ShamirTotal != 3 {
		t.Errorf("expected ShamirTotal=3, got %d", sealed.ShamirTotal)
	}

	recovered, err := s.UnsealRootKey(ctx, sealed, Credentials{})
	if err != nil {
		t.Fatalf("UnsealRootKey: %v", err)
	}

	if string(recovered) != string(rootKey) {
		t.Errorf("recovered root key mismatch: got %x, want %x", recovered, rootKey)
	}
}

func TestStrategy_Shamir_SealRootKey_PersistSharesFails(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Make Put fail so persistShares returns an error.
	fb.putErr = errors.New("disk full")

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err == nil {
		t.Fatal("expected error from persistShares failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

func TestStrategy_Shamir_UnsealRootKey_NilSealed(t *testing.T) {
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	_, err = s.UnsealRootKey(context.Background(), nil, Credentials{})
	if err != ErrNilSealedData {
		t.Errorf("expected ErrNilSealedData, got %v", err)
	}
}

func TestStrategy_Shamir_UnsealRootKey_NoShares(t *testing.T) {
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	sealed := &SealedRootKey{
		Version:         1,
		Strategy:        StrategyShamir,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}

	_, err = s.UnsealRootKey(context.Background(), sealed, Credentials{})
	if err != ErrShamirNoSharesFound {
		t.Errorf("expected ErrShamirNoSharesFound, got %v", err)
	}
}

func TestStrategy_Shamir_UnsealRootKey_QuorumIncomplete(t *testing.T) {
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 3, 5)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Seal with threshold=3, total=5, then delete shares to get below threshold.
	rootKey := []byte("0123456789abcdef0123456789abcdef")
	ctx := context.Background()
	sealed, err := s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Delete shares until only 2 remain (below threshold of 3).
	if err := s.DeleteShare(ctx, 1); err != nil {
		t.Fatalf("DeleteShare(1): %v", err)
	}
	if err := s.DeleteShare(ctx, 2); err != nil {
		t.Fatalf("DeleteShare(2): %v", err)
	}
	if err := s.DeleteShare(ctx, 3); err != nil {
		t.Fatalf("DeleteShare(3): %v", err)
	}

	_, err = s.UnsealRootKey(ctx, sealed, Credentials{})
	if err != ErrShamirQuorumIncomplete {
		t.Errorf("expected ErrShamirQuorumIncomplete, got %v", err)
	}
}

func TestStrategy_Shamir_UnsealRootKey_CombineFails(t *testing.T) {
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Seal normally, then corrupt shares in storage.
	rootKey := []byte("0123456789abcdef0123456789abcdef")
	ctx := context.Background()
	sealed, err := s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Corrupt the share values by writing bad data.
	badShare := &shamir.Share{Index: 1, Threshold: 2, Total: 3, Value: "corrupted!!"}
	data, _ := json.Marshal(badShare)
	if err := store.Put(ctx, "shamir-share-1", data); err != nil {
		t.Fatalf("Put corrupt share: %v", err)
	}
	badShare2 := &shamir.Share{Index: 2, Threshold: 2, Total: 3, Value: "also-bad!!"}
	data2, _ := json.Marshal(badShare2)
	if err := store.Put(ctx, "shamir-share-2", data2); err != nil {
		t.Fatalf("Put corrupt share 2: %v", err)
	}

	_, err = s.UnsealRootKey(ctx, sealed, Credentials{})
	if err == nil {
		t.Fatal("expected error from corrupted shares")
	}
	if !errors.Is(err, ErrShamirCombineFailed) {
		t.Errorf("expected ErrShamirCombineFailed, got %v", err)
	}
}

func TestStrategy_Shamir_UnsealRootKey_LoadAllSharesError(t *testing.T) {
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Make List fail.
	fb.listErr = errors.New("storage unavailable")

	sealed := &SealedRootKey{
		Version:         1,
		Strategy:        StrategyShamir,
		ShamirThreshold: 2,
		ShamirTotal:     3,
	}

	_, err = s.UnsealRootKey(context.Background(), sealed, Credentials{})
	if err == nil {
		t.Fatal("expected error from LoadAllShares failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

// ---------- Share management tests ----------

func TestStrategy_Shamir_LoadShare_Success(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	share, err := s.LoadShare(ctx, 1)
	if err != nil {
		t.Fatalf("LoadShare(1): %v", err)
	}
	if share.Index != 1 {
		t.Errorf("expected Index=1, got %d", share.Index)
	}
	if share.Threshold != 2 {
		t.Errorf("expected Threshold=2, got %d", share.Threshold)
	}
}

func TestStrategy_Shamir_LoadShare_NotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	_, err = s.LoadShare(ctx, 999)
	if err == nil {
		t.Fatal("expected error for non-existent share")
	}
	if !errors.Is(err, ErrShamirShareNotFound) {
		t.Errorf("expected ErrShamirShareNotFound, got %v", err)
	}
}

func TestStrategy_Shamir_LoadShare_CorruptJSON(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Store corrupt (non-JSON) data at the share key.
	if err := store.Put(ctx, "shamir-share-1", []byte("not-json{")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err = s.LoadShare(ctx, 1)
	if err == nil {
		t.Fatal("expected error for corrupt JSON")
	}
	if !errors.Is(err, ErrShamirSerializationFailed) {
		t.Errorf("expected ErrShamirSerializationFailed, got %v", err)
	}
}

func TestStrategy_Shamir_LoadAllShares_Success(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	shares, err := s.LoadAllShares(ctx)
	if err != nil {
		t.Fatalf("LoadAllShares: %v", err)
	}
	if len(shares) != 3 {
		t.Errorf("expected 3 shares, got %d", len(shares))
	}
}

func TestStrategy_Shamir_LoadAllShares_Empty(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	_, err = s.LoadAllShares(ctx)
	if err != ErrShamirNoSharesFound {
		t.Errorf("expected ErrShamirNoSharesFound, got %v", err)
	}
}

func TestStrategy_Shamir_LoadAllShares_ListError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	fb.listErr = errors.New("list failed")

	_, err = s.LoadAllShares(ctx)
	if err == nil {
		t.Fatal("expected error from List failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

func TestStrategy_Shamir_LoadAllShares_GetError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Seal successfully first (Put must work).
	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Now make Get fail.
	fb.getErr = errors.New("get failed")

	_, err = s.LoadAllShares(ctx)
	if err == nil {
		t.Fatal("expected error from Get failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

func TestStrategy_Shamir_LoadAllShares_CorruptJSON(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Store corrupt data at a share key.
	if err := store.Put(ctx, "shamir-share-1", []byte("{invalid")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err = s.LoadAllShares(ctx)
	if err == nil {
		t.Fatal("expected error for corrupt JSON in LoadAllShares")
	}
	if !errors.Is(err, ErrShamirSerializationFailed) {
		t.Errorf("expected ErrShamirSerializationFailed, got %v", err)
	}
}

func TestStrategy_Shamir_DeleteShare_Success(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	if err := s.DeleteShare(ctx, 1); err != nil {
		t.Fatalf("DeleteShare(1): %v", err)
	}

	count, err := s.ShareCount(ctx)
	if err != nil {
		t.Fatalf("ShareCount: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 shares remaining, got %d", count)
	}
}

func TestStrategy_Shamir_DeleteShare_StorageError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Store a share first so Exists returns true, then inject delete error.
	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}
	fb.delErr = errors.New("delete failed")

	err = s.DeleteShare(ctx, 1)
	if err == nil {
		t.Fatal("expected error from Delete failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

func TestStrategy_Shamir_DeleteShare_NotFound(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	err = s.DeleteShare(ctx, 99)
	if err == nil {
		t.Fatal("expected error for non-existent share")
	}
	if !errors.Is(err, ErrShamirShareNotFound) {
		t.Errorf("expected ErrShamirShareNotFound, got %v", err)
	}
}

func TestStrategy_Shamir_DeleteAllShares_Success(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	if err := s.DeleteAllShares(ctx); err != nil {
		t.Fatalf("DeleteAllShares: %v", err)
	}

	count, err := s.ShareCount(ctx)
	if err != nil {
		t.Fatalf("ShareCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 shares, got %d", count)
	}
}

func TestStrategy_Shamir_DeleteAllShares_Empty(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Deleting when no shares exist should not error.
	if err := s.DeleteAllShares(ctx); err != nil {
		t.Fatalf("DeleteAllShares (empty): %v", err)
	}
}

func TestStrategy_Shamir_DeleteAllShares_ListError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	fb.listErr = errors.New("list failed")

	err = s.DeleteAllShares(ctx)
	if err == nil {
		t.Fatal("expected error from List failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

func TestStrategy_Shamir_DeleteAllShares_DeleteError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Seal successfully first.
	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Now make Delete fail.
	fb.delErr = errors.New("delete failed")

	err = s.DeleteAllShares(ctx)
	if err == nil {
		t.Fatal("expected error from Delete failure in loop")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

// ---------- VerifyShares tests ----------

func TestStrategy_Shamir_VerifyShares_Success(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	if err := s.VerifyShares(ctx); err != nil {
		t.Fatalf("VerifyShares: %v", err)
	}
}

func TestStrategy_Shamir_VerifyShares_NoShares(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	err = s.VerifyShares(ctx)
	if err != ErrShamirNoSharesFound {
		t.Errorf("expected ErrShamirNoSharesFound, got %v", err)
	}
}

func TestStrategy_Shamir_VerifyShares_WrongThreshold(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()

	s1, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s1.SealRootKey(context.Background(), rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Create a strategy with a different threshold reading the same store.
	s2, err := NewShamirStrategy(store, 3, 5)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	err = s2.VerifyShares(ctx)
	if err == nil {
		t.Fatal("expected verification error for threshold mismatch")
	}
	if !errors.Is(err, ErrShamirVerificationFailed) {
		t.Errorf("expected ErrShamirVerificationFailed, got %v", err)
	}
}

func TestStrategy_Shamir_VerifyShares_WrongTotal(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()

	s1, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s1.SealRootKey(context.Background(), rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	// Create a strategy with matching threshold but different total.
	s2, err := NewShamirStrategy(store, 2, 5)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	err = s2.VerifyShares(ctx)
	if err == nil {
		t.Fatal("expected verification error for total mismatch")
	}
	if !errors.Is(err, ErrShamirVerificationFailed) {
		t.Errorf("expected ErrShamirVerificationFailed, got %v", err)
	}
}

func TestStrategy_Shamir_VerifyShares_DuplicateIndex(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	// Manually persist two shares with the same index at different keys.
	share := &shamir.Share{Index: 1, Threshold: 2, Total: 3, Value: "abc"}
	data1, err := json.Marshal(share)
	if err != nil {
		t.Fatalf("marshal share1: %v", err)
	}
	if err := store.Put(ctx, "shamir-share-1", data1); err != nil {
		t.Fatalf("Put share1: %v", err)
	}

	share2 := &shamir.Share{Index: 1, Threshold: 2, Total: 3, Value: "def"}
	data2, err := json.Marshal(share2)
	if err != nil {
		t.Fatalf("marshal share2: %v", err)
	}
	if err := store.Put(ctx, "shamir-share-2", data2); err != nil {
		t.Fatalf("Put share2: %v", err)
	}

	err = s.VerifyShares(ctx)
	if err == nil {
		t.Fatal("expected verification error for duplicate index")
	}
	if !errors.Is(err, ErrShamirVerificationFailed) {
		t.Errorf("expected ErrShamirVerificationFailed, got %v", err)
	}
}

// ---------- ShareCount tests ----------

func TestStrategy_Shamir_ShareCount_Empty(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	count, err := s.ShareCount(ctx)
	if err != nil {
		t.Fatalf("ShareCount: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0, got %d", count)
	}
}

func TestStrategy_Shamir_ShareCount_AfterSeal(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	_, err = s.SealRootKey(ctx, rootKey, Credentials{})
	if err != nil {
		t.Fatalf("SealRootKey: %v", err)
	}

	count, err := s.ShareCount(ctx)
	if err != nil {
		t.Fatalf("ShareCount: %v", err)
	}
	if count != 3 {
		t.Errorf("expected 3, got %d", count)
	}
}

func TestStrategy_Shamir_ShareCount_StorageError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	fb.listErr = errors.New("list failed")

	_, err = s.ShareCount(ctx)
	if err == nil {
		t.Fatal("expected error from List failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

// ---------- BarrierEncryptor tests ----------

func TestStrategy_Shamir_BarrierEncryptor_ReturnsNil(t *testing.T) {
	// ShamirStrategy delegates encryption to the barrier (not its own encryptor),
	// so BarrierEncryptor returns nil, nil — signaling the barrier should use
	// its own epoch-based HKDF DEK encryption.
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	rootKey := []byte("0123456789abcdef0123456789abcdef")
	enc, err := s.BarrierEncryptor(context.Background(), rootKey)
	if err != nil {
		t.Fatalf("BarrierEncryptor: unexpected error: %v", err)
	}
	if enc != nil {
		t.Error("expected nil encryptor from ShamirStrategy (barrier handles encryption)")
	}
}

func TestStrategy_Shamir_BarrierEncryptor_NilForDifferentKeys(t *testing.T) {
	store := NewMemoryBackend()
	s, err := NewShamirStrategy(store, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	enc1, err := s.BarrierEncryptor(context.Background(), []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatalf("BarrierEncryptor(key1): %v", err)
	}

	enc2, err := s.BarrierEncryptor(context.Background(), []byte("fedcba9876543210fedcba9876543210"))
	if err != nil {
		t.Fatalf("BarrierEncryptor(key2): %v", err)
	}

	if enc1 != nil || enc2 != nil {
		t.Error("expected nil encryptors from ShamirStrategy")
	}
}

// ---------- persistShares error tests ----------

func TestStrategy_Shamir_PersistShares_StoragePutError(t *testing.T) {
	ctx := context.Background()
	fb := newFailingBackend()
	s, err := NewShamirStrategy(fb, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}

	fb.putErr = errors.New("write failed")

	shares := []*shamir.Share{
		{Index: 1, Threshold: 2, Total: 3, Value: "a"},
		{Index: 2, Threshold: 2, Total: 3, Value: "b"},
	}

	err = s.persistShares(ctx, shares)
	if err == nil {
		t.Fatal("expected error from Put failure")
	}
	if !errors.Is(err, ErrShamirStorageFailed) {
		t.Errorf("expected ErrShamirStorageFailed, got %v", err)
	}
}

// ---------- shamirShareKey tests ----------

func TestStrategy_Shamir_ShareKeyFormat(t *testing.T) {
	tests := []struct {
		index    int
		expected string
	}{
		{1, "shamir-share-1"},
		{42, "shamir-share-42"},
		{100, "shamir-share-100"},
	}

	for _, tc := range tests {
		got := shamirShareKey(tc.index)
		if got != tc.expected {
			t.Errorf("shamirShareKey(%d): got %q, want %q", tc.index, got, tc.expected)
		}
	}
}
