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
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// kvPutEntry builds a raw KV Put entry ([op][keyLen:2 LE][key][value]) at
// the given Raft index, matching KVStateMachine's wire format.
func kvPutEntry(index uint64, key, value string) sm.Entry {
	payload := make([]byte, 0, 3+len(key)+len(value))
	payload = append(payload, 0x01) // kv.OpPut
	var kl [2]byte
	binary.LittleEndian.PutUint16(kl[:], uint16(len(key)))
	payload = append(payload, kl[:]...)
	payload = append(payload, key...)
	payload = append(payload, value...)
	return sm.Entry{Index: index, Cmd: payload}
}

// TestDiskAdapter_RestartResumesFromPersistedIndex pins the restart
// contract this adapter previously violated: Open used to return a
// hardcoded 0, so a restarted replica reported "fresh" while its storage
// still held data, and Raft replayed the entire log over non-empty state.
// Now the applied index is persisted with each Update and Open resumes
// from it over the SAME storage path.
func TestDiskAdapter_RestartResumesFromPersistedIndex(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	open := func() (*DiskAdapter, func()) {
		st, err := pebblestore.New(pebblestore.TestConfig(dir))
		if err != nil {
			t.Fatalf("open storage: %v", err)
		}
		backend := NewDiskBackend(st)
		kvsm := NewKVStateMachine(backend)
		a := &DiskAdapter{KVStateMachine: kvsm, store: st}
		return a, func() { _ = st.Close() }
	}

	// First "boot": fresh store reports index 0, applies entries 1..3.
	a, closeStore := open()
	idx, err := a.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if idx != 0 {
		t.Fatalf("fresh store Open = %d, want 0", idx)
	}
	entries := []sm.Entry{
		kvPutEntry(1, "a", "1"),
		kvPutEntry(2, "b", "2"),
		kvPutEntry(3, "c", "3"),
	}
	if err := a.Update(ctx, entries, make([]sm.Result, len(entries))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	closeStore()

	// "Restart": the same path must resume from index 3 with data intact.
	a2, closeStore2 := open()
	defer closeStore2()
	idx, err = a2.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("restart Open: %v", err)
	}
	if idx != 3 {
		t.Fatalf("restart Open = %d, want 3 (the persisted applied index)", idx)
	}
	val, err := a2.BytesLookup([]byte("c"))
	if err != nil {
		t.Fatalf("BytesLookup after restart: %v", err)
	}
	if string(val) != "3" {
		t.Fatalf("value after restart = %q, want %q", val, "3")
	}
}

// TestDiskAdapter_EmptyUpdateDoesNotRegressIndex verifies an Update with no
// entries leaves the persisted index untouched.
func TestDiskAdapter_EmptyUpdateDoesNotRegressIndex(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	st, err := pebblestore.New(pebblestore.TestConfig(dir))
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	defer func() { _ = st.Close() }()
	a := &DiskAdapter{KVStateMachine: NewKVStateMachine(NewDiskBackend(st)), store: st}

	entries := []sm.Entry{kvPutEntry(7, "k", "v")}
	if err := a.Update(ctx, entries, make([]sm.Result, 1)); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := a.Update(ctx, nil, nil); err != nil {
		t.Fatalf("empty Update: %v", err)
	}
	idx, err := a.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if idx != 7 {
		t.Fatalf("index after empty Update = %d, want 7", idx)
	}
}

// TestNewKVDiskCreateFunc_ShardIdentityReachesFactory pins the
// shard/replica plumbing: every (shard, replica) pair must reach the
// storage factory so replicas get distinct, reopenable paths — the
// previous hardcoded (0, 0) shared one location across every shard.
func TestNewKVDiskCreateFunc_ShardIdentityReachesFactory(t *testing.T) {
	type pair struct{ shard, replica uint64 }
	var got []pair
	factory := NewKVDiskCreateFunc(func(shardID, replicaID uint64) (storage.Storage, error) {
		got = append(got, pair{shardID, replicaID})
		st, err := pebblestore.New(pebblestore.TestConfig(t.TempDir()))
		if err != nil {
			t.Fatalf("storage: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		return st, nil
	})

	_ = factory(42, 7)
	if len(got) != 1 || got[0] != (pair{42, 7}) {
		t.Fatalf("factory saw %v, want [{42 7}]", got)
	}
}

// syncCountingStore is a storage.Storage that also implements
// storage.Syncer, counting Sync calls and returning err from each.
type syncCountingStore struct {
	storage.Storage
	calls int
	err   error
}

func (s *syncCountingStore) Sync() error {
	s.calls++
	return s.err
}

// TestDiskAdapter_SyncSyncsStore: DiskAdapter.Sync is the point go-quicraft
// relies on for the state machine's durability, so it must reach the store's
// Syncer and report its failure (#1618).
func TestDiskAdapter_SyncSyncsStore(t *testing.T) {
	st := &syncCountingStore{}
	a := &DiskAdapter{store: st}
	if err := a.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if st.calls != 1 {
		t.Fatalf("store Sync calls = %d, want 1", st.calls)
	}

	boom := errors.New("fsync failed")
	failing := &syncCountingStore{err: boom}
	a = &DiskAdapter{store: failing}
	err := a.Sync()
	if !errors.Is(err, boom) {
		t.Fatalf("Sync with failing store = %v, want it to wrap %v", err, boom)
	}
	if failing.calls != 1 {
		t.Fatalf("failing store Sync calls = %d, want 1", failing.calls)
	}
}

// TestDiskAdapter_SyncWithoutSyncer: a store with nothing to sync (no
// storage.Syncer) makes Sync a no-op.
func TestDiskAdapter_SyncWithoutSyncer(t *testing.T) {
	a := &DiskAdapter{store: struct{ storage.Storage }{}}
	if err := a.Sync(); err != nil {
		t.Fatalf("Sync without Syncer = %v, want nil", err)
	}
}

// TestDiskAdapter_SyncPersistsAppliedIndex: over a Pebble store with
// SyncWrites off, the applied index survives Sync and a crash that drops
// every unsynced write; without Sync it does not.
func TestDiskAdapter_SyncPersistsAppliedIndex(t *testing.T) {
	for _, synced := range []bool{true, false} {
		fs := vfs.NewCrashableMem()
		st := openCrashableStore(t, fs)
		ctx := context.Background()
		a := &DiskAdapter{KVStateMachine: NewKVStateMachine(NewDiskBackend(st)), store: st}
		entries := []sm.Entry{kvPutEntry(5, "k", "v")}
		if err := a.Update(ctx, entries, make([]sm.Result, 1)); err != nil {
			t.Fatalf("Update: %v", err)
		}
		if synced {
			if err := a.Sync(); err != nil {
				t.Fatalf("Sync: %v", err)
			}
		}
		crashed := fs.CrashClone(vfs.CrashCloneCfg{})
		_ = st.Close()

		st2 := openCrashableStore(t, crashed)
		a2 := &DiskAdapter{KVStateMachine: NewKVStateMachine(NewDiskBackend(st2)), store: st2}
		idx, err := a2.Open(ctx, "", nil)
		_ = st2.Close()
		if err != nil {
			t.Fatalf("Open after crash (synced=%v): %v", synced, err)
		}
		// Without Sync the crash drops the unsynced write: that proves the
		// filesystem double, so the synced case sees only what Sync made
		// durable.
		want := uint64(0)
		if synced {
			want = 5
		}
		if idx != want {
			t.Fatalf("applied index after crash (synced=%v) = %d, want %d", synced, idx, want)
		}
	}
}

// TestDiskAdapter_SyncReturnsPebbleError: a real Pebble Sync failure (here a
// closed store) reaches the caller of DiskAdapter.Sync.
func TestDiskAdapter_SyncReturnsPebbleError(t *testing.T) {
	st := openCrashableStore(t, vfs.NewCrashableMem())
	if err := st.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	a := &DiskAdapter{store: st}
	err := a.Sync()
	var closed *storage.ClosedError
	if !errors.As(err, &closed) {
		t.Fatalf("Sync on a closed Pebble store = %v, want a wrapped *storage.ClosedError", err)
	}
}

// openCrashableStore opens a Pebble store with SyncWrites off over fs.
func openCrashableStore(t *testing.T, fs vfs.FS) *pebblestore.PebbleStorage {
	t.Helper()
	cfg := pebblestore.TestConfig("/db")
	cfg.SyncWrites = false
	cfg.FS = fs
	st, err := pebblestore.New(cfg)
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	return st
}
