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
	"io"
	"net"
	"strconv"
	"testing"
)

// udpHeldTCPFree occupies a port on UDP at host while leaving the same number
// free on TCP, and returns that port.
//
// The TCP side is bound FIRST and only then released, which is what makes
// "TCP is free" a measurement rather than an assumption: a port picked at
// random could already be held on TCP by another process, and the test would
// then be asserting about a state it never established (go-qrdb#253).
func udpHeldTCPFree(t *testing.T, host string) int {
	t.Helper()
	for i := 0; i < portAttempts; i++ {
		ln, err := net.Listen(networkTCP, net.JoinHostPort(host, kernelDrawsPort))
		if err != nil {
			continue
		}
		addr := ln.Addr().String()
		_, portStr, splitErr := net.SplitHostPort(addr)
		if splitErr != nil {
			_ = ln.Close()
			t.Fatalf("SplitHostPort(%q): %v", addr, splitErr)
		}
		// Take UDP while TCP is still held, so nothing can slip into the
		// number in between, then release TCP to leave it demonstrably free.
		pc, udpErr := net.ListenPacket(networkUDP, addr)
		_ = ln.Close()
		if udpErr != nil {
			continue // UDP side already taken; this number proves nothing
		}
		t.Cleanup(func() { _ = pc.Close() })

		port, atoiErr := strconv.Atoi(portStr)
		if atoiErr != nil {
			t.Fatalf("Atoi(%q): %v", portStr, atoiErr)
		}
		return port
	}
	t.Skipf("no port on %s could be held on UDP with TCP free after %d attempts", host, portAttempts)
	return 0
}

// TestFreePortOn_RejectsAPortHeldOnUDPOnly is the mutation proof for
// go-qrdb#257.
//
// One port, one host, two acceptance rules. The rule the daemon used to apply
// -- "does it bind on TCP?" -- accepts it, which is exactly how an unusable
// number reached Server.AdminPort and failed the HTTP/3 co-bind at startup.
// The rule it applies now rejects it.
//
// The two halves must BOTH hold for this to be a proof. If the TCP-only
// reservation failed, the port was not really free on TCP and the rejection
// below would prove nothing; that is why the old rule is asserted to SUCCEED
// rather than merely assumed to.
func TestFreePortOn_RejectsAPortHeldOnUDPOnly(t *testing.T) {
	const host = "127.0.0.1"
	port := udpHeldTCPFree(t, host)
	candidate := strconv.Itoa(port)

	// The old rule: TCP alone. It hands the port out.
	got, err := reservePortOn(host, candidate, []portProbe{portProbes[networkTCP]})
	if err != nil {
		t.Fatalf("TCP-only probe of %d rejected it (%v); the port was not free on TCP, so this test proves nothing", port, err)
	}
	if got != port {
		t.Fatalf("TCP-only probe returned port %d, want %d", got, port)
	}

	// The new rule: TCP and UDP together. It must refuse the same number.
	if _, err = reservePortOn(host, candidate, []portProbe{portProbes[networkTCP], portProbes[networkUDP]}); err == nil {
		t.Fatalf("TCP+UDP probe accepted port %d, which is held on UDP; the daemon would fail the HTTP/3 co-bind on it", port)
	}
}

// TestFreePortOn_ProbesAtTheHostItIsAskedAbout pins the scope half of the
// rule: the probe binds at the host passed in, not at a hardcoded loopback.
//
// A wildcard reservation is the discriminating case. It succeeds only if the
// number is free on EVERY local address, so a helper that quietly probed
// 127.0.0.1 while the caller asked about 0.0.0.0 would certify ports that a
// wildcard listener cannot bind -- the go-qrdb#253 finding.
func TestFreePortOn_ProbesAtTheHostItIsAskedAbout(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "0.0.0.0"} {
		t.Run(host, func(t *testing.T) {
			port, err := freePortOn(host, networkTCP, networkUDP)
			if err != nil {
				t.Fatalf("freePortOn(%q, tcp, udp): %v", host, err)
			}
			addr := net.JoinHostPort(host, strconv.Itoa(port))

			// The port it returned must actually be bindable at that host on
			// both protocols at once -- which is the whole claim.
			ln, tcpErr := net.Listen(networkTCP, addr)
			if tcpErr != nil {
				t.Fatalf("TCP bind of the returned %s failed: %v", addr, tcpErr)
			}
			defer func() { _ = ln.Close() }()
			pc, udpErr := net.ListenPacket(networkUDP, addr)
			if udpErr != nil {
				t.Fatalf("UDP bind of the returned %s failed: %v", addr, udpErr)
			}
			_ = pc.Close()
		})
	}
}

// TestFreePortOn_RejectsAnUnknownNetwork: an unknown network is a coding
// error, so it fails immediately and by name rather than being retried
// twenty times and reported as a busy host.
func TestFreePortOn_RejectsAnUnknownNetwork(t *testing.T) {
	_, err := freePortOn("127.0.0.1", "sctp")
	var unknown *UnknownProbeNetworkError
	if !errors.As(err, &unknown) {
		t.Fatalf("freePortOn(sctp) error = %v, want *UnknownProbeNetworkError", err)
	}
	if unknown.Network != "sctp" {
		t.Fatalf("Network = %q, want %q", unknown.Network, "sctp")
	}
}

// TestFreePortOn_RejectsAnEmptyNetworkList: naming no network is not a
// request for any free port, it is a caller that never said what the port is
// for. There is nothing to match the listener against, so it is refused.
func TestFreePortOn_RejectsAnEmptyNetworkList(t *testing.T) {
	_, err := freePortOn("127.0.0.1")
	var empty *NoProbeNetworkError
	if !errors.As(err, &empty) {
		t.Fatalf("freePortOn() with no networks = %v, want *NoProbeNetworkError", err)
	}
	if empty.Host != "127.0.0.1" {
		t.Fatalf("Host = %q, want %q", empty.Host, "127.0.0.1")
	}
}

// TestFreePortOn_UnusableHostIsANamedError: a host no listener can bind
// exhausts the attempt budget and reports NoFreePortError carrying the last
// bind failure, rather than returning a zero port that a caller might use.
func TestFreePortOn_UnusableHostIsANamedError(t *testing.T) {
	// 203.0.113.1 is TEST-NET-3 (RFC 5737) and is not a local address, so
	// binding it fails on every attempt without depending on host state.
	port, err := freePortOn("203.0.113.1", networkTCP)
	var exhausted *NoFreePortError
	if !errors.As(err, &exhausted) {
		t.Fatalf("freePortOn(unbindable host) = (%d, %v), want *NoFreePortError", port, err)
	}
	if exhausted.Attempts != portAttempts {
		t.Fatalf("Attempts = %d, want %d", exhausted.Attempts, portAttempts)
	}
	if exhausted.Unwrap() == nil {
		t.Fatal("NoFreePortError carries no underlying bind error")
	}
}

// errWitnessPortWasFree reports that the witness probe below was able to bind
// a port an earlier probe in the same batch was supposed to still be holding.
var errWitnessPortWasFree = errors.New("qrdbd: witness bound a port an earlier probe should still hold")

// TestReservePortOn_HoldsEverySocketUntilItReturns proves the SIMULTANEITY
// property, which is what separates this helper from a sequential one.
//
// A sequential reserve -- take TCP, release it, then take UDP -- can certify a
// number that was never free on both at the same instant: another process can
// take the port on the protocol already probed and released, and the sequential
// probe, looking only forward, returns it anyway. That is the shape
// sdk/go/testutil/qrdbdharness/ports.go has, and the reason it could not be
// reused for go-qrdb#257.
//
// The proof observes the invariant FROM INSIDE the batch. The second probe is a
// witness that tries to take the candidate on TCP; the first probe is still
// holding it, so the witness must be REFUSED. Had the first probe released its
// socket before moving on, the witness would have bound the port -- so the
// witness failing is exactly the property under test.
func TestReservePortOn_HoldsEverySocketUntilItReturns(t *testing.T) {
	const host = "127.0.0.1"

	var witnessRefused bool
	witness := func(addr string) (net.Addr, io.Closer, error) {
		ln, err := net.Listen(networkTCP, addr)
		if err != nil {
			witnessRefused = true // still held by the first probe, as required
			return nil, nil, err
		}
		_ = ln.Close()
		return nil, nil, errWitnessPortWasFree
	}

	_, err := reservePortOn(host, kernelDrawsPort, []portProbe{portProbes[networkTCP], witness})
	if errors.Is(err, errWitnessPortWasFree) {
		t.Fatal("the first probe released its socket before the second ran; a port can be taken in that window and the reservation would certify it anyway")
	}
	if !witnessRefused {
		t.Fatalf("witness neither bound nor was refused the port (err = %v); the test proved nothing", err)
	}
}

// TestReservePortOn_WitnessDetectsAFreePort is the control for the test above.
//
// Without it, the witness being refused is not evidence: a witness that can
// never bind anything would "pass" the simultaneity test on a helper that
// released every socket immediately. This pins that the witness does succeed
// when the port is genuinely free, so its refusal above is about the socket
// being held.
func TestReservePortOn_WitnessDetectsAFreePort(t *testing.T) {
	const host = "127.0.0.1"

	// A port that is demonstrably free right now: bound, then released.
	ln, err := net.Listen(networkTCP, net.JoinHostPort(host, kernelDrawsPort))
	if err != nil {
		t.Skipf("could not bind a probe port on %s: %v", host, err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	witness := func(a string) (net.Addr, io.Closer, error) {
		l, werr := net.Listen(networkTCP, a)
		if werr != nil {
			return nil, nil, werr
		}
		_ = l.Close()
		return nil, nil, errWitnessPortWasFree
	}

	if _, _, err = witness(addr); !errors.Is(err, errWitnessPortWasFree) {
		t.Fatalf("witness on the free %s returned %v, want errWitnessPortWasFree; it cannot distinguish held from free, so the simultaneity test is vacuous", addr, err)
	}
}

// unparseableAddr is a net.Addr whose String form is not host:port.
type unparseableAddr struct{}

func (unparseableAddr) Network() string { return networkTCP }
func (unparseableAddr) String() string  { return "not-a-host-port" }

// TestPortOfAddr_UnparseableAddressIsANamedError: the port is read off the
// address string rather than type-asserted to *net.TCPAddr / *net.UDPAddr, so
// an address that does not parse is reported as a named error instead of
// silently falling back to a port number nobody chose.
func TestPortOfAddr_UnparseableAddressIsANamedError(t *testing.T) {
	got, err := portOfAddr(unparseableAddr{})
	var probeErr *ProbeAddrError
	if !errors.As(err, &probeErr) {
		t.Fatalf("portOfAddr(unparseable) = (%q, %v), want *ProbeAddrError", got, err)
	}
	if probeErr.Addr != "not-a-host-port" {
		t.Fatalf("ProbeAddrError.Addr = %q, want %q", probeErr.Addr, "not-a-host-port")
	}
	if probeErr.Unwrap() == nil {
		t.Fatal("ProbeAddrError carries no underlying parse error")
	}
}
