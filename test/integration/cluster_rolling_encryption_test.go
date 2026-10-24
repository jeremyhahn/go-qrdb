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
		var (
			status int
			body   string
			err    error
		)
		pollUntil(90*time.Second, 200*time.Millisecond, func() bool {
			if ctx.Err() != nil {
				return true
			}
			status, body, err = rollingPut(client, ports[next%len(ports)], key)
			// A write acknowledged, or refused outside the retryable class,
			// ends the retries; the switch below tells the two apart.
			if err == nil && status != http.StatusServiceUnavailable {
				return true
			}
			next++
			return false
		})
		switch {
		case err == nil && status == http.StatusNoContent:
			w.mu.Lock()
			w.acked = append(w.acked, time.Now())
			w.keys = append(w.keys, key)
			w.mu.Unlock()
		case ctx.Err() != nil:
			return
		case err == nil && status != http.StatusServiceUnavailable:
			t.Errorf("write %s on port %d: status %d outside the retryable class, body: %s", key, ports[next%len(ports)], status, body)
			return
		default:
			t.Errorf("write %s not acknowledged within 90s of retries: quorum lost (last: status %d, err %v, body %s)", key, status, err, body)
			return
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
	var (
		value string
		err   error
	)
	n := 0
	pollUntil(30*time.Second, 200*time.Millisecond, func() bool {
		port := ports[n%len(ports)]
		n++
		resp, gerr := client.Get(fmt.Sprintf("https://127.0.0.1:%d/kv/%s", port, key))
		if gerr != nil {
			value, err = "", gerr
			return false
		}
		raw, rerr := io.ReadAll(io.LimitReader(resp.Body, 1024))
		resp.Body.Close()
		switch {
		case rerr == nil && resp.StatusCode == http.StatusOK:
			value, err = string(raw), nil
			return true
		case rerr == nil && resp.StatusCode != http.StatusServiceUnavailable:
			value, err = string(raw), fmt.Errorf("port %d: status %d", port, resp.StatusCode)
			return true
		}
		if rerr == nil {
			rerr = fmt.Errorf("port %d: status %d", port, resp.StatusCode)
		}
		value, err = "", rerr
		return false
	})
	return value, err
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
// While each node is held sealed, `qrdb barrier status` on the other two,
// over every transport, shows the migration's steps: the earlier turns
// serving, this one recycling, the rest pending. Once the last owner -- the
// node that accepted the request, which recycles with the credential it held
// and has no file -- acknowledges, the record is reaped and every node, over
// every transport, reports no migration in progress.
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
		// The sampler runs until the migration is over (cancel), so it
		// ticks rather than polling toward a condition.
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
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
			select {
			case <-tick.C:
			case <-ctx.Done():
			}
		}
	}()

	// Let the writer commit before the migration starts.
	waitForAcked(t, writer, time.Now().Add(-time.Hour), 5, 30*time.Second, "before the migration")

	client := mtlsHTTPClient(t, ca, 15*time.Second)
	if out, err := cliEnableEncryption(t, cli, c, leaderIdx, newPassphrase); err != nil {
		t.Fatalf("%v: %s", err, out)
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
	// Three distinct owners took the three turns, and once the last of them
	// acknowledged the leader reaped the record: every node, over every
	// transport, reports no migration in progress.
	if len(recycled) != 3 {
		t.Errorf("owners recycling across the three turns: %v, want three distinct", recycled)
	}
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

// TestClusterEnableEncryption_Rolling_RF3_RetryAfterLeaderChange retries the
// request on a new leader while the owner whose turn it is has not yet
// recycled. The new leader is that owner, the one leader whose rebuilt order
// never puts it first whatever the owner order, so a record built from
// scratch always authorizes a second owner: two of three away. The
// superseding generation instead keeps the owner in turn first and alone
// recycling, and no other owner awaits a credential until it is back.
func TestClusterEnableEncryption_Rolling_RF3_RetryAfterLeaderChange(t *testing.T) {
	cli := buildCLIBinary(t)
	c := startNoneSealSystemRF3(t, "rollre")
	ca, adminPorts := c.ca, c.adminPorts
	waitForFeatureActive(t, c.logs, featuregate.RollingMigration, 90*time.Second)

	// No node has a credential file, so the first owner in turn holds it,
	// authorized and not yet recycled, until the test hands it one.
	const newPassphrase = "cluster-rolling-retry-pass"
	client := mtlsHTTPClient(t, ca, 15*time.Second)
	request := func(idx int) {
		t.Helper()
		var out string
		var err error
		if !pollUntil(60*time.Second, 250*time.Millisecond, func() bool {
			out, err = cliEnableEncryption(t, cli, c, idx, newPassphrase)
			return err == nil
		}) {
			t.Fatalf("%v: %s", err, out)
		}
	}
	leaderIdx := adminPortIndex(t, adminPorts, c.leaderPort)
	request(leaderIdx)

	inTurn := -1
	if !pollUntil(120*time.Second, 250*time.Millisecond, func() bool {
		for i, l := range c.logs {
			if awaitingCredentialIn(l.String(), "scope=system") {
				inTurn = i
				return true
			}
		}
		return false
	}) {
		t.Fatal("no owner awaited its credential within 120s")
	}
	first := waitForCLIMigration(t, cli, c, c.nodes[leaderIdx], multiProtocols[0], 60*time.Second, "naming the owner in turn",
		func(m *transport.ClusterMigration) bool {
			_, recycling, _, _ := rollingStepCounts(m)
			return recycling == 1
		})
	if first == nil {
		t.FailNow()
	}
	_, _, _, owner := rollingStepCounts(first)

	// Leadership moves to the owner in turn, and the client retries there.
	// Rebuilt from scratch, its record would order every other owner ahead
	// of itself and authorize one of them while it still holds the turn.
	out, err := runQRDB(t, cli, c.nodes[leaderIdx].cliArgsFor(ca, c.adminCert, c.adminKey, multiProtocols[0]),
		"admin", "member", "transfer-leadership", strconv.Itoa(inTurn+1), "--shard-id", "1")
	if err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("qrdb admin member transfer-leadership %d: %v: %s", inTurn+1, err, out)
	}
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[inTurn]}, 60*time.Second)
	request(inTurn)

	// Every node, over every transport: the superseding generation keeps the
	// owner in turn first and alone recycling.
	for _, node := range c.nodes {
		for _, proto := range multiProtocols {
			m := waitForCLIMigration(t, cli, c, node, proto, 60*time.Second,
				fmt.Sprintf("superseding generation %d", first.Generation),
				func(m *transport.ClusterMigration) bool { return m.Generation > first.Generation })
			if m == nil {
				continue
			}
			serving, recycling, pending, id := rollingStepCounts(m)
			if len(m.Nodes) != 3 || m.Nodes[0].NodeID != owner || id != owner || serving != 0 || recycling != 1 || pending != 2 {
				t.Errorf("[%s] admin port %d: generation %d steps %+v, want %s first and alone recycling", proto.name, node.admin, m.Generation, m.Nodes, owner)
			}
		}
	}

	// The owner in turn now leads and holds the retry's credential, so it
	// recycles first with no file while the other two wait unauthorized.
	// Their logs never named a credential they awaited before this point.
	settle := func(i int) {
		t.Logf("node %d migrated", i+1)
		unsealRolling(t, client, adminPorts[i], newPassphrase)
	}
	if !pollUntil(180*time.Second, 250*time.Millisecond, func() bool {
		for i, l := range c.logs {
			if i != inTurn && awaitingCredentialIn(l.String(), "scope=system") {
				t.Fatalf("node %d awaits its credential while node %d still holds the turn", i+1, inTurn+1)
			}
		}
		return readBarrierStatus(t, ca, adminPorts[inTurn]).Sealed
	}) {
		t.Fatalf("node %d did not migrate with the retry's credential within 180s", inTurn+1)
	}
	settle(inTurn)

	// Then the other two, one at a time, each on its own credential.
	done := map[int]bool{inTurn: true}
	for turn := 2; turn <= 3; turn++ {
		idx := waitForAwaitingOwner(t, c, done, turn)
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[idx], 30*time.Second); got.Sealed || got.Strategy != "none" {
			t.Fatalf("turn %d: node %d migrated before it had its own credential: barrier %s", turn, idx+1, got)
		}
		writeMigrationCredential(t, c.credDirs[idx], "system", newPassphrase)
		if !pollUntil(180*time.Second, 250*time.Millisecond, func() bool { return readBarrierStatus(t, ca, adminPorts[idx]).Sealed }) {
			t.Fatalf("turn %d: node %d did not migrate within 180s of getting its credential", turn, idx+1)
		}
		settle(idx)
		done[idx] = true
	}
	for i := range 3 {
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[i], 60*time.Second); got.Sealed || got.Strategy != "passphrase" {
			t.Errorf("node %d post-migration barrier: %s, want unsealed passphrase", i+1, got)
		}
	}
	for _, node := range c.nodes {
		for _, proto := range multiProtocols {
			waitForCLIMigrationReaped(t, cli, c, node, proto, 90*time.Second)
		}
	}
}

// TestClusterEnableEncryption_Rolling_RF3_CoordinatorRestart stops the
// coordinating leader in the middle of a rolling system-scope
// enable-encryption and starts it again. The rollout resumes from the
// replicated step record where it left off: the same generation, the owner
// that had served still serving, the owner in turn still alone in turn. The
// restarted node, leading again, hands that turn on, and no turn is taken
// twice.
func TestClusterEnableEncryption_Rolling_RF3_CoordinatorRestart(t *testing.T) {
	cli := buildCLIBinary(t)
	c := startNoneSealSystemRF3(t, "rollrs")
	ca, adminPorts := c.ca, c.adminPorts
	waitForFeatureActive(t, c.logs, featuregate.RollingMigration, 90*time.Second)

	// No node has a credential file, so each owner holds its turn,
	// authorized and not yet recycled, until the test hands it one.
	const newPassphrase = "cluster-rolling-restart-pass"
	client := mtlsHTTPClient(t, ca, 15*time.Second)
	coord := adminPortIndex(t, adminPorts, c.leaderPort)
	if out, err := cliEnableEncryption(t, cli, c, coord, newPassphrase); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	done := map[int]bool{}
	migrate := func(turn, idx int) {
		t.Helper()
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[idx], 30*time.Second); got.Sealed || got.Strategy != "none" {
			t.Fatalf("turn %d: node %d migrated before it had its own credential: barrier %s", turn, idx+1, got)
		}
		writeMigrationCredential(t, c.credDirs[idx], "system", newPassphrase)
		if _, ok := waitForSealed(t, ca, adminPorts[idx], 180*time.Second); !ok {
			t.Fatalf("turn %d: node %d did not migrate within 180s of getting its credential", turn, idx+1)
		}
		t.Logf("node %d migrated (turn %d)", idx+1, turn)
		unsealRolling(t, client, adminPorts[idx], newPassphrase)
		done[idx] = true
	}

	if first := waitForAwaitingOwner(t, c, done, 1); first == coord {
		t.Fatalf("node %d, the coordinating leader, took the first turn; it goes last", coord+1)
	} else {
		migrate(1, first)
	}
	second := waitForAwaitingOwner(t, c, done, 2)
	if second == coord {
		t.Fatalf("node %d, the coordinating leader, took the second turn; it goes last", coord+1)
	}
	before := waitForCLIMigration(t, cli, c, c.nodes[second], multiProtocols[0], 60*time.Second, "at turn 2 (1 serving, 1 recycling, 1 pending)",
		func(m *transport.ClusterMigration) bool {
			serving, recycling, pending, _ := rollingStepCounts(m)
			return len(m.Nodes) == 3 && serving == 1 && recycling == 1 && pending == 1
		})
	if before == nil {
		t.FailNow()
	}
	_, _, _, secondID := rollingStepCounts(before)

	// The coordinator stops and starts again while the second turn is held,
	// then leads again.
	c.stop(t, coord)
	restartedAt := len(c.logs[coord].String())
	c.restart(t, coord)
	out, err := runQRDB(t, cli, c.nodes[second].cliArgsFor(ca, c.adminCert, c.adminKey, multiProtocols[0]),
		"admin", "member", "transfer-leadership", strconv.Itoa(coord+1), "--shard-id", "1")
	if err != nil || !strings.Contains(out, "OK") {
		t.Fatalf("qrdb admin member transfer-leadership %d: %v: %s", coord+1, err, out)
	}
	waitForLeaderViaAdmin(t, ca, []int{adminPorts[coord]}, 60*time.Second)

	// Every node, over every transport: the record is where the restart
	// found it.
	for _, node := range c.nodes {
		for _, proto := range multiProtocols {
			m := waitForCLIMigration(t, cli, c, node, proto, 60*time.Second, fmt.Sprintf("generation %d after the restart", before.Generation),
				func(*transport.ClusterMigration) bool { return true })
			if m == nil {
				continue
			}
			same := m.Generation == before.Generation && len(m.Nodes) == len(before.Nodes)
			for j := 0; same && j < len(m.Nodes); j++ {
				same = m.Nodes[j].NodeID == before.Nodes[j].NodeID && m.Nodes[j].Step == before.Nodes[j].Step
			}
			if !same {
				t.Errorf("[%s] admin port %d: after the restart generation %d steps %+v, want generation %d steps %+v",
					proto.name, node.admin, m.Generation, m.Nodes, before.Generation, before.Nodes)
			}
		}
	}

	// The restarted coordinator hands the second turn on once its owner is
	// back.
	migrate(2, second)
	if !pollUntil(60*time.Second, 250*time.Millisecond, func() bool {
		return slices.ContainsFunc(handOnsInText(c.logs[coord].String()[restartedAt:]), func(h handOn) bool { return h.node == secondID })
	}) {
		t.Errorf("the restarted coordinator, node %d, never handed on %s's turn", coord+1, secondID)
	}

	// The coordinator's own turn comes last. The restart cost it the
	// request's credential, so it awaits its own file like any other owner.
	if last := waitForAwaitingOwner(t, c, done, 3); last != coord {
		t.Fatalf("node %d took the last turn, want the coordinator, node %d", last+1, coord+1)
	}
	migrate(3, coord)

	for i := range 3 {
		if got := waitForDefinitiveBarrierStatus(t, ca, adminPorts[i], 60*time.Second); got.Sealed || got.Strategy != "passphrase" {
			t.Errorf("node %d post-migration barrier: %s, want unsealed passphrase", i+1, got)
		}
	}
	for _, node := range c.nodes {
		for _, proto := range multiProtocols {
			waitForCLIMigrationReaped(t, cli, c, node, proto, 90*time.Second)
		}
	}
	// One hand-on per owner across every coordinator's log: the restart
	// repeated no turn.
	owners := map[string]bool{}
	for _, h := range waitForHandOns(t, c.logs, 3, 60*time.Second) {
		owners[h.node] = true
	}
	if len(owners) != 3 {
		t.Errorf("hand-ons named owners %v, want three distinct", owners)
	}
}

// waitForAwaitingOwner waits for the one owner not in done whose log says it
// awaits its own credential for the system-scope migration, and returns its
// index. Two such owners at once fail the test: owners take turns.
func waitForAwaitingOwner(t *testing.T, c *noneSealRF3, done map[int]bool, turn int) int {
	t.Helper()
	idx := -1
	if !pollUntil(180*time.Second, 250*time.Millisecond, func() bool {
		var waiting []int
		for i, l := range c.logs {
			if !done[i] && awaitingCredentialIn(l.String(), "scope=system") {
				waiting = append(waiting, i)
			}
		}
		if len(waiting) > 1 {
			t.Fatalf("turn %d: nodes %v await their credentials at once; owners recycle one at a time", turn, waiting)
		}
		if len(waiting) == 1 {
			idx = waiting[0]
		}
		return idx >= 0
	}) {
		t.Fatalf("turn %d: no node awaited its credential within 180s", turn)
	}
	return idx
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
// cliEnableEncryption starts the system barrier's rolling migration to the
// passphrase strategy with `qrdb barrier enable-encryption` against node idx,
// and returns an error unless the CLI exits zero reporting the migration
// started on the system scope.
func cliEnableEncryption(t *testing.T, cli string, c *noneSealRF3, idx int, passphrase string) (string, error) {
	t.Helper()
	out, err := runQRDB(t, cli, c.nodes[idx].cliArgsFor(c.ca, c.adminCert, c.adminKey, multiProtocols[0]),
		"barrier", "enable-encryption", "--strategy", "passphrase", "--passphrase", passphrase, "-y")
	if err != nil {
		return out, fmt.Errorf("qrdb barrier enable-encryption on node %d: %w", idx+1, err)
	}
	if !strings.Contains(out, "migration_started (scope: system)") {
		return out, fmt.Errorf("qrdb barrier enable-encryption on node %d: no system-scope migration_started", idx+1)
	}
	return out, nil
}

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
	var (
		m   *transport.ClusterMigration
		err error
	)
	if pollUntil(timeout, 250*time.Millisecond, func() bool {
		m, err = cliClusterMigration(t, cli, c, node, proto)
		return err == nil && m != nil && !m.Unconfirmed && ok(m)
	}) {
		return m
	}
	t.Errorf("[%s] admin port %d: barrier status migration never %s within %v (last: %+v, err: %v)", proto.name, node.admin, want, timeout, m, err)
	return nil
}

// waitForCLIMigrationReaped polls `qrdb barrier status` on one node over one
// transport until it definitively reports no cluster migration: the read
// succeeded and carries no record. An unconfirmed read is not an answer.
func waitForCLIMigrationReaped(t *testing.T, cli string, c *noneSealRF3, node gatedClusterNode, proto multiProtocol, timeout time.Duration) {
	t.Helper()
	var (
		m   *transport.ClusterMigration
		err error
	)
	if !pollUntil(timeout, 250*time.Millisecond, func() bool {
		m, err = cliClusterMigration(t, cli, c, node, proto)
		return err == nil && m == nil
	}) {
		t.Errorf("[%s] admin port %d: barrier status migration never reaped within %v (last: %+v, err: %v)", proto.name, node.admin, timeout, m, err)
	}
}

// waitForFeatureActive polls every node's log until each has activated
// feature f cluster-wide or observed its activation.
func waitForFeatureActive(t *testing.T, logs []*nodeLog, f featuregate.Feature, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for i, l := range logs {
		if !pollUntil(time.Until(deadline), 200*time.Millisecond, func() bool {
			return featureActiveIn(l.String(), f)
		}) {
			t.Fatalf("node %d never logged the activation of %s within %v", i+1, f, timeout)
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
		got = append(got, handOnsInText(l.String())...)
	}
	return got
}

// handOnsInText returns every hand-on line in one node's log text, in order.
func handOnsInText(log string) []handOn {
	var got []handOn
	for _, line := range strings.Split(log, "\n") {
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
	var at time.Time
	sealed := pollUntil(timeout, 250*time.Millisecond, func() bool {
		if readBarrierStatus(t, ca, port).Sealed {
			at = time.Now()
			return true
		}
		return false
	})
	return at, sealed
}

// waitForAcked waits until want writes have been acknowledged since since.
func waitForAcked(t *testing.T, w *rollingWriter, since time.Time, want int, timeout time.Duration, when string) {
	t.Helper()
	if !pollUntil(timeout, 250*time.Millisecond, func() bool {
		return w.ackedSince(since) >= want
	}) {
		t.Errorf("only %d of %d writes committed %s within %v: quorum not held", w.ackedSince(since), want, when, timeout)
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
	var (
		status int
		body   string
	)
	pollUntil(90*time.Second, time.Second, func() bool {
		status, body = 0, ""
		var resp *http.Response
		resp, err = client.Post(fmt.Sprintf("https://127.0.0.1:%d/v1/barrier/unseal", port), "application/json", bytes.NewReader(payload))
		if err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			resp.Body.Close()
			status, body = resp.StatusCode, string(raw)
		}
		return err == nil && status != http.StatusServiceUnavailable
	})
	if status == http.StatusNoContent || status == http.StatusConflict {
		return
	}
	t.Errorf("unseal on port %d: status %d, err %v, body: %s", port, status, err, body)
}
