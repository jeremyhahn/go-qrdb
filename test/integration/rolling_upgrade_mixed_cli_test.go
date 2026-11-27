//go:build integration

package integration_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
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
// The upgrade-probe keys put before activation read back on the N nodes,
// and the node logs prove the gated command never entered the Raft log: no
// member logged an OpUpgradeProbe apply, before or after the forwarding.
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
			requireNoProbeApplied(t, c, "in a mixed N/N+1 cluster")

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
			requireNoProbeApplied(t, c, "after the forwarded writes in a mixed N/N+1 cluster")
			requireNotActivated(t, c, "in a mixed N/N+1 cluster")
		})
	}
}

// requirePreActivationGating asserts, in the cluster's current mixed shape,
// that the probe's gated put invoked on every N+1 member over every
// transport takes the N path -- it reads back on every member, N members
// included, which an N replica could not do had it been sent the probe
// opcode -- that no member's log shows it applied as OpUpgradeProbe, so the
// gated command never entered the Raft log, and that writes through the N
// members keep applying everywhere.
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
	requireNoProbeApplied(t, c, "in the "+shape+" cluster")
	requireNotActivated(t, c, "in the "+shape+" cluster")
}

// TestT9_PreActivationGating (T9): in both mixed shapes the upgrade probe's
// gated operation invoked on an N+1 member is not sent to the cluster as the
// new command on any transport (the node logs show no OpUpgradeProbe apply),
// and the N members keep applying; after every member runs N+1 and the
// feature activates, the same operation succeeds and every member's log
// shows it applied as OpUpgradeProbe.
func TestT9_PreActivationGating(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	c := startUpgradeCluster(t, []string{bins.n, bins.n, bins.next})
	requirePreActivationGating(t, c, mixedShapes[0].name, mixedShapes[0].next)

	c.roll(t, 1, bins.next)
	requirePreActivationGating(t, c, mixedShapes[1].name, mixedShapes[1].next)

	c.roll(t, 0, bins.next)
	awaitActivated(t, c.logs, upgradeProbeFeature)

	after := make(map[string]string, 3*len(multiProtocols))
	afterKeys := make([]string, 0, 3*len(multiProtocols))
	for i, node := range c.nodes {
		for _, proto := range multiProtocols {
			key := fmt.Sprintf("%sactive/node%d-%s", upgradeProbeKeyPrefix, i+1, proto.name)
			value := "active-" + proto.name
			putAcked(t, node, proto, key, value)
			after[key] = value
			afterKeys = append(afterKeys, key)
		}
	}
	c.requireReadsEverywhere(t, after)
	awaitProbeApplied(t, c, afterKeys)
}

// TestRollingUpgrade_OldBinaryRefusedAfterActivation_CLI (T4): a cluster
// started on N rolls every node to N+1 and activates the probe, and the
// probe's put on every transport replicates as OpUpgradeProbe to every
// member. The N binary then refuses to start on an N+1 node's data
// directory with featuregate.UnsupportedFeatureError while the two
// surviving N+1 members keep serving writes on every transport; the node
// starts again on N+1 and every key reads back on every member.
func TestRollingUpgrade_OldBinaryRefusedAfterActivation_CLI(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	c := startUpgradeCluster(t, []string{bins.n, bins.n, bins.n})
	for i := range c.nodes {
		c.roll(t, i, bins.next)
	}
	awaitActivated(t, c.logs, upgradeProbeFeature)

	want := make(map[string]string, 3*len(multiProtocols))
	probeKeys := make([]string, 0, len(multiProtocols))
	for _, proto := range multiProtocols {
		key := fmt.Sprintf("%sactivated/%s", upgradeProbeKeyPrefix, proto.name)
		value := "activated-" + proto.name
		putAcked(t, c.nodes[0], proto, key, value)
		want[key] = value
		probeKeys = append(probeKeys, key)
	}
	c.requireReadsEverywhere(t, want)
	awaitProbeApplied(t, c, probeKeys)

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

	// The surviving N+1 members keep a quorum and serve every transport
	// while the N binary is refused.
	for _, i := range []int{0, 2} {
		for _, proto := range multiProtocols {
			key := fmt.Sprintf("refused/node%d/%s", i+1, proto.name)
			value := fmt.Sprintf("survivor-node%d-%s", i+1, proto.name)
			putAcked(t, c.nodes[i], proto, key, value)
			want[key] = value
		}
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

// keyDeliveryPeerPackage is the test peer that runs one half of a real
// clusterkeys HPKE key delivery: a Receiver behind TLS, or a Sender that
// ships an owner-sealed tenant barrier to it.
const keyDeliveryPeerPackage = "./test/integration/internal/keydeliverypeer"

// keyDeliveryPeerResult is the JSON line a keydeliverypeer process prints
// when it is done (or, for a receiver, the {"url":...} line it prints once
// it listens).
type keyDeliveryPeerResult struct {
	URL         string `json:"url"`
	Mode        string `json:"mode"`
	Release     string `json:"release"`
	Strategy    string `json:"strategy"`
	Fingerprint string `json:"fingerprint"`
	Status      int    `json:"status"`
	Body        string `json:"body"`
	Imported    bool   `json:"imported"`
	Error       string `json:"error"`
}

// TestRollingUpgrade_KeyEnvelopeAcrossVersions_CLI is T7's key-delivery
// half: T7's auto-unseal tenants never ship a key (every node initializes
// its own), so this test exchanges a real HPKE envelope between two
// processes built at different release stamps. Each direction -- N sends
// to N+1, N+1 sends to N -- must deliver an owner-sealed tenant barrier
// the receiver imports byte for byte (equal sealed-blob fingerprints). A
// sender two releases behind its receiver (the VersionGapRefused pair) is
// refused with the release-gap answer, and nothing is imported.
func TestRollingUpgrade_KeyEnvelopeAcrossVersions_CLI(t *testing.T) {
	const gapFrom, gapTo = "0.58.0-alpha", "0.60.0-alpha"
	bins := map[string]string{}
	for _, v := range []string{upgradeReleaseN, upgradeReleaseNext, gapFrom, gapTo} {
		bin, err := qrdbdharness.BuildBinary(keyDeliveryPeerPackage, qrdbdharness.WithReleaseVersion(v))
		if err != nil {
			t.Fatalf("build keydeliverypeer %s: %v", v, err)
		}
		bins[v] = bin
	}
	tlsDir := writeKeyDeliveryTLS(t)

	for _, dir := range []struct{ from, to string }{
		{from: upgradeReleaseN, to: upgradeReleaseNext},
		{from: upgradeReleaseNext, to: upgradeReleaseN},
	} {
		t.Run(dir.from+"_to_"+dir.to, func(t *testing.T) {
			sent, recv := runKeyDelivery(t, bins[dir.from], bins[dir.to], tlsDir)
			if sent.Release != dir.from || recv.Release != dir.to {
				t.Fatalf("peer releases = %q -> %q, want %q -> %q", sent.Release, recv.Release, dir.from, dir.to)
			}
			switch seal.StrategyID(sent.Strategy) {
			case seal.StrategyNone, seal.StrategyPassphrase, seal.StrategyShamir, "":
				t.Fatalf("sender barrier strategy %q is one DistributeTenantKey never ships", sent.Strategy)
			}
			if sent.Error != "" || !sent.Imported {
				t.Fatalf("send %s -> %s failed: %+v", dir.from, dir.to, sent)
			}
			if recv.Status != http.StatusOK || !recv.Imported || recv.Error != "" {
				t.Fatalf("receiver %s did not import: %+v", dir.to, recv)
			}
			if sent.Fingerprint == "" || recv.Fingerprint != sent.Fingerprint {
				t.Fatalf("imported fingerprint %q, want the sender's %q", recv.Fingerprint, sent.Fingerprint)
			}
		})
	}

	t.Run(gapFrom+"_to_"+gapTo+"_refused", func(t *testing.T) {
		sent, recv := runKeyDelivery(t, bins[gapFrom], bins[gapTo], tlsDir)
		if sent.Imported || sent.Error == "" {
			t.Fatalf("send across a release gap succeeded: %+v", sent)
		}
		if !strings.Contains(sent.Error, gapFrom) || !strings.Contains(sent.Error, gapTo) {
			t.Fatalf("sender error %q does not name both releases", sent.Error)
		}
		if recv.Status != http.StatusConflict || recv.Imported {
			t.Fatalf("receiver answer across a release gap: %+v, want 409 and nothing imported", recv)
		}
		var refusal struct {
			Error          string `json:"error"`
			ReleaseVersion string `json:"release_version"`
		}
		if err := json.Unmarshal([]byte(recv.Body), &refusal); err != nil {
			t.Fatalf("release refusal body %q: %v", recv.Body, err)
		}
		if refusal.ReleaseVersion != gapTo || !strings.Contains(refusal.Error, gapFrom) {
			t.Fatalf("release refusal = %+v, want release_version %q naming peer %q", refusal, gapTo, gapFrom)
		}
	})
}

// runKeyDelivery starts a receiver from recvBin, sends to it from sendBin
// and returns both processes' result lines.
func runKeyDelivery(t *testing.T, sendBin, recvBin, tlsDir string) (sent, recv keyDeliveryPeerResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	recvCmd := exec.CommandContext(ctx, recvBin, "receive", "--listen", "127.0.0.1:0", "--tls-dir", tlsDir)
	recvErr := &logCapture{}
	recvCmd.Stderr = recvErr
	out, err := recvCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("receiver stdout: %v", err)
	}
	if err := recvCmd.Start(); err != nil {
		t.Fatalf("start receiver: %v", err)
	}
	t.Cleanup(func() { _ = recvCmd.Process.Kill(); _ = recvCmd.Wait() })
	lines := make(chan keyDeliveryPeerResult, 2)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			var r keyDeliveryPeerResult
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				lines <- r
			}
		}
		close(lines)
	}()
	next := func(what string) keyDeliveryPeerResult {
		select {
		case r, ok := <-lines:
			if !ok {
				t.Fatalf("receiver exited before its %s line; stderr:\n%s", what, capturedLog(recvErr))
			}
			return r
		case <-ctx.Done():
			t.Fatalf("no receiver %s line; stderr:\n%s", what, capturedLog(recvErr))
		}
		return keyDeliveryPeerResult{}
	}
	listening := next("url")
	if listening.URL == "" {
		t.Fatalf("receiver did not listen: %+v", listening)
	}

	sendOut, err := exec.CommandContext(ctx, sendBin, "send", "--to", listening.URL, "--tls-dir", tlsDir).Output()
	if err != nil {
		t.Fatalf("sender: %v; stdout %q", err, sendOut)
	}
	if err := json.Unmarshal(bytes.TrimSpace(sendOut), &sent); err != nil {
		t.Fatalf("sender output %q: %v", sendOut, err)
	}
	recv = next("result")
	return sent, recv
}

// writeKeyDeliveryTLS issues a test CA and the two peers' node
// certificates (SPIFFE node SAN plus 127.0.0.1) into a fresh directory.
func writeKeyDeliveryTLS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Key Delivery Test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	writePEM(t, filepath.Join(dir, "ca.pem"), "CERTIFICATE", caDER)
	for i, node := range []string{"leader-node-1", "follower-node-1"} {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("%s key: %v", node, err)
		}
		spiffeID, err := url.Parse("spiffe://cluster.local/node/" + node)
		if err != nil {
			t.Fatalf("SPIFFE ID: %v", err)
		}
		der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(int64(i) + 2),
			Subject:      pkix.Name{CommonName: node},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
			URIs:         []*url.URL{spiffeID},
			IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		}, caCert, &key.PublicKey, caKey)
		if err != nil {
			t.Fatalf("%s certificate: %v", node, err)
		}
		keyDER, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("%s key encode: %v", node, err)
		}
		writePEM(t, filepath.Join(dir, node+".pem"), "CERTIFICATE", der)
		writePEM(t, filepath.Join(dir, node+"-key.pem"), "EC PRIVATE KEY", keyDER)
	}
	return dir
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// capturedLog returns what c has captured so far.
func capturedLog(c *logCapture) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
