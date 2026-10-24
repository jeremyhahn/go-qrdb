// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package encfs_test

import (
	"errors"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/jeremyhahn/go-qrdb/pkg/seal/encfs"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

var errKEKSealed = errors.New("sealableKEK: sealed")

// sealableKEK is a KEK that reports its state the way go-qrdb's
// StorageBarrier and go-quicraft's EpochBarrier do, and refuses to wrap or
// unwrap a DEK while sealed, as they do.
type sealableKEK struct {
	fakeKEK
	sealedState atomic.Bool
}

func (k *sealableKEK) IsSealed() bool { return k.sealedState.Load() }

func (k *sealableKEK) Encrypt(plaintext []byte) ([]byte, error) {
	if k.IsSealed() {
		return nil, errKEKSealed
	}
	return k.fakeKEK.Encrypt(plaintext)
}

func (k *sealableKEK) Decrypt(ciphertext []byte) ([]byte, error) {
	if k.IsSealed() {
		return nil, errKEKSealed
	}
	return k.fakeKEK.Decrypt(ciphertext)
}

func TestFS_BarrierSealedReportsTheBarrierState(t *testing.T) {
	kek := &sealableKEK{}
	fs := encfs.New(vfs.NewMem(), kek).(interface{ BarrierSealed() bool })
	if fs.BarrierSealed() {
		t.Fatal("BarrierSealed() = true under an unsealed barrier")
	}
	kek.sealedState.Store(true)
	if !fs.BarrierSealed() {
		t.Fatal("BarrierSealed() = false under a sealed barrier")
	}
}

func TestFS_BarrierSealedIsFalseWhenTheBarrierCannotSay(t *testing.T) {
	fs := encfs.New(vfs.NewMem(), fakeKEK{}).(interface{ BarrierSealed() bool })
	if fs.BarrierSealed() {
		t.Fatal("BarrierSealed() = true for a barrier with no IsSealed")
	}
}

// TestFS_SealedBarrierRefusesCreate pins the mechanism behind the store-level
// refusal below: every new file, a WAL included, needs the barrier to wrap
// its DEK.
func TestFS_SealedBarrierRefusesCreate(t *testing.T) {
	kek := &sealableKEK{}
	kek.sealedState.Store(true)
	fs := encfs.New(vfs.NewMem(), kek)
	if _, err := fs.Create("000002.log", vfs.WriteCategoryUnspecified); !errors.Is(err, errKEKSealed) {
		t.Fatalf("Create under a sealed barrier = %v; want the barrier's error", err)
	}
}

// TestPebbleStorage_SealedBarrierRefusesInsteadOfPanicking drives a store on
// encfs across a seal. Every flush rotates the WAL, and Pebble panics when it
// cannot create the next one, so without the refusal each call below that
// reaches Pebble while sealed ends the test binary.
func TestPebbleStorage_SealedBarrierRefusesInsteadOfPanicking(t *testing.T) {
	kek := &sealableKEK{}
	cfg := pebblestore.TestConfig("db")
	cfg.FS = encfs.New(vfs.NewMem(), kek)
	store, err := pebblestore.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if err := store.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	kek.sealedState.Store(true)
	var sealed *pebblestore.SealedError
	if err := store.Flush(); !errors.As(err, &sealed) || !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Flush under a sealed barrier = %v; want a *SealedError", err)
	}
	if err := store.Set([]byte("k2"), []byte("v")); !errors.As(err, &sealed) {
		t.Fatalf("Set under a sealed barrier = %v; want a *SealedError", err)
	}
	if err := store.Delete([]byte("k")); !errors.As(err, &sealed) {
		t.Fatalf("Delete under a sealed barrier = %v; want a *SealedError", err)
	}
	b := store.Batch()
	if err := b.Set([]byte("k3"), []byte("v")); err != nil {
		t.Fatalf("Batch.Set: %v", err)
	}
	if err := b.Commit(); !errors.As(err, &sealed) {
		t.Fatalf("Batch.Commit under a sealed barrier = %v; want a *SealedError", err)
	}
	if err := store.Compact(nil, nil); !errors.As(err, &sealed) {
		t.Fatalf("Compact under a sealed barrier = %v; want a *SealedError", err)
	}

	kek.sealedState.Store(false)
	if err := store.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Set after unseal: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush after unseal: %v", err)
	}
	got, err := store.Get([]byte("k2"))
	if err != nil || string(got) != "v2" {
		t.Fatalf("Get after unseal = %q, %v; want v2", got, err)
	}
}

// TestNewFromDB_SealedBarrierRefusesInsteadOfPanicking drives a store built
// with NewFromDB over a database pebble.Open opened on encfs, the shape
// pkg/store's MVCC and time-series stores use, across a seal.
func TestNewFromDB_SealedBarrierRefusesInsteadOfPanicking(t *testing.T) {
	kek := &sealableKEK{}
	db, seals, err := pebblestore.Open("db", &pebble.Options{FS: encfs.New(vfs.NewMem(), kek)}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	store := pebblestore.NewFromDB(db, true, pebblestore.WithSealWatch(seals))
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if err := store.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Set: %v", err)
	}

	kek.sealedState.Store(true)
	if err := store.Flush(); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Flush under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
	if err := store.Set([]byte("k2"), []byte("v")); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Set under a sealed barrier = %v; want ErrBarrierSealed", err)
	}
	if err := store.Compact(nil, nil); !errors.Is(err, pebblestore.ErrBarrierSealed) {
		t.Fatalf("Compact under a sealed barrier = %v; want ErrBarrierSealed", err)
	}

	kek.sealedState.Store(false)
	if err := store.Set([]byte("k2"), []byte("v2")); err != nil {
		t.Fatalf("Set after unseal: %v", err)
	}
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush after unseal: %v", err)
	}
}
