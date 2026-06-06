// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// watchSupportedProtocols returns protocols that support the watch streaming API.
// QUIC now implements true server-streaming watch (handleWatchStream), so it is
// validated alongside Unix/REST/gRPC. MCP exposes watch only as a bounded-poll
// snapshot (request/response transport) and is covered by its own test.
func watchSupportedProtocols() []ProtocolType {
	return []ProtocolType{ProtocolUnix, ProtocolREST, ProtocolGRPC, ProtocolQUIC}
}

// TestCLIMultiProtocolWatchKey tests watching a single key for put events
// across protocols that support streaming. It starts a background watch,
// triggers a put, and verifies the watch output contains the event.
func TestCLIMultiProtocolWatchKey(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range watchSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("watch-key-%s-%d", proto, ts)

			// Cleanup the key after the test.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Start background watch on the key.
			buf, cancel := execCLIBackground(t, cfg, proto, 10*time.Second, "watch", key)
			time.Sleep(1 * time.Second)

			// Trigger a put event.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "watched-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}

			// Wait for event propagation.
			time.Sleep(2 * time.Second)

			// Cancel the background watch and read output.
			cancel()
			output := buf.String()

			assertContains(t, output, "PUT",
				fmt.Sprintf("[%s] watch output should contain 'PUT'", proto))
			assertContains(t, output, "watched-value",
				fmt.Sprintf("[%s] watch output should contain 'watched-value'", proto))

			t.Logf("[%s] watch key passed for key: %s", proto, key)
		})
	}
}

// TestCLIMultiProtocolWatchPrefix tests watching a key prefix for put events
// across protocols that support streaming. It starts a background watch with
// --prefix, triggers a put on a sub-key, and verifies the watch output
// contains the event.
func TestCLIMultiProtocolWatchPrefix(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range watchSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			prefix := fmt.Sprintf("watch-pfx-%s-%d", proto, ts)
			childKey := prefix + "/child"

			// Cleanup the child key after the test.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", childKey) //nolint:errcheck
			})

			// Start background watch on the prefix.
			buf, cancel := execCLIBackground(t, cfg, proto, 10*time.Second, "watch", prefix, "--prefix")
			time.Sleep(1 * time.Second)

			// Trigger a put event on a sub-key.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", childKey, "child-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put failed: %v", proto, err)
			}

			// Wait for event propagation.
			time.Sleep(2 * time.Second)

			// Cancel the background watch and read output.
			cancel()
			output := buf.String()

			assertContains(t, output, "PUT",
				fmt.Sprintf("[%s] watch prefix output should contain 'PUT'", proto))
			assertContains(t, output, "child-value",
				fmt.Sprintf("[%s] watch prefix output should contain 'child-value'", proto))

			t.Logf("[%s] watch prefix passed for prefix: %s", proto, prefix)
		})
	}
}

// TestCLIMultiProtocolWatchPrevKV tests watching a key with --prev-kv to
// verify previous key-value pairs are included in watch events across
// protocols that support streaming.
func TestCLIMultiProtocolWatchPrevKV(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range watchSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("watch-prev-%s-%d", proto, ts)

			// Cleanup the key after the test.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Put the initial value.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "old-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put initial value failed: %v", proto, err)
			}

			// Start background watch with --prev-kv.
			buf, cancel := execCLIBackground(t, cfg, proto, 10*time.Second, "watch", key, "--prev-kv")
			time.Sleep(1 * time.Second)

			// Put the new value to trigger the watch event.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "new-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put new value failed: %v", proto, err)
			}

			// Wait for event propagation.
			time.Sleep(2 * time.Second)

			// Cancel the background watch and read output.
			cancel()
			output := buf.String()

			assertContains(t, output, "old-value",
				fmt.Sprintf("[%s] watch --prev-kv output should contain previous value 'old-value'", proto))

			t.Logf("[%s] watch --prev-kv passed for key: %s", proto, key)
		})
	}
}

// TestCLIMultiProtocolWatchStartRevision tests watching with --start-revision
// to replay events from a specific revision across protocols that support
// streaming. It advances the revision with a put, captures the current
// revision, starts a watch from that revision, triggers another put, and
// verifies the watch output captures the new event.
func TestCLIMultiProtocolWatchStartRevision(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range watchSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			ts := testTimestamp()
			key := fmt.Sprintf("watch-rev-%s-%d", proto, ts)

			// Cleanup the key after the test.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
			})

			// Put an initial value to advance the revision.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "initial-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put initial value failed: %v", proto, err)
			}

			// Get the current revision.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "revision")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv revision failed: %v", proto, err)
			}
			rev := strings.TrimSpace(stdout + stderr)
			assertNotEmpty(t, rev,
				fmt.Sprintf("[%s] kv revision should return a revision number", proto))

			// Start background watch with --start-revision.
			buf, cancel := execCLIBackground(t, cfg, proto, 10*time.Second,
				"watch", key, "--start-revision", rev)
			time.Sleep(1 * time.Second)

			// Put a new value to trigger a watch event.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "revision-value")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put revision value failed: %v", proto, err)
			}

			// Wait for event propagation.
			time.Sleep(2 * time.Second)

			// Cancel the background watch and read output.
			cancel()
			output := buf.String()

			assertContains(t, output, "PUT",
				fmt.Sprintf("[%s] watch --start-revision output should contain 'PUT'", proto))
			assertContains(t, output, "revision-value",
				fmt.Sprintf("[%s] watch --start-revision output should contain 'revision-value'", proto))

			t.Logf("[%s] watch --start-revision passed for key: %s (rev: %s)", proto, key, rev)
		})
	}
}
