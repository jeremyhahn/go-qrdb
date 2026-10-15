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

package main

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
)

// This file carries no build constraint on purpose: reserveNoisePort is
// called from files under every tag set this package builds with, and
// TestMain arms holdDefaultNoisePort under all of them. A helper's
// constraint must be a superset of every caller's (AGENTS.md).

// reserveNoisePort gives the caller's flags a Noise port nothing else on this
// host holds, probed AT THE HOST THE LISTENER WILL BIND.
//
// clusterenroll.Options.NoisePort documents zero as "defaults to
// DefaultNoisePort" -- 63006, a fixed number -- and NOT as the ":0" the Go
// convention would read it as. So `bootstrapNoisePort: 0` is not an ephemeral
// port, it is THE most contended port in the package, and every concurrent
// copy of these tests wants exactly it. Gitea runs pull_request CI on
// refs/pull/N/head across five runners sharing one host, so concurrent copies
// are the normal case rather than the exception: this reddened main's CI on
// 0676e9af with two of these tests failing 40 ms apart on
//
//	listen tcp 127.0.0.1:63006: bind: address already in use
//
// and it was the THIRD sighting -- see the comment above
// TestNewStartCmd_ValidConfigFileApplied in coverage_gaps_test.go for the
// first, which was misread as a config-loading bug and filed as go-qrdb#84.
//
// The host is read back off the flags through resolveNoiseHost rather than
// assumed to be testBindHost, because a port proven free at one address says
// nothing about the same number at another -- that is go-qrdb#253, and probing
// where you actually bind is the whole lesson of go-qrdb#313.
//
// Every test that can reach the Noise listener -- resolveTLS on a first node,
// or runStart on flags that get that far -- calls this. holdDefaultNoisePort
// is what makes forgetting it fail every time instead of one run in three.
func reserveNoisePort(t *testing.T, f *startFlags) {
	t.Helper()
	port, err := freePortOn(resolveNoiseHost(f), networkTCP)
	if err != nil {
		t.Fatalf("reserve a Noise port at %s: %v", resolveNoiseHost(f), err)
	}
	f.bootstrapNoisePort = port
}

// holdDefaultNoisePort occupies clusterenroll.DefaultNoisePort on the
// wildcard address for the life of this package's test binary, and returns
// the function that lets it go.
//
// It is the guard behind reserveNoisePort. A test that reaches the Noise
// listener without reserving a port binds the fixed default, and that only
// fails when some concurrent process happens to hold the port: the defect
// then surfaces as a flake in somebody else's run (go-qrdb#84 and its two
// repeats). Holding the port here makes the same omission fail on every run,
// in the test that has it, with "63006: bind: address already in use".
//
// It never depends on the port being free, and never leans on whoever else
// holds it: see portHold.
func holdDefaultNoisePort() func() {
	return holdPort(clusterenroll.DefaultNoisePort).release
}

// portHold occupies a TCP port on the wildcard address -- which covers both
// testBindHost and raftListenHost -- whoever else holds or lets go of the
// same port meanwhile.
//
// Every concurrent copy of this package's tests wants the same fixed port, so
// a hold that gives up when the port is taken, as this one once did, is held
// by the OTHER copy: when that copy exits, the port is free for the rest of
// this run and the guard is gone (go-qrdb CI, "bound 127.0.0.1:63006 during
// the package's tests"). Two things close that:
//
//   - The listener sets SO_REUSEPORT, so every copy of this guard holds the
//     port at once, each on its own socket. A test's ordinary listen, which
//     does not set it, still fails against any one of them.
//   - A holder that is not a copy of this guard -- an older build of it, or
//     any other process -- cannot be shared with. The guard then connects to
//     it and blocks on that connection, which ends when the holder closes its
//     listener or exits, and takes the port at that moment. No polling.
//
// A holder that cannot be reached at testBindHost while it still holds the
// wildcard port gives the guard nothing to wait on, so it polls instead,
// until it takes the port or is released.
type portHold struct {
	addr    string
	ctx     context.Context
	cancel  context.CancelFunc
	armed   chan struct{} // closed once this process holds the port itself
	stopped chan struct{} // closed once nothing is waiting to take the port
	mu      sync.Mutex    // orders a late listen against release
	ln      net.Listener
}

// holdPort holds port until the returned hold is released. It never blocks:
// when the port is taken it waits for the holder in the background.
func holdPort(port int) *portHold {
	ctx, cancel := context.WithCancel(context.Background())
	h := &portHold{
		addr:    net.JoinHostPort("", strconv.Itoa(port)),
		ctx:     ctx,
		cancel:  cancel,
		armed:   make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if h.listen() {
		close(h.stopped)
		return h
	}
	go h.await(net.JoinHostPort(testBindHost, strconv.Itoa(port)))
	return h
}

// listen takes the port, reporting whether there is nothing left to wait for:
// the port is held, or the hold was released meanwhile.
func (h *portHold) listen() bool {
	lc := net.ListenConfig{Control: reusePort}
	ln, err := lc.Listen(h.ctx, networkTCP, h.addr)
	if err != nil {
		return h.ctx.Err() != nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ctx.Err() != nil {
		_ = ln.Close()
		return true
	}
	h.ln = ln
	close(h.armed)
	return true
}

// await waits on the holder at addr and takes the port when it lets go.
func (h *portHold) await(holder string) {
	defer close(h.stopped)
	var d net.Dialer
	for {
		conn, err := d.DialContext(h.ctx, networkTCP, holder)
		if err == nil {
			stop := context.AfterFunc(h.ctx, func() { _ = conn.Close() })
			_, _ = io.Copy(io.Discard, conn)
			stop()
			_ = conn.Close()
		}
		if h.listen() {
			return
		}
		if err != nil {
			h.poll()
			return
		}
	}
}

// portHoldRetryInterval is how often poll tries the port again.
const portHoldRetryInterval = 50 * time.Millisecond

// poll retries the listen on a ticker until it takes the port or the hold is
// released. It serves the one holder await cannot wait on: one that holds the
// wildcard port without accepting at testBindHost. Giving up there instead
// would leave the guard unarmed for the rest of the run the moment that
// holder let go -- the failure this hold exists to rule out.
func (h *portHold) poll() {
	ticker := time.NewTicker(portHoldRetryInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			if h.listen() {
				return
			}
		}
	}
}

// release lets the port go and returns once nothing is waiting to take it.
func (h *portHold) release() {
	h.cancel()
	h.mu.Lock()
	if h.ln != nil {
		_ = h.ln.Close()
	}
	h.mu.Unlock()
	<-h.stopped
}

// TestDefaultNoisePortIsHeld proves the guard is armed: while this package's
// tests run, nothing in them can bind the default Noise port at the address
// these tests bind on. Removing holdDefaultNoisePort from TestMain turns this
// red on any host where the port is otherwise free.
func TestDefaultNoisePortIsHeld(t *testing.T) {
	assertPortHeld(t, clusterenroll.DefaultNoisePort)
}

// TestHoldDefaultNoisePort_SecondHoldIsANoOp: a second hold while TestMain's
// is live -- the shape it takes when a concurrent copy of this package already
// holds the port -- must hand back a release that leaves TestMain's hold in
// place.
func TestHoldDefaultNoisePort_SecondHoldIsANoOp(t *testing.T) {
	holdDefaultNoisePort()()
	assertPortHeld(t, clusterenroll.DefaultNoisePort)
}

// TestPortHold_OutlivesAConcurrentCopy is the shape that reddened CI: a
// concurrent copy of this package holds the port when this copy's guard arms,
// then exits. The guard must hold the port on its own afterwards, not lean on
// a holder that can leave.
func TestPortHold_OutlivesAConcurrentCopy(t *testing.T) {
	port := freeHoldPort(t)
	other := holdPort(port)
	h := holdPort(port)
	t.Cleanup(h.release)
	other.release()
	awaitArmed(t, h)
	assertPortHeld(t, port)
}

// TestPortHold_TakesThePortWhenAForeignHolderLetsGo: a holder that is not a
// copy of this guard -- an older build of it, or any other process -- cannot
// share the port, so the guard must take it the moment that holder lets go.
func TestPortHold_TakesThePortWhenAForeignHolderLetsGo(t *testing.T) {
	port := freeHoldPort(t)
	foreign, err := net.Listen(networkTCP, net.JoinHostPort("", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("foreign holder: %v", err)
	}
	h := holdPort(port)
	t.Cleanup(h.release)
	_ = foreign.Close()
	awaitArmed(t, h)
	assertPortHeld(t, port)
}

// TestPortHold_ReleaseWhileWaitingLeavesThePortFree: a guard released while
// it waits on a foreign holder stops waiting, and does not take the port when
// that holder lets go afterwards.
func TestPortHold_ReleaseWhileWaitingLeavesThePortFree(t *testing.T) {
	port := freeHoldPort(t)
	addr := net.JoinHostPort("", strconv.Itoa(port))
	foreign, err := net.Listen(networkTCP, addr)
	if err != nil {
		t.Fatalf("foreign holder: %v", err)
	}
	h := holdPort(port)
	h.release()
	_ = foreign.Close()
	ln, err := net.Listen(networkTCP, addr)
	if err != nil {
		t.Fatalf("a released guard still holds %s: %v", addr, err)
	}
	_ = ln.Close()
	select {
	case <-h.armed:
		t.Fatal("a released guard reported itself armed")
	default:
	}
}

// freeHoldPort returns a port free at every address this package's tests
// bind, for exercising portHold away from the default Noise port that
// TestMain holds.
func freeHoldPort(t *testing.T) int {
	t.Helper()
	port, err := freePortOn(raftListenHost, networkTCP)
	if err != nil {
		t.Fatalf("free port: %v", err)
	}
	return port
}

// awaitArmed fails t unless h comes to hold its port itself. It waits on the
// guard's own signal, bounded so a guard that never arms fails rather than
// hangs.
func awaitArmed(t *testing.T, h *portHold) {
	t.Helper()
	select {
	case <-h.armed:
	case <-time.After(10 * time.Second):
		t.Fatal("the guard never took the port")
	}
}

// assertPortHeld fails t if port can be bound at either address this
// package's tests bind on.
func assertPortHeld(t *testing.T, port int) {
	t.Helper()
	for _, host := range []string{testBindHost, raftListenHost} {
		addr := net.JoinHostPort(host, strconv.Itoa(port))
		ln, err := net.Listen(networkTCP, addr)
		if err == nil {
			_ = ln.Close()
			t.Fatalf("bound %s during the package's tests: holdDefaultNoisePort is not armed in TestMain, so a test that forgets reserveNoisePort would pass here and flake elsewhere", addr)
		}
	}
}
