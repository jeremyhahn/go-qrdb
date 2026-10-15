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

package server

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/net/ipv4"
)

// Pre-bound sockets (WithRESTListener, WithRESTPacketConn, WithGRPCListener,
// WithQUICPacketConn, WithMCPListener) outlive any one Server. An embedded node rebuilds its Server in process —
// an encryption migration tears the data plane down and builds a new one from
// the same options — so the socket a caller handed over must still be open
// for the second lifetime (go-qrdb#374). Each lifetime therefore serves on a
// wrapper whose Close ends THAT lifetime's use of the socket without closing
// the socket itself. The socket is really closed only by whoever owns it
// across lifetimes (the SDK node's Close, via PreboundSockets) or by Start
// when it refuses to serve at all.

// deadlineListener is a listener whose blocked Accept can be released by a
// deadline, which is how a lifetime's wrapper stops accepting without closing
// the shared socket. *net.TCPListener and *net.UnixListener satisfy it.
type deadlineListener interface {
	net.Listener
	SetDeadline(t time.Time) error
}

// lifetime is the state every lifetime wrapper shares: whether this lifetime
// is over, and which of its blocking calls (Accept, packet reads) are still in
// the shared socket. Ending a lifetime wakes those calls with a past deadline,
// waits for every one to return, and only then clears the deadline, so no
// call from an ended lifetime is still inside the socket when the next one
// starts — it would otherwise compete with the next lifetime for its
// connections or datagrams — and the next lifetime does not inherit a
// deadline that has already passed.
type lifetime struct {
	mu     sync.Mutex
	closed bool
	calls  sync.WaitGroup
}

// begin registers a blocking call. It reports false once the lifetime is
// over, when the call must not touch the socket.
func (l *lifetime) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return false
	}
	l.calls.Add(1)
	return true
}

// ended reports whether the lifetime ended while a call was in the socket:
// whatever that call got belongs to the next lifetime.
func (l *lifetime) ended() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.closed
}

// setDeadline applies set to the shared socket for as long as the lifetime
// is open, and does nothing once it has ended. The lock is held across the
// call, so a deadline set while the lifetime was open lands before end takes
// over the socket's deadline, never after it.
func (l *lifetime) setDeadline(set func(time.Time) error, t time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	return set(t)
}

// end closes the lifetime once, waking its calls through setDeadline and
// waiting for them before clearing the deadline.
func (l *lifetime) end(setDeadline func(time.Time) error) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()

	if err := setDeadline(time.Now()); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	l.calls.Wait()
	if err := setDeadline(time.Time{}); err != nil && !errors.Is(err, net.ErrClosed) {
		return err
	}
	return nil
}

// lifetimeListener is one Server lifetime's view of a pre-bound listener.
// Close marks the lifetime over and releases any Accept blocked in it; the
// underlying listener stays open for the next lifetime.
type lifetimeListener struct {
	inner deadlineListener
	life  lifetime
}

func newLifetimeListener(protocol string, ln net.Listener) (*lifetimeListener, error) {
	dl, ok := ln.(deadlineListener)
	if !ok {
		return nil, &PreboundSocketError{Protocol: protocol, Reason: "a pre-bound listener must support SetDeadline (a *net.TCPListener does)"}
	}
	return &lifetimeListener{inner: dl}, nil
}

// Accept accepts on the shared listener until this lifetime is closed.
func (l *lifetimeListener) Accept() (net.Conn, error) {
	if !l.life.begin() {
		return nil, net.ErrClosed
	}
	defer l.life.calls.Done()

	conn, err := l.inner.Accept()
	if l.life.ended() {
		// A connection that arrived as the lifetime ended belongs to the
		// next one; this lifetime's server is stopping and would drop it.
		// The client sees a reset and retries.
		if conn != nil {
			_ = conn.Close()
		}
		return nil, net.ErrClosed
	}
	return conn, err
}

// Close ends this lifetime. The shared listener is not closed.
func (l *lifetimeListener) Close() error { return l.life.end(l.inner.SetDeadline) }

// Addr returns the shared listener's address.
func (l *lifetimeListener) Addr() net.Addr { return l.inner.Addr() }

// lifetimePacketConn is one Server lifetime's view of a pre-bound packet
// socket. quic-go, its only reader, ends a server on a socket it did not
// create by moving the socket's read deadline into the past and never moving
// it back, and it stops reading only when it next observes that deadline.
// Close therefore ends the lifetime the same way lifetimeListener does:
// wake the reads, wait for them to leave the socket, clear the deadline.
//
// The ended lifetime's quic-go transport does not stop touching the socket at
// Close, either: a connection it closed keeps a timer (about three probe
// timeouts) that moves the read deadline into the past again when it fires,
// by which time the socket belongs to the next lifetime, whose reads would all
// time out. So SetReadDeadline and SetDeadline reach the socket only while
// this lifetime is open; afterwards they do nothing, and only end — which
// calls the socket directly — moves its deadline.
type lifetimePacketConn struct {
	net.PacketConn
	life lifetime
}

// ReadFrom reads a datagram on the shared socket until this lifetime is
// closed. A datagram read as the lifetime ended is dropped; QUIC retransmits.
func (c *lifetimePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if !c.life.begin() {
		return 0, nil, net.ErrClosed
	}
	defer c.life.calls.Done()
	n, addr, err := c.PacketConn.ReadFrom(p)
	if c.life.ended() {
		return 0, nil, net.ErrClosed
	}
	return n, addr, err
}

// SetReadDeadline sets the shared socket's read deadline while this lifetime
// is open, and does nothing once it has ended.
func (c *lifetimePacketConn) SetReadDeadline(t time.Time) error {
	return c.life.setDeadline(c.PacketConn.SetReadDeadline, t)
}

// SetDeadline sets the shared socket's deadlines while this lifetime is open,
// and does nothing once it has ended.
func (c *lifetimePacketConn) SetDeadline(t time.Time) error {
	return c.life.setDeadline(c.PacketConn.SetDeadline, t)
}

// Close ends this lifetime. The shared socket is not closed.
func (c *lifetimePacketConn) Close() error { return c.life.end(c.PacketConn.SetReadDeadline) }

// lifetimeUDPConn is lifetimePacketConn for a *net.UDPConn. It embeds the
// concrete type so quic-go still finds the OOB capabilities it looks for by
// type assertion, and it supplies the batch reader quic-go prefers to
// building its own over the file descriptor, so every read quic-go makes
// passes through this lifetime. Its deadline setters are inert once the
// lifetime has ended, for the reason given on lifetimePacketConn.
type lifetimeUDPConn struct {
	*net.UDPConn
	batch *ipv4.PacketConn
	life  lifetime
}

// ReadBatch reads datagrams on the shared socket until this lifetime is
// closed. Datagrams read as the lifetime ended are dropped; QUIC retransmits.
func (c *lifetimeUDPConn) ReadBatch(ms []ipv4.Message, flags int) (int, error) {
	if !c.life.begin() {
		return 0, net.ErrClosed
	}
	defer c.life.calls.Done()
	n, err := c.batch.ReadBatch(ms, flags)
	if c.life.ended() {
		return 0, net.ErrClosed
	}
	return n, err
}

// ReadMsgUDP reads one datagram on the shared socket until this lifetime is
// closed.
func (c *lifetimeUDPConn) ReadMsgUDP(b, oob []byte) (n, oobn, flags int, addr *net.UDPAddr, err error) {
	if !c.life.begin() {
		return 0, 0, 0, nil, net.ErrClosed
	}
	defer c.life.calls.Done()
	n, oobn, flags, addr, err = c.UDPConn.ReadMsgUDP(b, oob)
	if c.life.ended() {
		return 0, 0, 0, nil, net.ErrClosed
	}
	return n, oobn, flags, addr, err
}

// ReadFrom reads one datagram on the shared socket until this lifetime is
// closed.
func (c *lifetimeUDPConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if !c.life.begin() {
		return 0, nil, net.ErrClosed
	}
	defer c.life.calls.Done()
	n, addr, err := c.UDPConn.ReadFrom(p)
	if c.life.ended() {
		return 0, nil, net.ErrClosed
	}
	return n, addr, err
}

// SetReadDeadline sets the shared socket's read deadline while this lifetime
// is open, and does nothing once it has ended.
func (c *lifetimeUDPConn) SetReadDeadline(t time.Time) error {
	return c.life.setDeadline(c.UDPConn.SetReadDeadline, t)
}

// SetDeadline sets the shared socket's deadlines while this lifetime is open,
// and does nothing once it has ended.
func (c *lifetimeUDPConn) SetDeadline(t time.Time) error {
	return c.life.setDeadline(c.UDPConn.SetDeadline, t)
}

// Close ends this lifetime. The shared socket is not closed.
func (c *lifetimeUDPConn) Close() error { return c.life.end(c.UDPConn.SetReadDeadline) }

// newLifetimePacketConn wraps pc for one Server lifetime.
func newLifetimePacketConn(pc net.PacketConn) net.PacketConn {
	if udp, ok := pc.(*net.UDPConn); ok {
		return &lifetimeUDPConn{UDPConn: udp, batch: ipv4.NewPacketConn(udp)}
	}
	return &lifetimePacketConn{PacketConn: pc}
}

// PreboundSockets returns the sockets handed over by WithRESTListener,
// WithRESTPacketConn, WithGRPCListener, WithQUICPacketConn and
// WithMCPListener among opts. A caller that builds
// successive Servers from the same options owns these across every lifetime
// and closes them when it is done — a Server never closes them on Stop. The
// SDK node does this in Node.Close.
func PreboundSockets(opts ...ServerOption) []io.Closer {
	var o serverOptions
	for _, opt := range opts {
		opt(&o)
	}
	return o.preboundSockets()
}

func (o *serverOptions) preboundSockets() []io.Closer {
	var out []io.Closer
	if o.restListener != nil {
		out = append(out, o.restListener)
	}
	if o.restPacketConn != nil {
		out = append(out, o.restPacketConn)
	}
	if o.grpcListener != nil {
		out = append(out, o.grpcListener)
	}
	if o.quicPacketConn != nil {
		out = append(out, o.quicPacketConn)
	}
	if o.mcpListener != nil {
		out = append(out, o.mcpListener)
	}
	return out
}

// closePreboundSockets really closes every pre-bound socket. Start calls it
// when it refuses to serve, so a socket the caller handed over is not leaked.
func (s *Server) closePreboundSockets() {
	for _, c := range s.opts.preboundSockets() {
		_ = c.Close()
	}
}

// checkPreboundSockets refuses a pre-bound socket for a protocol this Server
// will not start: it would sit bound and unserved with nothing to say so.
func (s *Server) checkPreboundSockets() error {
	if !s.config.Protocols.REST && (s.opts.restListener != nil || s.opts.restPacketConn != nil) {
		return &PreboundSocketError{Protocol: "rest", Reason: "the REST protocol is disabled"}
	}
	if !s.config.Protocols.GRPC && s.opts.grpcListener != nil {
		return &PreboundSocketError{Protocol: "grpc", Reason: "the gRPC protocol is disabled"}
	}
	if !s.config.Protocols.QUIC && s.opts.quicPacketConn != nil {
		return &PreboundSocketError{Protocol: "quic", Reason: "the QUIC protocol is disabled"}
	}
	if !s.config.Protocols.MCP && s.opts.mcpListener != nil {
		return &PreboundSocketError{Protocol: "mcp", Reason: "the MCP protocol is disabled"}
	}
	return nil
}

// PreboundSocketError is returned by Start when a pre-bound socket cannot be
// served: its protocol is disabled, or the listener cannot be shared across
// server lifetimes.
type PreboundSocketError struct {
	// Protocol is the transport the socket was handed to.
	Protocol string
	// Reason says why it was refused.
	Reason string
}

// Error returns a human-readable description.
func (e *PreboundSocketError) Error() string {
	return "server: pre-bound " + e.Protocol + " socket refused: " + e.Reason
}
