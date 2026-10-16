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

package integration_test

// Cluster sealing across real 3-node qrdbd clusters (docs/barrier-seal):
//
//   - TestClusterSeal_NodeBarrier_RF3: a node-barrier seal through one node
//     reaches every node; each node restarts and unseals with its OWN
//     passphrase -- the three passphrases differ, so no key or credential can
//     have moved between them -- and comes back held until one cluster-unseal
//     clears the record; then every node serves the data it had.
//   - TestClusterSeal_CutOffNodeStopsServing_RF3: a node that cannot read the
//     seal records for the lease stops serving, keeps its keys, and resumes
//     without an unseal once it can read them again.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// clusterSealView is the cluster_seal object of GET /v1/barrier/status.
type clusterSealView struct {
	Active bool   `json:"active"`
	Sealed bool   `json:"sealed"`
	Epoch  uint64 `json:"epoch"`
	Local  string `json:"local"`
}

// clusterSealNode is one daemon of a cluster-seal test cluster.
type clusterSealNode struct {
	args       []string
	cmd        *exec.Cmd
	adminPort  int
	dataDir    string
	passphrase string
}

// sealClusterHarness is a 3-node cluster whose nodes each have their own
// passphrase.
type sealClusterHarness struct {
	t     *testing.T
	bin   string
	ca    *testCA
	nodes []*clusterSealNode
}

// writeClusterSealConfig writes a passphrase-barrier config with the given
// cluster seal timing.
func writeClusterSealConfig(t *testing.T, lease, poll, ack string) string {
	t.Helper()
	yaml := fmt.Sprintf(`seal:
  enabled: true
  strategy: passphrase
  cluster_lease: %s
  cluster_poll_interval: %s
  cluster_ack_timeout: %s
`, lease, poll, ack)
	p := filepath.Join(t.TempDir(), "qrdbd.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o600); err != nil {
		t.Fatalf("writeClusterSealConfig: %v", err)
	}
	return p
}

// startSealCluster starts three nodes, initializes each node's barrier with
// its own passphrase, and waits for a healthy cluster with a leader.
func startSealCluster(t *testing.T, lease, poll, ack string) *sealClusterHarness {
	t.Helper()
	h := &sealClusterHarness{t: t, bin: buildBinary(t), ca: generateTestCA(t)}
	raftPorts := allocatePorts(t, 3)
	adminPorts := allocateTCPPorts(t, 3)
	grpcPorts := allocateTCPPorts(t, 3)
	peersFlag := buildPeersFlag(raftPorts)
	certs, keys := make([]string, 3), make([]string, 3)
	for i := range 3 {
		certs[i], keys[i] = h.ca.generateNodeCertWithSPIFFE(t, fmt.Sprintf("127.0.0.1:%d", raftPorts[i]), iraftTrustDomain)
	}
	for i := range 3 {
		n := &clusterSealNode{
			adminPort:  adminPorts[i],
			dataDir:    t.TempDir(),
			passphrase: fmt.Sprintf("node-%d-own-passphrase", i+1),
		}
		n.args = []string{
			"start",
			"--config", writeClusterSealConfig(t, lease, poll, ack),
			"--listen", fmt.Sprintf("0.0.0.0:%d", raftPorts[i]),
			"--raft-address", fmt.Sprintf("127.0.0.1:%d", raftPorts[i]),
			"--data-dir", n.dataDir,
			"--sm", "pebble",
			"--discovery", "static",
			"--peers", peersFlag,
			"--member-identities", memberIdentitiesFlag(t, certs),
			"--shard-id", "1",
			"--replica-id", fmt.Sprintf("%d", i+1),
			"--admin-port", fmt.Sprintf("%d", adminPorts[i]),
			"--grpc-port", fmt.Sprintf("%d", grpcPorts[i]),
			"--cluster-trust-domain", iraftTrustDomain,
			"--tls-ca-file", h.ca.CertPath,
			"--tls-cert-file", certs[i],
			"--tls-key-file", keys[i],
		}
		h.nodes = append(h.nodes, n)
	}
	for i := range h.nodes {
		h.start(i)
	}
	var wg sync.WaitGroup
	for _, n := range h.nodes {
		wg.Add(1)
		go func(n *clusterSealNode) {
			defer wg.Done()
			waitForBarrierStatusReachable(t, h.ca, n.adminPort, 90*time.Second)
			initAndUnsealBarrier(t, h.ca, n.adminPort, n.passphrase)
		}(n)
	}
	wg.Wait()
	for _, n := range h.nodes {
		waitForHealthy(t, n.dataDir, 60*time.Second)
		waitForAdmin(t, h.ca.Pool, n.adminPort, 90*time.Second)
	}
	warmupWrite(t, h.ca, waitForLeaderViaAdmin(t, h.ca, h.ports(), 60*time.Second), 15*time.Second)
	return h
}

func (h *sealClusterHarness) ports() []int {
	ports := make([]int, len(h.nodes))
	for i, n := range h.nodes {
		ports[i] = n.adminPort
	}
	return ports
}

func (h *sealClusterHarness) start(i int) {
	h.nodes[i].cmd = startNode(h.t, h.bin, h.nodes[i].args, fmt.Sprintf("[cseal-node%d] ", i+1))
}

func (h *sealClusterHarness) stop(i int) {
	if cmd := h.nodes[i].cmd; cmd != nil {
		killNode(h.t, cmd)
	}
}

// status reads node i's barrier status: whether its node barrier is sealed,
// and its cluster_seal object. A node that is not serving serves only the
// unseal call. Sealed or held by the cluster record, it refuses status with
// the typed sealed refusal; that reads as sealed with a zero cluster_seal,
// since the refusal names no reason. Cut off, or not yet confirmed, it refuses
// status as SEAL_STATE_UNCONFIRMED, which is not sealed and reads as not ok
// here; waitForRefusal waits for it.
func (h *sealClusterHarness) status(i int) (sealed bool, cs clusterSealView, ok bool) {
	client := mtlsHTTPClient(h.t, h.ca, 5*time.Second)
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/status", h.nodes[i].adminPort))
	if err != nil {
		return false, cs, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return false, cs, false
	}
	if qrdbdharness.IsSealedRefusal(resp.StatusCode, raw) {
		return true, cs, true
	}
	if resp.StatusCode != http.StatusOK {
		return false, cs, false
	}
	var body struct {
		Sealed      bool             `json:"sealed"`
		ClusterSeal *clusterSealView `json:"cluster_seal"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.ClusterSeal == nil {
		return false, cs, false
	}
	return body.Sealed, *body.ClusterSeal, true
}

// waitFor polls cond on node i until it holds, failing with what after timeout.
func (h *sealClusterHarness) waitFor(i int, timeout time.Duration, what string, cond func(sealed bool, cs clusterSealView) bool) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last clusterSealView
	var lastSealed, seen bool
	for time.Now().Before(deadline) {
		if sealed, cs, ok := h.status(i); ok {
			last, lastSealed, seen = cs, sealed, true
			if cond(sealed, cs) {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	h.t.Fatalf("node %d: timed out after %v waiting for %s (last: reachable=%v sealed=%v cluster_seal=%+v)",
		i+1, timeout, what, seen, lastSealed, last)
}

// refusalCode returns the code of node i's 503 refusal of barrier status, or
// "" when it answers anything else or cannot be reached.
func (h *sealClusterHarness) refusalCode(i int) string {
	client := mtlsHTTPClient(h.t, h.ca, 5*time.Second)
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/status", h.nodes[i].adminPort))
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		Code string `json:"code"`
	}
	if resp.StatusCode != http.StatusServiceUnavailable || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body) != nil {
		return ""
	}
	return body.Code
}

// waitForRefusal waits until node i refuses barrier status with code.
func (h *sealClusterHarness) waitForRefusal(i int, timeout time.Duration, what, code string) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	var last string
	for time.Now().Before(deadline) {
		if last = h.refusalCode(i); last == code {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	h.t.Fatalf("node %d: timed out after %v waiting for %s (want refusal %s, last %q)", i+1, timeout, what, code, last)
}

// kvStatus returns the status of a KV read on node i, or 0 when the node did
// not answer.
func (h *sealClusterHarness) kvStatus(i int, key string) int {
	client := mtlsHTTPClient(h.t, h.ca, 5*time.Second)
	resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/kv/%s", h.nodes[i].adminPort, key))
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

// post sends a JSON POST to node i and returns the status and body.
func (h *sealClusterHarness) post(i int, path string, body any) (int, []byte) {
	h.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		h.t.Fatal(err)
	}
	client := mtlsHTTPClient(h.t, h.ca, 30*time.Second)
	resp, err := client.Post(fmt.Sprintf("https://127.0.0.1:%d%s", h.nodes[i].adminPort, path), "application/json", bytes.NewReader(raw))
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// unsealOwn unseals node i's barrier with its own passphrase, retrying while
// the boot listener comes up.
func (h *sealClusterHarness) unsealOwn(i int) {
	h.t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for {
		st, body := h.post(i, "/v1/barrier/unseal", map[string]any{"secret": []byte(h.nodes[i].passphrase)})
		if st == http.StatusNoContent || st == http.StatusOK || st == http.StatusConflict {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("node %d unseal with its own passphrase: status %d, body %s", i+1, st, body)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func TestClusterSeal_NodeBarrier_RF3(t *testing.T) {
	h := startSealCluster(t, "15s", "1s", "5s")
	leaderIdx := adminPortIndex(t, h.ports(), waitForLeaderViaAdmin(t, h.ca, h.ports(), 60*time.Second))
	httpPut(t, h.ca, h.nodes[leaderIdx].adminPort, "sealed-key", []byte("survives"))

	for i := range h.nodes {
		h.waitFor(i, 60*time.Second, "cluster-seal active", func(sealed bool, cs clusterSealView) bool {
			return cs.Active && !cs.Sealed && cs.Local == "serving"
		})
	}

	// Seal through a follower: any node may receive the seal. Once qrdb
	// init provisions an SO PIN (initAndUnsealBarrier, above), the SO
	// gate refuses a seal without it.
	via := (leaderIdx + 1) % 3
	if st, body := h.post(via, "/v1/barrier/seal", map[string]any{"so_pin": harnessSOPIN}); st != http.StatusNoContent && st != http.StatusOK {
		t.Fatalf("seal via node %d: status %d, body %s", via+1, st, body)
	}
	t.Logf("node barrier sealed through node %d", via+1)

	// Every node seals itself and refuses data requests.
	for i := range h.nodes {
		h.waitFor(i, 30*time.Second, "node barrier sealed", func(sealed bool, _ clusterSealView) bool { return sealed })
		if st := h.kvStatus(i, "sealed-key"); st == http.StatusOK {
			t.Fatalf("node %d served a read after the cluster seal", i+1)
		}
	}

	// Restart every node; each unseals with its OWN passphrase and comes back
	// held: keys loaded, requests refused, until the record is cleared. A
	// held node serves only the unseal call, so once its own unseal has
	// succeeded the typed sealed refusal of status is what "held" looks like.
	for i := range h.nodes {
		h.stop(i)
	}
	for i := range h.nodes {
		h.start(i)
	}
	var wg sync.WaitGroup
	for i := range h.nodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			waitForBarrierStatusReachable(t, h.ca, h.nodes[i].adminPort, 90*time.Second)
			h.unsealOwn(i)
		}(i)
	}
	wg.Wait()
	for i := range h.nodes {
		h.waitFor(i, 90*time.Second, "held after restart", func(sealed bool, _ clusterSealView) bool { return sealed })
		if st := h.kvStatus(i, "sealed-key"); st == http.StatusOK {
			t.Fatalf("node %d served a read while held", i+1)
		}
	}

	// Another node's passphrase does not unseal this one: the keys are each
	// node's own. (Checked on a fresh restart of node 1, sealed at boot.)
	h.stop(0)
	h.start(0)
	waitForBarrierStatusReachable(t, h.ca, h.nodes[0].adminPort, 90*time.Second)
	if st, _ := h.post(0, "/v1/barrier/unseal", map[string]any{"secret": []byte(h.nodes[1].passphrase)}); st == http.StatusNoContent || st == http.StatusOK {
		t.Fatal("node 1 unsealed with node 2's passphrase")
	}
	h.unsealOwn(0)
	h.waitFor(0, 90*time.Second, "held after restart", func(sealed bool, _ clusterSealView) bool { return sealed })
	if st := h.kvStatus(0, "sealed-key"); st == http.StatusOK {
		t.Fatal("node 1 served a read while held")
	}

	// One cluster-unseal, from any node, clears the record; every node serves.
	st, body := h.post(2, "/v1/barrier/cluster-unseal", map[string]any{})
	if st != http.StatusOK {
		t.Fatalf("cluster-unseal: status %d, body %s", st, body)
	}
	var cleared clusterSealView
	if err := json.Unmarshal(body, &cleared); err != nil || cleared.Sealed {
		t.Fatalf("cluster-unseal answered %s (%v), want a clear record", body, err)
	}
	for i := range h.nodes {
		h.waitFor(i, 30*time.Second, "serving after cluster-unseal", func(sealed bool, cs clusterSealView) bool {
			return !sealed && !cs.Sealed && cs.Local == "serving"
		})
	}
	leaderPort := waitForLeaderViaAdmin(t, h.ca, h.ports(), 60*time.Second)
	if got := string(httpGet(t, h.ca, leaderPort, "sealed-key")); got != "survives" {
		t.Fatalf("after cluster-unseal sealed-key = %q, want survives", got)
	}
}

func TestClusterSeal_CutOffNodeStopsServing_RF3(t *testing.T) {
	h := startSealCluster(t, "6s", "1s", "3s")
	leaderIdx := adminPortIndex(t, h.ports(), waitForLeaderViaAdmin(t, h.ca, h.ports(), 60*time.Second))
	httpPut(t, h.ca, h.nodes[leaderIdx].adminPort, "cutoff-key", []byte("kept"))
	for i := range h.nodes {
		h.waitFor(i, 60*time.Second, "cluster-seal active", func(_ bool, cs clusterSealView) bool {
			return cs.Active && cs.Local == "serving"
		})
	}

	// Cut node 1 off: the other two stop, so it cannot read the records.
	h.stop(1)
	h.stop(2)
	// A cut-off node serves only the unseal call. Its own barrier never
	// sealed, so it refuses status as unconfirmed, not as sealed.
	h.waitForRefusal(0, 30*time.Second, "cut off after the lease", replicaforward.ReasonSealStateUnconfirmed)
	if st := h.kvStatus(0, "cutoff-key"); st == http.StatusOK {
		t.Fatal("a cut-off node served a read")
	}

	// The others come back (each unsealing with its own passphrase); node 1
	// resumes on its own -- it never sealed and is not unsealed again.
	h.start(1)
	h.start(2)
	for _, i := range []int{1, 2} {
		waitForBarrierStatusReachable(t, h.ca, h.nodes[i].adminPort, 90*time.Second)
		h.unsealOwn(i)
	}
	h.waitFor(0, 90*time.Second, "serving again", func(sealed bool, cs clusterSealView) bool {
		return !sealed && cs.Local == "serving"
	})
	deadline := time.Now().Add(60 * time.Second)
	for h.kvStatus(0, "cutoff-key") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("node 1 did not serve again after reconnecting")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestClusterSeal_NodeUnsealedAheadOfItsPeersWaits_RF3 restarts the whole
// cluster and unseals one node well before the others, as an operator
// unsealing node by node does. That node has no quorum until a peer is
// unsealed too, and it waits for the system shard's leader for as long as it
// takes rather than giving up and exiting.
func TestClusterSeal_NodeUnsealedAheadOfItsPeersWaits_RF3(t *testing.T) {
	h := startSealCluster(t, "6s", "1s", "3s")
	httpPut(t, h.ca, waitForLeaderViaAdmin(t, h.ca, h.ports(), 60*time.Second), "ahead-key", []byte("kept"))
	for i := range h.nodes {
		h.stop(i)
	}
	for i := range h.nodes {
		h.start(i)
	}

	waitForBarrierStatusReachable(t, h.ca, h.nodes[0].adminPort, 90*time.Second)
	h.unsealOwn(0)
	// The operator's pace, not a condition to poll for: the peers stay sealed
	// for longer than a boot that gave up on its leader would have waited.
	time.Sleep(20 * time.Second)
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", h.nodes[0].adminPort), 5*time.Second)
	if err != nil {
		t.Fatalf("node 1, unsealed ahead of its peers, stopped listening: %v", err)
	}
	_ = conn.Close()

	for _, i := range []int{1, 2} {
		waitForBarrierStatusReachable(t, h.ca, h.nodes[i].adminPort, 90*time.Second)
		h.unsealOwn(i)
	}
	for _, n := range h.nodes {
		waitForAdmin(t, h.ca.Pool, n.adminPort, 90*time.Second)
	}
	deadline := time.Now().Add(60 * time.Second)
	for h.kvStatus(0, "ahead-key") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatal("node 1 did not serve the pre-restart write once its peers were unsealed")
		}
		time.Sleep(250 * time.Millisecond)
	}
}
