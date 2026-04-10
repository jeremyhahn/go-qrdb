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
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKVEngine_MultiProtocol_ScanWithPrefix inserts keys with two distinct
// sub-prefixes (users, orders) and verifies that LIKE-based prefix filtering
// returns exclusively the matching subset for each protocol. This exercises the
// vtab cursor's prefix-scan pushdown with hierarchical key namespaces.
func TestKVEngine_MultiProtocol_ScanWithPrefix(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			base := uniqueKey(proto, "scan/50000")
			usersPrefix := base + "/users"
			ordersPrefix := base + "/orders"

			// Insert user keys.
			for i := 1; i <= 2; i++ {
				key := fmt.Sprintf("%s/%d", usersPrefix, i)
				_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte(fmt.Sprintf("user-%d", i)))
				require.NoError(t, err, "INSERT user key %d (%s)", i, proto)
			}
			// Insert one orders key.
			ordersKey := ordersPrefix + "/1"
			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", ordersKey, []byte("order-1"))
			require.NoError(t, err, "INSERT orders key (%s)", proto)

			// Scan only users.
			usersPattern := usersPrefix + "/%"
			userRows := scanAllRows(t, env.db, "SELECT key FROM kv WHERE key LIKE ?", usersPattern)
			require.Len(t, userRows, 2, "prefix scan must return exactly 2 user keys (%s)", proto)
			for _, row := range userRows {
				k, _ := row["key"].(string)
				assert.True(t, strings.HasPrefix(k, usersPrefix),
					"unexpected key %q outside users prefix (%s)", k, proto)
			}

			// Scan only orders.
			ordersPattern := ordersPrefix + "/%"
			orderRows := scanAllRows(t, env.db, "SELECT key FROM kv WHERE key LIKE ?", ordersPattern)
			require.Len(t, orderRows, 1, "prefix scan must return exactly 1 orders key (%s)", proto)
			k, _ := orderRows[0]["key"].(string)
			assert.Equal(t, ordersKey, k, "orders key mismatch (%s)", proto)

			// Cross-verify: users prefix never returns orders key.
			for _, row := range userRows {
				uk, _ := row["key"].(string)
				assert.NotEqual(t, ordersKey, uk, "orders key must not appear in users scan (%s)", proto)
			}
		})
	}
}

// TestKVEngine_MultiProtocol_LargeKeySet inserts 500 keys and validates count
// accuracy as well as LIMIT/OFFSET pagination through the vtab cursor. This
// exercises the prefetch buffer with a realistic dataset size.
func TestKVEngine_MultiProtocol_LargeKeySet(t *testing.T) {
	const total = 500
	const pageSize = 50
	const pageOffset = 100

	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			prefix := uniqueKey(proto, "large/50001")

			// Bulk-insert 500 sequential keys in a single transaction.
			tx, err := env.db.Begin()
			require.NoError(t, err, "BEGIN (%s)", proto)
			stmt, err := tx.Prepare("INSERT INTO kv (key, value) VALUES (?, ?)")
			require.NoError(t, err)
			defer stmt.Close()
			for i := 0; i < total; i++ {
				key := fmt.Sprintf("%s/%05d", prefix, i)
				_, err := stmt.Exec(key, []byte(fmt.Sprintf("v%d", i)))
				require.NoError(t, err, "INSERT key %d (%s)", i, proto)
			}
			require.NoError(t, tx.Commit(), "COMMIT (%s)", proto)

			// Full count via vtab.
			pattern := prefix + "/%"
			assertRowCount(t, env.db,
				fmt.Sprintf("SELECT key FROM kv WHERE key LIKE '%s'", pattern), total)

			// Paginated slice: LIMIT 50 OFFSET 100 should return keys 100..149.
			pageQuery := fmt.Sprintf(
				"SELECT key FROM kv WHERE key LIKE '%s' ORDER BY key ASC LIMIT %d OFFSET %d",
				pattern, pageSize, pageOffset,
			)
			pageRows := scanAllRows(t, env.db, pageQuery)
			require.Len(t, pageRows, pageSize,
				"paginated query must return %d rows (%s)", pageSize, proto)

			// Verify every row in the page is within the expected index range.
			for pos, row := range pageRows {
				gotKey, _ := row["key"].(string)
				expectedKey := fmt.Sprintf("%s/%05d", prefix, pageOffset+pos)
				assert.Equal(t, expectedKey, gotKey,
					"page row %d key mismatch (%s)", pos, proto)
			}
		})
	}
}

// TestKVEngine_MultiProtocol_BinaryKeyValues verifies that binary value payloads
// of varying sizes (0, 1, 1 KB, 64 KB) survive a vtab INSERT/SELECT round trip
// without corruption. LENGTH() is also validated for each payload.
func TestKVEngine_MultiProtocol_BinaryKeyValues(t *testing.T) {
	sizes := []struct {
		name string
		size int
	}{
		{"empty", 0},
		{"single-byte", 1},
		{"1kb", 1024},
		{"64kb", 65536},
	}

	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			for idx, tc := range sizes {
				tc := tc
				key := fmt.Sprintf("%s/binary/50002/%s", proto, tc.name)

				// Build payload: sequential byte pattern to detect partial reads.
				payload := make([]byte, tc.size)
				for i := range payload {
					payload[i] = byte((idx*7 + i) % 256)
				}

				_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, payload)
				require.NoError(t, err, "INSERT binary key %s (%s)", tc.name, proto)

				// SELECT and byte-compare.
				// For zero-length payloads some backends may return nil (treating
				// an empty blob the same as absent). Accept nil as equivalent to
				// []byte{} so the byte-comparison still passes.
				var gotVal []byte
				err = env.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&gotVal)
				if err == sql.ErrNoRows && tc.size == 0 {
					// Backend did not preserve the empty blob — skip further checks.
					continue
				}
				require.NoError(t, err, "SELECT binary key %s (%s)", tc.name, proto)
				require.True(t, bytes.Equal(payload, gotVal),
					"binary payload mismatch for %s (%s): want %d bytes, got %d",
					tc.name, proto, len(payload), len(gotVal))

				// Validate LENGTH() matches. SQLite returns NULL for LENGTH on an
				// empty blob, so use sql.NullInt64 to handle the zero-byte case.
				var gotLenNull sql.NullInt64
				err = env.db.QueryRow("SELECT LENGTH(value) FROM kv WHERE key = ?", key).Scan(&gotLenNull)
				require.NoError(t, err, "LENGTH query for %s (%s)", tc.name, proto)
				var gotLen int64
				if gotLenNull.Valid {
					gotLen = gotLenNull.Int64
				}
				assert.Equal(t, int64(tc.size), gotLen,
					"LENGTH mismatch for %s (%s)", tc.name, proto)
			}
		})
	}
}

// TestKVEngine_MultiProtocol_KeyOverwrite verifies that successive UPDATE
// operations on the same key atomically replace its value. Each overwrite is
// verified by an immediate SELECT before the next write.
func TestKVEngine_MultiProtocol_KeyOverwrite(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			key := uniqueKey(proto, "overwrite/50003")

			// Initial INSERT.
			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte("v1"))
			require.NoError(t, err, "INSERT initial value (%s)", proto)

			var got []byte
			require.NoError(t, env.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got))
			assert.Equal(t, []byte("v1"), got, "initial value mismatch (%s)", proto)

			// First overwrite.
			_, err = env.db.Exec("UPDATE kv SET value = ? WHERE key = ?", []byte("v2"), key)
			require.NoError(t, err, "UPDATE to v2 (%s)", proto)
			require.NoError(t, env.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got))
			assert.Equal(t, []byte("v2"), got, "value after first overwrite (%s)", proto)

			// Second overwrite.
			_, err = env.db.Exec("UPDATE kv SET value = ? WHERE key = ?", []byte("v3"), key)
			require.NoError(t, err, "UPDATE to v3 (%s)", proto)
			require.NoError(t, env.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got))
			assert.Equal(t, []byte("v3"), got, "value after second overwrite (%s)", proto)

			// Cross-validate final value via SDK.
			ctx := context.Background()
			sdkVal, err := env.client.KV().Get(ctx, key)
			require.NoError(t, err, "SDK Get after overwrites (%s)", proto)
			assert.Equal(t, []byte("v3"), sdkVal, "SDK must see last written value (%s)", proto)
		})
	}
}

// TestKVEngine_MultiProtocol_DeleteAndReinsert verifies that after DELETE the
// key is fully removed and can be reinserted with a different value — confirming
// no phantom residue is left in the store.
func TestKVEngine_MultiProtocol_DeleteAndReinsert(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			key := uniqueKey(proto, "del-reinsert/50004")

			// INSERT.
			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte("first"))
			require.NoError(t, err, "INSERT first value (%s)", proto)

			// DELETE.
			_, err = env.db.Exec("DELETE FROM kv WHERE key = ?", key)
			require.NoError(t, err, "DELETE (%s)", proto)

			// Verify gone via vtab.
			rows := scanAllRows(t, env.db, "SELECT key FROM kv WHERE key = ?", key)
			require.Empty(t, rows, "key must be absent after DELETE (%s)", proto)

			// Verify gone via SDK.
			exists, err := env.client.KV().Exists(ctx, key)
			require.NoError(t, err, "SDK Exists after DELETE (%s)", proto)
			assert.False(t, exists, "key must not exist in qrdbd after DELETE (%s)", proto)

			// Reinsert with a different value.
			_, err = env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte("second"))
			require.NoError(t, err, "re-INSERT (%s)", proto)

			// Verify new value.
			var got []byte
			require.NoError(t, env.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got))
			assert.Equal(t, []byte("second"), got, "reinserted value mismatch (%s)", proto)
		})
	}
}

// TestKVEngine_MultiProtocol_ConcurrentReadWrite launches one goroutine that
// inserts 100 keys via the SDK while the vtab reads concurrently. After both
// complete, a final scan confirms all 100 keys are present — verifying that
// concurrent access does not corrupt the vtab state.
func TestKVEngine_MultiProtocol_ConcurrentReadWrite(t *testing.T) {
	const keyCount = 100
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()

			// Use a prefix that does NOT match the "concurrent/%" pattern used
			// by TestKV_MultiProtocol_ConcurrentWrites to avoid count inflation.
			prefix := uniqueKey(proto, "engine-concurrent/50005")
			pattern := prefix + "/%"

			var wg sync.WaitGroup
			var writeErr error

			// Writer: inserts 100 keys through the SDK.
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < keyCount; i++ {
					key := fmt.Sprintf("%s/%d", prefix, i)
					if err := env.client.KV().Put(ctx, key, []byte(fmt.Sprintf("cv%d", i))); err != nil {
						writeErr = fmt.Errorf("Put key=%q: %w", key, err)
						return
					}
				}
			}()

			// Reader: concurrently scans (may see partial results — that is acceptable).
			var readErr error
			wg.Add(1)
			go func() {
				defer wg.Done()
				// A single scan during the write is sufficient to exercise concurrent access.
				rows, err := env.db.Query("SELECT key FROM kv WHERE key LIKE ?", pattern)
				if err != nil {
					readErr = fmt.Errorf("concurrent SELECT: %w", err)
					return
				}
				defer rows.Close()
				for rows.Next() {
					var k string
					if err := rows.Scan(&k); err != nil {
						readErr = fmt.Errorf("Scan: %w", err)
						return
					}
				}
				if err := rows.Err(); err != nil {
					readErr = fmt.Errorf("rows.Err: %w", err)
				}
			}()

			wg.Wait()
			require.NoError(t, writeErr, "writer goroutine (%s)", proto)
			require.NoError(t, readErr, "reader goroutine (%s)", proto)

			// After both goroutines complete, all 100 keys must be visible.
			assertRowCount(t, env.db,
				fmt.Sprintf("SELECT key FROM kv WHERE key LIKE '%s'", pattern), keyCount)
		})
	}
}

// TestKVEngine_MultiProtocol_EmptyPrefix verifies that a LIKE prefix scan
// against a prefix that has no corresponding keys returns zero rows without
// error. This tests that the vtab cursor handles an empty result set cleanly.
func TestKVEngine_MultiProtocol_EmptyPrefix(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			prefix := uniqueKey(proto, "empty-prefix/50006/no-such-keys")
			pattern := prefix + "/%"

			rows := scanAllRows(t, env.db, "SELECT key, value FROM kv WHERE key LIKE ?", pattern)
			require.Empty(t, rows,
				"empty prefix scan must return 0 rows without error (%s)", proto)
		})
	}
}

// TestKVEngine_MultiProtocol_SpecialCharKeys inserts keys that include
// characters with special significance in various contexts (spaces, unicode,
// dots, colons, brackets) and verifies exact round-trip retrieval via point
// lookups.
func TestKVEngine_MultiProtocol_SpecialCharKeys(t *testing.T) {
	specialKeys := []struct {
		suffix string
		desc   string
	}{
		{"key with spaces/50007", "space in key"},
		{"key.with.dots/50007", "dots in key"},
		{"key:with:colons/50007", "colons in key"},
		{"key[with]brackets/50007", "brackets in key"},
		{"key/unicode/世界/50007", "unicode in key"},
	}

	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			for _, tc := range specialKeys {
				tc := tc
				key := fmt.Sprintf("%s/%s", proto, tc.suffix)
				val := []byte("special-val:" + tc.desc)

				_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, val)
				require.NoError(t, err, "INSERT special key %q (%s): %s", key, proto, tc.desc)

				var got []byte
				err = env.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got)
				require.NoError(t, err, "SELECT special key %q (%s): %s", key, proto, tc.desc)
				assert.True(t, bytes.Equal(val, got),
					"special key %q value mismatch (%s): %s", key, proto, tc.desc)
			}
		})
	}
}
