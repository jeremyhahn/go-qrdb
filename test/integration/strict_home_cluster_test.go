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

package integration_test

// TestTenant_StrictHome_AuthenticatesOnEveryNode proves a strict tenant's
// identity lives in its own home raft group and is served by every node:
//
//   - The cluster floor is strict (tenant.isolation_policy: strict), so a raft
//     tenant created on it is strict and its records go to its home group.
//   - Creating the tenant places and starts the home group on every node
//     before the first identity record is written: the shard count rises to
//     two on all three nodes without any database being created.
//   - The tenant's admin, whose user and certificate binding were written by
//     onboarding, authenticates on every node -- each node reads it from its
//     own home-group replica.
//   - A replica that was down while the home group snapshotted and compacted
//     its log past the replica's last entry catches up by snapshot install:
//     the restarted node serves a user created while it was down, which no
//     live replica still holds as a log entry.
//   - After a full-cluster restart and re-unseal, the home group comes back
//     on every node and the same admin authenticates on every node again.
//
// The exact-key proof that no tenant record is in the system shard is
// TestTenantHome_* in cmd/qrdbd and TestTenantHome_RecordsLiveOnlyInTheHomeStore
// here, which run in process: a daemon offers no raw read of the system shard
// to probe.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/role"
)

const (
	// strictHomeTenantID is the strict tenant under test.
	strictHomeTenantID = "strict1"

	// strictHomeSnapshotEntries and strictHomeCompactionOverhead are the
	// cluster's raft snapshot cadence: a snapshot every 40 applied entries,
	// keeping 10 entries of log behind it.
	strictHomeSnapshotEntries    = 40
	strictHomeCompactionOverhead = 10

	// strictHomeLagUsers is how many users are created while a replica is
	// down. Each is at least one home-group entry, so this many writes put
	// the newest snapshot at least strictHomeLagUsers-strictHomeSnapshotEntries
	// entries past the stopped replica and compact the log to above where it
	// stopped.
	strictHomeLagUsers = 2*strictHomeSnapshotEntries + strictHomeCompactionOverhead
)

func TestTenant_StrictHome_AuthenticatesOnEveryNode(t *testing.T) {
	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, iraftTrustDomain)
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}

	dataDirs := make([]string, 3)
	configPaths := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		configPaths[i] = writeIsolationRaftConfigWithPolicy(t, t.TempDir(), peerAdminURLs, "strict")
	}

	const passphrase = "integration-test-barrier-secret"
	cmds := make([]*exec.Cmd, 3)

	// launch starts node i, copying its log to every tee writer. The snapshot
	// cadence is far below the default so that every raft group -- the home
	// group included -- snapshots and compacts its log within the handful of
	// writes a test makes.
	launch := func(i int, run string, tee ...io.Writer) {
		cmds[i] = startNode(t, bin, []string{
			"start",
			"--config", configPaths[i],
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIdentitiesFlag(t, nodeCertPaths),
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--cluster-trust-domain", iraftTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
			"--snapshot-entries", fmt.Sprintf("%d", strictHomeSnapshotEntries),
			"--compaction-overhead", fmt.Sprintf("%d", strictHomeCompactionOverhead),
		}, fmt.Sprintf("[node%d-%s] ", i+1, run), tee...)
	}

	// openNode drives node i's sealed window: initialize on the first run,
	// unseal only on a restart.
	openNode := func(i int, run string, initialize bool) {
		waitForBarrierStatusReachable(t, ca, adminPorts[i], 90*time.Second)
		if initialize {
			initAndUnsealBarrier(t, ca, adminPorts[i], passphrase)
			return
		}
		body, status := iraftAPIPost(t, mtlsHTTPClient(t, ca, 10*time.Second),
			fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", adminPorts[i]),
			map[string]interface{}{"secret": []byte(passphrase)})
		if status != http.StatusNoContent {
			t.Errorf("%s: unseal node %d: status %d, body: %s", run, i+1, status, body)
		}
	}

	// stopNode stops node i gracefully.
	stopNode := func(i int) {
		cmd := cmds[i]
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Logf("signal node %d: %v", i+1, err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = cmd.Process.Kill()
			t.Errorf("node %d: timed out waiting for graceful shutdown", i+1)
		}
	}

	// startCluster starts every node and opens them together: a raft cluster
	// cannot finish initializing until a quorum is up.
	startCluster := func(run string, initialize bool) {
		for i := range 3 {
			launch(i, run)
		}
		var wg sync.WaitGroup
		for i := range 3 {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				openNode(idx, run, initialize)
			}(i)
		}
		wg.Wait()
		if t.Failed() {
			t.FailNow()
		}
		for i := range 3 {
			waitForHealthy(t, dataDirs[i], 60*time.Second)
			waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
		}
	}

	// assertHomeOnEveryNode waits for the system shard and the tenant's home
	// group on every node; no database exists, so the second shard is the home.
	assertHomeOnEveryNode := func(run string) {
		client := mtlsHTTPClient(t, ca, 15*time.Second)
		for i, port := range adminPorts {
			waitForShardCount(t, client, port, 2, 120*time.Second)
			t.Logf("%s: node %d serves the home group", run, i+1)
		}
	}

	// assertAuthenticatesEverywhere has the tenant admin read its own user
	// record on every node: the request authenticates against the user and
	// binding in the home group, and the record it returns is read from that
	// node's own home-group replica.
	adminUsername := strictHomeTenantID + "-admin"
	// A node trusts the tenant's CA once the tenant's trust anchor has reached
	// it, so until then it refuses the admin's certificate at the handshake:
	// wait for the node to serve the admin before reading the user through it.
	awaitUser := func(run string, client *http.Client, i int, username string, wait time.Duration) {
		waitForNodeToTrustClient(t, client, adminPorts[i], wait)
		var body []byte
		var status int
		pollUntil(wait, 250*time.Millisecond, func() bool {
			body, status = iraftAPIGet(t, client,
				fmt.Sprintf("https://127.0.0.1:%d/users/by-username/%s", adminPorts[i], username))
			return status == http.StatusOK
		})
		if status != http.StatusOK {
			t.Fatalf("%s: node %d: tenant admin GET /users/by-username/%s = %d, want 200; body: %s", run, i+1, username, status, body)
		}
		if !strings.Contains(string(body), username) {
			t.Fatalf("%s: node %d: user record does not name %s: %s", run, i+1, username, body)
		}
	}
	// wait bounds each node: on the first run the server is still completing
	// the admin's account after the create answered, so the caller passes
	// tenantAdminCompletionWindow; after a restart the account exists and
	// the wait is only for the node to trust the admin again.
	assertAuthenticatesEverywhere := func(run string, client *http.Client, wait time.Duration) {
		for i := range adminPorts {
			awaitUser(run, client, i, adminUsername, wait)
			t.Logf("%s: node %d authenticated the strict tenant's admin", run, i+1)
		}
	}

	startCluster("R1", true)
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	// Creating a strict tenant waits for its home group to elect a leader on
	// every node, inside the server's 25s create bound.
	sysClient := mtlsHTTPClient(t, ca, 30*time.Second)
	createVolumePoolREST(t, sysClient, leaderPort, "default")

	adminKeyPEM, tenantBody, status := createRaftTenant(t, sysClient, leaderPort, strictHomeTenantID, "Strict Home")
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("create strict tenant: status %d, body: %s", status, tenantBody)
	}
	assertHomeOnEveryNode("R1")
	adminClient := raftTenantAdminClient(t, ca, adminKeyPEM, tenantBody)
	assertAuthenticatesEverywhere("R1", adminClient, tenantAdminCompletionWindow)

	// Lagging replica. Node 3 stops; the two remaining nodes keep a quorum of
	// the home group and the tenant admin creates users on node 1. Each
	// creation is at least one home-group entry, and every
	// strictHomeSnapshotEntries entries the group snapshots and compacts its
	// log to strictHomeCompactionOverhead entries behind the snapshot, so
	// after strictHomeLagUsers creations the entries node 3 is missing are no
	// longer in any live replica's log. Node 3 can only catch up by installing
	// a snapshot: it must serve the last user created while it was down, and
	// its log must record the home group's snapshot arriving.
	const lagNode = 2
	stopNode(lagNode)
	if t.Failed() {
		t.FailNow()
	}
	userRole := role.SystemRoleID(strictHomeTenantID, role.RoleNameUser)
	lastUser := ""
	for n := range strictHomeLagUsers {
		username := fmt.Sprintf("lag-user-%02d", n)
		email := username + "@" + strictHomeTenantID + ".example.com"
		// An attempt answered with neither success nor a conflict -- the home
		// group electing a leader after node 3 left, or a create the server
		// could not confirm inside its reconcile window -- may still have
		// committed, and a retry of it then answers 409 for the user this test
		// made. A 409 therefore stops the retries, and it is success only
		// after such an ambiguous attempt and once the stored record reads
		// back as this create's; a 409 on the first attempt is a real conflict.
		var body []byte
		var status int
		ambiguous := false
		answered := pollUntil(60*time.Second, 250*time.Millisecond, func() bool {
			body, status = iraftAPIPost(t, adminClient, fmt.Sprintf("https://127.0.0.1:%d/users", adminPorts[0]),
				map[string]interface{}{
					"username": username, "email": email,
					"tenant_id": strictHomeTenantID, "role_ids": []string{userRole},
				})
			switch status {
			case http.StatusCreated, http.StatusOK, http.StatusConflict:
				return true
			}
			ambiguous = true
			return false
		})
		switch {
		case !answered:
			t.Fatalf("lag: create %s with node 3 down: status %d, body: %s", username, status, body)
		case status == http.StatusConflict && !ambiguous:
			t.Fatalf("lag: create %s with node 3 down: first attempt conflicted: %s", username, body)
		case status == http.StatusConflict:
			assertCreatedUser(t, adminClient, adminPorts[0], username, email, strictHomeTenantID, userRole)
			t.Logf("lag: %s committed on an attempt whose answer was lost; the retry's 409 is that record", username)
		}
		lastUser = username
	}
	lagLog := &nodeLog{}
	launch(lagNode, "R1-lag", lagLog)
	openNode(lagNode, "R1-lag", false)
	if t.Failed() {
		t.FailNow()
	}
	waitForHealthy(t, dataDirs[lagNode], 60*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[lagNode], 90*time.Second)
	waitForShardCount(t, mtlsHTTPClient(t, ca, 15*time.Second), adminPorts[lagNode], 2, 120*time.Second)
	awaitUser("R1-lag", adminClient, lagNode, lastUser, 120*time.Second)
	// The system shard is shard 1 (--shard-id), and no database exists, so a
	// snapshot received for any other shard is the home group's.
	homeSnapshot := ""
	for _, line := range lagLog.Lines(`msg="snapshot received"`) {
		if !strings.Contains(line, " shard=1 ") {
			homeSnapshot = line
		}
	}
	if homeSnapshot == "" {
		t.Fatalf("R1-lag: node %d serves %s but logged no home-group snapshot install; it caught up some other way", lagNode+1, lastUser)
	}
	t.Logf("R1-lag: node %d caught up past the compacted log and serves %s", lagNode+1, lastUser)

	// The whole cluster stops together, as an outage would take it.
	var stopping sync.WaitGroup
	for i := range cmds {
		stopping.Add(1)
		go func(idx int) {
			defer stopping.Done()
			stopNode(idx)
		}(i)
	}
	stopping.Wait()
	if t.Failed() {
		t.FailNow()
	}

	startCluster("R2", false)
	waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	assertHomeOnEveryNode("R2")
	assertAuthenticatesEverywhere("R2", raftTenantAdminClient(t, ca, adminKeyPEM, tenantBody), 60*time.Second)
}

// assertCreatedUser reads username back through the node on adminPort and
// fails the test unless the stored record is the one the caller's create
// asked for: its email, tenant and only role. A create whose retry answered
// 409 is success only when this holds, since the conflict alone does not say
// whose record holds the name.
func assertCreatedUser(t *testing.T, client *http.Client, adminPort int, username, email, tenantID, roleID string) {
	t.Helper()
	var body []byte
	var status int
	if !pollUntil(30*time.Second, 250*time.Millisecond, func() bool {
		body, status = iraftAPIGet(t, client,
			fmt.Sprintf("https://127.0.0.1:%d/users/by-username/%s", adminPort, username))
		return status == http.StatusOK
	}) {
		t.Fatalf("read back %s: status %d, body: %s", username, status, body)
	}
	var got struct {
		Username string   `json:"username"`
		Email    string   `json:"email"`
		TenantID string   `json:"tenant_id"`
		RoleIDs  []string `json:"role_ids"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("read back %s: decode: %v; body: %s", username, err, body)
	}
	if got.Username != username || got.Email != email || got.TenantID != tenantID || !slices.Equal(got.RoleIDs, []string{roleID}) {
		t.Fatalf("read back %s: record %+v is not the one created (email %s, tenant %s, role %s)", username, got, email, tenantID, roleID)
	}
}
