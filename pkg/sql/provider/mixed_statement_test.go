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

package provider

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStatement_WritingRelationalAndKVTablesIsRefused: an autocommit
// statement is atomic, and a kv write cannot join its transaction, so a
// statement that writes both kinds is refused before it writes either.
func TestStatement_WritingRelationalAndKVTablesIsRefused(t *testing.T) {
	h := newDDLHarness()
	c := newTxConn(t, h.newDDLProvider("shop"), "shop")
	c.mustExec(t, "CREATE TABLE t (id BIGINT PRIMARY KEY, v VARCHAR(8))")
	c.mustExec(t, "INSERT INTO t VALUES (1, 'a')")
	c.mustExec(t, "INSERT INTO kv (`key`, value) VALUES ('k', 'a')")

	_, _, err := c.exec("UPDATE t JOIN kv ON kv.`key` = 'k' SET t.v = 'x', kv.value = 'x'")
	var mixed *MixedTransactionalWriteError
	require.ErrorAs(t, err, &mixed)
	assert.Equal(t, "t", mixed.Relational)
	assert.Equal(t, "kv", mixed.NonTransactional)

	_, rows := c.mustExec(t, "SELECT v FROM t WHERE id = 1")
	assert.Equal(t, "a", rows[0][0], "the relational row is unchanged")
	raw, err := h.store.Get(context.Background(), "k")
	require.NoError(t, err)
	assert.Equal(t, "a", string(raw), "the kv row is unchanged")
}
