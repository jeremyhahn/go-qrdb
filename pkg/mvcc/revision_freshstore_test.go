// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mvcc

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// The regression these tests exist for: newRevisionManager recognised an
// absent revision key ONLY through errors.IsNotFound, but storage.Reader.Get
// is documented as returning (nil, nil) for a missing key and every shipped
// engine does exactly that. The IsNotFound arm never fired, execution fell
// through, and DecodeRevision(nil) rejected the empty slice — so
// NewRaftStorage could not be constructed over a fresh store with ANY engine,
// which is the state of every new deployment.
//
// It survived because this package's own test double answered a missing key
// with an ErrNotFound error, unlike every engine it stood in for. These tests
// therefore drive the REAL engines rather than the double; that is the whole
// point, and replacing them with the double would restore the blind spot.

// engineFixture names a storage engine and builds a fresh, empty instance.
type engineFixture struct {
	name string
	open func(t *testing.T) storage.Storage
}

func freshEngines() []engineFixture {
	return []engineFixture{
		{
			name: "memory",
			open: func(t *testing.T) storage.Storage {
				t.Helper()
				return memory.NewStorage()
			},
		},
		{
			name: "file",
			open: func(t *testing.T) storage.Storage {
				t.Helper()
				s, err := file.New(t.TempDir())
				if err != nil {
					t.Fatalf("file.New: %v", err)
				}
				t.Cleanup(func() { _ = s.Close() })
				return s
			},
		},
		{
			name: "pebble",
			open: func(t *testing.T) storage.Storage {
				t.Helper()
				// CacheSize and MemTableSize are set explicitly and small.
				// Config.WithDefaults would otherwise give each instance a
				// 128 MB block cache and a 64 MB memtable, and these fixtures
				// open one per subtest. `go test ./pkg/... ./cmd/...` runs
				// package binaries concurrently, so that much per-instance
				// memory does not stay inside this package: it competes with
				// whatever else is running and can get an unrelated package's
				// binary OOM-killed. These tests store one 8-byte revision;
				// they need no cache at all.
				s, err := pebble.New(&pebble.Config{
					DataDir:      t.TempDir(),
					CacheSize:    1 << 20, // 1 MB
					MemTableSize: 1 << 20, // 1 MB
				})
				if err != nil {
					t.Fatalf("pebble.New: %v", err)
				}
				t.Cleanup(func() { _ = s.Close() })
				return s
			},
		},
	}
}

// TestEngines_GetAbsentKeyReturnsNilNil pins the contract the fix depends on.
// If an engine ever starts reporting absence as an error instead, this fails
// here rather than as a confusing construction failure downstream.
func TestEngines_GetAbsentKeyReturnsNilNil(t *testing.T) {
	for _, e := range freshEngines() {
		t.Run(e.name, func(t *testing.T) {
			data, err := e.open(t).Get([]byte("key-that-was-never-written"))
			if err != nil {
				t.Fatalf("Get(absent): expected nil error per storage.Reader.Get, got %v", err)
			}
			if len(data) != 0 {
				t.Fatalf("Get(absent): expected empty data, got %d bytes", len(data))
			}
		})
	}
}

// TestNewRaftStorage_FreshStore is the regression proper: construction over an
// empty store must succeed on every engine and seed revision 1.
func TestNewRaftStorage_FreshStore(t *testing.T) {
	for _, e := range freshEngines() {
		t.Run(e.name, func(t *testing.T) {
			s, err := NewRaftStorage(&RaftStorageConfig{
				Store:  e.open(t),
				Logger: newTestLogger(t),
			})
			if err != nil {
				t.Fatalf("NewRaftStorage over a fresh %s store: %v", e.name, err)
			}
			if got := s.RaftCurrentRevision(); got != 1 {
				t.Fatalf("fresh store: revision = %d, want 1", got)
			}
		})
	}
}

// TestNewRaftStorage_FreshStorePersistsRevision checks that the seeded
// revision is written, not merely held in memory: a second manager over the
// same store must load it through the decode path rather than re-seeding.
func TestNewRaftStorage_FreshStorePersistsRevision(t *testing.T) {
	for _, e := range freshEngines() {
		t.Run(e.name, func(t *testing.T) {
			store := e.open(t)

			first, err := newRevisionManager(store)
			if err != nil {
				t.Fatalf("first newRevisionManager: %v", err)
			}
			for i := 0; i < 5; i++ {
				first.next()
			}
			if err := first.persist(); err != nil {
				t.Fatalf("persist: %v", err)
			}
			want := first.getCurrent()

			second, err := newRevisionManager(store)
			if err != nil {
				t.Fatalf("second newRevisionManager over a seeded store: %v", err)
			}
			if got := second.getCurrent(); got != want {
				t.Fatalf("reload: revision = %d, want %d", got, want)
			}
		})
	}
}

// notFoundStore reports an absent key as a typed ErrNotFound rather than as
// (nil, nil). No shipped engine does this, but a decorator may, and the fix
// keeps that spelling meaning "fresh" rather than "corrupt".
type notFoundStore struct {
	storage.Storage
}

func (n notFoundStore) Get(key []byte) ([]byte, error) {
	v, err := n.Storage.Get(key)
	if err == nil && len(v) == 0 {
		return nil, errors.New(errors.ErrNotFound, "notFoundStore.Get", "key not found")
	}
	return v, err
}

func TestNewRaftStorage_FreshStoreWithNotFoundError(t *testing.T) {
	rm, err := newRevisionManager(notFoundStore{Storage: memory.NewStorage()})
	if err != nil {
		t.Fatalf("newRevisionManager over a store that errors on absence: %v", err)
	}
	if got := rm.getCurrent(); got != 1 {
		t.Fatalf("revision = %d, want 1", got)
	}
}

// TestNewRevisionManager_CorruptRevisionStillRejected guards the direction the
// fix must NOT loosen: a present-but-malformed value is corruption and has to
// keep failing rather than being silently reseeded to 1.
func TestNewRevisionManager_CorruptRevisionStillRejected(t *testing.T) {
	store := memory.NewStorage()
	if err := store.Set(RevisionKey(), []byte("not-eight")); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if _, err := newRevisionManager(store); err == nil {
		t.Fatal("expected a decode error for a malformed revision value, got nil")
	}
}

// Construction succeeding is not the same as the package being usable.
//
// go-qrdb#24's second half: after NewRaftStorage was fixed, the very first Put
// failed over a real engine for the identical reason — it read any existing
// metadata, got the documented (nil, nil) for a key that does not exist yet,
// and decoded it:
//
//	Put: mvcc.Put: storage: failed to decode metadata: mvcc: metadata must be 32 bytes
//
// Get, Delete and Range shared the shape. This drives a full write/read/delete
// cycle against the REAL engines, which is the only thing that would have
// caught either half; the package's own double reported absence as an error and
// so exercised a path production never takes.
func TestRaftStorage_ReadWriteCycleOverRealEngines(t *testing.T) {
	for _, e := range freshEngines() {
		t.Run(e.name, func(t *testing.T) {
			s, err := NewRaftStorage(&RaftStorageConfig{
				Store:  e.open(t),
				Logger: newTestLogger(t),
			})
			if err != nil {
				t.Fatalf("NewRaftStorage over a fresh %s store: %v", e.name, err)
			}
			ctx := t.Context()

			// A key that has never been written must read as NotFound, not as
			// a decode failure.
			if _, err := s.Get(ctx, []byte("absent"), 0); !errors.IsNotFound(err) {
				t.Fatalf("Get(absent) = %v, want a NotFound error", err)
			}

			// First Put: the metadata read underneath it returns (nil, nil).
			if _, _, err := s.Put(ctx, []byte("k"), []byte("v1")); err != nil {
				t.Fatalf("first Put over a fresh %s store: %v", e.name, err)
			}
			kv, err := s.Get(ctx, []byte("k"), 0)
			if err != nil {
				t.Fatalf("Get after Put: %v", err)
			}
			if string(kv.Value) != "v1" {
				t.Errorf("value = %q, want %q", kv.Value, "v1")
			}

			// Second Put: now the metadata read returns real bytes, so this
			// exercises the other arm of the same branch.
			if _, _, err := s.Put(ctx, []byte("k"), []byte("v2")); err != nil {
				t.Fatalf("second Put: %v", err)
			}
			kv, err = s.Get(ctx, []byte("k"), 0)
			if err != nil {
				t.Fatalf("Get after second Put: %v", err)
			}
			if string(kv.Value) != "v2" {
				t.Errorf("value = %q, want %q", kv.Value, "v2")
			}
			if kv.Version != 2 {
				t.Errorf("version = %d, want 2 — the second Put must UPDATE, not re-create", kv.Version)
			}

			// Delete of a present key, then of an absent one.
			if _, _, err := s.Delete(ctx, []byte("k")); err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := s.Get(ctx, []byte("k"), 0); !errors.IsNotFound(err) {
				t.Fatalf("Get after Delete = %v, want a NotFound error", err)
			}
			if _, _, err := s.Delete(ctx, []byte("never-existed")); err != nil {
				t.Fatalf("Delete of an absent key must be a no-op, got: %v", err)
			}
		})
	}
}

// A key holding an EMPTY value is present, and must not read as missing.
//
// Scope note, because it would be easy to claim more for this test than it
// delivers: it does NOT distinguish the two candidate spellings of absence.
// Every guard on this path reads a METADATA key, and metadata is a fixed 32
// bytes, so it is never zero-length and len(data) == 0 and data == nil agree
// about it. Mutating absent() to len(data) == 0 leaves this test passing —
// measured, not assumed.
//
// What it does cover is that pkg/mvcc handles an empty value end to end over
// the real engines at all, which is worth pinning on its own: the empty value
// lives at the DATA key while presence is decided from the metadata key, and a
// future change that decided presence from the data key would report a
// legitimately empty value as missing.
//
// TestNewRevisionManager_AStoredEmptyRevisionIsCorruptionNotAFreshStore is the
// test that actually decides the predicate.
func TestRaftStorage_AnEmptyValueIsPresentNotMissing(t *testing.T) {
	for _, e := range freshEngines() {
		t.Run(e.name, func(t *testing.T) {
			store := e.open(t)

			// First, pin the property this rests on at the engine itself, so a
			// failure here names the engine rather than looking like an mvcc
			// bug.
			if data, err := store.Get([]byte("nothing")); err != nil || data != nil {
				t.Fatalf("%s Get(absent) = (%v, %v), want (nil, nil)", e.name, data, err)
			}

			s, err := NewRaftStorage(&RaftStorageConfig{
				Store:  store,
				Logger: newTestLogger(t),
			})
			if err != nil {
				t.Fatalf("NewRaftStorage: %v", err)
			}
			ctx := t.Context()

			if _, _, err := s.Put(ctx, []byte("empty"), []byte{}); err != nil {
				t.Fatalf("Put of an empty value: %v", err)
			}

			kv, err := s.Get(ctx, []byte("empty"), 0)
			if err != nil {
				t.Fatalf("Get of a key holding an empty value = %v; an empty value is "+
					"PRESENT, and reporting it as missing is a silent wrong answer", err)
			}
			if len(kv.Value) != 0 {
				t.Errorf("value = %q, want empty", kv.Value)
			}
			if kv.Version != 1 {
				t.Errorf("version = %d, want 1", kv.Version)
			}

			// And overwriting it with a non-empty value must UPDATE rather
			// than create, which is what proves the metadata was really there.
			if _, _, err := s.Put(ctx, []byte("empty"), []byte("now-set")); err != nil {
				t.Fatalf("Put over an empty value: %v", err)
			}
			kv, err = s.Get(ctx, []byte("empty"), 0)
			if err != nil {
				t.Fatalf("Get after overwrite: %v", err)
			}
			if kv.Version != 2 {
				t.Errorf("version = %d, want 2 — the empty value was treated as absent", kv.Version)
			}
		})
	}
}

// A revision record that was STORED but is zero-length is corruption, and must
// be reported as such rather than silently restarting the store at revision 1.
//
// This is the case that decides between the two candidate spellings of absence,
// and it is the reason absent() tests data == nil rather than len(data) == 0.
// Both agree about a key that was never written. They disagree here, and the
// disagreement is not cosmetic:
//
//   - data == nil  -> not absent, so the value reaches DecodeRevision, which
//     rejects it: the store refuses to open and an operator sees the
//     corruption.
//   - len(data)==0 -> "fresh store", so the revision counter silently RESETS TO
//     1 over a store that already holds data at higher revisions. Every
//     subsequent write reuses revisions that are already taken, and MVCC
//     history is corrupted with no error anywhere.
//
// The second is the worse failure by a wide margin — a loud refusal to open
// versus silent history corruption — which is why the predicate is worth
// pinning with a test rather than leaving to taste.
func TestNewRevisionManager_AStoredEmptyRevisionIsCorruptionNotAFreshStore(t *testing.T) {
	for _, e := range freshEngines() {
		t.Run(e.name, func(t *testing.T) {
			store := e.open(t)

			// A zero-length value at the revision key: present, but undecodable.
			if err := store.Set(RevisionKey(), []byte{}); err != nil {
				t.Fatalf("seeding an empty revision record: %v", err)
			}

			// Confirm the engine really distinguishes this from absence,
			// otherwise the test proves nothing about the predicate.
			data, err := store.Get(RevisionKey())
			if err != nil {
				t.Fatalf("Get(revision key): %v", err)
			}
			if data == nil {
				t.Skipf("%s returns a nil slice for a stored empty value, so it "+
					"cannot distinguish this case; the predicate is untestable here", e.name)
			}
			if len(data) != 0 {
				t.Fatalf("seeded value read back as %d bytes, want 0", len(data))
			}

			if _, err := newRevisionManager(store); err == nil {
				t.Fatal("newRevisionManager accepted a zero-length revision record and " +
					"started fresh; that silently resets the revision counter to 1 over a " +
					"store which may already hold higher revisions")
			}
		})
	}
}
