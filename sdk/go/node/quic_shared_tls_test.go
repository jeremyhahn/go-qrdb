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

//go:build !integration

package node

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"slices"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/internal/netport"
	"github.com/jeremyhahn/go-qrdb/pkg/api/quic/wire"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
)

// dialNativeQUIC dials the native QUIC listener at addr offering exactly
// protos.
func dialNativeQUIC(pool *x509.CertPool, addr string, protos ...string) (*quicgo.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return quicgo.DialAddr(ctx, addr,
		&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13, NextProtos: protos},
		&quicgo.Config{MaxIdleTimeout: 5 * time.Second})
}

// nativeQUICAnswers sends one admin.Health frame on conn and returns the
// request ID of the response frame the listener wrote back. The client
// presents no certificate, so the node may answer with an error status:
// a response frame at all is what proves the connection is served.
func nativeQUICAnswers(t *testing.T, conn *quicgo.Conn) uint64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := conn.OpenStreamSync(ctx)
	require.NoError(t, err, "open a stream")
	require.NoError(t, stream.SetDeadline(time.Now().Add(5*time.Second)))
	require.NoError(t, wire.WriteRequest(stream, "admin.Health", "", 741, nil))
	require.NoError(t, stream.Close())
	id, _, _, err := wire.ReadResponse(stream)
	require.NoError(t, err, "read the response frame")
	return id
}

// TestNode_NativeQUICUnderSharedHookedTLS builds the node the way go-quicbaas
// composes it (go-qrdb#741): ONE *tls.Config serves both REST and the native
// QUIC listener, and carries the composer's own GetConfigForClient hook,
// whose per-connection config offers h2 and http/1.1. The node wires its
// clienttrust.Pool over that hook. The native listener must negotiate
// qrdb-quic/3 and serve, refuse a client offering only h2, and leave both
// configs -- and so REST's h2 -- as the composer built them.
func TestNode_NativeQUICUnderSharedHookedTLS(t *testing.T) {
	base, pool := preboundServerTLS(t)
	httpProtos := []string{"h2", "http/1.1"}
	perConn := base.Clone()
	perConn.NextProtos = slices.Clone(httpProtos)
	shared := base.Clone()
	shared.NextProtos = slices.Clone(httpProtos)
	shared.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		return perConn, nil
	}

	restLn, h3, err := netport.ListenPair("127.0.0.1:0")
	require.NoError(t, err, "bind the REST listener and HTTP/3 on one port")
	quicPC, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)

	cfg := preboundNodeConfig(t)
	cfg.Protocols.GRPC = false
	cfg.Protocols.QUIC = true
	n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{
		server.WithTLS(shared),
		server.WithRESTListener(restLn),
		server.WithRESTPacketConn(h3),
		server.WithQUICPacketConn(quicPC),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	quicAddr := quicPC.LocalAddr().String()

	conn, err := dialNativeQUIC(pool, quicAddr, wire.ProtocolV3)
	require.NoError(t, err, "a client offering %s must complete the handshake", wire.ProtocolV3)
	t.Cleanup(func() { _ = conn.CloseWithError(0, "test done") })
	require.Equal(t, wire.ProtocolV3, conn.ConnectionState().TLS.NegotiatedProtocol)
	require.Equal(t, uint64(741), nativeQUICAnswers(t, conn), "the native listener must serve the connection")

	refused, err := dialNativeQUIC(pool, quicAddr, "h2")
	if err == nil {
		_ = refused.CloseWithError(0, "test done")
		t.Fatalf("a client offering only h2 completed a native QUIC handshake, negotiating %q",
			refused.ConnectionState().TLS.NegotiatedProtocol)
	}

	require.Equal(t, httpProtos, perConn.NextProtos, "the composer's per-connection config was mutated")
	require.Equal(t, httpProtos, shared.NextProtos, "the shared config was mutated")

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
			ForceAttemptHTTP2: true,
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + restLn.Addr().String() + "/health")
	require.NoError(t, err, "REST must still negotiate after native QUIC handshakes on the shared config")
	_ = resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, 2, resp.ProtoMajor, "REST must still negotiate h2 on the shared config")
}
