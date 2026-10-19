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

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster/featuregate"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// rollingWriter writes one idempotent key after another through whichever
// node answers, until stopped, and records when each write was acknowledged.
// A write is retried only in the class the key-value API documents as
// retryable: a 503 UNAVAILABLE (no leader, a sealed or recycling node, a
// timed-out proposal) or a node that does not answer at all, which is retried
// against another node. A PUT of the same value to the same key is idempotent,
// so a retry of a write that may already have committed is safe. Any other
// answer fails the test.
type rollingWriter struct {
	mu    sync.Mutex
	acked []time.Time
	keys  []string
}

func (w *rollingWriter) run(ctx context.Context, t *testing.T, ca *testCA, ports []int) {
	client := mtlsHTTPClient(t, ca, 5*time.Second)
	next := 0
	for n := 0; ctx.Err() == nil; n++ {
		key := fmt.Sprintf("rolling-%05d", n)
		deadline := time.Now().Add(90 * time.Second)
		for {
			if ctx.Err() != nil {
				return
			}
			status, body, err := rollingPut(client, ports[next%len(ports)], key)
			if err == nil && status == http.StatusNoContent {
				w.mu.Lock()
				w.acked = append(w.acked, time.Now())
				w.keys = append(w.keys, key)
				w.mu.Unlock()
				break
			}
			if err == nil && status != http.StatusServiceUnavailable {
				t.Errorf("write %s on port %d: status %d outside the retryable class, body: %s", key, ports[next%len(ports)], status, body)
				return
			}
			if !time.Now().Before(deadline) {
				t.Errorf("write %s not acknowledged within 90s of retries: quorum lost (last: status %d, err %v, body %s)", key, status, err, body)
				return
			}
			next++
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// ackedSince reports how many writes were acknowledged at or after since.
func (w *rollingWriter) ackedSince(since time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, at := range w.acked {
		if !at.Before(since) {
			n++
		}
	}
	return n
}

func (w *rollingWriter) committed() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.keys...)
}

func rollingPut(client *http.Client, port int, key string) (int, string, error) {
	req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key), bytes.NewReader([]byte(key)))
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return resp.StatusCode, string(raw), nil
}

// rollingGet reads key through whichever node answers, retrying the same
// class rollingWriter retries, for up to 30 s.
func rollingGet(client *http.Client, ports []int, key string) (string, error) {
	deadline := time.Now().Add(30 * time.Second)
	for n := 0; ; n++ {
		port := ports[n%len(ports)]
		resp, err := client.Get(fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key))
		if err == nil {
			raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			switch {
			case rerr == nil && resp.StatusCode == http.StatusOK:
				return string(raw), nil
			case rerr == nil && resp.StatusCode != http.StatusServiceUnavailable:
				return string(raw), fmt.Errorf("port %d: status %d", port, resp.StatusCode)
			}
			if rerr == nil {
				rerr = fmt.Errorf("port %d: status %d", port, resp.StatusCode)
			}
			err = rerr
		}
		if !time.Now().Before(deadline) {
			return "", err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// rollingAway reports whether a node is out of service for the migration:
// its barrier is sealed, or it does not answer at all (an in-process
// reboot). A node that answers SEAL_STATE_UNCONFIRMED during an election
// still holds its key and its Raft replica, so it is not away.
func rollingAway(r barrierStatusReading) bool {
	return r.Sealed || (r.Status == 0 && r.Err != nil)
}

// TestClusterEnableEncryption_Rolling_RF3 proves the rolling system-scope
// enable-encryption of #775 on a real three-node cluster: with the
// rolling-migration feature gate activated, the owners recycle one at a time,
// so no moment has two nodes away, and writes keep committing throughout.
//
// A writer puts keys continuously through whichever node answers. A monitor
// polls every node's barrier status and fails the test the first time two
// nodes are away together. Each node, once it comes back keyed and sealed, is
// held sealed until writes have committed during its absence -- proof that
// the other two held quorum at that step -- and only then unsealed with the
// new passphrase, which is what lets its step become serving and hands the
// turn to the next owner.
//
// It fails on a coordinator that writes no steps: every owner holds its
// credential before the request, so all three recycle at once and the
// monitor sees two or three nodes away.
func TestClusterEnableEncryption_Rolling_RF3(t *testing.T) {
	cli := buildCLIBinary(t)
	c := startNoneSealSystemRF3(t, "roll")
	ca, adminPorts := c.ca, c.adminPorts

	// The gate activates once every system-shard member advertises the
	// feature; a request made before then has no steps.
	waitForFeatureActive(t, c.logs, featuregate.RollingMigration, 90*time.Second)

	// Every owner can recycle the moment the record names it: each follower
	// has its credential file, the leader holds the request's passphrase.
	// Only the step record orders them.
	const newPassphrase = "cluster-rolling-encryption-pass"
	leaderIdx := adminPortIndex(t, adminPorts, c.leaderPort)
	for i := range 3 {
		if i != leaderIdx {
			writeMigrationCredential(t, c.credDirs[i], "system", newPassphrase)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writer := &rollingWriter{}
	var bg sync.WaitGroup
	bg.Add(2)
	go func() {
		defer bg.Done()
		writer.run(ctx, t, ca, adminPorts)
	}()

	var violation string
	var violationMu sync.Mutex
	go func() {
		defer bg.Done()
		for ctx.Err() == nil {
			var away []string
			for i, port := range adminPorts {
				if r := readBarrierStatus(t, ca, port); rollingAway(r) {
					away = append(away, fmt.Sprintf("node %d (%s)", i+1, r))
				}
			}
			if len(away) > 1 {
				violationMu.Lock()
				if violation == "" {
					violation = strings.Join(away, ", ")
				}
				violationMu.Unlock()
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()

	// Let the writer commit before the migration starts.
	waitForAcked(t, writer, time.Now().Add(-time.Hour), 5, 30*time.Second, "before the migration")

	client := mtlsHTTPClient(t, ca, 15*time.Second)
	body, status := iraftAPIPost(t, client,
		fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/enable-encryption", c.leaderPort),
		map[string]interface{}{"strategy": "passphrase", "passphrase": []byte(newPassphrase)},
	)
	if status != http.StatusOK && status != http.StatusAccepted {
		t.Fatalf("enable-encryption: status %d, body: %s", status, body)
	}

	// Each owner in turn: wait for it to come back sealed, hold it there until
	// writes commit without it and `qrdb barrier status` on the other two
	// shows its step, then unseal it. Owners recycle one at a time, so the
	// k-th node to come back sealed sees k-1 steps serving.
	var steps sync.WaitGroup
	var turn atomic.Int32
	var recycledMu sync.Mutex
	recycled := map[string]int{}
	steps.Add(3)
	for i := range 3 {
		go func(idx int) {
			defer steps.Done()
			sealedAt, ok := waitForSealed(t, ca, adminPorts[idx], 420*time.Second)
			if !ok {
				t.Errorf("node %d never came back sealed from its recycle", idx+1)
				return
			}
			k := int(turn.Add(1))
			t.Logf("node %d recycled and sealed (turn %d); holding it until writes commit without it", idx+1, k)
			waitForAcked(t, writer, sealedAt, 3, 90*time.Second, fmt.Sprintf("while node %d was away", idx+1))
			if id := assertRollingStep(t, cli, c, idx, k); id != "" {
				recycledMu.Lock()
				if prev, dup := recycled[id]; dup {
					t.Errorf("owner %s recycling at turn %d and again at turn %d", id, prev, k)
				}
				recycled[id] = k
				recycledMu.Unlock()
			}
			unsealRolling(t, client, adminPorts[idx], newPassphrase)
		}(i)
	}
	steps.Wait()

	for i, dir := range c.dataDirs {
		waitForHealthy(t, dir, 90*time.Second)
		waitForAdmin(t, ca.Pool, adminPorts[i], 90*time.Second)
	}
	for i := range 3 {
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[i], 30*time.Second); got.Sealed || got.Strategy != "passphrase" {
			t.Errorf("node %d post-migration barrier: %s, want unsealed passphrase", i+1, got)
		}
	}
	// Once every owner acked, the system leader reaps the completed record
	// (Coordinator.Reap), so every node, over every transport, comes to
	// report no migration. The hand-on log below proves each owner took its
	// turn.
	for _, node := range c.nodes {
		for _, proto := range multiProtocols {
			waitForCLIMigrationReaped(t, cli, c, node, proto, 90*time.Second)
		}
	}

	// Each turn handed on only once its owner's Raft log caught up: the
	// coordinator logged one hand-on per owner, each with the commit index
	// the owner had to reach, and the log only grows from turn to turn.
	handOns := waitForHandOns(t, c.logs, 3, 60*time.Second)
	slices.SortFunc(handOns, func(a, b handOn) int { return recycled[a.node] - recycled[b.node] })
	var prev uint64
	for _, h := range handOns {
		k, ok := recycled[h.node]
		switch {
		case !ok:
			t.Errorf("hand-on for %s, which never took a turn (turns: %v)", h.node, recycled)
		case h.catchUpIndex == 0:
			t.Errorf("turn %d (%s) handed on without a catch-up index", k, h.node)
		case h.catchUpIndex <= prev:
			t.Errorf("turn %d (%s) caught up to %d, not past the previous turn's %d", k, h.node, h.catchUpIndex, prev)
		}
		prev = h.catchUpIndex
	}

	waitForAcked(t, writer, time.Now(), 3, 60*time.Second, "after the migration")
	cancel()
	bg.Wait()

	violationMu.Lock()
	if violation != "" {
		t.Errorf("two owners away at once: %s", violation)
	}
	violationMu.Unlock()

	committed := writer.committed()
	reader := mtlsHTTPClient(t, ca, 5*time.Second)
	for _, key := range committed {
		if got, err := rollingGet(reader, adminPorts, key); err != nil || got != key {
			t.Fatalf("acknowledged write %s = %q (err %v) after the migration (DATA LOSS)", key, got, err)
		}
	}
	if !t.Failed() {
		t.Logf("rolling enable-encryption: %d writes committed throughout, never two owners away", len(committed))
	}
}

// assertRollingStep checks, while node idx is held sealed at turn k, that
// `qrdb barrier status` on each other node over every transport shows the
// rolling migration with k-1 steps serving, one recycling and the rest
// pending, all naming the same owner recycling. It returns that owner.
func assertRollingStep(t *testing.T, cli string, c *noneSealRF3, idx, k int) string {
	t.Helper()
	recycling := ""
	for j, node := range c.nodes {
		if j == idx {
			continue
		}
		for _, proto := range multiProtocols {
			m := waitForCLIMigration(t, cli, c, node, proto, 60*time.Second,
				fmt.Sprintf("at turn %d (%d serving, 1 recycling, %d pending)", k, k-1, 3-k),
				func(m *transport.ClusterMigration) bool {
					serving, inTurn, pending, _ := rollingStepCounts(m)
					return m.Target == "passphrase" && m.Total == 3 && len(m.Nodes) == 3 &&
						serving == k-1 && inTurn == 1 && pending == 3-k
				})
			if m == nil {
				continue
			}
			_, _, _, id := rollingStepCounts(m)
			if recycling == "" {
				recycling = id
			} else if id != recycling {
				t.Errorf("[%s] node %d names %s recycling at turn %d, another answer named %s", proto.name, j+1, id, k, recycling)
			}
		}
	}
	return recycling
}

// rollingStepCounts counts a migration's steps by state, and names the owner
// recycling.
func rollingStepCounts(m *transport.ClusterMigration) (serving, recycling, pending int, recyclingID string) {
	for _, n := range m.Nodes {
		switch n.Step {
		case "serving":
			serving++
		case "recycling":
			recycling++
			recyclingID = n.NodeID
		case "pending":
			pending++
		}
	}
	return serving, recycling, pending, recyclingID
}

// cliClusterMigration reads the node barrier's cluster migration through
// `qrdb --output json barrier status` against node over proto: nil when the
// status carries none.
func cliClusterMigration(t *testing.T, cli string, c *noneSealRF3, node gatedClusterNode, proto multiProtocol) (*transport.ClusterMigration, error) {
	t.Helper()
	out, err := runQRDB(t, cli, node.cliArgsFor(c.ca, c.adminCert, c.adminKey, proto), "--output", "json", "barrier", "status")
	if err != nil {
		return nil, fmt.Errorf("qrdb barrier status: %w: %s", err, out)
	}
	var status struct {
		ClusterMigration *transport.ClusterMigration `json:"cluster_migration"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		return nil, fmt.Errorf("decode `qrdb barrier status` JSON: %w: %s", err, out)
	}
	return status.ClusterMigration, nil
}

// waitForCLIMigration polls cliClusterMigration until it answers a confirmed
// migration that satisfies ok, and returns it. A failed command or an
// unconfirmed answer -- an election in progress -- is retried; at the timeout
// the test fails with the last answer and nil is returned.
func waitForCLIMigration(t *testing.T, cli string, c *noneSealRF3, node gatedClusterNode, proto multiProtocol, timeout time.Duration, want string, ok func(*transport.ClusterMigration) bool) *transport.ClusterMigration {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		m, err := cliClusterMigration(t, cli, c, node, proto)
		if err == nil && m != nil && !m.Unconfirmed && ok(m) {
			return m
		}
		if !time.Now().Before(deadline) {
			t.Errorf("[%s] admin port %d: barrier status migration never %s within %v (last: %+v, err: %v)", proto.name, node.admin, want, timeout, m, err)
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// waitForCLIMigrationReaped polls `qrdb barrier status` on one node over one
// transport until it definitively reports no cluster migration: the read
// succeeded and carries no record. An unconfirmed read is not an answer.
func waitForCLIMigrationReaped(t *testing.T, cli string, c *noneSealRF3, node gatedClusterNode, proto multiProtocol, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		m, err := cliClusterMigration(t, cli, c, node, proto)
		if err == nil && m == nil {
			return
		}
		if !time.Now().Before(deadline) {
			t.Errorf("[%s] admin port %d: barrier status migration never reaped within %v (last: %+v, err: %v)", proto.name, node.admin, timeout, m, err)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// waitForFeatureActive polls every node's log until each has activated
// feature f cluster-wide or observed its activation.
func waitForFeatureActive(t *testing.T, logs []*nodeLog, f featuregate.Feature, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for i, l := range logs {
		for !featureActiveIn(l.String(), f) {
			if !time.Now().Before(deadline) {
				t.Fatalf("node %d never logged the activation of %s within %v", i+1, f, timeout)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
}

// featureActiveIn reports whether a node log holds the gate's activation line
// -- "feature activated cluster-wide" or "feature activation observed" -- for
// feature f.
func featureActiveIn(log string, f featuregate.Feature) bool {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "feature gate: feature activat") && strings.Contains(line, string(f)) {
			return true
		}
	}
	return false
}

// handOn is one "cluster-migration step handed on" line: the owner whose turn
// ended and the commit index its Raft log had to reach first.
type handOn struct {
	node         string
	catchUpIndex uint64
}

// handOnNodeRE and handOnIndexRE read a hand-on line's attributes in slog's
// text form (node=a:1) or its JSON form ("node":"a:1").
var (
	handOnNodeRE  = regexp.MustCompile(`(?:^|[\s{,])"?node"?[=:]"?([^"\s,}]+)`)
	handOnIndexRE = regexp.MustCompile(`(?:^|[\s{,])"?catch_up_index"?[=:](\d+)`)
)

// handOnsIn returns every hand-on line in logs, in no particular order.
func handOnsIn(logs []*nodeLog) []handOn {
	var got []handOn
	for _, l := range logs {
		for _, line := range strings.Split(l.String(), "\n") {
			if !strings.Contains(line, "cluster-migration step handed on") {
				continue
			}
			h := handOn{}
			if m := handOnNodeRE.FindStringSubmatch(line); m != nil {
				h.node = m[1]
			}
			if m := handOnIndexRE.FindStringSubmatch(line); m != nil {
				h.catchUpIndex, _ = strconv.ParseUint(m[1], 10, 64)
			}
			got = append(got, h)
		}
	}
	return got
}

// waitForHandOns polls the node logs until they hold want hand-on lines and
// returns them; more than want, or fewer by the deadline, fails the test.
func waitForHandOns(t *testing.T, logs []*nodeLog, want int, timeout time.Duration) []handOn {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		got := handOnsIn(logs)
		if len(got) > want {
			t.Fatalf("the coordinators logged %d hand-ons, want %d: %+v", len(got), want, got)
		}
		if len(got) == want {
			return got
		}
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatalf("the coordinators logged %d hand-ons within %v, want %d: %+v", len(got), timeout, want, got)
		}
	}
}

// waitForSealed polls port until its barrier reports sealed, and returns when.
func waitForSealed(t *testing.T, ca *testCA, port int, timeout time.Duration) (time.Time, bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if r := readBarrierStatus(t, ca, port); r.Sealed {
			return time.Now(), true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return time.Time{}, false
}

// waitForAcked waits until want writes have been acknowledged since since.
func waitForAcked(t *testing.T, w *rollingWriter, since time.Time, want int, timeout time.Duration, when string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for w.ackedSince(since) < want {
		if !time.Now().Before(deadline) {
			t.Errorf("only %d of %d writes committed %s within %v: quorum not held", w.ackedSince(since), want, when, timeout)
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// unsealRolling unseals a recycled node with the new passphrase, retrying the
// documented retryable refusals until the node takes it.
func unsealRolling(t *testing.T, client *http.Client, port int, passphrase string) {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{"secret": []byte(passphrase)})
	if err != nil {
		t.Errorf("unseal payload: %v", err)
		return
	}
	deadline := time.Now().Add(90 * time.Second)
	for {
		status, body := 0, ""
		resp, err := client.Post(fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", port), "application/json", bytes.NewReader(payload))
		if err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			status, body = resp.StatusCode, string(raw)
		}
		if status == http.StatusNoContent || status == http.StatusConflict {
			return
		}
		if (err == nil && status != http.StatusServiceUnavailable) || !time.Now().Before(deadline) {
			t.Errorf("unseal on port %d: status %d, err %v, body: %s", port, status, err, body)
			return
		}
		time.Sleep(time.Second)
	}
}
