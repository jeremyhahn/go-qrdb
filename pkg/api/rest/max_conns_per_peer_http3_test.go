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

// Tests for the HTTP/3 (QUIC) per-peer connection cap (go-qrdb#576). The TCP
// side of WithMaxConnsPerPeer already has coverage; these confirm the SAME
// option reaches the HTTP/3 listener Start builds for ServeListener, using
// the SAME connlimit accounting rather than a second implementation.
package rest

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"

	"github.com/jeremyhahn/go-qrdb/pkg/api/connlimit"
)

// TestServer_Start_MaxConnsPerPeerZero_LeavesHTTP3ListenerUnwrapped confirms
// Start does not pay for a connlimit wrapper on the HTTP/3 listener it was
// not asked for.
func TestServer_Start_MaxConnsPerPeerZero_LeavesHTTP3ListenerUnwrapped(t *testing.T) {
	tlsCfg, _ := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0", WithTLS(tlsCfg))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	if srv.http3Listener == nil {
		t.Fatal("expected an HTTP/3 listener to be built")
	}
	if _, wrapped := srv.http3Listener.(*connlimit.QUICListener); wrapped {
		t.Fatal("expected the raw quic-go listener, not a connlimit.QUICListener, when maxConnsPerPeer is unset")
	}
}

// TestIntegration_MaxConnsPerPeer_RefusesOverCapHTTP3Connection is the
// end-to-end check: with WithMaxConnsPerPeer(1), a second QUIC connection
// from the same client while the first is still open is refused with
// connlimit.QUICOverCapErrorCode, on the SAME listener the HTTP/3 server
// serves via ServeListener.
func TestIntegration_MaxConnsPerPeer_RefusesOverCapHTTP3Connection(t *testing.T) {
	tlsCfg, pool := generateTestTLSConfig(t)
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithTLS(tlsCfg),
		WithMaxConnsPerPeer(1),
	)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(srv.Stop)

	if _, wrapped := srv.http3Listener.(*connlimit.QUICListener); !wrapped {
		t.Fatal("expected the HTTP/3 listener to be wrapped in a connlimit.QUICListener when maxConnsPerPeer is set")
	}

	clientCfg := &tls.Config{
		RootCAs:    pool,
		NextProtos: []string{"h3"},
		MinVersion: tls.VersionTLS13,
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	first, err := quicgo.DialAddr(dialCtx, srv.http3Conn.LocalAddr().String(), clientCfg, &quicgo.Config{
		MaxIdleTimeout: 5 * time.Second,
	})
	cancel()
	if err != nil {
		t.Fatalf("dial first connection: %v", err)
	}
	t.Cleanup(func() { _ = first.CloseWithError(0, "test done") })

	ctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	second, err := quicgo.DialAddr(ctx, srv.http3Conn.LocalAddr().String(), clientCfg, &quicgo.Config{
		MaxIdleTimeout: 5 * time.Second,
	})
	cancel2()
	if err != nil {
		// quic-go's handshake can itself observe the refusal as a dial
		// error rather than a connection that then closes; either is an
		// acceptable manifestation of "refused".
		return
	}
	t.Cleanup(func() { _ = second.CloseWithError(0, "test done") })

	// The client's dial returns when its side of the handshake completes;
	// the server admits a connection only when its own side completes, so
	// the two can be admitted in either order. Under -race with GOFIPS140
	// the server finished the second handshake first in about one fresh
	// process in five (go-qrdb#20) and refused the first connection. The
	// cap is one per peer, so the behaviour to assert is that exactly one
	// of the two is refused and the other stays open.
	refused, admitted := second, first
	select {
	case <-second.Context().Done():
	case <-first.Context().Done():
		refused, admitted = first, second
	case <-time.After(3 * time.Second):
		t.Fatal("over-cap HTTP/3 connection was not refused within timeout")
	}
	if err := admitted.Context().Err(); err != nil {
		t.Fatalf("both connections from one peer were closed under a cap of one: %v", context.Cause(admitted.Context()))
	}

	var appErr *quicgo.ApplicationError
	closeErr := context.Cause(refused.Context())
	if !errors.As(closeErr, &appErr) {
		t.Fatalf("expected an ApplicationError closing the refused connection, got %v (%T)", closeErr, closeErr)
	}
	if appErr.ErrorCode != connlimit.QUICOverCapErrorCode {
		t.Fatalf("expected error code %d, got %d", connlimit.QUICOverCapErrorCode, appErr.ErrorCode)
	}
}
