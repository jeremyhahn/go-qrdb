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
	"fmt"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKV_MultiProtocol_CRUD exercises the full INSERT → SELECT → UPDATE →
// DELETE lifecycle through every transport. For each protocol the virtual
// table write is cross-validated against the raw SDK client.
func TestKV_MultiProtocol_CRUD(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			key := uniqueKey(proto, "crud")

			// INSERT via virtual table.
			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte("hello"))
			require.NoError(t, err, "INSERT via vtab")

			// Cross-validate: SDK must see what the vtab wrote.
			sdkVal, err := env.client.KV().Get(ctx, key)
			require.NoError(t, err, "SDK Get after vtab INSERT")
			assert.Equal(t, []byte("hello"), sdkVal)

			// SELECT back through the virtual table.
			var gotKey string
			var gotVal []byte
			err = env.db.QueryRow("SELECT key, value FROM kv WHERE key = ?", key).Scan(&gotKey, &gotVal)
			require.NoError(t, err, "SELECT via vtab")
			assert.Equal(t, key, gotKey)
			assert.Equal(t, []byte("hello"), gotVal)

			// UPDATE via virtual table.
			_, err = env.db.Exec("UPDATE kv SET value = ? WHERE key = ?", []byte("updated"), key)
			require.NoError(t, err, "UPDATE via vtab")

			// Cross-validate UPDATE via SDK.
			sdkVal, err = env.client.KV().Get(ctx, key)
			require.NoError(t, err, "SDK Get after vtab UPDATE")
			assert.Equal(t, []byte("updated"), sdkVal)

			// DELETE via virtual table.
			_, err = env.db.Exec("DELETE FROM kv WHERE key = ?", key)
			require.NoError(t, err, "DELETE via vtab")

			// Cross-validate DELETE via SDK.
			exists, err := env.client.KV().Exists(ctx, key)
			require.NoError(t, err, "SDK Exists after vtab DELETE")
			assert.False(t, exists, "key must be absent in qrdbd after vtab DELETE")

			// SELECT after DELETE must return no rows.
			rows := scanAllRows(t, env.db, "SELECT key FROM kv WHERE key = ?", key)
			require.Empty(t, rows, "vtab SELECT must return no rows after DELETE")
		})
	}
}

// TestKV_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL writes a key through
// the raw SDK client and reads it back through the SQLite virtual table for
// each protocol.
func TestKV_MultiProtocol_CrossProtocol_WriteSDK_ReadSQL(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			key := uniqueKey(proto, "sdk-to-sql")

			require.NoError(t, env.client.KV().Put(ctx, key, []byte("sdk-origin")))

			assertKVValue(t, env.db, key, "sdk-origin")
		})
	}
}

// TestKV_MultiProtocol_CrossProtocol_WriteSQL_ReadSDK writes a key through
// the SQLite virtual table and reads it back through the raw SDK client for
// each protocol.
func TestKV_MultiProtocol_CrossProtocol_WriteSQL_ReadSDK(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()
			key := uniqueKey(proto, "sql-to-sdk")

			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, []byte("vtab-origin"))
			require.NoError(t, err, "vtab INSERT")

			val, err := env.client.KV().Get(ctx, key)
			require.NoError(t, err, "SDK Get")
			assert.Equal(t, []byte("vtab-origin"), val)
		})
	}
}

// TestKV_MultiProtocol_BulkInsert inserts 500 keys through each protocol's
// virtual table and asserts the full count is returned by a scan.
func TestKV_MultiProtocol_BulkInsert(t *testing.T) {
	const n = 500
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			prefix := uniqueKey(proto, "bulk")

			insertKVBatch(t, env.db, prefix, n)
			assertRowCount(t, env.db, fmt.Sprintf("SELECT key FROM kv WHERE key LIKE '%s/%%'", prefix), n)
		})
	}
}

// TestKV_MultiProtocol_BinaryValues verifies that a 256-byte payload
// containing every possible byte value round-trips through the vtab BLOB
// column without corruption for each protocol.
func TestKV_MultiProtocol_BinaryValues(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			key := uniqueKey(proto, "binary")

			payload := make([]byte, 256)
			for i := range payload {
				payload[i] = byte(i)
			}

			_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, payload)
			require.NoError(t, err, "INSERT binary BLOB")

			var got []byte
			err = env.db.QueryRow("SELECT value FROM kv WHERE key = ?", key).Scan(&got)
			require.NoError(t, err, "SELECT binary BLOB")
			require.True(t, bytes.Equal(payload, got),
				"binary payload mismatch: want %d bytes, got %d", len(payload), len(got))
		})
	}
}

// TestKV_MultiProtocol_PrefixScan inserts keys with two distinct prefixes and
// verifies that a WHERE key LIKE filter returns only the matching subset for
// each protocol.
func TestKV_MultiProtocol_PrefixScan(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			applePrefix := uniqueKey(proto, "apples")
			orangePrefix := uniqueKey(proto, "oranges")

			insertKVBatch(t, env.db, applePrefix, 30)
			insertKVBatch(t, env.db, orangePrefix, 20)

			applePattern := fmt.Sprintf("%s/%%", applePrefix)
			rows := scanAllRows(t, env.db, "SELECT key FROM kv WHERE key LIKE ?", applePattern)
			require.Len(t, rows, 30, "expected 30 apple keys for %s", proto)
			for _, r := range rows {
				k, _ := r["key"].(string)
				assert.True(t, len(k) > len(applePrefix) && k[:len(applePrefix)] == applePrefix,
					"unexpected key %q", k)
			}
		})
	}
}

// TestKV_MultiProtocol_ConcurrentWrites launches 5 goroutines per protocol,
// each writing 50 distinct keys via the SDK (to bypass SQLite's single-
// connection serialisation), then verifies all 250 keys are visible through
// the virtual table.
func TestKV_MultiProtocol_ConcurrentWrites(t *testing.T) {
	const (
		goroutines = 5
		keysEach   = 50
	)
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			ctx := context.Background()

			var wg sync.WaitGroup
			wg.Add(goroutines)
			errs := make([]error, goroutines)

			for g := 0; g < goroutines; g++ {
				g := g
				go func() {
					defer wg.Done()
					for i := 0; i < keysEach; i++ {
						key := fmt.Sprintf("%s/concurrent/%d/%d", proto, g, i)
						val := fmt.Sprintf("val-%d-%d", g, i)
						if err := env.client.KV().Put(ctx, key, []byte(val)); err != nil {
							errs[g] = fmt.Errorf("goroutine %d: Put key=%q: %w", g, key, err)
							return
						}
					}
				}()
			}
			wg.Wait()

			for g, err := range errs {
				require.NoError(t, err, "goroutine %d error", g)
			}

			prefix := fmt.Sprintf("%s/concurrent/%%", proto)
			assertRowCount(t, env.db,
				fmt.Sprintf("SELECT key FROM kv WHERE key LIKE '%s'", prefix),
				goroutines*keysEach)
		})
	}
}

// TestKV_MultiProtocol_OrderBy inserts five keys in random order and verifies
// that ORDER BY returns them in the correct sequence for each protocol.
func TestKV_MultiProtocol_OrderBy(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)

			prefix := uniqueKey(proto, "order")
			suffixes := []string{"zebra", "mango", "apple", "kiwi", "banana"}
			for _, s := range suffixes {
				key := prefix + "/" + s
				_, err := env.db.Exec("INSERT INTO kv (key, value) VALUES (?, ?)", key, key+"-val")
				require.NoError(t, err)
			}

			// Build the expected sorted order using the actual full key strings.
			allKeys := make([]string, len(suffixes))
			for i, s := range suffixes {
				allKeys[i] = prefix + "/" + s
			}
			sort.Strings(allKeys)

			pattern := prefix + "/%"
			rows := scanAllRows(t, env.db,
				fmt.Sprintf("SELECT key FROM kv WHERE key LIKE '%s' ORDER BY key ASC", pattern))
			require.Len(t, rows, len(suffixes))
			for i, r := range rows {
				got, _ := r["key"].(string)
				assert.Equal(t, allKeys[i], got, "ASC row %d", i)
			}
		})
	}
}

// TestKV_MultiProtocol_PointLookupMissing verifies that a point lookup for a
// non-existent key returns no rows (not an error) for each protocol.
func TestKV_MultiProtocol_PointLookupMissing(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			key := uniqueKey(proto, "no-such-key-xyz")
			rows := scanAllRows(t, env.db, "SELECT key, value FROM kv WHERE key = ?", key)
			require.Empty(t, rows, "missing key must return zero rows for %s", proto)
		})
	}
}

// TestKV_MultiProtocol_DeleteNonExistent verifies that deleting a non-existent
// key is a no-op (no error) for each protocol.
func TestKV_MultiProtocol_DeleteNonExistent(t *testing.T) {
	for _, proto := range allProtocols() {
		proto := proto
		t.Run(string(proto), func(t *testing.T) {
			t.Parallel()
			env := newTestEnv(t, proto)
			key := uniqueKey(proto, "phantom-delete")
			_, err := env.db.Exec("DELETE FROM kv WHERE key = ?", key)
			require.NoError(t, err, "DELETE non-existent key must not error for %s", proto)
		})
	}
}
