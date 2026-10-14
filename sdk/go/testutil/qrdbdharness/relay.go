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
	"sync"
	"sync/atomic"
)

// relayDatagramLimit is the largest datagram the relay forwards: the UDP
// payload ceiling, so no QUIC packet a node sends is ever truncated.
const relayDatagramLimit = 64 * 1024

// relayNetwork is the network every relay socket uses. Harnessed nodes listen
// on IPv4 loopback, and pinning the family keeps every source address the
// relay reports in the 4-byte form a test writes.
const relayNetwork = "udp4"

// UDPRelay is a test-controlled UDP path in front of one node's Raft
// listener. The node binds its real listener with --listen and advertises the
// relay's address with --raft-address, and every peer names it by that
// address in --peers, so every datagram a peer exchanges with the node
// crosses the relay. A test can then cut and restore that path without
// touching the host: no firewall rule, no traffic control, no privilege.
//
// The relay keeps one upstream socket per peer source address, as a NAT keeps
// one mapping per flow, so the node sees each peer as a distinct flow and its
// replies reach the peer that sent the request.
//
// A blocked source loses every datagram in both directions: what it sends
// toward the node and what the node sends back to it. A source address
// identifies a peer node because go-quicraft dials from the same UDP socket it
// listens on, so a peer's datagrams always come from its --listen address.
type UDPRelay struct {
	front  *net.UDPConn
	target *net.UDPAddr

	// blocked is replaced wholesale and never mutated in place, so both
	// forwarding directions read it without a lock.
	blocked atomic.Pointer[map[netip.AddrPort]struct{}]

	forwarded atomic.Uint64
	dropped   atomic.Uint64

	// serveErr records why the front socket stopped serving, when it stopped
	// for any reason other than Close.
	serveErr atomic.Pointer[error]

	closeOnce sync.Once
	closeErr  error
	wg        sync.WaitGroup
}

// NewUDPRelay binds front and starts relaying every datagram it receives to
// target, and every reply back to the sender. front may name port 0 to take
// any free port; Addr reports the bound address.
func NewUDPRelay(front, target string) (*UDPRelay, error) {
	targetAddr, err := net.ResolveUDPAddr(relayNetwork, target)
	if err != nil {
		return nil, &RelayError{Op: "resolve target", Addr: target, Err: err}
	}
	frontAddr, err := net.ResolveUDPAddr(relayNetwork, front)
	if err != nil {
		return nil, &RelayError{Op: "resolve front", Addr: front, Err: err}
	}
	conn, err := net.ListenUDP(relayNetwork, frontAddr)
	if err != nil {
		return nil, &RelayError{Op: "listen", Addr: front, Err: err}
	}
	r := &UDPRelay{front: conn, target: targetAddr}
	r.blocked.Store(&map[netip.AddrPort]struct{}{})
	r.wg.Add(1)
	go r.serveFront()
	return r, nil
}

// Addr returns the relay's bound front address, the address peers dial.
func (r *UDPRelay) Addr() string {
	return r.front.LocalAddr().String()
}

// Block drops every datagram from and to each of sources until Heal. Blocking
// a source that is already blocked is a no-op.
func (r *UDPRelay) Block(sources ...netip.AddrPort) {
	for {
		cur := r.blocked.Load()
		next := make(map[netip.AddrPort]struct{}, len(*cur)+len(sources))
		for src := range *cur {
			next[src] = struct{}{}
		}
		for _, src := range sources {
			next[unmapAddrPort(src)] = struct{}{}
		}
		if r.blocked.CompareAndSwap(cur, &next) {
			return
		}
	}
}

// Heal lifts every block, restoring the path for every source.
func (r *UDPRelay) Heal() {
	r.blocked.Store(&map[netip.AddrPort]struct{}{})
}

// Forwarded reports how many datagrams the relay has delivered, in either
// direction.
func (r *UDPRelay) Forwarded() uint64 { return r.forwarded.Load() }

// Dropped reports how many datagrams the relay has discarded, in either
// direction, because their source was blocked or a socket refused them.
func (r *UDPRelay) Dropped() uint64 { return r.dropped.Load() }

// Close stops the relay and waits for every forwarding goroutine to exit. It
// returns a *RelayError when the front socket had already failed while
// serving, since a relay that stopped on its own cut the path without the test
// asking. Close is idempotent.
func (r *UDPRelay) Close() error {
	r.closeOnce.Do(func() {
		closeErr := r.front.Close()
		r.wg.Wait()
		switch errp := r.serveErr.Load(); {
		case errp != nil:
			r.closeErr = &RelayError{Op: "serve", Addr: r.Addr(), Err: *errp}
		case closeErr != nil:
			r.closeErr = &RelayError{Op: "close", Addr: r.Addr(), Err: closeErr}
		}
	})
	return r.closeErr
}

// isBlocked reports whether src is cut off.
func (r *UDPRelay) isBlocked(src netip.AddrPort) bool {
	_, blocked := (*r.blocked.Load())[src]
	return blocked
}

// serveFront reads every datagram arriving at the front, opens an upstream
// flow for each new source, and forwards. It alone owns the session table, so
// the table needs no lock; on exit it closes every upstream socket, which is
// what stops their reply goroutines.
func (r *UDPRelay) serveFront() {
	defer r.wg.Done()
	sessions := make(map[netip.AddrPort]*net.UDPConn)
	defer func() {
		for _, up := range sessions {
			// The relay is stopping and UDP holds nothing unsent, so a
			// failed close loses no datagram and has no caller to tell.
			_ = up.Close()
		}
	}()
	buf := make([]byte, relayDatagramLimit)
	for {
		n, src, err := r.front.ReadFromUDPAddrPort(buf)
		if err != nil {
			// An unconnected UDP socket reports no per-peer errors, so any
			// failure other than Close is the socket itself giving out.
			if !errors.Is(err, net.ErrClosed) {
				r.serveErr.Store(&err)
			}
			return
		}
		src = unmapAddrPort(src)
		if r.isBlocked(src) {
			r.dropped.Add(1)
			continue
		}
		up, ok := sessions[src]
		if !ok {
			up, err = net.DialUDP(relayNetwork, nil, r.target)
			if err != nil {
				r.dropped.Add(1)
				continue
			}
			sessions[src] = up
			r.wg.Add(1)
			go r.serveUpstream(up, src)
		}
		if _, err := up.Write(buf[:n]); err != nil {
			r.dropped.Add(1)
			continue
		}
		r.forwarded.Add(1)
	}
}

// serveUpstream returns the target's replies on one flow to the source that
// opened it, until serveFront closes the flow.
func (r *UDPRelay) serveUpstream(up *net.UDPConn, src netip.AddrPort) {
	defer r.wg.Done()
	buf := make([]byte, relayDatagramLimit)
	for {
		n, err := up.Read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// A connected UDP socket reports an ICMP port-unreachable as a
			// read error while the target is down -- a node being restarted.
			// The flow is still good once the target is back.
			r.dropped.Add(1)
			continue
		}
		if r.isBlocked(src) {
			r.dropped.Add(1)
			continue
		}
		if _, err := r.front.WriteToUDPAddrPort(buf[:n], src); err != nil {
			r.dropped.Add(1)
			continue
		}
		r.forwarded.Add(1)
	}
}

// unmapAddrPort normalises an IPv4-mapped IPv6 address to its IPv4 form, so a
// source reads the same however the socket reported it.
func unmapAddrPort(ap netip.AddrPort) netip.AddrPort {
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}
