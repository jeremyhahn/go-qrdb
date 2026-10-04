// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

package integration_test

// The replica-forwarding suites' cluster: real qrdbd nodes, one raft tenant
// whose key every node holds, and callers authenticated as that tenant.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

const (
	fwdTrustDomain = "qrdb.cluster"
	fwdTenantID    = "fwd-t1"
	fwdPassphrase  = "integration-test-replica-forwarding-barrier-secret"

	// fwdWriter may read and write keys; fwdNoKV may only list databases.
	fwdWriter = "admin"
	fwdNoKV   = "lister"
)

// fwdNode is one running node.
type fwdNode struct {
	cmd      *exec.Cmd
	raftAddr string
	admin    int
	grpc     int
	quic     int
	mcp      int
	stopped  bool
}

// fwdCluster is a running cluster with one raft tenant.
type fwdCluster struct {
	ca    *testCA
	nodes []*fwdNode
	sys   *http.Client

	// creator is the node the tenant was created through: until placement
	// hands the tenant's key to a node, only it holds the tenant.
	creator int

	// writerCert/writerKey authenticate fwdWriter; noKVCert/noKVKey
	// authenticate fwdNoKV; otherCert/otherKey a caller from a tenant that
	// does not exist.
	writerCert, writerKey string
	noKVCert, noKVKey     string
	otherCert, otherKey   string
	tenant                *http.Client
}

// startFwdCluster starts and unseals n nodes, creates the raft tenant with a
// writer and a caller without key permissions, and places one database on
// every node so every node holds the tenant's key and can authenticate its
// callers.
func startFwdCluster(t *testing.T, n int) *fwdCluster {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	raftPorts := allocatePorts(t, n)
	adminPorts := allocateTCPPorts(t, n)
	grpcPorts := allocateTCPPorts(t, n)
	quicPorts := allocatePorts(t, n)
	mcpPorts := allocateTCPPorts(t, n)
	peers := buildPeersFlag(raftPorts)

	nodeCerts := make([]string, n)
	nodeKeys := make([]string, n)
	peerAdminURLs := make(map[string]string, n)
	for i := range n {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCerts[i], nodeKeys[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, fwdTrustDomain)
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}

	c := &fwdCluster{ca: ca}
	dataDirs := make([]string, n)
	for i := range n {
		dataDirs[i] = t.TempDir()
		node := &fwdNode{raftAddr: fmt.Sprintf("127.0.0.1:%d", raftPorts[i]), admin: adminPorts[i], grpc: grpcPorts[i], quic: quicPorts[i], mcp: mcpPorts[i]}
		node.cmd = exec.Command(bin,
			"start",
			"--config", writeIsolationRaftConfig(t, t.TempDir(), peerAdminURLs),
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", node.raftAddr,
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peers,
			"--member-identities", memberIdentitiesFlag(t, nodeCerts),
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--enable-quic",
			"--quic-port", fmt.Sprintf("%d", quicPorts[i]),
			"--enable-mcp",
			"--mcp-port", fmt.Sprintf("%d", mcpPorts[i]),
			"--cluster-trust-domain", fwdTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCerts[i],
			"--tls-key-file", nodeKeys[i],
		)
		node.cmd.Stdout = &logWriter{t: t, prefix: fmt.Sprintf("[fwd-node%d] ", i+1)}
		node.cmd.Stderr = &logWriter{t: t, prefix: fmt.Sprintf("[fwd-node%d] ", i+1)}
		if err := node.cmd.Start(); err != nil {
			t.Fatalf("start node %d: %v", i+1, err)
		}
		t.Cleanup(func() {
			if node.cmd.Process != nil && !node.stopped {
				_ = node.cmd.Process.Kill()
				_ = node.cmd.Wait()
			}
		})
		c.nodes = append(c.nodes, node)
	}

	var wg sync.WaitGroup
	wg.Add(n)
	for i := range n {
		go func(idx int) {
			defer wg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			initAndUnsealBarrier(t, ca, adminPorts[idx], fwdPassphrase)
		}(i)
	}
	wg.Wait()
	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)
	for i, port := range adminPorts {
		if port == leaderPort {
			c.creator = i
		}
	}

	c.sys = mtlsHTTPClient(t, ca, 15*time.Second)
	base := fmt.Sprintf("https://127.0.0.1:%d", leaderPort)
	if _, st := iraftAPIPost(t, c.sys, base+"/v1/tenants", map[string]interface{}{
		"id": fwdTenantID, "name": "Replica Forwarding Tenant",
		"isolation_type": "raft", "barrier_auto_unseal": true,
	}); st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create tenant: status %d", st)
	}
	writerRole := c.createRole(t, base, "kv-writer", "databases:create", "databases:create_raft", "databases:read",
		"databases:list", "databases:delete", "kv:put", "kv:get", "kv:delete", "watch:watch")
	listerRole := c.createRole(t, base, "db-lister", "databases:list")
	for user, role := range map[string]string{fwdWriter: writerRole, fwdNoKV: listerRole} {
		if body, st := iraftAPIPost(t, c.sys, base+"/users", map[string]interface{}{
			"username": user, "email": user + "@" + fwdTenantID + ".example.com",
			"tenant_id": fwdTenantID, "role_ids": []string{role},
		}); st != http.StatusCreated && st != http.StatusOK {
			t.Fatalf("create user %s: status %d, body %s", user, st, body)
		}
	}
	c.writerCert, c.writerKey = ca.generateClientCertForTenant(t, fwdTenantID)
	kp := ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(fwdNoKV, fwdTenantID))
	c.noKVCert, c.noKVKey = kp.CertPath, kp.KeyPath
	kp = ca.issue(t, t.TempDir(), qrdbdharness.WithSubject(fwdWriter, "fwd-unknown-tenant"))
	c.otherCert, c.otherKey = kp.CertPath, kp.KeyPath
	c.tenant = mtlsHTTPClientFromFiles(t, ca, c.writerCert, c.writerKey, 30*time.Second)

	c.createDatabase(t, "fwd-anchor", n)
	for i := range c.nodes {
		c.waitForTenantOn(t, i)
	}
	return c
}

// createRole creates a tenant role with perms and returns its ID.
func (c *fwdCluster) createRole(t *testing.T, base, name string, perms ...string) string {
	t.Helper()
	body, st := iraftAPIPost(t, c.sys, base+"/roles", map[string]interface{}{
		"name": name, "tenant_id": fwdTenantID, "permissions": perms,
	})
	if st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create role %s: status %d, body %s", name, st, body)
	}
	var role struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &role); err != nil || role.ID == "" {
		t.Fatalf("decode role id: %s", body)
	}
	return role.ID
}

// waitForTenantOn waits until node i authenticates the tenant's callers.
func (c *fwdCluster) waitForTenantOn(t *testing.T, i int) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/list", c.nodes[i].admin)
	deadline := time.Now().Add(120 * time.Second)
	for {
		body, st := iraftAPIGet(t, c.tenant, url)
		if st == http.StatusOK {
			return
		}
		if st != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("tenant not served by node %d: status %d, body %s", i+1, st, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// createDatabase creates a raft database with the given replica count
// through the node the tenant was created through, and returns its shard ID.
func (c *fwdCluster) createDatabase(t *testing.T, name string, replicas int) uint64 {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases", c.nodes[c.creator].admin)
	deadline := time.Now().Add(90 * time.Second)
	for {
		body, st := iraftAPIPost(t, c.tenant, url, map[string]interface{}{
			"name": name, "engine": "kv", "backend": "pebble", "replicas": replicas,
		})
		if st == http.StatusCreated || st == http.StatusOK {
			var info struct {
				ShardID uint64 `json:"shardId"`
			}
			if err := json.Unmarshal(body, &info); err != nil || info.ShardID == 0 {
				t.Fatalf("create %s: no shard ID in %s", name, body)
			}
			return info.ShardID
		}
		if st != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("create %s: status %d, body %s", name, st, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// live returns the indexes of the nodes still running.
func (c *fwdCluster) live() []int {
	var out []int
	for i, n := range c.nodes {
		if !n.stopped {
			out = append(out, i)
		}
	}
	return out
}

// shardPlacement is where a shard runs: the nodes holding a replica, their
// replica IDs, and the node of its current leader (-1 while it has none).
type shardPlacement struct {
	replicas  map[int]uint64
	leader    int
	leaderRID uint64
}

// placementOf reads the shard's replicas and leader from every live node's
// topology, waiting until the shard runs on want replicas and has a leader
// among them.
func (c *fwdCluster) placementOf(t *testing.T, shardID uint64, want int) shardPlacement {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	for {
		p := shardPlacement{replicas: map[int]uint64{}, leader: -1}
		for _, i := range c.live() {
			body, st := iraftAPIGet(t, c.sys, fmt.Sprintf("https://127.0.0.1:%d/admin/topology", c.nodes[i].admin))
			if st != http.StatusOK {
				continue
			}
			var topo struct {
				Shards []struct {
					ShardID   uint64 `json:"shard_id"`
					ReplicaID uint64 `json:"replica_id"`
					IsLeader  bool   `json:"is_leader"`
				} `json:"shards"`
			}
			if err := json.Unmarshal(body, &topo); err != nil {
				t.Fatalf("decode topology of node %d: %v", i+1, err)
			}
			for _, s := range topo.Shards {
				if s.ShardID != shardID {
					continue
				}
				p.replicas[i] = s.ReplicaID
				if s.IsLeader {
					p.leader, p.leaderRID = i, s.ReplicaID
				}
			}
		}
		if len(p.replicas) == want && p.leader >= 0 {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("shard %d: placement %+v; want %d replicas with a leader", shardID, p, want)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// nonReplicas returns the live nodes holding no replica in p.
func (c *fwdCluster) nonReplicas(p shardPlacement) []int {
	var out []int
	for _, i := range c.live() {
		if _, ok := p.replicas[i]; !ok {
			out = append(out, i)
		}
	}
	return out
}

// stop kills node i, as a crash.
func (c *fwdCluster) stop(t *testing.T, i int) {
	t.Helper()
	n := c.nodes[i]
	if err := n.cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatalf("kill node %d: %v", i+1, err)
	}
	_ = n.cmd.Wait()
	n.stopped = true
}
