// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestCLIEngineKV tests basic KV engine operations (non-MVCC) across all
// protocols. It verifies the complete put/get/exists/list/del lifecycle to
// confirm the KV engine processes commands correctly.
func TestCLIEngineKV(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("eng-kv-%s-%d", proto, ts)
			value := fmt.Sprintf("eng-val-%s", proto)

			// Register cleanup before any mutations so it always runs.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Step 1: Put a key-value pair.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put should output 'OK'", proto))

			// Step 2: Get and verify value.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get failed: %v", proto, err)
			}
			assertContains(t, strings.TrimSpace(stdout), value,
				fmt.Sprintf("[%s] kv get should return stored value", proto))

			// Step 3: Exists should return "true".
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "exists", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv exists failed: %v", proto, err)
			}
			assertEqual(t, "true", strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv exists should return 'true'", proto))

			// Step 4: List with key prefix should include the key.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "list", fmt.Sprintf("eng-kv-%s-", proto))
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv list failed: %v", proto, err)
			}
			assertContains(t, stdout, key,
				fmt.Sprintf("[%s] kv list should contain the key", proto))

			// Step 5: Delete the key.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "del", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv del failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv del should output 'OK'", proto))

			// Step 6: Exists should return "false" after deletion.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "exists", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv exists after delete failed: %v", proto, err)
			}
			assertEqual(t, "false", strings.TrimSpace(stdout),
				fmt.Sprintf("[%s] kv exists should return 'false' after delete", proto))

			t.Logf("[%s] KV engine lifecycle passed for key: %s", proto, key)
		})
	}
}

// TestCLIEngineMVCC tests MVCC-specific features across all protocols. It
// verifies revision tracking, revision-aware get, range queries with MVCC
// metadata, and compaction.
func TestCLIEngineMVCC(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("eng-mvcc-%s-%d", proto, ts)
			value := "mvcc-engine-value"

			// Register cleanup before any mutations so it always runs.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Step 1: Put a key to create an MVCC revision.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put should output 'OK'", proto))

			// Step 2: Verify kv revision returns a number.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "revision")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv revision failed: %v", proto, err)
			}
			revOutput := strings.TrimSpace(stdout)
			assertNotEmpty(t, revOutput,
				fmt.Sprintf("[%s] kv revision should return a revision number", proto))

			// Step 3: Get with --revision flag to verify MVCC metadata.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key, "--revision")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get --revision failed: %v", proto, err)
			}
			assertContains(t, stdout, "Key:",
				fmt.Sprintf("[%s] kv get --revision should show Key:", proto))
			assertContains(t, stdout, "CreateRevision:",
				fmt.Sprintf("[%s] kv get --revision should show CreateRevision:", proto))

			// Step 4: Range query with prefix to verify MVCC columns.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "range", fmt.Sprintf("eng-mvcc-%s-", proto))
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv range failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] kv range should produce output", proto))
			assertContains(t, output, key,
				fmt.Sprintf("[%s] kv range should contain the key", proto))

			// Step 5: Compact at the revision this subtest's put advanced the
			// store to. Compact is monotonic (service.MVCCKVService's
			// RevisionCompactedError), and every protocol subtest shares one
			// long-lived server, so a revision fixed in the source would only
			// ever succeed for whichever subtest ran first; revOutput (read
			// in step 2, after this subtest's own put, with no further
			// mutation since) is always ahead of the current floor.
			if _, err := strconv.ParseInt(revOutput, 10, 64); err != nil {
				t.Fatalf("[%s] kv revision did not return an integer: %q", proto, revOutput)
			}
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "compact", "--revision", revOutput)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv compact failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv compact should output 'OK'", proto))

			// Step 6: Delete the key.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "del", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv del failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv del should output 'OK'", proto))

			t.Logf("[%s] MVCC engine lifecycle passed for key: %s", proto, key)
		})
	}
}

// TestCLIEngineTimeseries tests timeseries engine features across all protocols.
// It verifies write, query, and aggregate operations. This test is feature-gated
// and will be skipped if the CLI binary does not support the ts command.
func TestCLIEngineTimeseries(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "ts")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			metric := fmt.Sprintf("eng-ts-%s-%d", proto, ts)

			// Step 1: Write a data point.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"ts", "write", "--metric", metric, "--value", "100.0")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts write failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts write should produce output", proto))

			// Step 2: Query the metric range.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "query", "--metric", metric, "--start", "0", "--end", "9223372036854775807")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts query failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts query should produce output", proto))

			// Step 3: Aggregate with count function.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"ts", "aggregate", "--metric", metric, "--start", "0", "--end", "9223372036854775807", "--function", "count")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] ts aggregate count failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] ts aggregate count should produce output", proto))

			t.Logf("[%s] timeseries engine lifecycle passed (metric: %s)", proto, metric)
		})
	}
}

// TestCLIEngineVector tests vector engine features across all protocols. It
// verifies namespace create, list, and delete operations. This test is
// feature-gated and will be skipped if the CLI binary does not support the
// vector command.
func TestCLIEngineVector(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)
	requireFeature(t, cfg, "vector")

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			namespace := fmt.Sprintf("eng-vec-%s-%d", proto, ts)

			// Register cleanup before any mutations so it always runs.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "vector", "namespace", "delete", "--namespace", namespace) //nolint:errcheck
			})

			// Step 1: Create a vector namespace with dimension 3.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "create",
				"--namespace", namespace,
				"--dimension", "3")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace create failed: %v", proto, err)
			}
			output := stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector namespace create should produce output", proto))

			// Step 2: List namespaces and verify the new namespace is present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace list failed: %v", proto, err)
			}
			output = stdout + stderr
			assertContains(t, output, namespace,
				fmt.Sprintf("[%s] vector namespace list should contain %s", proto, namespace))

			// Step 3: Delete the namespace.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto,
				"vector", "namespace", "delete",
				"--namespace", namespace)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] vector namespace delete failed: %v", proto, err)
			}
			output = stdout + stderr
			assertNotEmpty(t, output,
				fmt.Sprintf("[%s] vector namespace delete should produce output", proto))

			t.Logf("[%s] vector engine lifecycle passed (namespace: %s)", proto, strings.TrimSpace(namespace))
		})
	}
}
