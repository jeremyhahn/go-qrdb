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

package apimodule

import (
	"net"
	"sync"

	"google.golang.org/grpc/credentials"
)

// Transport names a listener of the unified server, as reported to a
// ConnObserver.
type Transport string

// The listeners a ConnObserver hears from. REST is served on two: HTTP/1.1
// and HTTP/2 over TLS on TCP (TransportREST), and HTTP/3 over QUIC on the
// same port number (TransportHTTP3). They are reported separately because
// they are separate connections on separate sockets.
const (
	// TransportGRPC is the gRPC listener (TCP).
	TransportGRPC Transport = "grpc"

	// TransportUnix is the gRPC wire served over the Unix domain socket.
	TransportUnix Transport = "unix"

	// TransportREST is the REST listener over TCP (HTTP/1.1 and HTTP/2).
	TransportREST Transport = "rest"

	// TransportHTTP3 is the REST listener over QUIC (HTTP/3).
	TransportHTTP3 Transport = "http3"

	// TransportMCP is the MCP Streamable HTTP listener (TCP).
	TransportMCP Transport = "mcp"

	// TransportQUIC is the native binary QUIC listener.
	TransportQUIC Transport = "quic"
)

// ConnObserver is an optional ServiceModule extension. A module implements
// it to observe the connection lifecycle on every listener of the unified
// server — so a composing host can keep its own active-connection metrics
// for the connections qrdb accepts on its behalf, without standing up a
// parallel listener.
//
// Every connection a listener accepts is reported exactly once to
// ConnOpened and, when it ends, exactly once to ConnClosed, on the same
// Transport. That holds for a connection that fails its TLS handshake too:
// it was accepted, so it is counted and then closed.
//
// Both methods are called synchronously on the listener's accept or close
// path, so they must return promptly and must not block — increment a
// counter, never do I/O. They may be called concurrently, from many
// connections at once.
type ConnObserver interface {
	// ConnOpened reports a connection accepted on t.
	ConnOpened(t Transport)

	// ConnClosed reports the end of a connection previously reported to
	// ConnOpened on t.
	ConnClosed(t Transport)
}

// ConnObservers is the set of registered modules that observe connections.
// The zero value observes nothing, and every method on it is then a no-op
// that adds no cost to the accept path.
type ConnObservers []ConnObserver

// ConnObserversOf collects the modules in mods that implement ConnObserver,
// in registration order.
func ConnObserversOf(mods []ServiceModule) ConnObservers {
	var out ConnObservers
	for _, m := range mods {
		if o, ok := m.(ConnObserver); ok {
			out = append(out, o)
		}
	}
	return out
}

// Open reports a connection opened on t to every observer and returns the
// function that reports its close. The returned function reports at most
// once however many times it is called, so every close path of a
// connection may call it.
func (o ConnObservers) Open(t Transport) (closed func()) {
	if len(o) == 0 {
		return func() {}
	}
	for _, obs := range o {
		obs.ConnOpened(t)
	}
	return sync.OnceFunc(func() {
		for _, obs := range o {
			obs.ConnClosed(t)
		}
	})
}

// Listener wraps ln so every connection it accepts is reported opened on t
// when accepted and closed on its first Close. It returns ln unchanged when
// there are no observers.
//
// Wrap the RAW listener, beneath any tls.NewListener: the TLS layer then
// wraps the observed connection, so the server still sees a *tls.Conn and
// closing it closes — and reports — the observed connection underneath.
//
// Use it only for a server that does not configure the accepted socket
// itself (net/http: REST and MCP). A gRPC server does — see Credentials.
func (o ConnObservers) Listener(ln net.Listener, t Transport) net.Listener {
	if len(o) == 0 {
		return ln
	}
	return &observedListener{Listener: ln, observers: o, transport: t}
}

// Credentials wraps a gRPC server's transport credentials so every connection
// they handshake is reported opened on t, and closed on its first Close. It
// returns c unchanged when there are no observers.
//
// It is how gRPC-wire listeners (gRPC and unix) are observed, INSTEAD of
// Listener. grpc-go configures the accepted socket itself — it sets
// TCP_USER_TIMEOUT only when the conn it accepted is a *net.TCPConn — so a
// wrapped listener silently disables that (the observed default fell from
// 20000ms to 0). Wrapping the credentials leaves the accepted socket exactly
// as grpc-go received it and observes only the conn the handshake returns.
//
// The open is reported when the handshake starts, so a connection that fails
// its handshake is still counted, and its close is reported with the failure.
func (o ConnObservers) Credentials(c credentials.TransportCredentials, t Transport) credentials.TransportCredentials {
	if len(o) == 0 {
		return c
	}
	return &observedCredentials{TransportCredentials: c, observers: o, transport: t}
}

// observedCredentials reports every server handshake to its observers.
type observedCredentials struct {
	credentials.TransportCredentials
	observers ConnObservers
	transport Transport
}

// ServerHandshake reports the connection opened, handshakes it, and returns
// a conn that reports its close. A failed handshake reports the close at once.
func (c *observedCredentials) ServerHandshake(raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	closed := c.observers.Open(c.transport)
	conn, info, err := c.TransportCredentials.ServerHandshake(raw)
	if err != nil {
		closed()
		return nil, nil, err
	}
	return &observedConn{Conn: conn, closed: closed}, info, nil
}

// Clone clones the wrapped credentials, keeping the observation.
func (c *observedCredentials) Clone() credentials.TransportCredentials {
	return &observedCredentials{
		TransportCredentials: c.TransportCredentials.Clone(),
		observers:            c.observers,
		transport:            c.transport,
	}
}

// observedListener reports each accepted connection to its observers.
type observedListener struct {
	net.Listener
	observers ConnObservers
	transport Transport
}

// Accept accepts the next connection and reports it opened.
func (l *observedListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &observedConn{Conn: c, closed: l.observers.Open(l.transport)}, nil
}

// observedConn reports its own close exactly once.
type observedConn struct {
	net.Conn
	closed func()
}

// Close closes the connection and reports it closed.
func (c *observedConn) Close() error {
	err := c.Conn.Close()
	c.closed()
	return err
}
