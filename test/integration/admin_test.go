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
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Table-Driven: SM Type x Discovery x Node Count
// ---------------------------------------------------------------------------

type cliTestCase struct {
	name      string
	smType    string
	discovery string
	nodes     int
}

var startTestCases = []cliTestCase{
	{name: "memory/static/1-node", smType: "memory", discovery: "static", nodes: 1},
	{name: "memory/static/3-node", smType: "memory", discovery: "static", nodes: 3},
	{name: "concurrent/static/1-node", smType: "concurrent", discovery: "static", nodes: 1},
	{name: "concurrent/static/3-node", smType: "concurrent", discovery: "static", nodes: 3},
}

func TestCLI_Start_TableDriven(t *testing.T) {
	binary := buildBinary(t)

	for _, tc := range startTestCases {
		t.Run(tc.name, func(t *testing.T) {
			ca := generateTestCA(t)
			ports := allocatePorts(t, tc.nodes)
			adminPorts := allocateTCPPorts(t, tc.nodes)
			grpcPorts := allocateTCPPorts(t, tc.nodes)
			peersFlag := buildPeersFlag(ports)
			dataDirs := make([]string, tc.nodes)

			for i := range tc.nodes {
				dataDir := t.TempDir()
				dataDirs[i] = dataDir

				args := []string{
					"start",
					"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
					"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
					"--data-dir", dataDir,
					"--sm", tc.smType,
					"--discovery", tc.discovery,
					"--peers", peersFlag,
					"--shard-id", "1",
					"--replica-id", fmt.Sprintf("%d", i+1),
					"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
					"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
				}
				args = append(args, ca.tlsFlags(t)...)

				prefix := fmt.Sprintf("[node%d] ", i+1)
				startNode(t, binary, args, prefix)
			}

			// Wait for all nodes to become healthy.
			for i, dir := range dataDirs {
				waitForHealthy(t, dir, 30*time.Second)
				t.Logf("node %d healthy", i+1)
			}
			t.Log("cluster formed successfully")
		})
	}
}

// ---------------------------------------------------------------------------
// Graceful Shutdown
// ---------------------------------------------------------------------------

func TestCLI_Start_GracefulShutdown(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--sm", "memory",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
	}
	args = append(args, ca.tlsFlags(t)...)

	cmd := exec.Command(binary, args...)
	cmd.Stdout = &logWriter{t: t, prefix: "[node1] "}
	cmd.Stderr = &logWriter{t: t, prefix: "[node1] "}
	if err := cmd.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	waitForHealthy(t, dataDir, 30*time.Second)

	// Send SIGTERM for graceful shutdown.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal failed: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("process exited with error: %v", err)
		}
		t.Log("node shut down gracefully with exit code 0")
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("timed out waiting for graceful shutdown")
	}
}

// ---------------------------------------------------------------------------
// Cluster Formation (host.id files)
// ---------------------------------------------------------------------------

func TestCLI_Start_ClusterFormation(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	dataDirs := make([]string, 3)

	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", "memory",
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
		}
		args = append(args, ca.tlsFlags(t)...)

		prefix := fmt.Sprintf("[node%d] ", i+1)
		startNode(t, binary, args, prefix)
	}

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 30*time.Second)

		// Verify host.id file exists.
		hostIDPath := fmt.Sprintf("%s/nodehost/host.id", dir)
		info, err := os.Stat(hostIDPath)
		if err != nil {
			t.Fatalf("node %d: host.id not found: %v", i+1, err)
		}
		if info.Size() == 0 {
			t.Errorf("node %d: host.id is empty", i+1)
		}
		t.Logf("node %d: host.id created (%d bytes)", i+1, info.Size())
	}
}

// ---------------------------------------------------------------------------
// Error Path Tests
// ---------------------------------------------------------------------------

func TestCLI_Start_InvalidSM(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "start",
		"--sm", "bogus",
		"--peers", "1=127.0.0.1:9001",
		"--data-dir", t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected non-zero exit for invalid SM type")
	}
	if !strings.Contains(out, "bogus") {
		t.Errorf("expected error mentioning 'bogus', got: %s", out)
	}
}

func TestCLI_Start_InvalidDiscovery(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "start",
		"--discovery", "bogus",
		"--peers", "1=127.0.0.1:9001",
		"--data-dir", t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected non-zero exit for invalid discovery method")
	}
	if !strings.Contains(out, "bogus") {
		t.Errorf("expected error mentioning 'bogus', got: %s", out)
	}
}

func TestCLI_Start_NoPeers(t *testing.T) {
	binary := buildBinary(t)
	_, err := runCLI(t, binary, "start",
		"--discovery", "static",
		"--data-dir", t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected non-zero exit when no peers specified")
	}
}

func TestCLI_Start_InvalidPeerFormat(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "start",
		"--discovery", "static",
		"--peers", "badformat",
		"--data-dir", t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected non-zero exit for invalid peer format")
	}
	if !strings.Contains(out, "badformat") {
		t.Errorf("expected error mentioning 'badformat', got: %s", out)
	}
}

func TestCLI_Start_MissingMulticastSecret(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "start",
		"--discovery", "multicast",
		"--multicast-secret", "",
		"--data-dir", t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected non-zero exit when multicast-secret is missing")
	}
	if !strings.Contains(strings.ToLower(out), "multicast-secret") {
		t.Errorf("expected error mentioning 'multicast-secret', got: %s", out)
	}
}

func TestCLI_Start_MissingDNSDomain(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "start",
		"--discovery", "dns",
		"--data-dir", t.TempDir(),
	)
	if err == nil {
		t.Fatal("expected non-zero exit when dns-domain is missing")
	}
	if !strings.Contains(strings.ToLower(out), "dns-domain") {
		t.Errorf("expected error mentioning 'dns-domain', got: %s", out)
	}
}

func TestCLI_Start_AllFlags(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	// Test that all flags are accepted without error.
	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--deployment-id", "42",
		"--sm", "memory",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "100",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
	}
	args = append(args, ca.tlsFlags(t)...)

	cmd := startNode(t, binary, args, "[allflags] ")
	waitForHealthy(t, dataDir, 30*time.Second)

	// Verify the node started correctly with all flags.
	t.Log("all flags accepted, node started successfully")

	cmd.Process.Signal(syscall.SIGTERM)
}

// ---------------------------------------------------------------------------
// Subcommand Tests
// ---------------------------------------------------------------------------

func TestCLI_Version(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "version")
	if err != nil {
		t.Fatalf("version command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "QuicRaft DB v") {
		t.Errorf("expected version output containing 'QuicRaft DB v', got: %s", out)
	}
	if !strings.Contains(out, "commit:") {
		t.Errorf("expected version output containing 'commit:', got: %s", out)
	}
	if !strings.Contains(out, "built:") {
		t.Errorf("expected version output containing 'built:', got: %s", out)
	}
}

func TestCLI_Status(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--sm", "memory",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, binary, args, "[node1] ")
	waitForHealthy(t, dataDir, 30*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 10*time.Second)

	out, err := runCLI(t, binary, "status",
		"--address", fmt.Sprintf("127.0.0.1:%d", adminPorts[0]),
		"--tls-ca-file", ca.CertPath)
	if err != nil {
		t.Fatalf("status command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Node ID:") {
		t.Errorf("status output should contain 'Node ID:', got: %s", out)
	}
	if !strings.Contains(out, "Shard 1:") {
		t.Errorf("status output should contain 'Shard 1:', got: %s", out)
	}
	t.Logf("status output:\n%s", out)
}

func TestCLI_Peers(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 1)
	adminPorts := allocateTCPPorts(t, 1)
	grpcPorts := allocateTCPPorts(t, 1)
	dataDir := t.TempDir()

	args := []string{
		"start",
		"--listen", fmt.Sprintf("0.0.0.0:%d", ports[0]),
		"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[0]),
		"--data-dir", dataDir,
		"--sm", "memory",
		"--discovery", "static",
		"--peers", fmt.Sprintf("1=127.0.0.1:%d", ports[0]),
		"--shard-id", "1",
		"--replica-id", "1",
		"--admin-port", fmt.Sprintf("%d", adminPorts[0]),
		"--grpc-port", fmt.Sprintf("%d", grpcPorts[0]),
	}
	args = append(args, ca.tlsFlags(t)...)

	startNode(t, binary, args, "[node1] ")
	waitForHealthy(t, dataDir, 30*time.Second)
	waitForAdmin(t, ca.Pool, adminPorts[0], 10*time.Second)

	out, err := runCLI(t, binary, "peers",
		"--address", fmt.Sprintf("127.0.0.1:%d", adminPorts[0]),
		"--tls-ca-file", ca.CertPath)
	if err != nil {
		t.Fatalf("peers command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "replica") {
		t.Errorf("peers output should contain 'replica', got: %s", out)
	}
	if !strings.Contains(out, "Shard 1") {
		t.Errorf("peers output should contain 'Shard 1', got: %s", out)
	}
	t.Logf("peers output:\n%s", out)
}

func TestCLI_Status_ThreeNode(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	dataDirs := make([]string, 3)

	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", "memory",
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
		}
		args = append(args, ca.tlsFlags(t)...)

		prefix := fmt.Sprintf("[node%d] ", i+1)
		startNode(t, binary, args, prefix)
	}

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 30*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 10*time.Second)
	}

	// Query status from node 1.
	out, err := runCLI(t, binary, "status",
		"--address", fmt.Sprintf("127.0.0.1:%d", adminPorts[0]),
		"--tls-ca-file", ca.CertPath)
	if err != nil {
		t.Fatalf("status command failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Shard 1:") {
		t.Errorf("status output should contain 'Shard 1:', got: %s", out)
	}
	t.Logf("3-node status:\n%s", out)
}

func TestCLI_Peers_ThreeNode(t *testing.T) {
	binary := buildBinary(t)
	ca := generateTestCA(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	dataDirs := make([]string, 3)

	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir

		args := []string{
			"start",
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", "memory",
			"--discovery", "static",
			"--peers", peersFlag,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
		}
		args = append(args, ca.tlsFlags(t)...)

		prefix := fmt.Sprintf("[node%d] ", i+1)
		startNode(t, binary, args, prefix)
	}

	for i, dir := range dataDirs {
		waitForHealthy(t, dir, 30*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 10*time.Second)
	}

	// Query peers from node 1.
	out, err := runCLI(t, binary, "peers",
		"--address", fmt.Sprintf("127.0.0.1:%d", adminPorts[0]),
		"--tls-ca-file", ca.CertPath)
	if err != nil {
		t.Fatalf("peers command failed: %v\n%s", err, out)
	}
	// Should list all 3 replicas.
	for i := range 3 {
		expected := fmt.Sprintf("replica %d", i+1)
		if !strings.Contains(out, expected) {
			t.Errorf("peers output should contain %q, got: %s", expected, out)
		}
	}
	t.Logf("3-node peers:\n%s", out)
}

func TestCLI_Status_NoServer(t *testing.T) {
	binary := buildBinary(t)
	_, err := runCLI(t, binary, "status", "--address", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected non-zero exit when no server is running")
	}
}

func TestCLI_Peers_NoServer(t *testing.T) {
	binary := buildBinary(t)
	_, err := runCLI(t, binary, "peers", "--address", "127.0.0.1:1")
	if err == nil {
		t.Fatal("expected non-zero exit when no server is running")
	}
}

func TestCLI_Help(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "--help")
	if err != nil {
		t.Fatalf("help command failed: %v\n%s", err, out)
	}
	for _, cmd := range []string{"start", "status", "peers", "version"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("help output missing command %q", cmd)
		}
	}
}

func TestCLI_Start_Help(t *testing.T) {
	binary := buildBinary(t)
	out, err := runCLI(t, binary, "start", "--help")
	if err != nil {
		t.Fatalf("start help failed: %v\n%s", err, out)
	}
	expectedFlags := []string{
		"--listen", "--raft-address", "--data-dir", "--deployment-id",
		"--sm", "--discovery", "--peers", "--shard-id", "--replica-id",
		"--multicast-secret", "--dns-domain", "--admin-port",
		"--tls-ca-file", "--tls-cert-file", "--tls-key-file",
	}
	for _, flag := range expectedFlags {
		if !strings.Contains(out, flag) {
			t.Errorf("start help missing flag %q", flag)
		}
	}
}
