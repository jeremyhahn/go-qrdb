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
	"time"
)

// rollWriter puts keys through the cluster's CLI, over each transport in
// turn, while the test rolls nodes, and records the keys the cluster
// acknowledged. It never writes through the node being rolled.
type rollWriter struct {
	c       *upgradeCluster
	rolling atomic.Int32
	phase   atomic.Int32
	seq     int

	mu          sync.Mutex
	acked       map[string]string
	failed      []rollFailure
	downAcks    int
	downReached chan struct{}
}

// rollFailure is one write the cluster did not acknowledge during a roll.
type rollFailure struct {
	key, proto string
	target     int
	phase      string
	down       bool
	err        error
}

// maxRollFailures bounds the writes a single roll may fail: one leader
// election (the stopped node may hold shard leadership) plus the requests
// in flight on the node being stopped. More than that is an outage, not a
// rolling restart.
const maxRollFailures = 8

// minDownAcks is how many writes must be acknowledged that both started and
// finished while the rolled node was stopped: proof that the remaining two
// nodes kept the cluster writable, not just that writes resumed once the
// node came back.
const minDownAcks = 3

// downWindow bounds how long a node is held down waiting for minDownAcks:
// one leader election plus one CLI timeout.
const downWindow = 45 * time.Second

// Writer phases of the rolled node, read by putOnce before and after each
// write.
const (
	phaseUp int32 = iota
	phaseDown
	phaseRestarting
)

var phaseNames = [...]string{phaseUp: "up", phaseDown: "down", phaseRestarting: "restarting"}

// phaseSpan names the phase a write ran in, or the transition it spanned.
func phaseSpan(before, after int32) string {
	if before == after {
		return phaseNames[before]
	}
	return phaseNames[before] + "->" + phaseNames[after]
}

func newRollWriter(c *upgradeCluster) *rollWriter {
	w := &rollWriter{c: c, acked: make(map[string]string)}
	w.rolling.Store(-1)
	return w
}

// during rolls node onto bin with writes in flight beside it, one at a
// time. It stops the node and holds it down until minDownAcks writes that
// started and finished while it was stopped are acknowledged (failing at
// downWindow), then restarts it, and asserts the cluster stayed available
// through the roll: at least one write acknowledged, and at most
// maxRollFailures failed overall and while the node was down.
func (w *rollWriter) during(t *testing.T, node int, bin string) {
	t.Helper()
	downReached := make(chan struct{})
	w.mu.Lock()
	ackedBefore, failedBefore := len(w.acked), len(w.failed)
	w.downAcks, w.downReached = 0, downReached
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
	var stopOnce sync.Once
	stopWriter := func() {
		stopOnce.Do(func() {
			close(stop)
			wg.Wait()
			w.phase.Store(phaseUp)
			w.rolling.Store(-1)
		})
	}
	defer stopWriter()

	w.c.stopNode(t, node)
	w.phase.Store(phaseDown)
	select {
	case <-downReached:
	case <-time.After(downWindow):
		stopWriter()
		w.mu.Lock()
		downAcks, failed := w.downAcks, slices.Clone(w.failed[failedBefore:])
		w.mu.Unlock()
		t.Fatalf("node %d stopped: %d writes acknowledged within %s while it was down (want >= %d):%s",
			node+1, downAcks, downWindow, minDownAcks, describeFailures(failed))
	}
	w.phase.Store(phaseRestarting)
	w.c.restartNode(t, node, bin)
	stopWriter()

	w.mu.Lock()
	acked := len(w.acked) - ackedBefore
	failed := slices.Clone(w.failed[failedBefore:])
	w.mu.Unlock()
	downFailed := 0
	for _, f := range failed {
		if f.down {
			downFailed++
		}
	}
	if acked == 0 || len(failed) > maxRollFailures || downFailed > maxRollFailures {
		t.Fatalf("roll of node %d: %d writes acknowledged, %d failed, %d while down (want >= 1 acknowledged, <= %d failed):%s",
			node+1, acked, len(failed), downFailed, maxRollFailures, describeFailures(failed))
	}
}

// describeFailures lists every recorded write failure with its phase.
func describeFailures(failed []rollFailure) string {
	var b strings.Builder
	for _, f := range failed {
		fmt.Fprintf(&b, "\n  [%s] %s via %s to node %d: %v", f.phase, f.key, f.proto, f.target+1, f.err)
	}
	return b.String()
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
	before := w.phase.Load()
	_, err := runCLIProto(t, w.c.nodes[target], proto, "kv", "put", key, value)
	after := w.phase.Load()
	down := before == phaseDown && after == phaseDown
	w.mu.Lock()
	defer w.mu.Unlock()
	if err != nil {
		w.failed = append(w.failed, rollFailure{
			key: key, proto: proto.name, target: target, phase: phaseSpan(before, after), down: down, err: err,
		})
		return
	}
	w.acked[key] = value
	if down {
		w.downAcks++
		if w.downAcks == minDownAcks {
			close(w.downReached)
		}
	}
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
		w.during(t, i, bins.next)
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
