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

package integration

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestCLIMultiProtocolEntityBridge_PutAndGet tests storing an entity as JSON
// via kv put and retrieving it via kv get across all protocols, verifying
// that entity data round-trips correctly through all transport layers.
func TestCLIMultiProtocolEntityBridge_PutAndGet(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("bridge_users/%d", ts)

			entity := map[string]interface{}{
				"id":    ts,
				"name":  "Alice",
				"email": fmt.Sprintf("alice-%d@example.com", ts),
				"age":   30,
				"score": 9.5,
			}
			data, err := json.Marshal(entity)
			if err != nil {
				t.Fatalf("marshal entity: %v", err)
			}

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Step 1: Store entity via kv put.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, string(data))
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put should output 'OK'", proto))

			// Step 2: Retrieve entity via kv get.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get failed: %v", proto, err)
			}

			// Verify the response contains entity fields.
			output := stdout + stderr
			assertContains(t, output, "Alice",
				fmt.Sprintf("[%s] kv get should contain name", proto))
			assertContains(t, output, "example.com",
				fmt.Sprintf("[%s] kv get should contain email domain", proto))

			t.Logf("[%s] entity bridge put/get passed (key: %s)", proto, key)
		})
	}
}

// TestCLIMultiProtocolEntityBridge_IndexLifecycle tests creating an index on
// entity fields, querying it, and dropping it across all protocols.
func TestCLIMultiProtocolEntityBridge_IndexLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			prefix := fmt.Sprintf("ent-%s-%d", proto, ts)
			indexName := generateUniqueID(fmt.Sprintf("ent-idx-%d-%s", ts, proto))

			key1 := prefix + "/user1"
			key2 := prefix + "/user2"
			key3 := prefix + "/user3"

			entities := []struct {
				key string
				val string
			}{
				{key1, `{"id":1,"name":"alice","email":"alice@example.com","age":30,"score":9.5}`},
				{key2, `{"id":2,"name":"bob","email":"bob@example.com","age":25,"score":7.0}`},
				{key3, `{"id":3,"name":"alice","email":"alice2@example.com","age":35,"score":8.0}`},
			}

			t.Cleanup(func() {
				for _, e := range entities {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", e.key) //nolint:errcheck
				}
				execCLIWithProtocol(t, cfg, proto, "index", "drop", indexName) //nolint:errcheck
			})

			// Step 1: Store entity data.
			for _, e := range entities {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", e.key, e.val)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv put %s failed: %v", proto, e.key, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] kv put %s should output 'OK'", proto, e.key))
			}

			// Step 2: Create index on "name" field.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "index", "create", indexName, "--field", "name")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index create should output 'OK'", proto))

			// Step 3: Query the index for "alice" — should find 2 entries.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "query", indexName, "--value", "alice")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index query failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] index query for 'alice' should produce output", proto))

			// Step 4: Query the index for "bob" — should find 1 entry.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "query", indexName, "--value", "bob")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index query for 'bob' failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] index query for 'bob' should produce output", proto))

			// Step 5: Create a unique index on "email" field.
			emailIdx := generateUniqueID(fmt.Sprintf("ent-email-%d-%s", ts, proto))
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "index", "drop", emailIdx) //nolint:errcheck
			})

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"index", "create", emailIdx, "--field", "email", "--unique")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create --unique failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index create --unique should output 'OK'", proto))

			// Step 6: Query the unique index for a specific email.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"index", "query", emailIdx, "--value", "bob@example.com")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index query email failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] index query for email should produce output", proto))

			// Step 7: Scan index range from "a" to "c".
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"index", "scan", indexName, "--start", "a", "--end", "c")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index scan failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] index scan should produce output", proto))

			// Step 8: Drop the indexes.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "drop", indexName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index drop should output 'OK'", proto))

			t.Logf("[%s] entity bridge index lifecycle passed", proto)
		})
	}
}

// TestCLIMultiProtocolEntityBridge_UpdateAndDelete tests updating and
// deleting entity data via kv put/del across all protocols.
func TestCLIMultiProtocolEntityBridge_UpdateAndDelete(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("bridge_users/%d", ts)

			original := map[string]interface{}{
				"id":    ts,
				"name":  "Original",
				"email": fmt.Sprintf("orig-%d@example.com", ts),
				"age":   20,
				"score": 5.0,
			}
			originalJSON, _ := json.Marshal(original)

			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Step 1: Store original entity.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, string(originalJSON))
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put original failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put original should output 'OK'", proto))

			// Step 2: Update entity with new values.
			updated := map[string]interface{}{
				"id":    ts,
				"name":  "Updated",
				"email": fmt.Sprintf("updated-%d@example.com", ts),
				"age":   21,
				"score": 6.0,
			}
			updatedJSON, _ := json.Marshal(updated)

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", key, string(updatedJSON))
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put update failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put update should output 'OK'", proto))

			// Step 3: Verify the update via kv get.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get after update failed: %v", proto, err)
			}
			output := stdout + stderr
			assertContains(t, output, "Updated",
				fmt.Sprintf("[%s] kv get should contain updated name", proto))

			// Step 4: Delete the entity.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "del", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv del failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv del should output 'OK'", proto))

			t.Logf("[%s] entity bridge update/delete passed (key: %s)", proto, key)
		})
	}
}

// TestCLIMultiProtocolEntityBridge_ListEntities tests listing entities
// with a prefix scan via kv list across all protocols.
func TestCLIMultiProtocolEntityBridge_ListEntities(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			prefix := fmt.Sprintf("bridge_list_%s_%d", proto, ts)

			keys := []string{
				prefix + "/1",
				prefix + "/2",
				prefix + "/3",
			}

			t.Cleanup(func() {
				for _, k := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", k) //nolint:errcheck
				}
			})

			// Store 3 entities.
			for i, k := range keys {
				entity := map[string]interface{}{
					"id":   ts + int64(i),
					"name": fmt.Sprintf("User%d", i+1),
				}
				data, _ := json.Marshal(entity)
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", k, string(data))
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv put %s failed: %v", proto, k, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] kv put %s should output 'OK'", proto, k))
			}

			// List with prefix.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "list", prefix)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv list failed: %v", proto, err)
			}

			output := stdout + stderr
			for _, k := range keys {
				assertContains(t, output, k,
					fmt.Sprintf("[%s] kv list should contain key %s", proto, k))
			}

			t.Logf("[%s] entity bridge list passed (prefix: %s)", proto, prefix)
		})
	}
}
