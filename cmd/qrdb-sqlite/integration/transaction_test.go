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

//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTransaction_MultiProtocol_CommitBatches opens a SQLite transaction,
// performs 100 INSERT operations into the kv virtual table, commits, and
// verifies all 100 rows are present in qrdbd (via both vtab and SDK) for each
// protocol.
func TestTransaction_MultiProtocol_CommitBatches(t *testing.T) {
	const n = 100
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			keyPrefix := uniqueKey(proto, "tx-key")

			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)

			stmt, err := tx.Prepare(`INSERT INTO kv (key, value) VALUES (?, ?)`)
			require.NoError(t, err)
			defer stmt.Close()

			for i := 0; i < n; i++ {
				_, err := stmt.Exec(fmt.Sprintf("%s-%04d", keyPrefix, i), []byte(fmt.Sprintf("val-%d", i)))
				require.NoError(t, err, "INSERT at iteration %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT (%s)", proto)

			pattern := fmt.Sprintf("%s-%%", keyPrefix)
			assertRowCount(t, env.db,
				fmt.Sprintf("SELECT * FROM kv WHERE key LIKE '%s'", pattern), n)

			// Spot-check via SDK.
			firstKey := fmt.Sprintf("%s-0000", keyPrefix)
			val, err := env.client.KV().Get(ctx, firstKey)
			require.NoError(t, err, "SDK Get %s (%s)", firstKey, proto)
			assert.Equal(t, []byte("val-0"), val)
		})
	}
}

// TestTransaction_MultiProtocol_Rollback documents the virtual table rollback
// contract for the qrdb-sqlite layer.
//
// In go-sqlite3 virtual tables, each xUpdate call is executed immediately as
// SQLite invokes it; there is no deferred batching at the SQLite vtab level.
// The txBuffer adapter provides batched semantics above the vtab layer, but
// the underlying qrdbd store receives every write via the SDK as it happens.
// As a result, rolling back a SQLite transaction does NOT undo writes that
// have already propagated to qrdbd through xUpdate.
//
// This test documents and asserts that observed behaviour across all protocols.
func TestTransaction_MultiProtocol_Rollback(t *testing.T) {
	const n = 10
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			keyPrefix := uniqueKey(proto, "rb-key")

			tx, err := env.db.Begin()
			require.NoError(t, err)

			for i := 0; i < n; i++ {
				_, err := tx.Exec(
					`INSERT INTO kv (key, value) VALUES (?, ?)`,
					fmt.Sprintf("%s-%04d", keyPrefix, i), []byte("rollback-val"),
				)
				require.NoError(t, err, "INSERT at iteration %d (%s)", i, proto)
			}
			require.NoError(t, tx.Rollback(), "ROLLBACK (%s)", proto)

			// The keys were already forwarded to qrdbd through the vtab xUpdate
			// calls and are therefore still present after SQLite's ROLLBACK.
			pattern := fmt.Sprintf("%s-%%", keyPrefix)
			assertRowCount(t, env.db,
				fmt.Sprintf("SELECT * FROM kv WHERE key LIKE '%s'", pattern), n)

			// Confirm the first key is also visible via SDK.
			firstKey := fmt.Sprintf("%s-0000", keyPrefix)
			exists, err := env.client.KV().Exists(ctx, firstKey)
			require.NoError(t, err, "SDK Exists (%s)", proto)
			assert.True(t, exists, "vtab xUpdate-forwarded key must exist in qrdbd after SQLite ROLLBACK (%s)", proto)
		})
	}
}

// TestTransaction_MultiProtocol_MixedOps performs INSERT and DELETE within the
// same transaction and verifies only the expected net state is committed for
// each protocol.
func TestTransaction_MultiProtocol_MixedOps(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()

			keyA := uniqueKey(proto, "existing-a")
			keyB := uniqueKey(proto, "existing-b")
			keyX := uniqueKey(proto, "new-x")
			keyY := uniqueKey(proto, "new-y")

			// Pre-populate two keys outside the test transaction.
			_, err := env.db.Exec(`INSERT INTO kv (key, value) VALUES (?, 'va')`, keyA)
			require.NoError(t, err)
			_, err = env.db.Exec(`INSERT INTO kv (key, value) VALUES (?, 'vb')`, keyB)
			require.NoError(t, err)

			tx, err := env.db.Begin()
			require.NoError(t, err)

			_, err = tx.Exec(`INSERT INTO kv (key, value) VALUES (?, 'vx')`, keyX)
			require.NoError(t, err)
			_, err = tx.Exec(`INSERT INTO kv (key, value) VALUES (?, 'vy')`, keyY)
			require.NoError(t, err)
			_, err = tx.Exec(`DELETE FROM kv WHERE key = ?`, keyA)
			require.NoError(t, err)

			require.NoError(t, tx.Commit())

			// Verify net state via SQL vtab.
			assertRowCount(t, env.db, fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", keyA), 0)
			assertRowCount(t, env.db, fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", keyB), 1)
			assertRowCount(t, env.db, fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", keyX), 1)
			assertRowCount(t, env.db, fmt.Sprintf("SELECT * FROM kv WHERE key = '%s'", keyY), 1)

			// Verify via SDK.
			existsA, err := env.client.KV().Exists(ctx, keyA)
			require.NoError(t, err)
			assert.False(t, existsA, "key-a must be absent in qrdbd after DELETE (%s)", proto)

			existsB, err := env.client.KV().Exists(ctx, keyB)
			require.NoError(t, err)
			assert.True(t, existsB, "key-b must still exist in qrdbd (%s)", proto)
		})
	}
}

// TestTransaction_MultiProtocol_ConcurrentTransactions launches 5 goroutines
// per protocol, each writing to its own isolated testEnv, and verifies that
// data committed in each goroutine is fully visible.
func TestTransaction_MultiProtocol_ConcurrentTransactions(t *testing.T) {
	const goroutines = 5
	const keysPerGoroutine = 20
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			var wg sync.WaitGroup
			errs := make([]error, goroutines)

			for g := 0; g < goroutines; g++ {
				g := g
				wg.Add(1)
				go func() {
					defer wg.Done()

					// Each goroutine gets its own isolated testEnv.
					env := newTestEnv(t, proto)

					tx, err := env.db.Begin()
					if err != nil {
						errs[g] = fmt.Errorf("goroutine %d: Begin: %w", g, err)
						return
					}

					for k := 0; k < keysPerGoroutine; k++ {
						key := fmt.Sprintf("%s/g%d-key-%04d", proto, g, k)
						if _, err := tx.Exec(`INSERT INTO kv (key, value) VALUES (?, ?)`,
							key, []byte("v")); err != nil {
							_ = tx.Rollback()
							errs[g] = fmt.Errorf("goroutine %d: INSERT key %s: %w", g, key, err)
							return
						}
					}

					if err := tx.Commit(); err != nil {
						errs[g] = fmt.Errorf("goroutine %d: Commit: %w", g, err)
						return
					}

					query := fmt.Sprintf("SELECT * FROM kv WHERE key LIKE '%s/g%d-key-%%'", proto, g)
					rows := scanAllRows(t, env.db, query)
					if len(rows) != keysPerGoroutine {
						errs[g] = fmt.Errorf("goroutine %d: expected %d rows, got %d",
							g, keysPerGoroutine, len(rows))
					}
				}()
			}
			wg.Wait()

			for g, err := range errs {
				assert.NoError(t, err, "goroutine %d (%s) reported error", g, proto)
			}
		})
	}
}
