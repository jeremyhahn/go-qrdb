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

package service

import (
	"context"
	"strings"
	"testing"
)

// TestTableDataPrefix_MatchesTheInternalLayout verifies the exported
// accessor the SQL data plane composes row keys with agrees, byte for byte,
// with the unexported prefix TableService itself proposes writes under. The
// two drifting apart would mean the SQL layer reading a keyspace nothing
// writes to.
func TestTableDataPrefix_MatchesTheInternalLayout(t *testing.T) {
	for _, tableID := range []uint64{1, 42, 1 << 40} {
		if got, want := TableDataPrefix("shop", tableID, 0), tableDataPrefixFor("shop", tableID, 0); got != want {
			t.Errorf("TableDataPrefix(shop, %d, 0) = %q, want %q", tableID, got, want)
		}
	}
}

// TestTableDataPrefix_IsDistinctPerTableAndDatabase verifies two tables can
// never scan into each other's rows.
func TestTableDataPrefix_IsDistinctPerTableAndDatabase(t *testing.T) {
	a := TableDataPrefix("shop", 1, 0)
	b := TableDataPrefix("shop", 2, 0)
	c := TableDataPrefix("other", 1, 0)

	if a == b || a == c || b == c {
		t.Fatalf("prefixes collide: %q, %q, %q", a, b, c)
	}
	if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
		t.Fatalf("one table's prefix is a prefix of another's: %q, %q", a, b)
	}
}

// TestTableDataPrefix_IsDistinctPerGeneration verifies go-qrdb#547's
// generation fencing at the key layer itself: TRUNCATE's old and new
// generations of the same table must never scan into each other, which is
// what lets the reclaim sweeper delete the old generation's keyspace while
// statements planned against the new one keep reading and writing normally.
func TestTableDataPrefix_IsDistinctPerGeneration(t *testing.T) {
	a := TableDataPrefix("shop", 1, 0)
	b := TableDataPrefix("shop", 1, 1)

	if a == b {
		t.Fatalf("generations 0 and 1 produced the same prefix: %q", a)
	}
	if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
		t.Fatalf("one generation's prefix is a prefix of the other's: %q, %q", a, b)
	}
}

// TestTableIndexPrefix_MatchesTheInternalLayout is TableDataPrefix's
// counterpart for one secondary index's entries.
func TestTableIndexPrefix_MatchesTheInternalLayout(t *testing.T) {
	for _, incarnation := range []uint64{1, 2, 10} {
		if got, want := TableIndexPrefix("shop", 7, 0, incarnation), tableIndexPrefixFor("shop", 7, 0, incarnation); got != want {
			t.Errorf("TableIndexPrefix(shop, 7, 0, %d) = %q, want %q", incarnation, got, want)
		}
	}
}

// TestTableIndexPrefix_IsDistinctFromRowData verifies an index scan can
// never pick up rows, and a row scan can never pick up index entries.
func TestTableIndexPrefix_IsDistinctFromRowData(t *testing.T) {
	data := TableDataPrefix("shop", 7, 0)
	index := TableIndexPrefix("shop", 7, 0, 1)

	if strings.HasPrefix(index, data) || strings.HasPrefix(data, index) {
		t.Fatalf("index prefix %q and data prefix %q overlap", index, data)
	}
	if index == TableIndexPrefix("shop", 7, 0, 2) {
		t.Fatal("two indexes on the same table share a prefix")
	}
}

// TestTableIndexPrefix_NoIncarnationPrefixesAnother is the "a" vs "a/b"
// overlap an index NAME segment would have: incarnation 1's prefix must not
// be a prefix of incarnation 10's (or 1's of 11's), or reclaiming one index
// would delete another's entries.
func TestTableIndexPrefix_NoIncarnationPrefixesAnother(t *testing.T) {
	for _, pair := range [][2]uint64{{1, 10}, {1, 11}, {2, 21}, {1, 2}} {
		a, b := TableIndexPrefix("shop", 7, 0, pair[0]), TableIndexPrefix("shop", 7, 0, pair[1])
		if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
			t.Fatalf("incarnation prefixes overlap: %q, %q", a, b)
		}
	}
}

// TestTableIndexPrefix_IsDistinctPerGeneration is
// TestTableDataPrefix_IsDistinctPerGeneration's counterpart for secondary
// index entries: the reclaim sweeper must be able to delete an old
// generation's index entries without touching the new generation's.
func TestTableIndexPrefix_IsDistinctPerGeneration(t *testing.T) {
	a := TableIndexPrefix("shop", 7, 0, 1)
	b := TableIndexPrefix("shop", 7, 1, 1)

	if a == b {
		t.Fatalf("generations 0 and 1 produced the same index prefix: %q", a)
	}
	if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
		t.Fatalf("one generation's index prefix is a prefix of the other's: %q, %q", a, b)
	}
}

// TestTableSequenceNames_AreDistinctPerPurpose is the invariant that keeps a
// table with BOTH an AUTO_INCREMENT column and a synthetic hidden row id --
// which is any keyless table whose AUTO_INCREMENT column is backed by a
// UNIQUE index rather than a PRIMARY KEY -- from having one counter consume
// the other's values.
func TestTableSequenceNames_AreDistinctPerPurpose(t *testing.T) {
	const tableID = 7
	autoInc := TableAutoIncrementSequenceName(tableID)
	rowID := TableRowIDSequenceName(tableID)

	if autoInc == rowID {
		t.Fatalf("both sequences are named %q", autoInc)
	}
	if strings.HasPrefix(autoInc, rowID) || strings.HasPrefix(rowID, autoInc) {
		t.Fatalf("one sequence name is a prefix of the other: %q, %q", autoInc, rowID)
	}
	if !strings.HasPrefix(autoInc, "__") || !strings.HasPrefix(rowID, "__") {
		t.Fatalf("sequence names must sit under the reserved prefix: %q, %q", autoInc, rowID)
	}
}

// TestTableSequenceNames_AreKeyedByTableID verifies both sequences are
// derived from the immutable TableID rather than the table's name, which is
// what makes RENAME TABLE a pure catalog operation that cannot reset a
// counter and start reissuing values already in use.
func TestTableSequenceNames_AreKeyedByTableID(t *testing.T) {
	cases := []struct {
		name string
		fn   func(uint64) string
	}{
		{"auto increment", TableAutoIncrementSequenceName},
		{"row id", TableRowIDSequenceName},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seen := make(map[string]bool)
			for _, tableID := range []uint64{0, 1, 2, 1 << 40} {
				name := tc.fn(tableID)
				if seen[name] {
					t.Fatalf("two table IDs produced the same sequence name %q", name)
				}
				seen[name] = true
			}
		})
	}
}

// TestEncodeDecodeTableGeneration_RoundTrips verifies the exported
// encode/decode pair (used by both TableService's own CAS and the SQL data
// plane's generation-fencing Compares, see relational_writer.go and
// writeset.go) agree with each other byte for byte.
func TestEncodeDecodeTableGeneration_RoundTrips(t *testing.T) {
	for _, generation := range []uint64{0, 1, 42, 1 << 40} {
		encoded := EncodeTableGeneration(generation)
		if got := DecodeTableGeneration(encoded); got != generation {
			t.Errorf("DecodeTableGeneration(EncodeTableGeneration(%d)) = %d", generation, got)
		}
	}
}

// TestDecodeTableGeneration_AbsentIsZero verifies a nil/short value -- what a
// generation key that has never been written reads back as -- decodes as
// generation 0, the same "absent means never truncated" contract
// TableGeneration and TableService.readGeneration use.
func TestDecodeTableGeneration_AbsentIsZero(t *testing.T) {
	for _, data := range [][]byte{nil, {}, {1, 2, 3}} {
		if got := DecodeTableGeneration(data); got != 0 {
			t.Errorf("DecodeTableGeneration(%v) = %d, want 0", data, got)
		}
	}
}

// singleKeyKV is a KVServicer double that implements only Get (backed by an
// in-memory map), embedding the nil interface for every other method --
// following the same "embed nil, implement only what's used" convention as
// ddl_harness_test.go's memBatch. TableGeneration's only KVServicer call is
// Get, so this is sufficient without pulling in a full KVServicer fake.
type singleKeyKV struct {
	KVServicer
	data map[string][]byte
}

func (s *singleKeyKV) Get(_ context.Context, key string) ([]byte, error) {
	v, ok := s.data[key]
	if !ok {
		return nil, &KeyNotFoundError{Key: key}
	}
	return v, nil
}

// TestTableGeneration_AbsentKeyIsZero verifies a table that has never been
// truncated reads generation 0 through the exported accessor, matching
// TableDef.Generation's zero value for a freshly created table.
func TestTableGeneration_AbsentKeyIsZero(t *testing.T) {
	kv := &singleKeyKV{data: map[string][]byte{}}
	got, err := TableGeneration(context.Background(), kv, "shop", 7)
	if err != nil {
		t.Fatalf("TableGeneration: %v", err)
	}
	if got != 0 {
		t.Fatalf("TableGeneration on a never-truncated table = %d, want 0", got)
	}
}

// TestTableGeneration_ReadsWhatWasWritten verifies TableGeneration reads
// back exactly what was written at TableGenerationKey, using
// EncodeTableGeneration's own encoding -- the same key and encoding
// TableService.TruncateTable writes at commit time.
func TestTableGeneration_ReadsWhatWasWritten(t *testing.T) {
	kv := &singleKeyKV{data: map[string][]byte{TableGenerationKey("shop", 7): EncodeTableGeneration(3)}}
	got, err := TableGeneration(context.Background(), kv, "shop", 7)
	if err != nil {
		t.Fatalf("TableGeneration: %v", err)
	}
	if got != 3 {
		t.Fatalf("TableGeneration = %d, want 3", got)
	}
}
