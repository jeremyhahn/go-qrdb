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
				s, err := pebble.New(&pebble.Config{DataDir: t.TempDir()})
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
