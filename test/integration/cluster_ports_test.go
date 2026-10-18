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
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/secure"
)

// portsPinTimeout bounds how long a node may take to bind its full listener
// set once the cluster serves.
const portsPinTimeout = 60 * time.Second

// pinnedSocket is one socket a qrdbd process listens on: TCP in LISTEN, or an
// unconnected UDP socket bound to a fixed port.
type pinnedSocket struct {
	proto string
	addr  netip.AddrPort
}

func (s pinnedSocket) String() string { return s.proto + " " + s.addr.String() }

// portsNode is one member of the pinned-port cluster: every port qrdbd can
// be told to bind, each set explicitly.
type portsNode struct {
	replicaID                                  uint64
	raft, admin, grpc, quic, mcp, mysql, pgsql int
	noise, join                                int
	dataDir                                    string
	pid                                        int
}

// want is the pinned set docs/cluster-verification/user.md ("Pinned port
// set") names for a node of this shape: operator-supplied TLS (--tls-*-file)
// with every protocol switched on. --bootstrap-noise-port and
// --cluster-join-listen-port are set too, and are deliberately absent here:
// with operator TLS the node holds no cluster CA, so neither the trust-anchor
// listener (cmd/qrdbd/bootstrap.go, servesAnchor) nor the join listener
// (pkg/server/lifecycle/cluster_join.go, StartClusterJoin) starts. Setting
// them is what makes their absence a check rather than an omission.
func (n *portsNode) want() map[pinnedSocket]struct{} {
	host := netip.MustParseAddr("127.0.0.1")
	set := map[pinnedSocket]struct{}{}
	for _, p := range []int{n.admin, n.grpc, n.mcp, n.mysql, n.pgsql} {
		set[pinnedSocket{"tcp", netip.AddrPortFrom(host, uint16(p))}] = struct{}{}
	}
	for _, p := range []int{n.raft, n.admin, n.quic} {
		set[pinnedSocket{"udp", netip.AddrPortFrom(host, uint16(p))}] = struct{}{}
	}
	return set
}

// TestClusterPorts_PinnedSetIsExactlyWhatQrdbdBinds boots a three-node raft
// cluster with every listener's port set, reads the sockets each qrdbd
// process actually holds from /proc, and fails when a node listens on a
// socket the pinned set does not name or lacks one it does. The set is
// checked twice: while every node is still sealed (a sealed node may bind
// less, never more or elsewhere) and once the cluster serves (exactly the
// set). Every API listener is expected on --host, so a listener that binds
// another address -- loopback, or every interface -- fails the check too.
//
// The per-database etcd listener is not exercised: its listen_addr is part of
// the replicated database record, so three members on one host cannot each
// bind it. A UDP socket bound in the kernel's ephemeral range is an outbound
// client socket (the pinned ports are reserved below that range) and is not a
// listener a firewall has to open.
func TestClusterPorts_PinnedSetIsExactlyWhatQrdbdBinds(t *testing.T) {
	// qrdbd makes itself non-dumpable at start, which leaves /proc/<pid>/fd
	// owned by root and the socket inodes unreadable. The documented opt-out
	// (docs/deployment/user.md, "Attaching a debugger") touches only process
	// inspection, not what the node binds; the daemons inherit it from here.
	t.Setenv(secure.AllowProcessInspectionEnv, "true")

	const n = 3
	bin := buildBinary(t)
	ca := generateTestCA(t)
	raftPorts := allocatePorts(t, n)
	quicPorts := allocatePorts(t, n)
	tcpPorts := allocateTCPPorts(t, 7*n)

	nodes := make([]*portsNode, n)
	for i := range n {
		p := tcpPorts[7*i:]
		nodes[i] = &portsNode{
			replicaID: uint64(i + 1),
			raft:      raftPorts[i],
			quic:      quicPorts[i],
			admin:     p[0],
			grpc:      p[1],
			mcp:       p[2],
			mysql:     p[3],
			pgsql:     p[4],
			noise:     p[5],
			join:      p[6],
			dataDir:   t.TempDir(),
		}
	}

	peers := make([]string, n)
	peerAdminURLs := make(map[string]string, n)
	certPaths := make([]string, n)
	keyPaths := make([]string, n)
	for i, node := range nodes {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", node.raft)
		peers[i] = fmt.Sprintf("%d=%s", node.replicaID, raftAddr)
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", node.admin)
		certPaths[i], keyPaths[i] = ca.generateNodeCertWithSPIFFE(t, raftAddr, clusterTestTrustDomain)
	}
	cfgPath := writePeerAdminURLConfig(t, peerAdminURLs)
	appendFaultSealConfig(t, cfgPath, faultSealNone)
	memberIDs := memberIdentitiesFlag(t, certPaths)

	// The Unix socket path is kept short: sun_path holds 108 bytes, and a
	// t.TempDir() path can exceed that on its own.
	sockDir, err := os.MkdirTemp("", "qp")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	for i, node := range nodes {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", node.raft)
		args := []string{
			"start",
			"--config", cfgPath,
			"--host", "127.0.0.1",
			"--listen", raftAddr,
			"--raft-address", raftAddr,
			"--data-dir", node.dataDir,
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", strings.Join(peers, ","),
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", strconv.FormatUint(node.replicaID, 10),
			"--tls-ca-file", ca.CertPath,
			"--tls-cert-file", certPaths[i],
			"--tls-key-file", keyPaths[i],
			"--admin-port", strconv.Itoa(node.admin),
			"--grpc-port", strconv.Itoa(node.grpc),
			"--enable-quic", "--quic-port", strconv.Itoa(node.quic),
			"--enable-mcp", "--mcp-port", strconv.Itoa(node.mcp),
			"--enable-mysql", "--mysql-port", strconv.Itoa(node.mysql),
			"--enable-pgsql", "--pgsql-port", strconv.Itoa(node.pgsql),
			"--enable-unix", "--unix-socket-path", filepath.Join(sockDir, fmt.Sprintf("n%d.sock", node.replicaID)),
			"--bootstrap-noise-port", strconv.Itoa(node.noise),
			"--cluster-join-enabled", "--cluster-join-listen-port", strconv.Itoa(node.join),
		}
		cmd := startNode(t, bin, args, fmt.Sprintf("[ports-node%d] ", node.replicaID))
		node.pid = cmd.Process.Pid
	}

	// Sealed: whatever is bound already must be pinned.
	for _, node := range nodes {
		waitForBarrierStatusReachable(t, ca, node.admin, 90*time.Second)
		got := listeningSockets(t, node.pid)
		if extra := setMinus(got, node.want()); len(extra) > 0 {
			t.Errorf("sealed node %d listens on sockets the pinned set does not name: %v", node.replicaID, extra)
		}
	}
	if t.Failed() {
		t.FailNow()
	}

	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Go(func() { initAndUnsealBarrier(t, ca, node.admin, "") })
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
	adminPorts := make([]int, n)
	for i, node := range nodes {
		waitForHealthy(t, node.dataDir, 60*time.Second)
		waitForAdmin(t, ca.Pool, node.admin, 90*time.Second)
		adminPorts[i] = node.admin
	}
	waitForLeaderViaAdmin(t, ca, adminPorts, 60*time.Second)

	// Serving: exactly the pinned set, on every node.
	for _, node := range nodes {
		waitForPinnedSet(t, node)
	}
}

// waitForPinnedSet polls node's sockets until they equal its pinned set,
// failing with the difference when they do not within portsPinTimeout.
// Polling covers listeners started after the cluster first reports a leader;
// an unpinned listener that stays bound fails at the deadline.
func waitForPinnedSet(t *testing.T, node *portsNode) {
	t.Helper()
	want := node.want()
	var extra, missing []string
	if !pollUntil(portsPinTimeout, 500*time.Millisecond, func() bool {
		got := listeningSockets(t, node.pid)
		extra, missing = setMinus(got, want), setMinus(want, got)
		return len(extra) == 0 && len(missing) == 0
	}) {
		t.Fatalf("node %d: listens on sockets the pinned set does not name: %v; pinned but not bound: %v",
			node.replicaID, extra, missing)
	}
}

// setMinus returns the members of a that b lacks, sorted for a stable message.
func setMinus(a, b map[pinnedSocket]struct{}) []string {
	var out []string
	for s := range a {
		if _, ok := b[s]; !ok {
			out = append(out, s.String())
		}
	}
	slices.Sort(out)
	return out
}

// Socket states from include/net/tcp_states.h, as /proc/net/{tcp,udp} print
// them: a listening TCP socket, and an unconnected UDP socket.
const (
	procStateTCPListen = "0A"
	procStateUDPClose  = "07"
)

// listeningSockets returns the sockets process pid listens on, read from the
// kernel rather than from any configuration: the socket inodes the process
// holds open (/proc/<pid>/fd), matched against its network namespace's socket
// tables (/proc/<pid>/net/{tcp,tcp6,udp,udp6}).
func listeningSockets(t *testing.T, pid int) map[pinnedSocket]struct{} {
	t.Helper()
	inodes := socketInodes(t, pid)
	ephLow, ephHigh := ephemeralPortRange(t)
	out := map[pinnedSocket]struct{}{}
	for _, table := range []struct{ file, proto, state string }{
		{"tcp", "tcp", procStateTCPListen},
		{"tcp6", "tcp", procStateTCPListen},
		{"udp", "udp", procStateUDPClose},
		{"udp6", "udp", procStateUDPClose},
	} {
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/net/%s", pid, table.file))
		if err != nil {
			t.Fatalf("read socket table %s of pid %d: %v", table.file, pid, err)
		}
		lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
		for _, line := range lines[1:] {
			fields := strings.Fields(line)
			if len(fields) < 10 || fields[3] != table.state {
				continue
			}
			if _, ours := inodes[fields[9]]; !ours {
				continue
			}
			addr := parseProcAddr(t, fields[1])
			if table.proto == "udp" && int(addr.Port()) >= ephLow && int(addr.Port()) <= ephHigh {
				continue
			}
			out[pinnedSocket{table.proto, addr}] = struct{}{}
		}
	}
	return out
}

// socketInodes returns the inode of every socket pid holds open.
func socketInodes(t *testing.T, pid int) map[string]struct{} {
	t.Helper()
	dir := fmt.Sprintf("/proc/%d/fd", pid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("list fds of pid %d: %v", pid, err)
	}
	inodes := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(dir, e.Name()))
		if err != nil {
			continue // closed between the listing and the readlink
		}
		if inode, ok := strings.CutPrefix(target, "socket:["); ok {
			inodes[strings.TrimSuffix(inode, "]")] = struct{}{}
		}
	}
	return inodes
}

// ephemeralPortRange reads the kernel's local port range, the ports an
// unbound or port-0 socket is assigned from.
func ephemeralPortRange(t *testing.T) (low, high int) {
	t.Helper()
	raw, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Fatalf("read ephemeral port range: %v", err)
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		t.Fatalf("ephemeral port range %q: want two fields", raw)
	}
	if low, err = strconv.Atoi(fields[0]); err == nil {
		high, err = strconv.Atoi(fields[1])
	}
	if err != nil {
		t.Fatalf("ephemeral port range %q: %v", raw, err)
	}
	return low, high
}

// parseProcAddr decodes a /proc/net local_address, "HEXADDR:HEXPORT". The
// address is printed as 32-bit words in host byte order (little-endian on
// every platform CI runs on); an IPv4-mapped IPv6 address is unmapped, so a
// dual-stack socket compares equal to its IPv4 form.
func parseProcAddr(t *testing.T, field string) netip.AddrPort {
	t.Helper()
	hexAddr, hexPort, ok := strings.Cut(field, ":")
	if !ok {
		t.Fatalf("socket address %q: no port", field)
	}
	raw, err := hex.DecodeString(hexAddr)
	if err != nil || (len(raw) != 4 && len(raw) != 16) {
		t.Fatalf("socket address %q: %v", field, err)
	}
	for w := 0; w < len(raw); w += 4 {
		slices.Reverse(raw[w : w+4])
	}
	port, err := strconv.ParseUint(hexPort, 16, 16)
	if err != nil {
		t.Fatalf("socket port %q: %v", field, err)
	}
	addr, _ := netip.AddrFromSlice(raw)
	return netip.AddrPortFrom(addr.Unmap(), uint16(port))
}
