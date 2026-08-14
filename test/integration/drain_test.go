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

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// drainProtocol describes a single transport target for the admin drain test.
// scheme is the URL scheme the qrdb CLI uses to select the transport, and tls
// reports whether the transport requires mTLS client material. Every
// transport, including Unix (grpc-over-UDS, "verified TLS 1.3 only"), speaks
// TLS 1.3 and the CLI must present the admin client certificate (CN=admin,
// OU=system) so RBAC authorizes the drain platform-wide.
type drainProtocol struct {
	name   string
	scheme string
	tls    bool
}

// TestAdminDrain_PerProtocol verifies that `qrdb admin drain` succeeds across
// every supported transport.
//
// Draining is destructive: once a node enters drain mode it refuses new
// connections, which is why a single shared server cannot be drained more than
// once. Each subtest therefore spawns its OWN freshly-initialized single-node
// qrdbd with every transport enabled (REST, gRPC, MCP, QUIC, Unix) plus auth
// and TLS, drains it through that transport, and lets t.Cleanup tear it down.
// Because the nodes are fully isolated, every drain operates on a pristine node
// and succeeds, and the test drains no shared resource.
func TestAdminDrain_PerProtocol(t *testing.T) {
	protocols := []drainProtocol{
		{name: "rest", scheme: "https", tls: true},
		{name: "grpc", scheme: "grpcs", tls: true},
		{name: "mcp", scheme: "mcp", tls: true},
		{name: "quic", scheme: "quics", tls: true},
		{name: "unix", scheme: "unix", tls: true},
	}

	for _, proto := range protocols {
		t.Run(proto.name, func(t *testing.T) {
			node := startDrainNode(t)

			var serverURL string
			if proto.name == "unix" {
				serverURL = "unix://" + node.unixSocket
			} else {
				serverURL = fmt.Sprintf("%s://127.0.0.1:%d", proto.scheme, node.portFor(proto.name))
			}

			args := []string{"--server", serverURL, "--timeout", "15s"}
			if proto.tls {
				args = append(args,
					"--ca-file", node.ca.CertPath,
					"--cert-file", node.clientCert,
					"--key-file", node.clientKey,
				)
			}
			args = append(args, "admin", "drain")

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			cmd := exec.CommandContext(ctx, node.cli, args...)
			out, err := cmd.CombinedOutput()
			output := string(out)
			if err != nil {
				t.Fatalf("[%s] admin drain failed: %v\noutput: %s", proto.name, err, output)
			}
			if !strings.Contains(output, "OK") {
				t.Fatalf("[%s] admin drain should contain 'OK', got: %s", proto.name, output)
			}
			t.Logf("[%s] admin drain: %s", proto.name, strings.TrimSpace(output))
		})
	}
}

// drainNode holds the spawned daemon's connection details for a single
// drain subtest.
type drainNode struct {
	cli        string
	ca         *testCA
	clientCert string
	clientKey  string
	unixSocket string
	restPort   int
	grpcPort   int
	mcpPort    int
	quicPort   int
}

// portFor returns the TCP/UDP port serving the given protocol.
func (n *drainNode) portFor(proto string) int {
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

// startDrainNode spawns a fresh single-node qrdbd with every transport enabled
// (REST, gRPC, MCP, QUIC, Unix), authentication on, and TLS configured. It
// blocks until the node is healthy, its admin server is serving, and it has
// won leadership, then returns the connection details. The CLI client binary
// and an admin client certificate (CN=admin, OU=system) are prepared so callers
// can authorize the drain over any TLS transport.
func startDrainNode(t *testing.T) *drainNode {
	t.Helper()

	bin := buildBinary(t)
	cli := buildCLIBinary(t)
	ca := generateTestCA(t)
	dataDir := t.TempDir()

	// Unix socket paths are limited to ~108 bytes by the kernel and t.TempDir()
	// can exceed that, so place the socket under a short, unique /tmp path.
	sockDir, err := os.MkdirTemp("", "qrdb-drain-")
	if err != nil {
		t.Fatalf("startDrainNode: socket dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	unixSocket := filepath.Join(sockDir, "qrdbd.sock")

	clientCert, clientKey := ca.generateAdminClientCert(t, dataDir)

	raftPorts := allocatePorts(t, 1)
	quicPorts := allocatePorts(t, 1)
	tcpPorts := allocateTCPPorts(t, 3) // admin (REST), grpc, mcp

	restPort := tcpPorts[0]
	grpcPort := tcpPorts[1]
	mcpPort := tcpPorts[2]
	quicPort := quicPorts[0]

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[0]),
		"--data-dir", dataDir,
		"--sm", "mvcc",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", raftPorts[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", restPort),
		"--grpc-port", fmt.Sprintf("%d", grpcPort),
		"--auth-enabled",
		// Enable every non-default transport the drain test targets.
		"--enable-rest",
		"--enable-grpc",
		"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPort),
		"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPort),
		"--enable-unix", "--unix-socket-path", unixSocket,
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, bin, args, "[drain] ")
	waitForHealthy(t, dataDir, 60*time.Second)
	waitForAdmin(t, ca.Pool, restPort, 90*time.Second)
	waitForLeaderViaAdmin(t, ca, []int{restPort}, 60*time.Second)

	return &drainNode{
		cli:        cli,
		ca:         ca,
		clientCert: clientCert,
		clientKey:  clientKey,
		unixSocket: unixSocket,
		restPort:   restPort,
		grpcPort:   grpcPort,
		mcpPort:    mcpPort,
		quicPort:   quicPort,
	}
}
