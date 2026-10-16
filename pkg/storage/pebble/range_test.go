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

package pebble

import (
	"errors"
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// seedRangeKeys writes each key with its own name as the value.
func seedRangeKeys(t *testing.T, s *PebbleStorage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := s.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
}

// storedKeys returns every committed key in order.
func storedKeys(t *testing.T, s *PebbleStorage) []string {
	t.Helper()
	keys, err := s.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = string(k)
	}
	return out
}

// rangeDeleters runs one range delete through the store and through a
// committed batch, so both paths are held to one contract.
var rangeDeleters = map[string]func(s *PebbleStorage, start, end []byte) error{
	"storage": func(s *PebbleStorage, start, end []byte) error { return s.DeleteRange(start, end) },
	"batch": func(s *PebbleStorage, start, end []byte) error {
		b := s.Batch()
		defer func() { _ = b.Close() }()
		if err := b.DeleteRange(start, end); err != nil {
			return err
		}
		return b.Commit()
	},
}

// TestDeleteRange pins [start, end) with an empty end reaching the end of the
// keyspace. The unbounded case carries keys after start that do not share its
// bytes as a prefix: "z" must go too, not only "m" and "ma".
func TestDeleteRange(t *testing.T) {
	cases := map[string]struct {
		start, end []byte
		want       []string
	}{
		"bounded keeps its exclusive end":  {[]byte("m"), []byte("z"), []string{"a", "z"}},
		"nil end reaches the keyspace end": {[]byte("m"), nil, []string{"a"}},
		"nil end from between keys":        {[]byte("b"), nil, []string{"a"}},
		"nil end past every key":           {[]byte("zz"), nil, []string{"a", "m", "ma", "z"}},
		"empty range is a no-op":           {[]byte("n"), []byte("y"), []string{"a", "m", "ma", "z"}},
	}
	for name, del := range rangeDeleters {
		for caseName, tc := range cases {
			t.Run(name+"/"+caseName, func(t *testing.T) {
				for _, sync := range []bool{false, true} {
					cfg := TestConfig(t.TempDir())
					cfg.SyncWrites = sync
					s, err := New(cfg)
					if err != nil {
						t.Fatalf("New: %v", err)
					}
					seedRangeKeys(t, s, "a", "m", "ma", "z")
					if err := del(s, tc.start, tc.end); err != nil {
						t.Fatalf("DeleteRange (sync=%v): %v", sync, err)
					}
					if got := storedKeys(t, s); !reflect.DeepEqual(got, tc.want) {
						t.Fatalf("keys (sync=%v) = %v, want %v", sync, got, tc.want)
					}
					_ = s.Close()
				}
			})
		}
	}
}

// TestBatch_DeleteRangeUnboundedSeesPendingWrites checks an unbounded range
// delete in a batch covers the batch's own pending writes past the last
// committed key, and still leaves a write the batch makes after it.
func TestBatch_DeleteRangeUnboundedSeesPendingWrites(t *testing.T) {
	s := openTestStorage(t)
	seedRangeKeys(t, s, "a", "m")

	b := s.Batch()
	defer func() { _ = b.Close() }()
	for _, step := range []func() error{
		func() error { return b.Set([]byte("zz"), []byte("pending")) },
		func() error { return b.DeleteRange([]byte("m"), nil) },
		func() error { return b.Set([]byte("q"), []byte("later")) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := b.Get([]byte("zz")); err != nil || v != nil {
		t.Fatalf("batch Get(zz) = %q, %v, want the pending write deleted", v, err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := storedKeys(t, s); !reflect.DeepEqual(got, []string{"a", "q"}) {
		t.Fatalf("keys = %v, want [a q]", got)
	}
}

func TestDeleteRange_Refusals(t *testing.T) {
	s := openTestStorage(t)
	seedRangeKeys(t, s, "a", "b")

	if err := s.DeleteRange([]byte("b"), []byte("a")); !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatalf("DeleteRange(b, a) = %v, want ErrInvalidRange", err)
	}
	if err := s.DeleteRange(nil, []byte("b")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("DeleteRange(nil, b) = %v, want ErrEmptyKey", err)
	}

	b := s.Batch()
	err := b.DeleteRange([]byte("b"), []byte("a"))
	var batchErr *storage.BatchError
	if !errors.As(err, &batchErr) || batchErr.Op != "delete_range" || !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatalf("batch DeleteRange(b, a) = %v, want a delete_range BatchError over ErrInvalidRange", err)
	}
	_ = b.Close()
	if err := b.DeleteRange([]byte("a"), nil); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("DeleteRange on a closed batch = %v, want ErrStorageClosed", err)
	}
	if got := storedKeys(t, s); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("a refused range delete removed keys: %v", got)
	}
}

// TestDeleteRange_ClosedStore covers the store closing under an open batch:
// an unbounded range delete has to read the database to bound itself, so it
// refuses, while a bounded one is buffered and refused only at Commit.
func TestDeleteRange_ClosedStore(t *testing.T) {
	s, err := New(TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b := s.Batch()
	defer func() { _ = b.Close() }()
	_ = s.Close()

	if err := s.DeleteRange([]byte("a"), nil); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("DeleteRange on a closed store = %v, want ErrStorageClosed", err)
	}
	err = b.DeleteRange([]byte("a"), nil)
	var batchErr *storage.BatchError
	if !errors.As(err, &batchErr) || batchErr.Op != "delete_range" || !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("unbounded batch DeleteRange on a closed store = %v, want a delete_range BatchError over ErrStorageClosed", err)
	}
	if err := b.DeleteRange([]byte("a"), []byte("b")); err != nil {
		t.Fatalf("bounded batch DeleteRange on a closed store = %v, want it buffered", err)
	}
	if err := b.Commit(); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("Commit on a closed store = %v, want ErrStorageClosed", err)
	}
}
