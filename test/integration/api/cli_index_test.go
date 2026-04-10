// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"testing"
)

// TestCLIMultiProtocolIndexLifecycle tests the full index lifecycle (create,
// query, scan, drop) across all protocols. It first inserts KV data with JSON
// values containing indexable fields, then exercises every index subcommand.
func TestCLIMultiProtocolIndexLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			kvPrefix := fmt.Sprintf("idx-%s-%d", proto, ts)
			indexName := generateUniqueID(fmt.Sprintf("idx-%d-%s", ts, proto))

			key1 := kvPrefix + "/user1"
			key2 := kvPrefix + "/user2"
			val1 := `{"name":"alice","age":"30"}`
			val2 := `{"name":"bob","age":"25"}`

			// Register cleanup before any mutations so it always runs.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key1)          //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key2)          //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "index", "drop", indexName) //nolint:errcheck
			})

			// Step 1: Insert KV data with JSON values containing indexable fields.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key1, val1)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put %s failed: %v", proto, key1, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put %s should output 'OK'", proto, key1))

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", key2, val2)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put %s failed: %v", proto, key2, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put %s should output 'OK'", proto, key2))

			// Step 2: Create an index on the "name" field.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "create", indexName, "--field", "name")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index create should output 'OK'", proto))

			// Step 3: Query the index for value "alice".
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "query", indexName, "--value", "alice")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index query failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] index query should produce output", proto))

			// Step 4: Scan the index from "a" to "z".
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "scan", indexName, "--start", "a", "--end", "z")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index scan failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] index scan should produce output", proto))

			// Step 5: Drop the index.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "drop", indexName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index drop should output 'OK'", proto))

			t.Logf("[%s] index lifecycle passed (index: %s, keys: %s, %s)",
				proto, indexName, key1, key2)
		})
	}
}

// TestCLIMultiProtocolIndexCreateFlags tests the --unique and --sparse flags
// on index create across all protocols.
func TestCLIMultiProtocolIndexCreateFlags(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()

			// Test --unique flag.
			uniqueIdx := generateUniqueID(fmt.Sprintf("uniq-%d-%s", ts, proto))
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "index", "drop", uniqueIdx) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"index", "create", uniqueIdx, "--field", "email", "--unique")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create --unique failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index create --unique should output 'OK'", proto))

			// Test --sparse flag.
			sparseIdx := generateUniqueID(fmt.Sprintf("sparse-%d-%s", ts, proto))
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "index", "drop", sparseIdx) //nolint:errcheck
			})

			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"index", "create", sparseIdx, "--field", "optional_field", "--sparse")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create --sparse failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index create --sparse should output 'OK'", proto))

			t.Logf("[%s] index create flags passed", proto)
		})
	}
}

// TestCLIMultiProtocolIndexCreateUniqueSparse tests the combined --unique
// and --sparse flags on index create across all protocols.
func TestCLIMultiProtocolIndexCreateUniqueSparse(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			indexName := generateUniqueID(fmt.Sprintf("uniqsparse-%d-%s", ts, proto))

			// Register cleanup to drop the index in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "index", "drop", indexName) //nolint:errcheck
			})

			// Create an index with both --unique and --sparse flags.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"index", "create", indexName, "--field", "email", "--unique", "--sparse")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index create --unique --sparse failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index create --unique --sparse should output 'OK'", proto))

			// Drop the index.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "index", "drop", indexName)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] index drop failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] index drop should output 'OK'", proto))

			t.Logf("[%s] index create unique+sparse passed (index: %s)", proto, indexName)
		})
	}
}
