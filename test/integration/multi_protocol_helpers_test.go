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

// multi_protocol_helpers_test.go generalizes drain_test.go's single-purpose
// drainNode/drainProtocol fixture into a reusable harness every CLI-binary,
// multi-protocol, table-driven test in this package can share: one
// multiProtoNode serves REST, gRPC, QUIC, MCP, and Unix simultaneously with
// auth enabled, TLS configured, and a passphrase-strategy system barrier
// already initialized and unsealed, so KV/database data-plane commands work
// immediately over any of the five transports. See
// test/integration/CLAUDE.md and drain_test.go's TestAdminDrain_PerProtocol
// for the convention this harness implements.
package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// multiProtocol describes a single CLI-selectable transport target. scheme
// is the URL scheme the qrdb CLI's --server flag expects to select this
// transport (see cmd/qrdb/root.go's schemePrefixes).
type multiProtocol struct {
	name   string
	scheme string
}

// multiProtocols is the canonical set of CLI-selectable transports every
// multi-protocol table-driven test in this package loops over. It mirrors
// drainProtocol's set in drain_test.go, the reference model for this
// convention (test/integration/CLAUDE.md).
var multiProtocols = []multiProtocol{
	{name: "rest", scheme: "https"},
	{name: "grpc", scheme: "grpcs"},
	{name: "quic", scheme: "quics"},
	{name: "mcp", scheme: "mcp"},
	{name: "unix", scheme: "unix"},
}

// multiProtoNode holds the connection details for a single-node qrdbd
// serving every transport with an already-unsealed, passphrase-keyed system
// barrier. It is the shared fixture every kv/database multi-protocol CLI
// test in this batch boots once (via startMultiProtocolNode) and reuses
// across its per-protocol subtests.
type multiProtoNode struct {
	cli        string
	ca         *testCA
	dataDir    string
	clientCert string
	clientKey  string
	unixSocket string
	restPort   int
	grpcPort   int
	mcpPort    int
	quicPort   int
	passphrase string
}

// portFor returns the TCP/UDP port serving the given named protocol ("rest",
// "grpc", "mcp", "quic"). "unix" has no port -- see serverURLFor.
func (n *multiProtoNode) portFor(proto string) int {
	switch proto {
	case "rest":
		return n.restPort
	case "grpc":
		return n.grpcPort
	case "mcp":
		return n.mcpPort
	case "quic":
		return n.quicPort
	default:
		return 0
	}
}

// serverURLFor returns the --server value that selects the given protocol
// against this node.
func (n *multiProtoNode) serverURLFor(proto multiProtocol) string {
	if proto.name == "unix" {
		return "unix://" + n.unixSocket
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", proto.scheme, n.portFor(proto.name))
}

// cliArgsFor returns the root-level qrdb CLI flags that select the given
// protocol and present the admin mTLS identity (CN=admin, OU=system) every
// transport in this harness authorizes as Owner platform-wide.
func (n *multiProtoNode) cliArgsFor(proto multiProtocol) []string {
	return []string{
		"--server", n.serverURLFor(proto),
		"--ca-file", n.ca.CertPath,
		"--cert-file", n.clientCert,
		"--key-file", n.clientKey,
		"--timeout", "20s",
	}
}

// runCLIProto runs the qrdb client CLI against the node over the given
// protocol, prepending cliArgsFor(proto) to args. It never mutates or
// aliases the node's backing connection-arg slices across calls.
func runCLIProto(t *testing.T, node *multiProtoNode, proto multiProtocol, args ...string) (string, error) {
	t.Helper()
	full := append(append([]string{}, node.cliArgsFor(proto)...), args...)
	return runCLI(t, node.cli, full...)
}

// writeMultiProtocolSealConfig writes a daemon YAML config enabling the seal
// barrier with the passphrase strategy. A node booted with this config comes
// up in the pre-host sealed window and must be initialized and unsealed
// (see initAndUnsealBarrier) before the Raft host, and therefore every other
// transport, starts serving.
func writeMultiProtocolSealConfig(t *testing.T) string {
	t.Helper()
	yaml := "seal:\n  enabled: true\n  strategy: passphrase\n"
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "qrdbd.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeMultiProtocolSealConfig: %v", err)
	}
	return cfgPath
}

// startMultiProtocolNode boots a single-node qrdbd with every transport
// enabled (REST, gRPC, QUIC, MCP, Unix), authentication on, TLS configured,
// and a passphrase-strategy system barrier initialized and unsealed, so
// data-plane commands (kv, database) work immediately over any of the 5
// transports. It blocks until the node is healthy, has won leadership over
// the REST admin port, and every transport is confirmed serving via a live
// CLI round trip, then returns a handle with everything a per-protocol
// subtest needs.
//
// It runs raft.sm_type: mvcc (see startMultiProtocolNodeWithSM), which is
// what every MVCC-dependent command (kv get --at-revision, kv restore, ...)
// requires.
func startMultiProtocolNode(t *testing.T) *multiProtoNode {
	t.Helper()
	return startMultiProtocolNodeWithSM(t, "mvcc")
}

// startMultiProtocolNodeWithSM is startMultiProtocolNode parameterized on the
// daemon's state machine type. smType is passed as the "--sm" flag value; an
// empty smType omits the flag entirely so the daemon boots under its own
// default (raft.sm_type: pebble -- see cmd/qrdbd/start.go), which is what the
// default-state-machine MVCC-refusal coverage
// (kv_restore_default_sm_cli_test.go) needs: those tests must run against
// the actual out-of-the-box default, not merely an explicit "--sm pebble"
// that could silently drift from it.
func startMultiProtocolNodeWithSM(t *testing.T, smType string) *multiProtoNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()
	cfgPath := writeMultiProtocolSealConfig(t)

	// Unix socket paths are limited to ~108 bytes by the kernel and
	// t.TempDir() can exceed that, so place the socket under a short,
	// unique /tmp path (matches drainNode's identical rationale).
	sockDir, err := os.MkdirTemp("", "qrdb-mp-")
	if err != nil {
		t.Fatalf("startMultiProtocolNodeWithSM: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	restPort := tcpPorts[0]
	grpcPort := tcpPorts[1]
	mcpPort := tcpPorts[2]
	quicPort := quicPorts[0]

	const passphrase = "multi-protocol-harness-passphrase"

	args := []string{
		"start",
		"--config", cfgPath,
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled",
		// Enable every transport the multi-protocol tests target.
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}
	if smType != "" {
		args = append(args, "--sm", smType)
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, fmt.Sprintf("[multi-proto sm=%s] ", smType))

	// The barrier subsystem runs a pre-host sealed-window REST listener
	// (unauthenticated by design -- the secret itself is the
	// authorization) that must be initialized and unsealed before the Raft
	// host, and therefore every other transport, comes up.
	waitForBarrierStatusReachable(t, ca, restPort, 90*time.Second)
	initAndUnsealBarrier(t, ca, restPort, passphrase)

	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{restPort}, 60*time.Second)

	clientCert, clientKey := ca.generateAdminClientCert(t, dataDir)

	n := &multiProtoNode{
		cli:        cli,
		ca:         ca,
		dataDir:    dataDir,
		clientCert: clientCert,
		clientKey:  clientKey,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
		passphrase: passphrase,
	}

	// Confirm every transport is actually serving (not just that its
	// listener socket is bound) by round-tripping a real read-only CLI
	// command over each one before handing the node to callers.
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, n, proto, 60*time.Second)
	}

	return n
}

// waitForCLIProtoReady polls the given protocol with a lightweight
// read-only CLI command ("kv exists", an ordinary KVServicer.Get -- see
// cmd/qrdb/kv.go's newKVExistsCmd) until it succeeds or the timeout elapses.
//
// This deliberately does NOT use "kv revision": CurrentRevision is an
// MVCC-only query (MVCCRevisionQuery) that a raft.sm_type: pebble shard
// refuses with *MVCCBackendRequiredError, so it would never succeed against
// a startMultiProtocolNodeWithSM(t, "pebble") node and this readiness probe
// would spin until timeout on every non-MVCC harness. "kv exists" round-trips
// the transport and the service/engine layer identically on every state
// machine.
func waitForCLIProtoReady(t *testing.T, node *multiProtoNode, proto multiProtocol, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastOut string
	var lastErr error
	for time.Now().Before(deadline) {
		out, err := runCLIProto(t, node, proto, "kv", "exists", "__multi_proto_ready_probe__")
		if err == nil {
			return
		}
		lastOut, lastErr = out, err
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("[%s] timed out after %v waiting for transport to serve requests: %v\noutput: %s",
		proto.name, timeout, lastErr, lastOut)
}
