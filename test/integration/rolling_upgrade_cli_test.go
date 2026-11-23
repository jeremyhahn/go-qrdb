//go:build integration

package integration_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rollWriter puts keys through the cluster's CLI, over each transport in
// turn, while the test rolls nodes, and records the keys the cluster
// acknowledged. It never writes through the node being rolled. Beside the
// writer a reader gets recently acknowledged keys on every node that is up,
// and every put and get is recorded, with call and return times, in an
// operation history the clusterproof linearizability checker reads.
type rollWriter struct {
	c       *upgradeCluster
	rolling atomic.Int32
	phase   atomic.Int32
	seq     int
	readSeq int
	clock   time.Time

	mu          sync.Mutex
	acked       map[string]string
	order       []string
	failed      []rollFailure
	downAcks    int
	downReads   int
	downReached chan struct{}
	history     []rollOp
}

// rollOp is one recorded operation. Its JSON tags are the clusterproof
// history format's (test/clusterproof/history.Op), which this module may
// not import: the checker's dependency stays out of the root module.
type rollOp struct {
	ID       uint64     `json:"id"`
	Client   int        `json:"client"`
	Kind     string     `json:"op"`
	Key      string     `json:"key"`
	Input    *rollInput `json:"input,omitempty"`
	Output   *rollOut   `json:"output,omitempty"`
	CallNS   int64      `json:"call_ns"`
	ReturnNS int64      `json:"return_ns"`
	Outcome  string     `json:"outcome"`
	Error    string     `json:"error,omitempty"`
}

// rollInput is a put's input in the history format.
type rollInput struct {
	Value string `json:"value,omitempty"`
}

// rollOut is a get's output in the history format.
type rollOut struct {
	Value string `json:"value,omitempty"`
	Found bool   `json:"found,omitempty"`
}

// Clients of the recorded history: the writer, then one reader client.
const (
	writerClient = 0
	readerClient = 1
)

// minDownReads is how many reads must succeed that both started and
// finished while the rolled node was stopped, so the history checks reads
// served during each replacement.
const minDownReads = 1

// recentReadKeys is how far back from the newest acknowledged key the
// reader reaches.
const recentReadKeys = 8

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
	w := &rollWriter{c: c, acked: make(map[string]string), clock: time.Now()}
	w.rolling.Store(-1)
	return w
}

// now reads the history's one clock: unix nanoseconds advanced by the
// monotonic reading since the writer was made, so call and return times of
// every operation are ordered as they happened.
func (w *rollWriter) now() int64 {
	return w.clock.UnixNano() + time.Since(w.clock).Nanoseconds()
}

// record appends op to the history under w.mu, numbering it.
func (w *rollWriter) record(op rollOp) {
	op.ID = uint64(len(w.history) + 1)
	w.history = append(w.history, op)
}

// checkDownReached closes downReached, under w.mu, once enough writes and
// reads succeeded while the rolled node was stopped.
func (w *rollWriter) checkDownReached() {
	if w.downReached != nil && w.downAcks >= minDownAcks && w.downReads >= minDownReads {
		close(w.downReached)
		w.downReached = nil
	}
}

// during rolls node onto bin with writes in flight beside it, one at a
// time, and a reader beside them. It stops the node and holds it down until
// minDownAcks writes and minDownReads reads that started and finished while
// it was stopped succeeded (failing at downWindow), then restarts it, and
// asserts the cluster stayed available through the roll: at least one write
// acknowledged, and at most maxRollFailures failed overall and while the
// node was down.
func (w *rollWriter) during(t *testing.T, node int, bin string) {
	t.Helper()
	downReached := make(chan struct{})
	w.mu.Lock()
	ackedBefore, failedBefore := len(w.acked), len(w.failed)
	w.downAcks, w.downReads, w.downReached = 0, 0, downReached
	w.mu.Unlock()

	w.rolling.Store(int32(node))
	var wg sync.WaitGroup
	stop := make(chan struct{})
	loop := func(step func(*testing.T)) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			step(t)
		}
	}
	wg.Add(2)
	go loop(w.putOnce)
	go loop(w.getOnce)
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
		downAcks, downReads, failed := w.downAcks, w.downReads, slices.Clone(w.failed[failedBefore:])
		w.mu.Unlock()
		t.Fatalf("node %d stopped: %d writes acknowledged and %d reads served within %s while it was down (want >= %d and >= %d):%s",
			node+1, downAcks, downReads, downWindow, minDownAcks, minDownReads, describeFailures(failed))
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
	call := w.now()
	_, err := runCLIProto(t, w.c.nodes[target], proto, "kv", "put", key, value)
	ret := w.now()
	after := w.phase.Load()
	down := before == phaseDown && after == phaseDown
	w.mu.Lock()
	defer w.mu.Unlock()
	op := rollOp{Client: writerClient, Kind: "put", Key: key, Input: &rollInput{Value: value},
		CallNS: call, ReturnNS: ret, Outcome: "ok"}
	if err != nil {
		// The put may still have committed: its outcome is unknown.
		op.Outcome, op.Error = "indeterminate", err.Error()
		w.record(op)
		w.failed = append(w.failed, rollFailure{
			key: key, proto: proto.name, target: target, phase: phaseSpan(before, after), down: down, err: err,
		})
		return
	}
	w.record(op)
	w.acked[key] = value
	w.order = append(w.order, key)
	if down {
		w.downAcks++
		w.checkDownReached()
	}
}

// getOnce reads one recently acknowledged key, linearizably, over the next
// transport through the next node that is up -- every node but the rolled
// one while it is stopped or restarting -- and records the read.
func (w *rollWriter) getOnce(t *testing.T) {
	r := w.readSeq
	w.readSeq++
	w.mu.Lock()
	n := len(w.order)
	var key string
	if n > 0 {
		key = w.order[n-1-r%min(n, recentReadKeys)]
	}
	w.mu.Unlock()
	if key == "" {
		// Nothing acknowledged yet: read the key the writer puts first.
		key = "roll/000"
	}
	proto := multiProtocols[r%len(multiProtocols)]
	target := r % 3
	if rolled := int(w.rolling.Load()); rolled >= 0 && target == rolled && w.phase.Load() != phaseUp {
		target = (rolled + 1) % 3
	}
	before := w.phase.Load()
	call := w.now()
	out, err := runCLIProto(t, w.c.nodes[target], proto, "kv", "get", key, "--consistency", "linearizable")
	ret := w.now()
	after := w.phase.Load()
	op := rollOp{Client: readerClient, Kind: "get", Key: key, CallNS: call, ReturnNS: ret, Outcome: "ok"}
	switch {
	case err == nil:
		op.Output = &rollOut{Value: strings.TrimSpace(out), Found: true}
	case strings.Contains(strings.ToLower(out), "key not found"):
		op.Output = &rollOut{}
	default:
		// A failed read observed nothing.
		op.Outcome, op.Error = "fail", err.Error()
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.record(op)
	if op.Outcome == "ok" && before == phaseDown && after == phaseDown {
		w.downReads++
		w.checkDownReached()
	}
}

// checkHistory writes the recorded history to a JSON-lines file, builds the
// clusterproof module's historycheck command and runs it on the file,
// failing the test with the checker's verdict unless the history is
// linearizable.
func (w *rollWriter) checkHistory(t *testing.T) {
	t.Helper()
	w.mu.Lock()
	ops := slices.Clone(w.history)
	w.mu.Unlock()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, op := range ops {
		if err := enc.Encode(op); err != nil {
			t.Fatalf("encode history: %v", err)
		}
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "roll-history.jsonl")
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write history: %v", err)
	}

	proof, err := filepath.Abs(filepath.Join("..", "clusterproof"))
	if err != nil {
		t.Fatalf("clusterproof module: %v", err)
	}
	bin := filepath.Join(dir, "historycheck")
	build := exec.Command("go", "build", "-tags", "clusterproof", "-o", bin, "./cmd/historycheck")
	build.Dir = proof
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build historycheck: %v\n%s", err, out)
	}
	check := exec.Command(bin, path)
	out, err := check.CombinedOutput()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			t.Fatalf("the roll's history (%d operations) is not linearizable:\n%s", len(ops), out)
		}
		t.Fatalf("historycheck on the roll's history (%d operations): %v\n%s", len(ops), err, out)
	}
}

func (w *rollWriter) snapshot() map[string]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return maps.Clone(w.acked)
}

// TestRollingUpgrade_RollForward_CLI is the rolling-upgrade matrix's T1: a
// three-node cluster on N is rolled, one node at a time, to the synthetic
// N+1 while a writer puts keys over every transport and a reader gets
// recently written keys on every node that is up, including while the
// rolled node is stopped. Every acknowledged write reads back on every node
// over every transport after each roll, and the history of every put and
// get across the three rolls passes the clusterproof linearizability check
// (docs/rolling-upgrades/engineering.md, T1): no stale read during the
// upgrade. The
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
	w.checkHistory(t)

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
