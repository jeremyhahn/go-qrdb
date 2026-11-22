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

//go:build !integration

package node

import (
	"context"
	"io"
	"log/slog"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/clock/clocktest"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// gatedClock is a clocktest.Fake that, once armed, holds the lease-expiry
// sweeper inside its own read of its ticker: the sweeper's first C call on a
// ticker from this clock closes held, and every sweeper C call blocks until
// release is closed. Every other caller, and every other method, gets the
// Fake unchanged. The double holds only the sweeper's own read of the clock
// and asserts nothing about Close. It is the first of the test's two gates;
// gatedHandler (the member record, clustered node only) is the second.
//
// The hold is on Ticker.C, not on Now: lease.(*Lessor).RunExpiry evaluates
// ticker.C() at the head of every pass of its select loop, outside any lock,
// whereas each of its Now reads (ExpiredLeaseIDs, Promote) runs under the
// Lessor's mutex, which Close itself needs (volume.Reporter.Close revokes
// its lease) before it stops the sweeper. A hold under that mutex would make
// Close wait whether or not it joins the sweeper.
type gatedClock struct {
	*clocktest.Fake
	held    chan struct{}
	release chan struct{}
	once    sync.Once
	armed   atomic.Bool
}

func (c *gatedClock) NewTicker(d time.Duration) clock.Ticker {
	return &gatedTicker{Ticker: c.Fake.NewTicker(d), clk: c}
}

// gatedTicker is a ticker of a gatedClock; see gatedClock.
type gatedTicker struct {
	clock.Ticker
	clk *gatedClock
}

func (t *gatedTicker) C() <-chan time.Time {
	if t.clk.armed.Load() {
		buf := make([]byte, 64<<10)
		buf = buf[:runtime.Stack(buf, false)]
		if strings.Contains(string(buf), "lease.(*Lessor).RunExpiry") {
			t.clk.once.Do(func() { close(t.clk.held) })
			<-t.clk.release
		}
	}
	return t.Ticker.C()
}

// closeRaceAuditSink is an audit sink whose teardown is an unsynchronized
// write the way an embedder's is (go-xkms closes its audit sink and its
// shared TPM right after node.Close): every Emit reads torn, and the test
// sets torn without a lock once Close has returned. A goroutine the node
// constructor started that still emits after Close is a data race the race
// detector reports against that write.
type closeRaceAuditSink struct {
	*audit.RingBuffer
	torn bool
}

func (s *closeRaceAuditSink) Emit(ctx context.Context, ev audit.AuditEvent) error {
	if s.torn {
		return nil
	}
	return s.RingBuffer.Emit(ctx, ev)
}

// qrdbGoroutines returns the stacks of the live goroutines that run or were
// created by this module's code, keyed by goroutine header ("goroutine N").
func qrdbGoroutines() map[string]string {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	out := make(map[string]string)
	for _, g := range strings.Split(string(buf), "\n\n") {
		if !strings.Contains(g, "github.com/jeremyhahn/go-qrdb/") {
			continue
		}
		head, _, _ := strings.Cut(g, " [")
		out[head] = g
	}
	return out
}

// constructorWorkerFrames are the run frames of the node constructors'
// long-lived workers. Each frame returns before its goroutine's deferred
// join signal (WaitGroup.Done or close(done)) fires, so a worker Close has
// joined can never still show one of them.
var constructorWorkerFrames = []string{
	"lease.(*Lessor).RunExpiry",
	"service.(*ReclaimSweeper).Run",
	"clienttrust.(*Pool).Run",
	"volume.(*Reporter).refreshLoop",
	"server.(*TenantShardReconciler).Run",
	"clusterseal.(*Monitor).Run",
}

// workerFrames maps each constructorWorkerFrames entry to the headers of the
// goroutines in gs whose stack runs it. A "created by" line names the
// creator of a goroutine, not a frame it runs, so it does not count.
func workerFrames(gs map[string]string) map[string][]string {
	out := make(map[string][]string)
	for head, stack := range gs {
		for _, line := range strings.Split(stack, "\n") {
			if strings.HasPrefix(line, "created by ") {
				continue
			}
			for _, f := range constructorWorkerFrames {
				if strings.Contains(line, f) && !slices.Contains(out[f], head) {
					out[f] = append(out[f], head)
				}
			}
		}
	}
	return out
}

// memberGate is the shared state of a gatedHandler and the handlers derived
// from it with WithAttrs or WithGroup.
type memberGate struct {
	held    chan struct{}
	release chan struct{}
	once    sync.Once
	armed   atomic.Bool
}

// gatedHandler is a slog.Handler double that, once armed, holds the node's
// member-record worker inside its own "member record published" log call:
// when the record comes from the worker (startMemberRecord's goroutine is on
// the caller's own stack) it closes held once and blocks until release is
// closed. Every other record passes through to a discarding text handler.
// The double holds only that worker's own log call and asserts nothing
// about Close.
type gatedHandler struct {
	slog.Handler
	gate *memberGate
}

func newGatedHandler() *gatedHandler {
	return &gatedHandler{
		Handler: slog.NewTextHandler(io.Discard, nil),
		gate:    &memberGate{held: make(chan struct{}), release: make(chan struct{})},
	}
}

func (h *gatedHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *gatedHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.gate.armed.Load() && r.Message == "member record published" {
		buf := make([]byte, 64<<10)
		buf = buf[:runtime.Stack(buf, false)]
		if strings.Contains(string(buf), "startMemberRecord.func") {
			h.gate.once.Do(func() { close(h.gate.held) })
			<-h.gate.release
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h *gatedHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &gatedHandler{Handler: h.Handler.WithAttrs(attrs), gate: h.gate}
}

func (h *gatedHandler) WithGroup(name string) slog.Handler {
	return &gatedHandler{Handler: h.Handler.WithGroup(name), gate: h.gate}
}

// TestNodeCloseJoinsConstructorGoroutines pins go-qrdb#1665: Node.Close
// returns only after every goroutine the node constructor started has
// exited, so an embedder may tear down what it passed in (the audit sink)
// straight after Close without racing one of them.
//
// The check is an ordering one, not an eventual one, with two gates on two
// different workers and dependencies. The node runs on a gatedClock, so a
// lease-expiry sweep is held inside its read of the clock while Close runs
// at the same time; on the clustered node a gatedHandler also holds the
// member-record worker inside its "member record published" log call. Close
// must not return while either worker is held, and must return once both
// are released. A stop that only cancels a gated worker without waiting for
// it fails here.
//
// Right after Close returns, one goroutine snapshot (no poll, no sleep) must
// show no constructor worker frame (constructorWorkerFrames) outside the
// goroutines that ran before the node was built: a joined worker has left
// its run frame before its join signal fires, so this is deterministic. The
// frame check covers the workers no gate can hold: the reclaim sweeper and
// the trust pool run on wall-clock tickers or timers and storage reads that
// the SDK test cannot hold, as do the volume reporter, the tenant reconciler
// and the cluster-seal monitor. The frame check catches such a worker when
// it is still in its run frame at the snapshot: one Close returned without
// joining while it was busy or blocked. A cancel-only stop of a worker that
// idles in its select and exits at once on cancel usually leaves its frame
// before Close returns, so the check alone does not reliably catch that.
//
// Both constructors are covered: an embedder (go-xkms pkg/embeddednode)
// builds either one.
func TestNodeCloseJoinsConstructorGoroutines(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		assertCloseJoinsConstructorGoroutines(t, false, func(opts []server.ServerOption, nodeOpts ...NodeOption) (*Node, error) {
			return NewStandaloneNode(context.Background(), newMemoryNodeConfig(t), opts, nodeOpts...)
		})
	})
	t.Run("clustered", func(t *testing.T) {
		assertCloseJoinsConstructorGoroutines(t, true, func(opts []server.ServerOption, nodeOpts ...NodeOption) (*Node, error) {
			return NewClusteredNode(context.Background(), newRaftIsolationConfig(t), raftMembers(), opts,
				append(nodeOpts, WithNoSyncWrites())...)
		})
	})
}

func assertCloseJoinsConstructorGoroutines(t *testing.T, gateMember bool, build func([]server.ServerOption, ...NodeOption) (*Node, error)) {
	t.Helper()
	before := qrdbGoroutines()
	fresh := func(gs map[string]string) map[string]string {
		out := make(map[string]string)
		for head, stack := range gs {
			if _, ok := before[head]; !ok {
				out[head] = stack
			}
		}
		return out
	}

	clk := &gatedClock{
		Fake:    clocktest.NewAt(time.Now()),
		held:    make(chan struct{}),
		release: make(chan struct{}),
	}
	var releaseClock sync.Once
	t.Cleanup(func() { releaseClock.Do(func() { close(clk.release) }) })
	h := newGatedHandler()
	var releaseMember sync.Once
	t.Cleanup(func() { releaseMember.Do(func() { close(h.gate.release) }) })
	if gateMember {
		h.gate.armed.Store(true)
	}

	sink := &closeRaceAuditSink{RingBuffer: audit.NewRingBuffer(64)}
	n, err := build([]server.ServerOption{server.WithAuditLogger(sink)}, WithClock(clk), WithLogger(slog.New(h)))
	require.NoError(t, err)
	require.Greater(t, len(qrdbGoroutines()), len(before),
		"the open node runs goroutines of its own; without them this test proves nothing")

	// The post-close frame check below must not be vacuous: the workers
	// both constructors start reach their run frames. A worker goroutine
	// can exist before it enters its run frame, so wait for it, bounded.
	var running map[string][]string
	deadline := time.Now().Add(10 * time.Second)
	for _, f := range []string{"lease.(*Lessor).RunExpiry", "service.(*ReclaimSweeper).Run"} {
		for running = workerFrames(fresh(qrdbGoroutines())); len(running[f]) == 0; running = workerFrames(fresh(qrdbGoroutines())) {
			if time.Now().After(deadline) {
				require.Fail(t, "constructor worker never reached its run frame on the open node", f)
			}
			runtime.Gosched()
		}
	}
	for _, f := range constructorWorkerFrames {
		t.Logf("open node runs %s in %d goroutine(s)", f, len(running[f]))
	}

	// Hold the lease-expiry sweeper inside its read of its ticker. It parks
	// in its select on a channel it read before arming, so tick until a
	// pass completes and the next pass reaches the gate.
	clk.armed.Store(true)
	deadline = time.Now().Add(10 * time.Second)
	for holding := false; !holding; {
		if time.Now().After(deadline) {
			require.Fail(t, "no lease-expiry sweep reached the gated clock")
		}
		clk.Advance(lease.DefaultExpiryInterval)
		select {
		case <-clk.held:
			holding = true
		default:
			runtime.Gosched()
		}
	}

	// On the clustered node, hold the member-record worker inside its
	// "member record published" log call too.
	if gateMember {
		select {
		case <-h.gate.held:
		case <-time.After(10 * time.Second):
			require.Fail(t, "the clustered node's member record was never published")
		}
	}

	var closeErr error
	closeDone := make(chan struct{})
	go func() {
		closeErr = n.Close()
		close(closeDone)
	}()

	// Bounded negative windows, not synchronizing sleeps: each gated worker
	// is held until its release, so a Close that returns inside a window
	// did not wait for it.
	select {
	case <-closeDone:
		t.Fatal("Close returned while a constructor worker was still inside a sweep")
	case <-time.After(250 * time.Millisecond):
	}

	releaseClock.Do(func() { close(clk.release) })
	if gateMember {
		select {
		case <-closeDone:
			t.Fatal("Close returned while the member-record worker was still held")
		case <-time.After(250 * time.Millisecond):
		}
		releaseMember.Do(func() { close(h.gate.release) })
	}
	select {
	case <-closeDone:
	case <-time.After(10 * time.Second):
		require.Fail(t, "Close did not return after the held workers were released")
	}
	require.NoError(t, closeErr)

	// One snapshot, straight after Close: no constructor worker may still
	// be in its run frame.
	after := fresh(qrdbGoroutines())
	for f, heads := range workerFrames(after) {
		for _, head := range heads {
			t.Errorf("constructor worker %s outlived Close:\n%s", f, after[head])
		}
	}

	// The embedder's teardown: an unsynchronized write and the sink's Close.
	sink.torn = true
	require.NoError(t, sink.RingBuffer.Close())
}
