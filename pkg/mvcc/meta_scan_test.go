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
	"math/rand"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	pebblestorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// metaScanStore opens a real pebble store holding a metadata record for each
// key, plus records in the neighbouring namespaces the scan must never see.
func metaScanStore(t *testing.T, keys [][]byte) storage.Storage {
	t.Helper()
	store, err := pebblestorage.New(pebblestorage.TestConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("pebble.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, key := range keys {
		if err := store.Set(EncodeMetaKey(key), []byte{1}); err != nil {
			t.Fatalf("Set: %v", err)
		}
		// A data record for the same key sits in the namespace below.
		if err := store.Set(append([]byte{0x01}, key...), []byte{1}); err != nil {
			t.Fatalf("Set data: %v", err)
		}
	}
	if err := store.Set([]byte{prefixRevision}, []byte{1}); err != nil {
		t.Fatalf("Set revision: %v", err)
	}
	return store
}

func metaIterator(store storage.Storage) storage.Iterator {
	return store.NewIterator(&storage.IteratorOptions{LowerBound: MetaLowerBound(), UpperBound: MetaUpperBound()})
}

// bruteForceRange is what the scan replaced: walk every metadata key and
// filter. The seeking scan must agree with it exactly, order included.
func bruteForceRange(t *testing.T, store storage.Storage, start, end []byte) []string {
	t.Helper()
	iter := metaIterator(store)
	defer func() { _ = iter.Close() }()
	var out []string
	for iter.SeekToFirst(); iter.Valid(); iter.Next() {
		userKey, err := DecodeMetaKey(iter.Key())
		if err != nil {
			continue
		}
		if IsInRange(userKey, start, end) {
			out = append(out, string(userKey))
		}
	}
	return out
}

func seekingRange(t *testing.T, store storage.Storage, start, end []byte) []string {
	t.Helper()
	iter := metaIterator(store)
	defer func() { _ = iter.Close() }()
	var out []string
	if err := ForEachMetaKeyInRange(iter, start, end, func(k []byte) bool {
		out = append(out, string(k))
		return true
	}); err != nil {
		t.Fatalf("ForEachMetaKeyInRange: %v", err)
	}
	return out
}

// TestForEachMetaKeyInRange_AgreesWithTheFullScan is the correctness proof:
// over keys of many lengths and many ranges, including bounds shorter and
// longer than the keys, the seeking scan returns exactly the full scan's keys
// in the full scan's order.
func TestForEachMetaKeyInRange_AgreesWithTheFullScan(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []byte("abj:\x00\xff")
	randomKey := func(maxLen int) []byte {
		key := make([]byte, rng.Intn(maxLen+1))
		for i := range key {
			key[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return key
	}
	var keys [][]byte
	for range 600 {
		keys = append(keys, randomKey(9))
	}
	keys = append(keys, []byte(""), []byte("job:1"), []byte("job:12"), []byte("jobs"), []byte("jo"))
	store := metaScanStore(t, keys)

	cases := [][2][]byte{
		{nil, nil}, {[]byte("job:"), prefixEnd([]byte("job:"))}, {[]byte("a"), []byte("b")},
		{[]byte("ab"), nil}, {[]byte("zzzzzzzzzzzz"), nil}, {[]byte("\xff\xff"), nil}, {[]byte(""), []byte("a")},
	}
	for range 300 {
		start, end := randomKey(6), randomKey(6)
		if rng.Intn(4) == 0 {
			end = nil
		}
		cases = append(cases, [2][]byte{start, end})
	}
	for _, c := range cases {
		want, got := bruteForceRange(t, store, c[0], c[1]), seekingRange(t, store, c[0], c[1])
		if !slices.Equal(want, got) {
			t.Fatalf("range [%q, %q): seeking scan = %q, full scan = %q", c[0], c[1], got, want)
		}
	}
}

// TestForEachMetaKeyWithPrefix covers the prefix form the engine's List and
// Scan use, including the empty prefix (every key) and an all-0xFF prefix,
// which has no successor.
func TestForEachMetaKeyWithPrefix(t *testing.T) {
	store := metaScanStore(t, [][]byte{
		[]byte("job:a"), []byte("job:bb"), []byte("jobz"), []byte("task:1"), []byte("jo"), []byte("\xff\xffx"), []byte("\xff"),
	})
	collect := func(prefix string) []string {
		iter := metaIterator(store)
		defer func() { _ = iter.Close() }()
		var out []string
		if err := ForEachMetaKeyWithPrefix(iter, []byte(prefix), func(k []byte) bool {
			out = append(out, string(k))
			return true
		}); err != nil {
			t.Fatalf("ForEachMetaKeyWithPrefix(%q): %v", prefix, err)
		}
		return out
	}
	if got := collect("job:"); !slices.Equal(got, []string{"job:a", "job:bb"}) {
		t.Fatalf("prefix job: = %q", got)
	}
	if got := collect("\xff\xff"); !slices.Equal(got, []string{"\xff\xffx"}) {
		t.Fatalf("prefix 0xFF 0xFF = %q", got)
	}
	if got := collect(""); len(got) != 7 {
		t.Fatalf("the empty prefix visits every key, got %q", got)
	}
	if got := collect("nope"); got != nil {
		t.Fatalf("no key matches, got %q", got)
	}
}

// TestForEachMetaKeyInRange_StopsWhenTheVisitorDoes proves visit's false ends
// the scan at once, which Limit depends on.
func TestForEachMetaKeyInRange_StopsWhenTheVisitorDoes(t *testing.T) {
	store := metaScanStore(t, [][]byte{[]byte("a1"), []byte("a2"), []byte("a33"), []byte("a444")})
	iter := metaIterator(store)
	defer func() { _ = iter.Close() }()
	var seen []string
	if err := ForEachMetaKeyWithPrefix(iter, []byte("a"), func(k []byte) bool {
		seen = append(seen, string(k))
		return len(seen) < 2
	}); err != nil {
		t.Fatalf("ForEachMetaKeyWithPrefix: %v", err)
	}
	if !slices.Equal(seen, []string{"a1", "a2"}) {
		t.Fatalf("visited %q, want exactly the first two", seen)
	}
}

// failingIterator reports an error once the scan starts, so the scan must
// surface it rather than return an empty, successful answer.
type failingIterator struct{ storage.Iterator }

func (failingIterator) Seek([]byte) bool { return false }
func (failingIterator) Valid() bool      { return false }
func (failingIterator) Error() error     { return errors.New("disk read failed") }

func TestForEachMetaKeyInRange_SurfacesTheIteratorError(t *testing.T) {
	err := ForEachMetaKeyInRange(failingIterator{}, []byte("a"), nil, func([]byte) bool { return true })
	if err == nil {
		t.Fatal("an iterator error was reported as an empty scan")
	}
}

// staleSeekIterator finds nothing on every seek but stays positioned on an
// earlier key, as an iterator may; the scan must end, not loop.
type staleSeekIterator struct{ storage.Iterator }

func (staleSeekIterator) Seek([]byte) bool { return false }
func (staleSeekIterator) Valid() bool      { return true }
func (staleSeekIterator) Key() []byte      { return EncodeMetaKey([]byte("a")) }
func (staleSeekIterator) Error() error     { return nil }

func TestForEachMetaKeyInRange_ASeekThatFindsNothingEndsTheScan(t *testing.T) {
	visited := 0
	if err := ForEachMetaKeyInRange(staleSeekIterator{}, []byte("b"), nil, func([]byte) bool {
		visited++
		return true
	}); err != nil || visited != 0 {
		t.Fatalf("scan = (%d visits, %v), want an empty scan that ends", visited, err)
	}
}

func TestPrefixEnd(t *testing.T) {
	cases := map[string][]byte{"": nil, "a": []byte("b"), "a\xff": []byte("b"), "\xff\xff": nil, "job:": []byte("job;")}
	for in, want := range cases {
		if got := prefixEnd([]byte(in)); !slices.Equal(got, want) {
			t.Fatalf("prefixEnd(%q) = %q, want %q", in, got, want)
		}
	}
}
