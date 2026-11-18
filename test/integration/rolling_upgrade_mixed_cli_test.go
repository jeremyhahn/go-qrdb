//go:build integration

package integration_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
)

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
				if got := c.shard1Leader(t); got != dir.leader {
					t.Fatalf("%s: leadership moved to node %d during the writes, want node %d", dir.name, got+1, dir.leader+1)
				}
				c.requireReadsEverywhere(t, fwd)
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
	c := startUpgradeCluster(t, nodeBins)

	for _, dir := range []struct {
		tenant string
		leader int
	}{
		{tenant: "upg-from-n", leader: nIdx},
		{tenant: "upg-from-next", leader: nextIdx},
	} {
		c.moveLeader(t, dir.leader)
		proto := multiProtocols[0]
		certPath, keyPath := createTenantWithAdminCert(t, c.nodes[dir.leader], proto, dir.tenant,
			"Upgrade key delivery "+dir.tenant, "raft", true, dir.tenant+"-admin")
		client := mtlsHTTPClientFromFiles(t, c.ca, certPath, keyPath, 15*time.Second)
		assertPerNodeTenantKeys(t, client, c.adminPorts, dir.tenant, 2*time.Minute)
	}
	requireNotActivated(t, c, "in a mixed N/N+1 cluster")
}
