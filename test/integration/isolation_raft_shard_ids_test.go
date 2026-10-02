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

// Cluster-wide shard IDs on three real qrdbd nodes.
//
// Every node's shard allocator used to start at the same value and move past
// only the IDs that node had issued, so databases created through DIFFERENT
// nodes were handed the same shard ID, and a name dropped through one node and
// created again through another could get the dropped incarnation's ID back.
// Each database here is created through the node named in its row, never
// through a forwarding leader: a create runs on the node that receives it.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"testing"
	"time"
)

const (
	shardIDsTrustDomain = "qrdb.cluster"
	shardIDsTenantID    = "shard-ids-t1"
	shardIDsPassphrase  = "integration-test-shard-ids-barrier-secret"
)

// shardIDsCluster is a running three-node cluster with one raft tenant and a
// client authenticated as that tenant's database administrator.
type shardIDsCluster struct {
	adminPorts []int
	tenant     *http.Client
	// anchorShard is the shard issued to the database every node hosts.
	anchorShard uint64
}

// startShardIDsCluster starts and unseals three nodes, creates the raft
// tenant, and returns the tenant's client. It follows the bring-up of the
// other isolation_raft suites.
func startShardIDsCluster(t *testing.T) *shardIDsCluster {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	nodeCerts := make([]string, 3)
	nodeKeys := make([]string, 3)
	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCerts[i], nodeKeys[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, shardIDsTrustDomain)
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}

	dataDirs := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		configPath := writeIsolationRaftConfig(t, t.TempDir(), peerAdminURLs)
		cmd := exec.Command(bin,
			"start",
			"--config", configPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIdentitiesFlag(t, nodeCerts),
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--cluster-trust-domain", shardIDsTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCerts[i],
			"--tls-key-file", nodeKeys[i],
		)
		cmd.Stdout = &logWriter{t: t, prefix: fmt.Sprintf("[shard-ids-node%d] ", i+1)}
		cmd.Stderr = &logWriter{t: t, prefix: fmt.Sprintf("[shard-ids-node%d] ", i+1)}
		if err := cmd.Start(); err != nil {
			t.Fatalf("start node %d: %v", i+1, err)
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
	}

	var wg sync.WaitGroup
	wg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer wg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			initAndUnsealBarrier(t, ca, adminPorts[idx], shardIDsPassphrase)
		}(i)
	}
	wg.Wait()
	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	sys := mtlsHTTPClient(t, ca, 15*time.Second)
	if _, st := iraftAPIPost(t, sys, fmt.Sprintf("https://127.0.0.1:%d/v1/tenants", leaderPort),
		map[string]interface{}{
			"id": shardIDsTenantID, "name": "Shard IDs Test Tenant",
			"isolation_type": "raft", "barrier_auto_unseal": true,
		}); st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create tenant: status %d", st)
	}
	roleBody, st := iraftAPIPost(t, sys, fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name": "db-admin", "tenant_id": shardIDsTenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft", "databases:read",
				"databases:list", "databases:delete",
			},
		})
	if st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create role: status %d, body %s", st, roleBody)
	}
	var role struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &role); err != nil || role.ID == "" {
		t.Fatalf("decode role id: %s", roleBody)
	}
	if _, st := iraftAPIPost(t, sys, fmt.Sprintf("https://127.0.0.1:%d/users", leaderPort),
		map[string]interface{}{
			"username": "admin", "email": "admin@" + shardIDsTenantID + ".example.com",
			"tenant_id": shardIDsTenantID, "role_ids": []string{role.ID},
		}); st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create user: status %d", st)
	}

	certPath, keyPath := ca.generateClientCertForTenant(t, shardIDsTenantID)
	c := &shardIDsCluster{
		adminPorts: adminPorts,
		tenant:     mtlsHTTPClientFromFiles(t, ca, certPath, keyPath, 30*time.Second),
	}

	// A raft tenant's key reaches a node only when placement assigns that
	// node one of the tenant's replicas; until then the node cannot read the
	// tenant's identity records and answers "sealed". Placing one database on
	// all three nodes is what lets the tests below create through any node.
	body, st := iraftAPIPost(t, c.tenant, fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
		map[string]interface{}{"name": "sid-anchor", "engine": "kv", "backend": "pebble", "replicas": 3})
	if st != http.StatusCreated && st != http.StatusOK {
		t.Fatalf("create anchor database: status %d, body %s", st, body)
	}
	var anchor struct {
		ShardID uint64 `json:"shardId"`
	}
	if err := json.Unmarshal(body, &anchor); err != nil || anchor.ShardID == 0 {
		t.Fatalf("anchor database: no shard ID in %s", body)
	}
	c.anchorShard = anchor.ShardID
	for node := range adminPorts {
		c.waitForTenantOn(t, node)
	}
	return c
}

// waitForTenantOn waits until the node at index node authenticates the
// tenant: its key has arrived and the tenant's identity records are readable.
func (c *shardIDsCluster) waitForTenantOn(t *testing.T, node int) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/list", c.adminPorts[node])
	deadline := time.Now().Add(120 * time.Second)
	for {
		body, st := iraftAPIGet(t, c.tenant, url)
		if st == http.StatusOK {
			return
		}
		if st != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("tenant not served by node %d: status %d, body %s", node+1, st, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// createThrough creates a single-replica raft database through the node at
// index node and returns the shard ID the create response reports. The
// tenant's IAM can lag a node that only just joined the tenant, so a
// retryable refusal is retried; any other refusal fails the test.
func (c *shardIDsCluster) createThrough(t *testing.T, node int, name string) uint64 {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases", c.adminPorts[node])
	deadline := time.Now().Add(60 * time.Second)
	for {
		body, st := iraftAPIPost(t, c.tenant, url, map[string]interface{}{
			"name": name, "engine": "kv", "backend": "pebble", "replicas": 1,
		})
		if st == http.StatusCreated || st == http.StatusOK {
			var info struct {
				ShardID uint64 `json:"shardId"`
			}
			if err := json.Unmarshal(body, &info); err != nil || info.ShardID == 0 {
				t.Fatalf("create %s through node %d: no shard ID in %s", name, node+1, body)
			}
			return info.ShardID
		}
		if st != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("create %s through node %d: status %d, body %s", name, node+1, st, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// placedShardID reads the database's shard ID back through the node at index
// node. The read is refreshed from the replicated placement table
// (DatabaseService.GetForTenant), so it reports what placement recorded rather
// than what the create returned. The database catalog is node-local, so the
// node must be the one that created the database.
func (c *shardIDsCluster) placedShardID(t *testing.T, node int, name string) uint64 {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", c.adminPorts[node], name)
	deadline := time.Now().Add(60 * time.Second)
	for {
		body, st := iraftAPIGet(t, c.tenant, url)
		if st == http.StatusOK {
			var info struct {
				ShardID uint64 `json:"shardId"`
			}
			if err := json.Unmarshal(body, &info); err != nil {
				t.Fatalf("decode %s: %v (%s)", name, err, body)
			}
			return info.ShardID
		}
		if time.Now().After(deadline) {
			t.Fatalf("read %s through node %d: status %d, body %s", name, node+1, st, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// dropThrough drops the database through the node at index node.
func (c *shardIDsCluster) dropThrough(t *testing.T, node int, name string) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", c.adminPorts[node], name)
	deadline := time.Now().Add(60 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodDelete, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := c.tenant.Do(req)
		if err != nil {
			t.Fatalf("drop %s through node %d: %v", name, node+1, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
			return
		}
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			t.Fatalf("drop %s through node %d: status %d, body %s", name, node+1, resp.StatusCode, body)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestIsolationRaft_ShardIDs_UniqueAcrossNodes(t *testing.T) {
	c := startShardIDsCluster(t)

	names := []string{"sid-one", "sid-two", "sid-three"}
	// The anchor database holds a shard too; no later create may reuse it.
	seen := map[uint64]string{c.anchorShard: "sid-anchor"}
	for node, name := range names {
		id := c.createThrough(t, node, name)
		if prev, dup := seen[id]; dup {
			t.Fatalf("shard ID %d issued to %q (node %d) and to %q: shard IDs are unique per node only",
				id, name, node+1, prev)
		}
		seen[id] = name
		t.Logf("%s created through node %d on shard %d", name, node+1, id)
	}

	// Placement holds the same answer for each.
	for node, name := range names {
		got := c.placedShardID(t, node, name)
		if seen[got] != name {
			t.Fatalf("%s: placement reports shard %d, which was issued to %q", name, got, seen[got])
		}
	}
}

func TestIsolationRaft_ShardIDs_DroppedIDNotReusedThroughAnotherNode(t *testing.T) {
	c := startShardIDsCluster(t)
	const name = "sid-recreated"

	first := c.createThrough(t, 0, name)
	c.dropThrough(t, 0, name)
	second := c.createThrough(t, 1, name)
	if second == first {
		t.Fatalf("%s created again through node 2 got the dropped incarnation's shard %d", name, first)
	}
	if got := c.placedShardID(t, 1, name); got != second {
		t.Fatalf("placement reports shard %d for the new incarnation, create reported %d", got, second)
	}
	t.Logf("%s: dropped shard %d, new incarnation on shard %d", name, first, second)
}
