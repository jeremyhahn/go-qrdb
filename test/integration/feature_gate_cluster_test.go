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
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// gatedDatabase is a database every node provisions from its configuration,
// so all three nodes resolve it and every write to it carries the drop-fence
// guard once the gate enables guarded writes.
const gatedDatabase = "gated"

// writeGatedDatabaseConfig writes a qrdbd configuration that provisions
// gatedDatabase beside the default database.
func writeGatedDatabaseConfig(t *testing.T) string {
	t.Helper()
	cfg := "databases:\n" +
		"  - name: system\n    backend: pebble\n" +
		"  - name: " + gatedDatabase + "\n    backend: pebble\n"
	path := filepath.Join(t.TempDir(), "qrdbd.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// kvRequest issues one KV request to port, scoped to db when db is set.
func kvRequest(t *testing.T, client *http.Client, method string, port int, db, key string, body []byte, query string) (int, []byte) {
	t.Helper()
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s%s", port, key, query)
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if db != "" {
		req.Header.Set("X-QRDB-Database", db)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// On a three-node cluster -- on both the default pebble state machine and
// the MVCC one -- every node advertises its features, the cluster activates
// guarded writes, and writes to a named database are then proposed guarded:
// with its drop-fence marker set, a write through any node is refused by the
// state machine, where an unguarded one would have landed.
func TestCluster_FeatureGateActivatesGuardedWrites(t *testing.T) {
	for _, sm := range []string{"pebble", "mvcc"} {
		t.Run(sm, func(t *testing.T) {
			adminPorts, _, ca, logs := startThreeNodeClusterLogged(t, sm, "--config", writeGatedDatabaseConfig(t))
			client := mtlsHTTPClient(t, ca, 10*time.Second)

			deadline := time.Now().Add(60 * time.Second)
			activated := func() bool {
				for _, l := range logs {
					if l.Contains("feature gate: feature activated cluster-wide") {
						return true
					}
				}
				return false
			}
			for i, l := range logs {
				for !l.Contains("registered cluster member") {
					if time.Now().After(deadline) {
						t.Fatalf("node %d never advertised its features", i+1)
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
			for !activated() {
				if time.Now().After(deadline) {
					t.Fatal("the cluster never activated guarded writes")
				}
				time.Sleep(200 * time.Millisecond)
			}
			for i, l := range logs {
				if reason, failed := advertisementFailed(l.String()); failed {
					t.Fatalf("node %d refused or failed its advertisement: %s", i+1, reason)
				}
			}

			// A write to the named database lands on every replica.
			for i, port := range adminPorts {
				key := fmt.Sprintf("before-fence-%d", i)
				waitStatus(t, deadline, func() int {
					code, _ := kvRequest(t, client, http.MethodPut, port, gatedDatabase, key, []byte("v"), "")
					return code
				}, http.StatusNoContent, "write to %s through node %d", gatedDatabase, i+1)
			}

			// Drop the database through node 1. The catalog is node-local, so
			// nodes 2 and 3 still resolve it and admit a write to it; only
			// the guard the write carries -- evaluated by the state machine
			// against the tombstone the drop left -- refuses it. An unguarded
			// write through them would land in the dropped keyspace.
			waitStatus(t, deadline, func() int {
				req, err := http.NewRequest(http.MethodDelete,
					fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", adminPorts[0], gatedDatabase), nil)
				if err != nil {
					t.Fatalf("new request: %v", err)
				}
				resp, err := client.Do(req)
				if err != nil {
					return 0
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}, http.StatusNoContent, "drop %s through node 1", gatedDatabase)
			for i, port := range adminPorts[1:] {
				key := fmt.Sprintf("after-drop-%d", i)
				waitStatus(t, deadline, func() int {
					code, _ := kvRequest(t, client, http.MethodPut, port, gatedDatabase, key, []byte("v"), "")
					return code
				}, http.StatusConflict, "guarded write to the dropped %s through node %d", gatedDatabase, i+2)
				for j, readPort := range adminPorts[1:] {
					code, body := kvRequest(t, client, http.MethodGet, readPort, gatedDatabase, key, nil, "?consistency=eventual")
					if code == http.StatusOK {
						t.Fatalf("node %d applied the refused write %q (%s)", j+2, key, strings.TrimSpace(string(body)))
					}
				}
			}
		})
	}
}

// advertisementFailed reports the line of a node's log that shows it refused
// to run in the cluster or gave up advertising itself. A registration whose
// attempt hit its bound is not one: the start retries it, and its progress
// line names the attempt's error while the node goes on to register.
func advertisementFailed(log string) (string, bool) {
	refusal := (&featuregate.UnsupportedFeatureError{Feature: featuregate.GuardedWrites}).Error()
	for line := range strings.Lines(log) {
		switch {
		case strings.Contains(line, refusal), strings.Contains(line, "cannot run in this cluster"):
			return line, true
		case strings.Contains(line, `msg="startup read did not succeed before its deadline"`) &&
			strings.Contains(line, `op="feature gate start"`):
			return line, true
		}
	}
	return "", false
}

// TestFeatureGateLog_RetriedRegistrationIsNotAFailure feeds
// advertisementFailed the lines qrdbd's feature-gate start writes, produced by
// the startup read it runs under: a registration whose attempt hit its bound
// is retried and reported, and only a refusal or the deadline fails the boot.
func TestFeatureGateLog_RetriedRegistrationIsNotAFailure(t *testing.T) {
	registration := func(succeedAfter int, log *nodeLog) func(context.Context) (struct{}, error) {
		attempts := 0
		return func(actx context.Context) (struct{}, error) {
			attempts++
			if attempts > succeedAfter {
				// Succeed only once the progress line has reported the
				// retried failure, as a node's log shows it.
				for !log.Contains("failed to register self") {
					select {
					case <-actx.Done():
						return struct{}{}, actx.Err()
					case <-time.After(5 * time.Millisecond):
					}
				}
				return struct{}{}, nil
			}
			<-actx.Done()
			return struct{}{}, &cluster.MembershipCauseError{Reason: "failed to register self", Cause: actx.Err()}
		}
	}
	read := func(budget time.Duration, log *nodeLog) server.StartupRead {
		return server.StartupRead{
			Op:             "feature gate start",
			Budget:         budget,
			AttemptTimeout: 20 * time.Millisecond,
			Delay:          time.Millisecond,
			ReportInterval: 10 * time.Millisecond,
			Logger:         slog.New(slog.NewTextHandler(log, nil)),
		}
	}

	t.Run("retried registration", func(t *testing.T) {
		log := &nodeLog{}
		if _, err := server.RunStartupRead(context.Background(), read(time.Minute, log), registration(1, log)); err != nil {
			t.Fatalf("registration: %v", err)
		}
		if !log.Contains("failed to register self") {
			t.Fatalf("the retried attempt was not reported:\n%s", log)
		}
		if reason, failed := advertisementFailed(log.String()); failed {
			t.Fatalf("a registration retried to success read as a failure: %s", reason)
		}
	})
	t.Run("registration that gave up", func(t *testing.T) {
		log := &nodeLog{}
		if _, err := server.RunStartupRead(context.Background(), read(50*time.Millisecond, log), registration(1<<30, log)); err == nil {
			t.Fatal("a registration that never succeeded returned no error")
		}
		if _, failed := advertisementFailed(log.String()); !failed {
			t.Fatalf("a registration that gave up did not read as a failure:\n%s", log)
		}
	})
	t.Run("refusal", func(t *testing.T) {
		refusal := (&featuregate.UnsupportedFeatureError{Feature: featuregate.GuardedWrites}).Error()
		// qrdbd's main prints the error that ended the boot to stderr.
		line := refusal + "\n"
		if _, failed := advertisementFailed(line); !failed {
			t.Fatalf("a refusal did not read as a failure: %s", line)
		}
	})
}

// waitStatus polls do until it returns want or the deadline passes.
func waitStatus(t *testing.T, deadline time.Time, do func() int, want int, format string, args ...any) {
	t.Helper()
	last := 0
	for time.Now().Before(deadline) {
		if last = do(); last == want {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf(format+": last status %d, want %d", append(args, last, want)...)
}

// A node stopped gracefully withdraws its feature advertisement at once --
// its lease is revoked while its host is still up -- rather than leaving the
// record to count until the lease expires (cluster.DefaultLeaseTTL).
func TestCluster_GracefulShutdownWithdrawsTheAdvertisement(t *testing.T) {
	_, _, _, logs := startThreeNodeClusterLogged(t, "pebble")
	deadline := time.Now().Add(60 * time.Second)
	for i, l := range logs {
		for !l.Contains("registered cluster member") {
			if time.Now().After(deadline) {
				t.Fatalf("node %d never advertised its features", i+1)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}

	stopped := time.Now()
	if err := logs[2].cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM node 3: %v", err)
	}
	const withdrawn = `feature gate: node withdrew its advertisement" node_id=3`
	for !logs[0].Contains(withdrawn) && !logs[1].Contains(withdrawn) {
		if time.Since(stopped) > 5*time.Second {
			t.Fatal("node 3's advertisement was not withdrawn within 5s of a graceful stop; " +
				"it was left to expire with its lease")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := awaitNodeExit(t, logs[2].cmd, nodeStopTimeout); err != nil {
		t.Fatalf("node 3 did not stop cleanly: %v", err)
	}
}

// gatedClusterNode bundles one startGatedClusterAllTransports member's
// per-transport connection coordinates, so the _PerProtocol suite below can
// drive the drop-fence refusal over gRPC, QUIC, MCP, and Unix exactly as
// TestCluster_FeatureGateActivatesGuardedWrites drives it over REST.
type gatedClusterNode struct {
	admin    int
	grpcPort int
	quicPort int
	mcpPort  int
	unixSock string
}

// serverURLFor returns the --server value selecting proto against n.
func (n gatedClusterNode) serverURLFor(proto multiProtocol) string {
	if proto.name == "unix" {
		return "unix://" + n.unixSock
	}
	port := n.admin
	switch proto.name {
	case "grpc":
		port = n.grpcPort
	case "quic":
		port = n.quicPort
	case "mcp":
		port = n.mcpPort
	}
	return fmt.Sprintf("%s://127.0.0.1:%d", proto.scheme, port)
}

// cliArgsFor returns the root-level qrdb CLI flags selecting proto against
// n and presenting ca's shared bootstrap admin identity (certPath, keyPath).
func (n gatedClusterNode) cliArgsFor(ca *testCA, certPath, keyPath string, proto multiProtocol) []string {
	return []string{
		"--server", n.serverURLFor(proto),
		"--ca-file", ca.CertPath,
		"--cert-file", certPath,
		"--key-file", keyPath,
		"--timeout", "20s",
	}
}

// startGatedClusterAllTransports starts a three-node cluster -- otherwise
// identical to startThreeNodeClusterLogged with writeGatedDatabaseConfig --
// with gRPC (on by default), native QUIC, MCP, and Unix socket all enabled
// on every node, so TestCluster_FeatureGateActivatesGuardedWrites's refusal
// can be driven over any transport, not just REST. It returns each node's
// connection coordinates, the shared bootstrap admin identity's client
// certificate, and the per-node captured logs the activation assertions
// need.
func startGatedClusterAllTransports(t *testing.T, smType string) ([]gatedClusterNode, *testCA, string, string, []*nodeLog) {
	t.Helper()
	logs := make([]*nodeLog, 3)
	bin := buildBinary(t)
	ca := generateTestCA(t)
	cfgPath := writeGatedDatabaseConfig(t)
	ports := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	mcpPorts := allocateTCPPorts(t, 3)
	quicPorts := allocatePorts(t, 3)
	peersFlag := buildPeersFlag(ports)
	nodeTLS, memberIDs := ca.clusterNodeTLS(t, len(ports))
	dataDirs := make([]string, 3)
	nodes := make([]gatedClusterNode, 3)

	for i := range 3 {
		dataDir := t.TempDir()
		dataDirs[i] = dataDir
		sockDir, err := os.MkdirTemp("", "qrdb-gated-")
		if err != nil {
			t.Fatalf("socket dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(sockDir) })
		unixSock := filepath.Join(sockDir, "qrdbd.sock")

		args := []string{
			"start",
			"--config", cfgPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", dataDir,
			"--sm", smType,
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPorts[i]),
			"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPorts[i]),
			"--enable-unix", "--unix-socket-path", unixSock,
		}
		args = append(args, nodeTLS[i]...)

		prefix := fmt.Sprintf("[gated%d] ", i+1)
		logs[i] = &nodeLog{}
		logs[i].cmd = startNode(t, bin, args, prefix, logs[i])

		nodes[i] = gatedClusterNode{
			admin:    adminPorts[i],
			grpcPort: grpcPorts[i],
			quicPort: quicPorts[i],
			mcpPort:  mcpPorts[i],
			unixSock: unixSock,
		}
	}

	var sealWg sync.WaitGroup
	sealWg.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer sealWg.Done()
			waitForBarrierStatusReachable(t, ca, nodes[idx].admin, 90*time.Second)
			initAndUnsealBarrier(t, ca, nodes[idx].admin, "")
		}(i)
	}
	sealWg.Wait()

	for _, dir := range dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
	}
	adminPortsOnly := make([]int, 3)
	for i, n := range nodes {
		waitForAdmin(t, ca.Pool, n.admin, 90*time.Second)
		adminPortsOnly[i] = n.admin
	}

	leaderPort := waitForLeaderViaAdmin(t, ca, adminPortsOnly, 60*time.Second)
	warmupWrite(t, ca, leaderPort, 15*time.Second)

	certPath, keyPath := ca.boundAdminCert(t, dataDirs[0])
	return nodes, ca, certPath, keyPath, logs
}

// On a three-node cluster with every transport enabled, the drop-fence
// refusal TestCluster_FeatureGateActivatesGuardedWrites proves over REST
// behaves identically over gRPC, QUIC, MCP, and Unix (REST included for a
// uniform table): once guarded writes activate cluster-wide and the gated
// database is dropped through node 1, a `kv put` to it through node 2 --
// issued over each transport in turn -- is refused, and no node applies it.
func TestCluster_FeatureGateActivatesGuardedWrites_CLI_PerProtocol(t *testing.T) {
	cli := buildCLIBinary(t)
	nodes, ca, certPath, keyPath, logs := startGatedClusterAllTransports(t, "pebble")
	client := mtlsHTTPClient(t, ca, 10*time.Second)

	deadline := time.Now().Add(60 * time.Second)
	activated := func() bool {
		for _, l := range logs {
			if l.Contains("feature gate: feature activated cluster-wide") {
				return true
			}
		}
		return false
	}
	for i, l := range logs {
		for !l.Contains("registered cluster member") {
			if time.Now().After(deadline) {
				t.Fatalf("node %d never advertised its features", i+1)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	for !activated() {
		if time.Now().After(deadline) {
			t.Fatal("the cluster never activated guarded writes")
		}
		time.Sleep(200 * time.Millisecond)
	}

	// A write to the named database lands on every replica before the drop.
	for i, n := range nodes {
		key := fmt.Sprintf("cli-before-fence-%d", i)
		waitStatus(t, deadline, func() int {
			code, _ := kvRequest(t, client, http.MethodPut, n.admin, gatedDatabase, key, []byte("v"), "")
			return code
		}, http.StatusNoContent, "write to %s through node %d", gatedDatabase, i+1)
	}

	// Drop the database through node 1; the transport that performs the
	// drop is not the axis under test, so it stays on REST exactly like
	// TestCluster_FeatureGateActivatesGuardedWrites.
	waitStatus(t, deadline, func() int {
		req, err := http.NewRequest(http.MethodDelete,
			fmt.Sprintf("https://127.0.0.1:%d/v1/databases/%s", nodes[0].admin, gatedDatabase), nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}, http.StatusNoContent, "drop %s through node 1", gatedDatabase)

	for _, proto := range multiProtocols {
		t.Run(proto.name, func(t *testing.T) {
			node := nodes[1]
			key := fmt.Sprintf("cli-after-drop-%s", proto.name)
			args := append(node.cliArgsFor(ca, certPath, keyPath, proto),
				"--database", gatedDatabase, "kv", "put", key, "v")

			var out string
			var err error
			protoDeadline := time.Now().Add(20 * time.Second)
			for {
				out, err = runCLI(t, cli, args...)
				if err != nil || time.Now().After(protoDeadline) {
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			if err == nil {
				t.Fatalf("[%s] kv put %s/%s succeeded, want the guarded write refused (output: %s)", proto.name, gatedDatabase, key, out)
			}

			for j, n := range nodes[1:] {
				code, body := kvRequest(t, client, http.MethodGet, n.admin, gatedDatabase, key, nil, "?consistency=eventual")
				if code == http.StatusOK {
					t.Fatalf("[%s] node %d applied the refused write %q (%s)", proto.name, j+2, key, strings.TrimSpace(string(body)))
				}
			}
		})
	}
}
