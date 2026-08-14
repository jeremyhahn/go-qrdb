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
		if got, want := TableDataPrefix("shop", tableID), tableDataPrefixFor("shop", tableID); got != want {
			t.Errorf("TableDataPrefix(shop, %d) = %q, want %q", tableID, got, want)
		}
	}
}

// TestTableDataPrefix_IsDistinctPerTableAndDatabase verifies two tables can
// never scan into each other's rows.
func TestTableDataPrefix_IsDistinctPerTableAndDatabase(t *testing.T) {
	a := TableDataPrefix("shop", 1)
	b := TableDataPrefix("shop", 2)
	c := TableDataPrefix("other", 1)

	if a == b || a == c || b == c {
		t.Fatalf("prefixes collide: %q, %q, %q", a, b, c)
	}
	if strings.HasPrefix(b, a) || strings.HasPrefix(a, b) {
		t.Fatalf("one table's prefix is a prefix of another's: %q, %q", a, b)
	}
}

// TestTableIndexPrefix_MatchesTheInternalLayout is TableDataPrefix's
// counterpart for one secondary index's entries.
func TestTableIndexPrefix_MatchesTheInternalLayout(t *testing.T) {
	for _, index := range []string{"PRIMARY", "idx_email", "uq_code"} {
		if got, want := TableIndexPrefix("shop", 7, index), tableIndexPrefixFor("shop", 7, index); got != want {
			t.Errorf("TableIndexPrefix(shop, 7, %q) = %q, want %q", index, got, want)
		}
	}
}

// TestTableIndexPrefix_IsDistinctFromRowData verifies an index scan can
// never pick up rows, and a row scan can never pick up index entries.
func TestTableIndexPrefix_IsDistinctFromRowData(t *testing.T) {
	data := TableDataPrefix("shop", 7)
	index := TableIndexPrefix("shop", 7, "idx_email")

	if strings.HasPrefix(index, data) || strings.HasPrefix(data, index) {
		t.Fatalf("index prefix %q and data prefix %q overlap", index, data)
	}
	if index == TableIndexPrefix("shop", 7, "idx_other") {
		t.Fatal("two indexes on the same table share a prefix")
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
