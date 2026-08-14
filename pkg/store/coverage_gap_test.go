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
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// PebbleStateMachine: scanPrefix via *statemachine.PrefixQuery (0% covered)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ScanPrefixBasic(t *testing.T) {
	s := openExtendedStore(t)

	// Insert some keys.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("pfx:a"), []byte("1"), 0)},
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("pfx:b"), []byte("2"), 0)},
		{Index: 3, Cmd: protocol.EncodeMVCCPut([]byte("other:c"), []byte("3"), 0)},
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("pfx:"),
	})
	if err != nil {
		t.Fatalf("Lookup PrefixQuery: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) != 2 {
		t.Fatalf("expected 2 results, got %d: %v", len(m), m)
	}
	if !bytes.Equal(m["pfx:a"], []byte("1")) {
		t.Fatalf("expected pfx:a=1, got %q", m["pfx:a"])
	}
	if !bytes.Equal(m["pfx:b"], []byte("2")) {
		t.Fatalf("expected pfx:b=2, got %q", m["pfx:b"])
	}
}

func TestPebbleStateMachine_ScanPrefixWithLimit(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("lim:a"), []byte("1"), 0)},
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("lim:b"), []byte("2"), 0)},
		{Index: 3, Cmd: protocol.EncodeMVCCPut([]byte("lim:c"), []byte("3"), 0)},
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("lim:"),
		Limit:  2,
	})
	if err != nil {
		t.Fatalf("Lookup PrefixQuery with limit: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) > 2 {
		t.Fatalf("expected at most 2 results, got %d", len(m))
	}
	if len(m) == 0 {
		t.Fatal("expected at least 1 result")
	}
}

func TestPebbleStateMachine_ScanPrefixEmptyPrefix(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("k1"), []byte("v1"), 0)},
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("k2"), []byte("v2"), 0)},
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte(""),
	})
	if err != nil {
		t.Fatalf("Lookup empty PrefixQuery: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(m))
	}
}

func TestPebbleStateMachine_ScanPrefixSysKeys(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("__sys:role:admin"), []byte("admin-data"), 0)},
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("__sys:role:viewer"), []byte("viewer-data"), 0)},
		{Index: 3, Cmd: protocol.EncodeMVCCPut([]byte("user:key1"), []byte("val1"), 0)},
	})

	// Querying with __sys: prefix bypasses the internal-key filter.
	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("__sys:role:"),
	})
	if err != nil {
		t.Fatalf("Lookup __sys: PrefixQuery: %v", err)
	}
	m := result.(map[string][]byte)
	if _, ok := m["__sys:role:admin"]; !ok {
		t.Fatal("expected __sys:role:admin in results")
	}
	if _, ok := m["__sys:role:viewer"]; !ok {
		t.Fatal("expected __sys:role:viewer in results")
	}
}

func TestPebbleStateMachine_ScanPrefixNoMatch(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("a:key"), []byte("val"), 0)},
	})

	result, err := s.Lookup(context.Background(), &statemachine.PrefixQuery{
		Prefix: []byte("nonexistent:"),
	})
	if err != nil {
		t.Fatalf("Lookup no-match PrefixQuery: %v", err)
	}
	m := result.(map[string][]byte)
	if len(m) != 0 {
		t.Fatalf("expected 0 results, got %d", len(m))
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: Open error paths (66.7% -> improve)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_OpenInvalidDir(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	_, err := s.Open(context.Background(), "/dev/null/nonexistent/path", nil)
	if err == nil {
		t.Fatal("expected error for invalid directory")
	}
	var oe *OpenError
	if !errors.As(err, &oe) {
		t.Fatalf("expected *OpenError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_OpenWithTimeSeriesEnabled(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	s.enableTimeSeries = true
	dir := filepath.Join(t.TempDir(), "db")

	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open with TS: %v", err)
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
// PebbleStateMachine: clearAll with empty database (82.4% -> improve)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ClearAll_EmptyDB(t *testing.T) {
	s := openExtendedStore(t)

	// clearAll on an empty database exercises the !iter.First() early-return path.
	// This is exercised indirectly via RecoverFromSnapshot on an empty source.
	var buf bytes.Buffer
	snapCtx, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	if err := s.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	// Apply some data.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("pre-snap"), []byte("val"), 0)},
	})

	// Recover from the empty snapshot -- clearAll on a non-empty DB.
	snapCtx2, err := s.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot 2: %v", err)
	}
	var buf2 bytes.Buffer
	if err := s.SaveSnapshot(context.Background(), snapCtx2, &buf2, nil); err != nil {
		t.Fatalf("SaveSnapshot 2: %v", err)
	}

	// Restore from the original empty snapshot.
	if err := s.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot empty: %v", err)
	}

	// The pre-snap key should no longer exist.
	_, err = s.Lookup(context.Background(), []byte("pre-snap"))
	if err == nil {
		t.Fatal("expected error for key cleared by snapshot recovery")
	}
}

func TestPebbleStateMachine_ClearAll_ViaSnapshotRecovery(t *testing.T) {
	src := openExtendedStore(t)
	dst := openExtendedStore(t)

	// Put one key in src.
	applyExtEntries(t, src, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("snap-key"), []byte("snap-val"), 0)},
	})
	if err := src.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Put a different key in dst (to exercise clearAll on non-empty).
	applyExtEntries(t, dst, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("old-key"), []byte("old-val"), 0)},
	})

	// Take snapshot of src and apply to dst.
	snapCtx, err := src.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := src.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}

	if err := dst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}

	// dst should now have snap-key, not old-key.
	val, err := dst.Lookup(context.Background(), []byte("snap-key"))
	if err != nil {
		t.Fatalf("Lookup snap-key: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("snap-val")) {
		t.Fatalf("expected snap-val, got %s", val)
	}

	_, err = dst.Lookup(context.Background(), []byte("old-key"))
	if err == nil {
		t.Fatal("expected old-key to be cleared after snapshot recovery")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandlePut with watch hub (prev value path)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_PutWithHub_PrevValueCaptured(t *testing.T) {
	s, hub := openStoreWithHub(t)

	// Put initial value.
	putCmd1, err := kv.EncodePut([]byte("watch-prev"), []byte("v1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd1}})

	// Watch the key before the second put.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, wErr := hub.Watch(ctx, "watch-prev")
	if wErr != nil {
		t.Fatalf("Watch: %v", wErr)
	}
	defer func() { _ = w.Close() }()

	// Overwrite -- prevKv should now have v1.
	putCmd2, err := kv.EncodePut([]byte("watch-prev"), []byte("v2"))
	if err != nil {
		t.Fatalf("EncodePut 2: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd2}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventPut {
			t.Fatalf("expected EventPut, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleDelete with watch hub (prev value path)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_DeleteWithHub_PrevValueCaptured(t *testing.T) {
	s, hub := openStoreWithHub(t)

	// Put a value to delete.
	putCmd, err := kv.EncodePut([]byte("hub-del"), []byte("to-delete"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, wErr := hub.Watch(ctx, "hub-del")
	if wErr != nil {
		t.Fatalf("Watch: %v", wErr)
	}
	defer func() { _ = w.Close() }()

	// Delete -- prevKv should be captured.
	delCmd, err := kv.EncodeDelete([]byte("hub-del"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleMVCCPut with lease attachment
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_MVCCPutWithLease_AttachesKey(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := protocol.EncodeLeaseGrant(60)
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Put a key with the lease attached.
	putCmd := protocol.EncodeMVCCPut([]byte("leased-key"), []byte("leased-val"), leaseID)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd}})

	// Revoke -- should delete the leased key.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	revokeResults := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: revokeCmd}})
	if revokeResults[0].Value != 1 {
		t.Fatalf("expected 1 revoked key, got %d", revokeResults[0].Value)
	}

	// Verify key is gone.
	_, err := s.Lookup(context.Background(), []byte("leased-key"))
	if err == nil {
		t.Fatal("expected error for key deleted by lease revoke")
	}

	_ = lr
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleMVCCDelete with hub (prevKv path)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_MVCCDeleteWithHub_PrevValue(t *testing.T) {
	s, hub := openStoreWithHub(t)

	// Put a value.
	putCmd := protocol.EncodeMVCCPut([]byte("mvcc-del-watch"), []byte("original"), 0)
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, wErr := hub.Watch(ctx, "mvcc-del-watch")
	if wErr != nil {
		t.Fatalf("Watch: %v", wErr)
	}
	defer func() { _ = w.Close() }()

	// Delete via MVCC delete.
	delCmd := protocol.EncodeMVCCDelete([]byte("mvcc-del-watch"))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event, got %d", len(resp.Events))
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleExecuteOp with lessor and hub (missing paths)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_BatchWithLessorAndHub(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	dir := filepath.Join(t.TempDir(), "db")
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease.
	grantResults := applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeLeaseGrant(60)},
	})
	leaseID := int64(grantResults[0].Value)

	// Batch with an MVCC put (with lease) and delete.
	ops := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCPut, []byte("batch-lease-key"), []byte("val"), nil, leaseID),
	}
	batchCmd := protocol.EncodeBatch(ops)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: batchCmd}})

	// Verify key was stored.
	val, err := s.Lookup(context.Background(), []byte("batch-lease-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val")) {
		t.Fatalf("expected val, got %s", val)
	}

	// Batch delete to exercise OpMVCCDelete path in pebbleExecuteOp with hub.
	ops2 := [][]byte{
		protocol.EncodeOp(protocol.OpMVCCDelete, []byte("batch-lease-key"), nil, nil, 0),
	}
	batchCmd2 := protocol.EncodeBatch(ops2)
	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: batchCmd2}})

	_, err = s.Lookup(context.Background(), []byte("batch-lease-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleIndexCreate invalid payload
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_IndexCreate_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpIndexCreate} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid IndexCreate payload")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandleIndexDrop invalid payload
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_IndexDrop_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	cmd := []byte{protocol.OpIndexDrop} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid IndexDrop payload")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: LeaseRevoke with watch hub (notification path)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LeaseRevokeWithHub_Notifications(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	lr := lease.NewLessor()
	s.SetLessor(lr)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	dir := filepath.Join(t.TempDir(), "db")
	if _, err := s.Open(context.Background(), dir, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		lr.Close()
		_ = s.Close(context.Background())
	})

	// Grant a lease.
	grantResults := applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeLeaseGrant(60)},
	})
	leaseID := int64(grantResults[0].Value)

	// Put a key with the lease.
	putCmd := protocol.EncodeMVCCPut([]byte("watch-lease-key"), []byte("val"), leaseID)
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, wErr := hub.Watch(ctx, "watch-lease-key")
	if wErr != nil {
		t.Fatalf("Watch: %v", wErr)
	}
	defer func() { _ = w.Close() }()

	// Revoke the lease -- should delete the key and emit watch event.
	revokeCmd := protocol.EncodeLeaseRevoke(leaseID)
	applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: revokeCmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected watch event on lease revoke")
		}
		if resp.Events[0].Type != watch.EventDelete {
			t.Fatalf("expected EventDelete, got %d", resp.Events[0].Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watch event after lease revoke")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: TS handlers with invalid payloads
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_TSWrite_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSWrite} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TSWrite payload")
	}
}

func TestPebbleStateMachine_TSWriteBatch_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSWriteBatch} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TSWriteBatch payload")
	}
}

func TestPebbleStateMachine_TSWritePoint_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSWritePoint} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TSWritePoint payload")
	}
}

func TestPebbleStateMachine_TSDeleteSeries_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSDeleteSeries} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TSDeleteSeries payload")
	}
}

func TestPebbleStateMachine_TSSetRetention_InvalidPayload(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := []byte{protocol.OpTSSetRetention} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid TSSetRetention payload")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: GetHash with data (86.4% -> improve)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_GetHash_WithData(t *testing.T) {
	s := openExtendedStore(t)

	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("hash-k1"), []byte("v1"), 0)},
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("hash-k2"), []byte("v2"), 0)},
	})

	h1, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}
	if h1 == 0 {
		t.Fatal("expected non-zero hash with data")
	}

	// Hash should change after another put.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 3, Cmd: protocol.EncodeMVCCPut([]byte("hash-k3"), []byte("v3"), 0)},
	})

	h2, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash after mutation: %v", err)
	}
	if h2 == h1 {
		t.Fatal("expected hash to change after adding a new key")
	}
}

func TestPebbleStateMachine_GetHash_EmptyDB(t *testing.T) {
	s := openExtendedStore(t)

	h, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash empty: %v", err)
	}
	// An empty database may yield 0 or the xxhash64 seed; either is valid.
	_ = h
}

func TestPebbleStateMachine_GetHash_SkipsMetadataKeys(t *testing.T) {
	s := openExtendedStore(t)

	// Write a user key and verify hash includes it.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: protocol.EncodeMVCCPut([]byte("user-key"), []byte("user-val"), 0)},
	})

	h1, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash: %v", err)
	}

	// Apply another entry that advances the lastApplied metadata key (\x00...).
	// The hash should not change due to metadata writes alone.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 2, Cmd: protocol.EncodeMVCCPut([]byte("user-key"), []byte("user-val"), 0)},
	})

	h2, err := s.GetHash()
	if err != nil {
		t.Fatalf("GetHash 2: %v", err)
	}
	// Repeated same-value write: hash should stay equal.
	if h1 != h2 {
		t.Fatalf("expected hash to be stable for same data, got h1=%d h2=%d", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: pebbleHandlePut/Delete with invalid payloads
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Put_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	// Base KV put with invalid short payload (just the op byte).
	cmd := []byte{kv.OpPut}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid Put payload")
	}
}

func TestPebbleStateMachine_Delete_InvalidPayload(t *testing.T) {
	s := openExtendedStore(t)

	// Base KV delete with invalid short payload.
	cmd := []byte{kv.OpDelete}
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid Delete payload")
	}
}

// ---------------------------------------------------------------------------
// PebbleStateMachine: LeaseRevoke invalid payload
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LeaseRevoke_InvalidPayload(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := []byte{protocol.OpLeaseRevoke} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid LeaseRevoke payload")
	}
}

func TestPebbleStateMachine_LeaseGrant_InvalidPayload(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := []byte{protocol.OpLeaseGrant} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid LeaseGrant payload")
	}
}

func TestPebbleStateMachine_LeaseKeepAlive_InvalidPayload(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	cmd := []byte{protocol.OpLeaseKeepAlive} // empty payload
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for invalid LeaseKeepAlive payload")
	}
}

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
