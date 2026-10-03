//go:build qrdb_sql

// Copyright 2026 Jeremy Hahn
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

package provider

import (
	"testing"

	"github.com/dolthub/vitess/go/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An UPDATE that keeps its row's primary key but moves a UNIQUE column onto
// a value already taken must fail with 1062 inside a transaction exactly as
// it does in autocommit -- whether the value is taken by a committed row or
// by a row this same transaction wrote.

func TestBufferedUpdate_UniqueCollisionWithCommittedRow(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE u (id BIGINT PRIMARY KEY, code BIGINT, UNIQUE KEY uq_code (code))")
	c.must("INSERT INTO u VALUES (1, 10), (2, 20)")

	c.must("BEGIN")
	_, err := c.exec("UPDATE u SET code = 10 WHERE id = 2")
	requireSQLError(t, err, mysql.ERDupEntry)
	c.must("ROLLBACK")
}

func TestBufferedUpdate_UniqueCollisionWithinTheTransaction(t *testing.T) {
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE u (id BIGINT PRIMARY KEY, code BIGINT, UNIQUE KEY uq_code (code))")
	c.must("INSERT INTO u VALUES (1, 10), (2, 20), (3, 30)")

	c.must("BEGIN")
	c.must("UPDATE u SET code = 32 WHERE id = 2")
	_, err := c.exec("UPDATE u SET code = 32 WHERE id = 3")
	requireSQLError(t, err, mysql.ERDupEntry)
	c.must("COMMIT")

	rows := c.must("SELECT id, code FROM u WHERE code = 32")
	require.Len(t, rows, 1, "two rows committed with one UNIQUE value")
	assert.EqualValues(t, 2, rows[0][0])
}

func TestBufferedUpdate_UniqueValueFreedEarlierInTheTransaction(t *testing.T) {
	// Moving a value off one row and onto another in the same transaction
	// is not a collision.
	h := newLockHarness(t)
	c := h.conn(t)
	c.must("CREATE TABLE u (id BIGINT PRIMARY KEY, code BIGINT, UNIQUE KEY uq_code (code))")
	c.must("INSERT INTO u VALUES (1, 10), (2, 20)")

	c.must("BEGIN")
	c.must("UPDATE u SET code = 11 WHERE id = 1")
	c.must("UPDATE u SET code = 10 WHERE id = 2")
	c.must("COMMIT")

	assert.EqualValues(t, 2, c.must("SELECT id FROM u WHERE code = 10")[0][0])
	assert.EqualValues(t, 1, c.must("SELECT id FROM u WHERE code = 11")[0][0])
}
