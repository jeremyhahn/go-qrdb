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

package storage_test

import (
	"bytes"
	"slices"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// nulTable is the prefix of a table whose keys begin with NUL, the shape the
// engines' internal keyspaces use. Every byte value follows it.
var nulTable = []byte{0x00, 0x01}

// binaryParityKeys returns every single-byte key, every key of the NUL-prefixed
// table, and the neighbours that sit immediately either side of that table, so
// a prefix scan that leaks across its bounds is visible.
func binaryParityKeys() [][]byte {
	keys := make([][]byte, 0, 2*256+3)
	for b := range 256 {
		keys = append(keys, []byte{byte(b)})
		keys = append(keys, append(slices.Clone(nulTable), byte(b)))
	}
	keys = append(keys,
		[]byte{0x00, 0x00, 0xff},
		[]byte{0x00, 0x02},
		[]byte{0x00, 0x02, 0x00},
	)
	return keys
}

// binaryParityValue derives a value that differs per key and carries both NUL
// and 0xff, so a backend that truncates or re-encodes values is caught.
func binaryParityValue(key []byte) []byte {
	v := append([]byte{0x00}, key...)
	return append(v, 0xff, byte(len(key)))
}

// collect drains an iterator, forward or backward, into a slice of copied keys, checking each value.
func collect(t *testing.T, name string, s storage.Storage, opts *storage.IteratorOptions, backward bool) [][]byte {
	t.Helper()
	it := s.NewIterator(opts)
	defer func() { _ = it.Close() }()
	var out [][]byte
	ok := it.SeekToFirst()
	if backward {
		ok = it.SeekToLast()
	}
	for ; ok && it.Valid(); ok = step(it, backward) {
		key := slices.Clone(it.Key())
		if !bytes.Equal(it.Value(), binaryParityValue(key)) {
			t.Errorf("%s: iterator value for %x = %x", name, key, it.Value())
		}
		out = append(out, key)
	}
	if err := it.Error(); err != nil {
		t.Fatalf("%s: iterator: %v", name, err)
	}
	return out
}

// assertKeys fails unless got is exactly want, in order.
func assertKeys(t *testing.T, name, what string, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %s yielded %d keys, want %d", name, what, len(got), len(want))
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("%s: %s position %d = %x, want %x", name, what, i, got[i], want[i])
		}
	}
}

// TestBinaryKeyParity drives every backend with all 256 byte values, as whole
// keys and as the last byte of a NUL-prefixed table, and requires each one to
// round-trip, to iterate in unsigned byte order both ways, and to answer a
// prefix scan of the NUL-prefixed table with exactly that table.
func TestBinaryKeyParity(t *testing.T) {
	keys := binaryParityKeys()
	sorted := slices.Clone(keys)
	slices.SortFunc(sorted, bytes.Compare)
	reversed := slices.Clone(sorted)
	slices.Reverse(reversed)

	var table [][]byte
	for _, k := range sorted {
		if bytes.HasPrefix(k, nulTable) {
			table = append(table, k)
		}
	}
	if len(table) != 256 {
		t.Fatalf("fixture: NUL table holds %d keys, want 256", len(table))
	}

	for _, b := range newParityBackends(t) {
		t.Run(b.name, func(t *testing.T) {
			for _, k := range keys {
				if err := b.s.Set(k, binaryParityValue(k)); err != nil {
					t.Fatalf("set %x: %v", k, err)
				}
			}
			for _, k := range keys {
				got, err := b.s.Get(k)
				if err != nil {
					t.Fatalf("get %x: %v", k, err)
				}
				if !bytes.Equal(got, binaryParityValue(k)) {
					t.Fatalf("get %x = %x, want %x", k, got, binaryParityValue(k))
				}
			}

			assertKeys(t, b.name, "forward scan", collect(t, b.name, b.s, &storage.IteratorOptions{}, false), sorted)
			assertKeys(t, b.name, "reverse scan", collect(t, b.name, b.s, &storage.IteratorOptions{}, true), reversed)

			bounded := collect(t, b.name, b.s, &storage.IteratorOptions{
				LowerBound: nulTable,
				UpperBound: []byte{0x00, 0x02},
			}, false)
			assertKeys(t, b.name, "NUL-table bounded scan", bounded, table)

			listed, err := b.s.List(nulTable)
			if err != nil {
				t.Fatalf("list NUL table: %v", err)
			}
			slices.SortFunc(listed, bytes.Compare)
			assertKeys(t, b.name, "NUL-table list", listed, table)

			nulListed, err := b.s.List([]byte{0x00})
			if err != nil {
				t.Fatalf("list NUL: %v", err)
			}
			if want := 256 + 4; len(nulListed) != want {
				t.Fatalf("list NUL yielded %d keys, want %d", len(nulListed), want)
			}
		})
	}
}
