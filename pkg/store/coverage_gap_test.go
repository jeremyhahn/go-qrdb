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

//go:build !integration

package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// MVCCStateMachine: Lessor before Open (80% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LessorBeforeOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	defer lr.Close()

	// Before Open, Lessor() returns the pre-configured lessor directly.
	s.SetLessor(lr)
	if s.Lessor() != lr {
		t.Fatal("expected the pre-Open lessor to be returned")
	}
}

func TestMVCCStateMachine_LessorAfterOpen(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	dir := t.TempDir()
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	// After Open, Lessor() delegates through the engine.
	got := s.Lessor()
	if got == nil {
		t.Fatal("expected non-nil lessor after open")
	}
}

func TestMVCCStateMachine_LessorNilWhenNotSet(t *testing.T) {
	s := NewMVCCStateMachine(nil)

	// No lessor configured, no engine open -- should return nil without panic.
	if s.Lessor() != nil {
		t.Fatal("expected nil lessor when not configured")
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: Open error paths (73.7% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_OpenInvalidDir(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	_, err := s.Open(context.Background(), "/dev/null/nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for invalid directory")
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
}

func TestMVCCStateMachine_OpenWithLessor(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	dir := t.TempDir()
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with lessor: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for fresh DB, got %d", idx)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	if s.Lessor() == nil {
		t.Fatal("expected lessor to be available after open")
	}
}

func TestMVCCStateMachine_OpenWithHub(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	dir := t.TempDir()
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with hub: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for fresh DB, got %d", idx)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(context.Background())
	})
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: clearAll via snapshot recovery (82.4% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ClearAll_ViaSnapshotRecovery(t *testing.T) {
	src := newTestMVCCStateMachine(t)
	dst := newTestMVCCStateMachine(t)

	// Put keys in src.
	applyMVCCEntries(t, src, []sm.Entry{
		mvccPutEntry(1, []byte("mvcc-snap-key"), []byte("mvcc-snap-val"), 0),
	})
	if err := src.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Put a different key in dst.
	applyMVCCEntries(t, dst, []sm.Entry{
		mvccPutEntry(1, []byte("mvcc-old-key"), []byte("mvcc-old-val"), 0),
	})

	// Save snapshot from src.
	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Recover dst from src's snapshot.
	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// dst should have mvcc-snap-key.
	val, err := dst.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("mvcc-snap-key")})
	if err != nil {
		t.Fatalf("Lookup mvcc-snap-key: %v", err)
	}
	kv := val.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("mvcc-snap-val")) {
		t.Fatalf("expected mvcc-snap-val, got %q", kv.Value)
	}

	// dst should not have mvcc-old-key.
	_, err = dst.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("mvcc-old-key")})
	if err == nil {
		t.Fatal("expected mvcc-old-key to be cleared after snapshot recovery")
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: GetHash with data (94.4% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_GetHash_WithData(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("hk1"), []byte("hv1"), 0),
		mvccPutEntry(2, []byte("hk2"), []byte("hv2"), 0),
	})

	h1, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if h1 == 0 {
		t.Fatal("expected non-zero hash with data")
	}

	// Hash changes after a new key is added.
	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(3, []byte("hk3"), []byte("hv3"), 0),
	})

	h2, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash after mutation: %v", err)
	}
	if h2 == h1 {
		t.Fatal("expected hash to change after adding a new key")
	}
}

func TestMVCCStateMachine_GetHash_EmptyDB(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	h, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash empty: %v", err)
	}
	_ = h
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: Update - revision persisted in batch (88.9% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_Update_PersistsRevision(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("rev-key"), []byte("v1"), 0),
		mvccPutEntry(2, []byte("rev-key2"), []byte("v2"), 0),
	})

	rev, err := s.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup revision: %v", err)
	}
	revVal := rev.(int64)
	if revVal <= 0 {
		t.Fatalf("expected positive revision, got %d", revVal)
	}

	// Close and reopen -- last applied index should be restored.
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2 := NewMVCCStateMachine(nil)
	idx, err := s2.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	t.Cleanup(func() { _ = s2.Close(context.Background()) })

	if idx != 2 {
		t.Fatalf("expected last applied index 2, got %d", idx)
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: readLastApplied error branch (87.5% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_ReadLastApplied_MalformedValue(t *testing.T) {
	// Open a pebble DB directly and write a malformed lastApplied value.
	dir := filepath.Join(t.TempDir(), "db")
	db, err := pebble.Open(dir, nil)
	if err != nil {
		t.Fatalf("pebble.Open: %v", err)
	}

	// Write a malformed value (not 8 bytes) for the last applied key.
	if err := db.Set([]byte(metaKeyLastApplied), []byte{0x01, 0x02, 0x03}, pebble.Sync); err != nil {
		t.Fatalf("Set malformed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close: %v", err)
	}

	// Open the MVCCStateMachine -- it should return 0 for malformed data.
	s := NewMVCCStateMachine(nil)
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with malformed last applied: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	// 3-byte value != 8 bytes, so readLastApplied returns 0.
	if idx != 0 {
		t.Fatalf("expected 0 for malformed last applied, got %d", idx)
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: SaveSnapshot round-trip (86.0% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SaveSnapshot_RoundTrip(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("snap-k1"), []byte("snap-v1"), 0),
		mvccPutEntry(2, []byte("snap-k2"), []byte("snap-v2"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	var buf bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Recover into a fresh state machine.
	s2 := newTestMVCCStateMachine(t)
	if err := s2.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	for _, tc := range []struct {
		key, val string
	}{
		{"snap-k1", "snap-v1"},
		{"snap-k2", "snap-v2"},
	} {
		result, err := s2.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte(tc.key)})
		if err != nil {
			t.Fatalf("Lookup %s: %v", tc.key, err)
		}
		kv := result.(*service.KeyValue)
		if !bytes.Equal(kv.Value, []byte(tc.val)) {
			t.Fatalf("expected %s, got %q", tc.val, kv.Value)
		}
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: RecoverFromSnapshot (87.0% -> improve)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_RecoverFromSnapshot_WithTimeSeries(t *testing.T) {
	// Verify that RecoverFromSnapshot preserves time-series when enabled.
	src := NewMVCCStateMachine(nil)
	src.enableTimeSeries = true
	srcDir := filepath.Join(t.TempDir(), "src")
	if _, err := src.Open(context.Background(), srcDir, nil); err != nil {
		t.Fatalf("src Open: %v", err)
	}
	t.Cleanup(func() { _ = src.Close(context.Background()) })

	applyMVCCEntries(t, src, []sm.Entry{
		mvccPutEntry(1, []byte("ts-key"), []byte("ts-val"), 0),
	})
	if err := src.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	dst := NewMVCCStateMachine(nil)
	dst.enableTimeSeries = true
	dstDir := filepath.Join(t.TempDir(), "dst")
	if _, err := dst.Open(context.Background(), dstDir, nil); err != nil {
		t.Fatalf("dst Open: %v", err)
	}
	t.Cleanup(func() { _ = dst.Close(context.Background()) })

	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	result, err := dst.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("ts-key")})
	if err != nil {
		t.Fatalf("Lookup ts-key: %v", err)
	}
	kv := result.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("ts-val")) {
		t.Fatalf("expected ts-val, got %q", kv.Value)
	}
}

// ---------------------------------------------------------------------------
// Factory: WithCacheSize and WithWALDir (factory options coverage)
// ---------------------------------------------------------------------------

func TestWithCacheSize_Option(t *testing.T) {
	cf := NewMVCCCreateFunc(WithCacheSize(16 * 1024 * 1024))
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

func TestWithWALDir_Option(t *testing.T) {
	walDir := t.TempDir()
	cf := NewMVCCCreateFunc(WithWALDir(walDir))
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

// ---------------------------------------------------------------------------
// MVCCStateMachine: pebbleHandleTSWrite/WriteBatch/WritePoint/etc. error paths
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_TSWrite_EmptyPayload(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	s.enableTimeSeries = true
	dir := t.TempDir()
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	cmd := []byte{protocol.OpTSWrite} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid MVCC TSWrite empty payload")
	}
}
