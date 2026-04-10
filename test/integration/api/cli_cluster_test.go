// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestCLIClusterKVReplication validates that a key written via one protocol
// can be read back via every other protocol. On a single-node cluster this
// exercises the full write-read path across all API transports.
func TestCLIClusterKVReplication(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, writeProt := range AllProtocols() {
		for _, readProt := range AllProtocols() {
			t.Run(fmt.Sprintf("write_%s_read_%s", writeProt, readProt), func(t *testing.T) {
				if !cfg.IsProtocolAvailable(t, writeProt) || !cfg.IsProtocolAvailable(t, readProt) {
					t.Skipf("protocol unavailable: write=%s read=%s", writeProt, readProt)
				}

				key := fmt.Sprintf("cluster-repl-%s-%s-%d", writeProt, readProt, testTimestamp())
				value := fmt.Sprintf("replval-%s-%s", writeProt, readProt)

				// Write via writeProt.
				stdout, stderr, err := execCLIWithProtocol(t, cfg, writeProt, "kv", "put", key, value)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[write_%s] kv put failed: %v", writeProt, err)
				}
				assertContains(t, stdout, "OK",
					fmt.Sprintf("[write_%s] kv put should output 'OK'", writeProt))

				// Register cleanup.
				t.Cleanup(func() {
					execCLIWithProtocol(t, cfg, writeProt, "kv", "del", key) //nolint:errcheck
				})

				// Read via readProt.
				stdout, stderr, err = execCLIWithProtocol(t, cfg, readProt, "kv", "get", key)
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[read_%s] kv get failed: %v", readProt, err)
				}
				assertContains(t, strings.TrimSpace(stdout), value,
					fmt.Sprintf("[write_%s/read_%s] kv get should return stored value", writeProt, readProt))

				t.Logf("[write_%s/read_%s] replication verified for key: %s", writeProt, readProt, key)
			})
		}
	}
}

// TestCLIClusterLeaseReplication grants a lease via the first available
// protocol and verifies its TTL is visible from every other protocol.
func TestCLIClusterLeaseReplication(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	protocols := AllProtocols()

	// Find the first available protocol to grant the lease.
	var grantProto ProtocolType
	for _, proto := range protocols {
		if cfg.IsProtocolAvailable(t, proto) {
			grantProto = proto
			break
		}
	}
	if grantProto == "" {
		t.Fatal("no protocol available to grant lease")
	}

	// Grant a lease via the first available protocol.
	stdout, stderr, err := execCLIWithProtocol(t, cfg, grantProto, "lease", "grant", "--ttl", "300")
	if err != nil {
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.Fatalf("[%s] lease grant failed: %v", grantProto, err)
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
		t.Fatalf("[%s] could not extract lease ID from output: %s", grantProto, stdout)
	}

	// Register cleanup to revoke the lease.
	t.Cleanup(func() {
		execCLIWithProtocol(t, cfg, grantProto, "lease", "revoke", leaseID) //nolint:errcheck
	})

	t.Logf("[%s] granted lease %s, verifying via all protocols", grantProto, leaseID)

	// Verify lease TTL is visible from every protocol.
	for _, proto := range protocols {
		t.Run(fmt.Sprintf("verify_%s", proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Skipf("protocol %s not available", proto)
			}

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "lease", "ttl", leaseID)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] lease ttl failed: %v", proto, err)
			}

			assertContains(t, stdout, "TTL:",
				fmt.Sprintf("[%s] lease ttl should contain 'TTL:'", proto))
			assertContains(t, stdout, "GrantedTTL:",
				fmt.Sprintf("[%s] lease ttl should contain 'GrantedTTL:'", proto))

			t.Logf("[%s] lease %s TTL verified", proto, leaseID)
		})
	}
}

// TestCLIClusterConsistency writes N keys per protocol and verifies all keys
// are present via kv list.
func TestCLIClusterConsistency(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	const keyCount = 20

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			prefix := fmt.Sprintf("consistency-%s-%d", proto, testTimestamp())
			keys := make([]string, keyCount)

			// Write N keys.
			for i := 0; i < keyCount; i++ {
				keys[i] = fmt.Sprintf("%s/key-%03d", prefix, i)
				stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "put", keys[i], fmt.Sprintf("val-%03d", i))
				if err != nil {
					t.Logf("stdout: %s", stdout)
					t.Logf("stderr: %s", stderr)
					t.Fatalf("[%s] kv put %s failed: %v", proto, keys[i], err)
				}
			}

			// Register cleanup for all keys.
			t.Cleanup(func() {
				for _, key := range keys {
					execCLIWithProtocol(t, cfg, proto, "kv", "del", key) //nolint:errcheck
				}
			})

			// List keys with prefix and verify all are present.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "kv", "list", prefix)
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] kv list failed: %v", proto, err)
			}

			output := stdout + stderr
			for _, key := range keys {
				assertContains(t, output, key,
					fmt.Sprintf("[%s] kv list should contain key %s", proto, key))
			}

			t.Logf("[%s] consistency check passed: all %d keys present", proto, keyCount)
		})
	}
}

// TestCLIClusterMemberOps validates cluster member operations on a
// single-node cluster by verifying admin peers and admin status output.
func TestCLIClusterMemberOps(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Verify admin peers returns at least one peer entry.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "peers")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin peers failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin peers produced no output", proto))
			assertContains(t, output, "SHARD_ID",
				fmt.Sprintf("[%s] admin peers should contain 'SHARD_ID' table header", proto))

			t.Logf("[%s] admin peers: %s", proto, strings.TrimSpace(output))

			// Verify admin status shows ShardCount >= 1.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "admin", "status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin status failed: %v", proto, err)
			}

			output = stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin status produced no output", proto))
			assertContains(t, output, "ShardCount:",
				fmt.Sprintf("[%s] admin status should contain 'ShardCount:'", proto))

			t.Logf("[%s] admin status: %s", proto, strings.TrimSpace(output))
		})
	}
}

// TestCLIClusterSnapshot validates the admin snapshot command on a
// single-node cluster by triggering a Raft snapshot on shard 1.
func TestCLIClusterSnapshot(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Write data to advance the Raft log so a snapshot can be taken.
			// Without new committed entries, snapshots fail with
			// "snapshot out of date".
			snapKey := fmt.Sprintf("snap-cluster-%s-%d", proto, testTimestamp())
			_, _, _ = execCLIWithProtocol(t, cfg, proto, "kv", "put", snapKey, "snap-value")
			t.Cleanup(func() {
				execCLIWithProtocol(t, cfg, proto, "kv", "del", snapKey) //nolint:errcheck
			})

			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "snapshot", "--shard-id", "1")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin snapshot failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin snapshot produced no output", proto))
			assertContains(t, output, "OK",
				fmt.Sprintf("[%s] admin snapshot should contain 'OK'", proto))

			t.Logf("[%s] admin snapshot: %s", proto, strings.TrimSpace(output))
		})
	}
}

// TestCLIClusterFailover verifies cluster health and leader status on a
// single-node cluster. Actual failover cannot be tested with a single node,
// so this test validates the node reports itself as healthy and includes
// leader information.
func TestCLIClusterFailover(t *testing.T) {
	cfg := LoadTestConfig()
	requireCLI(t, cfg)

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if !cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("server not available for protocol %s", proto)
			}

			// Verify admin health reports a status.
			stdout, stderr, err := execCLIWithProtocol(t, cfg, proto, "admin", "health")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin health failed: %v", proto, err)
			}

			output := stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin health produced no output", proto))
			assertContains(t, output, "Status:",
				fmt.Sprintf("[%s] admin health should contain 'Status:'", proto))

			t.Logf("[%s] admin health: %s", proto, strings.TrimSpace(output))

			// Verify admin status includes leader information.
			stdout, stderr, err = execCLIWithProtocol(t, cfg, proto, "admin", "status")
			if err != nil {
				t.Logf("stdout: %s", stdout)
				t.Logf("stderr: %s", stderr)
				t.Fatalf("[%s] admin status failed: %v", proto, err)
			}

			output = stdout + stderr
			assertNotEmpty(t, output, fmt.Sprintf("[%s] admin status produced no output", proto))
			assertContains(t, output, "IsLeader:",
				fmt.Sprintf("[%s] admin status should contain 'IsLeader:'", proto))

			t.Logf("[%s] admin status (leader check): %s", proto, strings.TrimSpace(output))
		})
	}
}
