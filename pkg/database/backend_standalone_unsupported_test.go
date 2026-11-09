// Copyright (c) 2026 Jeremy Hahn
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

package database

import (
	"context"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// unsupportedExclusiveStore is a store whose type implements storage.Exclusive
// but which, like an object store on a provider that cannot exclude a second
// opener, refuses it before running fn.
type unsupportedExclusiveStore struct {
	storage.Storage
	calls int
}

func (s *unsupportedExclusiveStore) Exclusive(func(storage.ReadWriter) error) error {
	s.calls++
	return &storage.ExclusiveUnsupportedError{Backend: "test"}
}

// TestStandalone_GuardedVerbsFallBackWhenExclusiveIsUnsupported checks that a
// guarded verb on a store whose Exclusive is refused still runs, under the
// backend's own writeMu, rather than failing with the refusal.
func TestStandalone_GuardedVerbsFallBackWhenExclusiveIsUnsupported(t *testing.T) {
	ctx := context.Background()
	store := &unsupportedExclusiveStore{Storage: memory.NewStorage()}
	b := NewStandaloneBackend(store)

	created, err := b.PutIfAbsent(ctx, []byte("k"), []byte("v1"))
	if err != nil || !created {
		t.Fatalf("first PutIfAbsent = %v, %v; want true, nil", created, err)
	}
	created, err = b.PutIfAbsent(ctx, []byte("k"), []byte("v2"))
	if err != nil || created {
		t.Fatalf("second PutIfAbsent = %v, %v; want false, nil", created, err)
	}
	if store.calls != 2 {
		t.Fatalf("Exclusive was asked %d times, want 2", store.calls)
	}
	v, err := store.Get([]byte("k"))
	if err != nil || string(v) != "v1" {
		t.Fatalf("Get = %q, %v; want v1", v, err)
	}
}

// guardedStore is an unsupportedExclusiveStore that also offers
// storage.Guarded, as an object store on such a provider does.
type guardedStore struct {
	unsupportedExclusiveStore
	guarded int
}

func (s *guardedStore) Guarded(fn func(storage.ReadWriter) error) error {
	s.guarded++
	return fn(s.Storage)
}

// TestStandalone_GuardedVerbsUseGuardedWhenExclusiveIsUnsupported checks that
// the fallback runs a guarded verb under the store's storage.Guarded when the
// store has one, so an object store still settles a half-applied batch before
// the verb compares.
func TestStandalone_GuardedVerbsUseGuardedWhenExclusiveIsUnsupported(t *testing.T) {
	ctx := context.Background()
	store := &guardedStore{unsupportedExclusiveStore: unsupportedExclusiveStore{Storage: memory.NewStorage()}}
	b := NewStandaloneBackend(store)

	created, err := b.PutIfAbsent(ctx, []byte("k"), []byte("v1"))
	if err != nil || !created {
		t.Fatalf("PutIfAbsent = %v, %v; want true, nil", created, err)
	}
	if store.calls != 1 || store.guarded != 1 {
		t.Fatalf("Exclusive asked %d times and Guarded %d, want 1 and 1", store.calls, store.guarded)
	}
}
