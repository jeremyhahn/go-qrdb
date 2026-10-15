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

package qrdbdharness

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// relayIOTimeout bounds every read a relay test expects to succeed. On
// loopback a datagram crosses the relay in microseconds; the bound only turns
// a broken relay into a failure instead of a hang.
const relayIOTimeout = 5 * time.Second

// relayFixture is a relay between test-owned sockets: target stands in for
// the node, and each client stands in for a peer.
type relayFixture struct {
	relay  *UDPRelay
	target *net.UDPConn
	front  *net.UDPAddr
}

func newRelayFixture(t *testing.T) *relayFixture {
	t.Helper()
	target := listenLoopbackUDP(t)
	relay, err := NewUDPRelay("127.0.0.1:0", target.LocalAddr().String())
	if err != nil {
		t.Fatalf("NewUDPRelay: %v", err)
	}
	t.Cleanup(func() { relay.Close() })
	front, err := net.ResolveUDPAddr(relayNetwork, relay.Addr())
	if err != nil {
		t.Fatalf("resolve relay address %s: %v", relay.Addr(), err)
	}
	return &relayFixture{relay: relay, target: target, front: front}
}

// listenLoopbackUDP binds a UDP socket on a free loopback port, closed at the
// end of the test.
func listenLoopbackUDP(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP(relayNetwork, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// send writes payload from client to the relay's front.
func (f *relayFixture) send(t *testing.T, client *net.UDPConn, payload string) {
	t.Helper()
	if _, err := client.WriteToUDP([]byte(payload), f.front); err != nil {
		t.Fatalf("send %q: %v", payload, err)
	}
}

// receive reads one datagram from conn, failing the test after relayIOTimeout.
func receive(t *testing.T, conn *net.UDPConn) (string, *net.UDPAddr) {
	t.Helper()
	buf := make([]byte, relayDatagramLimit)
	if err := conn.SetReadDeadline(time.Now().Add(relayIOTimeout)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	n, from, err := conn.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("receive on %s: %v", conn.LocalAddr(), err)
	}
	return string(buf[:n]), from
}

// addrPortOf returns conn's bound address as the relay keys sources.
func addrPortOf(t *testing.T, conn *net.UDPConn) netip.AddrPort {
	t.Helper()
	ap, err := netip.ParseAddrPort(conn.LocalAddr().String())
	if err != nil {
		t.Fatalf("parse %s: %v", conn.LocalAddr(), err)
	}
	return ap
}

// waitDropped waits until the relay has dropped at least want datagrams.
func waitDropped(t *testing.T, relay *UDPRelay, want uint64) {
	t.Helper()
	waitCount(t, "dropped", relay.Dropped, want)
}

// waitForwarded waits until the relay has forwarded at least want datagrams.
// A relay counts a datagram once its write has returned, so a reader on the
// far side can receive the datagram before the count moves; asserting the
// count straight after a receive races that increment.
func waitForwarded(t *testing.T, relay *UDPRelay, want uint64) {
	t.Helper()
	waitCount(t, "forwarded", relay.Forwarded, want)
}

// waitCount waits until count reports at least want, failing the test once
// relayIOTimeout passes without it.
func waitCount(t *testing.T, what string, count func() uint64, want uint64) {
	t.Helper()
	deadline := time.Now().Add(relayIOTimeout)
	for count() < want {
		if time.Now().After(deadline) {
			t.Fatalf("relay %s %d datagrams, want at least %d", what, count(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestUDPRelay_ForwardsBothDirections(t *testing.T) {
	f := newRelayFixture(t)
	client := listenLoopbackUDP(t)

	f.send(t, client, "request")
	got, flow := receive(t, f.target)
	if got != "request" {
		t.Fatalf("target received %q, want %q", got, "request")
	}
	if flow.Port == client.LocalAddr().(*net.UDPAddr).Port {
		t.Fatalf("target saw the client's own port %d; the relay must send from its own flow socket", flow.Port)
	}

	if _, err := f.target.WriteToUDP([]byte("reply"), flow); err != nil {
		t.Fatalf("reply: %v", err)
	}
	got, from := receive(t, client)
	if got != "reply" {
		t.Fatalf("client received %q, want %q", got, "reply")
	}
	if from.String() != f.relay.Addr() {
		t.Fatalf("reply came from %s, want the relay front %s", from, f.relay.Addr())
	}
	// Both datagrams have arrived, but each direction counts after its write
	// returns, so the counter may still trail the receive.
	waitForwarded(t, f.relay, 2)
	if n := f.relay.Forwarded(); n != 2 {
		t.Fatalf("Forwarded() = %d, want 2", n)
	}
	if n := f.relay.Dropped(); n != 0 {
		t.Fatalf("Dropped() = %d, want 0", n)
	}
}

func TestUDPRelay_KeepsOneFlowPerSource(t *testing.T) {
	f := newRelayFixture(t)
	a := listenLoopbackUDP(t)
	b := listenLoopbackUDP(t)

	f.send(t, a, "a1")
	_, flowA := receive(t, f.target)
	f.send(t, b, "b1")
	_, flowB := receive(t, f.target)
	f.send(t, a, "a2")
	_, flowA2 := receive(t, f.target)

	if flowA.String() == flowB.String() {
		t.Fatalf("two sources share flow %s", flowA)
	}
	if flowA.String() != flowA2.String() {
		t.Fatalf("one source used two flows: %s then %s", flowA, flowA2)
	}

	// Replies on each flow reach only the source that opened it.
	if _, err := f.target.WriteToUDP([]byte("to-b"), flowB); err != nil {
		t.Fatalf("reply to b: %v", err)
	}
	if got, _ := receive(t, b); got != "to-b" {
		t.Fatalf("b received %q, want %q", got, "to-b")
	}
	if _, err := f.target.WriteToUDP([]byte("to-a"), flowA); err != nil {
		t.Fatalf("reply to a: %v", err)
	}
	if got, _ := receive(t, a); got != "to-a" {
		t.Fatalf("a received %q, want %q", got, "to-a")
	}
}

func TestUDPRelay_BlockDropsInboundFromSource(t *testing.T) {
	f := newRelayFixture(t)
	blocked := listenLoopbackUDP(t)
	open := listenLoopbackUDP(t)

	f.relay.Block(addrPortOf(t, blocked))
	f.send(t, blocked, "lost")
	waitDropped(t, f.relay, 1)

	// The relay serves its front in arrival order, so once the open source's
	// datagram reaches the target the blocked one has already been judged:
	// the first datagram the target sees is the open one.
	f.send(t, open, "kept")
	if got, _ := receive(t, f.target); got != "kept" {
		t.Fatalf("target received %q, want %q (the blocked datagram leaked)", got, "kept")
	}
}

func TestUDPRelay_BlockDropsRepliesToSource(t *testing.T) {
	f := newRelayFixture(t)
	client := listenLoopbackUDP(t)

	f.send(t, client, "request")
	_, flow := receive(t, f.target)

	f.relay.Block(addrPortOf(t, client))
	if _, err := f.target.WriteToUDP([]byte("lost-reply"), flow); err != nil {
		t.Fatalf("reply: %v", err)
	}
	waitDropped(t, f.relay, 1)

	f.relay.Heal()
	if _, err := f.target.WriteToUDP([]byte("healed-reply"), flow); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if got, _ := receive(t, client); got != "healed-reply" {
		t.Fatalf("client received %q, want %q (the blocked reply leaked)", got, "healed-reply")
	}
}

func TestUDPRelay_HealRestoresInbound(t *testing.T) {
	f := newRelayFixture(t)
	client := listenLoopbackUDP(t)
	src := addrPortOf(t, client)

	f.relay.Block(src, src)
	f.relay.Block(src)
	f.send(t, client, "lost")
	waitDropped(t, f.relay, 1)

	f.relay.Heal()
	f.send(t, client, "after-heal")
	if got, _ := receive(t, f.target); got != "after-heal" {
		t.Fatalf("target received %q, want %q", got, "after-heal")
	}
}

func TestUDPRelay_BlockMatchesMappedSource(t *testing.T) {
	f := newRelayFixture(t)
	client := listenLoopbackUDP(t)
	src := addrPortOf(t, client)

	// A source written in IPv4-mapped IPv6 form blocks the same peer.
	mapped := netip.AddrPortFrom(netip.AddrFrom16(src.Addr().As16()), src.Port())
	f.relay.Block(mapped)
	if !f.relay.isBlocked(src) {
		t.Fatalf("blocking %s did not block %s", mapped, src)
	}
}

func TestUDPRelay_CloseIsIdempotentAndStopsForwarding(t *testing.T) {
	f := newRelayFixture(t)
	client := listenLoopbackUDP(t)

	f.send(t, client, "open-flow")
	receive(t, f.target)

	if err := f.relay.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := f.relay.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	before := f.relay.Forwarded()
	f.send(t, client, "after-close")
	if got := f.relay.Forwarded(); got != before {
		t.Fatalf("Forwarded() moved from %d to %d after Close", before, got)
	}
}

func TestUDPRelay_CloseReportsServeFailure(t *testing.T) {
	f := newRelayFixture(t)
	serveErr := cause
	f.relay.serveErr.Store(&serveErr)

	err := f.relay.Close()
	var relayErr *RelayError
	if !errors.As(err, &relayErr) || relayErr.Op != "serve" {
		t.Fatalf("Close() = %v, want a *RelayError for op serve", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("Close() = %v, want it to wrap %v", err, cause)
	}
}

func TestUDPRelay_CloseReportsFrontCloseFailure(t *testing.T) {
	f := newRelayFixture(t)
	// Closing the front behind the relay's back makes its own close fail,
	// and the serve loop sees net.ErrClosed, which is not a serve failure.
	if err := f.relay.front.Close(); err != nil {
		t.Fatalf("front.Close: %v", err)
	}

	err := f.relay.Close()
	var relayErr *RelayError
	if !errors.As(err, &relayErr) || relayErr.Op != "close" {
		t.Fatalf("Close() = %v, want a *RelayError for op close", err)
	}
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Close() = %v, want it to wrap net.ErrClosed", err)
	}
}

func TestNewUDPRelay_RejectsBadAddresses(t *testing.T) {
	busy := listenLoopbackUDP(t)
	tests := []struct {
		name   string
		front  string
		target string
		op     string
	}{
		{name: "unparseable target", front: "127.0.0.1:0", target: "no-port", op: "resolve target"},
		{name: "unparseable front", front: "no-port", target: "127.0.0.1:9", op: "resolve front"},
		{name: "front in use", front: busy.LocalAddr().String(), target: "127.0.0.1:9", op: "listen"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			relay, err := NewUDPRelay(tt.front, tt.target)
			if err == nil {
				relay.Close()
				t.Fatalf("NewUDPRelay(%q, %q) succeeded, want an error", tt.front, tt.target)
			}
			var relayErr *RelayError
			if !errors.As(err, &relayErr) || relayErr.Op != tt.op {
				t.Fatalf("NewUDPRelay(%q, %q) = %v, want a *RelayError for op %q", tt.front, tt.target, err, tt.op)
			}
		})
	}
}
