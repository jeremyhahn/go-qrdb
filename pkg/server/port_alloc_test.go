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

// Dual-stack test port allocation (go-qrdb#248).
//
// The REST listener takes a TCP port and the HTTP/3 listener then binds THE
// SAME NUMBER ON UDP. TCP and UDP are separate port namespaces, so a port the
// kernel hands out as a free TCP port says nothing about whether UDP is free
// on it -- and a test running in parallel in this package holds ephemeral UDP
// ports (its own QUIC listener, for one). Asking for AdminPort: 0 therefore
// races: the kernel gives us TCP port P, another test already holds UDP P, and
// the HTTP/3 bind fails.
//
// That failure used to be one log line inside a goroutine; go-qrdb#165 made it
// a *rest.HTTP3StartError returned from Start, which is correct and is why the
// race became visible. The fix belongs in how the tests choose a port, not in
// softening the error or disabling HTTP/3.
package server

import (
	"errors"
	"net"
	"strconv"
	"testing"
)

// dualStackPortAttempts bounds the search. Each attempt is two binds and two
// closes; a handful is ample because a collision needs a concurrent holder of
// that exact UDP port, and the kernel does not hand out the same ephemeral TCP
// port twice in the window.
const dualStackPortAttempts = 20

// errNoDualStackPort is returned when no candidate was free on both stacks.
var errNoDualStackPort = errors.New("server: no port free on both TCP and UDP")

// freeDualStackPortFrom returns the first candidate port that is free on BOTH
// TCP and UDP, asking candidate() for successive suggestions.
//
// The candidate supplier is a parameter so the selection behaviour itself can
// be tested: a test can offer a port whose UDP side is held and assert that
// this function skips it rather than returning it. Verifying only that "the
// suite passes" would assert nothing about the selection.
func freeDualStackPortFrom(candidate func() (int, error), attempts int) (int, error) {
	for i := 0; i < attempts; i++ {
		port, err := candidate()
		if err != nil {
			continue
		}
		if dualStackFree(port) {
			return port, nil
		}
	}
	return 0, errNoDualStackPort
}

// dualStackFree reports whether port binds on both TCP and UDP right now. Both
// sockets are opened before either is closed, so the pair is proven
// simultaneously available rather than one-then-the-other.
func dualStackFree(port int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	defer func() { _ = ln.Close() }()
	pc, err := net.ListenPacket("udp", addr)
	if err != nil {
		return false
	}
	_ = pc.Close()
	return true
}

// ephemeralTCPCandidate asks the kernel for a free TCP port and releases it.
func ephemeralTCPCandidate() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port //nolint:errcheck // always *net.TCPAddr for a tcp listener
	if err := ln.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

// freeDualStackPort returns a port free on both TCP and UDP, for a test that
// starts a REST listener whose HTTP/3 sibling shares the port number.
//
// A residual race remains between this returning and the server binding, and
// it is deliberately not papered over: it is far smaller than the one it
// replaces, because the failure mode being removed is a port the kernel
// considers free on TCP while a long-lived UDP socket in another test holds it
// for that test's whole duration.
func freeDualStackPort(t *testing.T) int {
	t.Helper()
	port, err := freeDualStackPortFrom(ephemeralTCPCandidate, dualStackPortAttempts)
	if err != nil {
		t.Fatalf("freeDualStackPort: %v", err)
	}
	return port
}

// ---------------------------------------------------------------------------
// Selection behaviour
// ---------------------------------------------------------------------------

// TestFreeDualStackPortFrom_SkipsPortHeldOnUDP is the #248 assertion. It offers
// a candidate whose TCP side is free and whose UDP side is held -- the exact
// condition a parallel test creates -- and requires the allocator to reject it
// and move on. This asserts the SELECTION, not that some test passed once.
func TestFreeDualStackPortFrom_SkipsPortHeldOnUDP(t *testing.T) {
	held, err := ephemeralTCPCandidate()
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	udpHolder, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(held)))
	if err != nil {
		t.Fatalf("hold udp %d: %v", held, err)
	}
	defer func() { _ = udpHolder.Close() }()

	// Confirm the premise: TCP really is free on that port.
	if ln, lerr := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(held))); lerr != nil {
		t.Fatalf("premise failed, TCP %d not free: %v", held, lerr)
	} else {
		_ = ln.Close()
	}

	offered := 0
	got, err := freeDualStackPortFrom(func() (int, error) {
		offered++
		if offered == 1 {
			return held, nil // the poisoned candidate
		}
		return ephemeralTCPCandidate()
	}, dualStackPortAttempts)
	if err != nil {
		t.Fatalf("freeDualStackPortFrom: %v", err)
	}
	if got == held {
		t.Fatalf("allocator returned port %d, which is held on UDP; it must skip a TCP-only-free port", held)
	}
	if offered < 2 {
		t.Fatalf("allocator asked for %d candidate(s); it must have rejected the first and asked again", offered)
	}
	if !dualStackFree(got) {
		t.Fatalf("returned port %d is not free on both stacks", got)
	}
}

// TestFreeDualStackPortFrom_FailsClosedWhenNothingIsFree: the search is bounded
// and reports rather than looping or returning a port it never validated.
func TestFreeDualStackPortFrom_FailsClosedWhenNothingIsFree(t *testing.T) {
	held, err := ephemeralTCPCandidate()
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	udpHolder, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(held)))
	if err != nil {
		t.Fatalf("hold udp: %v", err)
	}
	defer func() { _ = udpHolder.Close() }()

	attempts := 0
	got, err := freeDualStackPortFrom(func() (int, error) {
		attempts++
		return held, nil // never free on UDP
	}, 5)
	if !errors.Is(err, errNoDualStackPort) {
		t.Fatalf("err = %v, want errNoDualStackPort", err)
	}
	if got != 0 {
		t.Errorf("returned port %d alongside an error, want 0", got)
	}
	if attempts != 5 {
		t.Errorf("made %d attempts, want exactly the bound of 5", attempts)
	}
}

// TestStartRESTWithHTTP3_OnDualStackPort is the end-to-end form: a REST server
// started on a port chosen this way brings up both the TCP listener and its
// HTTP/3 sibling. With a TCP-only port pick this is the call that failed in CI.
func TestStartRESTWithHTTP3_OnDualStackPort(t *testing.T) {
	cfg := testConfigFull(t, protocolFlags{rest: true}, "")
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithTLS(selfSignedTLSConfig(t)))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	if srv.RESTAddr() == "" {
		t.Fatal("REST listener reported no address")
	}
}
