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

package memory

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// seedKeys writes each key with its own name as the value.
func seedKeys(t *testing.T, s *MemoryStorage, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if err := s.Set([]byte(k), []byte(k)); err != nil {
			t.Fatalf("Set(%q): %v", k, err)
		}
	}
}

// listKeys returns every committed key in order.
func listKeys(t *testing.T, s *MemoryStorage) []string {
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

func TestDeleteRange(t *testing.T) {
	cases := map[string]struct {
		start, end []byte
		want       []string
	}{
		"bounded keeps its exclusive end":  {[]byte("b"), []byte("d"), []string{"a", "d", "e"}},
		"nil end reaches the keyspace end": {[]byte("c"), nil, []string{"a", "b", "bb"}},
		"start between keys":               {[]byte("ba"), []byte("c"), []string{"a", "b", "c", "d", "e"}},
		"empty range is a no-op":           {[]byte("x"), []byte("y"), []string{"a", "b", "bb", "c", "d", "e"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := openTestStorage(t)
			seedKeys(t, s, "a", "b", "bb", "c", "d", "e")
			if err := s.DeleteRange(tc.start, tc.end); err != nil {
				t.Fatalf("DeleteRange: %v", err)
			}
			if got := listKeys(t, s); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("keys = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDeleteRange_RefusesAnInvalidRange(t *testing.T) {
	s := openTestStorage(t)
	seedKeys(t, s, "a", "b")

	if err := s.DeleteRange([]byte("b"), []byte("a")); !errors.Is(err, storage.ErrInvalidRange) {
		t.Fatalf("DeleteRange(b, a) = %v, want ErrInvalidRange", err)
	}
	if err := s.DeleteRange(nil, []byte("b")); !errors.Is(err, storage.ErrEmptyKey) {
		t.Fatalf("DeleteRange(nil, b) = %v, want ErrEmptyKey", err)
	}
	if got := listKeys(t, s); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("a refused range delete removed keys: %v", got)
	}
}

func TestDeleteRange_Closed(t *testing.T) {
	s := NewStorage()
	_ = s.Close()
	if err := s.DeleteRange([]byte("a"), []byte("b")); !errors.Is(err, storage.ErrStorageClosed) {
		t.Fatalf("DeleteRange after Close = %v, want ErrStorageClosed", err)
	}
}

// TestBatch_DeleteRangeAppliesInOrder checks a committed batch applies its
// range delete between its neighbours: it removes a key the batch wrote
// earlier and leaves one the batch wrote later.
func TestBatch_DeleteRangeAppliesInOrder(t *testing.T) {
	s := openTestStorage(t)
	seedKeys(t, s, "a", "b", "c", "d")

	b := s.Batch()
	defer func() { _ = b.Close() }()
	for _, step := range []func() error{
		func() error { return b.Set([]byte("bb"), []byte("earlier")) },
		func() error { return b.DeleteRange([]byte("b"), []byte("d")) },
		func() error { return b.Set([]byte("c"), []byte("later")) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if got := listKeys(t, s); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("an uncommitted batch changed storage: %v", got)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if got := listKeys(t, s); !reflect.DeepEqual(got, []string{"a", "c", "d"}) {
		t.Fatalf("keys = %v, want [a c d]", got)
	}
	if v, err := s.Get([]byte("c")); err != nil || string(v) != "later" {
		t.Fatalf("Get(c) = %q, %v, want the write made after the range delete", v, err)
	}
}

func TestBatch_DeleteRangeUnbounded(t *testing.T) {
	s := openTestStorage(t)
	seedKeys(t, s, "a", "m", "z")

	b := s.Batch()
	defer func() { _ = b.Close() }()
	if err := b.DeleteRange([]byte("m"), nil); err != nil {
		t.Fatalf("DeleteRange: %v", err)
	}
	if err := b.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got := listKeys(t, s); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("keys = %v, want [a]", got)
	}
}

// TestBatch_CommitInterleavedKeyFamiliesMatchesModel commits batches whose
// writes hop between key families with different leading bytes -- in sorted
// runs, against the grain, and over keys rewritten and deleted within the same
// batch -- and requires the committed tree to equal the same writes applied in
// order to a map. The commit resumes each family's search from a hint left by
// that family's previous write; a stale or crossed hint must never misplace a
// key.
func TestBatch_CommitInterleavedKeyFamiliesMatchesModel(t *testing.T) {
	families := []string{"\x01", "\x02", "\x7f", "m", "\xff"}
	for seed := int64(1); seed <= 10; seed++ {
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed)) // #nosec G404 -- fixed-seed property-test fixture, not a cryptographic use
			s := openTestStorage(t)
			model := make(map[string]string)
			for round := 0; round < 4; round++ {
				b := s.Batch()
				for i := 0; i < 500; i++ {
					key := families[rng.Intn(len(families))] + fmt.Sprintf("%04d", rng.Intn(300))
					if rng.Intn(4) == 0 {
						mustBatch(t, b.Delete([]byte(key)))
						delete(model, key)
						continue
					}
					value := fmt.Sprintf("r%d-%d", round, i)
					mustBatch(t, b.Set([]byte(key), []byte(value)))
					model[key] = value
				}
				mustBatch(t, b.Commit())
				_ = b.Close()
			}

			want := make([]string, 0, len(model))
			for k := range model {
				want = append(want, k)
			}
			sort.Strings(want)
			if got := listKeys(t, s); !reflect.DeepEqual(got, want) {
				t.Fatalf("committed keys diverge from the model: %d keys, want %d", len(got), len(want))
			}
			for _, k := range want {
				if v, err := s.Get([]byte(k)); err != nil || string(v) != model[k] {
					t.Fatalf("Get(%q) = %q, %v, want %q", k, v, err, model[k])
				}
			}
		})
	}
}

func mustBatch(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
