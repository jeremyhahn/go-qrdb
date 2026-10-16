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

// cluster_seal_cli_test.go drives `barrier cluster-unseal`'s SUCCESS path
// through the real CLI binary, on every transport. cluster_seal_test.go's
// TestClusterSeal_NodeBarrier_RF3 already proves the full lifecycle --
// node-seal -> every node restarts and comes back held under its OWN
// passphrase -> one cluster-unseal clears the record for all three -- but
// entirely over raw HTTP; only the REFUSAL path (an inactive cluster seal)
// has ever gone through the CLI (cmd/qrdb/cluster_seal_test.go, against a
// mock transport). This file is the integration-level CLI success path, on
// every one of the 5 CLI-selectable transports.
package integration_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// multiProtocolSealNode is one daemon of a multiProtocolSealHarness: every
// transport enabled, its own barrier passphrase, and a *multiProtoNode
// handle so every existing runCLIProto-based helper works unmodified
// against it.
type multiProtocolSealNode struct {
	*multiProtoNode
	cmd  *exec.Cmd
	args []string
}

// multiProtocolSealHarness is a 3-node cluster-seal harness like
// cluster_seal_test.go's sealClusterHarness, except every node serves all 5
// CLI-selectable transports (REST, gRPC, QUIC, MCP, Unix), so
// `barrier cluster-unseal`'s success path can be driven through the real CLI
// binary on each one in turn.
type multiProtocolSealHarness struct {
	t     *testing.T
	nodes []*multiProtocolSealNode
}

// startMultiProtocolSealCluster boots a 3-node cluster-seal harness with
// every transport enabled on every node, each node initialized and unsealed
// with its OWN passphrase (node-%d-own-passphrase, i in [1,3], matching
// cluster_seal_test.go's startSealCluster), and a CLI binary + shared admin
// mTLS identity. lease/poll/ack configure the cluster-seal timing exactly as
// writeClusterSealConfig (cluster_seal_test.go) does.
func startMultiProtocolSealCluster(t *testing.T, lease, poll, ack string) *multiProtocolSealHarness {
	t.Helper()

	const n = 3
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)

	raftPorts := allocatePorts(t, n)
	quicPorts := allocatePorts(t, n)
	tcpPorts := allocateTCPPorts(t, n*3) // per node: admin (REST), grpc, mcp
	peersFlag := buildPeersFlag(raftPorts)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, n)

	h := &multiProtocolSealHarness{t: t}

	for i := range n {
		dataDir := t.TempDir()

		// Unix socket paths are limited to ~108 bytes by the kernel and
		// t.TempDir() can exceed that -- see startMultiProtocolNodeWithSM's
		// identical rationale.
		sockDir, err := os.MkdirTemp("", fmt.Sprintf("qrdb-cseal-%d-", i+1))
		if err != nil {
			t.Fatalf("startMultiProtocolSealCluster: socket dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(sockDir) })
		unixSocket := filepath.Join(sockDir, "qrdbd.sock")

		restPort, grpcPort, mcpPort := tcpPorts[i*3], tcpPorts[i*3+1], tcpPorts[i*3+2]
		quicPort := quicPorts[i]

		args := []string{
			"start",
			"--config", writeClusterSealConfig(t, lease, poll, ack),
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", dataDir,
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", restPort),
			"--grpc-port", fmt.Sprintf("%d", grpcPort),
			"--auth-enabled",
			"--enable-rest",
			"--enable-grpc",
			"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
			"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
			"--enable-unix", "--unix-socket-path", unixSocket,
		}
		args = append(args, nodeTLS[i]...)

		n := &multiProtocolSealNode{
			multiProtoNode: &multiProtoNode{
				cli:        cli,
				ca:         ca,
				dataDir:    dataDir,
				unixSocket: unixSocket,
				restPort:   restPort,
				grpcPort:   grpcPort,
				mcpPort:    mcpPort,
				quicPort:   quicPort,
				passphrase: fmt.Sprintf("node-%d-own-passphrase", i+1),
			},
			args: args,
		}
		h.nodes = append(h.nodes, n)
		h.start(i)
	}

	// Sealed-window: initialize and unseal the passphrase barrier on all n
	// nodes in parallel, exactly as start3NodeMultiProtocolCluster does. The
	// admin certificate the ceremony binds is the ONE every CLI call below
	// presents: under mtls_binding_policy "required" (the default) a
	// certificate no ceremony bound is refused, however valid its chain.
	var clientCert, clientKey string
	var sealWg sync.WaitGroup
	sealWg.Add(n)
	for i := range n {
		go func(idx int) {
			defer sealWg.Done()
			waitForBarrierStatusReachable(t, ca, h.nodes[idx].restPort, 90*time.Second)
			cp, kp := initAndUnsealBarrier(t, ca, h.nodes[idx].restPort, h.nodes[idx].passphrase)
			if idx == 0 {
				clientCert, clientKey = cp, kp
			}
		}(i)
	}
	sealWg.Wait()

	for _, node := range h.nodes {
		waitForHealthy(t, node.dataDir, 60*time.Second)
		waitForAdmin(t, ca.Pool, node.restPort, 90*time.Second)
	}
	warmupWrite(t, ca, waitForLeaderViaAdmin(t, ca, h.ports(), 60*time.Second), 15*time.Second)

	for _, node := range h.nodes {
		node.clientCert = clientCert
		node.clientKey = clientKey
	}

	// Confirm every transport on every node is actually serving before
	// handing the cluster to callers.
	for _, node := range h.nodes {
		for _, proto := range multiProtocols {
			waitForCLIProtoReady(t, node.multiProtoNode, proto, 60*time.Second)
		}
	}

	return h
}

// ports returns every node's admin (REST) port.
func (h *multiProtocolSealHarness) ports() []int {
	ports := make([]int, len(h.nodes))
	for i, n := range h.nodes {
		ports[i] = n.restPort
	}
	return ports
}

// start (re)starts node i.
func (h *multiProtocolSealHarness) start(i int) {
	h.nodes[i].cmd = startNode(h.t, buildBinary(h.t), h.nodes[i].args, fmt.Sprintf("[cseal-cli-node%d] ", i+1))
}

// stop sends SIGTERM to node i and waits (with a hard-kill fallback) for it
// to exit, so a restart can bind the same ports immediately afterward.
func (h *multiProtocolSealHarness) stop(i int) {
	cmd := h.nodes[i].cmd
	if cmd == nil || cmd.Process == nil {
		return
	}
	stopNode(h.t, cmd, 15*time.Second)
}

// cliBarrierStatusFull is the full `barrier status --output json` response this
// file asserts on: sdk/go/transport.BarrierStatusResponse's wire shape.
type cliBarrierStatusFull struct {
	Sealed      bool             `json:"sealed"`
	ClusterSeal *clusterSealView `json:"cluster_seal,omitempty"`
}

// cliBarrierStatusFor runs `barrier status --output json` against node over
// proto and decodes it. It never fails the test itself: a connection failure
// or a non-JSON error body is expected for the brief window right after a
// restart, before the listener comes back up. A node whose barrier is sealed
// answers Sealed with its initialized flag and nothing else; a node held or
// cut off refuses status, and its typed sealed refusal reads as Sealed with
// no cluster_seal. On any other
// failure ok is false and
// failure says what the CLI answered, so waitForCLIClusterSeal -- the only
// caller, and the one that owns the timeout -- can report a read that never
// succeeded as that, not as a zero status.
func cliBarrierStatusFor(t *testing.T, node *multiProtocolSealNode, proto multiProtocol) (st cliBarrierStatusFull, failure string, ok bool) {
	t.Helper()
	out, err := runCLIProto(t, node.multiProtoNode, proto, "--output", "json", "barrier", "status")
	if err != nil && cliSealedRefusal(out) {
		return cliBarrierStatusFull{Sealed: true}, "", true
	}
	if err != nil {
		return cliBarrierStatusFull{}, err.Error() + ": " + strings.TrimSpace(out), false
	}
	if jerr := json.Unmarshal([]byte(out), &st); jerr != nil {
		return cliBarrierStatusFull{}, "undecodable output (" + jerr.Error() + "): " + strings.TrimSpace(out), false
	}
	return st, "", true
}

// waitForCLIClusterSeal polls node (over proto) until cond(barrier status)
// holds or timeout elapses, tolerating transient CLI/connection errors
// (e.g. a node mid-restart) throughout the window rather than failing on
// the first one.
func waitForCLIClusterSeal(t *testing.T, node *multiProtocolSealNode, proto multiProtocol, timeout time.Duration, what string, cond func(cliBarrierStatusFull) bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var (
		last        cliBarrierStatusFull
		read        bool
		lastFailure string
	)
	for time.Now().Before(deadline) {
		st, failure, ok := cliBarrierStatusFor(t, node, proto)
		if ok {
			last, read = st, true
			if cond(last) {
				return
			}
		} else {
			lastFailure = failure
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !read {
		t.Fatalf("node %s: timed out after %v waiting for %s: barrier status over %s never answered (last failure: %s)",
			node.dataDir, timeout, what, proto.name, lastFailure)
	}
	t.Fatalf("node %s: timed out after %v waiting for %s (last status over %s: sealed=%t cluster_seal=%+v; last failure: %q)",
		node.dataDir, timeout, what, proto.name, last.Sealed, last.ClusterSeal, lastFailure)
}

// TestBarrierClusterUnseal_CLI_PerProtocol drives the full cluster-seal
// success lifecycle -- seal -> every node restarts and comes back held
// under its OWN passphrase -> `barrier cluster-unseal` clears the record for
// all three -> every node resumes serving -- once per transport, each
// cycle on its OWN freshly booted 3-node cluster (so the cycles are
// perfectly isolated from each other: restarting node 2 of the "quic" cycle's
// cluster can never race node 2 of the "mcp" cycle's), which is what lets
// them run in parallel subtests.
//
// Only the LAST step -- `barrier cluster-unseal` itself, the one command
// this file exists to prove -- is dispatched over the protocol under test;
// every setup step (seal, restart, each node's own unseal) runs over REST,
// identically across all 5 cycles, so a cycle's own protocol is exercised
// for exactly the one verb that was previously untested at the integration
// level through the CLI (see the package doc comment).
func TestBarrierClusterUnseal_CLI_PerProtocol(t *testing.T) {
	for _, proto := range multiProtocols {
		proto := proto
		t.Run(proto.name, func(t *testing.T) {
			t.Parallel()

			h := startMultiProtocolSealCluster(t, "15s", "1s", "5s")
			restProto := multiProtocols[0] // rest

			leaderPort := waitForLeaderViaAdmin(t, h.nodes[0].ca, h.ports(), 60*time.Second)
			var leaderIdx int
			for i, node := range h.nodes {
				if node.restPort == leaderPort {
					leaderIdx = i
				}
			}
			httpPut(t, h.nodes[0].ca, leaderPort, "sealed-key", []byte("survives"))

			for _, node := range h.nodes {
				waitForCLIClusterSeal(t, node, restProto, 60*time.Second, "cluster-seal active", func(st cliBarrierStatusFull) bool {
					return st.ClusterSeal != nil && st.ClusterSeal.Active && !st.ClusterSeal.Sealed && st.ClusterSeal.Local == "serving"
				})
			}

			// Seal through a follower, over REST: any node may receive it,
			// and this is setup, not the behavior under test.
			followerIdx := (leaderIdx + 1) % len(h.nodes)
			sealOut, err := runCLIProto(t, h.nodes[followerIdx].multiProtoNode, restProto, "barrier", "seal", "--assume-yes", "--so-pin", harnessSOPIN)
			if err != nil {
				t.Fatalf("barrier seal (node %d): %v\noutput: %s", followerIdx+1, err, sealOut)
			}

			// Every node seals itself and refuses data requests.
			for i, node := range h.nodes {
				waitForCLIClusterSeal(t, node, restProto, 30*time.Second, "node barrier sealed", func(st cliBarrierStatusFull) bool {
					return st.Sealed
				})
				if st := httpGetStatus(t, h.nodes[0].ca, node.restPort, "sealed-key"); st == http.StatusOK {
					t.Fatalf("node %d served a read after the cluster seal", i+1)
				}
			}

			// Restart every node; each unseals with its OWN passphrase over
			// REST (setup) and comes back held: keys loaded, requests
			// refused, until cluster-unseal clears the record.
			for i := range h.nodes {
				h.stop(i)
			}
			for i := range h.nodes {
				h.start(i)
			}
			var wg sync.WaitGroup
			for _, node := range h.nodes {
				wg.Add(1)
				go func(node *multiProtocolSealNode) {
					defer wg.Done()
					waitForBarrierStatusReachable(t, h.nodes[0].ca, node.restPort, 90*time.Second)
					out, err := runCLIProto(t, node.multiProtoNode, restProto, "barrier", "unseal", "--passphrase", node.passphrase)
					if err != nil {
						t.Errorf("barrier unseal (node %s): %v\noutput: %s", node.dataDir, err, out)
					}
				}(node)
			}
			wg.Wait()
			// Each node's own unseal succeeded, so the typed sealed refusal
			// of status is the cluster record holding it.
			for i, node := range h.nodes {
				waitForCLIClusterSeal(t, node, restProto, 90*time.Second, "held after restart", func(st cliBarrierStatusFull) bool {
					return st.Sealed
				})
				if st := httpGetStatus(t, h.nodes[0].ca, node.restPort, "sealed-key"); st == http.StatusOK {
					t.Fatalf("node %d served a read while held", i+1)
				}
			}

			// The command under test: `barrier cluster-unseal`, over proto.
			// One clear, from any node, clears the record; a held node
			// answers no status, so there is no leader to look up first. A
			// clear whose record the system shard could not confirm -- its
			// write met the restart's leader election -- changes nothing and
			// is the documented retryable refusal, so it is retried.
			current := h.nodes[0]
			var unsealOut string
			for deadline := time.Now().Add(60 * time.Second); ; {
				var err error
				unsealOut, err = runCLIProto(t, current.multiProtoNode, proto, "--output", "json", "barrier", "cluster-unseal")
				if err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("[%s] barrier cluster-unseal: %v\noutput: %s", proto.name, err, unsealOut)
				}
				time.Sleep(time.Second)
			}
			var cleared clusterSealView
			if jerr := json.Unmarshal([]byte(unsealOut), &cleared); jerr != nil || cleared.Sealed {
				t.Fatalf("[%s] barrier cluster-unseal answered %s (%v), want a clear record", proto.name, unsealOut, jerr)
			}

			// Table-format sanity.
			tableOut, err := runCLIProto(t, current.multiProtoNode, proto, "barrier", "cluster-unseal")
			if err != nil {
				t.Fatalf("[%s] barrier cluster-unseal (table, second call): %v\noutput: %s", proto.name, err, tableOut)
			}

			for _, node := range h.nodes {
				waitForCLIClusterSeal(t, node, restProto, 30*time.Second, "serving after cluster-unseal", func(st cliBarrierStatusFull) bool {
					return !st.Sealed && st.ClusterSeal != nil && !st.ClusterSeal.Sealed && st.ClusterSeal.Local == "serving"
				})
			}
			leaderPort = waitForLeaderViaAdmin(t, h.nodes[0].ca, h.ports(), 60*time.Second)
			if got := string(httpGet(t, h.nodes[0].ca, leaderPort, "sealed-key")); got != "survives" {
				t.Fatalf("[%s] after cluster-unseal sealed-key = %q, want survives", proto.name, got)
			}
			if !strings.Contains(tableOut, "clear") || !strings.Contains(tableOut, "serving") {
				t.Fatalf("[%s] barrier cluster-unseal (table): expected \"clear\"/\"serving\", got: %s", proto.name, tableOut)
			}
		})
	}
}
