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

package connlimit

import (
	"net"
	"sync"
	"testing"
	"time"
)

// dial opens n connections to ln's address and returns them still open;
// the caller must close them.
func dial(t *testing.T, addr string, n int) []net.Conn {
	t.Helper()
	conns := make([]net.Conn, 0, n)
	for i := 0; i < n; i++ {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			t.Fatalf("dial %d: %v", i, err)
		}
		conns = append(conns, c)
	}
	return conns
}

// acceptedConns collects every connection a test server accepts, guarded by
// a mutex since Accept runs on its own goroutine.
type acceptedConns struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (a *acceptedConns) add(c net.Conn) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conns = append(a.conns, c)
}

// closeAll closes every connection the server has accepted so far, which is
// what releases that connection's per-peer slot.
func (a *acceptedConns) closeAll() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, c := range a.conns {
		_ = c.Close()
	}
	a.conns = nil
}

// serve runs an accept loop against ln until it is closed, recording every
// accepted connection in accepted rather than closing or reading it, so a
// test controls exactly when (and whether) the server side closes.
func serve(ln net.Listener, accepted *acceptedConns) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accepted.add(c)
	}
}

func TestListenerRefusesOverLimitFromSamePeer(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer raw.Close()

	ln := New(raw, 2)
	accepted := &acceptedConns{}
	go serve(ln, accepted)

	conns := dial(t, raw.Addr().String(), 3)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	// The third connection from the same peer (127.0.0.1) must be refused:
	// the server side closes it, which the client observes as EOF on read.
	third := conns[2]
	_ = third.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	n, readErr := third.Read(buf)
	if readErr == nil || n != 0 {
		t.Fatalf("expected the over-limit connection to be closed by the server, got n=%d err=%v", n, readErr)
	}
}

func TestListenerReleasesSlotOnClose(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer raw.Close()

	ln := New(raw, 1)
	accepted := &acceptedConns{}
	go serve(ln, accepted)

	first := dial(t, raw.Addr().String(), 1)[0]
	waitForAccepted(t, accepted, 1)

	// Close the connection from the SERVER side, which is what releases the
	// per-peer slot (Listener.Accept tracks the connection it hands back,
	// not the client's end).
	accepted.closeAll()
	_ = first.Close()

	// Poll rather than sleep a fixed duration: release() runs on whichever
	// goroutine calls Close, and this asserts only that it has happened by
	// the time a second connection from the same peer is admitted.
	deadline := time.Now().Add(2 * time.Second)
	for {
		second, err := net.DialTimeout("tcp", raw.Addr().String(), time.Second)
		if err == nil {
			_ = second.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			buf := make([]byte, 1)
			_, readErr := second.Read(buf)
			netErr, ok := readErr.(net.Error)
			_ = second.Close()
			if ok && netErr.Timeout() {
				return // accepted and left open: the slot was released.
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("second connection from the same peer was never accepted after the first was closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitForAccepted blocks until the server has accepted at least n
// connections, or the test times out.
func waitForAccepted(t *testing.T, a *acceptedConns, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		a.mu.Lock()
		got := len(a.conns)
		a.mu.Unlock()
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server accepted %d connections, want at least %d", got, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListenerUnlimitedWhenMaxNonPositive(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer raw.Close()

	ln := New(raw, 0)
	accepted := &acceptedConns{}
	go serve(ln, accepted)

	conns := dial(t, raw.Addr().String(), 10)
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	for i, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		buf := make([]byte, 1)
		_, readErr := c.Read(buf)
		netErr, ok := readErr.(net.Error)
		if !ok || !netErr.Timeout() {
			t.Fatalf("connection %d: expected it to stay open (read timeout), got %v", i, readErr)
		}
	}
}

func TestPeerHostFallsBackToFullAddrWithoutPort(t *testing.T) {
	addr := fakeAddr("no-port-here")
	if got := peerHost(addr); got != "no-port-here" {
		t.Errorf("peerHost = %q, want %q", got, "no-port-here")
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }
