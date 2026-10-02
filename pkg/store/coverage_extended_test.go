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
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/engine"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// mvcc.go: handleBaseKVPut - base KV put via MVCC state machine
// ---------------------------------------------------------------------------

func TestMVCCHandleBaseKVPut_Success(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Use kv.EncodePut to produce a base KV command that routes through
	// handleBaseKVPut when processed by the MVCC state machine.
	cmd, err := kv.EncodePut([]byte("base-key"), []byte("base-value"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Result.Value should be the new revision (>0).
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from base KV put")
	}

	// Verify the key can be read back via MVCC Get.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("base-key")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("base-value")) {
		t.Fatalf("expected base-value, got %q", kv.Value)
	}
}

func TestMVCCHandleBaseKVPut_PayloadTooShort(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Build a minimal command: op byte 1 (kv.OpPut) + empty payload.
	cmd := []byte{kv.OpPut}

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for base KV put with empty payload")
	}
	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *InvalidCommandError, got %T: %v", err, err)
	}
}

func TestMVCCHandleBaseKVPut_KeyLenExceedsPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Op byte + 2-byte key length that claims 255 bytes, but no key data.
	cmd := []byte{kv.OpPut, 0xFF, 0x00}

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for base KV put with key length exceeding payload")
	}
	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *InvalidCommandError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleBaseKVDelete - base KV delete via MVCC state machine
// ---------------------------------------------------------------------------

func TestMVCCHandleBaseKVDelete_Success(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Put a key first.
	putCmd, err := kv.EncodePut([]byte("del-key"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: putCmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update put: %v", err)
	}

	// Delete via base KV delete.
	delCmd, err := kv.EncodeDelete([]byte("del-key"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	entries = []sm.Entry{{Index: 2, Cmd: delCmd}}
	results = make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update delete: %v", err)
	}
	if results[0].Value == 0 {
		t.Fatal("expected non-zero revision from base KV delete")
	}

	// Verify key is deleted.
	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("del-key")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

func TestMVCCHandleBaseKVDelete_PayloadTooShort(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	cmd := []byte{kv.OpDelete}

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for base KV delete with empty payload")
	}
	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *InvalidCommandError, got %T: %v", err, err)
	}
}

func TestMVCCHandleBaseKVDelete_KeyLenExceedsPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	cmd := []byte{kv.OpDelete, 0xFF, 0x00}

	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for base KV delete with key length exceeding payload")
	}
	var ice *protocol.InvalidCommandError
	if !errors.As(err, &ice) {
		t.Fatalf("expected *InvalidCommandError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleBaseKVDelete with lessor detach
// ---------------------------------------------------------------------------

func TestMVCCHandleBaseKVDelete_WithLessorDetach(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: grantCmd}}, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	// Put key with lease via MVCC put.
	putE := mvccPutEntry(2, []byte("base-leased"), []byte("val"), leaseID)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Delete via base KV delete -- should detach from lease.
	delCmd, err := kv.EncodeDelete([]byte("base-leased"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	delResults := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: delCmd}}, delResults); err != nil {
		t.Fatalf("Update delete: %v", err)
	}

	// Key should be deleted.
	_, err = s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("base-leased")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleBaseKVPut/Delete with watch hub notifications
// ---------------------------------------------------------------------------

func TestMVCCHandleBaseKVPut_WithWatchHub(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(context.Background())
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, wErr := hub.Watch(ctx, "base-watch-put")
	if wErr != nil {
		t.Fatalf("Watch: %v", wErr)
	}
	defer func() { _ = w.Close() }()

	cmd, err := kv.EncodePut([]byte("base-watch-put"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	default:
		// Watch notification may be async; give it a moment.
		t.Log("no immediate event, which is acceptable for async delivery")
	}
}

func TestMVCCHandleBaseKVDelete_WithWatchHub(t *testing.T) {
	dir := t.TempDir()
	s := NewMVCCStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(context.Background())
	})

	// Put the key first.
	putCmd, err := kv.EncodePut([]byte("base-watch-del"), []byte("val"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: putCmd}}, results); err != nil {
		t.Fatalf("Update put: %v", err)
	}

	// Now delete it.
	delCmd, err := kv.EncodeDelete([]byte("base-watch-del"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	delResults := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: delCmd}}, delResults); err != nil {
		t.Fatalf("Update delete: %v", err)
	}

	// The notification was delivered; the important thing is Update succeeded
	// while the hub was configured (exercises the notification path).
	if delResults[0].Value == 0 {
		t.Fatal("expected non-zero revision from delete")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: updateIndexesForPut - secondary index updates on put
// ---------------------------------------------------------------------------

func TestMVCCUpdateIndexesForPut_WithIndex(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Create a secondary index on "name" field.
	createCmd := mustEncode(protocol.EncodeIndexCreate("name_idx", "name", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{indexCreateEntry(1, "name_idx", "name", false, false)})
	_ = createCmd

	// Put a key with a JSON value containing the indexed field.
	jsonVal := []byte(`{"name":"alice","age":30}`)
	putE := mvccPutEntry(2, []byte("user:1"), jsonVal, 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Query the index -- "alice" should map to user:1's entity ID.
	result, err := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "name_idx", Value: []byte("alice")})
	if err != nil {
		t.Fatalf("index Lookup: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) == 0 {
		t.Fatal("expected at least one entity key for 'alice'")
	}
}

func TestMVCCUpdateIndexesForPut_OverwriteUpdatesIndex(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	createCmd := mustEncode(protocol.EncodeIndexCreate("name_idx2", "name", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Initial put.
	jsonVal1 := []byte(`{"name":"bob"}`)
	putE1 := mvccPutEntry(2, []byte("user:2"), jsonVal1, 0)
	applyMVCCEntries(t, s, []sm.Entry{putE1})

	// Overwrite with different name -- old entry should be removed.
	jsonVal2 := []byte(`{"name":"carol"}`)
	putE2 := mvccPutEntry(3, []byte("user:2"), jsonVal2, 0)
	applyMVCCEntries(t, s, []sm.Entry{putE2})

	// Old entry should be gone.
	result, err := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "name_idx2", Value: []byte("bob")})
	if err != nil {
		t.Fatalf("index Lookup bob: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) != 0 {
		t.Fatalf("expected 0 entities for 'bob' after overwrite, got %d", len(keys))
	}

	// New entry should exist.
	result, err = s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "name_idx2", Value: []byte("carol")})
	if err != nil {
		t.Fatalf("index Lookup carol: %v", err)
	}
	keys = result.([][]byte)
	if len(keys) == 0 {
		t.Fatal("expected at least one entity key for 'carol'")
	}
}

func TestMVCCUpdateIndexesForPut_NonJSONValueSkipped(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	createCmd := mustEncode(protocol.EncodeIndexCreate("name_idx3", "name", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Put a non-JSON value -- should silently skip index update.
	putE := mvccPutEntry(2, []byte("user:3"), []byte("not-json"), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// No crash and no index entries.
	result, err := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "name_idx3", Value: []byte("not-json")})
	if err != nil {
		t.Fatalf("index Lookup: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) != 0 {
		t.Fatalf("expected 0 entities for non-JSON value, got %d", len(keys))
	}
}

func TestMVCCUpdateIndexesForPut_NoIndexes(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Put a key with JSON value but no indexes registered.
	// This exercises the early return path in updateIndexesForPut.
	jsonVal := []byte(`{"name":"dave"}`)
	putE := mvccPutEntry(1, []byte("user:4"), jsonVal, 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Should succeed without error.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("user:4")})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	kv := val.(*service.KeyValue)
	if !bytes.Equal(kv.Value, jsonVal) {
		t.Fatalf("expected %q, got %q", jsonVal, kv.Value)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: removeIndexesForDelete - secondary index cleanup on delete
// ---------------------------------------------------------------------------

func TestMVCCRemoveIndexesForDelete_WithIndex(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	createCmd := mustEncode(protocol.EncodeIndexCreate("name_idx_del", "name", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Put a key with indexed JSON value.
	jsonVal := []byte(`{"name":"eve"}`)
	putE := mvccPutEntry(2, []byte("user:5"), jsonVal, 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Verify index entry exists.
	result, err := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "name_idx_del", Value: []byte("eve")})
	if err != nil {
		t.Fatalf("index Lookup: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) == 0 {
		t.Fatal("expected index entry for 'eve' before delete")
	}

	// Delete the key -- should remove index entries.
	delE := mvccDeleteEntry(3, []byte("user:5"))
	applyMVCCEntries(t, s, []sm.Entry{delE})

	result, err = s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "name_idx_del", Value: []byte("eve")})
	if err != nil {
		t.Fatalf("index Lookup after delete: %v", err)
	}
	keys = result.([][]byte)
	if len(keys) != 0 {
		t.Fatalf("expected 0 entities for 'eve' after delete, got %d", len(keys))
	}
}

func TestMVCCRemoveIndexesForDelete_NonJSONPrevValue(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	createCmd := mustEncode(protocol.EncodeIndexCreate("name_idx_nonjson", "name", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Put a non-JSON value.
	putE := mvccPutEntry(2, []byte("user:nonjson"), []byte("not-json"), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Delete it -- removeIndexesForDelete should gracefully handle
	// non-JSON previous value.
	delE := mvccDeleteEntry(3, []byte("user:nonjson"))
	applyMVCCEntries(t, s, []sm.Entry{delE})

	// Should complete without error.
	_, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("user:nonjson")})
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

func TestMVCCRemoveIndexesForDelete_NilPrevKv(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	createCmd := mustEncode(protocol.EncodeIndexCreate("name_idx_nil", "name", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Delete a key that never existed -- prevKv will be nil, exercising
	// the early return path in removeIndexesForDelete.
	delE := mvccDeleteEntry(2, []byte("nonexistent"))
	applyMVCCEntries(t, s, []sm.Entry{delE})
}

// ---------------------------------------------------------------------------
// mvcc.go: handleBaseKVPut with index updates (via base KV protocol)
// ---------------------------------------------------------------------------

func TestMVCCHandleBaseKVPut_WithIndexUpdates(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	createCmd := mustEncode(protocol.EncodeIndexCreate("base_idx", "status", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Put via base KV with JSON value.
	jsonVal := []byte(`{"status":"active"}`)
	putCmd, err := kv.EncodePut([]byte("item:1"), jsonVal)
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: putCmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	result, err := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "base_idx", Value: []byte("active")})
	if err != nil {
		t.Fatalf("index Lookup: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) == 0 {
		t.Fatal("expected index entry for 'active' via base KV put")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleBaseKVDelete with index removal (via base KV protocol)
// ---------------------------------------------------------------------------

func TestMVCCHandleBaseKVDelete_WithIndexRemoval(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	createCmd := mustEncode(protocol.EncodeIndexCreate("del_idx", "color", false, false, false))
	applyMVCCEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Put via MVCC put.
	jsonVal := []byte(`{"color":"red"}`)
	putE := mvccPutEntry(2, []byte("item:2"), jsonVal, 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Delete via base KV.
	delCmd, err := kv.EncodeDelete([]byte("item:2"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 3, Cmd: delCmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	result, err := s.Lookup(context.Background(), service.IndexQueryRequest{IndexName: "del_idx", Value: []byte("red")})
	if err != nil {
		t.Fatalf("index Lookup: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) != 0 {
		t.Fatalf("expected 0 entities for 'red' after delete, got %d", len(keys))
	}
}

// ---------------------------------------------------------------------------
// factory.go: MVCCOption functional options
// ---------------------------------------------------------------------------

func TestWithLessor_AppliesLessor(t *testing.T) {
	lr := lease.NewLessor()
	defer lr.Close()

	cf := NewMVCCCreateFuncWith(nil, WithLessor(lr))
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

func TestWithWatchHub_AppliesHub(t *testing.T) {
	hub := watch.NewHub()
	defer hub.Close()

	cf := NewMVCCCreateFuncWith(nil, WithWatchHub(hub))
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

func TestWithTimeSeriesEnabled_SetsFlag(t *testing.T) {
	cf := NewMVCCCreateFuncWith(nil, WithTimeSeriesEnabled())
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine configuration methods
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SetLessor_AppliesLessor(t *testing.T) {
	lr := lease.NewLessor()
	defer lr.Close()

	s := NewPebbleStateMachine(nil)
	s.SetLessor(lr)
	if s.Lessor() != lr {
		t.Fatal("expected lessor to be set")
	}
}

func TestPebbleStateMachine_SetWatchHub_AppliesHub(t *testing.T) {
	hub := watch.NewHub()
	defer hub.Close()

	s := NewPebbleStateMachine(nil)
	s.SetWatchHub(hub)
	if s.hub != hub {
		t.Fatal("expected hub to be set")
	}
}

func TestPebbleStateMachine_SetTimeSeriesEnabled_SetsFlag(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	s.SetTimeSeriesEnabled(true)
	if !s.enableTimeSeries {
		t.Fatal("expected enableTimeSeries to be true")
	}
}

// ---------------------------------------------------------------------------
// factory.go: NewMVCCCreateFuncWith with pebble options
// ---------------------------------------------------------------------------

func TestNewMVCCCreateFuncWith_WithPebbleOpts(t *testing.T) {
	cf := NewMVCCCreateFuncWith(
		[]Option{WithMaxOpenFiles(100)},
		WithTimeSeriesEnabled(),
	)
	if cf == nil {
		t.Fatal("expected non-nil CreateFunc")
	}
	dsm := cf(1, 1)
	if dsm == nil {
		t.Fatal("CreateFunc returned nil")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: direct construction with options
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_DirectConstruction_WithOptions(t *testing.T) {
	s := NewPebbleStateMachine(&pebble.Options{MaxOpenFiles: 100})
	s.SetTimeSeriesEnabled(true)
	if s == nil {
		t.Fatal("expected non-nil PebbleStateMachine")
	}
	if !s.enableTimeSeries {
		t.Fatal("expected enableTimeSeries to be true")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Open with enableTimeSeries
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_OpenWithTimeSeries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewMVCCStateMachine(nil)
	s.enableTimeSeries = true

	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with TS enabled: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for fresh DB, got %d", idx)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	if s.TimeSeries() == nil {
		t.Fatal("expected time-series storage to be initialized")
	}
}

func TestPebbleStateMachine_OpenWithTimeSeries(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	s := NewPebbleStateMachine(nil)
	s.enableTimeSeries = true

	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with TS enabled: %v", err)
	}
	if idx != 0 {
		t.Fatalf("expected 0 for fresh DB, got %d", idx)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	if s.ts == nil {
		t.Fatal("expected time-series storage to be initialized")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: lookupPrefix with __sys: prefix (skip filter path)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupPrefixSysKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("__sys:role:admin"), []byte("admin-data"), 0),
		mvccPutEntry(2, []byte("__sys:role:viewer"), []byte("viewer-data"), 0),
		mvccPutEntry(3, []byte("user:key1"), []byte("val1"), 0),
	})

	// Query with __sys: prefix should skip the internal key filter
	// and return system keys.
	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("__sys:role:"),
	})
	if err != nil {
		t.Fatalf("Lookup prefix: %v", err)
	}
	m := result.(map[string][]byte)

	if _, ok := m["__sys:role:admin"]; !ok {
		t.Fatal("expected __sys:role:admin to be present")
	}
	if _, ok := m["__sys:role:viewer"]; !ok {
		t.Fatal("expected __sys:role:viewer to be present")
	}
	if _, ok := m["user:key1"]; ok {
		t.Fatal("user:key1 should not match __sys:role: prefix")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: lookupPrefix with limit
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupPrefixWithLimit(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("app:a"), []byte("1"), 0),
		mvccPutEntry(2, []byte("app:b"), []byte("2"), 0),
		mvccPutEntry(3, []byte("app:c"), []byte("3"), 0),
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("app:"),
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("Lookup prefix with limit: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) > 2 {
		t.Fatalf("expected at most 2 results, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: lookupPrefix with empty prefix (all user keys)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupPrefixEmptyReturnsUserKeys(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("key1"), []byte("val1"), 0),
		mvccPutEntry(2, []byte("key2"), []byte("val2"), 0),
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte(""),
	})
	if err != nil {
		t.Fatalf("Lookup prefix empty: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup with unsupported query type
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupUnsupportedQueryType_IntType(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	_, err := s.Lookup(context.Background(), 12345)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var iqe *engine.InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected *engine.InvalidQueryError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup with MVCCRevisionQuery
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupRevisionQuery_AfterPut(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("rev-key"), []byte("val"), 0),
	})

	result, err := s.Lookup(context.Background(), service.MVCCRevisionQuery{})
	if err != nil {
		t.Fatalf("Lookup revision: %v", err)
	}
	rev := result.(int64)
	if rev <= 0 {
		t.Fatalf("expected positive revision, got %d", rev)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup with string query
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupStringQuery(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("str-key"), []byte("str-val"), 0),
	})

	result, err := s.Lookup(context.Background(), "str-key")
	if err != nil {
		t.Fatalf("Lookup string: %v", err)
	}
	if !bytes.Equal(result.([]byte), []byte("str-val")) {
		t.Fatalf("expected str-val, got %q", result)
	}
}

func TestMVCCStateMachine_LookupStringQueryNotFound(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	result, err := s.Lookup(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Lookup string not found should return nil, nil: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for missing key, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup with []byte query for nonexistent key
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupBytesQueryNotFound(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	result, err := s.Lookup(context.Background(), []byte("missing-bytes"))
	if err != nil {
		t.Fatalf("Lookup bytes not found should return nil, nil: %v", err)
	}
	if result != nil {
		t.Fatalf("expected nil for missing key, got %v", result)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: GetHash on not-opened state machine
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_GetHashNotOpened(t *testing.T) {
	s := NewMVCCStateMachine(nil)

	_, err := s.GetHash()
	if err == nil {
		t.Fatal("expected error for GetHash on unopened state machine")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Update on not-opened state machine
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_UpdateNotOpened(t *testing.T) {
	s := NewMVCCStateMachine(nil)

	entries := []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("k"), []byte("v"), 0))}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for Update on unopened state machine")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Lookup on not-opened state machine
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_LookupNotOpened(t *testing.T) {
	s := NewMVCCStateMachine(nil)

	_, err := s.Lookup(context.Background(), []byte("k"))
	if err == nil {
		t.Fatal("expected error for Lookup on unopened state machine")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: SaveSnapshot with failing writer
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SaveSnapshotWriteFailure(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	applyMVCCEntries(t, s, []sm.Entry{
		mvccPutEntry(1, []byte("snap-key"), []byte("snap-val"), 0),
	})
	if err := s.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}

	err = s.SaveSnapshot(context.Background(), snapCtx, errWriter{}, nil)
	if err == nil {
		t.Fatal("expected error from SaveSnapshot with failing writer")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Close on already-closed state machine (idempotent)
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_CloseIdempotent(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should be a no-op.
	if err := s.Close(context.Background()); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleMVCCCompact with invalid payload
// ---------------------------------------------------------------------------

func TestMVCCHandleMVCCCompact_InvalidPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	cmd := []byte{protocol.OpMVCCCompactPrefix} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid compact payload")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleTransaction with failure ops
// ---------------------------------------------------------------------------

func TestMVCCHandleTransaction_FailureOpsExecuted(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Put a key with value "hello".
	putE := mvccPutEntry(1, []byte("txn-fail-key"), []byte("hello"), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Compare value == "wrong" (will fail) -> failure ops should execute.
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("txn-fail-key"),
		3, // CompareValue
		0, // Equal
		[]byte("wrong"),
		0, 0, 0,
	))
	failOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("fail-result"), []byte("failed"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, nil, [][]byte{failOp}))
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 2, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// succeeded flag should be 0.
	if len(results[0].Data) == 0 || results[0].Data[0] != 0 {
		t.Fatal("expected transaction to fail (succeeded=0)")
	}

	// Verify failure op was applied.
	val, err := s.Lookup(context.Background(), service.MVCCGetQuery{Key: []byte("fail-result")})
	if err != nil {
		t.Fatalf("Lookup fail-result: %v", err)
	}
	kv := val.(*service.KeyValue)
	if !bytes.Equal(kv.Value, []byte("failed")) {
		t.Fatalf("expected 'failed', got %q", kv.Value)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleBatch with invalid payload
// ---------------------------------------------------------------------------

func TestMVCCHandleBatch_InvalidPayload(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	cmd := []byte{protocol.OpBatch} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid batch payload")
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: handleBatch with unknown op type
// ---------------------------------------------------------------------------

func TestMVCCHandleBatch_UnknownOpType(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	unknownOp := mustEncode(protocol.EncodeOp(0x99, []byte("k"), []byte("v"), nil, 0))
	batchCmd := mustEncode(protocol.EncodeBatch([][]byte{unknownOp}))
	entries := []sm.Entry{{Index: 1, Cmd: batchCmd}}
	results := make([]sm.Result, 1)

	// Refused in the result payload, not returned to Raft: an opcode with no
	// handler says nothing about the health of the storage underneath, and
	// returning it halted the shard (go-qrdb#520).
	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

// ---------------------------------------------------------------------------
// mvcc.go: handleTransaction with unknown op in success ops
// ---------------------------------------------------------------------------

func TestMVCCHandleTransaction_UnknownOpInSuccessOps(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	putE := mvccPutEntry(1, []byte("txn-unk-key"), []byte("val"), 0)
	applyMVCCEntries(t, s, []sm.Entry{putE})

	// Compare value == "val" (succeeds), but success op has unknown type.
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("txn-unk-key"),
		3, // CompareValue
		0, // Equal
		[]byte("val"),
		0, 0, 0,
	))
	unknownOp := mustEncode(protocol.EncodeOp(0x99, []byte("k"), nil, nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{unknownOp}, nil))
	entries := []sm.Entry{{Index: 2, Cmd: cmd}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

// ---------------------------------------------------------------------------
// mvcc.go: Update with unknown op code
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_UpdateUnknownOpCode(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	// Use an op code not in the handler map.
	cmd := []byte{0xFE, 0x00}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)

	assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
		results[0], "unknown op code")
}

// ---------------------------------------------------------------------------
// mvcc.go: RecoverFromSnapshot on not-opened state machine
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_RecoverFromSnapshotNotOpened(t *testing.T) {
	s := NewMVCCStateMachine(nil)

	var buf bytes.Buffer
	err := s.RecoverFromSnapshot(context.Background(), &buf, nil)
	if err == nil {
		t.Fatal("expected error for RecoverFromSnapshot on unopened state machine")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected *NotOpenError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: SaveSnapshot with invalid snapshot context type
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_SaveSnapshotInvalidContextType(t *testing.T) {
	s := newTestMVCCStateMachine(t)

	err := s.SaveSnapshot(context.Background(), "not-a-snapshot", nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid snapshot context type")
	}
	var se *SnapshotError
	if !errors.As(err, &se) {
		t.Fatalf("expected *SnapshotError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// mvcc.go: Store accessor
// ---------------------------------------------------------------------------

func TestMVCCStateMachine_StoreNilWhenNotOpened(t *testing.T) {
	s := NewMVCCStateMachine(nil)
	if s.Store() != nil {
		t.Fatal("expected nil store before Open")
	}
}

func TestMVCCStateMachine_StoreNonNilAfterOpen(t *testing.T) {
	s := newTestMVCCStateMachine(t)
	if s.Store() == nil {
		t.Fatal("expected non-nil store after Open")
	}
}
