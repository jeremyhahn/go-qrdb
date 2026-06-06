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

// TestCLIMultiProtocolLeaseLifecycle tests the full lease lifecycle
// (grant, ttl, keep-alive, list, revoke) across all protocols.
func TestCLIMultiProtocolLeaseLifecycle(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Step 1: Grant a lease with 300s TTL.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "lease", "grant", "--ttl", "300")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease grant failed: %v", proto, err)
			}

			// Extract lease ID from output.
			var leaseID string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					leaseID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if leaseID == "" {
				t.Fatalf("[%s] could not extract lease ID from output: %s", proto, stdout)
			}

			// Register cleanup to revoke the lease in case of early failure.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "lease", "revoke", leaseID) //nolint:errcheck
			})

			// Step 2: Check TTL of the lease.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "lease", "ttl", leaseID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease ttl failed: %v", proto, err)
			}
			assertContains(t, stdout, "TTL:",
				fmt.Sprintf("[%s] lease ttl should contain 'TTL:'", proto))
			assertContains(t, stdout, "GrantedTTL:",
				fmt.Sprintf("[%s] lease ttl should contain 'GrantedTTL:'", proto))

			// Step 3: Keep-alive the lease.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "lease", "keep-alive", leaseID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease keep-alive failed: %v", proto, err)
			}
			assertContains(t, stdout, "ID:",
				fmt.Sprintf("[%s] lease keep-alive should contain 'ID:'", proto))

			// Step 4: List leases and verify our lease is present.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "lease", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease list failed: %v", proto, err)
			}
			assertContains(t, stdout, leaseID,
				fmt.Sprintf("[%s] lease list should contain lease ID %s", proto, leaseID))

			// Step 5: Revoke the lease.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "lease", "revoke", leaseID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease revoke failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] lease revoke should output 'OK'", proto))

			// Step 6: Verify lease is no longer listed.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "lease", "list")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease list after revoke failed: %v", proto, err)
			}
			if strings.Contains(stdout, leaseID) {
				t.Fatalf("[%s] lease list should not contain revoked lease ID %s, got: %s", proto, leaseID, stdout)
			}

			t.Logf("[%s] lease lifecycle passed for lease ID: %s", proto, leaseID)
		})
	}
}

// TestCLIMultiProtocolLeaseWithKV tests attaching a lease to a KV entry
// across all protocols, verifying the key is accessible while the lease
// is active.
func TestCLIMultiProtocolLeaseWithKV(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Step 1: Grant a lease.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "lease", "grant", "--ttl", "300")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease grant failed: %v", proto, err)
			}

			// Extract lease ID from output.
			var leaseID string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					leaseID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if leaseID == "" {
				t.Fatalf("[%s] could not extract lease ID from output: %s", proto, stdout)
			}

			key := fmt.Sprintf("test-lease-key-%s-%d", proto, testTimestamp())
			value := "test-value"

			// Register cleanup to delete the key and revoke the lease.
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key)           //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "lease", "revoke", leaseID) //nolint:errcheck
			})

			// Step 2: Put a key with the lease attached.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", key, value, "--lease-id", leaseID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put --lease-id failed: %v", proto, err)
			}
			assertContains(t, stdout, "OK",
				fmt.Sprintf("[%s] kv put --lease-id should output 'OK'", proto))

			// Step 3: Get the key and verify the value.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "get", key)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv get failed: %v", proto, err)
			}
			assertContains(t, strings.TrimSpace(stdout), value,
				fmt.Sprintf("[%s] kv get should return stored value", proto))

			t.Logf("[%s] lease with KV passed for key: %s, lease: %s", proto, key, leaseID)
		})
	}
}

// TestCLIClusterLeaseExpiryNotifiesWatch validates the exact primitive
// combination a service registry (e.g. go-quicbaas) relies on over a network
// transport: a key attached to a short-lived lease is automatically deleted
// when the lease expires (no keep-alive), and a prefix watch observes that
// DELETE. This is the "node death -> lease expiry -> auto-delete -> watchers
// notified" path, exercised end-to-end across each streaming-capable protocol.
func TestCLIClusterLeaseExpiryNotifiesWatch(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range watchSupportedProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Grant a short-lived lease (no keep-alive -> it will expire).
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "lease", "grant", "--ttl", "3")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease grant failed: %v", proto, err)
			}
			var leaseID string
			for _, line := range strings.Split(stdout, "\n") {
				if strings.HasPrefix(line, "ID:") {
					leaseID = strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
					break
				}
			}
			if leaseID == "" {
				t.Fatalf("[%s] could not extract lease ID from output: %s", proto, stdout)
			}

			ts := testTimestamp()
			prefix := fmt.Sprintf("__svc-test-%s-%d", proto, ts)
			key := prefix + "/instance-1"
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", key)           //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "lease", "revoke", leaseID) //nolint:errcheck
			})

			// Register the instance: put the key under the watched prefix with
			// the lease attached.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "kv", "put", key, "instance-addr", "--lease-id", leaseID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv put --lease-id failed: %v", proto, err)
			}

			// Start a prefix watch BEFORE the lease expires so it observes the
			// expiry-driven delete.
			buf, cancel := execCLIBackground(t, cfg, proto, 20*time.Second, "watch", prefix, "--prefix")

			// Do not keep-alive: wait for the lease (ttl=3s) to expire and the
			// lessor to delete the attached key, plus propagation margin.
			time.Sleep(10 * time.Second)

			cancel()
			output := buf.String()

			assertContains(t, output, "DELETE",
				fmt.Sprintf("[%s] watch should observe a DELETE when the lease expires; output: %s", proto, output))

			t.Logf("[%s] lease-expiry delete observed by watch for prefix: %s", proto, prefix)
		})
	}
}
