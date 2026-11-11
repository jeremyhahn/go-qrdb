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

package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// casEngine is a backend that is both a Storage and a GuardedStorage.
type casEngine interface {
	storage.Storage
	storage.GuardedStorage
}

func casStore(t *testing.T) casEngine {
	t.Helper()
	return memory.NewStorage()
}

func TestCompareAndSwap_Success(t *testing.T) {
	s := casStore(t)
	if err := s.Set([]byte("k"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	swapped, err := s.CompareAndSwap(context.Background(), []byte("k"), []byte("a"), []byte("b"))
	if err != nil || !swapped {
		t.Fatalf("CompareAndSwap = %v, %v; want true, nil", swapped, err)
	}
	got, err := s.Get([]byte("k"))
	if err != nil || string(got) != "b" {
		t.Fatalf("Get = %q, %v; want b", got, err)
	}
}

func TestCompareAndSwap_MismatchLeavesValue(t *testing.T) {
	s := casStore(t)
	if err := s.Set([]byte("k"), []byte("a")); err != nil {
		t.Fatal(err)
	}
	swapped, err := s.CompareAndSwap(context.Background(), []byte("k"), []byte("x"), []byte("b"))
	if err != nil || swapped {
		t.Fatalf("CompareAndSwap = %v, %v; want false, nil", swapped, err)
	}
	if got, _ := s.Get([]byte("k")); string(got) != "a" {
		t.Fatalf("value = %q, want a", got)
	}
}

func TestCompareAndSwap_AbsentKeyIsNotCreated(t *testing.T) {
	s := casStore(t)
	swapped, err := s.CompareAndSwap(context.Background(), []byte("k"), nil, []byte("b"))
	if err != nil || swapped {
		t.Fatalf("CompareAndSwap = %v, %v; want false, nil", swapped, err)
	}
	if got, _ := s.Get([]byte("k")); got != nil {
		t.Fatalf("absent key was created: %q", got)
	}
}

func TestCompareAndSwap_ArgumentErrors(t *testing.T) {
	s := casStore(t)
	if _, err := s.CompareAndSwap(context.Background(), nil, nil, nil); !errors.Is(err, storage.ErrEmptyKey) {
		t.Errorf("empty key = %v, want ErrEmptyKey", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CompareAndSwap(ctx, []byte("k"), nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestCompareAndSwap_ClosedStore(t *testing.T) {
	s := casStore(t)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompareAndSwap(context.Background(), []byte("k"), nil, nil); !errors.Is(err, storage.ErrStorageClosed) {
		t.Errorf("closed store = %v, want ErrStorageClosed", err)
	}
}

// SeekToLast with an upper bound above every key lands on the last key; with a
// lower bound above it the iterator is empty.
// A batch committed after its store closed refuses with the closed error.
func TestBatchCommit_AfterClose(t *testing.T) {
	s := memory.NewStorage()
	b := s.Batch()
	if err := b.Set([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Commit(); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("Commit = %v, want ErrStorageClosed", err)
	}
}
