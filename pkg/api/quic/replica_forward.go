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

package quic

import (
	"context"
	"crypto"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/replicaforward"
)

// WithReplicaForwarding makes this listener forward a request it refuses
// because the node holds no replica of the database to a node that does, on
// the caller's behalf (docs/replica-forwarding), as a wire.MethodOnBehalf
// request. Without it the listener answers the refusal. The watch stream is
// never forwarded.
func WithReplicaForwarding(cfg replicaforward.Config) ServerOption {
	return func(s *Server) {
		c := cfg
		s.replicaForwarding = &c
	}
}

// FrameForwarder forwards a QUIC request frame a node refused with
// NotAReplica to a replica, carrying a signed assertion of the caller, and
// returns the replica's answer frame. A QUIC listener forwards with one, and
// the embedded transport forwards its calls with the node's (docs/replica-
// forwarding: embedded forwards over QUIC). It is safe for concurrent use.
type FrameForwarder struct {
	cfg       replicaforward.Config
	key       crypto.Signer
	nodeID    string
	clientTLS *tls.Config

	// conns holds one connection per target endpoint, opened on first use
	// and closed with the listener.
	mu     sync.Mutex
	conns  map[string]*targetConn
	closed bool
}

// targetConn is a connection to one target over a UDP socket of its own,
// CONNECTED to the target: the kernel then reports an ICMP port-unreachable
// for it, so a target whose process died fails the next keep-alive and
// closes the connection, and the next forward dials -- and is refused at
// once -- rather than writing a request into a connection nobody answers.
type targetConn struct {
	conn *quicgo.Conn
	tr   *quicgo.Transport
	udp  *net.UDPConn
}

// close closes the connection and its socket.
func (c *targetConn) close(reason string) {
	_ = c.conn.CloseWithError(0, reason)
	_ = c.tr.Close()
	_ = c.udp.Close()
}

// forwardKeepAlive paces keep-alives on a target connection; it bounds how
// long a dead target's connection stays cached.
const forwardKeepAlive = time.Second

// connectedPacketConn presents a UDP socket connected to one peer as the
// net.PacketConn quic-go reads and writes. A connected socket refuses a
// write that names a destination, so WriteTo writes to the peer it is
// connected to; and it is deliberately not a *net.UDPConn, which quic-go
// would drive through ReadMsgUDP/WriteMsgUDP with explicit addresses. A read
// returns the ICMP refusal the kernel reports for the peer, which ends the
// transport and every connection on it.
type connectedPacketConn struct {
	udp *net.UDPConn
}

func (c connectedPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.udp.Read(p)
	return n, c.udp.RemoteAddr(), err
}

func (c connectedPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return c.udp.Write(p) }
func (c connectedPacketConn) Close() error                              { return c.udp.Close() }
func (c connectedPacketConn) LocalAddr() net.Addr                       { return c.udp.LocalAddr() }
func (c connectedPacketConn) SetDeadline(t time.Time) error             { return c.udp.SetDeadline(t) }
func (c connectedPacketConn) SetReadDeadline(t time.Time) error         { return c.udp.SetReadDeadline(t) }
func (c connectedPacketConn) SetWriteDeadline(t time.Time) error        { return c.udp.SetWriteDeadline(t) }

// NewFrameForwarder returns a forwarder for the node whose TLS identity is
// nodeTLS: its certificate is the client certificate a forward presents and
// its key signs the caller assertion.
func NewFrameForwarder(cfg replicaforward.Config, nodeTLS *tls.Config) *FrameForwarder {
	key, nodeID := forward.NodeIdentity(nodeTLS)
	return &FrameForwarder{
		cfg:    cfg,
		key:    key,
		nodeID: nodeID,
		clientTLS: &tls.Config{
			RootCAs:              cfg.RootCAs,
			Certificates:         nodeTLS.Certificates,
			GetClientCertificate: nodeTLS.GetClientCertificate,
			NextProtos:           []string{wire.ProtocolV3},
			MinVersion:           tls.VersionTLS13,
		},
		conns: map[string]*targetConn{},
	}
}

// Close closes every target connection; a forward after Close is never
// sent.
func (f *FrameForwarder) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	for ep, cc := range f.conns {
		cc.close("listener stopped")
		delete(f.conns, ep)
	}
}

// Forward forwards method on database with payload, as caller, after this
// node refused it with refusal, and returns the status and payload of the
// frame a replica answered. It returns false when no replica served it -- no
// caller, no signing identity, no target -- and the caller is then answered
// the refusal, never a local answer. A failure after the request reached a
// replica is that failure's frame.
func (f *FrameForwarder) Forward(ctx context.Context, caller *auth.Identity, method, database string, payload []byte, refusal *placement.NotAReplicaError) (uint16, []byte, bool) {
	assertion, sig, err := forward.SignCaller(caller, forward.RPCBinding("quic", method, payload), f.key, f.nodeID, time.Now())
	if err != nil {
		slog.Warn("replicaforward: not forwarded", "protocol", cluster.APIProtocolQUIC, "tenant", refusal.TenantID, "database", refusal.Database, "error", err)
		return 0, nil, false
	}
	envelope, err := wire.EncodeOnBehalf(wire.OnBehalf{Method: method, Assertion: assertion, Signature: sig, Payload: payload})
	if err != nil {
		return 0, nil, false
	}

	var (
		status uint16
		body   []byte
	)
	send := func(ctx context.Context, t replicaforward.Target) (replicaforward.Result, error) {
		stream, ok := f.stream(ctx, t.Endpoint)
		if !ok {
			return replicaforward.Result{Outcome: replicaforward.Unreachable}, nil
		}
		if dl, ok := ctx.Deadline(); ok {
			_ = stream.SetDeadline(dl)
		}
		if err := wire.WriteRequest(stream, wire.MethodOnBehalf, database, 1, envelope); err != nil {
			stream.CancelRead(0)
			stream.CancelWrite(0)
			return replicaforward.Result{}, &replicaforward.SendError{Protocol: cluster.APIProtocolQUIC, Target: t.Endpoint, Err: err}
		}
		_, st, b, err := wire.ReadResponse(stream)
		// Closing the write side after reading the whole frame is this
		// protocol's acknowledgement that the answer arrived.
		_ = stream.Close()
		if err != nil {
			return replicaforward.Result{}, &replicaforward.SendError{Protocol: cluster.APIProtocolQUIC, Target: t.Endpoint, Err: err}
		}
		if res, refused := classifyQUICAnswer(st, b); refused {
			return res, nil
		}
		status, body = st, b
		return replicaforward.Result{Outcome: replicaforward.Served}, nil
	}

	ctx, cancel := context.WithTimeout(ctx, f.cfg.ForwardTimeout())
	defer cancel()
	if _, ferr := f.cfg.Locator.Forward(ctx, refusal, cluster.APIProtocolQUIC, send); ferr != nil {
		var sendErr *replicaforward.SendError
		if errors.As(ferr, &sendErr) {
			st, b := mapServiceError(ferr)
			return st, b, true
		}
		return 0, nil, false
	}
	return status, body, true
}

// ForwardToLeader sends method/payload, unchanged, to the system shard's
// leader at leaderAddr. Unlike Forward it is not a placement-based retry
// across a replica set -- the shared system shard has none to retry across
// -- it is the single hop a leader-only write's own refusal
// (service.AdminCredentialNotLeaderError and its siblings) already names by
// address. It resolves that address through the SAME member records Forward's
// Locator reads, signs the identical on-behalf assertion, and relays the
// leader's answer. forwarded is false whenever the write was not sent (no
// forwarder, unresolvable leader, or a transport failure before any request
// reached it), and the caller falls back to its own refusal.
func (f *FrameForwarder) ForwardToLeader(ctx context.Context, caller *auth.Identity, method, database string, payload []byte, leaderAddr string) (uint16, []byte, bool) {
	if leaderAddr == "" {
		slog.Warn("admin forwarding: refusal names no leader", "method", method)
		return 0, nil, false
	}
	member, found, err := f.cfg.Locator.MemberByAddress(ctx, leaderAddr)
	if err != nil || !found {
		slog.Warn("admin forwarding: leader not found in member records", "leader_raft_addr", leaderAddr, "error", err)
		return 0, nil, false
	}
	endpoint, ok := member.APIEndpoint(cluster.APIProtocolQUIC)
	if !ok || endpoint == "" {
		slog.Warn("admin forwarding: leader advertises no QUIC endpoint", "leader_raft_addr", leaderAddr)
		return 0, nil, false
	}
	assertion, sig, err := forward.SignCaller(caller, forward.RPCBinding("quic", method, payload), f.key, f.nodeID, time.Now())
	if err != nil {
		slog.Warn("admin forwarding: not forwarded", "protocol", cluster.APIProtocolQUIC, "leader_raft_addr", leaderAddr, "error", err)
		return 0, nil, false
	}
	envelope, err := wire.EncodeOnBehalf(wire.OnBehalf{Method: method, Assertion: assertion, Signature: sig, Payload: payload})
	if err != nil {
		slog.Warn("admin forwarding: not forwarded", "leader_raft_addr", leaderAddr, "error", err)
		return 0, nil, false
	}

	fctx, cancel := context.WithTimeout(ctx, f.cfg.ForwardTimeout())
	defer cancel()
	stream, ok := f.stream(fctx, endpoint)
	if !ok {
		slog.Warn("admin forwarding: could not connect to leader", "leader_raft_addr", leaderAddr, "endpoint", endpoint)
		return 0, nil, false
	}
	if dl, ok := fctx.Deadline(); ok {
		_ = stream.SetDeadline(dl)
	}
	if err := wire.WriteRequest(stream, wire.MethodOnBehalf, database, 1, envelope); err != nil {
		stream.CancelRead(0)
		stream.CancelWrite(0)
		slog.Warn("admin forwarding: could not send to leader", "leader_raft_addr", leaderAddr, "endpoint", endpoint, "error", err)
		return 0, nil, false
	}
	_, st, b, err := wire.ReadResponse(stream)
	// Closing the write side after reading the whole frame is this
	// protocol's acknowledgement that the answer arrived.
	_ = stream.Close()
	if err != nil {
		slog.Warn("admin forwarding: could not read the leader's answer", "leader_raft_addr", leaderAddr, "endpoint", endpoint, "error", err)
		return 0, nil, false
	}
	return st, b, true
}

// stream opens a request stream to endpoint, dialing when there is no live
// connection. It returns false when none could be opened: the request then
// never left this node.
func (f *FrameForwarder) stream(ctx context.Context, endpoint string) (*quicgo.Stream, bool) {
	if cc, ok := f.cached(endpoint); ok {
		if s, err := cc.conn.OpenStreamSync(ctx); err == nil {
			return s, true
		}
		f.drop(endpoint, cc)
	}
	cc, ok := f.dial(ctx, endpoint)
	if !ok || !f.keep(endpoint, cc) {
		return nil, false
	}
	s, err := cc.conn.OpenStreamSync(ctx)
	if err != nil {
		f.drop(endpoint, cc)
		return nil, false
	}
	return s, true
}

// dial opens a connection to endpoint, within the connection budget
// (Config.ConnectTimeout) so a target that never answers the handshake leaves
// time for the one retry. A handshake that fails, as a restarting or loaded
// target's does, is made again on the reconnect schedule until one passes.
func (f *FrameForwarder) dial(ctx context.Context, endpoint string) (*targetConn, bool) {
	connect := f.cfg.ConnectTimeout()
	dctx, cancel := context.WithTimeout(ctx, connect)
	defer cancel()
	var cc *targetConn
	reached := replicaforward.Reconnect(dctx, connect, func() bool {
		var ok bool
		cc, ok = f.dialOnce(dctx, endpoint)
		return ok
	})
	return cc, reached
}

// dialOnce makes one handshake with endpoint, over a socket of its own
// connected to it.
func (f *FrameForwarder) dialOnce(ctx context.Context, endpoint string) (*targetConn, bool) {
	raddr, err := net.ResolveUDPAddr("udp", endpoint)
	if err != nil {
		return nil, false
	}
	udp, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, false
	}
	tr := &quicgo.Transport{Conn: connectedPacketConn{udp: udp}}
	conn, err := tr.Dial(ctx, raddr, f.clientTLS, &quicgo.Config{MaxIdleTimeout: 30 * time.Second, KeepAlivePeriod: forwardKeepAlive})
	if err != nil {
		_ = tr.Close()
		_ = udp.Close()
		return nil, false
	}
	return &targetConn{conn: conn, tr: tr, udp: udp}, true
}

// cached returns the live connection to endpoint.
func (f *FrameForwarder) cached(endpoint string) (*targetConn, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cc, ok := f.conns[endpoint]
	if !ok || cc.conn.Context().Err() != nil {
		return nil, false
	}
	return cc, true
}

// keep records cc as endpoint's connection, replacing any other. A closed
// forwarder keeps nothing and closes cc.
func (f *FrameForwarder) keep(endpoint string, cc *targetConn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		cc.close("listener stopped")
		return false
	}
	if old, ok := f.conns[endpoint]; ok && old != cc {
		old.close("replaced")
	}
	f.conns[endpoint] = cc
	return true
}

// drop closes cc and forgets it, if it is still endpoint's connection.
func (f *FrameForwarder) drop(endpoint string, cc *targetConn) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.conns[endpoint] == cc {
		delete(f.conns, endpoint)
	}
	cc.close("unusable")
}

// classifyQUICAnswer reports whether a replica's answer frame is a replica
// refusal -- it holds no replica either, or is a follower refusing the
// write. Any other frame is the caller's answer.
func classifyQUICAnswer(status uint16, body []byte) (replicaforward.Result, bool) {
	if status != wire.StatusUnavailable {
		return replicaforward.Result{}, false
	}
	p, err := wire.DecodeErrorPayload(body)
	if err != nil || p.Info == nil || p.Info.Domain != replicaforward.ErrorDomain {
		return replicaforward.Result{}, false
	}
	switch p.Info.Reason {
	case replicaforward.ReasonNotAReplica:
		return replicaforward.Result{Outcome: replicaforward.NotAReplica}, true
	case replicaforward.ReasonNotLeader:
		return replicaforward.Result{Outcome: replicaforward.NotLeader, LeaderRaftAddr: replicaforward.LeaderRaftAddr(p.Info.Metadata)}, true
	}
	return replicaforward.Result{}, false
}
