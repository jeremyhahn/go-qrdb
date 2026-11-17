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
	"errors"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/jeremyhahn/go-quicraft/pkg/sm"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	memstore "github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebblestore "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// syncingStore is a memory store that also implements storage.Syncer, so a
// test can see whether DiskAdapter.Sync reached the store and what the
// store's sync returned.
type syncingStore struct {
	*memstore.MemoryStorage
	syncs int
	err   error
}

var _ storage.Syncer = (*syncingStore)(nil)

func (s *syncingStore) Sync() error {
	s.syncs++
	return s.err
}

// TestDiskAdapter_SyncReachesStoreSync pins that DiskAdapter.Sync makes the
// store durable: it used to return nil without touching the store, so the
// Raft engine compacted its log past state that was only in a NoSync
// store's memory.
func TestDiskAdapter_SyncReachesStoreSync(t *testing.T) {
	st := &syncingStore{MemoryStorage: memstore.NewStorage()}
	a := newDiskAdapter(st)
	if err := a.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if st.syncs != 1 {
		t.Fatalf("store syncs = %d, want 1", st.syncs)
	}
}

// TestDiskAdapter_SyncReturnsStoreError pins that a failed store sync fails
// DiskAdapter.Sync, so the Raft engine does not compact its log over state
// the store could not make durable.
func TestDiskAdapter_SyncReturnsStoreError(t *testing.T) {
	want := errors.New("fsync: input/output error")
	st := &syncingStore{MemoryStorage: memstore.NewStorage(), err: want}
	a := newDiskAdapter(st)
	err := a.Sync()
	if !errors.Is(err, want) {
		t.Fatalf("Sync error = %v, want %v", err, want)
	}
	var serr *StorageError
	if !errors.As(err, &serr) || serr.Op != "sync" {
		t.Fatalf("Sync error = %#v, want a *StorageError with Op sync", err)
	}
}

// TestDiskAdapter_LastAppliedSurvivesCrashAfterSync opens the disk state
// machine's Pebble store, as the registry does, on a crashable in-memory
// filesystem. Entries 1 and 2 are applied and synced, entry 3 is applied but
// not synced, then the filesystem crashes, dropping every byte that was not
// synced. The reopened state machine must resume from the last-applied
// index (diskMetaKeyLastApplied) and data Sync made durable, and must not
// see entry 3, which shows the crash really drops unsynced writes.
func TestDiskAdapter_LastAppliedSurvivesCrashAfterSync(t *testing.T) {
	ctx := context.Background()
	mem := vfs.NewCrashableMem()

	open := func(fs vfs.FS) *DiskAdapter {
		cfg := pebblestore.TestConfig("/data")
		cfg.FS = fs
		cfg.SyncWrites = false
		st, err := pebblestore.New(cfg)
		if err != nil {
			t.Fatalf("open storage: %v", err)
		}
		return newDiskAdapter(st)
	}

	a := open(mem)
	if _, err := a.Open(ctx, "", nil); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	synced := []sm.Entry{kvPutEntry(1, "a", "1"), kvPutEntry(2, "b", "2")}
	if err := a.Update(ctx, synced, make([]sm.Result, len(synced))); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if err := a.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	unsynced := []sm.Entry{kvPutEntry(3, "c", "3")}
	if err := a.Update(ctx, unsynced, make([]sm.Result, 1)); err != nil {
		t.Fatalf("Update after Sync: %v", err)
	}

	crashed := mem.CrashClone(vfs.CrashCloneCfg{})
	t.Cleanup(func() { _ = a.Close(ctx) })

	a2 := open(crashed)
	defer func() { _ = a2.Close(ctx) }()
	idx, err := a2.Open(ctx, "", nil)
	if err != nil {
		t.Fatalf("Open after crash: %v", err)
	}
	if idx != 2 {
		t.Fatalf("Open after crash = %d, want 2 (the index Sync made durable)", idx)
	}
	val, err := a2.BytesLookup([]byte("b"))
	if err != nil {
		t.Fatalf("BytesLookup after crash: %v", err)
	}
	if string(val) != "2" {
		t.Fatalf("value after crash = %q, want %q", val, "2")
	}
}
