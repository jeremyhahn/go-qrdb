// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIBackendPersistence tests that data written through the storage
// backend is persistent and readable after write. It writes a batch of
// keys, reads them back to verify correctness, lists them to confirm
// visibility, and finally cleans up. This exercises the storage backend
// under moderate load across all protocols.
func TestCLIBackendPersistence(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	const keyCount = 10

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("persist-%s-%d", proto, testTimestamp())
			keys := make([]string, keyCount)
			values := make([]string, keyCount)

			for i := 0; i < keyCount; i++ {
				keys[i] = fmt.Sprintf("%s/key-%03d", prefix, i)
				values[i] = fmt.Sprintf("persist-value-%03d", i)
			}

			// Cleanup all keys after test.
			t.Cleanup(func() {
				for _, key := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
				}
			})

			// Step 1: Write all keys.
			for i, key := range keys {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, values[i])
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv put %s failed: %v", proto, key, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] kv put %s should output 'OK'", proto, key))
			}

			// Step 2: Read all keys back and verify values match.
			for i, key := range keys {
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv get %s failed: %v", proto, key, err)
				}
				assertContains(t, strings.TrimSpace(stdout), values[i],
					fmt.Sprintf("[%s] kv get %s should return stored value", proto, key))
			}

			// Step 3: List with prefix and verify all keys are present.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "list", prefix)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv list %s failed: %v", proto, prefix, err)
			}

			output := stdout + stderr
			for _, key := range keys {
				assertContains(t, output, key,
					fmt.Sprintf("[%s] kv list should contain key %s", proto, key))
			}

			// Step 4: Delete all keys.
			for _, key := range keys {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "del", key)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv del %s failed: %v", proto, key, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[%s] kv del %s should output 'OK'", proto, key))
			}

			// Verify all keys are deleted.
			for _, key := range keys {
				stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "exists", key)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv exists %s after delete failed: %v", proto, key, err)
				}
				assertEqual(t, "false", strings.TrimSpace(stdout),
					fmt.Sprintf("[%s] kv exists %s should return 'false' after delete", proto, key))
			}

			t.Logf("[%s] backend persistence test passed for %d keys", proto, keyCount)
		})
	}
}
