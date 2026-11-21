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
