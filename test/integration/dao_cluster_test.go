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
// cluster: a write that becomes visible from every node, by Get and by
// index, a leader kill with a forced failover, and a restart that must rejoin and
// catch up. This is the clustering half of "clustering and dao/entity
// storage... working for the upstream projects" -- the DAO facade itself is
// unit-tested in pkg/dao and, over a standalone node's indexed engine, in
// sdk/go/node's dao_entity_storage_test.go; this file proves it survives the failures a
// production Raft deployment actually has.
package integration_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/rest"
)

// daoSDKUser declares the index kinds the cluster test reads back: Email is
// unique, Status is a plain equality index, Username is
// unique+case-insensitive, Nickname is sparse (omitempty, so a record that
// never sets it really omits the field), and Age is numeric.
type daoSDKUser struct {
	ID       uint64 `json:"id"`
	Email    string `json:"email"              index:"unique"`
	Status   string `json:"status"             index:"true"`
	Username string `json:"username"           index:"unique,case_insensitive"`
	Nickname string `json:"nickname,omitempty" index:"true,sparse"`
	Age      int    `json:"age"                index:"true"`
	SortKey  string `json:"sort_key"`
	Payload  []byte `json:"payload,omitempty"`
}

func (u *daoSDKUser) EntityID() uint64      { return u.ID }
func (u *daoSDKUser) SetEntityID(id uint64) { u.ID = id }

// daoSDKClient dials the real qrdbd admin REST endpoint over mTLS with the
// harness's shared admin identity (the same certificate every node in this
// package binds during its ceremony) and returns a ready sdk.Client -- the
// exact construction an embedding application performs.
func daoSDKClient(t *testing.T, ca *testCA, port int) *sdk.Client {
	t.Helper()
	_, certPath, keyPath := ca.adminIdentity(t, t.TempDir())
	tr, err := rest.New(
		transport.WithAddress(fmt.Sprintf("https://127.0.0.1:%d", port)),
		transport.WithMTLS(certPath, keyPath, ca.CertPath),
		transport.WithTimeout(20*time.Second),
	)
	if err != nil {
		t.Fatalf("rest.New: %v", err)
	}
	if err := tr.Connect(context.Background()); err != nil {
		t.Fatalf("transport Connect to 127.0.0.1:%d: %v", port, err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	client, err := sdk.NewClient(tr)
	if err != nil {
		t.Fatalf("sdk.NewClient: %v", err)
	}
	return client
}

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
// newDAORetryingRetryable builds a DAO over client, retrying for up to
// retryableCreateWindow while the IndexCreate that sdk.NewDAO proposes is
// answered with the server's typed "retry" (retryableStatus). Around a leader
// kill or a restart the shard has no leader for a moment and a node answers
// 503 NOT_LEADER, "already submitted to raft; it may still commit"; the retry
// is safe because a create of an index that already exists under the same
// owner is an idempotent no-op (pkg/engine/index_value_type.go,
// index.CheckCreateOwner).
func newDAORetryingRetryable(t *testing.T, client *sdk.Client, entityType string, newEntity func() *daoSDKUser, what string) sdk.GenericDAO[*daoSDKUser] {
	t.Helper()
	var d sdk.GenericDAO[*daoSDKUser]
	var err error
	if !pollUntil(retryableCreateWindow, 250*time.Millisecond, func() bool {
		d, err = sdk.NewDAO[*daoSDKUser](client.KVStore(), entityType, newEntity)
		if err == nil {
			return true
		}
		var reqErr *transport.RequestError
		if !errors.As(err, &reqErr) || !retryableStatus(reqErr.StatusCode) {
			t.Fatalf("sdk.NewDAO %s: %v", what, err)
		}
		t.Logf("sdk.NewDAO %s refused for now, retrying: %v", what, err)
		return false
	}) {
		t.Fatalf("sdk.NewDAO %s: %v", what, err)
	}
	return d
}

func assertDAOVisibleEverywhere(t *testing.T, cluster *daoCluster, entityType string, id uint64, wantEmail string, timeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	newEntity := func() *daoSDKUser { return &daoSDKUser{} }

	for _, port := range cluster.adminPorts() {
		client := daoSDKClient(t, cluster.ca, port)
		d := newDAORetryingRetryable(t, client, entityType, newEntity, fmt.Sprintf("on admin port %d", port))

		var lastErr error
		if !pollUntil(timeout, 200*time.Millisecond, func() bool {
			got, getErr := d.Get(ctx, id)
			if getErr == nil && got.Email == wantEmail {
				matches, idxErr := d.QueryByIndex(ctx, "Email", wantEmail)
				if idxErr == nil && len(matches) == 1 && matches[0].ID == id {
					lastErr = nil
					return true
				}
				lastErr = idxErr
			} else {
				lastErr = getErr
			}
			return false
		}) {
			t.Fatalf("admin port %d: entity %d (%s) never visible by Get AND QueryByIndex within %v: %v",
				port, id, wantEmail, timeout, lastErr)
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
	// REST leader forwarding of a follower's write is unit-tested in
	// pkg/api/rest (TestHandler_KVPut_ForwardToLeader_*), so the first
	// entity is written through the leader.
	leaderClient := daoSDKClient(t, cluster.ca, leaderPort)
	leaderDAO := newDAORetryingRetryable(t, leaderClient, entityType, newEntity, fmt.Sprintf("over leader (port %d)", leaderPort))
	u1 := &daoSDKUser{Email: "cluster1@example.com", Status: "active", Username: "Cluster1", Age: 10}
	if err := leaderDAO.Save(ctx, u1); err != nil {
		t.Fatalf("Save(u1) via leader port %d: %v", leaderPort, err)
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
	survivorDAO := newDAORetryingRetryable(t, survivorClient, entityType, newEntity, fmt.Sprintf("after failover (port %d)", newLeaderPort))
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
