// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

// dao_cluster_test.go proves pkg/dao's GenericDAO over a real 3-node Raft
// cluster: a write issued against a FOLLOWER (REST leader-forwarding
// carries it to the leader), reads that are visible from every node, a
// leader kill with a forced failover, and a restart that must rejoin and
// catch up. This is the clustering half of "clustering and dao/entity
// storage... working for the upstream projects" -- dao_sdk_test.go proves
// the DAO facade itself; this file proves it survives the failures a
// production Raft deployment actually has.
package integration_test

import (
	"context"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

type daoClusterNode struct {
	cmd       *exec.Cmd
	dataDir   string
	adminPort int
	args      []string
}

type daoCluster struct {
	bin   string
	ca    *testCA
	nodes []*daoClusterNode
}

func (c *daoCluster) adminPorts() []int {
	ports := make([]int, len(c.nodes))
	for i, n := range c.nodes {
		ports[i] = n.adminPort
	}
	return ports
}

func (c *daoCluster) indexOf(adminPort int) int {
	for i, n := range c.nodes {
		if n.adminPort == adminPort {
			return i
		}
	}
	return -1
}

// kill sends SIGTERM to node i and waits for it to exit, the same
// graceful-shutdown signal TestCluster_ForwardedWriteSurvivesRestart uses.
func (c *daoCluster) kill(t *testing.T, i int) {
	t.Helper()
	n := c.nodes[i]
	if err := n.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal node %d: %v", i+1, err)
	}
	_ = awaitNodeExit(t, n.cmd, nodeStopTimeout)
}

// restart relaunches node i with its original args and data directory --
// so it rejoins the SAME Raft group with its persisted log and snapshots
// -- then unseals its barrier, which persists sealed across a restart.
func (c *daoCluster) restart(t *testing.T, i int) {
	t.Helper()
	n := c.nodes[i]
	cmd := spawnNode(t, c.bin, n.args, &logWriter{t: t, prefix: fmt.Sprintf("[daocluster node%d-restart] ", i+1)})
	n.cmd = cmd
	t.Cleanup(func() {
		killNode(t, cmd)
	})

	waitForBarrierStatusReachable(t, c.ca, n.adminPort, 90*time.Second)
	unsealOnlyBarrier(t, c.ca, n.adminPort)
	waitForHealthy(t, n.dataDir, 60*time.Second)
	waitForAdmin(t, c.ca.Pool, n.adminPort, 90*time.Second)
}

// startDAOCluster boots a fresh 3-node Raft cluster, each node reachable
// over its own admin REST port, sharing one admin identity (testCA caches
// it) so one sdk.Client construction pattern works against any of them.
func startDAOCluster(t *testing.T, smType string) *daoCluster {
	t.Helper()
	bin := buildBinary(t)
	ca := generateTestCA(t)
	cfgPath := writeNoneSealConfig(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, 3)

	cluster := &daoCluster{bin: bin, ca: ca}
	for i := range 3 {
		dataDir := t.TempDir()
		args := []string{
			"start",
			"--config", cfgPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDir,
			"--sm", smType,
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
		}
		args = append(args, nodeTLS[i]...)

		cmd := startNode(t, bin, args, fmt.Sprintf("[daocluster node%d] ", i+1))
		cluster.nodes = append(cluster.nodes, &daoClusterNode{
			cmd: cmd, dataDir: dataDir, adminPort: adminPorts[i], args: args,
		})
	}

	bootstrapNodes(t, ca, cluster.adminPorts()...)
	for _, n := range cluster.nodes {
		waitForHealthy(t, n.dataDir, 60*time.Second)
		waitForAdmin(t, ca.Pool, n.adminPort, 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, cluster.adminPorts(), 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	return cluster
}

// assertDAOVisibleEverywhere polls every node in the cluster, by its own
// sdk.Client, until the entity is readable both by Get and by its unique
// Email index -- the two access paths exerciseDAOEntityStorage exercises
// against a single node -- or the deadline expires.
func assertDAOVisibleEverywhere(t *testing.T, cluster *daoCluster, entityType string, id uint64, wantEmail string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	newEntity := func() *daoSDKUser { return &daoSDKUser{} }

	for _, port := range cluster.adminPorts() {
		client := daoSDKClient(t, cluster.ca, port)
		d, err := sdk.NewDAO[*daoSDKUser](client.KVStore(), entityType, newEntity)
		if err != nil {
			t.Fatalf("sdk.NewDAO on admin port %d: %v", port, err)
		}

		deadline := time.Now().Add(timeout)
		var lastErr error
		for {
			got, getErr := d.Get(ctx, id)
			if getErr == nil && got.Email == wantEmail {
				matches, idxErr := d.QueryByIndex(ctx, "Email", wantEmail)
				if idxErr == nil && len(matches) == 1 && matches[0].ID == id {
					lastErr = nil
					break
				}
				lastErr = idxErr
			} else {
				lastErr = getErr
			}
			if time.Now().After(deadline) {
				t.Fatalf("admin port %d: entity %d (%s) never visible by Get AND QueryByIndex within %v: %v",
					port, id, wantEmail, timeout, lastErr)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// TestDAO_RealBinary_ThreeNodeCluster_LeaderKillAndRestart writes through
// a follower, confirms the write and its index are visible from every
// node, kills the leader, confirms the surviving two elect a new leader
// and keep serving DAO writes and index queries, then restarts the killed
// node and confirms it rejoins and catches up on both the pre- and
// post-failover data.
func TestDAO_RealBinary_ThreeNodeCluster_LeaderKillAndRestart(t *testing.T) {
	cluster := startDAOCluster(t, "pebble")
	ctx := context.Background()
	const entityType = "daocluster-user"
	newEntity := func() *daoSDKUser { return &daoSDKUser{} }

	leaderPort := waitForLeaderViaAdmin(t, cluster.ca, cluster.adminPorts(), 30*time.Second)
	var followerPort int
	for _, p := range cluster.adminPorts() {
		if p != leaderPort {
			followerPort = p
			break
		}
	}
	if followerPort == 0 {
		t.Fatal("no follower admin port found in a 3-node cluster")
	}

	// Write through a FOLLOWER: REST leader-forwarding must carry it.
	followerClient := daoSDKClient(t, cluster.ca, followerPort)
	followerDAO, err := sdk.NewDAO[*daoSDKUser](followerClient.KVStore(), entityType, newEntity)
	if err != nil {
		t.Fatalf("sdk.NewDAO over follower (port %d): %v", followerPort, err)
	}
	u1 := &daoSDKUser{Email: "cluster1@example.com", Status: "active", Username: "Cluster1", Age: 10}
	if err := followerDAO.Save(ctx, u1); err != nil {
		t.Fatalf("Save(u1) via follower port %d (leader-forwarded write): %v", followerPort, err)
	}

	// Read from EVERY node, by Get and by index.
	assertDAOVisibleEverywhere(t, cluster, entityType, u1.ID, u1.Email, 15*time.Second)

	// Kill the leader.
	leaderIdx := cluster.indexOf(leaderPort)
	if leaderIdx < 0 {
		t.Fatalf("leader admin port %d not found among cluster nodes", leaderPort)
	}
	cluster.kill(t, leaderIdx)

	// The two survivors must elect a new leader and keep serving writes
	// and index queries.
	var survivors []int
	for _, p := range cluster.adminPorts() {
		if p != leaderPort {
			survivors = append(survivors, p)
		}
	}
	newLeaderPort := waitForLeaderViaAdmin(t, cluster.ca, survivors, 60*time.Second)
	warmupWrite(t, cluster.ca, newLeaderPort, 15*time.Second)

	survivorClient := daoSDKClient(t, cluster.ca, newLeaderPort)
	survivorDAO, err := sdk.NewDAO[*daoSDKUser](survivorClient.KVStore(), entityType, newEntity)
	if err != nil {
		t.Fatalf("sdk.NewDAO after failover (port %d): %v", newLeaderPort, err)
	}
	u2 := &daoSDKUser{Email: "cluster2@example.com", Status: "active", Username: "Cluster2", Age: 11}
	if err := survivorDAO.Save(ctx, u2); err != nil {
		t.Fatalf("Save(u2) after leader kill: %v", err)
	}
	matches, err := survivorDAO.QueryByIndex(ctx, "Email", u2.Email)
	if err != nil {
		t.Fatalf("QueryByIndex(Email, %s) after failover: %v", u2.Email, err)
	}
	if len(matches) != 1 || matches[0].ID != u2.ID {
		t.Fatalf("QueryByIndex(Email, %s) after failover = %v, want exactly u2", u2.Email, matches)
	}

	// Restart the killed node. It must rejoin and eventually serve BOTH
	// the pre- and post-failover entities, by Get and by index.
	cluster.restart(t, leaderIdx)
	assertDAOVisibleEverywhere(t, cluster, entityType, u1.ID, u1.Email, 30*time.Second)
	assertDAOVisibleEverywhere(t, cluster, entityType, u2.ID, u2.Email, 30*time.Second)
}
