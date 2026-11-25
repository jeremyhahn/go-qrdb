package unix

import (
	"context"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
)

// peerCredCredentials wraps the socket's TLS transport credentials so that
// the kernel-reported peer credential of every accepted connection (the
// connecting process's uid, gid and pid) reaches the request context.
//
// The credential is read from the raw socket BEFORE the TLS handshake and
// carried as the connection's remote address (a *peerCredAddr), because
// grpc-go builds peer.Peer.Addr from the conn ServerHandshake returns: that
// is the one per-connection channel into the request context that leaves
// AuthInfo as credentials.TLSInfo. peerCredUnaryInterceptor and
// peerCredStreamInterceptor then bind it with auth.WithPeerCredential.
//
// A read failure (or a platform without SO_PEERCRED) leaves the connection
// unwrapped: the request carries no credential and the peer-credential
// authenticator refuses it. It never fails the handshake.
type peerCredCredentials struct {
	credentials.TransportCredentials
}

func newPeerCredCredentials(inner credentials.TransportCredentials) credentials.TransportCredentials {
	return &peerCredCredentials{TransportCredentials: inner}
}

// ServerHandshake reads the peer credential from raw, then delegates the
// TLS handshake.
func (c *peerCredCredentials) ServerHandshake(raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	cred, ok := readPeerCredential(raw)
	conn, info, err := c.TransportCredentials.ServerHandshake(raw)
	if err != nil || !ok {
		return conn, info, err
	}
	return &peerCredConn{Conn: conn, addr: &peerCredAddr{Addr: conn.RemoteAddr(), cred: cred}}, info, nil
}

// Clone keeps the wrapper.
func (c *peerCredCredentials) Clone() credentials.TransportCredentials {
	return &peerCredCredentials{TransportCredentials: c.TransportCredentials.Clone()}
}

// peerCredConn is the handshaken connection with the peer credential
// attached to its remote address.
type peerCredConn struct {
	net.Conn
	addr *peerCredAddr
}

// RemoteAddr returns the original remote address carrying the credential.
func (c *peerCredConn) RemoteAddr() net.Addr { return c.addr }

// peerCredAddr is the connection's original remote address with the peer
// credential attached. Network and String are the original address's, so
// every key derived from the address (the client-address limiter's) is
// unchanged.
type peerCredAddr struct {
	net.Addr
	cred auth.PeerCredential
}

// Network returns the original address's network.
func (a *peerCredAddr) Network() string {
	if a.Addr == nil {
		return "unix"
	}
	return a.Addr.Network()
}

// String returns the original address's string form.
func (a *peerCredAddr) String() string {
	if a.Addr == nil {
		return ""
	}
	return a.Addr.String()
}

// withPeerCredential binds the peer credential of ctx's connection, when
// the Unix transport read one, into ctx.
func withPeerCredential(ctx context.Context) context.Context {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return ctx
	}
	if a, ok := p.Addr.(*peerCredAddr); ok {
		return auth.WithPeerCredential(ctx, a.cred)
	}
	return ctx
}

// peerCredUnaryInterceptor binds the connection's peer credential into the
// request context.
func peerCredUnaryInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	return handler(withPeerCredential(ctx), req)
}

// peerCredStreamInterceptor binds the connection's peer credential into the
// stream's context.
func peerCredStreamInterceptor(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	ctx := withPeerCredential(ss.Context())
	if ctx == ss.Context() {
		return handler(srv, ss)
	}
	return handler(srv, &peerCredStream{ServerStream: ss, ctx: ctx})
}

type peerCredStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *peerCredStream) Context() context.Context { return s.ctx }
