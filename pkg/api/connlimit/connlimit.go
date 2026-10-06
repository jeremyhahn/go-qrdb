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

// Package connlimit bounds the number of concurrent connections a single
// remote peer may hold open against a listener.
//
// golang.org/x/net/netutil.LimitListener caps the TOTAL number of
// connections a listener accepts, shared across every peer; one abusive
// peer can still exhaust that shared budget and starve every other caller.
// Counter instead keys the budget by remote IP, so the cap is a genuine
// per-peer control: a single peer opening connections past its share is
// refused while every other peer is unaffected.
//
// Counter is the shared accounting primitive. Listener applies it to a
// net.Listener (TCP, Unix); QUICListener (quic.go) applies the SAME
// primitive to a QUIC listener, so native QUIC and the QUIC-backed HTTP/3
// server account against one implementation rather than two (go-qrdb#576).
package connlimit

import (
	"net"
	"sync"
)

// Counter bounds the number of concurrent holders a single remote peer may
// have claimed at once, keyed by the host portion of a net.Addr. It carries
// no transport-specific behavior -- what happens to a refused peer (closing
// a TCP conn, closing a QUIC conn with an application error code, ...) is
// the caller's decision; Counter only answers "is this host under its cap".
type Counter struct {
	max int

	mu     sync.Mutex
	counts map[string]int
}

// NewCounter returns a Counter that enforces max concurrent holders per
// remote host. A non-positive max means unlimited: every Admit succeeds
// and its release func is a no-op.
func NewCounter(max int) *Counter {
	return &Counter{max: max, counts: make(map[string]int)}
}

// Admit attempts to claim a slot for addr's host. ok is false when that
// host is already at the limit, in which case release is nil and the
// caller must refuse the holder itself. A true result's release func is
// idempotent -- the caller may call it exactly once, from whichever path
// first observes the holder going away, without separately guarding
// against a double call.
func (c *Counter) Admit(addr net.Addr) (release func(), ok bool) {
	if c.max <= 0 {
		return func() {}, true
	}

	host := peerHost(addr)

	c.mu.Lock()
	if c.counts[host] >= c.max {
		c.mu.Unlock()
		return nil, false
	}
	c.counts[host]++
	c.mu.Unlock()

	var once sync.Once
	return func() { once.Do(func() { c.release(host) }) }, true
}

// release decrements the open-holder count for host, pruning the entry
// once it reaches zero so the map does not grow without bound across the
// lifetime of a long-running Counter.
func (c *Counter) release(host string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[host]--
	if c.counts[host] <= 0 {
		delete(c.counts, host)
	}
}

// peerHost extracts the host portion of addr, with the whole string as a
// fallback when it carries no separable port (e.g. a non-TCP address), so
// every holder is still attributed to SOME peer identity.
func peerHost(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// Listener wraps a net.Listener and refuses a new connection from a remote
// IP that already holds max open connections against it. A non-positive
// max disables the limit (every connection is accepted).
type Listener struct {
	net.Listener

	counter *Counter
}

// New returns a Listener that enforces max concurrent connections per
// remote IP on ln. A non-positive max means unlimited.
func New(ln net.Listener, max int) *Listener {
	return &Listener{Listener: ln, counter: NewCounter(max)}
}

// Accept waits for and returns the next connection whose remote peer is
// under its per-peer limit. A connection from a peer at its limit is
// closed immediately and Accept continues waiting for the next one,
// rather than returning an error that would stop the caller's accept loop.
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}

		release, ok := l.counter.Admit(conn.RemoteAddr())
		if !ok {
			_ = conn.Close()
			continue
		}

		return &trackedConn{Conn: conn, release: release}, nil
	}
}

// trackedConn releases its Counter slot exactly once, on the first Close,
// however many times Close is called.
type trackedConn struct {
	net.Conn

	release   func()
	closeOnce sync.Once
}

// Close releases this connection's slot in its peer's budget before
// delegating to the underlying connection's Close.
func (c *trackedConn) Close() error {
	c.closeOnce.Do(c.release)
	return c.Conn.Close()
}
