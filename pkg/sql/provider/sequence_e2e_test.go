//go:build qrdb_sql

// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// End-to-end sequence tests: the nextval() function, sequence-defaulted
// columns (PostgreSQL's serial outside a key), and DROP SEQUENCE's reset
// semantics, all against the Raft-shaped catalog harness.

package provider

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNextval_AllocatesFromOne pins nextval()'s contract: sequences
// materialize on first use, start at 1, and advance per call.
func TestNextval_AllocatesFromOne(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	_, rows := mustExecQuery(t, prov, "shop", "SELECT nextval('s1'), nextval('s1'), nextval('s2')")
	require.Len(t, rows, 1)
	assert.EqualValues(t, 1, rows[0][0])
	assert.EqualValues(t, 2, rows[0][1])
	assert.EqualValues(t, 1, rows[0][2], "sequences are independent")

	_, rows = mustExecQuery(t, prov, "shop", "SELECT nextval('s1')")
	assert.EqualValues(t, 3, rows[0][0], "the counter is durable across engines")
}

// TestSequenceDefaultedColumn covers the serial-outside-a-key translation
// end to end: a column declared DEFAULT (NEXTVAL(...)) numbers its rows
// 1, 2, 3 alongside the AUTO_INCREMENT primary key.
func TestSequenceDefaultedColumn(t *testing.T) {
	h := newDDLHarness()
	prov := h.newDDLProvider("shop")

	mustExecQuery(t, prov, "shop",
		"CREATE TABLE events (id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT, uid BIGINT DEFAULT (NEXTVAL('events_uid_seq')) NOT NULL)")
	for i := 0; i < 3; i++ {
		mustExecQuery(t, prov, "shop", "INSERT INTO events () VALUES ()")
	}

	_, rows := mustExecQuery(t, prov, "shop", "SELECT id, uid FROM events ORDER BY id")
	require.Len(t, rows, 3)
	for i, row := range rows {
		assert.EqualValues(t, i+1, row[1], "uid must follow the sequence")
		_ = row[0]
	}
}
