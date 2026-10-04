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

// admin_cluster_cli_test.go drives the cluster-relevant slice of the `qrdb
// admin` command tree (capabilities, topology, member add/remove, snapshot)
// through the real client CLI binary (cmd/qrdb), against every transport
// (REST, gRPC, QUIC, MCP, Unix). Per the CLI coverage audit (Batch F),
// `admin snapshot` and `admin capabilities` had ZERO integration coverage,
// and `admin topology`/`admin member add/remove` were REST-only or
// single-protocol.
//
// `admin capabilities` reports a purely compile-time, per-process feature
// set (pkg/capabilities.Get(), gated only by build tags -- see
// buildBinary/buildCLIBinary's `-tags "... qrdb_ts qrdb_vector ..."`), so it
// is tested on the single-node startMultiProtocolNode harness.
//
// `admin topology`, `admin member add/remove`, and `admin snapshot` are
// cluster-relevant and are tested on a purpose-built 3-node, all-transport,
// all-TLS, auth-enabled cluster (start3NodeMultiProtocolCluster below,
// modeled on startMultiProtocolNode + startThreeNodeClusterSPIFFE).
//
//   - `admin topology` is a local, read-only view "as seen from this node"
//     (service.AdminService.GetTopology never touches Raft consensus), so it
//     is exercised against BOTH the leader and a follower.
//   - `admin snapshot` (service.AdminService.RequestSnapshot ->
//     quicraft.Host.RequestSnapshot) triggers a local log-compaction
//     snapshot and is NOT leader-gated at the quicraft layer, so it too is
//     exercised against both the leader and a follower.
//   - `admin member add`/`admin member remove` propose Raft configuration
//     changes, which only the shard's current LEADER can commit. Every
//     transport except REST returns service.NotLeaderError verbatim when a
//     write lands on a follower (pkg/api/grpc/errors.go, pkg/api/quic/
//     errors.go both map *service.NotLeaderError straight through instead of
//     forwarding -- only pkg/api/rest/server.go implements
//     forwardToLeader). Follower-forwarding over REST is already covered by
//     TestCluster_MembershipAdd/Remove_ForwardsToLeader (cluster_test.go),
//     so this file targets the LEADER directly on every protocol -- the
//     realistic, documented way any non-REST client performs a cluster
//     write -- to prove all 5 transports commit membership changes
//     end-to-end when routed correctly.
package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// adminClusterPassphrase is the shared system-barrier passphrase every node
// in the 3-node admin-cluster harness is initialized and unsealed with.
const adminClusterPassphrase = "admin-cluster-harness-passphrase"

// adminClusterSpareReplica is a genesis member of the cluster
// start3NodeMultiProtocolCluster boots beyond the 3 control nodes, one per
// protocol in multiProtocols. Its own process keeps running for the whole
// test, but go-quicraft stops a replica participating in its shard within
// seconds of a Raft config change that removes it -- including refreshing
// its own feature-gate advertisement's lease, which needs the very
// consensus membership just took it out of -- so a spare's address is only
// a valid joiner for a brief window right after ITS OWN removal. Every
// protocol therefore gets its OWN spare, removed only immediately before
// that protocol's add/remove cycle (see testAdminMemberLifecycleOverProtocol),
// rather than one spare shared across all 5 and removed once up front, which
// is time enough for earlier protocols but not later ones under load
// (go-qrdb#593).
type adminClusterSpareReplica struct {
	replicaID uint64
	raftAddr  string
}

// start3NodeMultiProtocolCluster boots a (3+len(multiProtocols))-node qrdbd
// cluster with every transport enabled (REST, gRPC, QUIC, MCP, Unix) on each
// node, TLS and authentication on, and a passphrase-strategy system barrier
// initialized and unsealed on every node. Every node beyond the first 3 is a
// genesis member of shard 1 and is handed back as a adminClusterSpareReplica,
// one per protocol, still a CURRENT member -- see that type's doc comment for
// why removal is deferred to the moment each spare is actually used. It
// returns the 3 control nodes as *multiProtoNode (the same handle type
// startMultiProtocolNode uses, so every existing runCLIProto-based test
// helper works unmodified against a cluster member), the index of the node
// that won leadership of shard 1, and the spares slice (len(multiProtocols),
// in the same order).
func start3NodeMultiProtocolCluster(t *testing.T) (nodes [3]*multiProtoNode, leaderIdx int, spares []adminClusterSpareReplica) {
	t.Helper()

	n := 3 + len(multiProtocols)
	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	cfgPath := writeMultiProtocolSealConfig(t)

	raftPorts := allocatePorts(t, n)
	quicPorts := allocatePorts(t, n)
	adminPorts := allocateTCPPorts(t, n)
	grpcPorts := allocateTCPPorts(t, n)
	mcpPorts := allocateTCPPorts(t, n)
	peersFlag := buildPeersFlag(raftPorts)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, len(raftPorts))

	dataDirs := make([]string, n)
	unixSockets := make([]string, n)

	for i := range n {
		dataDirs[i] = t.TempDir()

		// Unix socket paths are limited to ~108 bytes by the kernel and
		// t.TempDir() can exceed that -- see startMultiProtocolNode's
		// identical rationale.
		sockDir, err := os.MkdirTemp("", fmt.Sprintf("qrdb-admincluster-%d-", i+1))
		if err != nil {
			t.Fatalf("start3NodeMultiProtocolCluster: socket dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(sockDir) })
		unixSockets[i] = filepath.Join(sockDir, "qrdbd.sock")

		args := []string{
			"start",
			"--config", cfgPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDirs[i],
			"--sm", "mvcc",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--auth-enabled",
			"--enable-rest",
			"--enable-grpc",
			"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPorts[i]),
			"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPorts[i]),
			"--enable-unix", "--unix-socket-path", unixSockets[i],
		}
		args = append(args, nodeTLS[i]...)

		startNode(t, bin, args, fmt.Sprintf("[admincluster-node%d] ", i+1))
	}

	// Sealed-window: initialize and unseal the passphrase barrier on all n
	// nodes in parallel, exactly as TestClusterEnableEncryption_SystemScope_RF3
	// does for the "none" strategy -- each node's local storage barrier must
	// be unsealed before that node's own Raft host (and therefore every
	// other transport on that node) starts serving.
	var sealWg sync.WaitGroup
	sealWg.Add(n)
	for i := range n {
		go func(idx int) {
			defer sealWg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			initAndUnsealBarrier(t, ca, adminPorts[idx], adminClusterPassphrase)
		}(i)
	}
	sealWg.Wait()

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	// Leadership of an 8-voter genesis group can land on any of them,
	// including one of the 5 nodes meant as spares -- the 3 "control" nodes
	// are therefore chosen AFTER the election, always including whichever
	// genesis index actually won it, rather than being hardcoded as the
	// first 3. The rest, in their original order, are the spares.
	var controlGenesisIdx []int
	for i := range n {
		if adminPorts[i] == leaderPort {
			controlGenesisIdx = append(controlGenesisIdx, i)
		}
	}
	for i := range n {
		if len(controlGenesisIdx) == 3 {
			break
		}
		if adminPorts[i] != leaderPort {
			controlGenesisIdx = append(controlGenesisIdx, i)
		}
	}
	isControl := make(map[int]bool, 3)
	for _, idx := range controlGenesisIdx {
		isControl[idx] = true
	}

	spares = make([]adminClusterSpareReplica, 0, n-3)
	for i := range n {
		if isControl[i] {
			continue
		}
		spares = append(spares, adminClusterSpareReplica{
			replicaID: uint64(i + 1),
			raftAddr:  fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
		})
	}

	clientCert, clientKey := ca.generateAdminClientCert(t, t.TempDir())

	leaderIdx = -1
	for pos, idx := range controlGenesisIdx {
		nodes[pos] = &multiProtoNode{
			cli:        cli,
			ca:         ca,
			dataDir:    dataDirs[idx],
			clientCert: clientCert,
			clientKey:  clientKey,
			unixSocket: unixSockets[idx],
			restPort:   adminPorts[idx],
			grpcPort:   grpcPorts[idx],
			mcpPort:    mcpPorts[idx],
			quicPort:   quicPorts[idx],
			passphrase: adminClusterPassphrase,
		}
		if adminPorts[idx] == leaderPort {
			leaderIdx = pos
		}
	}
	if leaderIdx == -1 {
		t.Fatalf("start3NodeMultiProtocolCluster: leader admin port %d not found among the control nodes %v",
			leaderPort, controlGenesisIdx)
	}

	// Confirm every transport on every control node is actually serving
	// before handing the cluster to callers.
	for i := range 3 {
		for _, proto := range multiProtocols {
			waitForCLIProtoReady(t, nodes[i], proto, 60*time.Second)
		}
	}

	return nodes, leaderIdx, spares
}

// adminTopologyDisplay mirrors transport.TopologyResponse's wire JSON
// shape, decoded from `admin topology --output json`.
type adminTopologyDisplay struct {
	NodeID      string `json:"node_id"`
	RaftAddress string `json:"raft_address"`
	ShardCount  int    `json:"shard_count"`
	Shards      []struct {
		ShardID   uint64            `json:"shard_id"`
		ReplicaID uint64            `json:"replica_id"`
		IsLeader  bool              `json:"is_leader"`
		LeaderID  uint64            `json:"leader_id"`
		Nodes     map[string]string `json:"nodes"`
	} `json:"shards"`
}

// adminCapabilitiesDisplay mirrors transport.CapabilitiesResponse's wire
// JSON shape, decoded from `admin capabilities --output json`.
type adminCapabilitiesDisplay struct {
	KV          bool `json:"kv"`
	MVCC        bool `json:"mvcc"`
	Transaction bool `json:"transaction"`
	Batch       bool `json:"batch"`
	Watch       bool `json:"watch"`
	Lease       bool `json:"lease"`
	Index       bool `json:"index"`
	TimeSeries  bool `json:"timeSeries"`
	Vector      bool `json:"vector"`
}

// adminPeersDisplay mirrors transport.PeersResponse's wire JSON shape,
// decoded from `admin peers --output json`.
type adminPeersDisplay struct {
	Shards []struct {
		ShardID  uint64 `json:"shard_id"`
		LeaderID uint64 `json:"leader_id"`
		Peers    []struct {
			ReplicaID uint64 `json:"replica_id"`
			Address   string `json:"address"`
			IsLeader  bool   `json:"is_leader"`
		} `json:"peers"`
	} `json:"shards"`
}

// TestAdminCapabilities_CLI_PerProtocol drives `qrdb admin capabilities`
// through the CLI binary against every transport on a single node. The
// response is a pure compile-time feature set, so every field is expected
// to be true given buildBinary/buildCLIBinary's `qrdb_ts qrdb_vector` tags.
func TestAdminCapabilities_CLI_PerProtocol(t *testing.T) {
	node := startMultiProtocolNode(t)

	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			out, err := runCLIProto(t, node, proto, "--output", "json", "admin", "capabilities")
			if err != nil {
				t.Fatalf("[%s] admin capabilities: %v\noutput: %s", proto.name, err, out)
			}
			var caps adminCapabilitiesDisplay
			if jerr := json.Unmarshal([]byte(out), &caps); jerr != nil {
				t.Fatalf("[%s] decode `admin capabilities` JSON: %v\noutput: %s", proto.name, jerr, out)
			}
			want := adminCapabilitiesDisplay{
				KV: true, MVCC: true, Transaction: true, Batch: true,
				Watch: true, Lease: true, Index: true, TimeSeries: true, Vector: true,
			}
			if caps != want {
				t.Fatalf("[%s] admin capabilities: got %+v, want %+v", proto.name, caps, want)
			}

			// Table-format sanity: every feature name must be listed.
			tableOut, err := runCLIProto(t, node, proto, "admin", "capabilities")
			if err != nil {
				t.Fatalf("[%s] admin capabilities (table): %v\noutput: %s", proto.name, err, tableOut)
			}
			for _, feature := range []string{"kv", "mvcc", "transaction", "batch", "watch", "lease", "index", "timeSeries", "vector"} {
				if !strings.Contains(tableOut, feature) {
					t.Fatalf("[%s] admin capabilities (table): missing feature %q in output: %s", proto.name, feature, tableOut)
				}
			}
		})
	}
}

// TestAdminCluster_CLI_PerProtocol drives the cluster-relevant `qrdb admin`
// subcommands -- topology, snapshot, and member add/remove -- through the
// CLI binary against every transport, sharing one 3-node cluster across all
// 5 protocol subtests (each subtest namespaces its own throwaway replica ID
// so they never collide).
func TestAdminCluster_CLI_PerProtocol(t *testing.T) {
	nodes, leaderIdx, spares := start3NodeMultiProtocolCluster(t)
	followerIdx := (leaderIdx + 1) % 3

	// quicraft refuses to re-admit a replica ID -- or a member identity --
	// it has ever removed ("raft: node was previously removed" /
	// "node identity belongs to a replica this shard has removed"), so a
	// throwaway replica ID cannot be recomputed as "one more than whatever
	// admin peers currently shows" in each protocol subtest -- once a
	// subtest adds AND removes its throwaway replica, that ID is
	// permanently retired and a later subtest recomputing from the
	// (now 3+len(spares)-member) peer list would collide with it. A single
	// monotonic counter, starting one past the highest genesis replica ID,
	// guarantees every subtest gets a fresh, never-before-used replica ID.
	nextReplicaID := uint64(len(nodes)+len(spares)) + 1

	for i, proto := range multiProtocols {
		proto, spare := proto, spares[i]
		t.Run(proto.name, func(t *testing.T) {
			testAdminTopologyOverProtocol(t, nodes[leaderIdx], proto, "leader")
			testAdminTopologyOverProtocol(t, nodes[followerIdx], proto, "follower")

			testAdminSnapshotOverProtocol(t, nodes[followerIdx], proto, "follower")
			testAdminSnapshotOverProtocol(t, nodes[leaderIdx], proto, "leader")

			testAdminMemberLifecycleOverProtocol(t, nodes[leaderIdx], proto, nextReplicaID, spare)
		})
		nextReplicaID++
	}
}

// testAdminTopologyOverProtocol runs `admin topology` against node (labeled
// role for failure messages) over proto and asserts the response describes
// shard 1 as seen from that node.
func testAdminTopologyOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol, role string) {
	t.Helper()

	out, err := runCLIProto(t, node, proto, "--output", "json", "admin", "topology")
	if err != nil {
		t.Fatalf("[%s/%s] admin topology: %v\noutput: %s", proto.name, role, err, out)
	}
	var topo adminTopologyDisplay
	if jerr := json.Unmarshal([]byte(out), &topo); jerr != nil {
		t.Fatalf("[%s/%s] decode `admin topology` JSON: %v\noutput: %s", proto.name, role, jerr, out)
	}
	if topo.NodeID == "" {
		t.Fatalf("[%s/%s] admin topology: empty NodeID, output: %s", proto.name, role, out)
	}
	if topo.RaftAddress == "" {
		t.Fatalf("[%s/%s] admin topology: empty RaftAddress, output: %s", proto.name, role, out)
	}
	if topo.ShardCount < 1 || len(topo.Shards) != topo.ShardCount {
		t.Fatalf("[%s/%s] admin topology: ShardCount=%d len(Shards)=%d, want both >=1 and equal",
			proto.name, role, topo.ShardCount, len(topo.Shards))
	}
	found := false
	for _, s := range topo.Shards {
		if s.ShardID == 1 {
			found = true
			if len(s.Nodes) < 3 {
				t.Fatalf("[%s/%s] admin topology: shard 1 has %d membership entries, want >=3 (3-node cluster), shard=%+v",
					proto.name, role, len(s.Nodes), s)
			}
		}
	}
	if !found {
		t.Fatalf("[%s/%s] admin topology: shard 1 not present in %+v", proto.name, role, topo.Shards)
	}

	// Table-format sanity.
	tableOut, err := runCLIProto(t, node, proto, "admin", "topology")
	if err != nil {
		t.Fatalf("[%s/%s] admin topology (table): %v\noutput: %s", proto.name, role, err, tableOut)
	}
	if !strings.Contains(tableOut, topo.NodeID) {
		t.Fatalf("[%s/%s] admin topology (table): missing NodeID %s in output: %s", proto.name, role, topo.NodeID, tableOut)
	}
}

// testAdminSnapshotOverProtocol runs `admin snapshot --shard-id 1` against
// node (labeled role) over proto and asserts it succeeds and the shard
// remains readable afterward -- proving the request is a genuine,
// non-destructive log-compaction trigger rather than a stub.
func testAdminSnapshotOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol, role string) {
	t.Helper()

	key := fmt.Sprintf("admincluster/snapshot/%s/%s", proto.name, role)
	out, err := runCLIProto(t, node, proto, "kv", "put", key, "pre-snapshot-value")
	if err != nil {
		t.Fatalf("[%s/%s] kv put %s: %v\noutput: %s", proto.name, role, key, err, out)
	}

	out, err = runCLIProto(t, node, proto, "admin", "snapshot", "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s/%s] admin snapshot --shard-id 1: %v\noutput: %s", proto.name, role, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s/%s] admin snapshot: expected OK, got: %s", proto.name, role, out)
	}

	// admin snapshot requires --shard-id.
	out, err = runCLIProto(t, node, proto, "admin", "snapshot")
	if err == nil {
		t.Fatalf("[%s/%s] admin snapshot (missing --shard-id) unexpectedly succeeded, output: %s", proto.name, role, out)
	}

	// The shard must remain fully readable across the leader/replica set
	// after the snapshot request completes.
	getOut, err := runCLIProto(t, node, proto, "kv", "get", key)
	if err != nil {
		t.Fatalf("[%s/%s] kv get %s (post-snapshot): %v\noutput: %s", proto.name, role, key, err, getOut)
	}
	if !strings.Contains(getOut, "pre-snapshot-value") {
		t.Fatalf("[%s/%s] kv get %s (post-snapshot): expected pre-snapshot-value, got: %s", proto.name, role, key, getOut)
	}
}

// testAdminMemberLifecycleOverProtocol first removes spare -- a genesis
// member of shard 1 dedicated to this protocol, still a CURRENT member at
// this point (see adminClusterSpareReplica's doc comment) -- then adds a
// brand-new throwaway replica newReplicaID at spare's now-freed real Raft
// address against the cluster LEADER over proto, confirms it is reflected
// in `admin peers`, then removes it and confirms it disappears -- the
// realistic non-REST usage pattern documented in this file's header comment
// (member writes must be routed to the leader on every transport except
// REST, which alone auto-forwards).
//
// The add happens immediately after spare's removal, with no other Raft
// traffic against shard 1 in between: go-quicraft stops a removed replica
// participating in its old shard within seconds, including refreshing its
// own feature-gate advertisement's lease (see adminClusterSpareReplica), so
// spare's address is only a valid joiner for a brief window right after its
// removal -- every protocol gets its own spare precisely so this window
// never has to stretch across 5 protocols' worth of topology and snapshot
// calls.
//
// newReplicaID must never have been used by an earlier add/remove cycle in
// this test run -- see TestAdminCluster_CLI_PerProtocol's monotonic counter,
// which exists because quicraft permanently refuses to re-admit a
// once-removed replica ID ("raft: node was previously removed") or member
// identity ("node identity belongs to a replica this shard has removed").
// Reusing spare's ADDRESS is fine: that restriction is keyed on replica ID
// and member identity, never on address.
func testAdminMemberLifecycleOverProtocol(t *testing.T, leader *multiProtoNode, proto multiProtocol, newReplicaID uint64, spare adminClusterSpareReplica) {
	t.Helper()

	removeSpareOut, err := runCLIProto(t, leader, proto, "admin", "member", "remove",
		fmt.Sprintf("%d", spare.replicaID), "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s] admin member remove (spare replica %d): %v\noutput: %s", proto.name, spare.replicaID, err, removeSpareOut)
	}
	if !strings.Contains(removeSpareOut, "OK") {
		t.Fatalf("[%s] admin member remove (spare): expected OK, got: %s", proto.name, removeSpareOut)
	}

	// The replica being added is on no real node of its own -- it shares
	// spare's now-freed address -- so no committed membership names it yet:
	// its member identity is supplied, as an operator does for a brand-new
	// node. It is unique per replica ID because go-quicraft refuses one
	// identity bound to two replicas (current or once-removed).
	sum := sha256.Sum256([]byte(fmt.Sprintf("throwaway-replica-%d", newReplicaID)))
	identity := "spki:" + hex.EncodeToString(sum[:])

	out, err := runCLIProto(t, leader, proto, "admin", "member", "add", spare.raftAddr,
		"--shard-id", "1", "--replica-id", fmt.Sprintf("%d", newReplicaID), "--identity", identity)
	if err != nil {
		t.Fatalf("[%s] admin member add (replica %d): %v\noutput: %s", proto.name, newReplicaID, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] admin member add: expected OK, got: %s", proto.name, out)
	}

	if !adminPeersContainsReplica(t, leader, proto, newReplicaID) {
		t.Fatalf("[%s] admin peers: replica %d not present after admin member add", proto.name, newReplicaID)
	}

	// admin member remove takes the replica ID as a positional argument and
	// --shard-id as a flag.
	out, err = runCLIProto(t, leader, proto, "admin", "member", "remove", fmt.Sprintf("%d", newReplicaID), "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s] admin member remove (replica %d): %v\noutput: %s", proto.name, newReplicaID, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] admin member remove: expected OK, got: %s", proto.name, out)
	}

	if adminPeersContainsReplica(t, leader, proto, newReplicaID) {
		t.Fatalf("[%s] admin peers: replica %d still present after admin member remove", proto.name, newReplicaID)
	}

	// admin member remove: an invalid (non-numeric) replica ID argument.
	out, err = runCLIProto(t, leader, proto, "admin", "member", "remove", "not-a-number", "--shard-id", "1")
	if err == nil {
		t.Fatalf("[%s] admin member remove (non-numeric replica ID) unexpectedly succeeded, output: %s", proto.name, out)
	}
}

// adminPeersContainsReplica reports whether `admin peers` for shard 1
// against node over proto lists replicaID as a peer.
func adminPeersContainsReplica(t *testing.T, node *multiProtoNode, proto multiProtocol, replicaID uint64) bool {
	t.Helper()
	out, err := runCLIProto(t, node, proto, "--output", "json", "admin", "peers")
	if err != nil {
		t.Fatalf("[%s] admin peers: %v\noutput: %s", proto.name, err, out)
	}
	var peers adminPeersDisplay
	if jerr := json.Unmarshal([]byte(out), &peers); jerr != nil {
		t.Fatalf("[%s] decode `admin peers` JSON: %v\noutput: %s", proto.name, jerr, out)
	}
	for _, shard := range peers.Shards {
		if shard.ShardID != 1 {
			continue
		}
		for _, p := range shard.Peers {
			if p.ReplicaID == replicaID {
				return true
			}
		}
	}
	return false
}
