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
// `admin topology`, `admin member add/add-observer/remove/promote`, and
// `admin snapshot` are cluster-relevant and are tested on a purpose-built
// 3-node, all-transport, all-TLS, auth-enabled cluster
// (start3NodeMultiProtocolCluster below, modeled on startMultiProtocolNode +
// startThreeNodeClusterSPIFFE).
//
//   - `admin topology` is a local, read-only view "as seen from this node"
//     (service.AdminService.GetTopology never touches Raft consensus), so it
//     is exercised against every node, leader and followers alike.
//   - `admin snapshot` (service.AdminService.RequestSnapshot ->
//     quicraft.Host.RequestSnapshot) triggers a local log-compaction
//     snapshot and is NOT leader-gated at the quicraft layer, so it too is
//     exercised against every node.
//   - `admin replica-states` reports only the answering node's replicas and
//     is never forwarded, so it is asked of all 3 control nodes, which must
//     report one hash for shard 1 once they report one applied index.
//   - `admin member add`/`add-observer`/`remove`/`promote` propose Raft
//     configuration changes, which only the shard's current LEADER can
//     commit. Every node serves them on every transport: a follower that
//     refuses one as *service.NotLeaderError forwards it, single hop, to
//     the leader under a signed assertion of the original caller (see
//     docs/replica-forwarding/), and the leader authorizes that caller.
//     The membership changes are therefore driven against a node that
//     currently FOLLOWS shard 1's leader, on every protocol, so that
//     forwarding path is what the test proves.
package integration_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// adminClusterPassphrase is the shared system-barrier passphrase every node
// in the 3-node admin-cluster harness is initialized and unsealed with.
const adminClusterPassphrase = "admin-cluster-harness-passphrase"

// adminClusterTrustDomain is the SPIFFE trust domain of the harness's node
// certificates, and the --cluster-trust-domain every node is started with.
const adminClusterTrustDomain = "qrdb.cluster"

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
// helper works unmodified against a cluster member) and the spares slice
// (len(multiProtocols), in the same order). It deliberately does not say
// which control node leads: leadership moves, and every node must serve
// every admin command whoever holds it (see adminClusterFollower).
//
// Shares startMultiProtocolNodeWithSM's (see multi_protocol_helpers_test.go)
// bring-your-own TLS shape -- ca.clusterNodeSPIFFETLS is also
// bring-your-own, so every node's s.ceremonyCA is nil -- which qrdb init supports directly
// (go-qrdb#548). Each node's system barrier is local, so each one runs its
// own ceremony during its own sealed window in parallel, all handed the
// SAME bring-your-own-certificate admin identity (minted once, before the
// loop): first boot on whichever node's write lands first binds it, and
// every other node's write is an idempotent no-op once raft replicates it.
func start3NodeMultiProtocolCluster(t *testing.T) (nodes [3]*multiProtoNode, spares []adminClusterSpareReplica) {
	t.Helper()
	return startMultiProtocolCluster(t, len(multiProtocols))
}

// startMultiProtocolCluster is start3NodeMultiProtocolCluster with the
// number of spare genesis members as a parameter; spareCount 0 boots a plain
// 3-node cluster. Each control node's handle carries its process, for a test
// that stops one.
func startMultiProtocolCluster(t *testing.T, spareCount int) (nodes [3]*multiProtoNode, spares []adminClusterSpareReplica) {
	t.Helper()

	n := 3 + spareCount
	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, n)
	quicPorts := allocatePorts(t, n)
	adminPorts := allocateTCPPorts(t, n)
	grpcPorts := allocateTCPPorts(t, n)
	mcpPorts := allocateTCPPorts(t, n)
	peersFlag := buildPeersFlag(raftPorts)
	raftAddrs := make([]string, n)
	peerAdminURLs := make(map[string]string, n)
	for i, port := range raftPorts {
		raftAddrs[i] = fmt.Sprintf("127.0.0.1:%d", port)
		peerAdminURLs[raftAddrs[i]] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
	}
	// Every node shares one host on its own admin port, so REST's leader
	// forwarding needs each peer's admin URL spelled out: its fallback --
	// the leader's Raft host on THIS node's admin port -- names this node.
	cfgPath := writeMultiProtocolSealConfig(t, peerAdminURLsYAML(peerAdminURLs))
	// Node certificates carry the SPIFFE node ID a follower signs its
	// on-behalf caller assertion with when it forwards a membership change
	// to shard 1's leader; without it there is no assertion and the leader
	// sees only the node certificate, which names no account.
	nodeTLS, memberIDs := ca.clusterNodeSPIFFETLS(t, raftAddrs, adminClusterTrustDomain)

	dataDirs := make([]string, n)
	unixSockets := make([]string, n)
	cmds := make([]*exec.Cmd, n)

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
			"--raft-address", raftAddrs[i],
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

		cmds[i] = startNode(t, bin, args, fmt.Sprintf("[admincluster-node%d] ", i+1))
	}

	// "qrdb init" is the only system bootstrap (go-qrdb#548): each node's
	// system barrier is local, so each one still runs its own ceremony
	// during its own sealed window, in parallel, exactly as
	// TestClusterEnableEncryption_SystemScope_RF3 does for the "none"
	// strategy. initAndUnsealBarrier's admin identity is this CA's ONE
	// shared bootstrap admin certificate (see testCA.adminIdentity), so
	// calling it once per node hands every one of them the identical
	// certificate: first boot on whichever node's write lands first binds
	// it, and every other node's write is an idempotent no-op once raft
	// replicates it, so this one certificate authenticates against every
	// node.
	var clientCert, clientKey string
	var sealWg sync.WaitGroup
	sealWg.Add(n)
	for i := range n {
		go func(idx int) {
			defer sealWg.Done()
			waitForBarrierStatusReachable(t, ca, adminPorts[idx], 90*time.Second)
			cp, kp := initAndUnsealBarrier(t, ca, adminPorts[idx], adminClusterPassphrase)
			if idx == 0 {
				clientCert, clientKey = cp, kp
			}
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
			raftAddr:  raftAddrs[i],
		})
	}

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
			proc:       cmds[idx],
		}
	}

	// Confirm every transport on every control node is actually serving
	// before handing the cluster to callers.
	for i := range 3 {
		for _, proto := range multiProtocols {
			waitForCLIProtoReady(t, nodes[i], proto, 60*time.Second)
		}
	}

	return nodes, spares
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
//
// Topology and snapshot run against every control node, whichever leads.
// The membership changes run against a node that follows shard 1's leader
// at the moment each protocol's turn starts: a follower refuses a membership
// change as not the leader, so that is the path that proves it reaches the
// leader on the caller's behalf. Leadership can move mid-turn; a change that
// lands on a node that has since become the leader is served there, which is
// equally correct, and a follower that has since lost track of the leader
// answers with its own refusal, which fails the test rather than being
// retried.
func TestAdminCluster_CLI_PerProtocol(t *testing.T) {
	nodes, spares := start3NodeMultiProtocolCluster(t)

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
			for j, node := range nodes {
				role := fmt.Sprintf("node%d", j+1)
				testAdminTopologyOverProtocol(t, node, proto, role)
				testAdminSnapshotOverProtocol(t, node, proto, role)
			}

			testAdminReplicaStatesOverProtocol(t, nodes, proto)

			// Two throwaway replica IDs this protocol's turn needs: one for
			// the observer-add/promote-success cycle, one for the
			// voter-add/promote-refusal cycle below.
			follower := adminClusterFollower(t, nodes, 30*time.Second)
			testAdminMemberLifecycleOverProtocol(t, follower, proto, nextReplicaID, nextReplicaID+1, spare)
		})
		nextReplicaID += 2
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

// adminReplicaStatesDisplay mirrors the `qrdb --output json admin
// replica-states` shape.
type adminReplicaStatesDisplay struct {
	NodeID   string `json:"node_id"`
	Replicas []struct {
		ShardID      uint64 `json:"shard_id"`
		ReplicaID    uint64 `json:"replica_id"`
		AppliedIndex uint64 `json:"applied_index"`
		Hash         uint64 `json:"hash"`
	} `json:"replicas"`
}

// testAdminReplicaStatesOverProtocol writes through a node that follows a
// known shard-1 leader, which forwards the write there, then asks each of
// the 3 control nodes for its shard-1 replica state until all three
// report one applied index, and requires them to report one hash there. The
// read is per node and never forwarded, so each node is asked directly; the
// three reads are not one snapshot, which is why an unequal index is retried
// rather than failed.
func testAdminReplicaStatesOverProtocol(t *testing.T, nodes [3]*multiProtoNode, proto multiProtocol) {
	t.Helper()

	key := "admincluster/replica-states/" + proto.name
	writer := adminClusterFollower(t, nodes, 30*time.Second)
	if out, err := runCLIProto(t, writer, proto, "kv", "put", key, "replica-state-value"); err != nil {
		t.Fatalf("[%s] kv put %s: %v\noutput: %s", proto.name, key, err, out)
	}

	var readings [3]adminReplicaStatesDisplay
	if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
		for i, node := range nodes {
			out, err := runCLIProto(t, node, proto, "--output", "json", "admin", "replica-states", "--shard-id", "1")
			if err != nil {
				t.Fatalf("[%s] node %d admin replica-states: %v\noutput: %s", proto.name, i, err, out)
			}
			var r adminReplicaStatesDisplay
			if jerr := json.Unmarshal([]byte(out), &r); jerr != nil {
				t.Fatalf("[%s] node %d decode `admin replica-states` JSON: %v\noutput: %s", proto.name, i, jerr, out)
			}
			if len(r.Replicas) != 1 || r.Replicas[0].ShardID != 1 {
				t.Fatalf("[%s] node %d admin replica-states --shard-id 1: want exactly one shard-1 replica, got %+v", proto.name, i, r)
			}
			readings[i] = r
		}
		index := readings[0].Replicas[0].AppliedIndex
		return readings[1].Replicas[0].AppliedIndex == index && readings[2].Replicas[0].AppliedIndex == index
	}) {
		t.Fatalf("[%s] the 3 replicas of shard 1 never reported one applied index: %+v", proto.name, readings)
	}

	// A shard holding the key just written has applied entries and a
	// non-empty state; zeroes would agree without proving anything.
	if r := readings[0].Replicas[0]; r.AppliedIndex == 0 || r.Hash == 0 {
		t.Fatalf("[%s] shard 1 reported applied index %d hash %d; want both non-zero", proto.name, r.AppliedIndex, r.Hash)
	}
	nodeIDs := map[string]bool{}
	replicaIDs := map[uint64]bool{}
	for i, r := range readings {
		nodeIDs[r.NodeID] = true
		replicaIDs[r.Replicas[0].ReplicaID] = true
		if r.Replicas[0].Hash != readings[0].Replicas[0].Hash {
			t.Fatalf("[%s] shard 1 at applied index %d: node %d hash %016x, node 0 hash %016x; replicas at one index must agree",
				proto.name, r.Replicas[0].AppliedIndex, i, r.Replicas[0].Hash, readings[0].Replicas[0].Hash)
		}
	}
	if len(nodeIDs) != 3 || len(replicaIDs) != 3 {
		t.Fatalf("[%s] want 3 distinct nodes and replicas answering, got %+v", proto.name, readings)
	}

	tableOut, err := runCLIProto(t, nodes[0], proto, "admin", "replica-states", "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s] admin replica-states (table): %v\noutput: %s", proto.name, err, tableOut)
	}
	for _, want := range []string{"SHARD", "REPLICA", "APPLIED INDEX", "HASH"} {
		if !strings.Contains(tableOut, want) {
			t.Fatalf("[%s] admin replica-states (table): missing %q in output: %s", proto.name, want, tableOut)
		}
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
// this point (see adminClusterSpareReplica's doc comment) -- then runs TWO
// add/remove cycles at spare's now-freed real Raft address against the
// node over proto -- in practice a follower of shard 1, so every change
// is one the node forwards to the leader on the caller's behalf -- each
// confirmed via `admin peers`: first
// observerReplicaID, added as an OBSERVER (`admin member add-observer`) and
// promoted to a voter (`admin member promote`) -- the success path, which
// had no way to be exercised by CLI before add-observer existed -- then
// newReplicaID, added as an ordinary voter and confirmed REFUSED promotion
// ("not a committed observer").
//
// Both cycles happen immediately after spare's removal, with no other Raft
// traffic against shard 1 in between: go-quicraft stops a removed replica
// participating in its old shard within seconds, including refreshing its
// own feature-gate advertisement's lease (see adminClusterSpareReplica), so
// spare's address is only a valid joiner for a brief window right after its
// removal -- every protocol gets its own spare precisely so this window
// never has to stretch across 5 protocols' worth of topology and snapshot
// calls.
//
// Neither observerReplicaID nor newReplicaID may ever have been used by an
// earlier add/remove cycle in this test run -- see
// TestAdminCluster_CLI_PerProtocol's monotonic counter, which exists
// because quicraft permanently refuses to re-admit a once-removed replica
// ID ("raft: node was previously removed") or member identity ("node
// identity belongs to a replica this shard has removed"). Reusing spare's
// ADDRESS (across both cycles, and across protocols) is fine: that
// restriction is keyed on replica ID and member identity, never on
// address.
func testAdminMemberLifecycleOverProtocol(t *testing.T, node *multiProtoNode, proto multiProtocol, observerReplicaID, newReplicaID uint64, spare adminClusterSpareReplica) {
	t.Helper()

	removeSpareOut, err := runCLIProto(t, node, proto, "admin", "member", "remove",
		fmt.Sprintf("%d", spare.replicaID), "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s] admin member remove (spare replica %d): %v\noutput: %s", proto.name, spare.replicaID, err, removeSpareOut)
	}
	if !strings.Contains(removeSpareOut, "OK") {
		t.Fatalf("[%s] admin member remove (spare): expected OK, got: %s", proto.name, removeSpareOut)
	}

	// admin member add-observer + admin member promote, the SUCCESS path:
	// an observer has no voting member of its own until this cycle exists,
	// so promote's success path had no way to be exercised by CLI before
	// it (only the "not a committed observer" refusal below, against a
	// replica a plain `member add` always votes). Reuses spare's
	// freshly-freed address, in the same brief post-removal window the
	// voter cycle below also depends on (see adminClusterSpareReplica).
	observerSum := sha256.Sum256([]byte(fmt.Sprintf("throwaway-observer-%d", observerReplicaID)))
	observerIdentity := "spki:" + hex.EncodeToString(observerSum[:])

	addObserverOut, err := runCLIProto(t, node, proto, "admin", "member", "add-observer", spare.raftAddr,
		"--shard-id", "1", "--replica-id", fmt.Sprintf("%d", observerReplicaID), "--identity", observerIdentity)
	if err != nil {
		t.Fatalf("[%s] admin member add-observer (replica %d): %v\noutput: %s", proto.name, observerReplicaID, err, addObserverOut)
	}
	if !strings.Contains(addObserverOut, "OK") {
		t.Fatalf("[%s] admin member add-observer: expected OK, got: %s", proto.name, addObserverOut)
	}

	promoteObserverOut, err := runCLIProto(t, node, proto, "admin", "member", "promote",
		fmt.Sprintf("%d", observerReplicaID), "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s] admin member promote (observer replica %d): %v\noutput: %s", proto.name, observerReplicaID, err, promoteObserverOut)
	}
	if !strings.Contains(promoteObserverOut, "OK") {
		t.Fatalf("[%s] admin member promote (observer): expected OK, got: %s", proto.name, promoteObserverOut)
	}

	// Promoted in place: now a voter, so it must show up as an ordinary
	// peer exactly as a plain `member add` replica does.
	if !waitForAdminPeersReplica(t, node, proto, observerReplicaID, true, 15*time.Second) {
		t.Fatalf("[%s] admin peers: promoted observer replica %d not present after admin member promote", proto.name, observerReplicaID)
	}

	removeObserverOut, err := runCLIProto(t, node, proto, "admin", "member", "remove",
		fmt.Sprintf("%d", observerReplicaID), "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s] admin member remove (promoted observer replica %d): %v\noutput: %s", proto.name, observerReplicaID, err, removeObserverOut)
	}
	if !strings.Contains(removeObserverOut, "OK") {
		t.Fatalf("[%s] admin member remove (promoted observer): expected OK, got: %s", proto.name, removeObserverOut)
	}

	// The replica being added is on no real node of its own -- it shares
	// spare's now-freed address -- so no committed membership names it yet:
	// its member identity is supplied, as an operator does for a brand-new
	// node. It is unique per replica ID because go-quicraft refuses one
	// identity bound to two replicas (current or once-removed).
	sum := sha256.Sum256([]byte(fmt.Sprintf("throwaway-replica-%d", newReplicaID)))
	identity := "spki:" + hex.EncodeToString(sum[:])

	out, err := runCLIProto(t, node, proto, "admin", "member", "add", spare.raftAddr,
		"--shard-id", "1", "--replica-id", fmt.Sprintf("%d", newReplicaID), "--identity", identity)
	if err != nil {
		t.Fatalf("[%s] admin member add (replica %d): %v\noutput: %s", proto.name, newReplicaID, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] admin member add: expected OK, got: %s", proto.name, out)
	}

	if !waitForAdminPeersReplica(t, node, proto, newReplicaID, true, 15*time.Second) {
		t.Fatalf("[%s] admin peers: replica %d not present after admin member add", proto.name, newReplicaID)
	}

	// admin member promote refuses a replica that is a committed VOTER, not
	// an observer: newReplicaID was just added by admin member add, which
	// always votes (there is no observer-mode add yet). This proves the
	// verb dispatches end-to-end on every protocol and that the typed
	// refusal (service.MemberNotObserverError) reaches the CLI.
	promoteOut, err := runCLIProto(t, node, proto, "admin", "member", "promote",
		fmt.Sprintf("%d", newReplicaID), "--shard-id", "1")
	if err == nil {
		t.Fatalf("[%s] admin member promote (voter replica %d) unexpectedly succeeded, output: %s", proto.name, newReplicaID, promoteOut)
	}
	if !strings.Contains(promoteOut, "not a committed observer") {
		t.Fatalf("[%s] admin member promote (voter replica %d): expected \"not a committed observer\", got: %v\noutput: %s", proto.name, newReplicaID, err, promoteOut)
	}

	// admin member remove takes the replica ID as a positional argument and
	// --shard-id as a flag.
	out, err = runCLIProto(t, node, proto, "admin", "member", "remove", fmt.Sprintf("%d", newReplicaID), "--shard-id", "1")
	if err != nil {
		t.Fatalf("[%s] admin member remove (replica %d): %v\noutput: %s", proto.name, newReplicaID, err, out)
	}
	if !strings.Contains(out, "OK") {
		t.Fatalf("[%s] admin member remove: expected OK, got: %s", proto.name, out)
	}

	if !waitForAdminPeersReplica(t, node, proto, newReplicaID, false, 15*time.Second) {
		t.Fatalf("[%s] admin peers: replica %d still present after admin member remove", proto.name, newReplicaID)
	}

	// admin member remove: an invalid (non-numeric) replica ID argument.
	out, err = runCLIProto(t, node, proto, "admin", "member", "remove", "not-a-number", "--shard-id", "1")
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

// waitForAdminPeersReplica reports whether `admin peers` against node over
// proto comes to list replicaID (present) or to omit it (!present) within
// timeout. A membership change is acknowledged once the leader commits it;
// a follower lists it once it has applied that entry too, which can be a
// moment later. This waits for that replication -- it never re-issues the
// change itself.
func waitForAdminPeersReplica(t *testing.T, node *multiProtoNode, proto multiProtocol, replicaID uint64, present bool, timeout time.Duration) bool {
	t.Helper()
	return pollUntil(timeout, 250*time.Millisecond, func() bool {
		return adminPeersContainsReplica(t, node, proto, replicaID) == present
	})
}

// adminClusterFollower returns a control node that, by its own /status,
// currently follows shard 1's leader, waiting up to timeout for one: right
// after a leader change a node can briefly know of no leader at all.
func adminClusterFollower(t *testing.T, nodes [3]*multiProtoNode, timeout time.Duration) *multiProtoNode {
	t.Helper()
	client := mtlsHTTPClient(t, nodes[0].ca, 5*time.Second)
	var follower *multiProtoNode
	if !pollUntil(timeout, 250*time.Millisecond, func() bool {
		for _, node := range nodes {
			status, _ := httpStatusRaw(t, client, node.restPort)
			if followsShard1Leader(status) {
				follower = node
				return true
			}
		}
		return false
	}) {
		t.Fatalf("no control node followed a known leader of shard 1 within %s", timeout)
	}
	return follower
}

// followsShard1Leader reports whether a decoded /status body shows its node
// as a follower of shard 1 that knows which replica leads it.
func followsShard1Leader(status map[string]interface{}) bool {
	shards, _ := status["shards"].([]interface{})
	for _, s := range shards {
		shard, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		if id, _ := shard["shard_id"].(float64); id != 1 {
			continue
		}
		isLeader, _ := shard["is_leader"].(bool)
		replicaID, _ := shard["replica_id"].(float64)
		leaderID, _ := shard["leader_id"].(float64)
		return !isLeader && leaderID != 0 && leaderID != replicaID
	}
	return false
}
