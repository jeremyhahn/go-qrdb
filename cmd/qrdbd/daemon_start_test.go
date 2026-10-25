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
	"errors"
	"fmt"
	"net"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
)

// lostPortAttempts bounds how many fresh port draws startDaemon makes for one
// daemon. A draw is lost only when another process takes one of its numbers in
// the window between netport.FreePortOn releasing it and the daemon binding it, so a
// second draw almost always succeeds. The bound is what keeps a daemon that
// collides with ITSELF -- two of its own listeners on one number, which every
// draw reproduces -- failing on the collision instead of retrying forever.
// It is not tighter because a sealed boot has up to six such windows (raft,
// admin, gRPC, HTTP/3, Noise, and the admin port again when the sealed window
// hands it to the regular server), and it costs no time: every attempt shares
// one readiness deadline.
const lostPortAttempts = 10

// daemonPorts is one draw of the ports a test hands a daemon, each probed
// where and on what its listener binds: raft on UDP at raftListenHost, admin on
// TCP and UDP (REST and its HTTP/3 sibling) and gRPC on TCP, both at
// testBindHost.
type daemonPorts struct {
	raft  int
	admin int
	grpc  int
}

// drawDaemonPorts draws one set of daemon ports.
func drawDaemonPorts() (daemonPorts, error) {
	raft, err := netport.FreePortOn(raftListenHost, netport.NetworkUDP)
	if err != nil {
		return daemonPorts{}, err
	}
	admin, err := netport.FreePortOn(testBindHost, netport.NetworkTCP, netport.NetworkUDP)
	if err != nil {
		return daemonPorts{}, err
	}
	grpc, err := netport.FreePortOn(testBindHost, netport.NetworkTCP)
	if err != nil {
		return daemonPorts{}, err
	}
	return daemonPorts{raft: raft, admin: admin, grpc: grpc}, nil
}

// fullStartFlags is the single-node static start every full-startup test in
// this package begins from: pebble, auth off, REST and gRPC on, every listener
// on a port of p and bound at testBindHost. Tests set what they are about on
// top of it, and reserve the Noise port once they have. The tenant trust
// domain is the one resolveStartFlags yields when nothing configures it.
func fullStartFlags(dir string, p daemonPorts) *startFlags {
	raftAddr := net.JoinHostPort(raftAdvertiseHost, strconv.Itoa(p.raft))
	return &startFlags{
		dataDir:            dir,
		smType:             "pebble",
		discoveryMethod:    "static",
		peers:              []string{"1=" + raftAddr},
		listen:             net.JoinHostPort(raftListenHost, strconv.Itoa(p.raft)),
		raftAddress:        raftAddr,
		shardID:            1,
		replicaID:          1,
		deploymentID:       1,
		adminPort:          p.admin,
		grpcPort:           p.grpc,
		enableREST:         true,
		enableGRPC:         true,
		host:               testBindHost,
		clusterTrustDomain: qrdbconfig.DefaultClusterTrustDomain,
	}
}

// adminReady reports that the admin listener of p accepts. Any process holding
// the port satisfies it, including the one that took it from the daemon, so it
// is for the sealed window only, which has no hook; a daemon that serves is
// awaited with servingHook instead.
func adminReady(p daemonPorts) func() bool {
	return listenerReady(net.JoinHostPort(testBindHost, strconv.Itoa(p.admin)))
}

// hookFired is a readiness predicate for a startup hook that reports itself
// on fired: it turns true at the first report and stays true, so the poll
// that observed the hook and the one that returns are allowed to differ. Build
// it inside the launch: it discards a report left by an attempt that reached
// the hook and then lost a port, which would otherwise pass for this one's.
func hookFired(fired <-chan struct{}) func() bool {
	select {
	case <-fired:
	default:
	}
	var seen atomic.Bool
	return func() bool {
		select {
		case <-fired:
			seen.Store(true)
		default:
		}
		return seen.Load()
	}
}

// servingHook installs serverServing for the rest of t and returns the
// channel it reports on. Awaited with hookFired, it is exact: the hook runs
// once srv.Start has bound every listener, where a dial of one port passes
// while the next is still unbound, or when another process holds the port.
func servingHook(t *testing.T) <-chan struct{} {
	t.Helper()
	serving := make(chan struct{}, 1)
	orig := serverServing
	t.Cleanup(func() { serverServing = orig })
	serverServing = func() {
		select {
		case serving <- struct{}{}:
		default:
		}
	}
	return serving
}

// runStartLaunch reserves f's Noise port, at the host f now names, and returns
// the launch that runs runStart on f.
func runStartLaunch(t *testing.T, f *startFlags, ready func() bool) daemonLaunch {
	t.Helper()
	reserveNoisePort(t, f)
	return daemonLaunch{run: func() error { return runStart(f) }, ready: ready}
}

// lostPort reports whether err is a listener bind that found its port taken.
func lostPort(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

// retryLostPorts calls attempt with a fresh draw of ports until attempt returns
// anything other than a lost port, at most attempts times, and returns what the
// last attempt returned. Only a lost port is retried: every other failure is
// the test's to report, on the draw that produced it.
func retryLostPorts(attempts int, draw func() (daemonPorts, error), attempt func(daemonPorts) error) error {
	var err error
	for i := 0; i < attempts; i++ {
		p, drawErr := draw()
		if drawErr != nil {
			return drawErr
		}
		if err = attempt(p); !lostPort(err) {
			return err
		}
	}
	return err
}

// daemonExitedError is a daemon that returned before its test saw it ready.
// It wraps what the daemon returned, so a start that lost a port is still a
// lost port to lostPort.
type daemonExitedError struct {
	After time.Duration
	Err   error
}

func (e *daemonExitedError) Error() string {
	return fmt.Sprintf("runStart returned after %v, before becoming ready: %v", e.After, e.Err)
}

func (e *daemonExitedError) Unwrap() error { return e.Err }

// daemonLaunch is one attempt at a daemon: run blocks for the daemon's whole
// life and returns what it returns, and ready reports that it has started.
type daemonLaunch struct {
	run   func() error
	ready func() bool
}

// startDaemon launches the daemon launch builds from a fresh draw of ports and
// returns, once that daemon is ready, the channel its result arrives on.
//
// Every port a test hands a daemon comes from netport.FreePortOn, which proves a number
// free and then releases it, and the daemon binds it some hundreds of
// milliseconds later. Anything else running in the same `go test` invocation
// can take the number in between -- another package's listener, or an
// outbound connection's source port -- and the daemon then exits with
// EADDRINUSE before it is ready (go-qrdb run 51450). The daemon has no way to
// accept a listener bound in advance on every port it opens (raft's QUIC
// socket, the Noise listener and the HTTP/3 sibling among them), so the window
// cannot be closed from here. What can be done is to recognise its one
// symptom: a daemon that exits with a lost port before it is ready is started
// again on a fresh draw, at most lostPortAttempts times. A daemon that exits
// for any other reason, or never becomes ready, fails the test as before.
//
// launch runs on the test goroutine once per attempt, so everything it
// derives from the ports -- the flags, the Noise port, the readiness probe --
// is rebuilt with them.
func startDaemon(t *testing.T, launch func(p daemonPorts) daemonLaunch) <-chan error {
	t.Helper()
	limit := readyDeadline(t)
	var started <-chan error
	err := retryLostPorts(lostPortAttempts, drawDaemonPorts, func(p daemonPorts) error {
		l := launch(p)
		done := make(chan error, 1)
		go func() { done <- l.run() }()
		if exited := waitReady(t, done, l.ready, limit); exited != nil {
			if lostPort(exited) {
				t.Logf("daemon lost a port to another process, starting it on fresh ports: %v", exited)
			}
			return exited
		}
		started = done
		return nil
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	return started
}

// occupiedTCP holds a loopback TCP port for the rest of the test and returns
// its number, so a bind of that number collides deterministically.
func occupiedTCP(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen(netport.NetworkTCP, net.JoinHostPort(testBindHost, "0"))
	if err != nil {
		t.Fatalf("hold a port: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("held listener reports %T, want *net.TCPAddr", ln.Addr())
	}
	return addr.Port
}

// bindGRPC binds the draw's gRPC port the way the daemon's gRPC listener does.
func bindGRPC(p daemonPorts) error {
	ln, err := net.Listen(netport.NetworkTCP, net.JoinHostPort(testBindHost, strconv.Itoa(p.grpc)))
	if err != nil {
		return err
	}
	return ln.Close()
}

// A draw whose port another process takes before the bind is retried on a fresh
// draw, and the fresh draw is what the caller gets.
func TestRetryLostPorts_RedrawsAPortTakenBeforeTheBind(t *testing.T) {
	taken := occupiedTCP(t)
	draws := 0
	draw := func() (daemonPorts, error) {
		draws++
		if draws == 1 {
			return daemonPorts{grpc: taken}, nil
		}
		return drawDaemonPorts()
	}
	var bound []int
	err := retryLostPorts(lostPortAttempts, draw, func(p daemonPorts) error {
		bound = append(bound, p.grpc)
		return bindGRPC(p)
	})
	if err != nil {
		t.Fatalf("retryLostPorts = %v, want nil after a fresh draw", err)
	}
	if len(bound) != 2 || bound[0] != taken || bound[1] == taken {
		t.Fatalf("attempts bound %v, want the taken port %d and then a fresh one", bound, taken)
	}
}

// A collision every draw reproduces is not retried forever: after the bound,
// the collision itself is returned.
func TestRetryLostPorts_StopsAtTheBound(t *testing.T) {
	taken := occupiedTCP(t)
	attempts := 0
	err := retryLostPorts(lostPortAttempts, func() (daemonPorts, error) {
		return daemonPorts{grpc: taken}, nil
	}, func(p daemonPorts) error {
		attempts++
		return bindGRPC(p)
	})
	if !lostPort(err) {
		t.Fatalf("retryLostPorts = %v, want the EADDRINUSE of the last attempt", err)
	}
	if attempts != lostPortAttempts {
		t.Fatalf("made %d attempts, want %d", attempts, lostPortAttempts)
	}
}

// Only a lost port is retried. Success and any other failure end the search on
// the first attempt.
func TestRetryLostPorts_RetriesNothingButALostPort(t *testing.T) {
	other := errors.New("startup failed")
	for _, want := range []error{nil, other, &daemonExitedError{Err: other}} {
		attempts := 0
		err := retryLostPorts(lostPortAttempts, drawDaemonPorts, func(daemonPorts) error {
			attempts++
			return want
		})
		if err != want || attempts != 1 {
			t.Errorf("attempt returning %v: got %v after %d attempts, want it after 1", want, err, attempts)
		}
	}
}

// A failed draw ends the search with the draw's error: there are no ports to
// hand the daemon.
func TestRetryLostPorts_ReturnsADrawFailure(t *testing.T) {
	drawErr := &netport.NoProbeNetworkError{Host: testBindHost}
	err := retryLostPorts(lostPortAttempts, func() (daemonPorts, error) {
		return daemonPorts{}, drawErr
	}, func(daemonPorts) error {
		t.Fatal("attempt ran without ports")
		return nil
	})
	if err != drawErr {
		t.Fatalf("retryLostPorts = %v, want the draw's error", err)
	}
}

// The daemon's own report of a port taken from under it is what lostPort
// classifies. This is the shape of go-qrdb run 51450: the gRPC port went to
// another process and runStart returned before becoming ready.
func TestRunStart_ALostGRPCPortIsALostPort(t *testing.T) {
	p, err := drawDaemonPorts()
	if err != nil {
		t.Fatalf("draw ports: %v", err)
	}
	p.grpc = occupiedTCP(t)
	f := fullStartFlags(t.TempDir(), p)
	reserveNoisePort(t, f)

	done := make(chan error, 1)
	go func() { done <- runStart(f) }()
	guardDaemonShutdown(t, done)
	exited := waitReady(t, done, func() bool { return false }, readyDeadline(t))
	if exited == nil || !lostPort(exited) {
		t.Fatalf("runStart on a taken gRPC port returned %v, want a lost port", exited)
	}
	resetSignalHandlers(syscall.SIGINT, syscall.SIGTERM)
}

// A daemon that loses a port before it is ready is started again on fresh
// ports, and the caller gets the daemon that became ready.
func TestStartDaemon_StartsAgainOnALostPort(t *testing.T) {
	stop := make(chan struct{})
	var launches []daemonPorts
	done := startDaemon(t, func(p daemonPorts) daemonLaunch {
		launches = append(launches, p)
		if len(launches) == 1 {
			return daemonLaunch{
				run:   func() error { return &net.OpError{Op: "listen", Err: syscall.EADDRINUSE} },
				ready: func() bool { return false },
			}
		}
		return daemonLaunch{
			run:   func() error { <-stop; return nil },
			ready: func() bool { return true },
		}
	})
	if len(launches) != 2 || launches[0] == launches[1] {
		t.Fatalf("launches = %v, want two, on different draws", launches)
	}
	close(stop)
	if err := <-done; err != nil {
		t.Fatalf("the ready daemon returned %v, want nil", err)
	}
}

// A daemon that returns early for any other reason, nil included, is reported
// as having exited, not as ready.
func TestWaitReady_ReportsAnEarlyReturn(t *testing.T) {
	for _, want := range []error{nil, errors.New("startup failed")} {
		done := make(chan error, 1)
		done <- want
		exited := waitReady(t, done, func() bool { return false }, readyDeadline(t))
		if exited == nil || exited.Err != want {
			t.Errorf("early return of %v: waitReady = %v, want it reported", want, exited)
		}
		// The result is gone from done, so the guard must not wait for it.
		if !daemonHasReturned(t, done) {
			t.Errorf("early return of %v was not recorded for the shutdown guard", want)
		}
	}
}

// Once the daemon is ready, waitReady reports nothing and leaves its result on
// done for the caller.
func TestWaitReady_LeavesAReadyDaemonRunning(t *testing.T) {
	done := make(chan error, 1)
	if exited := waitReady(t, done, func() bool { return true }, readyDeadline(t)); exited != nil {
		t.Fatalf("waitReady on a ready daemon = %v, want nil", exited)
	}
	done <- nil
	if err := <-done; err != nil {
		t.Fatalf("done = %v", err)
	}
}
