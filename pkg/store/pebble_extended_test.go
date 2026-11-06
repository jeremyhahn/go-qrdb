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

package store

import (
	"bytes"
	"context"
	"errors"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Extended PebbleStateMachine test helpers
// ---------------------------------------------------------------------------

// openExtendedStore creates a PebbleStateMachine with optional components, opens it,
// and registers cleanup.
func openExtendedStore(t *testing.T) *PebbleStateMachine {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	dir := filepath.Join(t.TempDir(), "db")
	idx, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: unexpected error: %v", err)
	}
	if idx != 0 {
		t.Fatalf("Open: expected last applied 0 on fresh DB, got %d", idx)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Errorf("Close: unexpected error: %v", err)
		}
	})
	return s
}

// readBatch returns an indexed batch usable as the reader argument of the
// apply-path helpers, which take the batch they are writing into so they read
// their own writes. A test that only wants to read committed state passes an
// empty one.
func readBatch(t *testing.T, s *PebbleStateMachine) *pebble.Batch {
	t.Helper()
	b := s.db.NewIndexedBatch()
	t.Cleanup(func() { _ = b.Close() })
	return b
}

// openStoreWithLessor creates a PebbleStateMachine with a Lessor attached.
func openStoreWithLessor(t *testing.T, opts ...lease.Option) (*PebbleStateMachine, *lease.Lessor) {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	lr := lease.NewLessor(opts...)
	s.SetLessor(lr)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		lr.Close()
		_ = s.Close(context.Background())
	})
	return s, lr
}

func TestPebbleStateMachine_Lookup_LeaseKeepAlive(t *testing.T) {
	s, lr := openStoreWithLessor(t)
	results := make([]sm.Result, 1)
	grant := []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(30))}}
	if err := s.Update(context.Background(), grant, results); err != nil {
		t.Fatalf("Grant: %v", err)
	}
	leaseID := int64(results[0].Value)

	var np *lease.NotPrimaryError
	if _, err := s.Lookup(context.Background(), lease.LeaseKeepAliveQuery{ID: leaseID}); !errors.As(err, &np) {
		t.Fatalf("Lookup before RunExpiry error = %v, want *lease.NotPrimaryError", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Hour, func() bool { return true },
			func(context.Context, int64) error { return nil }, nil)
	}()
	defer func() { cancel(); <-done }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := s.Lookup(context.Background(), lease.LeaseKeepAliveQuery{ID: leaseID})
		if err == nil {
			if l := got.(*lease.Lease); l.ID != leaseID {
				t.Fatalf("lease ID = %d, want %d", l.ID, leaseID)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Lookup never renewed: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	var lnf *service.LeaseNotFoundError
	if _, err := s.Lookup(context.Background(), lease.LeaseKeepAliveQuery{ID: leaseID + 99}); !errors.As(err, &lnf) {
		t.Fatalf("unknown lease error = %v, want *service.LeaseNotFoundError", err)
	}
}

func TestPebbleStateMachine_Lookup_LeaseKeepAliveWithoutLessor(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if _, err := s.Open(context.Background(), filepath.Join(t.TempDir(), "db"), nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })

	var iq *InvalidQueryError
	if _, err := s.Lookup(context.Background(), lease.LeaseKeepAliveQuery{ID: 1}); !errors.As(err, &iq) {
		t.Fatalf("error = %v, want *InvalidQueryError", err)
	}
}

// recordingCheckpointer is a lease.Checkpointer that records every batch the
// checkpoint loop proposes and reports it applied.
type recordingCheckpointer struct {
	mu  sync.Mutex
	cps []lease.Checkpoint
}

var _ lease.Checkpointer = (*recordingCheckpointer)(nil)

func (r *recordingCheckpointer) CheckpointEnabled() bool { return true }

func (r *recordingCheckpointer) Checkpoint(_ context.Context, _, _ uint64, cps []lease.Checkpoint) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cps = append(r.cps, cps...)
	return nil
}

func (r *recordingCheckpointer) AwaitApplied(context.Context) error { return nil }

func (r *recordingCheckpointer) recorded() []lease.Checkpoint {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.cps)
}

func TestPebbleStateMachine_Lookup_LeaseCheckpointNow(t *testing.T) {
	fc := clocktest.New()
	s, lr := openStoreWithLessor(t, lease.WithClock(fc))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(30))}})
	leaseID := int64(results[0].Value)

	var np *lease.NotPrimaryError
	if _, err := s.Lookup(context.Background(), lease.LeaseCheckpointNowQuery{}); !errors.As(err, &np) {
		t.Fatalf("Lookup before RunExpiry error = %v, want *lease.NotPrimaryError", err)
	}

	cp := &recordingCheckpointer{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lr.RunExpiry(ctx, time.Hour, func() bool { return true },
			func(context.Context, int64) error { return nil }, nil,
			lease.WithCheckpoint(time.Hour, cp))
	}()
	defer func() { cancel(); <-done }()

	// The loop marks itself checkpointing before it arms the expiry ticker,
	// its second, so two armed tickers mean the request is accepted.
	armCtx, armCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer armCancel()
	if err := fc.BlockUntil(armCtx, 2); err != nil {
		t.Fatalf("the expiry loop never armed its tickers: %v", err)
	}
	got, err := s.Lookup(context.Background(), lease.LeaseCheckpointNowQuery{})
	if err != nil {
		t.Fatalf("Lookup on the primary: %v", err)
	}
	answer, ok := got.(<-chan error)
	if !ok {
		t.Fatalf("Lookup answered %T, want <-chan error", got)
	}
	select {
	case err := <-answer:
		if err != nil {
			t.Fatalf("checkpoint-now answered %v", err)
		}
	case <-armCtx.Done():
		t.Fatal("checkpoint-now was never answered")
	}
	if rec := cp.recorded(); len(rec) != 1 || rec[0].ID != leaseID || rec[0].RemainingTTL != 30 {
		t.Fatalf("checkpointed %+v, want lease %d at 30s", rec, leaseID)
	}
}

func TestPebbleStateMachine_Lookup_LeaseCheckpointNowWithoutLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor

	var iq *InvalidQueryError
	if _, err := s.Lookup(context.Background(), lease.LeaseCheckpointNowQuery{}); !errors.As(err, &iq) {
		t.Fatalf("error = %v, want *InvalidQueryError", err)
	}
}

func TestPebbleStateMachine_LeaseCheckpoint_NoLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor

	cmd := mustEncode(protocol.EncodeLeaseCheckpoint(0, 0, []lease.Checkpoint{{ID: 1, RemainingTTL: 5}}))
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: cmd}}, results); err != nil {
		t.Fatalf("Update must not return a fatal error when lessor not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when lessor not configured")
	}
}

// openStoreWithHub creates a PebbleStateMachine with a watch Hub attached.
func openStoreWithHub(t *testing.T) (*PebbleStateMachine, *watch.Hub) {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	hub := watch.NewHub()
	s.SetWatchHub(hub)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		hub.Close()
		_ = s.Close(context.Background())
	})
	return s, hub
}

// openStoreWithTS creates a PebbleStateMachine with a TimeSeries storage attached.
func openStoreWithTS(t *testing.T) *PebbleStateMachine {
	t.Helper()
	s := NewPebbleStateMachine(nil)
	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Time series must ride the SAME PebbleDB as the state machine: tsAutoCommit
	// stages every time-series write into a batch built over s.db, so a
	// *timeseries.Storage built over a different DB would write where nothing
	// ever reads from.
	ts, err := timeseries.New(pebbleStorage.NewFromDB(s.db, true))
	if err != nil {
		t.Fatalf("create ts storage: %v", err)
	}
	s.SetTimeSeries(ts)

	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// applyExtEntries applies entries to the extended store and returns results.
func applyExtEntries(t *testing.T, s *PebbleStateMachine, entries []sm.Entry) []sm.Result {
	t.Helper()
	results := make([]sm.Result, len(entries))
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update: unexpected error: %v", err)
	}
	return results
}

func lookupExtKey(t *testing.T, s *PebbleStateMachine, key string) []byte {
	t.Helper()
	got, err := s.Lookup(context.Background(), []byte(key))
	if err != nil {
		var keyNotFound *kv.KeyNotFoundError
		if errors.As(err, &keyNotFound) {
			return nil
		}
		t.Fatalf("Lookup %s: %v", key, err)
	}
	value, ok := got.([]byte)
	if !ok {
		t.Fatalf("Lookup %s returned %T", key, got)
	}
	return value
}

// ---------------------------------------------------------------------------
// Tests: Backward compatibility (base KV ops still work)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_ExtendedBackwardCompat_PutGetDelete(t *testing.T) {
	s := openExtendedStore(t)

	// Put via base KV protocol.
	putCmd, err := kv.EncodePut([]byte("key1"), []byte("val1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: putCmd},
	}
	results := applyExtEntries(t, s, entries)
	if results[0].Value != uint64(len("val1")) {
		t.Fatalf("expected result %d, got %d", len("val1"), results[0].Value)
	}

	// Lookup via []byte query.
	val, err := s.Lookup(context.Background(), []byte("key1"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("val1")) {
		t.Fatalf("expected val1, got %s", val)
	}

	// Delete via base KV protocol.
	delCmd, err := kv.EncodeDelete([]byte("key1"))
	if err != nil {
		t.Fatalf("EncodeDelete: %v", err)
	}
	delEntries := []sm.Entry{
		{Index: 2, Cmd: delCmd},
	}
	delResults := applyExtEntries(t, s, delEntries)
	if delResults[0].Value != 1 {
		t.Fatalf("expected delete result 1, got %d", delResults[0].Value)
	}

	// Verify key is gone.
	_, err = s.Lookup(context.Background(), []byte("key1"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
	var knf *kv.KeyNotFoundError
	if !errors.As(err, &knf) {
		t.Fatalf("expected KeyNotFoundError, got %T: %v", err, err)
	}
}

func TestPebbleStateMachine_ExtendedBackwardCompat_ListQuery(t *testing.T) {
	s := openExtendedStore(t)

	cmd1, err := kv.EncodePut([]byte("prefix:a"), []byte("1"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	cmd2, err := kv.EncodePut([]byte("prefix:b"), []byte("2"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	cmd3, err := kv.EncodePut([]byte("other:c"), []byte("3"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{
		{Index: 1, Cmd: cmd1},
		{Index: 2, Cmd: cmd2},
		{Index: 3, Cmd: cmd3},
	}
	applyExtEntries(t, s, entries)

	result, err := s.Lookup(context.Background(), kv.ListQuery{Prefix: "prefix:"})
	if err != nil {
		t.Fatalf("Lookup ListQuery: %v", err)
	}
	keys := result.([]string)
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[0] != "prefix:a" || keys[1] != "prefix:b" {
		t.Fatalf("unexpected keys: %v", keys)
	}
}

// ---------------------------------------------------------------------------
// Tests: MVCC-compatible put/delete (no revision tracking)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_MVCCPut(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeMVCCPut([]byte("mvcc-key"), []byte("mvcc-val"), 0))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := applyExtEntries(t, s, entries)

	if results[0].Value != uint64(len("mvcc-val")) {
		t.Fatalf("expected %d, got %d", len("mvcc-val"), results[0].Value)
	}

	// Verify the value is stored.
	val, err := s.Lookup(context.Background(), []byte("mvcc-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("mvcc-val")) {
		t.Fatalf("expected mvcc-val, got %s", val)
	}
}

// TestPebbleStateMachine_InvalidPayload checks that every handler fails the
// apply on a payload it cannot decode.
func TestPebbleStateMachine_InvalidPayload(t *testing.T) {
	withLessor := func(t *testing.T) *PebbleStateMachine {
		s, _ := openStoreWithLessor(t)
		return s
	}
	for _, tc := range []struct {
		name string
		open func(*testing.T) *PebbleStateMachine
		cmd  []byte
	}{
		{"mvcc put", openExtendedStore, []byte{protocol.OpMVCCPut}},
		{"mvcc delete", openExtendedStore, []byte{protocol.OpMVCCDelete}},
		{"batch", openExtendedStore, []byte{protocol.OpBatch}},
		{"transaction", openExtendedStore, []byte{protocol.OpTransaction}},
		{"index create", openExtendedStore, []byte{protocol.OpIndexCreate}},
		{"index drop", openExtendedStore, []byte{protocol.OpIndexDrop}},
		{"lease grant", withLessor, []byte{protocol.OpLeaseGrant}},
		{"lease keep-alive", withLessor, []byte{protocol.OpLeaseKeepAlive}},
		{"lease revoke", withLessor, []byte{protocol.OpLeaseRevoke}},
		{"lease checkpoint truncated", withLessor, []byte{protocol.OpLeaseCheckpoint, 0x01}},
		{"ts write", openStoreWithTS, []byte{protocol.OpTSWrite}},
		{"ts write batch", openStoreWithTS, []byte{protocol.OpTSWriteBatch}},
		{"ts write point", openStoreWithTS, []byte{protocol.OpTSWritePoint}},
		{"ts delete series", openStoreWithTS, []byte{protocol.OpTSDeleteSeries}},
		{"ts set retention", openStoreWithTS, []byte{protocol.OpTSSetRetention}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.open(t)
			results := make([]sm.Result, 1)
			if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: tc.cmd}}, results); err == nil {
				t.Fatal("expected an error for an invalid payload")
			}
		})
	}
}

func TestPebbleStateMachine_MVCCDelete(t *testing.T) {
	s := openExtendedStore(t)

	// First put a value.
	putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("del-key"), []byte("del-val"), 0))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Then delete it.
	delCmd := mustEncode(protocol.EncodeMVCCDelete([]byte("del-key")))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}

	// Verify key is gone.
	_, err := s.Lookup(context.Background(), []byte("del-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}
}

func TestPebbleStateMachine_DeleteRange_RemovesRangeAndHashesDeterministically(t *testing.T) {
	left := openExtendedStore(t)
	right := openExtendedStore(t)
	entries := []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("app/a"), []byte("1"), 0))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("app/b"), []byte("2"), 0))},
		{Index: 3, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("app/c"), []byte("3"), 0))},
		{Index: 4, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("other/a"), []byte("4"), 0))},
		{Index: 5, Cmd: mustEncode(protocol.EncodeDeleteRange([]byte("app/"), storage.PrefixEnd([]byte("app/"))))},
	}
	applyExtEntries(t, left, entries)
	applyExtEntries(t, right, entries)

	if got := lookupExtKey(t, left, "app/a"); got != nil {
		t.Fatalf("app/a survived range delete: %q", got)
	}
	if got := lookupExtKey(t, left, "other/a"); string(got) != "4" {
		t.Fatalf("other/a = %q, want 4", got)
	}
	lh, err := left.GetHash()
	if err != nil {
		t.Fatalf("left hash: %v", err)
	}
	rh, err := right.GetHash()
	if err != nil {
		t.Fatalf("right hash: %v", err)
	}
	if lh != rh {
		t.Fatalf("hash mismatch after range delete")
	}

	snapCtx, err := left.PrepareSnapshot()
	if err != nil {
		t.Fatalf("PrepareSnapshot: %v", err)
	}
	var buf bytes.Buffer
	if err := left.SaveSnapshot(context.Background(), snapCtx, &buf, nil); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	snapshotDst := openExtendedStore(t)
	applyExtEntries(t, snapshotDst, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeMVCCPut([]byte("app/stale"), []byte("stale"), 0))},
	})
	if err := snapshotDst.RecoverFromSnapshot(context.Background(), &buf, nil); err != nil {
		t.Fatalf("RecoverFromSnapshot: %v", err)
	}
	if got := lookupExtKey(t, snapshotDst, "app/stale"); got != nil {
		t.Fatalf("app/stale survived snapshot install: %q", got)
	}
	if got := lookupExtKey(t, snapshotDst, "other/a"); string(got) != "4" {
		t.Fatalf("snapshot other/a = %q, want 4", got)
	}

	restartDir := filepath.Join(t.TempDir(), "db")
	restart := NewPebbleStateMachine(nil)
	if idx, err := restart.Open(context.Background(), restartDir, nil); err != nil || idx != 0 {
		t.Fatalf("restart Open fresh: index=%d err=%v", idx, err)
	}
	applyExtEntries(t, restart, entries)
	if err := restart.Close(context.Background()); err != nil {
		t.Fatalf("Close before restart: %v", err)
	}
	restarted := NewPebbleStateMachine(nil)
	idx, err := restarted.Open(context.Background(), restartDir, nil)
	if err != nil {
		t.Fatalf("Open after restart: %v", err)
	}
	if idx != uint64(len(entries)) {
		t.Fatalf("Open after restart index=%d, want %d", idx, len(entries))
	}
	t.Cleanup(func() {
		if err := restarted.Close(context.Background()); err != nil {
			t.Errorf("Close restarted: %v", err)
		}
	})
	if got := lookupExtKey(t, restarted, "app/b"); got != nil {
		t.Fatalf("app/b survived restart: %q", got)
	}
	if got := lookupExtKey(t, restarted, "other/a"); string(got) != "4" {
		t.Fatalf("restarted other/a = %q, want 4", got)
	}
}

// ---------------------------------------------------------------------------
// Tests: Batch operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Batch(t *testing.T) {
	s := openExtendedStore(t)

	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("b1"), []byte("v1"), nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("b2"), []byte("v2"), nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 2 {
		t.Fatalf("expected batch count 2, got %d", results[0].Value)
	}

	// Verify both keys exist.
	for _, key := range []string{"b1", "b2"} {
		_, err := s.Lookup(context.Background(), []byte(key))
		if err != nil {
			t.Fatalf("Lookup %s: %v", key, err)
		}
	}
}

func TestPebbleStateMachine_Batch_WithDelete(t *testing.T) {
	s := openExtendedStore(t)

	// First put a value.
	putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("bd-key"), []byte("bd-val"), 0))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Batch delete + put.
	ops := [][]byte{
		mustEncode(protocol.EncodeOp(protocol.OpMVCCDelete, []byte("bd-key"), nil, nil, 0)),
		mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("bd-new"), []byte("new-val"), nil, 0)),
	}
	cmd := mustEncode(protocol.EncodeBatch(ops))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	// Old key should be gone.
	_, err := s.Lookup(context.Background(), []byte("bd-key"))
	if err == nil {
		t.Fatal("expected error for deleted key")
	}

	// New key should exist.
	val, err := s.Lookup(context.Background(), []byte("bd-new"))
	if err != nil {
		t.Fatalf("Lookup bd-new: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("new-val")) {
		t.Fatalf("expected new-val, got %s", val)
	}
}

// ---------------------------------------------------------------------------
// Tests: Transaction (compare-and-swap)
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Transaction_SuccessPath(t *testing.T) {
	s := openExtendedStore(t)

	// Put initial value.
	putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("txn-key"), []byte("initial"), 0))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Transaction: compare value == "initial", then set to "updated".
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("txn-key"),
		3, // CompareValue
		0, // CompareEqual
		[]byte("initial"),
		0, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("updated"), nil, 0))
	failureOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("failed"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, [][]byte{failureOp}))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	// Check succeeded flag.
	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed")
	}

	// Verify value was updated.
	val, err := s.Lookup(context.Background(), []byte("txn-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("updated")) {
		t.Fatalf("expected updated, got %s", val)
	}
}

func TestPebbleStateMachine_Transaction_FailurePath(t *testing.T) {
	s := openExtendedStore(t)

	// Put initial value.
	putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("txn-key"), []byte("initial"), 0))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Transaction: compare value == "wrong", then set to "updated" (should fail).
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("txn-key"),
		3, // CompareValue
		0, // CompareEqual
		[]byte("wrong"),
		0, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("updated"), nil, 0))
	failureOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("txn-key"), []byte("failed"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, [][]byte{failureOp}))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	// Check succeeded flag is 0.
	if len(results[0].Data) > 0 && results[0].Data[0] != 0 {
		t.Fatal("expected transaction to fail")
	}

	// Verify failure ops ran.
	val, err := s.Lookup(context.Background(), []byte("txn-key"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("failed")) {
		t.Fatalf("expected failed, got %s", val)
	}
}

func TestPebbleStateMachine_Transaction_VersionCompare(t *testing.T) {
	s := openExtendedStore(t)

	// Put a key so it exists (version = 1 in non-MVCC mode).
	putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("ver-key"), []byte("val"), 0))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	// Compare version == 1 (key exists -> version = 1).
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("ver-key"),
		0, // CompareVersion
		0, // CompareEqual
		nil,
		1, 0, 0, // version=1
	))
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("ver-key"), []byte("success"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected version compare to succeed")
	}
}

func TestPebbleStateMachine_Transaction_NonexistentKey(t *testing.T) {
	s := openExtendedStore(t)

	// Compare value of nonexistent key == empty (should succeed).
	compare := mustEncode(protocol.EncodeCompare(
		[]byte("missing"),
		3, // CompareValue
		0, // CompareEqual
		[]byte{},
		0, 0, 0,
	))
	successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("missing"), []byte("created"), nil, 0))
	cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if len(results[0].Data) == 0 || results[0].Data[0] != 1 {
		t.Fatal("expected transaction to succeed for missing key with empty value compare")
	}

	val, err := s.Lookup(context.Background(), []byte("missing"))
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !bytes.Equal(val.([]byte), []byte("created")) {
		t.Fatalf("expected created, got %s", val)
	}
}

// TestPebbleStateMachine_Transaction_RevisionCompareRejected checks that a
// transaction comparing on a revision target is refused with an
// application error on a non-MVCC state machine, and that its success ops
// do not run: admitting the compare was a lost update.
func TestPebbleStateMachine_Transaction_RevisionCompareRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target byte
	}{
		{"create revision", protocol.CompareTargetCreateRevision},
		{"mod revision", protocol.CompareTargetModRevision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openExtendedStore(t)
			putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("rev-key"), []byte("val"), 0))
			applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

			// Compare against revision 0, the exact shape that used to pass.
			compare := mustEncode(protocol.EncodeCompare([]byte("rev-key"), tc.target, 0 /* Equal */, nil, 0, 0, 0))
			successOp := mustEncode(protocol.EncodeOp(protocol.OpMVCCPut, []byte("rev-result"), []byte("ok"), nil, 0))
			cmd := mustEncode(protocol.EncodeTransaction([][]byte{compare}, [][]byte{successOp}, nil))
			results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: cmd}})

			msg, ok := protocol.DecodeAppError(results[0].Data)
			if !ok {
				t.Fatalf("expected an application error for a revision compare on a non-MVCC state machine, got Data=%v", results[0].Data)
			}
			if !strings.Contains(msg, "sm_type") {
				t.Errorf("app error should name the required configuration, got %q", msg)
			}
			if _, err := s.getKeyInBatch(readBatch(t, s), []byte("rev-result")); err == nil {
				t.Fatal("success op executed despite the rejected compare")
			}
		})
	}
}

// TestPebbleEvaluateCompare checks a compare against the key's state read
// through the apply batch. A non-MVCC state machine has no revisions, so a
// revision compare fails closed even on a key that exists.
func TestPebbleEvaluateCompare(t *testing.T) {
	s := openExtendedStore(t)
	putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("cmp-key"), []byte("bbb"), 0))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: putCmd}})

	present, absent := []byte("cmp-key"), []byte("cmp-missing")
	for _, tc := range []struct {
		name string
		dc   protocol.DecodedCompare
		want bool
	}{
		{"create revision fails closed", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetCreateRevision, CreateRevision: 1}, false},
		{"mod revision fails closed", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetModRevision, ModRevision: 1}, false},
		{"unknown target", protocol.DecodedCompare{Key: present, Target: 99}, false},
		{"version 1 on present", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetVersion, Version: 1}, true},
		{"version 0 on absent", protocol.DecodedCompare{Key: absent, Target: protocol.CompareTargetVersion}, true},
		{"version greater", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetVersion, Result: protocol.CompareResultGreater}, true},
		{"value equal", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetValue, Value: []byte("bbb")}, true},
		{"value mismatch", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetValue, Value: []byte("other")}, false},
		{"value greater", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetValue, Result: protocol.CompareResultGreater, Value: []byte("aaa")}, true},
		{"value less", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetValue, Result: protocol.CompareResultLess, Value: []byte("ccc")}, true},
		{"value not equal", protocol.DecodedCompare{Key: present, Target: protocol.CompareTargetValue, Result: protocol.CompareResultNotEqual, Value: []byte("ccc")}, true},
		{"non-empty value on absent", protocol.DecodedCompare{Key: absent, Target: protocol.CompareTargetValue, Value: []byte("x")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pebbleEvaluateCompare(s, readBatch(t, s), tc.dc); got != tc.want {
				t.Fatalf("pebbleEvaluateCompare = %v, want %v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Lease operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_LeaseGrant(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	cmd := mustEncode(protocol.EncodeLeaseGrant(10))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	leaseID := int64(results[0].Value)
	if leaseID <= 0 {
		t.Fatalf("expected positive lease ID, got %d", leaseID)
	}

	// Verify lease exists in lessor.
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("expected lease to exist")
	}
	if l.TTL != 10 {
		t.Fatalf("expected TTL 10, got %d", l.TTL)
	}
}

func TestPebbleStateMachine_LeaseGrant_NoLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor configured

	cmd := mustEncode(protocol.EncodeLeaseGrant(10))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when lessor not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when lessor not configured")
	}
}

func TestPebbleStateMachine_LeaseRevoke(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(60))
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Put a key with the lease attached.
	putCmd := mustEncode(protocol.EncodeMVCCPut([]byte("lease-key"), []byte("lease-val"), leaseID))
	applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: putCmd}})

	// Revoke the lease.
	revokeCmd := mustEncode(protocol.EncodeLeaseRevoke(leaseID))
	revokeResults := applyExtEntries(t, s, []sm.Entry{{Index: 3, Cmd: revokeCmd}})

	// Should have revoked 1 attached key.
	if revokeResults[0].Value != 1 {
		t.Fatalf("expected 1 revoked key, got %d", revokeResults[0].Value)
	}

	// Verify key is deleted.
	_, err := s.Lookup(context.Background(), []byte("lease-key"))
	if err == nil {
		t.Fatal("expected error for deleted key after lease revoke")
	}

	// Verify lease is gone.
	if lr.Lookup(leaseID) != nil {
		t.Fatal("expected lease to be revoked")
	}
}

func TestPebbleStateMachine_LeaseKeepAlive(t *testing.T) {
	s, lr := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(5))
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Keep alive.
	kaCmd := mustEncode(protocol.EncodeLeaseKeepAlive(leaseID))
	kaResults := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: kaCmd}})

	if int64(kaResults[0].Value) != leaseID {
		t.Fatalf("expected lease ID %d, got %d", leaseID, kaResults[0].Value)
	}

	// Verify lease still exists.
	l := lr.Lookup(leaseID)
	if l == nil {
		t.Fatal("expected lease to exist after keep-alive")
	}
}

func TestPebbleStateMachine_LeaseKeepAlive_NoLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor

	cmd := mustEncode(protocol.EncodeLeaseKeepAlive(99))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when lessor not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when lessor not configured")
	}
}

func TestPebbleStateMachine_LeaseRevoke_NoLessor(t *testing.T) {
	s := openExtendedStore(t) // no lessor

	cmd := mustEncode(protocol.EncodeLeaseRevoke(1))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when lessor not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when lessor not configured")
	}
}

// TestPebbleStateMachine_LeaseUnknownIsIdempotent checks that keep-alive and
// revoke of a lease that does not exist apply as an unrefused no-op: a
// non-nil error from Update would stall the Raft group. Keep-alive carries
// the not-found outcome back in its own Data tag (go-qrdb#275), so the
// service layer can answer the caller without a second linearized read.
func TestPebbleStateMachine_LeaseUnknownIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		name     string
		cmd      []byte
		notFound bool
	}{
		{"keep-alive", mustEncode(protocol.EncodeLeaseKeepAlive(99999)), true},
		{"revoke", mustEncode(protocol.EncodeLeaseRevoke(99999)), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := openStoreWithLessor(t)
			results := make([]sm.Result, 1)
			if err := s.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: tc.cmd}}, results); err != nil {
				t.Fatalf("Update must not return a fatal error for a nonexistent lease: %v", err)
			}
			if _, ok := protocol.DecodeAppError(results[0].Data); ok {
				t.Fatal("expected no app-error for a nonexistent lease")
			}
			if tc.notFound {
				if id, ok := protocol.DecodeLeaseKeepAliveNotFound(results[0].Data); !ok || id != 99999 {
					t.Fatalf("expected lease-keep-alive-not-found payload for lease 99999, got ok=%v id=%d", ok, id)
				}
			}
			if results[0].Value != 0 {
				t.Fatalf("expected idempotent result Value 0, got %d", results[0].Value)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Lease Lookup queries
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_LeaseTimeToLive(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	// Grant a lease.
	grantCmd := mustEncode(protocol.EncodeLeaseGrant(30))
	grantResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: grantCmd}})
	leaseID := int64(grantResults[0].Value)

	// Query the lease.
	result, err := s.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: leaseID})
	if err != nil {
		t.Fatalf("Lookup LeaseTimeToLiveQuery: %v", err)
	}
	l := result.(*lease.Lease)
	if l.ID != leaseID {
		t.Fatalf("expected lease ID %d, got %d", leaseID, l.ID)
	}
	if l.TTL != 30 {
		t.Fatalf("expected TTL 30, got %d", l.TTL)
	}
}

// TestPebbleStateMachine_Lookup_LeaseTimeToLive_NotFound pins go-qrdb#387 on
// the "pebble" raft.sm_type (the default): a missing lease must answer
// *service.LeaseNotFoundError, the typed error every transport's error
// mapping recognizes, not a generic query error.
func TestPebbleStateMachine_Lookup_LeaseTimeToLive_NotFound(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	_, err := s.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: 999})
	if err == nil {
		t.Fatal("expected error for nonexistent lease")
	}

	var lnf *service.LeaseNotFoundError
	if !errors.As(err, &lnf) {
		t.Fatalf("expected *service.LeaseNotFoundError, got %T: %v", err, err)
	}
	if lnf.LeaseID != 999 {
		t.Errorf("LeaseID = %d, want 999", lnf.LeaseID)
	}
}

func TestPebbleStateMachine_Lookup_LeaseTimeToLive_NoLessor(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), lease.LeaseTimeToLiveQuery{ID: 1})
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

func TestPebbleStateMachine_Lookup_Leases(t *testing.T) {
	s, _ := openStoreWithLessor(t)

	// Grant two leases.
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(10))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeLeaseGrant(20))},
	})

	result, err := s.Lookup(context.Background(), lease.LeasesQuery{})
	if err != nil {
		t.Fatalf("Lookup LeasesQuery: %v", err)
	}
	leases := result.([]*lease.Lease)
	if len(leases) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(leases))
	}
}

func TestPebbleStateMachine_Lookup_Leases_NoLessor(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), lease.LeasesQuery{})
	if err == nil {
		t.Fatal("expected error when lessor not configured")
	}
}

// ---------------------------------------------------------------------------
// Tests: Index operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_IndexCreate(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("test_idx", "name", protocol.IndexCreateOptions{Unique: true, Sparse: false, Numeric: false}))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}

	// Verify index exists via Lookup.
	result, err := s.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names := result.([]string)
	if len(names) != 1 || names[0] != "test_idx" {
		t.Fatalf("expected [test_idx], got %v", names)
	}
}

func TestPebbleStateMachine_IndexCreate_Duplicate(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeIndexCreateOpts("dup_idx", "field", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
	firstResults := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if firstResults[0].Value != 1 {
		t.Fatalf("first create: expected result 1, got %d", firstResults[0].Value)
	}

	// Creating the same index again is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so duplicate-create must succeed without error.
	entries := []sm.Entry{{Index: 2, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("duplicate create: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("duplicate create: expected result 1, got %d", results[0].Value)
	}

	// Only one index entry should exist.
	listResult, err := s.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup IndexListRequest: %v", err)
	}
	names := listResult.([]string)
	if len(names) != 1 {
		t.Fatalf("expected 1 index after duplicate create, got %d", len(names))
	}
	if names[0] != "dup_idx" {
		t.Fatalf("expected index name dup_idx, got %s", names[0])
	}
}

func TestPebbleStateMachine_IndexDrop(t *testing.T) {
	s := openExtendedStore(t)

	// Create then drop.
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("drop_idx", "field", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	dropCmd := mustEncode(protocol.EncodeIndexDrop("drop_idx"))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: dropCmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}

	// Verify index is gone.
	result, err := s.Lookup(context.Background(), index.IndexListRequest{})
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	names := result.([]string)
	if len(names) != 0 {
		t.Fatalf("expected no indexes, got %v", names)
	}
}

func TestPebbleStateMachine_IndexDrop_NotFound(t *testing.T) {
	s := openExtendedStore(t)

	// Dropping a non-existent index is a no-op success (idempotent). A
	// non-nil error from a Raft Update() handler permanently stalls the
	// cluster, so drop-not-found must succeed without error.
	cmd := mustEncode(protocol.EncodeIndexDrop("nonexistent"))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("drop nonexistent: expected no-op success, got error: %v", err)
	}
	if results[0].Value != 1 {
		t.Fatalf("drop nonexistent: expected result 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_Lookup_IndexQuery(t *testing.T) {
	s := openExtendedStore(t)

	// Create an index.
	createCmd := mustEncode(protocol.EncodeIndexCreateOpts("q_idx", "field", protocol.IndexCreateOptions{Unique: false, Sparse: false, Numeric: false}))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: createCmd}})

	// Manually add entries to the index via the PebbleDB-backed index.
	idx := s.indexes["q_idx"]
	if err := idx.Put([]byte("42"), []byte("hello")); err != nil {
		t.Fatalf("index Put: %v", err)
	}

	// Query via Lookup.
	result, err := s.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "q_idx",
		Value:     []byte("hello"),
	})
	if err != nil {
		t.Fatalf("Lookup IndexQueryRequest: %v", err)
	}
	keys := result.([][]byte)
	if len(keys) != 1 || string(keys[0]) != "42" {
		t.Fatalf("expected [42], got %v", keys)
	}
}

func TestPebbleStateMachine_Lookup_IndexQuery_NotFound(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "missing_idx",
		Value:     []byte("test"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index")
	}
}

func TestPebbleStateMachine_Lookup_IndexScan_NotFound(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), index.IndexScanRequest{
		IndexName: "missing_idx",
		Start:     []byte("a"),
		End:       []byte("z"),
	})
	if err == nil {
		t.Fatal("expected error for nonexistent index")
	}
}

// ---------------------------------------------------------------------------
// Tests: Time-series operations
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_TSWrite(t *testing.T) {
	s := openStoreWithTS(t)

	ts := time.Now().UnixNano()
	cmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", ts, 42.5, map[string]string{"host": "node1"}))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWrite_NoStorage(t *testing.T) {
	s := openExtendedStore(t) // no TS configured

	ts := time.Now().UnixNano()
	cmd := mustEncode(protocol.EncodeTSWrite("cpu.usage", ts, 42.5, nil))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSWriteBatch(t *testing.T) {
	s := openStoreWithTS(t)

	now := time.Now().UnixNano()
	params := []protocol.TSWriteParams{
		{Metric: "cpu.usage", Timestamp: now, Value: 42.5, Tags: map[string]string{"host": "node1"}},
		{Metric: "mem.usage", Timestamp: now, Value: 85.0, Tags: map[string]string{"host": "node1"}},
	}
	cmd := mustEncode(protocol.EncodeTSWriteBatch(params))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 2 {
		t.Fatalf("expected 2, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWriteBatch_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	params := []protocol.TSWriteParams{
		{Metric: "cpu", Timestamp: time.Now().UnixNano(), Value: 1.0, Tags: nil},
	}
	cmd := mustEncode(protocol.EncodeTSWriteBatch(params))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSWritePoint(t *testing.T) {
	s := openStoreWithTS(t)

	ts := time.Now().UnixNano()
	cmd := mustEncode(protocol.EncodeTSWritePoint("cpu.usage", ts, 42.5, map[string]string{"host": "node1"}))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})

	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSWritePoint_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeTSWritePoint("cpu", time.Now().UnixNano(), 1.0, nil))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSDeleteSeries(t *testing.T) {
	now := time.Now().UnixNano()
	for _, tc := range []struct {
		name       string
		start, end int64
	}{
		{"bounded", now - 1000, now + 1000},
		{"no time bounds", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openStoreWithTS(t)
			writeCmd := mustEncode(protocol.EncodeTSWrite("del.metric", now, 1.0, nil))
			applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

			delCmd := mustEncode(protocol.EncodeTSDeleteSeries("del.metric", tc.start, tc.end, nil))
			results := applyExtEntries(t, s, []sm.Entry{{Index: 2, Cmd: delCmd}})
			if results[0].Value != 1 {
				t.Fatalf("expected 1, got %d", results[0].Value)
			}
		})
	}
}

func TestPebbleStateMachine_TSDeleteSeries_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeTSDeleteSeries("m", 0, 0, nil))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

func TestPebbleStateMachine_TSSetRetention(t *testing.T) {
	s := openStoreWithTS(t)

	cmd := mustEncode(protocol.EncodeTSSetRetention("cpu.usage", int64(24*time.Hour), int64(time.Minute), 1))
	results := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
	if results[0].Value != 1 {
		t.Fatalf("expected 1, got %d", results[0].Value)
	}
}

func TestPebbleStateMachine_TSSetRetention_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	cmd := mustEncode(protocol.EncodeTSSetRetention("cpu", int64(time.Hour), 0, 0))
	entries := []sm.Entry{{Index: 1, Cmd: cmd}}
	results := make([]sm.Result, 1)
	if err := s.Update(context.Background(), entries, results); err != nil {
		t.Fatalf("Update must not return a fatal error when time-series not configured: %v", err)
	}
	if _, ok := protocol.DecodeAppError(results[0].Data); !ok {
		t.Fatal("expected app-error payload when time-series not configured")
	}
}

// ---------------------------------------------------------------------------
// Tests: Time-series Lookup queries
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_TSQuery(t *testing.T) {
	s := openStoreWithTS(t)

	// Write a data point.
	now := time.Now()
	writeCmd := mustEncode(protocol.EncodeTSWrite("qry.metric", now.UnixNano(), 42.5, nil))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	// Query the data.
	result, err := s.Lookup(context.Background(), timeseries.TSQueryRequest{
		Metric: "qry.metric",
		Start:  now.Add(-time.Second),
		End:    now.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("Lookup TSQueryRequest: %v", err)
	}
	points := result.([]timeseries.DataPoint)
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	if math.Abs(points[0].Value-42.5) > 0.001 {
		t.Fatalf("expected value 42.5, got %f", points[0].Value)
	}
}

func TestPebbleStateMachine_Lookup_TSQuery_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSQueryRequest{
		Metric: "m",
		Start:  time.Now(),
		End:    time.Now(),
	})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSAggregate_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSAggregateRequest{
		Metric:   "m",
		Start:    time.Now(),
		End:      time.Now(),
		Func:     1,
		Interval: time.Second,
	})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSGetRetention_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSGetRetentionRequest{Metric: "m"})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSListMetrics_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSListMetricsRequest{Prefix: "", Limit: 10})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSRichQuery_NoStorage(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), timeseries.TSRichQueryRequest{})
	if err == nil {
		t.Fatal("expected error when time-series not configured")
	}
}

func TestPebbleStateMachine_Lookup_TSListMetrics(t *testing.T) {
	s := openStoreWithTS(t)

	// Write data to create metric entries.
	now := time.Now().UnixNano()
	applyExtEntries(t, s, []sm.Entry{
		{Index: 1, Cmd: mustEncode(protocol.EncodeTSWrite("cpu.usage", now, 1.0, nil))},
		{Index: 2, Cmd: mustEncode(protocol.EncodeTSWrite("mem.usage", now, 2.0, nil))},
	})

	result, err := s.Lookup(context.Background(), timeseries.TSListMetricsRequest{Prefix: "", Limit: 100})
	if err != nil {
		t.Fatalf("Lookup TSListMetricsRequest: %v", err)
	}
	metrics := result.([]string)
	if len(metrics) < 2 {
		t.Fatalf("expected at least 2 metrics, got %d: %v", len(metrics), metrics)
	}
}

func TestPebbleStateMachine_Lookup_TSGetRetention(t *testing.T) {
	s := openStoreWithTS(t)
	setCmd := mustEncode(protocol.EncodeTSSetRetention("ret-metric", int64(24*time.Hour), int64(time.Minute), 1))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: setCmd}})

	result, err := s.Lookup(context.Background(), timeseries.TSGetRetentionRequest{Metric: "ret-metric"})
	if err != nil {
		t.Fatalf("Lookup TSGetRetentionRequest: %v", err)
	}
	if policy := result.(*timeseries.RetentionPolicy); policy.Duration != 24*time.Hour {
		t.Fatalf("expected 24h duration, got %v", policy.Duration)
	}
}

func TestPebbleStateMachine_Lookup_TSAggregate(t *testing.T) {
	s := openStoreWithTS(t)
	now := time.Now()
	writeCmd := mustEncode(protocol.EncodeTSWrite("agg-metric", now.UnixNano(), 42.0, nil))
	applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: writeCmd}})

	result, err := s.Lookup(context.Background(), timeseries.TSAggregateRequest{
		Metric:   "agg-metric",
		Start:    now.Add(-time.Minute),
		End:      now.Add(time.Minute),
		Func:     timeseries.AggAvg,
		Interval: time.Minute,
	})
	if err != nil {
		t.Fatalf("Lookup TSAggregateRequest: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil aggregate result")
	}
}

func TestPebbleStateMachine_Lookup_TSRichQuery(t *testing.T) {
	s := openStoreWithTS(t)

	result, err := s.Lookup(context.Background(), timeseries.TSRichQueryRequest{
		Request: &timeseries.QueryRequest{
			Metric:    "cpu",
			StartTime: time.Unix(0, 0),
			EndTime:   time.Unix(0, 0).Add(time.Hour),
		},
	})
	if err != nil {
		t.Fatalf("Lookup TSRichQueryRequest: %v", err)
	}
	if _, ok := result.(*timeseries.QueryResult); !ok {
		t.Fatalf("Lookup TSRichQueryRequest returned %T, want *timeseries.QueryResult", result)
	}
}

// ---------------------------------------------------------------------------
// Tests: Watch hub integration
// ---------------------------------------------------------------------------

// TestPebbleStateMachine_WatchNotifies checks that every write path notifies
// the watch hub with one event of the right type, and fills PrevKv when the
// watcher asks for it. Each row's store holds a lessor and lease leaseID,
// granted at index 1.
func TestPebbleStateMachine_WatchNotifies(t *testing.T) {
	kvPut := func(k, v string) []byte {
		cmd, err := kv.EncodePut([]byte(k), []byte(v))
		if err != nil {
			t.Fatalf("EncodePut: %v", err)
		}
		return cmd
	}
	kvDelete := func(k string) []byte {
		cmd, err := kv.EncodeDelete([]byte(k))
		if err != nil {
			t.Fatalf("EncodeDelete: %v", err)
		}
		return cmd
	}
	mvccPut := func(k string, leaseID int64) []byte {
		return mustEncode(protocol.EncodeMVCCPut([]byte(k), []byte("val"), leaseID))
	}
	batch := func(op byte, k string, v []byte, leaseID int64) []byte {
		return mustEncode(protocol.EncodeBatch([][]byte{mustEncode(protocol.EncodeOp(op, []byte(k), v, nil, leaseID))}))
	}
	for _, tc := range []struct {
		name     string
		setup    func(leaseID int64) []byte // applied before the watch; nil for none
		cmd      func(leaseID int64) []byte
		want     watch.EventType
		wantPrev bool
	}{
		{"kv put", nil, func(int64) []byte { return kvPut("k", "v") }, watch.EventPut, false},
		{"kv delete with prev", func(int64) []byte { return kvPut("k", "v") },
			func(int64) []byte { return kvDelete("k") }, watch.EventDelete, true},
		{"mvcc put", nil, func(int64) []byte { return mvccPut("k", 0) }, watch.EventPut, false},
		{"mvcc put if absent", nil, func(int64) []byte {
			return mustEncode(protocol.EncodeMVCCPutIfAbsent([]byte("k"), []byte("val"), 0))
		}, watch.EventPut, false},
		{"mvcc overwrite with prev", func(int64) []byte { return mvccPut("k", 0) },
			func(int64) []byte { return mvccPut("k", 0) }, watch.EventPut, true},
		{"mvcc delete", func(int64) []byte { return mvccPut("k", 0) },
			func(int64) []byte { return mustEncode(protocol.EncodeMVCCDelete([]byte("k"))) }, watch.EventDelete, false},
		{"batch delete", func(int64) []byte { return mvccPut("k", 0) },
			func(int64) []byte { return batch(protocol.OpMVCCDelete, "k", nil, 0) }, watch.EventDelete, false},
		{"batch put with lease", nil,
			func(id int64) []byte { return batch(protocol.OpMVCCPut, "k", []byte("val"), id) }, watch.EventPut, false},
		{"lease revoke deletes attached key", func(id int64) []byte { return mvccPut("k", id) },
			func(id int64) []byte { return mustEncode(protocol.EncodeLeaseRevoke(id)) }, watch.EventDelete, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, lr := openStoreWithLessor(t)
			hub := watch.NewHub()
			t.Cleanup(hub.Close)
			s.SetWatchHub(hub)
			grant := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: mustEncode(protocol.EncodeLeaseGrant(60))}})
			leaseID := int64(grant[0].Value)
			if lr.Lookup(leaseID) == nil {
				t.Fatal("lease grant did not register the lease")
			}
			index := uint64(2)
			if tc.setup != nil {
				applyExtEntries(t, s, []sm.Entry{{Index: index, Cmd: tc.setup(leaseID)}})
				index++
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var opts []watch.WatchOption
			if tc.wantPrev {
				opts = append(opts, watch.WithPrevKV())
			}
			w, err := hub.Watch(ctx, "k", opts...)
			if err != nil {
				t.Fatalf("Watch: %v", err)
			}
			defer func() { _ = w.Close() }()

			applyExtEntries(t, s, []sm.Entry{{Index: index, Cmd: tc.cmd(leaseID)}})
			select {
			case resp := <-w.Events():
				if len(resp.Events) != 1 {
					t.Fatalf("expected 1 event, got %d", len(resp.Events))
				}
				ev := resp.Events[0]
				if ev.Type != tc.want {
					t.Fatalf("event type = %d, want %d", ev.Type, tc.want)
				}
				if !bytes.Equal(ev.Kv.Key, []byte("k")) {
					t.Fatalf("unexpected key: %s", ev.Kv.Key)
				}
				if tc.wantPrev && ev.PrevKv == nil {
					t.Fatal("expected PrevKv to be set")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for watch event")
			}
		})
	}
}

// guardedWatchEvent applies one guarded-verb entry and returns the single
// event a watcher on key receives.
func guardedWatchEvent(t *testing.T, s *PebbleStateMachine, hub *watch.Hub, key string, index uint64, cmd []byte) *watch.Event {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, key)
	if err != nil {
		t.Fatalf("Watch(%s): %v", key, err)
	}
	defer func() { _ = w.Close() }()

	applyExtEntries(t, s, []sm.Entry{{Index: index, Cmd: cmd}})

	select {
	case resp := <-w.Events():
		if len(resp.Events) != 1 {
			t.Fatalf("expected 1 event for %s, got %d", key, len(resp.Events))
		}
		return resp.Events[0]
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for the watch event on %s", key)
	}
	return nil
}

// TestPebbleStateMachine_GuardedVerbs_NotifyWatchers proves PutIfAbsent,
// CompareAndSwap and CompareAndDelete each deliver the event a watcher of the
// key expects, with the value the verb committed.
func TestPebbleStateMachine_GuardedVerbs_NotifyWatchers(t *testing.T) {
	s, hub := openStoreWithHub(t)

	put := guardedWatchEvent(t, s, hub, "guard-key", 1, mustEncode(kv.EncodePutIfAbsent([]byte("guard-key"), []byte("v1"))))
	if put.Type != watch.EventPut || string(put.Kv.Value) != "v1" {
		t.Fatalf("PutIfAbsent event = type %d value %q, want EventPut v1", put.Type, put.Kv.Value)
	}

	swap := guardedWatchEvent(t, s, hub, "guard-key", 2,
		mustEncode(protocol.EncodeCompareAndSwap([]byte("guard-key"), []byte("v1"), []byte("v2"))))
	if swap.Type != watch.EventPut || string(swap.Kv.Value) != "v2" {
		t.Fatalf("CompareAndSwap event = type %d value %q, want EventPut v2", swap.Type, swap.Kv.Value)
	}

	del := guardedWatchEvent(t, s, hub, "guard-key", 3,
		mustEncode(protocol.EncodeCompareAndDelete([]byte("guard-key"), []byte("v2"))))
	if del.Type != watch.EventDelete {
		t.Fatalf("CompareAndDelete event type = %d, want EventDelete", del.Type)
	}
}

// TestPebbleStateMachine_GuardedVerbs_RefuseReservedKeys proves the guarded
// verbs refuse the reserved key space with a typed application error, as a
// plain Put does, rather than writing into it.
func TestPebbleStateMachine_GuardedVerbs_RefuseReservedKeys(t *testing.T) {
	reserved := []byte{0x00, 'r', 'e', 's'}
	cmds := map[string][]byte{
		"PutIfAbsent":      mustEncode(kv.EncodePutIfAbsent(reserved, []byte("v"))),
		"CompareAndSwap":   mustEncode(protocol.EncodeCompareAndSwap(reserved, []byte("a"), []byte("b"))),
		"CompareAndDelete": mustEncode(protocol.EncodeCompareAndDelete(reserved, []byte("a"))),
	}
	for name, cmd := range cmds {
		t.Run(name, func(t *testing.T) {
			s := openExtendedStore(t)
			res := applyExtEntries(t, s, []sm.Entry{{Index: 1, Cmd: cmd}})
			if len(res[0].Data) == 0 {
				t.Fatalf("%s on a reserved key returned no refusal: %+v", name, res[0])
			}
			if _, err := s.Lookup(context.Background(), reserved); err == nil {
				t.Fatalf("%s wrote into the reserved key space", name)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: Unknown opcode
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_UnknownOp(t *testing.T) {
	// An opcode that is not registered is REFUSED in the result payload, not
	// returned to Raft: an opcode with no handler says nothing about the
	// health of the storage underneath, and returning it halted the shard
	// (go-qrdb#520). The same holds for an unknown op inside a batch.
	unknownOp := mustEncode(protocol.EncodeOp(0x99, []byte("k"), []byte("v"), nil, 0))
	for _, tc := range []struct {
		name string
		cmd  []byte
	}{
		{"top-level command", []byte{200, 0, 0}},
		{"batch op", mustEncode(protocol.EncodeBatch([][]byte{unknownOp}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := openExtendedStore(t)
			entries := []sm.Entry{{Index: 1, Cmd: tc.cmd}}
			results := make([]sm.Result, 1)
			assertRefusedNotHalted(t, s.Update(context.Background(), entries, results),
				results[0], "unknown op code")
		})
	}
}

func TestPebbleStateMachine_EmptyCommand(t *testing.T) {
	s := openExtendedStore(t)

	entries := []sm.Entry{{Index: 1, Cmd: []byte{}}}
	results := make([]sm.Result, 1)
	err := s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

// ---------------------------------------------------------------------------
// Tests: Setter methods
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_SetTimeSeries(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.TimeSeries() != nil {
		t.Fatal("expected nil TimeSeries")
	}

	tsStg := memory.NewStorage()
	defer func() { _ = tsStg.Close() }()

	ts, err := timeseries.New(tsStg)
	if err != nil {
		t.Fatalf("ts new: %v", err)
	}
	s.SetTimeSeries(ts)
	if s.TimeSeries() != ts {
		t.Fatal("expected TimeSeries to be set")
	}
}

func TestPebbleStateMachine_SetLessor(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.Lessor() != nil {
		t.Fatal("expected nil Lessor")
	}

	lr := lease.NewLessor()
	s.SetLessor(lr)
	if s.Lessor() != lr {
		t.Fatal("expected Lessor to be set")
	}
}

func TestPebbleStateMachine_DB(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	if s.DB() != nil {
		t.Fatal("expected nil DB before open")
	}

	dir := filepath.Join(t.TempDir(), "db")
	_, err := s.Open(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = s.Close(context.Background()) }()

	if s.DB() == nil {
		t.Fatal("expected non-nil DB after open")
	}
}

// ---------------------------------------------------------------------------
// Tests: Lookup unsupported query type
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Lookup_UnsupportedType(t *testing.T) {
	s := openExtendedStore(t)

	_, err := s.Lookup(context.Background(), 42)
	if err == nil {
		t.Fatal("expected error for unsupported query type")
	}
	var iqe *InvalidQueryError
	if !errors.As(err, &iqe) {
		t.Fatalf("expected InvalidQueryError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Not-open errors
// ---------------------------------------------------------------------------

func TestPebbleStateMachine_Update_NotOpen(t *testing.T) {
	s := NewPebbleStateMachine(nil)
	putCmd, err := kv.EncodePut([]byte("k"), []byte("v"))
	if err != nil {
		t.Fatalf("EncodePut: %v", err)
	}
	entries := []sm.Entry{{Index: 1, Cmd: putCmd}}
	results := make([]sm.Result, 1)
	err = s.Update(context.Background(), entries, results)
	if err == nil {
		t.Fatal("expected NotOpenError")
	}
	var noe *NotOpenError
	if !errors.As(err, &noe) {
		t.Fatalf("expected NotOpenError, got %T: %v", err, err)
	}
}
