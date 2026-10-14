// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration

// lease_binding_test.go drives real three-node qrdbd clusters through the
// REST API to check that a key belongs to the lease its LATEST write named
// (go-qrdb#581): revoking the lease an earlier value named must not delete
// the key on any replica, and the bindings must survive a full cluster
// restart, after which every replica rebuilds its lessor from disk.
package integration_test

import (
	"fmt"
	"net/http"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// leaseBindingKeys writes the fixture every test here asserts on:
//
//	moved    -- written under A, then again under B
//	released -- written under A, then again with no lease
//	withA    -- written under A only
func leaseBindingKeys(t *testing.T, ca *testCA, leaderPort int) (a, b int64) {
	t.Helper()
	a = httpLeaseGrant(t, ca, leaderPort, 3600).ID
	b = httpLeaseGrant(t, ca, leaderPort, 3600).ID
	httpBatch(t, ca, leaderPort, []batchOp{
		{Type: "put", Key: "lb/moved", Value: "old", LeaseID: a},
		{Type: "put", Key: "lb/released", Value: "leased", LeaseID: a},
		{Type: "put", Key: "lb/withA", Value: "v", LeaseID: a},
	})
	httpBatch(t, ca, leaderPort, []batchOp{
		{Type: "put", Key: "lb/moved", Value: "new", LeaseID: b},
	})
	httpPut(t, ca, leaderPort, "lb/released", []byte("permanent"))
	return a, b
}

// assertLeaseBindings revokes A and then B on the leader and checks every
// replica after each.
func assertLeaseBindings(t *testing.T, ca *testCA, adminPorts []int, leaderPort int, a, b int64) {
	t.Helper()
	httpLeaseRevoke(t, ca, leaderPort, a)
	for _, port := range adminPorts {
		waitForKeyAbsent(t, ca, port, "lb/withA", 60*time.Second)
		waitForKV(t, ca, port, "lb/moved", []byte("new"), 60*time.Second)
		waitForKV(t, ca, port, "lb/released", []byte("permanent"), 60*time.Second)
	}

	httpLeaseRevoke(t, ca, leaderPort, b)
	for _, port := range adminPorts {
		waitForKeyAbsent(t, ca, port, "lb/moved", 60*time.Second)
		waitForKV(t, ca, port, "lb/released", []byte("permanent"), 60*time.Second)
	}
}

// leaseSMType is the state machine these tests run qrdbd on. qrdbd's lease
// service needs an MVCC-backed shard: on sm_type pebble a grant is refused
// with 501 before it reaches the state machine. The Pebble state machine's
// lease bindings are covered at the state-machine level, by
// pkg/store/lease_binding_test.go, through the same commands Raft applies.
const leaseSMType = "mvcc"

func TestLease_ThreeNode_RePutLeavesOldLease(t *testing.T) {
	adminPorts, _, ca := startThreeNodeCluster(t, leaseSMType)
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	a, b := leaseBindingKeys(t, ca, leaderPort)
	for _, port := range adminPorts {
		waitForKV(t, ca, port, "lb/moved", []byte("new"), 60*time.Second)
	}
	assertLeaseBindings(t, ca, adminPorts, leaderPort, a, b)
}

// restartableCluster is a three-node cluster whose processes this test stops
// and restarts over the same data directories.
type restartableCluster struct {
	bin        string
	ca         *testCA
	smType     string
	raftPorts  []int
	adminPorts []int
	grpcPorts  []int
	dataDirs   []string
	nodeTLS    [][]string
	memberIDs  string
	cmds       []*exec.Cmd
}

func newRestartableCluster(t *testing.T, smType string) *restartableCluster {
	t.Helper()
	c := &restartableCluster{
		bin:        buildBinary(t),
		ca:         generateTestCA(t),
		smType:     smType,
		raftPorts:  allocatePorts(t, 3),
		adminPorts: allocateTCPPorts(t, 3),
		grpcPorts:  allocateTCPPorts(t, 3),
		dataDirs:   []string{t.TempDir(), t.TempDir(), t.TempDir()},
	}
	c.nodeTLS, c.memberIDs = c.ca.clusterNodeTLS(t, 3)
	return c
}

// start launches all three nodes, takes them through the sealed window, and
// returns the leader's admin port once one is elected.
func (c *restartableCluster) start(t *testing.T, round int) int {
	t.Helper()
	peers := buildPeersFlag(c.raftPorts)
	c.cmds = make([]*exec.Cmd, 3)
	for i := range 3 {
		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", c.raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", c.raftPorts[i]),
			"--data-dir", c.dataDirs[i],
			"--sm", c.smType,
			"--discovery", "static",
			"--peers", peers,
			"--member-identities", c.memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", c.adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", c.grpcPorts[i]),
		}
		args = append(args, c.nodeTLS[i]...)
		c.cmds[i] = startNode(t, c.bin, args, fmt.Sprintf("[node%d-R%d] ", i+1, round))
	}
	// Round 1 is the cluster's first boot; every later round finds the
	// barrier the first boot initialized, sealed again by the restart.
	if round == 1 {
		bootstrapNodes(t, c.ca, c.adminPorts...)
	} else {
		unsealNodes(t, c.ca, c.adminPorts...)
	}
	for i := range 3 {
		waitForAdmin(t, c.ca.Pool, c.adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, c.ca, c.adminPorts, 60*time.Second)
	warmupWrite(t, c.ca, leaderPort, 30*time.Second)
	return leaderPort
}

// stop sends every node SIGTERM and waits for each to exit.
func (c *restartableCluster) stop(t *testing.T) {
	t.Helper()
	for i, cmd := range c.cmds {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatalf("signal node %d: %v", i+1, err)
		}
	}
	for _, cmd := range c.cmds {
		_ = awaitNodeExit(t, cmd, nodeStopTimeout)
	}
}

// A full cluster restart: every replica resumes from its own disk without
// replaying the log, so its lessor comes back only from the persisted lease
// table. Before the table existed, every lease vanished here and revoking
// either one deleted nothing.
func TestLease_ThreeNode_BindingsSurviveFullRestart(t *testing.T) {
	c := newRestartableCluster(t, leaseSMType)
	leaderPort := c.start(t, 1)

	a, b := leaseBindingKeys(t, c.ca, leaderPort)
	for _, port := range c.adminPorts {
		waitForKV(t, c.ca, port, "lb/moved", []byte("new"), 60*time.Second)
		waitForKV(t, c.ca, port, "lb/released", []byte("permanent"), 60*time.Second)
	}

	c.stop(t)
	leaderPort = c.start(t, 2)

	for _, port := range c.adminPorts {
		if got := httpLeaseTTL(t, c.ca, port, a); got.ID != a {
			t.Fatalf("port %d: lease %d after restart = %+v, want it restored", port, a, got)
		}
	}
	next := httpLeaseGrant(t, c.ca, leaderPort, 60)
	if next.ID <= b {
		t.Fatalf("first grant after restart = %d, want an ID above %d: the sequence must survive", next.ID, b)
	}

	assertLeaseBindings(t, c.ca, c.adminPorts, leaderPort, a, b)
}

// waitForKeyAbsent polls key on port until it answers 404.
func waitForKeyAbsent(t *testing.T, ca *testCA, port int, key string, timeout time.Duration) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key)
	client := mtlsHTTPClient(t, ca, 2*time.Second)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		resp, err := client.Get(url)
		if err == nil {
			status := resp.StatusCode
			if cerr := resp.Body.Close(); cerr != nil {
				t.Logf("close body: %v", cerr)
			}
			if status == http.StatusNotFound {
				return
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("port %d: key %q still present after %v", port, key, timeout)
		case <-tick.C:
		}
	}
}
