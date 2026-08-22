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

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// mvccCompareAndDeleteEntry builds a guarded-delete entry.
func mvccCompareAndDeleteEntry(idx uint64, key, expected string) sm.Entry {
	cmd, err := protocol.EncodeCompareAndDelete([]byte(key), []byte(expected))
	if err != nil {
		panic(err) // test keys are small; an error here is a test bug
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

// MVCCEngine is the THIRD implementation of the guarded delete, and the one
// that had no direct coverage: KVEngine and PebbleStateMachine were held to
// this contract and this handler was not. It is the release half of identity
// claiming, so a regression here removes the wrong claim.
func TestMVCCEngine_CompareAndDelete_Contract(t *testing.T) {
	t.Run("deletes on a match", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "claim", "acme")})
		results := applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(2, "claim", "acme")})

		if results[0].Value != protocol.ResultDeleted {
			t.Fatalf("Value = %d, want ResultDeleted", results[0].Value)
		}
		if _, err := eng.Store().Get([]byte("claim")); err == nil {
			t.Fatal("the key survived a matching guarded delete")
		}
	})

	t.Run("refuses on a mismatch and keeps the holder", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "claim", "acme")})
		results := applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(2, "claim", "globex")})

		if results[0].Value != protocol.ResultNotDeleted {
			t.Fatalf("Value = %d, want ResultNotDeleted — a release holding a "+
				"stale expectation removed the current holder's value", results[0].Value)
		}
		got, err := eng.Store().Get([]byte("claim"))
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(got.Value) != "acme" {
			t.Fatalf("value = %q, want the holder's %q", got.Value, "acme")
		}
	})

	t.Run("refuses on an absent key", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		results := applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(1, "never", "acme")})
		if results[0].Value != protocol.ResultNotDeleted {
			t.Fatalf("Value = %d, want ResultNotDeleted", results[0].Value)
		}
	})

	t.Run("sees a write packed earlier in the SAME batch", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		// The guard must read through the apply batch, or its decision would
		// depend on how the apply loop happened to group the entries — and
		// replicas grouping differently would diverge.
		results := applyMVCCEntries(t, eng, []sm.Entry{
			baseKVPutIfAbsentEntry(1, "claim", "acme"),
			mvccCompareAndDeleteEntry(2, "claim", "acme"),
		})
		if results[1].Value != protocol.ResultDeleted {
			t.Fatalf("Value = %d, want ResultDeleted — the guard did not see the "+
				"write packed ahead of it in the same batch", results[1].Value)
		}
	})

	t.Run("allocates a tombstone revision", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "claim", "acme")})
		before := eng.Store().CurrentRevision()
		applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(2, "claim", "acme")})

		if after := eng.Store().CurrentRevision(); after <= before {
			t.Fatalf("revision %d -> %d: a guarded delete must allocate a fresh "+
				"revision like any other delete", before, after)
		}
	})

	t.Run("a REFUSED delete allocates nothing", func(t *testing.T) {
		eng, stg := newMVCCEngine(t)
		defer func() { _ = stg.Close() }()
		defer func() { _ = eng.Close(context.Background()) }()

		applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "claim", "acme")})
		before := eng.Store().CurrentRevision()
		applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(2, "claim", "globex")})

		if after := eng.Store().CurrentRevision(); after != before {
			t.Fatalf("revision %d -> %d: a refused guard must change nothing at all",
				before, after)
		}
	})
}

// A guarded delete that stores must retract secondary indexes, exactly as an
// ordinary delete does. The guard changes WHEN a delete happens, never what a
// delete means.
func TestMVCCEngine_CompareAndDelete_RetractsIndexes(t *testing.T) {
	eng, stg := newMVCCEngine(t)
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	if err := eng.Update(context.Background(),
		[]sm.Entry{{Index: 1, Cmd: idxCmd}}, make([]sm.Result, 1)); err != nil {
		t.Fatalf("Update (index create): %v", err)
	}

	const record = `{"name":"alice"}`
	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(2, "user:1", record)})
	applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(3, "user:1", record)})

	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name", Value: []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup (index query): %v", err)
	}
	if ids := got.([][]byte); len(ids) != 0 {
		t.Fatalf("index still returns %d id(s) after a guarded delete — the "+
			"entry was not retracted", len(ids))
	}
}

// A stored guarded delete notifies watchers with the previous value; a
// refused one notifies nobody, because nothing changed.
func TestMVCCEngine_CompareAndDelete_NotifiesOnlyWhenDeleted(t *testing.T) {
	hub := watch.NewHub()
	defer hub.Close()

	eng, stg := newMVCCEngine(t, WithMVCCWatchHub(hub))
	defer func() { _ = stg.Close() }()
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "claim", "acme")})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := hub.Watch(ctx, "claim")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	// A refused guard emits nothing.
	applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(2, "claim", "globex")})
	select {
	case resp := <-w.Events():
		t.Fatalf("a REFUSED guarded delete emitted %d event(s); nothing changed",
			len(resp.Events))
	case <-time.After(200 * time.Millisecond):
	}

	// A matching guard emits a DELETE event, exactly as handleMVCCDelete
	// does. This deliberately does NOT assert a populated PrevKv: measured on
	// this engine, an ORDINARY MVCC delete also delivers PrevKv=nil through
	// the hub, so requiring one here would hold the guarded path to a
	// standard its own sibling does not meet — and would fail for a reason
	// that has nothing to do with the guard.
	applyMVCCEntries(t, eng, []sm.Entry{mvccCompareAndDeleteEntry(3, "claim", "acme")})
	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected a DELETE event for the guarded delete")
		}
		if got := resp.Events[0].Type; got != watch.EventDelete {
			t.Fatalf("event type = %v, want a DELETE", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for the guarded delete's watch event")
	}
}

// A guarded delete whose GUARD READ fails must not decide. A local read fault
// treated as "value differs" leaves the key on this replica while peers that
// read successfully removed it, diverging the group from an identical log.
func TestMVCCEngine_CompareAndDelete_AFailedGuardReadDeletesNothing(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	stg := &mvccReadFaultStorage{Storage: inner}
	eng, err := NewMVCCEngine(stg)
	if err != nil {
		t.Fatalf("NewMVCCEngine: %v", err)
	}
	defer func() { _ = eng.Close(context.Background()) }()

	applyMVCCEntries(t, eng, []sm.Entry{baseKVPutIfAbsentEntry(1, "claim", "acme")})

	stg.armed = true
	uerr := eng.Update(context.Background(),
		[]sm.Entry{mvccCompareAndDeleteEntry(2, "claim", "acme")}, make([]sm.Result, 1))
	stg.armed = false

	if !errors.Is(uerr, errMVCCReadFault) {
		t.Fatalf("Update = %v, want the read fault propagated", uerr)
	}
	got, gerr := eng.Store().Get([]byte("claim"))
	if gerr != nil {
		t.Fatalf("Get after the failed guard read: %v", gerr)
	}
	if string(got.Value) != "acme" {
		t.Fatalf("value = %q, want the holder's %q", got.Value, "acme")
	}
}

var errMVCCReadFault = errors.New("injected mvcc read fault")

// mvccReadFaultStorage fails reads once armed.
//
// It wraps Batch() as well as Get, because the apply-path guard reads THROUGH
// THE BATCH — GetInBatch, so that an entry observes writes packed ahead of it
// in the same batch. A wrapper that faulted only the store's own Get would
// inject nothing and the test would pass with the propagation removed.
type mvccReadFaultStorage struct {
	storage.Storage
	armed bool
}

func (s *mvccReadFaultStorage) Get(key []byte) ([]byte, error) {
	if s.armed {
		return nil, errMVCCReadFault
	}
	return s.Storage.Get(key)
}

func (s *mvccReadFaultStorage) Batch() storage.Batch {
	return &mvccReadFaultBatch{Batch: s.Storage.Batch(), owner: s}
}

type mvccReadFaultBatch struct {
	storage.Batch
	owner *mvccReadFaultStorage
}

func (b *mvccReadFaultBatch) Get(key []byte) ([]byte, error) {
	if b.owner.armed {
		return nil, errMVCCReadFault
	}
	return b.Batch.Get(key)
}
