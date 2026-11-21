//go:build integration

package integration_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

// upgradeProbeALPN is the identifier the upgrade-probe (N+1) build prefers
// (wire.ProtocolV4Probe, declared only under the qrdb_upgrade_probe tag this
// test binary is not built with).
const upgradeProbeALPN = "qrdb-quic/4-probe"

// requireStampedReleases asserts every node's binary announces the release
// the upgrade matrix stamped on it: upgradeReleaseN on N, upgradeReleaseNext
// on N+1 (the release each node advertises to the cluster).
func requireStampedReleases(t *testing.T, bins upgradeBinaries, nodeBins []string) {
	t.Helper()
	for i, bin := range nodeBins {
		want := upgradeReleaseN
		if bin == bins.next {
			want = upgradeReleaseNext
		}
		out, err := exec.Command(bin, "version").CombinedOutput()
		if err != nil {
			t.Fatalf("node %d: %s version: %v\n%s", i+1, bin, err, out)
		}
		if !strings.Contains(string(out), "QuicRaft DB v"+want+"\n") {
			t.Fatalf("node %d: version output %q does not announce release %s", i+1, out, want)
		}
	}
}

// negotiatedALPN completes a QUIC handshake with node as a client offering
// offer, in order, and returns the ALPN identifier the node selected.
func negotiatedALPN(t *testing.T, node *multiProtoNode, offer []string) string {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(node.clientCert, node.clientKey)
	if err != nil {
		t.Fatalf("load client certificate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := quicgo.DialAddr(ctx, fmt.Sprintf("127.0.0.1:%d", node.quicPort), &tls.Config{
		RootCAs: node.ca.Pool, Certificates: []tls.Certificate{cert}, NextProtos: offer, MinVersion: tls.VersionTLS13,
	}, &quicgo.Config{})
	if err != nil {
		t.Fatalf("QUIC handshake offering %v: %v", offer, err)
	}
	defer func() { _ = conn.CloseWithError(0, "") }()
	return conn.ConnectionState().TLS.NegotiatedProtocol
}

// requireALPNNegotiation asserts the QUIC version negotiation of a mixed
// cluster: an N+1 peer reaching an N node and an N peer reaching an N+1
// node both fall back to wire.ProtocolV3, and two N+1 peers agree on the
// newer identifier.
func requireALPNNegotiation(t *testing.T, c *upgradeCluster, nIdx, nextIdx int) {
	t.Helper()
	offerNext := []string{upgradeProbeALPN, wire.ProtocolV3}
	offerN := []string{wire.ProtocolV3}
	for _, tc := range []struct {
		name  string
		node  int
		offer []string
		want  string
	}{
		{name: "N+1 view of an N node", node: nIdx, offer: offerNext, want: wire.ProtocolV3},
		{name: "N view of an N+1 node", node: nextIdx, offer: offerN, want: wire.ProtocolV3},
		{name: "N+1 view of an N+1 node", node: nextIdx, offer: offerNext, want: upgradeProbeALPN},
	} {
		if got := negotiatedALPN(t, c.nodes[tc.node], tc.offer); got != tc.want {
			t.Fatalf("%s (node %d): negotiated %q, want %q", tc.name, tc.node+1, got, tc.want)
		}
	}
}

// mixedShapes are the two mixed-version clusters a rolling upgrade passes
// through: true marks a node on N+1.
var mixedShapes = []struct {
	name string
	next [3]bool
}{
	{name: "N-N-next", next: [3]bool{false, false, true}},
	{name: "N-next-next", next: [3]bool{false, true, true}},
}

// shapeBinaries returns each node's daemon for a mixed shape, and the
// index of the first N node and of the first N+1 node.
func shapeBinaries(bins upgradeBinaries, next [3]bool) (nodeBins []string, nIdx, nextIdx int) {
	nIdx, nextIdx = -1, -1
	for i, isNext := range next {
		if isNext {
			nodeBins = append(nodeBins, bins.next)
			if nextIdx < 0 {
				nextIdx = i
			}
			continue
		}
		nodeBins = append(nodeBins, bins.n)
		if nIdx < 0 {
			nIdx = i
		}
	}
	return nodeBins, nIdx, nextIdx
}

// requireNotActivated fails the test if any node logged the activation of
// the upgrade probe: the gate holds while an N member is present.
func requireNotActivated(t *testing.T, c *upgradeCluster, when string) {
	t.Helper()
	if on := activatedOn(c.logs, upgradeProbeFeature); len(on) != 0 {
		t.Fatalf("%s activated on nodes %v %s", upgradeProbeFeature, on, when)
	}
}

// TestRollingUpgrade_MixedCluster_CLI (T2): a mixed cluster -- two N and
// one N+1, and one N and two N+1 -- serves every transport through the CLI
// on every node, and forwards writes in both directions: through an N+1
// follower to an N leader, and through an N follower to an N+1 leader.
func TestRollingUpgrade_MixedCluster_CLI(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	for _, shape := range mixedShapes {
		t.Run(shape.name, func(t *testing.T) {
			nodeBins, nIdx, nextIdx := shapeBinaries(bins, shape.next)
			c := startUpgradeCluster(t, nodeBins)
			requireStampedReleases(t, bins, nodeBins)
			// The CLI is an N build: its QUIC puts below reach the N+1
			// nodes over the fallback the negotiation asserts here.
			requireALPNNegotiation(t, c, nIdx, nextIdx)

			want := make(map[string]string, 2*3*len(multiProtocols))
			for i, node := range c.nodes {
				for _, proto := range multiProtocols {
					key := fmt.Sprintf("mixed/node%d/%s", i+1, proto.name)
					value := fmt.Sprintf("v-node%d-%s", i+1, proto.name)
					putAcked(t, node, proto, key, value)
					want[key] = value

					// Before activation an upgrade-probe key is a plain put on
					// every build: an N replica refuses the probe opcode and
					// would store nothing, so reading it back on the N nodes
					// proves no member was sent the new command.
					probeKey := fmt.Sprintf("%snode%d-%s", upgradeProbeKeyPrefix, i+1, proto.name)
					probeValue := "probe-" + value
					putAcked(t, node, proto, probeKey, probeValue)
					want[probeKey] = probeValue
				}
			}
			c.requireReadsEverywhere(t, want)

			// The SQL wires serve the mixed cluster too: a row inserted
			// over MySQL or PostgreSQL on any node, N or N+1, reads back on
			// every node over both wires.
			wantSQL := make(map[string]string, 3*len(upgradeSQLWires))
			for i := range c.nodes {
				for _, wire := range upgradeSQLWires {
					key := fmt.Sprintf("mixed/node%d/%s", i+1, wire)
					value := fmt.Sprintf("v-node%d-%s", i+1, wire)
					c.sqlPutAcked(t, i, wire, key, value)
					wantSQL[key] = value
				}
			}
			c.requireSQLReadsEverywhere(t, wantSQL)

			for _, dir := range []struct {
				name        string
				leader, via int
			}{
				{name: "next follower to N leader", leader: nIdx, via: nextIdx},
				{name: "N follower to next leader", leader: nextIdx, via: nIdx},
			} {
				c.moveLeader(t, dir.leader)
				fwd := make(map[string]string, len(multiProtocols))
				for _, proto := range multiProtocols {
					key := fmt.Sprintf("forward/leader%d-via%d/%s", dir.leader+1, dir.via+1, proto.name)
					value := "fwd-" + proto.name
					putAcked(t, c.nodes[dir.via], proto, key, value)
					fwd[key] = value
				}
				// A SQL write on the follower is forwarded to the leader
				// of the other release over both wires.
				fwdSQL := make(map[string]string, len(upgradeSQLWires))
				for _, wire := range upgradeSQLWires {
					key := fmt.Sprintf("forward/leader%d-via%d/%s", dir.leader+1, dir.via+1, wire)
					value := "fwd-" + string(wire)
					c.sqlPutAcked(t, dir.via, wire, key, value)
					fwdSQL[key] = value
				}
				if got := c.shard1Leader(t); got != dir.leader {
					t.Fatalf("%s: leadership moved to node %d during the writes, want node %d", dir.name, got+1, dir.leader+1)
				}
				c.requireReadsEverywhere(t, fwd)
				c.requireSQLReadsEverywhere(t, fwdSQL)
			}
			requireNotActivated(t, c, "in a mixed N/N+1 cluster")
		})
	}
}

// requirePreActivationGating asserts, in the cluster's current mixed shape,
// that the probe's gated put invoked on every N+1 member over every
// transport takes the N path -- it reads back on every member, N members
// included, which an N replica could not do had it been sent the probe
// opcode -- and that writes through the N members keep applying everywhere.
func requirePreActivationGating(t *testing.T, c *upgradeCluster, shape string, next [3]bool) {
	t.Helper()
	want := make(map[string]string, 3*len(multiProtocols))
	for i, node := range c.nodes {
		for _, proto := range multiProtocols {
			if next[i] {
				key := fmt.Sprintf("%sgated/%s/node%d-%s", upgradeProbeKeyPrefix, shape, i+1, proto.name)
				value := "gated-" + proto.name
				putAcked(t, node, proto, key, value)
				want[key] = value
				continue
			}
			key := fmt.Sprintf("gated/%s/node%d-%s", shape, i+1, proto.name)
			value := "n-" + proto.name
			putAcked(t, node, proto, key, value)
			want[key] = value
		}
	}
	c.requireReadsEverywhere(t, want)
	requireNotActivated(t, c, "in the "+shape+" cluster")
}

// TestT9_PreActivationGating (T9): in both mixed shapes the upgrade probe's
// gated operation invoked on an N+1 member is not sent to the cluster as the
// new command on any transport, and the N members keep applying; after every
// member runs N+1 and the feature activates, the same operation succeeds
// and replicates to every member.
func TestT9_PreActivationGating(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	c := startUpgradeCluster(t, []string{bins.n, bins.n, bins.next})
	requirePreActivationGating(t, c, mixedShapes[0].name, mixedShapes[0].next)

	c.roll(t, 1, bins.next)
	requirePreActivationGating(t, c, mixedShapes[1].name, mixedShapes[1].next)

	c.roll(t, 0, bins.next)
	awaitActivated(t, c.logs, upgradeProbeFeature)

	after := make(map[string]string, 3*len(multiProtocols))
	for i, node := range c.nodes {
		for _, proto := range multiProtocols {
			key := fmt.Sprintf("%sactive/node%d-%s", upgradeProbeKeyPrefix, i+1, proto.name)
			value := "active-" + proto.name
			putAcked(t, node, proto, key, value)
			after[key] = value
		}
	}
	c.requireReadsEverywhere(t, after)
}

// TestRollingUpgrade_OldBinaryRefusedAfterActivation_CLI (T4): once the
// cluster has activated the probe, the N binary refuses to start on an
// N+1 node's data directory with featuregate.UnsupportedFeatureError, and
// the node starts again on N+1 and reads the data.
func TestRollingUpgrade_OldBinaryRefusedAfterActivation_CLI(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	c := startUpgradeCluster(t, []string{bins.next, bins.next, bins.next})
	awaitActivated(t, c.logs, upgradeProbeFeature)

	want := map[string]string{upgradeProbeKeyPrefix + "x": "activated"}
	for key, value := range want {
		putAcked(t, c.nodes[0], multiProtocols[0], key, value)
	}
	c.requireReadsEverywhere(t, want)

	const node = 1
	stopNode(t, c.cmds[node], 10*time.Second)
	oldLog := &nodeLog{}
	c.cmds[node] = startNode(t, bins.n, c.args[node], fmt.Sprintf("[node%d-N] ", node+1), oldLog)
	if err := awaitNodeExit(t, c.cmds[node], 2*time.Minute); err == nil {
		t.Fatalf("the N binary exited cleanly on node %d after %s activated, want a refusal", node+1, upgradeProbeFeature)
	}
	// upgradeProbeFeature is featuregate.UpgradeProbe by value; the N
	// binary names it from the activated set it read.
	refusal := (&featuregate.UnsupportedFeatureError{Feature: upgradeProbeFeature}).Error()
	if !strings.Contains(oldLog.String(), refusal) {
		t.Fatalf("the N binary's log on node %d does not hold the refusal %q", node+1, refusal)
	}

	c.roll(t, node, bins.next)
	c.requireReadsEverywhere(t, want)
}

// TestRollingUpgrade_KeyDeliveryAcrossVersions_CLI (T7): in a cluster of
// one N and two N+1 members a raft-isolated tenant created through the CLI
// is served on every node under that node's own key, both with the leader
// on the N node (N proposes, N+1 members follow) and with the leader on an
// N+1 node (N+1 proposes, the N member follows).
func TestRollingUpgrade_KeyDeliveryAcrossVersions_CLI(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	nodeBins, nIdx, nextIdx := shapeBinaries(bins, mixedShapes[1].next)
	c := startIsolationRaftCluster(t, nodeBins)

	cli := buildCLIBinary(t)
	var nodes [3]*multiProtoNode
	for i := range nodes {
		certPath, keyPath := c.ca.boundAdminCert(t, c.dataDirs[i])
		nodes[i] = &multiProtoNode{
			cli:        cli,
			ca:         c.ca,
			dataDir:    c.dataDirs[i],
			clientCert: certPath,
			clientKey:  keyPath,
			restPort:   c.adminPorts[i],
			grpcPort:   c.grpcPorts[i],
		}
	}

	for _, dir := range []struct {
		tenant string
		leader int
	}{
		{tenant: "upg-from-n", leader: nIdx},
		{tenant: "upg-from-next", leader: nextIdx},
	} {
		moveShard1Leader(t, c.ca, nodes, dir.leader)
		certPath, keyPath := createTenantWithAdminCert(t, nodes[dir.leader], multiProtocols[0], dir.tenant,
			"Upgrade key delivery "+dir.tenant, "raft", true, dir.tenant+"-admin")
		client := mtlsHTTPClientFromFiles(t, c.ca, certPath, keyPath, 15*time.Second)
		// A node trusts the tenant admin's certificate once the tenant's
		// record has replicated to it.
		for i, port := range c.adminPorts {
			url := fmt.Sprintf("https://127.0.0.1:%d/v1/tenants/%s/barrier/status", port, dir.tenant)
			var code int
			if !pollUntil(2*time.Minute, 250*time.Millisecond, func() bool {
				_, code = iraftAPIGetTolerant(t, client, url)
				return code != 0
			}) {
				t.Fatalf("node %d never accepted tenant %q's admin certificate (last status %d)", i+1, dir.tenant, code)
			}
		}
		assertPerNodeTenantKeys(t, client, c.adminPorts, dir.tenant, 2*time.Minute)
	}
}
