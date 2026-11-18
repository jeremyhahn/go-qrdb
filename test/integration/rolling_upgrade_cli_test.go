//go:build integration

package integration_test

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// rollWriter puts keys through the cluster's CLI, over each transport in
// turn, while the test rolls nodes, and records the keys the cluster
// acknowledged. It never writes through the node being rolled.
type rollWriter struct {
	c       *upgradeCluster
	rolling atomic.Int32
	seq     int

	mu     sync.Mutex
	acked  map[string]string
	failed []rollFailure
}

// rollFailure is one write the cluster did not acknowledge during a roll.
type rollFailure struct {
	key, proto string
	target     int
	err        error
}

// maxRollFailures bounds the writes a single roll may fail: one leader
// election (the stopped node may hold shard leadership) plus the requests
// in flight on the node being stopped. More than that is an outage, not a
// rolling restart.
const maxRollFailures = 8

func newRollWriter(c *upgradeCluster) *rollWriter {
	w := &rollWriter{c: c, acked: make(map[string]string)}
	w.rolling.Store(-1)
	return w
}

// during runs roll with writes in flight beside it, one at a time, until
// roll returns, and then asserts the cluster stayed available through the
// roll: at least one write acknowledged during it and at most
// maxRollFailures failed.
func (w *rollWriter) during(t *testing.T, node int, roll func()) {
	t.Helper()
	w.mu.Lock()
	ackedBefore, failedBefore := len(w.acked), len(w.failed)
	w.mu.Unlock()

	w.rolling.Store(int32(node))
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			w.putOnce(t)
		}
	}()
	roll()
	close(stop)
	wg.Wait()
	w.rolling.Store(-1)

	w.mu.Lock()
	acked := len(w.acked) - ackedBefore
	failed := slices.Clone(w.failed[failedBefore:])
	w.mu.Unlock()
	if acked == 0 || len(failed) > maxRollFailures {
		var b strings.Builder
		for _, f := range failed {
			fmt.Fprintf(&b, "\n  %s via %s to node %d: %v", f.key, f.proto, f.target+1, f.err)
		}
		t.Fatalf("roll of node %d: %d writes acknowledged, %d failed (want >= 1 acknowledged, <= %d failed):%s",
			node+1, acked, len(failed), maxRollFailures, b.String())
	}
}

// putOnce writes the next key over the next transport through a node that
// is not being rolled, recording it only when acknowledged.
func (w *rollWriter) putOnce(t *testing.T) {
	k := w.seq
	w.seq++
	proto := multiProtocols[k%len(multiProtocols)]
	target := k % 3
	if r := int(w.rolling.Load()); r >= 0 && target == r {
		target = (r + 1) % 3
	}
	key := fmt.Sprintf("roll/%03d", k)
	value := fmt.Sprintf("v%03d-%s", k, proto.name)
	if _, err := runCLIProto(t, w.c.nodes[target], proto, "kv", "put", key, value); err != nil {
		w.mu.Lock()
		w.failed = append(w.failed, rollFailure{key: key, proto: proto.name, target: target, err: err})
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	w.acked[key] = value
	w.mu.Unlock()
}

func (w *rollWriter) snapshot() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.acked)
}

// TestRollingUpgrade_RollForward_CLI is the rolling-upgrade matrix's T1: a
// three-node cluster on N is rolled, one node at a time, to the synthetic
// N+1 while a writer puts keys over every transport. Every acknowledged
// write reads back on every node over every transport after each roll; the
// new feature activates only once the last node runs N+1, and its command
// then applies on every node.
func TestRollingUpgrade_RollForward_CLI(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	c := startUpgradeCluster(t, []string{bins.n, bins.n, bins.n})
	w := newRollWriter(c)

	for i := range 3 {
		w.during(t, i, func() { c.roll(t, i, bins.next) })
		acked := w.snapshot()
		c.requireReadsEverywhere(t, acked)
		if i < 2 {
			if on := activatedOn(c.logs, upgradeProbeFeature); len(on) != 0 {
				t.Fatalf("%s activated on nodes %v with only %d of 3 nodes on N+1", upgradeProbeFeature, on, i+1)
			}
		}
	}

	awaitActivated(t, c.logs, upgradeProbeFeature)

	after := make(map[string]string, len(multiProtocols))
	for k, proto := range multiProtocols {
		key := fmt.Sprintf("%safter-%s", upgradeProbeKeyPrefix, proto.name)
		value := "probe-" + proto.name
		putAcked(t, c.nodes[k%3], proto, key, value)
		after[key] = value
	}
	c.requireReadsEverywhere(t, after)
	c.requireReadsEverywhere(t, w.snapshot())
}

// TestRollingUpgrade_RollBackBeforeActivation_CLI is the matrix's T3: one
// node rolled to N+1 and back to N before the new feature activates. While
// the cluster is mixed the feature never activates and a probe-prefixed put
// is an ordinary put; after the rollback every write made in the mixed
// cluster reads back on every node over every transport.
func TestRollingUpgrade_RollBackBeforeActivation_CLI(t *testing.T) {
	bins := buildUpgradeBinaries(t)
	c := startUpgradeCluster(t, []string{bins.n, bins.n, bins.n})

	c.roll(t, 0, bins.next)

	want := make(map[string]string, 2*len(multiProtocols))
	for k, proto := range multiProtocols {
		key := fmt.Sprintf("mixed/%s", proto.name)
		value := "mixed-" + proto.name
		putAcked(t, c.nodes[k%3], proto, key, value)
		want[key] = value

		// Through the N+1 node, which would propose the probe opcode were
		// the feature active.
		probeKey := fmt.Sprintf("%smixed-%s", upgradeProbeKeyPrefix, proto.name)
		probeValue := "probe-mixed-" + proto.name
		putAcked(t, c.nodes[0], proto, probeKey, probeValue)
		want[probeKey] = probeValue
	}
	c.requireReadsEverywhere(t, want)
	if on := activatedOn(c.logs, upgradeProbeFeature); len(on) != 0 {
		t.Fatalf("%s activated on nodes %v in a mixed N/N+1 cluster", upgradeProbeFeature, on)
	}

	c.roll(t, 0, bins.n)

	c.requireReadsEverywhere(t, want)
	if on := activatedOn(c.logs, upgradeProbeFeature); len(on) != 0 {
		t.Fatalf("%s activated on nodes %v after the rollback to N", upgradeProbeFeature, on)
	}
}
