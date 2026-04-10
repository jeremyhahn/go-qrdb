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

package statemachine

import (
	"bytes"
	"context"
	"testing"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"
)

// ---------------------------------------------------------------------------
// StandardAdapter tests
// ---------------------------------------------------------------------------

func TestStandardAdapter_InterfaceCompliance(t *testing.T) {
	var _ sm.StateMachine = (*StandardAdapter)(nil)
	var _ sm.Hasher = (*StandardAdapter)(nil)
}

func TestStandardAdapter_UpdateAndLookup(t *testing.T) {
	backend := NewStandardBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &StandardAdapter{KVStateMachine: kvsm}

	cmd, err := kv.EncodePut([]byte("a-key"), []byte("a-val"))
	if err != nil {
		t.Fatal(err)
	}
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := adapter.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision")
	}

	result, err := adapter.Lookup(context.Background(), []byte("a-key"))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	val, ok := result.([]byte)
	if !ok || string(val) != "a-val" {
		t.Fatalf("expected 'a-val', got %v", result)
	}
}

func TestStandardAdapter_SaveAndRecover(t *testing.T) {
	backend := NewStandardBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &StandardAdapter{KVStateMachine: kvsm}

	cmd, _ := kv.EncodePut([]byte("snap-key"), []byte("snap-val"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	_ = adapter.Update(context.Background(), entries, results)

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := adapter.SaveSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	backend2 := NewStandardBackend()
	kvsm2 := NewKVStateMachine(backend2)
	adapter2 := &StandardAdapter{KVStateMachine: kvsm2}

	if err := adapter2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	result, err := adapter2.Lookup(context.Background(), []byte("snap-key"))
	if err != nil {
		t.Fatalf("Lookup after recovery: %v", err)
	}
	val, ok := result.([]byte)
	if !ok || string(val) != "snap-val" {
		t.Fatalf("expected 'snap-val', got %v", result)
	}
}

func TestStandardAdapter_GetHash(t *testing.T) {
	backend := NewStandardBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &StandardAdapter{KVStateMachine: kvsm}

	h1, _ := adapter.GetHash()

	cmd, _ := kv.EncodePut([]byte("hk"), []byte("hv"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	_ = adapter.Update(context.Background(), entries, results)

	h2, _ := adapter.GetHash()
	if h1 == h2 {
		t.Fatal("hash should change after put")
	}
}

func TestStandardAdapter_Close(t *testing.T) {
	backend := NewStandardBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &StandardAdapter{KVStateMachine: kvsm}

	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ConcurrentAdapter tests
// ---------------------------------------------------------------------------

func TestConcurrentAdapter_InterfaceCompliance(t *testing.T) {
	var _ sm.ConcurrentStateMachine = (*ConcurrentAdapter)(nil)
	var _ sm.Hasher = (*ConcurrentAdapter)(nil)
}

func TestConcurrentAdapter_UpdateAndLookup(t *testing.T) {
	backend := NewConcurrentBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &ConcurrentAdapter{KVStateMachine: kvsm}

	cmd, _ := kv.EncodePut([]byte("c-key"), []byte("c-val"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := adapter.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	result, err := adapter.Lookup(context.Background(), []byte("c-key"))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	val, ok := result.([]byte)
	if !ok || string(val) != "c-val" {
		t.Fatalf("expected 'c-val', got %v", result)
	}
}

func TestConcurrentAdapter_PrepareAndSaveSnapshot(t *testing.T) {
	backend := NewConcurrentBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &ConcurrentAdapter{KVStateMachine: kvsm}

	cmd, _ := kv.EncodePut([]byte("cs-key"), []byte("cs-val"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	_ = adapter.Update(context.Background(), entries, results)

	snapshotCtx, err := adapter.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := adapter.SaveSnapshot(context.Background(), snapshotCtx, &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Recover.
	backend2 := NewConcurrentBackend()
	kvsm2 := NewKVStateMachine(backend2)
	adapter2 := &ConcurrentAdapter{KVStateMachine: kvsm2}

	if err := adapter2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	result, _ := adapter2.Lookup(context.Background(), []byte("cs-key"))
	val, ok := result.([]byte)
	if !ok || string(val) != "cs-val" {
		t.Fatalf("expected 'cs-val' after recovery, got %v", result)
	}
}

func TestConcurrentAdapter_Close(t *testing.T) {
	backend := NewConcurrentBackend()
	kvsm := NewKVStateMachine(backend)
	adapter := &ConcurrentAdapter{KVStateMachine: kvsm}

	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DiskAdapter tests
// ---------------------------------------------------------------------------

func TestDiskAdapter_InterfaceCompliance(t *testing.T) {
	var _ sm.DiskStateMachine = (*DiskAdapter)(nil)
	var _ sm.Hasher = (*DiskAdapter)(nil)
	var _ sm.NALookup = (*DiskAdapter)(nil)
}

func TestDiskAdapter_UpdateAndLookup(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	cmd, _ := kv.EncodePut([]byte("d-key"), []byte("d-val"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)

	if err := adapter.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	result, err := adapter.Lookup(context.Background(), []byte("d-key"))
	if err != nil {
		t.Fatalf("Lookup failed: %v", err)
	}
	val, ok := result.([]byte)
	if !ok || string(val) != "d-val" {
		t.Fatalf("expected 'd-val', got %v", result)
	}
}

func TestDiskAdapter_Open(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	idx, err := adapter.Open(context.Background(), "/tmp/test", make(chan struct{}))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0, got %d", idx)
	}
}

func TestDiskAdapter_Sync(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	if err := adapter.Sync(); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}
}

func TestDiskAdapter_BytesLookup(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	cmd, _ := kv.EncodePut([]byte("na-key"), []byte("na-val"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	_ = adapter.Update(context.Background(), entries, results)

	val, err := adapter.BytesLookup([]byte("na-key"))
	if err != nil {
		t.Fatalf("BytesLookup failed: %v", err)
	}
	if string(val) != "na-val" {
		t.Fatalf("expected 'na-val', got %q", val)
	}
}

func TestDiskAdapter_BytesLookup_NotFound(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	val, err := adapter.BytesLookup([]byte("ghost"))
	if err != nil {
		t.Fatalf("BytesLookup failed: %v", err)
	}
	if val != nil {
		t.Fatalf("expected nil, got %q", val)
	}
}

func TestDiskAdapter_PrepareAndSaveSnapshot(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	cmd, _ := kv.EncodePut([]byte("ds-key"), []byte("ds-val"))
	entries := []sm.Entry{{Cmd: cmd}}
	results := make([]sm.Result, 1)
	_ = adapter.Update(context.Background(), entries, results)

	snapshotCtx, err := adapter.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot failed: %v", err)
	}

	var buf bytes.Buffer
	stopper := make(chan struct{})
	if err := adapter.SaveSnapshot(context.Background(), snapshotCtx, &buf, stopper); err != nil {
		t.Fatalf("SaveSnapshot failed: %v", err)
	}

	// Recover into new adapter.
	store2 := newMemStorage()
	backend2 := NewDiskBackend(store2)
	kvsm2 := NewKVStateMachine(backend2)
	adapter2 := &DiskAdapter{KVStateMachine: kvsm2, store: store2}

	if err := adapter2.RecoverFromSnapshot(context.Background(), &buf, stopper); err != nil {
		t.Fatalf("RecoverFromSnapshot failed: %v", err)
	}

	result, _ := adapter2.Lookup(context.Background(), []byte("ds-key"))
	val, ok := result.([]byte)
	if !ok || string(val) != "ds-val" {
		t.Fatalf("expected 'ds-val', got %v", result)
	}
}

func TestDiskAdapter_Close(t *testing.T) {
	store := newMemStorage()
	backend := NewDiskBackend(store)
	kvsm := NewKVStateMachine(backend)
	adapter := &DiskAdapter{KVStateMachine: kvsm, store: store}

	if err := adapter.Close(context.Background()); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
