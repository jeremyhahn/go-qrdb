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
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-quicraft/pkg/sm"
	"github.com/jeremyhahn/go-quicraft/pkg/sm/kv"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/protocol"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// kvPutIfAbsentEntry builds a base KV conditional-put entry.
func kvPutIfAbsentEntry(idx uint64, key, value string) sm.Entry {
	cmd, _ := kv.EncodePutIfAbsent([]byte(key), []byte(value))
	return sm.Entry{Index: idx, Cmd: cmd}
}

// KVEngine gained this opcode with #41: tenant.IdentityIndex claims an id
// before writing an identity record, and without a conditional write the
// claim is a read-then-write two proposers can both win — which is what lets
// one tenant capture another tenant's caller-supplied role id and redirect
// every by-id operation performed on it. The opcode already existed in the
// command format; only this engine's handler was missing, so a conditional
// write failed the whole proposal with UnsupportedOperationError.
//
// These assertions are the same contract PebbleStateMachine and MVCCEngine
// are held to, so the three implementations cannot drift.
func TestKVEngine_PutIfAbsent_StoresWhenAbsent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	results := applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})

	if results[0].Value != kv.ResultStored {
		t.Fatalf("Value = %d, want kv.ResultStored (%d)", results[0].Value, kv.ResultStored)
	}
	got, err := eng.Lookup(context.Background(), "claim")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "acme" {
		t.Fatalf("stored value = %q, want %q", got, "acme")
	}
}

// The losing caller must be told it lost — through Result.Value, not an error,
// which would stall consensus — and must not have overwritten the holder.
func TestKVEngine_PutIfAbsent_DeclinesWhenPresent(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})
	results := applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(2, "claim", "globex")})

	if results[0].Value != kv.ResultNotStored {
		t.Fatalf("Value = %d, want kv.ResultNotStored (%d)", results[0].Value, kv.ResultNotStored)
	}
	got, err := eng.Lookup(context.Background(), "claim")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "acme" {
		t.Fatalf("value after a declined write = %q, want the holder's %q — the "+
			"loser overwrote the winner, which is the capture this opcode prevents",
			got, "acme")
	}
}

// An id released by its holder becomes claimable again; otherwise a deleted
// user's id would be permanently unroutable.
func TestKVEngine_PutIfAbsent_ReclaimableAfterDelete(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)
	applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})
	applyKVEntries(t, eng, []sm.Entry{kvDeleteEntry(2, "claim")})
	results := applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(3, "claim", "globex")})

	if results[0].Value != kv.ResultStored {
		t.Fatalf("Value = %d, want kv.ResultStored after the holder released it",
			results[0].Value)
	}
	got, err := eng.Lookup(context.Background(), "claim")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if string(got.([]byte)) != "globex" {
		t.Fatalf("value = %q, want %q", got, "globex")
	}
}

// A conditional put that stores must maintain secondary indexes exactly as an
// unconditional put does — a record indexed only on the Put path would be
// invisible to every index query.
func TestKVEngine_PutIfAbsent_MaintainsSecondaryIndexes(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	eng := NewKVEngine(stg)

	idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
	results := make([]sm.Result, 1)
	if err := eng.Update(context.Background(), []sm.Entry{{Index: 1, Cmd: idxCmd}}, results); err != nil {
		t.Fatalf("Update (index create): %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(2, "user:1", `{"name":"alice"}`)})

	got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
		IndexName: "by_name",
		Value:     []byte("alice"),
	})
	if err != nil {
		t.Fatalf("Lookup (index query): %v", err)
	}
	if ids := got.([][]byte); len(ids) != 1 {
		t.Fatalf("index returned %d ids, want 1 — a conditionally-stored record "+
			"was not indexed", len(ids))
	}
}

// A stored conditional put notifies watchers; a declined one must not, because
// nothing changed.
func TestKVEngine_PutIfAbsent_NotifiesOnlyWhenStored(t *testing.T) {
	stg := memory.NewStorage()
	defer func() { _ = stg.Close() }()

	hub := watch.NewHub()
	defer hub.Close()

	eng := NewKVEngine(stg, WithKVWatchHub(hub))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, err := hub.Watch(ctx, "claim")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})
	select {
	case resp := <-w.Events():
		if len(resp.Events) == 0 {
			t.Fatal("expected a PUT event for the stored claim")
		}
		if pv := resp.Events[0].PrevKv; pv != nil {
			t.Fatalf("PrevKV = %v, want nil — the guard established the key was absent", pv)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for the stored claim's watch event")
	}

	applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(2, "claim", "globex")})
	select {
	case resp := <-w.Events():
		t.Fatalf("a DECLINED conditional put emitted %d event(s); nothing changed",
			len(resp.Events))
	case <-time.After(200 * time.Millisecond):
	}
}

// THE CLAIM-ATOMICITY RACE. Of N conditional puts to the same key, exactly
// one may store — whatever grouping the apply loop happens to pack them in.
// This is the property tenant.IdentityIndex's claim rests on: two proposers
// racing to claim the same identity id must not both win, or both would go on
// to write a record and the loser's would silently capture where that id
// routes.
//
// PebbleStateMachine and MVCCStateMachine are held to this same requirement
// (TestPebbleStateMachine_PutIfAbsentHasExactlyOneStore,
// TestMVCCStateMachine_BaseKVPutIfAbsentHasExactlyOneStore); KVEngine is the
// third implementation and must not be the weak one.
func TestKVEngine_PutIfAbsentHasExactlyOneStore(t *testing.T) {
	const contenders = 8

	// The apply loop may hand an engine any grouping of committed entries.
	// The decision must not depend on it.
	groupings := [][]int{
		{contenders},
		{contenders / 2, contenders - contenders/2},
		{1, 1, 1, 1, 1, 1, 1, 1},
		{1, contenders - 2, 1},
	}

	for gi, sizes := range groupings {
		t.Run(groupingName(sizes), func(t *testing.T) {
			stg := memory.NewStorage()
			defer func() { _ = stg.Close() }()
			eng := NewKVEngine(stg)

			entries := make([]sm.Entry, contenders)
			for i := range entries {
				entries[i] = kvPutIfAbsentEntry(uint64(1+i), "create-once",
					"v"+string(rune('0'+i)))
			}

			results := make([]sm.Result, contenders)
			off := 0
			for _, size := range sizes {
				if err := eng.Update(context.Background(),
					entries[off:off+size], results[off:off+size]); err != nil {
					t.Fatalf("grouping %d: Update: %v", gi, err)
				}
				off += size
			}

			stored := 0
			var winner int
			for i, r := range results {
				if r.Value == kv.ResultStored {
					stored++
					winner = i
				}
			}
			if stored != 1 {
				t.Fatalf("%d of %d conditional puts stored, want exactly 1 — two "+
					"claimants both won and the loser captures the id", stored, contenders)
			}

			// The stored value must be the winner's, not a later loser's.
			got, err := eng.Lookup(context.Background(), "create-once")
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			want := "v" + string(rune('0'+winner))
			if string(got.([]byte)) != want {
				t.Fatalf("committed value = %q, want the winner's %q", got, want)
			}
		})
	}
}

// groupingName renders an apply grouping for a subtest name.
func groupingName(sizes []int) string {
	name := "grouping"
	for _, s := range sizes {
		name += "-" + string(rune('0'+s))
	}
	return name
}

// errReadFault is the fault readFaultStorage raises.
var errReadFault = errors.New("injected read fault")

// readFaultStorage fails Get for one key, leaving every other operation
// intact. It models a transient local read fault on a replica.
//
// failPrefix is the same fault expressed over a key PREFIX, which is what an
// MVCC-plane test needs: the MVCC store reads a key's metadata under an encoded
// meta key rather than under the key itself, so naming the raw key would arm
// nothing.
type readFaultStorage struct {
	storage.Storage
	failKey    string
	failPrefix []byte
}

// faults reports whether a read of key is the injected fault.
func (s *readFaultStorage) faults(key []byte) bool {
	if string(key) == s.failKey {
		return true
	}
	return len(s.failPrefix) > 0 && bytes.HasPrefix(key, s.failPrefix)
}

func (s *readFaultStorage) Get(key []byte) ([]byte, error) {
	if s.faults(key) {
		return nil, errReadFault
	}
	return s.Storage.Get(key)
}

// Batch wraps the underlying batch so reads made INSIDE the apply batch fault
// too. Without this the injection is invisible to the apply path: an engine
// reads through storage.Batch, and the memory batch reads from the storage it
// was created by rather than from this wrapper, so an unwrapped batch would
// answer every read successfully while Get is armed.
func (s *readFaultStorage) Batch() storage.Batch {
	return &readFaultBatch{Batch: s.Storage.Batch(), storage: s}
}

// readFaultBatch raises the storage's injected fault for reads made through the
// batch. Writes and commits are untouched, so a test can still observe exactly
// what the faulting apply wrote.
type readFaultBatch struct {
	storage.Batch
	storage *readFaultStorage
}

func (b *readFaultBatch) Get(key []byte) ([]byte, error) {
	if b.storage.faults(key) {
		return nil, errReadFault
	}
	return b.Batch.Get(key)
}

// A conditional put whose EXISTENCE READ fails must not decide. Folding a read
// fault into "absent" overwrites the holder and reports ResultStored — and
// because the fault is LOCAL to one replica, replicas whose reads succeeded
// would decline the same entry this one stored, so the group diverges from an
// identical log. That is the one thing a state machine may never do, which is
// why this returns a Go error and stalls instead of guessing.
func TestKVEngine_PutIfAbsent_AFailedExistenceReadWritesNothing(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	stg := &readFaultStorage{Storage: inner, failKey: "claim"}
	eng := NewKVEngine(stg)

	// Seed the holder through the fault-free path.
	stg.failKey = ""
	applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})
	stg.failKey = "claim"

	results := make([]sm.Result, 1)
	err := eng.Update(context.Background(),
		[]sm.Entry{kvPutIfAbsentEntry(2, "claim", "globex")}, results)
	if !errors.Is(err, errReadFault) {
		t.Fatalf("Update = %v, want the read fault propagated — a fault read as "+
			"absence overwrites the holder and diverges replicas", err)
	}

	// Nothing was written: the holder survives.
	stg.failKey = ""
	got, gerr := eng.Lookup(context.Background(), "claim")
	if gerr != nil {
		t.Fatalf("Lookup: %v", gerr)
	}
	if string(got.([]byte)) != "acme" {
		t.Fatalf("value after a failed existence read = %q, want the holder's %q",
			got, "acme")
	}
}

// kvCompareAndDeleteEntry builds a guarded-delete entry.
func kvCompareAndDeleteEntry(idx uint64, key, expected string) sm.Entry {
	cmd, err := protocol.EncodeCompareAndDelete([]byte(key), []byte(expected))
	if err != nil {
		panic(err) // test keys are small; an error here is a test bug
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

// The guarded delete is the release half of the conditional put, and is held
// to the mirror-image contract: it removes the key only when the stored value
// still matches, reports a refusal through Result.Value rather than an error,
// retracts secondary indexes, and notifies watchers only when it deleted.
func TestKVEngine_CompareAndDelete_Contract(t *testing.T) {
	t.Run("deletes on a match", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})
		results := applyKVEntries(t, eng, []sm.Entry{kvCompareAndDeleteEntry(2, "claim", "acme")})

		if results[0].Value != protocol.ResultDeleted {
			t.Fatalf("Value = %d, want ResultDeleted", results[0].Value)
		}
		if got, err := eng.Lookup(context.Background(), "claim"); err == nil && got != nil {
			if b, ok := got.([]byte); ok && b != nil {
				t.Fatalf("value survived a matching guarded delete: %q", b)
			}
		}
	})

	t.Run("refuses on a mismatch and changes nothing", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})
		results := applyKVEntries(t, eng, []sm.Entry{kvCompareAndDeleteEntry(2, "claim", "globex")})

		if results[0].Value != protocol.ResultNotDeleted {
			t.Fatalf("Value = %d, want ResultNotDeleted — a release holding a "+
				"stale expectation removed the current holder's value", results[0].Value)
		}
		got, err := eng.Lookup(context.Background(), "claim")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if string(got.([]byte)) != "acme" {
			t.Fatalf("value = %q, want the holder's %q", got, "acme")
		}
	})

	t.Run("refuses on an absent key", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		results := applyKVEntries(t, eng, []sm.Entry{kvCompareAndDeleteEntry(1, "never", "acme")})
		if results[0].Value != protocol.ResultNotDeleted {
			t.Fatalf("Value = %d, want ResultNotDeleted", results[0].Value)
		}
	})

	t.Run("retracts secondary indexes", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		idxCmd := mustEncode(protocol.EncodeIndexCreate("by_name", "name", false, false, false))
		if err := eng.Update(context.Background(),
			[]sm.Entry{{Index: 1, Cmd: idxCmd}}, make([]sm.Result, 1)); err != nil {
			t.Fatalf("Update (index create): %v", err)
		}
		const record = `{"name":"alice"}`
		applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(2, "user:1", record)})
		applyKVEntries(t, eng, []sm.Entry{kvCompareAndDeleteEntry(3, "user:1", record)})

		got, err := eng.Lookup(context.Background(), index.IndexQueryRequest{
			IndexName: "by_name", Value: []byte("alice"),
		})
		if err != nil {
			t.Fatalf("Lookup (index query): %v", err)
		}
		if ids := got.([][]byte); len(ids) != 0 {
			t.Fatalf("index still returns %d ids after a guarded delete — the "+
				"entry was not retracted", len(ids))
		}
	})
}

// A guarded delete whose READ fails must not decide. The read-fault rule
// applies to a delete guard exactly as it does to a conditional put's: a local
// fault treated as "value differs" leaves the key here while peers that read
// successfully removed it, diverging the group from an identical log.
func TestKVEngine_CompareAndDelete_AFailedGuardReadDeletesNothing(t *testing.T) {
	inner := memory.NewStorage()
	defer func() { _ = inner.Close() }()

	stg := &readFaultStorage{Storage: inner}
	eng := NewKVEngine(stg)

	applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "claim", "acme")})

	stg.failKey = "claim"
	err := eng.Update(context.Background(),
		[]sm.Entry{kvCompareAndDeleteEntry(2, "claim", "acme")}, make([]sm.Result, 1))
	stg.failKey = ""

	if !errors.Is(err, errReadFault) {
		t.Fatalf("Update = %v, want the read fault propagated", err)
	}
	got, gerr := eng.Lookup(context.Background(), "claim")
	if gerr != nil {
		t.Fatalf("Lookup: %v", gerr)
	}
	if string(got.([]byte)) != "acme" {
		t.Fatalf("value after a failed guard read = %q, want %q", got, "acme")
	}
}

// kvCompareAndSwapEntry builds a guarded-swap entry for the base KV engine.
func kvCompareAndSwapEntry(idx uint64, key, expected, next string) sm.Entry {
	cmd, err := protocol.EncodeCompareAndSwap([]byte(key), []byte(expected), []byte(next))
	if err != nil {
		panic(err) // test values are small; an error here is a test bug
	}
	return sm.Entry{Index: idx, Cmd: cmd}
}

// The base KV engine's guarded swap, held to the same contract as its guarded
// delete: decided inside the state machine, and a refused guard changes
// nothing.
func TestKVEngine_CompareAndSwap_Contract(t *testing.T) {
	lookup := func(t *testing.T, eng *KVEngine, key string) ([]byte, bool) {
		t.Helper()
		got, err := eng.Lookup(context.Background(), key)
		if err != nil || got == nil {
			return nil, false
		}
		b, ok := got.([]byte)
		if !ok || b == nil {
			return nil, false
		}
		return b, true
	}

	t.Run("swaps on a match", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "lease", "gen-1")})
		results := applyKVEntries(t, eng, []sm.Entry{kvCompareAndSwapEntry(2, "lease", "gen-1", "gen-2")})

		if results[0].Value != protocol.ResultSwapped {
			t.Fatalf("Value = %d, want ResultSwapped", results[0].Value)
		}
		got, ok := lookup(t, eng, "lease")
		if !ok || string(got) != "gen-2" {
			t.Fatalf("value = %q (present=%v), want %q", got, ok, "gen-2")
		}
	})

	t.Run("refuses on a mismatch and keeps the current value", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "lease", "gen-2")})
		results := applyKVEntries(t, eng, []sm.Entry{kvCompareAndSwapEntry(2, "lease", "gen-1", "gen-9")})

		if results[0].Value != protocol.ResultNotSwapped {
			t.Fatalf("Value = %d, want ResultNotSwapped — a writer holding a stale "+
				"expectation overwrote the current holder", results[0].Value)
		}
		got, ok := lookup(t, eng, "lease")
		if !ok || string(got) != "gen-2" {
			t.Fatalf("value = %q (present=%v), want the holder's %q", got, ok, "gen-2")
		}
	})

	t.Run("does NOT create an absent key", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		results := applyKVEntries(t, eng, []sm.Entry{kvCompareAndSwapEntry(1, "never", "", "value")})
		if results[0].Value != protocol.ResultNotSwapped {
			t.Fatalf("Value = %d, want ResultNotSwapped", results[0].Value)
		}
		if got, ok := lookup(t, eng, "never"); ok {
			t.Fatalf("a guarded swap CREATED an absent key (%q); that is PutIfAbsent's job", got)
		}
	})

	t.Run("swapping to an empty value is a swap, not a delete", func(t *testing.T) {
		stg := memory.NewStorage()
		defer func() { _ = stg.Close() }()
		eng := NewKVEngine(stg)

		applyKVEntries(t, eng, []sm.Entry{kvPutIfAbsentEntry(1, "k", "full")})
		results := applyKVEntries(t, eng, []sm.Entry{kvCompareAndSwapEntry(2, "k", "full", "")})

		if results[0].Value != protocol.ResultSwapped {
			t.Fatalf("Value = %d, want ResultSwapped", results[0].Value)
		}
		got, err := eng.Lookup(context.Background(), "k")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		b, _ := got.([]byte)
		if len(b) != 0 {
			t.Fatalf("value = %q, want it stored as empty", b)
		}
	})
}
