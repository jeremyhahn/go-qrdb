// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	// leaseExpiryTTL is the TTL of the lease TestCLIClusterLeaseExpiryNotifiesWatch
	// lets expire. The kv put that attaches the key must land inside it, one
	// CLI round trip after the grant: a 3 s TTL lost that race on a loaded
	// runner, where the grant and the put took 7.3 s between them.
	leaseExpiryTTL = 10 * time.Second

	// leaseExpiryMargin is how long past the TTL the test waits for the
	// expiry sweep to delete the key and the watch to print it. The sweep
	// (lease.Lessor.RunExpiry) ticks every lease.DefaultExpiryInterval (1s)
	// and revokes a lease once its apply-time expiry has passed, so the
	// delete is one sweep tick plus one revoke proposal past the TTL; the
	// margin leaves that proposal the same loaded-runner headroom as the
	// 7.3s grant-to-put round trip measured above.
	leaseExpiryMargin = 10 * time.Second
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

			ts := testTimestamp()
			prefix := fmt.Sprintf("__svc-test-%s-%d", proto, ts)
			key := prefix + "/instance-1"
			probeKey := prefix + "/probe"
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", probeKey) //nolint:errcheck
			})

			// Establish the prefix watch BEFORE the lease exists, so the
			// lease's countdown cannot run out while the watch is starting.
			watch := startCLIWatch(t, cfg, proto, probeKey, "watch", prefix, "--prefix")

			// Grant a lease that is never kept alive, so it expires. Its TTL
			// is the window the put below must land in: a TTL shorter than
			// one CLI round trip expires the lease before the put names it,
			// and the put fails with "lease not found".
			grantStart := time.Now()
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "lease", "grant", "--ttl", strconv.Itoa(int(leaseExpiryTTL/time.Second)))
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
				t.Fatalf("[%s] kv put --lease-id failed %s after the grant began (ttl %s): %v",
					proto, time.Since(grantStart).Round(time.Millisecond), leaseExpiryTTL, err)
			}

			// Do not keep-alive: the lease expires and the lessor deletes the
			// attached key, which the watch reports. The wait is bounded by the
			// grant, the TTL and the expiry margin, not by the watch's start.
			want := "DELETE " + key
			if output, ok := watch.await(want, grantStart.Add(leaseExpiryTTL+leaseExpiryMargin)); !ok {
				t.Fatalf("[%s] watch output does not contain %q within %s of the grant; output: %q",
					proto, want, leaseExpiryTTL+leaseExpiryMargin, output)
			}

			t.Logf("[%s] lease-expiry delete observed by watch for prefix: %s", proto, prefix)
		})
	}
}

// TestCLIMultiProtocolLeaseRePutLeavesOldLease checks, over every transport,
// that a key belongs to the lease its latest write named: a key re-put under
// a second lease, or with no lease, survives the revoke of the lease its
// earlier value named. This is the service-registry case -- an instance that
// re-registers under a fresh lease while its previous registration's lease is
// still counting down must not be deleted when that old lease ends.
func TestCLIMultiProtocolLeaseRePutLeavesOldLease(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	grant := func(t *testing.T, proto ProtocolType) string {
		t.Helper()
		stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "lease", "grant", "--ttl", "300")
		if err != nil {
			t.Fatalf("[%s] lease grant: %v\nstdout: %s\nstderr: %s", proto, err, stdout, stderr)
		}
		for _, line := range strings.Split(stdout, "\n") {
			if strings.HasPrefix(line, "ID:") {
				return strings.TrimSpace(strings.TrimPrefix(line, "ID:"))
			}
		}
		t.Fatalf("[%s] could not extract lease ID from output: %s", proto, stdout)
		return ""
	}
	run := func(t *testing.T, proto ProtocolType, args ...string) string {
		t.Helper()
		stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, args...)
		if err != nil {
			t.Fatalf("[%s] %v: %v\nstdout: %s\nstderr: %s", proto, args, err, stdout, stderr)
		}
		return stdout
	}

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			oldLease, newLease := grant(t, proto), grant(t, proto)
			ts := testTimestamp()
			moved := fmt.Sprintf("test-lease-reput-%s-%d/moved", proto, ts)
			released := fmt.Sprintf("test-lease-reput-%s-%d/released", proto, ts)
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", moved)          //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "kv", "del", released)       //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "lease", "revoke", newLease) //nolint:errcheck
				execCLIWithProtocol(t, cfg, proto, "lease", "revoke", oldLease) //nolint:errcheck
			})

			run(t, proto, "kv", "put", moved, "old", "--lease-id", oldLease)
			run(t, proto, "kv", "put", moved, "new", "--lease-id", newLease)
			run(t, proto, "kv", "put", released, "leased", "--lease-id", oldLease)
			run(t, proto, "kv", "put", released, "permanent")

			run(t, proto, "lease", "revoke", oldLease)

			assertContains(t, strings.TrimSpace(run(t, proto, "kv", "get", moved)), "new",
				fmt.Sprintf("[%s] a key re-put under another lease must survive the old lease's revoke", proto))
			assertContains(t, strings.TrimSpace(run(t, proto, "kv", "get", released)), "permanent",
				fmt.Sprintf("[%s] a key re-put without a lease must survive the old lease's revoke", proto))

			run(t, proto, "lease", "revoke", newLease)
			if stdout, _, err := execCLIWithProtocol(t, cfg, proto, "kv", "get", moved); err == nil {
				t.Fatalf("[%s] %s survived the revoke of the lease its latest value named: %s", proto, moved, stdout)
			}
		})
	}
}
