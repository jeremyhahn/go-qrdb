//go:build integration

package integration_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/feature"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// upgradeProbeFeature is featuregate.UpgradeProbe, which only a
// qrdb_upgrade_probe build declares; the integration package is built
// without that tag, so it names the feature by its value.
const upgradeProbeFeature feature.Feature = "upgrade-probe"

// upgradeProbeKeyPrefix is the key prefix whose puts the synthetic N+1
// proposes as protocol.OpUpgradeProbe once upgradeProbeFeature is active.
const upgradeProbeKeyPrefix = "upgrade-probe/"

// The upgrade matrix stamps two adjacent releases (pkg/release.Adjacent):
// the N build announces upgradeReleaseN, the N+1 build upgradeReleaseNext,
// so a mixed cluster exercises the release check on every channel.
const (
	upgradeReleaseN    = "0.90.0-alpha"
	upgradeReleaseNext = "0.91.0-alpha"
)

// upgradeBinaries are the rolling-upgrade matrix's two daemon builds: n,
// today's qrdbd, and next, the synthetic N+1 -- the same tree built with the
// qrdb_upgrade_probe tag, which adds upgradeProbeFeature and its opcode.
type upgradeBinaries struct {
	n    string
	next string
}

// buildUpgradeBinaries builds the N and N+1 daemons.
func buildUpgradeBinaries(t *testing.T) upgradeBinaries {
	t.Helper()
	n, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage,
		qrdbdharness.WithReleaseVersion(upgradeReleaseN))
	if err != nil {
		t.Fatalf("build qrdbd (N): %v", err)
	}
	next, err := qrdbdharness.BuildBinary(qrdbdharness.DaemonPackage,
		qrdbdharness.WithBuildTags(qrdbdharness.DefaultBuildTags+" qrdb_upgrade_probe"),
		qrdbdharness.WithBinaryName("qrdbd-next"),
		qrdbdharness.WithReleaseVersion(upgradeReleaseNext))
	if err != nil {
		t.Fatalf("build qrdbd-next (N+1): %v", err)
	}
	return upgradeBinaries{n: n, next: next}
}

// upgradeCluster is a three-node pebble cluster serving every transport
// whose nodes may each run a different daemon binary, so a test can roll
// one node at a time from one build to another on the same data directory
// and start arguments.
type upgradeCluster struct {
	bins       []string
	ca         *testCA
	adminPorts []int
	dataDirs   []string
	args       [][]string
	cmds       []*exec.Cmd
	logs       []*nodeLog
	nodes      []*multiProtoNode
	// mysqlPorts and pgsqlPorts are each node's SQL wire listeners
	// (rolling_upgrade_sql_wire_test.go).
	mysqlPorts []int
	pgsqlPorts []int
}

// startUpgradeCluster starts node i on bins[i] and returns once a leader
// accepts writes and every node serves every transport to the CLI.
func startUpgradeCluster(t *testing.T, bins []string) *upgradeCluster {
	t.Helper()
	if len(bins) != 3 {
		t.Fatalf("startUpgradeCluster: %d binaries, want 3", len(bins))
	}
	cli := buildCLIBinary(t)
	c := &upgradeCluster{
		bins:       append([]string(nil), bins...),
		ca:         generateTestCA(t),
		adminPorts: allocateTCPPorts(t, 3),
		dataDirs:   make([]string, 3),
		args:       make([][]string, 3),
		cmds:       make([]*exec.Cmd, 3),
		logs:       make([]*nodeLog, 3),
		nodes:      make([]*multiProtoNode, 3),
		mysqlPorts: allocateTCPPorts(t, 3),
		pgsqlPorts: allocateTCPPorts(t, 3),
	}
	grpcPorts := allocateTCPPorts(t, 3)
	quicPorts := allocateTCPPorts(t, 3)
	mcpPorts := allocateTCPPorts(t, 3)
	unixPaths := make([]string, 3)
	ports := allocatePorts(t, 3)
	peersFlag := buildPeersFlag(ports)

	peerAdminURLs := make(map[string]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", ports[i])
		peerAdminURLs[raftAddr] = fmt.Sprintf("https://127.0.0.1:%d", c.adminPorts[i])
	}
	cfgPath := writePeerAdminURLConfig(t, peerAdminURLs)

	// Every certificate is issued before any node starts: each node needs
	// every peer's member identity (#556).
	nodeCertPaths := make([]string, 3)
	nodeKeyPaths := make([]string, 3)
	for i := range 3 {
		raftAddr := fmt.Sprintf("127.0.0.1:%d", ports[i])
		nodeCertPaths[i], nodeKeyPaths[i] = c.ca.generateNodeCertWithSPIFFE(t, raftAddr, clusterTestTrustDomain)
	}
	memberIDs := memberIdentitiesFlag(t, nodeCertPaths)

	for i := range 3 {
		c.dataDirs[i] = t.TempDir()
		// os.MkdirTemp keeps the socket path under the length limit a
		// test-named t.TempDir can pass.
		sockDir, err := os.MkdirTemp("", "qrdb-upg-")
		if err != nil {
			t.Fatalf("socket dir: %v", err)
		}
		t.Cleanup(func() { os.RemoveAll(sockDir) })
		unixPaths[i] = filepath.Join(sockDir, "qrdbd.sock")

		c.args[i] = []string{
			"start",
			"--config", cfgPath,
			"--listen", fmt.Sprintf("0.0.0.0:%d", ports[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", ports[i]),
			"--data-dir", c.dataDirs[i],
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIDs,
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", c.adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--enable-quic", "--quic-port", fmt.Sprintf("%d", quicPorts[i]),
			"--enable-mcp", "--mcp-port", fmt.Sprintf("%d", mcpPorts[i]),
			"--enable-unix", "--unix-socket-path", unixPaths[i],
			"--enable-mysql", "--mysql-port", fmt.Sprintf("%d", c.mysqlPorts[i]),
			"--enable-pgsql", "--pgsql-port", fmt.Sprintf("%d", c.pgsqlPorts[i]),
			"--sql-database", sqlClusterDatabase,
			"--tls-ca-file", c.ca.CertPath,
			"--tls-cert-file", nodeCertPaths[i],
			"--tls-key-file", nodeKeyPaths[i],
		}
		c.logs[i] = &nodeLog{}
		c.cmds[i] = startNode(t, c.bins[i], c.args[i], fmt.Sprintf("[node%d] ", i+1), c.logs[i])
	}

	bootstrapNodes(t, c.ca, c.adminPorts...)
	for i, dir := range c.dataDirs {
		waitForHealthy(t, dir, 60*time.Second)
		waitForAdmin(t, c.ca.Pool, c.adminPorts[i], 90*time.Second)
	}
	leaderPort := waitForLeaderViaAdmin(t, c.ca, c.adminPorts, 60*time.Second)
	warmupWrite(t, c.ca, leaderPort, 15*time.Second)

	for i := range 3 {
		certPath, keyPath := c.ca.boundAdminCert(t, c.dataDirs[i])
		c.nodes[i] = &multiProtoNode{
			cli:        cli,
			ca:         c.ca,
			dataDir:    c.dataDirs[i],
			clientCert: certPath,
			clientKey:  keyPath,
			unixSocket: unixPaths[i],
			restPort:   c.adminPorts[i],
			grpcPort:   grpcPorts[i],
			quicPort:   quicPorts[i],
			mcpPort:    mcpPorts[i],
		}
	}
	c.awaitServing(t, -1)
	return c
}

// awaitServing waits until every node but skip serves every transport.
func (c *upgradeCluster) awaitServing(t *testing.T, skip int) {
	t.Helper()
	for i, node := range c.nodes {
		if i == skip {
			continue
		}
		for _, proto := range multiProtocols {
			waitForCLIProtoReady(t, node, proto, 60*time.Second)
		}
	}
}

// roll stops node i and starts it again on bin with its data directory and
// start arguments unchanged -- one step of a rolling upgrade or rollback --
// and returns once the node is healthy, the cluster has a leader and the
// node serves every transport again.
func (c *upgradeCluster) roll(t *testing.T, i int, bin string) {
	t.Helper()
	c.stopNode(t, i)
	c.restartNode(t, i, bin)
}

// stopNode stops node i and forgets its process.
func (c *upgradeCluster) stopNode(t *testing.T, i int) {
	t.Helper()
	stopNode(t, c.cmds[i], 10*time.Second)
	c.cmds[i] = nil
}

// restartNode starts node i on bin and waits until it is unsealed,
// healthy, serving admin, the cluster has a leader, and every protocol
// answers on it.
func (c *upgradeCluster) restartNode(t *testing.T, i int, bin string) {
	t.Helper()
	c.bins[i] = bin
	c.cmds[i] = startNode(t, bin, c.args[i], fmt.Sprintf("[node%d] ", i+1), c.logs[i])
	waitForNodesSelfUnsealed(t, c.ca, c.adminPorts[i])
	waitForHealthy(t, c.dataDirs[i], 60*time.Second)
	waitForAdmin(t, c.ca.Pool, c.adminPorts[i], 90*time.Second)
	waitForLeaderViaAdmin(t, c.ca, c.adminPorts, 60*time.Second)
	for _, proto := range multiProtocols {
		waitForCLIProtoReady(t, c.nodes[i], proto, 60*time.Second)
	}
}

// awaitActivated waits until every node has logged the cluster-wide
// activation of f.
func awaitActivated(t *testing.T, logs []*nodeLog, f feature.Feature) {
	t.Helper()
	waitForFeatureActive(t, logs, f, 90*time.Second)
}

// activatedOn returns the 1-based numbers of the nodes that logged the
// cluster-wide activation of f.
func activatedOn(logs []*nodeLog, f feature.Feature) []int {
	var on []int
	for i, l := range logs {
		if featureActiveIn(l.String(), f) {
			on = append(on, i+1)
		}
	}
	return on
}

// upgradeProbeAppliedLog is the line a qrdb_upgrade_probe qrdbd logs, with the
// key, each time its state machine applies protocol.OpUpgradeProbe
// (store.UpgradeProbeAppliedLog, defined only under that tag). Every member
// applies every committed entry, so the line on a node shows that the key
// travelled through the Raft log as the gated command, not as a plain put.
const upgradeProbeAppliedLog = "upgrade-probe: applied OpUpgradeProbe"

// probeAppliedKeys returns the keys a node log shows applied as
// OpUpgradeProbe, with any "<tenant>/<database>/" namespace kept.
func probeAppliedKeys(log string) []string {
	var keys []string
	for _, line := range strings.Split(log, "\n") {
		i := strings.Index(line, upgradeProbeAppliedLog)
		if i < 0 {
			continue
		}
		rest := line[i+len(upgradeProbeAppliedLog):]
		if j := strings.Index(rest, `"key":"`); j >= 0 {
			v := rest[j+len(`"key":"`):]
			if end := strings.IndexByte(v, '"'); end >= 0 {
				keys = append(keys, v[:end])
			}
			continue
		}
		if j := strings.Index(rest, "key="); j >= 0 {
			v := strings.Trim(strings.Fields(rest[j+len("key="):] + " ")[0], `"`)
			keys = append(keys, v)
		}
	}
	return keys
}

// probeKeyMatches reports whether an applied key is want, at the root of the
// keyspace or inside a database namespace.
func probeKeyMatches(applied, want string) bool {
	return applied == want || strings.HasSuffix(applied, "/"+want)
}

// awaitProbeApplied polls every node's log until it shows each of keys
// applied as OpUpgradeProbe: the gated command replicated to every member.
func awaitProbeApplied(t *testing.T, c *upgradeCluster, keys []string) {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for i, l := range c.logs {
		var missing []string
		if !pollUntil(time.Until(deadline), 200*time.Millisecond, func() bool {
			applied := probeAppliedKeys(l.String())
			missing = missing[:0]
			for _, want := range keys {
				if !slices.ContainsFunc(applied, func(a string) bool { return probeKeyMatches(a, want) }) {
					missing = append(missing, want)
				}
			}
			return len(missing) == 0
		}) {
			t.Fatalf("node %d never applied %v as OpUpgradeProbe", i+1, missing)
		}
	}
}

// requireNoProbeApplied fails if any node's log shows any key applied as
// OpUpgradeProbe: before activation the gated command must never enter the
// Raft log. Each put it follows was acknowledged after its commit, and every
// member that is up applies what is committed before it serves a
// linearizable read, so callers check after requireReadsEverywhere.
func requireNoProbeApplied(t *testing.T, c *upgradeCluster, when string) {
	t.Helper()
	for i, l := range c.logs {
		if applied := probeAppliedKeys(l.String()); len(applied) != 0 {
			t.Fatalf("node %d applied %v as OpUpgradeProbe %s", i+1, applied, when)
		}
	}
}

// putAcked puts key=value through node over proto, retrying while the
// cluster has no leader, and fails the test if the write is never
// acknowledged.
func putAcked(t *testing.T, node *multiProtoNode, proto multiProtocol, key, value string) {
	t.Helper()
	var out string
	var err error
	if !pollUntil(60*time.Second, 200*time.Millisecond, func() bool {
		out, err = runCLIProto(t, node, proto, "kv", "put", key, value)
		return err == nil
	}) {
		t.Fatalf("[%s] kv put %s never acknowledged: %v\noutput: %s", proto.name, key, err, out)
	}
}

// requireReadsEverywhere fails the test unless every key in want reads back
// its value on every node over every transport.
func (c *upgradeCluster) requireReadsEverywhere(t *testing.T, want map[string]string) {
	t.Helper()
	for i, node := range c.nodes {
		for _, proto := range multiProtocols {
			for key, value := range want {
				var out string
				var err error
				if !pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
					out, err = runCLIProto(t, node, proto, "kv", "get", key)
					return err == nil && strings.Contains(out, value)
				}) {
					t.Fatalf("node %d [%s]: kv get %s never read %q: %v\noutput: %s",
						i+1, proto.name, key, value, err, out)
				}
			}
		}
	}
}

// shard1Leader returns the index of the node every node agrees leads
// shard 1.
func (c *upgradeCluster) shard1Leader(t *testing.T) int {
	t.Helper()
	return agreedShard1LeaderOf(t, c.ca, [3]*multiProtoNode(c.nodes))
}

// moveLeader hands shard 1's leadership to node to.
func (c *upgradeCluster) moveLeader(t *testing.T, to int) {
	t.Helper()
	moveShard1Leader(t, c.ca, [3]*multiProtoNode(c.nodes), to)
}

// agreedShard1LeaderOf returns the index of the node every one of nodes,
// replicas 1 to 3 of shard 1, agrees leads it.
func agreedShard1LeaderOf(t *testing.T, ca *testCA, nodes [3]*multiProtoNode) int {
	t.Helper()
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	return agreedShard1Leader(t, client, nodes, [3]uint64{1, 2, 3}, []int{0, 1, 2}, 60*time.Second)
}

// moveShard1Leader hands shard 1's leadership to nodes[to] with `qrdb admin
// member transfer-leadership`, asked of the current leader over REST, and
// returns once every node agrees that nodes[to] leads.
func moveShard1Leader(t *testing.T, ca *testCA, nodes [3]*multiProtoNode, to int) {
	t.Helper()
	var out string
	var err error
	if !pollUntil(90*time.Second, 250*time.Millisecond, func() bool {
		from := agreedShard1LeaderOf(t, ca, nodes)
		if from == to {
			return true
		}
		out, err = runCLIProto(t, nodes[from], multiProtocols[0],
			"admin", "member", "transfer-leadership", strconv.Itoa(to+1), "--shard-id", "1")
		return false
	}) {
		t.Fatalf("shard 1 leadership never moved to node %d: last transfer-leadership: %v\noutput: %s", to+1, err, out)
	}
}
