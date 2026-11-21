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
	"runtime"
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
// and asserts nothing about Close.
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

// TestNodeCloseJoinsConstructorGoroutines pins go-qrdb#1665: Node.Close
// returns only after every goroutine the node constructor started has
// exited, so an embedder may tear down what it passed in (the audit sink)
// straight after Close without racing one of them.
//
// The check is an ordering one, not an eventual one: the node runs on a
// gatedClock, so a lease-expiry sweep (a constructor worker) is held inside
// its read of the clock while Close runs at the same time. Close must not
// return while the sweep is held, and must return once it is released. A
// stop that only cancels the worker without waiting for it fails here.
//
// Both constructors are covered: an embedder (go-xkms pkg/embeddednode)
// builds either one.
func TestNodeCloseJoinsConstructorGoroutines(t *testing.T) {
	t.Run("standalone", func(t *testing.T) {
		assertCloseJoinsConstructorGoroutines(t, func(opts []server.ServerOption, nodeOpts ...NodeOption) (*Node, error) {
			return NewStandaloneNode(context.Background(), newMemoryNodeConfig(t), opts, nodeOpts...)
		})
	})
	t.Run("clustered", func(t *testing.T) {
		assertCloseJoinsConstructorGoroutines(t, func(opts []server.ServerOption, nodeOpts ...NodeOption) (*Node, error) {
			return NewClusteredNode(context.Background(), newRaftIsolationConfig(t), raftMembers(), opts,
				append(nodeOpts, WithNoSyncWrites())...)
		})
	})
}

func assertCloseJoinsConstructorGoroutines(t *testing.T, build func([]server.ServerOption, ...NodeOption) (*Node, error)) {
	t.Helper()
	before := qrdbGoroutines()

	clk := &gatedClock{
		Fake:    clocktest.NewAt(time.Now()),
		held:    make(chan struct{}),
		release: make(chan struct{}),
	}
	sink := &closeRaceAuditSink{RingBuffer: audit.NewRingBuffer(64)}
	n, err := build([]server.ServerOption{server.WithAuditLogger(sink)}, WithClock(clk))
	require.NoError(t, err)
	require.Greater(t, len(qrdbGoroutines()), len(before),
		"the open node runs goroutines of its own; without them this test proves nothing")

	// Hold the lease-expiry sweeper inside its read of its ticker. It parks
	// in its select on a channel it read before arming, so tick until a
	// pass completes and the next pass reaches the gate.
	clk.armed.Store(true)
	deadline := time.Now().Add(10 * time.Second)
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

	var closeErr error
	closeDone := make(chan struct{})
	go func() {
		closeErr = n.Close()
		close(closeDone)
	}()

	// A bounded negative window, not a synchronizing sleep: the sweep is
	// held until release, so a Close that returns inside it did not wait.
	select {
	case <-closeDone:
		t.Fatal("Close returned while a constructor worker was still inside a sweep")
	case <-time.After(250 * time.Millisecond):
	}

	close(clk.release)
	select {
	case <-closeDone:
	case <-time.After(10 * time.Second):
		require.Fail(t, "Close did not return after the held sweep was released")
	}
	require.NoError(t, closeErr)

	// The embedder's teardown: an unsynchronized write and the sink's Close.
	sink.torn = true
	require.NoError(t, sink.RingBuffer.Close())
}
