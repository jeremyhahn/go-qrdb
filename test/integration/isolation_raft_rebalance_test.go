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

// runIsolationRaftRebalanceMove, shared by TestIsolationRaft_Rebalance_Move
// and TestIsolationRaft_Rebalance_Move_SubmittedFromNonMember below, stands
// up a real 3-node cluster (same harness as TestIsolationRaft_EndToEnd /
// TestIsolationRaft_Scale_Foundation),
// creates a dedicated-raft ("IsolationRaft") database at RF=2 (two of the
// three nodes host a replica), writes data through it, then drives a
// dedicated-tenant shard MOVE end to end through the real qrdbd REST surface
// (POST /v1/rebalance/execute) -- the direct regression test for the three
// defects RebalanceService.moveTenantGroupShard shared with
// DatabaseService.ScaleForTenant before the elastic-scaling fixes were
// ported over (placement-before-key-delivery ordering, leader-routing, and
// observer-first admission; see rebalance_service.go's moveTenantGroupShard
// doc comment) PLUS a fourth defect this test itself discovered while it was
// being written: removing the source replica via RemoveFollowerNode while it
// was STILL the group's current leader stranded the surviving voter in an
// endless campaign loop, because go-quicraft's self-removing leader steps
// down as soon as it applies the config-change entry to its own log,
// without necessarily replicating it to any follower first -- the reconciler
// then tears the removed (former) leader down before that entry ever
// reaches the survivor. The fix -- transferLeadershipAwayFromRemoved,
// wired into the shared removeReplicaFromGroup helper so it also
// (harmlessly; scaleDown never removes the current leader by construction)
// protects DatabaseService.scaleDown -- transfers leadership to another
// voting replica FIRST whenever the replica about to be removed is the
// current leader.
//
// RF=2 (rather than RF=1) is deliberate: MOVE admits the destination as a
// non-voting OBSERVER and only removes the source's voting membership
// afterward. At RF=1 the source is the group's ONLY voter, so removing it
// before the destination is promoted to a voter (an asynchronous,
// out-of-band step performed by TenantShardReconciler) would transiently
// leave the group with zero voting members. At RF=2 the untouched THIRD
// replica ("peer") remains a continuous voter throughout the MOVE, so
// quorum is never at risk regardless of how quickly the destination is
// promoted -- this is the same reason DatabaseService.scaleDown's
// selectReplicasToRemove policy never drops a group's last voter.
//
// Assertions:
//   - MOVE executes successfully (moves_completed=1, moves_failed=0).
//   - Node B (the destination) serves the pre-existing data.
//   - Node B's on-disk shard directory is ciphertext (no plaintext value
//     substrings), proving the destination's snapshot-caught-up state is
//     encrypted under the tenant barrier, not plaintext.
//   - Node A (the source) stops hosting the shard and its on-disk shard
//     directory is purged (CloseAndPurge on demotion).
//   - No election storm: the cluster's node logs show at most a handful of
//     (not dozens of repeated) "campaign launched" or "no leader currently
//     known" lines emitted AFTER the MOVE is issued -- the direct proof
//     that observer-first admission (never admitting the destination
//     directly as a voter) AND transferring leadership away from the
//     source before evicting it (when the source is the group's current
//     leader) together preserved the group's existing quorum throughout.
//     See stormWatcher/stormThreshold below for the exact bound and the
//     single-digit noise floor it tolerates.
//
// Because MOVE's observer-first admission is the exact quorum-break fix
// class the elastic-scaling work introduced (and the flakiness class this
// change ports over to MOVE), TestIsolationRaft_Rebalance_Move is run
// standalone at least 3 times by the test runner/CI job to confirm it is
// storm-free on every run, not just once.
//
// TestIsolationRaft_Rebalance_Move_SubmittedFromNonMember runs the identical
// scenario but submits the rebalance-execute request through the one node
// that holds NO replica of the dedicated shard at all, rather than through a
// known shard member -- the previously unreachable path that used to risk a
// false "forwarding loop" error; see runIsolationRaftRebalanceMove's
// submitPort comment and resolveMoveLeader's doc comment (case 2) in
// rebalance_service.go.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// rebalMoveTrustDomain is the cluster trust domain used for SPIFFE node
// identities in this file's harness.
const rebalMoveTrustDomain = "qrdb.rebalance.cluster"

// rebalMoveTenantID is the tenant under test.
const rebalMoveTenantID = "rebal-t1"

// rebalMoveDBName is the dedicated-raft database created by this test.
const rebalMoveDBName = "rebaldb"

// stormWatcher wraps a *logWriter, additionally scanning every complete line
// written to it (independent of the chunk boundaries the underlying process
// pipe delivers, which need not align with line boundaries) for substrings
// that indicate a Raft election storm -- repeated campaigns or "leader
// unknown" churn -- so a test can assert NO storm occurred during a
// sensitive membership change such as an observer-first MOVE. Matching is
// gated by an armed flag so bootstrap-time elections (expected, harmless)
// are not counted; call arm() once the cluster is healthy and the operation
// under test is about to begin. Only lines naming the shard passed to arm()
// are counted: the system shard's elections and leaderless reads come from
// whatever load the node is under, not from the MOVE, and a storm detector
// that counts them fails the MOVE for a shard it never touched.
type stormWatcher struct {
	inner *logWriter

	mu      sync.Mutex
	buf     []byte
	armed   bool
	shard   string
	matches []string
}

// stormSubstrings are the log fragments considered proof of election-storm
// activity: go-quicraft logs "campaign launched" when a replica starts an
// election, and "no leader currently known" when a request is rejected
// because the shard has no confirmed leader -- both become frequent exactly
// when a config change transiently breaks quorum.
var stormSubstrings = []string{"campaign launched", "no leader currently known"}

// shardLogKeys are the attribute keys go-quicraft logs a shard ID under:
// "campaign launched" carries shard=<id>, the ReadIndex and peer.Handle
// failures carry shard_id=<id>.
var shardLogKeys = []string{"shard=", "shard_id="}

// lineNamesShard reports whether line carries a shard attribute whose value
// is exactly shard, so shard=1 does not match a watch on shard 1000.
func lineNamesShard(line, shard string) bool {
	for _, field := range strings.Fields(line) {
		for _, key := range shardLogKeys {
			if v, ok := strings.CutPrefix(field, key); ok && v == shard {
				return true
			}
		}
	}
	return false
}

func newStormWatcher(t *testing.T, prefix string) *stormWatcher {
	return &stormWatcher{inner: &logWriter{t: t, prefix: prefix}}
}

// arm enables match recording for shardID's log lines. Safe to call once the
// caller wants to start watching for storm activity (typically right before
// issuing the MOVE of shardID).
func (w *stormWatcher) arm(shardID uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.armed = true
	w.shard = strconv.FormatUint(shardID, 10)
}

func (w *stormWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.buf = append(w.buf, p...)
	for {
		idx := bytes.IndexByte(w.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(w.buf[:idx])
		w.buf = w.buf[idx+1:]
		if w.armed && lineNamesShard(line, w.shard) {
			lower := strings.ToLower(line)
			for _, s := range stormSubstrings {
				if strings.Contains(lower, s) {
					w.matches = append(w.matches, line)
					break
				}
			}
		}
	}
	w.mu.Unlock()
	// Forward the raw chunk to logWriter unchanged for its own (separate,
	// independent) line buffering / t.Logf display.
	return w.inner.Write(p)
}

// snapshot returns a copy of every matched line recorded since arm() was
// called.
func (w *stormWatcher) snapshot() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.matches))
	copy(out, w.matches)
	return out
}

// TestStormWatcher_CountsOnlyArmedShard feeds the watcher lines of the shape
// a loaded MOVE run logged: the system shard (1) losing and regaining its
// leader around the MOVE of shard 1000. Only the moved shard's lines may be
// counted, and shard=1 must not match a watch on 1000 or 10000.
func TestStormWatcher_CountsOnlyArmedShard(t *testing.T) {
	lines := []string{
		`level=INFO msg="campaign launched" shard=1000 replica=1 term=2`,
		`level=INFO msg="campaign launched" shard=1 replica=1 term=3`,
		`level=WARN msg="batched ReadIndex failed" shard_id=1 replica_id=3 num_requests=1 error="raft: not leader, no leader currently known"`,
		`level=WARN msg="peer.Handle failed" shard_id=1 replica_id=3 msg_type=19 msg_from=1 error="raft: not leader, no leader currently known"`,
		`level=WARN msg="batched ReadIndex failed" shard_id=1000 replica_id=3 num_requests=1 error="raft: not leader, no leader currently known"`,
		`level=INFO msg="campaign launched" shard=10000 replica=2 term=2`,
		`level=WARN msg="rejected message from removed replica" shard=1000 from=1 type=RequestPreVote`,
	}
	w := newStormWatcher(t, "[storm-unit] ")
	w.arm(1000)
	for _, l := range lines {
		if _, err := w.Write([]byte(l + "\n")); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	got := w.snapshot()
	want := []string{lines[0], lines[4]}
	if len(got) != len(want) {
		t.Fatalf("matched %d lines, want %d:\n%s", len(got), len(want), strings.Join(got, "\n"))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("match %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestStormWatcher_IgnoresLinesBeforeArm checks bootstrap-time elections,
// written before arm, are never counted even for the armed shard.
func TestStormWatcher_IgnoresLinesBeforeArm(t *testing.T) {
	w := newStormWatcher(t, "[storm-unit] ")
	if _, err := w.Write([]byte(`msg="campaign launched" shard=1000 replica=1 term=1` + "\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	w.arm(1000)
	if got := w.snapshot(); len(got) != 0 {
		t.Fatalf("matched %d pre-arm lines, want 0:\n%s", len(got), strings.Join(got, "\n"))
	}
}

// waitForNShardOwners polls GET /admin/topology on every port in ports until
// exactly wantOwners of them report ShardCount >= wantCount, returning their
// indices (into ports). Unlike waitForAllShardOwners (which requires EVERY
// port to own the shard, correct for RF == len(ports)), this supports
// RF < len(ports) placements such as this file's RF=2 database on a 3-node
// cluster.
func waitForNShardOwners(t *testing.T, client *http.Client, ports []int, wantCount, wantOwners int, timeout time.Duration) []int {
	t.Helper()

	deadline := time.Now().Add(timeout)
	var lastOwners []int
	for time.Now().Before(deadline) {
		var owners []int
		for i, port := range ports {
			rawURL := fmt.Sprintf("https://127.0.0.1:%d/admin/topology", port)
			body, status := iraftAPIGetTolerant(t, client, rawURL)
			if status != http.StatusOK {
				continue
			}
			var topo struct {
				ShardCount int `json:"shard_count"`
			}
			if err := json.Unmarshal(body, &topo); err != nil {
				continue
			}
			if topo.ShardCount >= wantCount {
				owners = append(owners, i)
			}
		}
		lastOwners = owners
		if len(owners) == wantOwners {
			return owners
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for exactly %d of %v to reach shard count >= %d (last owners: %v)",
		timeout, wantOwners, ports, wantCount, lastOwners)
	return nil
}

// rebalanceExecuteRequest mirrors the JSON request body handleRebalanceExecute
// (pkg/api/rest/handlers_rebalance.go) decodes for POST /v1/rebalance/execute.
type rebalanceExecuteRequest struct {
	Moves []rebalanceExecuteMove `json:"moves"`
}

// rebalanceExecuteMove mirrors a single move entry in rebalanceExecuteRequest.
type rebalanceExecuteMove struct {
	ShardID  uint64 `json:"shard_id"`
	FromNode string `json:"from_node"`
	ToNode   string `json:"to_node"`
}

// rebalanceExecuteResponse mirrors the JSON response of POST
// /v1/rebalance/execute.
type rebalanceExecuteResponse struct {
	MovesCompleted int     `json:"moves_completed"`
	MovesFailed    int     `json:"moves_failed"`
	DurationMs     int64   `json:"duration_ms"`
	FinalImbalance float64 `json:"final_imbalance"`
}

// runIsolationRaftRebalanceMove is documented in the package-level doc
// comment above. When submitFromNonMember is true, the rebalance-execute
// request is submitted through the one node that holds NO replica of the
// dedicated shard at all, instead of through a known shard member -- see
// the submitPort comment below.
func runIsolationRaftRebalanceMove(t *testing.T, submitFromNonMember bool) {
	bin := buildBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)

	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		nodeCertPaths[i], nodeKeyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, rebalMoveTrustDomain)
	}

	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[i])
		adminURL := fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
		peerAdminURLs[raftAddr] = adminURL
	}

	dataDirs := make([]string, 3)
	volumePoolDirs := make([]string, 3)
	configPaths := make([]string, 3)
	for i := range 3 {
		dataDirs[i] = t.TempDir()
		volumePoolDirs[i] = t.TempDir()
		configPaths[i] = writeIsolationRaftConfig(t, volumePoolDirs[i], peerAdminURLs)
	}

	// ---------------------------------------------------------------------
	// Start 3-node cluster, capturing each node's log output through a
	// stormWatcher so the MOVE-triggered assertion below can inspect it.
	// ---------------------------------------------------------------------
	watchers := make([]*stormWatcher, 3)
	cmds := make([]*exec.Cmd, 3)
	for i := range 3 {
		args := []string{
			"start",
			"--config", configPaths[i],
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIdentitiesFlag(t, nodeCertPaths[:]),
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--cluster-trust-domain", rebalMoveTrustDomain,
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}
		watchers[i] = newStormWatcher(t, fmt.Sprintf("[rebal-node%d] ", i+1))
		cmd := spawnNode(t, bin, args, watchers[i])
		cmds[i] = cmd
		t.Cleanup(func() {
			killNode(t, cmd)
		})
	}

	const rebalMoveBarrierPassphrase = "integration-test-rebalance-barrier-secret"

	var sealWg sync.WaitGroup
	sealWg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer sealWg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			initAndUnsealBarrier(t, ca, adminPorts[idx], rebalMoveBarrierPassphrase)
		}(i)
	}
	sealWg.Wait()

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)
	t.Logf("cluster healthy, leader on admin port %d", leaderPort)

	sysClient := mtlsHTTPClient(t, ca, 15*time.Second)

	createVolumePoolREST(t, sysClient, leaderPort, "default")
	waitForVolumePoolReady(t, sysClient, leaderPort, "default", len(dataDirs), 60*time.Second)

	// ---------------------------------------------------------------------
	// Tenant + role + user, mirroring TestIsolationRaft_Scale_Foundation.
	// ---------------------------------------------------------------------
	adminKeyPEM, tenantBody, tenantStatus := createRaftTenant(t, sysClient, leaderPort, rebalMoveTenantID, "Rebalance Move Test Tenant")
	if tenantStatus != http.StatusCreated && tenantStatus != http.StatusOK {
		t.Fatalf("create tenant: status %d, body: %s", tenantStatus, tenantBody)
	}

	// The system tenant can no longer provision records inside another
	// tenant's IAM at all (owner ruling, 2026-10-01), so this authenticates
	// as the tenant's own onboarded admin; its built-in Admin role cannot
	// create a NEW role (RolesCreate is SO-exclusive), so it self-assigns
	// its own pre-seeded SO role first.
	raftAdminClient := raftTenantAdminClient(t, ca, adminKeyPEM, tenantBody)
	selfAssignTenantSO(t, leaderPort, rebalMoveTenantID, raftAdminClient)
	roleBody, roleStatus := iraftAPIPost(t, raftAdminClient,
		fmt.Sprintf("https://127.0.0.1:%d/roles", leaderPort),
		map[string]interface{}{
			"name":      "db-admin",
			"tenant_id": rebalMoveTenantID,
			"permissions": []string{
				"databases:create", "databases:create_raft",
				"databases:create_raft",
				"databases:read",
				"databases:list",
				"kv:put",
				"kv:get",
			},
		},
	)
	if roleStatus != http.StatusCreated && roleStatus != http.StatusOK {
		t.Fatalf("create role: status %d, body: %s", roleStatus, roleBody)
	}
	var roleResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(roleBody, &roleResp); err != nil || roleResp.ID == "" {
		t.Fatalf("create role: could not decode role ID from response: %s", roleBody)
	}

	tenantCertPath, tenantKeyPath := ca.generateClientCertForTenant(t, rebalMoveTenantID)
	createBoundUser(t, raftAdminClient, leaderPort,
		map[string]interface{}{
			"username":  "admin",
			"email":     "admin@" + rebalMoveTenantID + ".example.com",
			"tenant_id": rebalMoveTenantID,
			"role_ids":  []string{roleResp.ID},
		}, tenantCertPath)
	tenantClient := mtlsHTTPClientFromFiles(t, ca, tenantCertPath, tenantKeyPath, 15*time.Second)

	// ---------------------------------------------------------------------
	// Create the dedicated-raft database at RF=2 -- see the package-level
	// doc comment for why RF=2 (not RF=1) is used.
	// ---------------------------------------------------------------------
	t.Logf("creating database %q as tenant %q (IsolationRaft, RF=2)", rebalMoveDBName, rebalMoveTenantID)
	dbBody, dbStatus := iraftAPIPost(t, tenantClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/databases", leaderPort),
		map[string]interface{}{
			"name":     rebalMoveDBName,
			"backend":  "pebble",
			"replicas": 2,
		},
	)
	if dbStatus != http.StatusCreated && dbStatus != http.StatusOK {
		t.Fatalf("create database: status %d, body: %s", dbStatus, dbBody)
	}

	var dbInfoData []byte
	var dbInfoStatus int
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		dbInfoData, dbInfoStatus = iraftAPIGetTolerant(t, tenantClient,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", leaderPort, rebalMoveDBName))
		if dbInfoStatus == http.StatusOK && len(dbInfoData) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if dbInfoStatus != http.StatusOK {
		t.Fatalf("GET /v1/databases/%s: status %d, body: %s", rebalMoveDBName, dbInfoStatus, dbInfoData)
	}
	var dbInfo struct {
		Name     string `json:"name"`
		Backend  string `json:"backend"`
		ShardID  uint64 `json:"shardId"`
		Replicas int    `json:"replicas"`
	}
	if err := json.Unmarshal(dbInfoData, &dbInfo); err != nil {
		t.Fatalf("decode database info: %v\nraw: %s", err, dbInfoData)
	}
	if dbInfo.Replicas != 2 {
		t.Fatalf("expected replicas=2, got %d\ndb info: %s", dbInfo.Replicas, dbInfoData)
	}
	if dbInfo.ShardID == 0 {
		t.Fatalf("expected shardID > 0, got 0\ndb info: %s", dbInfoData)
	}
	shardID := dbInfo.ShardID
	t.Logf("database created: backend=%s shardID=%d replicas=%d", dbInfo.Backend, shardID, dbInfo.Replicas)

	// Discover which 2 of the 3 nodes host the shard (owners), leaving
	// exactly 1 node (dst) that does not.
	t.Log("locating the 2 nodes hosting the RF=2 shard")
	ownerIdx := waitForNShardOwners(t, sysClient, adminPorts, 2, 2, 120*time.Second)
	var dstIdx int = -1
	for i := range 3 {
		isOwner := false
		for _, o := range ownerIdx {
			if o == i {
				isOwner = true
				break
			}
		}
		if !isOwner {
			dstIdx = i
			break
		}
	}
	if dstIdx < 0 {
		t.Fatalf("could not identify a non-owning node among %v (owners %v)", adminPorts, ownerIdx)
	}
	srcIdx := ownerIdx[0]
	peerIdx := ownerIdx[1]
	t.Logf("src=node%d(port %d) peer=node%d(port %d) dst=node%d(port %d)",
		srcIdx+1, adminPorts[srcIdx], peerIdx+1, adminPorts[peerIdx], dstIdx+1, adminPorts[dstIdx])

	// ---------------------------------------------------------------------
	// Write and read back pre-existing data through the RF=2 dedicated shard.
	// ---------------------------------------------------------------------
	const numKeys = 10
	values := make([]string, numKeys)
	for i := range numKeys {
		values[i] = fmt.Sprintf("rebal-val-%d", i)
	}

	t.Log("writing baseline keys to dedicated shard")
	writeDeadline := time.Now().Add(120 * time.Second)
	for i := range numKeys {
		key := fmt.Sprintf("rebal-data/%04d", i)
		val := []byte(values[i])
		var written bool
		for time.Now().Before(writeDeadline) {
			status, err := dbScopedPutTolerant(t, tenantClient, adminPorts[srcIdx], rebalMoveDBName, key, val)
			if err == nil && status == http.StatusNoContent {
				written = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !written {
			t.Fatalf("timed out writing baseline key %q", key)
		}
		if i == 0 {
			writeDeadline = time.Now().Add(30 * time.Second)
		}
	}

	for i := range numKeys {
		key := fmt.Sprintf("rebal-data/%04d", i)
		var got []byte
		var status int
		readDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(readDeadline) {
			got, status = dbScopedGetTolerant(t, tenantClient, adminPorts[srcIdx], rebalMoveDBName, key)
			if status == http.StatusOK {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if status != http.StatusOK {
			t.Fatalf("baseline key %q read timed out (last status: %d)", key, status)
		}
		if string(got) != values[i] {
			t.Fatalf("baseline key %q: got %q, want %q", key, got, values[i])
		}
	}
	t.Logf("baseline data PASS: %d keys committed and readable before MOVE", numKeys)

	// ---------------------------------------------------------------------
	// Execute the MOVE: src -> dst. Arm every node's stormWatcher immediately
	// before issuing the request so bootstrap-time elections (expected,
	// harmless) are excluded from the storm assertion below, and scope them
	// to the moved shard so system-shard churn is not charged to the MOVE.
	// ---------------------------------------------------------------------
	for _, w := range watchers {
		w.arm(shardID)
	}

	srcAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[srcIdx])
	dstAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[dstIdx])

	// submitPort is the node the client's rebalance-execute request lands
	// on first. Historically this test always targeted a known shard
	// member (src), specifically to avoid resolveGroupLeader's "guess any
	// member" fallback: that fallback used to risk a false "forwarding
	// loop" error whenever the guessed member happened to be both the
	// group's current leader AND the MOVE's own source (observed directly
	// while writing this test) -- see rebalance_service.go's
	// resolveMoveLeader doc comment, case 2. resolveMoveLeader no longer
	// guesses the MOVE's source, so submitting from dst -- which, before
	// the MOVE, is genuinely NOT a member of shard 1000 at all -- is
	// exactly that previously unreachable path, exercised here when
	// submitFromNonMember is true.
	submitPort := adminPorts[srcIdx]
	if submitFromNonMember {
		submitPort = adminPorts[dstIdx]
	}
	t.Logf("executing rebalance MOVE: shard=%d from=%s to=%s submitted-via-port=%d (non-member=%v)",
		shardID, srcAddr, dstAddr, submitPort, submitFromNonMember)

	execReq := rebalanceExecuteRequest{
		Moves: []rebalanceExecuteMove{
			{ShardID: shardID, FromNode: srcAddr, ToNode: dstAddr},
		},
	}
	execBody, execStatus := iraftAPIPost(t, sysClient,
		fmt.Sprintf("https://127.0.0.1:%d/v1/rebalance/execute", submitPort), execReq)
	if execStatus != http.StatusOK {
		t.Fatalf("POST /v1/rebalance/execute: status %d, body: %s", execStatus, execBody)
	}
	var execResp rebalanceExecuteResponse
	if err := json.Unmarshal(execBody, &execResp); err != nil {
		t.Fatalf("decode rebalance execute response: %v\nraw: %s", err, execBody)
	}
	if execResp.MovesCompleted != 1 {
		t.Fatalf("expected moves_completed=1, got %d (moves_failed=%d)\nraw: %s",
			execResp.MovesCompleted, execResp.MovesFailed, execBody)
	}
	if execResp.MovesFailed != 0 {
		t.Fatalf("expected moves_failed=0, got %d\nraw: %s", execResp.MovesFailed, execBody)
	}
	t.Logf("rebalance MOVE accepted: moves_completed=%d moves_failed=%d", execResp.MovesCompleted, execResp.MovesFailed)

	// ---------------------------------------------------------------------
	// Assertion: node B (dst) serves the pre-existing data.
	// ---------------------------------------------------------------------
	t.Log("polling destination node directly for pre-existing baseline data")
	for i := range numKeys {
		key := fmt.Sprintf("rebal-data/%04d", i)
		var got []byte
		var status int
		readDeadline := time.Now().Add(60 * time.Second)
		for time.Now().Before(readDeadline) {
			got, status = dbScopedGetTolerant(t, tenantClient, adminPorts[dstIdx], rebalMoveDBName, key)
			if status == http.StatusOK {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if status != http.StatusOK {
			t.Fatalf("destination node (port %d) key %q read timed out (last status: %d)",
				adminPorts[dstIdx], key, status)
		}
		if string(got) != values[i] {
			t.Fatalf("destination node (port %d) key %q: got %q, want %q",
				adminPorts[dstIdx], key, got, values[i])
		}
	}
	t.Logf("destination node PASS: all %d baseline keys served after MOVE", numKeys)

	// ---------------------------------------------------------------------
	// Assertion: node B's (dst) on-disk shard directory is ciphertext.
	// ---------------------------------------------------------------------
	t.Log("verifying destination node's on-disk shard directory is encrypted at rest")
	// Poll rather than a single lookup: the KV read above only proves the
	// shard is serving from its in-memory state; the on-disk directory can
	// lag slightly behind (e.g. first SST flush) even once reads succeed.
	var dstShardDir string
	dstDirDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(dstDirDeadline) {
		dstShardDir = findDirEndingIn(t, volumePoolDirs[dstIdx], filepath.Join("databases", rebalMoveDBName))
		if dstShardDir != "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if dstShardDir == "" {
		t.Fatalf("destination node %d (port %d): dedicated shard dir for %s not found under %s",
			dstIdx+1, adminPorts[dstIdx], rebalMoveDBName, volumePoolDirs[dstIdx])
	}
	for _, val := range values {
		assertPlaintextAbsentInDir(t, dstShardDir, []byte(val))
	}
	t.Logf("destination node PASS: no plaintext baseline values found under %s", dstShardDir)

	// ---------------------------------------------------------------------
	// Assertion: node A (src) stopped hosting the shard and its on-disk
	// shard directory was purged.
	// ---------------------------------------------------------------------
	t.Log("waiting for source node to stop hosting the dedicated shard")
	waitForShardCountBelow(t, sysClient, adminPorts[srcIdx], 2, 60*time.Second)
	t.Logf("source node PASS: no longer hosts the dedicated shard")

	t.Log("verifying source node's on-disk shard directory was purged")
	purgeDeadline := time.Now().Add(30 * time.Second)
	var srcShardDir string
	for time.Now().Before(purgeDeadline) {
		srcShardDir = findDirEndingIn(t, volumePoolDirs[srcIdx], filepath.Join("databases", rebalMoveDBName))
		if srcShardDir == "" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if srcShardDir != "" {
		t.Fatalf("source node %d (port %d): shard dir %q still present after MOVE (expected purge)",
			srcIdx+1, adminPorts[srcIdx], srcShardDir)
	}
	t.Logf("source node PASS: shard dir purged")

	// The peer replica (untouched by the MOVE) must still serve the data,
	// confirming the group's continuous voter was never disturbed. A request
	// resolves its tenant through a system-shard read before it reaches the
	// moved shard, and that read answers 503 while the system shard is
	// between leaders, which load alone produces. So 503 means "not served
	// yet" and is waited out; any other status fails at once. A disturbed
	// voter on the moved shard is the storm assertion's to catch.
	for i := range numKeys {
		key := fmt.Sprintf("rebal-data/%04d", i)
		var got []byte
		var status int
		peerDeadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(peerDeadline) {
			got, status = dbScopedGetTolerant(t, tenantClient, adminPorts[peerIdx], rebalMoveDBName, key)
			if status != http.StatusServiceUnavailable {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if status != http.StatusOK {
			t.Fatalf("peer node (port %d) key %q: status %d, body: %s", adminPorts[peerIdx], key, status, got)
		}
		if string(got) != values[i] {
			t.Fatalf("peer node (port %d) key %q: got %q, want %q", adminPorts[peerIdx], key, got, values[i])
		}
	}
	t.Logf("peer node PASS: untouched replica still serves all %d baseline keys", numKeys)

	// ---------------------------------------------------------------------
	// Assertion: NO election storm on the moved shard. A genuine
	// quorum-break storm looks like dozens of repeated "campaign
	// launched"/"no leader currently known" lines for that shard spanning
	// many seconds, as a stranded voter retries an election
	// it can never win alone (this is exactly what an earlier version of
	// this fix produced: a surviving voter campaigned every ~2-3s for over
	// 55s straight after its peer self-removed as leader without replicating
	// the removal first). A single transient "no leader currently known" is
	// expected and harmless: the destination's freshly-started (join=true)
	// shard has not yet received its first heartbeat when this test's own
	// polling loop above issues its first read. stormThreshold draws the
	// line well below "storm" (dozens) but above that single-digit noise
	// floor.
	// ---------------------------------------------------------------------
	const stormThreshold = 5
	var allMatches []string
	for i, w := range watchers {
		for _, m := range w.snapshot() {
			allMatches = append(allMatches, fmt.Sprintf("node%d: %s", i+1, m))
		}
	}
	if len(allMatches) > stormThreshold {
		t.Fatalf("election storm detected (%d matching log lines since MOVE was issued, threshold %d):\n%s",
			len(allMatches), stormThreshold, strings.Join(allMatches, "\n"))
	}
	t.Logf("storm-free PASS: %d campaign/leader-unknown log line(s) since the MOVE was issued (threshold %d)",
		len(allMatches), stormThreshold)

	// ---------------------------------------------------------------------
	// Graceful shutdown.
	// ---------------------------------------------------------------------
	for i, cmd := range cmds {
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Logf("signal node %d: %v (may have already exited)", i+1, err)
		}
	}
	for i, cmd := range cmds {
		_ = awaitNodeExit(t, cmd, nodeStopTimeout)
		t.Logf("node %d stopped", i+1)
	}

	t.Logf("isolation-raft rebalance MOVE PASS (submitFromNonMember=%v)", submitFromNonMember)
}

// TestIsolationRaft_Rebalance_Move submits the MOVE through a node that
// already hosts the dedicated shard (a group member) -- see the
// package-level doc comment above.
func TestIsolationRaft_Rebalance_Move(t *testing.T) {
	runIsolationRaftRebalanceMove(t, false)
}

// TestIsolationRaft_Rebalance_Move_SubmittedFromNonMember is the direct
// end-to-end regression test for the false "forwarding loop" bug fixed in
// RebalanceService.resolveMoveLeader (pkg/service/rebalance_service.go): it
// submits the IDENTICAL MOVE through the one node that holds NO replica of
// the dedicated shard at all -- the previously unreachable path that used
// to risk forwardToLeader rejecting a second, legitimate hop as a loop (see
// resolveMoveLeader's doc comment, case 2, and this file's submitPort
// comment above). TestMoveTenantGroupShard_LeaderRouting_NonMemberAvoidsGuessingTheSource
// (pkg/service/rebalance_move_test.go) is the equivalent, faster
// service-layer regression test for the same fix; this test is the
// end-to-end proof over the real qrdbd REST surface across 3 real nodes.
func TestIsolationRaft_Rebalance_Move_SubmittedFromNonMember(t *testing.T) {
	runIsolationRaftRebalanceMove(t, true)
}
