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

// This file holds what the cluster fault proofs (cluster_crash_test.go,
// cluster_partition_test.go) share: a 3-node cluster the test can SIGKILL,
// restart and -- with a qrdbdharness.UDPRelay in front of every node's Raft
// listener -- partition without touching the host; a write ledger that
// records every write issued and which were acknowledged; and the checks run
// once the faults are over.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

const (
	// faultMaxElectionTimeout is the longest randomized election timeout a
	// qrdbd replica on a networked host draws: go-quicraft's defaults
	// (RTTMillisecond 200, ElectionRTT 10) give a 2s base timeout, randomized
	// over [base, 2*base).
	faultMaxElectionTimeout = 4 * time.Second

	// faultElectionBound is how long a surviving majority may take to elect a
	// new leader once the old one is gone. Three maximal timeouts: one for a
	// follower's timer to fire after the last heartbeat it saw, one for a
	// split vote to be retried, and one of slack for a loaded host running the
	// race detector. Election itself is a PreVote and a vote round trip on
	// loopback, which is milliseconds.
	faultElectionBound = 3 * faultMaxElectionTimeout

	// faultRecoveryTimeout bounds every wait for a cluster to settle after a
	// fault: a restart, a heal, a lagging replica catching up.
	faultRecoveryTimeout = 90 * time.Second

	// faultWriteTimeout is the client timeout on a load write. It is longer
	// than the server's own 5s request bound, so a write the cluster cannot
	// commit is answered by the server rather than abandoned by the client.
	faultWriteTimeout = 10 * time.Second

	// faultPollInterval is the period of every condition poll.
	faultPollInterval = 50 * time.Millisecond
)

// faultNode is one qrdbd daemon of a faultCluster.
type faultNode struct {
	replicaID uint64
	adminPort int
	dataDir   string
	args      []string
	cmd       *exec.Cmd

	// listen is the node's real Raft socket. Every datagram it sends to a
	// peer leaves from this address, so it is how a relay names the node.
	listen netip.AddrPort

	// relay sits in front of listen when the cluster was started with
	// relays; the node advertises relay.Addr() as its Raft address.
	relay *qrdbdharness.UDPRelay

	// passphrase is this node's own unseal credential under
	// faultSealPassphrase; no other node holds it.
	passphrase string

	starts int
}

// faultSeal is the barrier seal strategy every node of a faultCluster runs
// under.
type faultSeal string

// The seal types the fault tests run under. go-qrdb's release candidate ships
// none, passphrase and shamir (docs/cluster-verification/architecture.md#seal-types);
// shamir is absent because a raft member that does not hold the cluster CA
// key cannot enrol its share holders, so no multi-node cluster can initialize
// a Shamir node barrier yet. Hardware-backed strategies are go-xkms's.
const (
	faultSealNone       faultSeal = "none"
	faultSealPassphrase faultSeal = "passphrase"
)

// faultSeals is every seal type the fault tests run under, in the order
// their subtests run.
var faultSeals = []faultSeal{faultSealNone, faultSealPassphrase}

// faultSealProbeKey is written once the cluster first settles, so a read of
// it through a node answers 200 exactly when that node serves data.
const faultSealProbeKey = "fault-seal-probe"

// faultCluster is a 3-node pebble cluster for fault injection.
type faultCluster struct {
	bin   string
	ca    *testCA
	nodes []*faultNode

	// seal is the strategy every node runs under.
	seal faultSeal

	// status polls /status, where a slow answer is as good as none; read
	// fetches whole key ranges, which take longer on a loaded host.
	status *http.Client
	read   *http.Client
}

// faultShard is the part of one /status shard entry the fault checks read.
type faultShard struct {
	ShardID   uint64 `json:"shard_id"`
	ReplicaID uint64 `json:"replica_id"`
	LeaderID  uint64 `json:"leader_id"`
	Term      uint64 `json:"term"`
	IsLeader  bool   `json:"is_leader"`
	Health    string `json:"health"`
}

// faultShardHalted is the health go-quicraft reports for a shard stopped by a
// fatal state machine or log failure.
const faultShardHalted = "error"

// faultShardID is the data shard every faultCluster node is a replica of.
const faultShardID = 1

// startFaultCluster boots a 3-node cluster and waits for a leader and a
// committed warmup write. With relays, every node binds its Raft listener on
// its own port and advertises a relay in front of it, so every peer reaches
// it through the relay.
func startFaultCluster(t *testing.T, withRelays bool, seal faultSeal) *faultCluster {
	t.Helper()
	const n = 3
	c := &faultCluster{bin: buildBinary(t), ca: generateTestCA(t), seal: seal}
	raftPorts := allocatePorts(t, n)
	adminPorts := allocateTCPPorts(t, n)
	grpcPorts := allocateTCPPorts(t, n)

	advertise := make([]string, n)
	for i := range n {
		listen := netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), uint16(raftPorts[i]))
		node := &faultNode{
			replicaID:  uint64(i + 1),
			adminPort:  adminPorts[i],
			dataDir:    t.TempDir(),
			listen:     listen,
			passphrase: fmt.Sprintf("fault-node-%d-own-passphrase", i+1),
		}
		advertise[i] = listen.String()
		if withRelays {
			relay, err := qrdbdharness.NewUDPRelay("127.0.0.1:0", listen.String())
			if err != nil {
				t.Fatalf("relay for node %d: %v", i+1, err)
			}
			// Registered before any node starts, so it runs after every
			// node has been killed: a relay that failed on its own while
			// the test ran cut a path nobody asked it to.
			t.Cleanup(func() {
				if err := relay.Close(); err != nil {
					t.Errorf("relay for node %d: %v", node.replicaID, err)
				}
			})
			node.relay = relay
			advertise[i] = relay.Addr()
		}
		c.nodes = append(c.nodes, node)
	}

	peers := make([]string, n)
	peerAdminURLs := make(map[string]string, n)
	certPaths := make([]string, n)
	keyPaths := make([]string, n)
	for i := range n {
		peers[i] = fmt.Sprintf("%d=%s", i+1, advertise[i])
		peerAdminURLs[advertise[i]] = fmt.Sprintf("https://127.0.0.1:%d", adminPorts[i])
		certPaths[i], keyPaths[i] = c.ca.generateNodeCertWithSPIFFE(t, advertise[i], clusterTestTrustDomain)
	}
	cfgPath := writePeerAdminURLConfig(t, peerAdminURLs)
	appendFaultSealConfig(t, cfgPath, seal)
	memberIDs := memberIdentitiesFlag(t, certPaths)

	for i, node := range c.nodes {
		node.args = []string{
			"start",
			"--config", cfgPath,
			"--listen", node.listen.String(),
			"--raft-address", advertise[i],
			"--data-dir", node.dataDir,
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", strings.Join(peers, ","),
			"--member-identities", memberIDs,
			"--shard-id", fmt.Sprintf("%d", faultShardID),
			"--replica-id", fmt.Sprintf("%d", node.replicaID),
			"--admin-port", fmt.Sprintf("%d", node.adminPort),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--tls-ca-file", c.ca.CertPath,
			"--tls-cert-file", certPaths[i],
			"--tls-key-file", keyPaths[i],
		}
		c.spawn(t, i)
	}
	c.initBarriers(t)
	for _, node := range c.nodes {
		waitForHealthy(t, node.dataDir, 60*time.Second)
		waitForAdmin(t, c.ca.Pool, node.adminPort, 90*time.Second)
	}
	c.status = mtlsHTTPClient(t, c.ca, 2*time.Second)
	c.read = mtlsHTTPClient(t, c.ca, faultWriteTimeout)
	leaderPort := waitForLeaderViaAdmin(t, c.ca, adminPorts, 60*time.Second)
	warmupWrite(t, c.ca, leaderPort, 15*time.Second)
	c.putAcked(t, c.read, 0, faultSealProbeKey, faultValue(faultSealProbeKey), faultRecoveryTimeout)
	return c
}

// appendFaultSealConfig adds the seal section for seal to the daemon
// configuration at cfgPath.
func appendFaultSealConfig(t *testing.T, cfgPath string, seal faultSeal) {
	t.Helper()
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("appendFaultSealConfig: open %s: %v", cfgPath, err)
	}
	_, err = fmt.Fprintf(f, "seal:\n  enabled: true\n  strategy: %s\n", seal)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("appendFaultSealConfig: write %s: %v", cfgPath, err)
	}
}

// initBarriers initializes and unseals every node's own barrier under the
// cluster's seal type, each node with its own credential: nothing a node
// unseals with is ever shared with another.
func (c *faultCluster) initBarriers(t *testing.T) {
	t.Helper()
	var wg sync.WaitGroup
	for _, node := range c.nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			waitForBarrierStatusReachable(t, c.ca, node.adminPort, 90*time.Second)
			switch c.seal {
			case faultSealNone:
				initAndUnsealBarrier(t, c.ca, node.adminPort, "")
			case faultSealPassphrase:
				initAndUnsealBarrier(t, c.ca, node.adminPort, node.passphrase)
			default:
				t.Errorf("node %d: unknown seal type %q", node.replicaID, c.seal)
			}
		}()
	}
	wg.Wait()
	if t.Failed() {
		t.FailNow()
	}
}

// spawn starts node i's daemon with its fixed arguments.
func (c *faultCluster) spawn(t *testing.T, i int) {
	t.Helper()
	node := c.nodes[i]
	node.starts++
	node.cmd = startNode(t, c.bin, node.args, fmt.Sprintf("[fault-node%d-run%d] ", node.replicaID, node.starts))
}

// kill SIGKILLs node i: no drain, no flush, no graceful leadership transfer.
func (c *faultCluster) kill(t *testing.T, i int) {
	t.Helper()
	node := c.nodes[i]
	killNode(t, node.cmd)
}

// restart starts node i again on its existing data directory and unseals it.
func (c *faultCluster) restart(t *testing.T, i int) {
	t.Helper()
	c.spawn(t, i)
	node := c.nodes[i]
	waitForBarrierStatusReachable(t, c.ca, node.adminPort, 90*time.Second)
	switch c.seal {
	case faultSealNone:
		// A none barrier has no credential: the node unseals itself at
		// boot, so the heal sends no unseal and only waits for it.
		waitForSelfUnsealed(t, c.ca, node.adminPort)
	case faultSealPassphrase:
		c.requireSealed(t, i)
		c.unsealPassphrase(t, i)
	default:
		t.Fatalf("node %d: unknown seal type %q", node.replicaID, c.seal)
	}
	waitForAdmin(t, c.ca.Pool, node.adminPort, 90*time.Second)
}

// requireSealed fails the test unless restarted node i reports its barrier
// sealed and refuses a read of a key the cluster holds: a node that serves
// data before it is unsealed with its own credential has bypassed its seal.
func (c *faultCluster) requireSealed(t *testing.T, i int) {
	t.Helper()
	node := c.nodes[i]
	resp, err := c.status.Get(fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/status", node.adminPort))
	if err != nil {
		t.Fatalf("node %d: barrier status after restart: %v", node.replicaID, err)
	}
	var status struct {
		Sealed bool `json:"sealed"`
	}
	err = json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("node %d: barrier status after restart: %v", node.replicaID, err)
	}
	if !status.Sealed {
		t.Fatalf("node %d came back from a restart unsealed under %s, before any credential was presented", node.replicaID, c.seal)
	}
	if resp, err := c.status.Get(fmt.Sprintf("https://127.0.0.1:%d/kv/%s", node.adminPort, faultSealProbeKey)); err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("node %d served a read while sealed under %s", node.replicaID, c.seal)
		}
	}
}

// unsealPassphrase unseals node i with its own passphrase, retrying while
// the node's sealed-window listener comes up.
func (c *faultCluster) unsealPassphrase(t *testing.T, i int) {
	t.Helper()
	node := c.nodes[i]
	body, err := json.Marshal(map[string]any{"secret": []byte(node.passphrase)})
	if err != nil {
		t.Fatalf("node %d: unseal request: %v", node.replicaID, err)
	}
	url := fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", node.adminPort)
	var lastErr error
	if !pollUntil(faultRecoveryTimeout, faultPollInterval, func() bool {
		resp, err := c.read.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			lastErr = err
			return false
		}
		reply, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusOK, http.StatusNoContent, http.StatusConflict:
			return true
		}
		lastErr = &faultStatusError{Port: node.adminPort, Code: resp.StatusCode, Body: string(reply)}
		return false
	}) {
		t.Fatalf("node %d: unseal with its own passphrase: %v", node.replicaID, lastErr)
	}
}

// others returns the index of every node other than i.
func (c *faultCluster) others(i int) []int {
	var out []int
	for j := range c.nodes {
		if j != i {
			out = append(out, j)
		}
	}
	return out
}

// shards reads node i's view of every shard it hosts.
func (c *faultCluster) shards(i int) ([]faultShard, error) {
	resp, err := c.status.Get(fmt.Sprintf("https://127.0.0.1:%d/status", c.nodes[i].adminPort))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &faultStatusError{Port: c.nodes[i].adminPort, Code: resp.StatusCode, Body: string(body)}
	}
	var status struct {
		Shards []faultShard `json:"shards"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, err
	}
	return status.Shards, nil
}

// dataShard reads node i's view of the data shard.
func (c *faultCluster) dataShard(i int) (faultShard, error) {
	shards, err := c.shards(i)
	if err != nil {
		return faultShard{}, err
	}
	for _, s := range shards {
		if s.ShardID == faultShardID {
			return s, nil
		}
	}
	return faultShard{}, &faultStatusError{Port: c.nodes[i].adminPort, Code: http.StatusOK, Body: "no data shard"}
}

// leader waits until one of among reports itself leader of the data shard in
// a term after afterTerm, and returns that node's index and term.
func (c *faultCluster) leader(t *testing.T, among []int, afterTerm uint64, timeout time.Duration) (int, uint64) {
	t.Helper()
	var last string
	leader, term := -1, uint64(0)
	if !pollUntil(timeout, faultPollInterval, func() bool {
		for _, i := range among {
			s, err := c.dataShard(i)
			if err != nil {
				last = fmt.Sprintf("node %d: %v", c.nodes[i].replicaID, err)
				continue
			}
			if s.IsLeader && s.Term > afterTerm {
				leader, term = i, s.Term
				return true
			}
			last = fmt.Sprintf("node %d: leader %d term %d", c.nodes[i].replicaID, s.LeaderID, s.Term)
		}
		return false
	}) {
		t.Fatalf("no leader in a term after %d within %v (last: %s)", afterTerm, timeout, last)
	}
	return leader, term
}

// waitSettled waits until every node reports every shard it hosts healthy
// enough to serve and every node agrees on each shard's leader, and returns
// the index of the data shard's leader. A halted shard fails the test at
// once: nothing brings it back.
func (c *faultCluster) waitSettled(t *testing.T, timeout time.Duration) int {
	t.Helper()
	var last string
	leader := -1
	if !pollUntil(timeout, faultPollInterval, func() bool {
		leaders, why := c.agreedLeaders(t)
		if why != "" {
			last = why
			return false
		}
		leader = int(leaders[faultShardID]) - 1
		return true
	}) {
		t.Fatalf("cluster did not settle within %v: %s", timeout, last)
	}
	return leader
}

// agreedLeaders reads every node's shards once. It returns each shard's
// leader when every node names the same live leader for every shard, and
// otherwise why not.
func (c *faultCluster) agreedLeaders(t *testing.T) (map[uint64]uint64, string) {
	t.Helper()
	leaders := make(map[uint64]uint64)
	for i, node := range c.nodes {
		shards, err := c.shards(i)
		if err != nil {
			return nil, fmt.Sprintf("node %d: %v", node.replicaID, err)
		}
		if len(shards) == 0 {
			return nil, fmt.Sprintf("node %d hosts no shards yet", node.replicaID)
		}
		for _, s := range shards {
			if s.Health == faultShardHalted {
				t.Fatalf("node %d: shard %d is halted", node.replicaID, s.ShardID)
			}
			if s.LeaderID == 0 {
				return nil, fmt.Sprintf("node %d: shard %d has no leader", node.replicaID, s.ShardID)
			}
			if s.IsLeader != (s.LeaderID == s.ReplicaID) {
				return nil, fmt.Sprintf("node %d: shard %d names leader %d but is_leader=%v", node.replicaID, s.ShardID, s.LeaderID, s.IsLeader)
			}
			if prev, ok := leaders[s.ShardID]; ok && prev != s.LeaderID {
				return nil, fmt.Sprintf("shard %d: nodes disagree on the leader (%d, %d)", s.ShardID, prev, s.LeaderID)
			}
			leaders[s.ShardID] = s.LeaderID
		}
	}
	if _, ok := leaders[faultShardID]; !ok {
		return nil, "no node hosts the data shard"
	}
	return leaders, ""
}

// put issues one write to node i and reports whether the cluster
// acknowledged it. Anything but 204 -- an error, a timeout, a refusal -- is
// not an acknowledgement.
func (c *faultCluster) put(client *http.Client, i int, key, value string) bool {
	url := fmt.Sprintf("https://127.0.0.1:%d/kv/%s", c.nodes[i].adminPort, key)
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(value))
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

// putAcked writes key through node i until the cluster acknowledges it.
func (c *faultCluster) putAcked(t *testing.T, client *http.Client, i int, key, value string, timeout time.Duration) {
	t.Helper()
	if !pollUntil(timeout, faultPollInterval, func() bool {
		return c.put(client, i, key, value)
	}) {
		t.Fatalf("write %s through node %d not acknowledged within %v", key, c.nodes[i].replicaID, timeout)
	}
}

// scan reads node i's own copy of every key under prefix, without a quorum
// round trip, so it shows exactly what that replica has applied.
func (c *faultCluster) scan(i int, prefix string) (map[string]string, error) {
	resp, err := c.read.Get(fmt.Sprintf("https://127.0.0.1:%d/kv/scan?prefix=%s&consistency=eventual", c.nodes[i].adminPort, prefix))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &faultStatusError{Port: c.nodes[i].adminPort, Code: resp.StatusCode, Body: string(body)}
	}
	var out struct {
		Pairs map[string][]byte `json:"pairs"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&out); err != nil {
		return nil, err
	}
	pairs := make(map[string]string, len(out.Pairs))
	for k, v := range out.Pairs {
		pairs[k] = string(v)
	}
	return pairs, nil
}

// waitConverged waits until every node holds the same keys under the
// ledger's prefix with the same values, every acknowledged write among them.
// A key the ledger never wrote, a value it never wrote, or a key it recorded
// as lost fails the test at once: no amount of catching up undoes those.
func (c *faultCluster) waitConverged(t *testing.T, l *writeLedger, timeout time.Duration) {
	t.Helper()
	var last string
	if !pollUntil(timeout, faultPollInterval, func() bool {
		last = c.convergence(t, l)
		return last == ""
	}) {
		t.Fatalf("replicas did not converge within %v: %s", timeout, last)
	}
}

// convergence reads every node's copy once and returns why the replicas have
// not converged, or "" when they have.
func (c *faultCluster) convergence(t *testing.T, l *writeLedger) string {
	t.Helper()
	var first map[string]string
	for i, node := range c.nodes {
		pairs, err := c.scan(i, l.prefix)
		if err != nil {
			return fmt.Sprintf("node %d: %v", node.replicaID, err)
		}
		for k, v := range pairs {
			if _, lost := l.lost[k]; lost {
				t.Fatalf("node %d holds %s, a write only the isolated minority accepted", node.replicaID, k)
			}
			want, attempted := l.attempted[k]
			if !attempted {
				t.Fatalf("node %d holds %s, which was never written", node.replicaID, k)
			}
			if v != want {
				t.Fatalf("node %d holds %s=%q, but the only value written was %q", node.replicaID, k, v, want)
			}
		}
		var missing []string
		for k := range l.acked {
			if _, ok := pairs[k]; !ok {
				missing = append(missing, k)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return fmt.Sprintf("node %d lacks %d acknowledged writes (first %s)", node.replicaID, len(missing), missing[0])
		}
		if first == nil {
			first = pairs
			continue
		}
		if diff := faultDiff(first, pairs); diff != "" {
			return fmt.Sprintf("node %d differs from node %d: %s", node.replicaID, c.nodes[0].replicaID, diff)
		}
	}
	return ""
}

// faultDiff names one key on which a and b differ, or returns "".
func faultDiff(a, b map[string]string) string {
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return fmt.Sprintf("%s only on the first", k)
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			return fmt.Sprintf("%s only on the second", k)
		}
	}
	return ""
}

// writeLedger is every write a test issued under one key prefix and what
// became of it.
type writeLedger struct {
	prefix string

	// attempted maps every key written to the one value it was written
	// with. A key is written at most once, so a replica holding any other
	// value has corrupted it.
	attempted map[string]string

	// acked holds the writes the cluster acknowledged. Each must be on
	// every replica once the cluster recovers.
	acked map[string]struct{}

	// lost holds writes only an isolated minority saw. Raft must discard
	// every one of them.
	lost map[string]struct{}
}

func newWriteLedger(prefix string) *writeLedger {
	return &writeLedger{
		prefix:    prefix,
		attempted: make(map[string]string),
		acked:     make(map[string]struct{}),
		lost:      make(map[string]struct{}),
	}
}

// faultValue is the one value key is ever written with.
func faultValue(key string) string { return "value-of-" + key }

// faultLoad is a set of concurrent writers spreading unique keys across
// every node until stopped. A writer keeps its own record, so writers share
// nothing but the acknowledgement count.
type faultLoad struct {
	acked   atomic.Int64
	stop    chan struct{}
	stopped sync.Once
	records chan faultLoadRecord
	writers int
}

// faultLoadRecord is what one writer issued.
type faultLoadRecord struct {
	attempted []string
	acked     []string
}

// startFaultLoad starts writers concurrent writers, writer w writing keys
// <prefix>-w<w>-<seq> to the nodes in turn. Writes to a dead node fail fast
// and are recorded like any other unacknowledged write.
func (c *faultCluster) startFaultLoad(t *testing.T, prefix string, writers int) *faultLoad {
	t.Helper()
	g := &faultLoad{
		stop:    make(chan struct{}),
		records: make(chan faultLoadRecord, writers),
		writers: writers,
	}
	client := mtlsHTTPClient(t, c.ca, faultWriteTimeout)
	ports := len(c.nodes)
	for w := range writers {
		go func() {
			var rec faultLoadRecord
			defer func() { g.records <- rec }()
			for seq := 0; ; seq++ {
				select {
				case <-g.stop:
					return
				default:
				}
				key := fmt.Sprintf("%s-w%d-%06d", prefix, w, seq)
				rec.attempted = append(rec.attempted, key)
				if c.put(client, (w+seq)%ports, key, faultValue(key)) {
					rec.acked = append(rec.acked, key)
					g.acked.Add(1)
				}
			}
		}()
	}
	t.Cleanup(func() { g.halt() })
	return g
}

func (g *faultLoad) halt() { g.stopped.Do(func() { close(g.stop) }) }

// waitAcked waits until the writers have had want writes acknowledged in all.
func (g *faultLoad) waitAcked(t *testing.T, want int64, timeout time.Duration) {
	t.Helper()
	if !pollUntil(timeout, faultPollInterval, func() bool {
		return g.acked.Load() >= want
	}) {
		t.Fatalf("load reached %d acknowledged writes within %v, want %d", g.acked.Load(), timeout, want)
	}
}

// finish stops every writer, waits for each to return its last write's
// outcome, and records them all in l.
func (g *faultLoad) finish(t *testing.T, l *writeLedger) {
	t.Helper()
	g.halt()
	deadline := time.After(2 * faultWriteTimeout)
	for range g.writers {
		select {
		case rec := <-g.records:
			for _, k := range rec.attempted {
				l.attempted[k] = faultValue(k)
			}
			for _, k := range rec.acked {
				l.acked[k] = struct{}{}
			}
		case <-deadline:
			t.Fatalf("load writers did not stop within %v", 2*faultWriteTimeout)
		}
	}
}

// isolate cuts node i off from every other node in both directions, at the
// relays in front of each Raft listener. The heal is registered with t at
// once, so a test that fails while the node is cut off does not leave the
// cluster partitioned for the tests after it; heal is idempotent, so a test
// that heals on its own path is unaffected.
func (c *faultCluster) isolate(t *testing.T, i int) {
	t.Helper()
	for _, j := range c.others(i) {
		c.nodes[i].relay.Block(c.nodes[j].listen)
		c.nodes[j].relay.Block(c.nodes[i].listen)
	}
	t.Cleanup(c.heal)
}

// heal restores every path every relay carries.
func (c *faultCluster) heal() {
	for _, node := range c.nodes {
		node.relay.Heal()
	}
}

// faultStatusError is a non-200 answer from a node's admin endpoint.
type faultStatusError struct {
	Port int
	Code int
	Body string
}

func (e *faultStatusError) Error() string {
	return fmt.Sprintf("admin port %d: status %d: %s", e.Port, e.Code, e.Body)
}
