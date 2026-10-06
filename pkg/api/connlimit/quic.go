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
	"context"
	"net"

	quicgo "github.com/quic-go/quic-go"
)

// QUICOverCapErrorCode is the QUIC application error code a connection is
// closed with when its remote host is already at the per-peer connection
// cap. The refusal happens before any stream exists, so this is a
// CONNECTION_CLOSE code, not a stream error -- a client that hits it sees
// a closed connection, not a reset stream.
const QUICOverCapErrorCode quicgo.ApplicationErrorCode = 0x51524442 // ASCII "QRDB", deliberately recognizable on the wire.

// QUICAcceptor is the minimal shape of a QUIC listener this package can
// wrap: accept a connection, report the bound address, and be closed. It
// is satisfied directly by *quic.Transport.Listen's result (native QUIC's
// own listener, pkg/api/quic) and is the exact shape http3.QUICListener
// requires (REST's HTTP/3 server, via Server.ServeListener) -- the two
// callers' needs are identical, which is why one wrapper type below
// serves both.
type QUICAcceptor interface {
	Accept(context.Context) (*quicgo.Conn, error)
	Addr() net.Addr
	Close() error
}

// QUICListener wraps a QUICAcceptor and refuses a new connection from a
// remote host that already holds max open connections against it, using
// the same per-peer Counter as Listener (TCP). A non-positive max means
// unlimited.
type QUICListener struct {
	QUICAcceptor

	counter *Counter
}

// NewQUICListener returns a QUICListener that enforces max concurrent
// connections per remote host on ln. A non-positive max means unlimited.
func NewQUICListener(ln QUICAcceptor, max int) *QUICListener {
	return &QUICListener{QUICAcceptor: ln, counter: NewCounter(max)}
}

// Accept waits for and returns the next connection whose remote peer is
// under its per-peer limit. A connection from a peer at its limit is
// closed immediately with QUICOverCapErrorCode and Accept continues
// waiting for the next one, matching Listener's TCP behavior rather than
// returning an error that would stop the caller's accept loop.
//
// The admitted slot is released when the connection's own context ends --
// however it ends: idle timeout, an explicit local or peer CloseWithError,
// or a transport error -- rather than when some caller-specific "done with
// this connection" event fires, since this type has no visibility into
// that and quic-go guarantees the context ends exactly once per
// connection.
func (l *QUICListener) Accept(ctx context.Context) (*quicgo.Conn, error) {
	for {
		conn, err := l.QUICAcceptor.Accept(ctx)
		if err != nil {
			return nil, err
		}

		release, ok := l.counter.Admit(conn.RemoteAddr())
		if !ok {
			_ = conn.CloseWithError(QUICOverCapErrorCode, "too many connections from this peer")
			continue
		}

		context.AfterFunc(conn.Context(), release)
		return conn, nil
	}
}
