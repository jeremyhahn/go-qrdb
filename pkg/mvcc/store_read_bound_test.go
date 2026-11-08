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

package mvcc

import (
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
)

// newCorruptibleStore returns a store over a memory storage the test can
// write to directly, seeded with keys a, b and c each holding a 10-byte value.
func newCorruptibleStore(t *testing.T) (*Store, storage.Storage) {
	t.Helper()
	mem := memory.NewStorage()
	t.Cleanup(func() { _ = mem.Close() })
	s, err := New(mem)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, err := s.Put([]byte(k), []byte("0123456789"), 0); err != nil {
			t.Fatalf("seed Put %s: %v", k, err)
		}
	}
	return s, mem
}

func TestRangeFrom_MaxResultBytesStopsTheScan(t *testing.T) {
	s, mem := newCorruptibleStore(t)
	// Each key answers 11 bytes; the second crosses a 15-byte bound.
	_, _, _, err := s.RangeFrom(mem, s.CurrentRevision(), []byte("a"), []byte("z"), service.WithMaxResultBytes(15))
	var tooLarge *service.RangeResultTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 15 || tooLarge.Read != 22 {
		t.Fatalf("RangeFrom = %v, want a RangeResultTooLargeError at 22 of 15 bytes", err)
	}
	if tooLarge.Error() == "" {
		t.Fatal("empty error message")
	}
}

func TestRangeFrom_MaxResultBytesAdmitsWhatFits(t *testing.T) {
	s, mem := newCorruptibleStore(t)
	kvs, count, _, err := s.RangeFrom(mem, s.CurrentRevision(), []byte("a"), []byte("z"), service.WithMaxResultBytes(33))
	if err != nil || count != 3 || len(kvs) != 3 {
		t.Fatalf("RangeFrom = (%d kvs, count %d, %v), want all three keys within 33 bytes", len(kvs), count, err)
	}
	// Keys-only answers count only the keys.
	if _, _, _, err := s.RangeFrom(mem, s.CurrentRevision(), []byte("a"), []byte("z"), service.WithMaxResultBytes(3), service.WithKeysOnly()); err != nil {
		t.Fatalf("keys-only RangeFrom = %v, want three one-byte keys within 3 bytes", err)
	}
}

func TestRangeFrom_MaxResultBytesBoundsASingleKeyGet(t *testing.T) {
	s, mem := newCorruptibleStore(t)
	var tooLarge *service.RangeResultTooLargeError
	if _, _, _, err := s.RangeFrom(mem, s.CurrentRevision(), []byte("a"), nil, service.WithMaxResultBytes(10)); !errors.As(err, &tooLarge) || tooLarge.Read != 11 {
		t.Fatalf("get over the bound = %v, want a RangeResultTooLargeError at 11 bytes", err)
	}
	if kvs, _, _, err := s.RangeFrom(mem, s.CurrentRevision(), []byte("a"), nil, service.WithMaxResultBytes(11)); err != nil || len(kvs) != 1 {
		t.Fatalf("get at the bound = (%d kvs, %v), want the key", len(kvs), err)
	}
}
