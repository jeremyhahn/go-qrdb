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
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// preboundNodeConfig is a standalone node that serves REST and gRPC — the two
// transports whose ports default to fixed numbers — and nothing else.
func preboundNodeConfig(t *testing.T) *qrdbconfig.Config {
	t.Helper()
	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Seal.Enabled = false
	cfg.Server.Host = "127.0.0.1"
	cfg.Protocols.REST = true
	cfg.Protocols.GRPC = true
	cfg.Protocols.QUIC = false
	cfg.Protocols.Unix = false
	cfg.Protocols.MCP = false
	return cfg
}

// TestNode_TwoNodesFromPreboundListeners is go-qrdb#374's verification: two
// nodes in one process, each handed kernel-assigned sockets the test bound,
// start without colliding — both configs name the SAME fixed admin and gRPC
// ports, so the only way both start is that neither binds its configured port.
func TestNode_TwoNodesFromPreboundListeners(t *testing.T) {
	type sockets struct {
		rest, grpc net.Listener
		h3         net.PacketConn
	}
	bind := func() sockets {
		rest, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		h3, err := net.ListenPacket("udp", rest.Addr().String())
		require.NoError(t, err, "bind HTTP/3 beside the REST listener")
		grpc, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		return sockets{rest: rest, grpc: grpc, h3: h3}
	}

	// The transports are TLS-only; one server certificate serves both nodes.
	tlsCfg, pool := preboundServerTLS(t)

	for range 2 {
		s := bind()
		n, err := NewStandaloneNode(context.Background(), preboundNodeConfig(t), []server.ServerOption{
			server.WithTLS(tlsCfg),
			server.WithRESTListener(s.rest),
			server.WithRESTPacketConn(s.h3),
			server.WithGRPCListener(s.grpc),
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = n.Close() })

		srv := n.Server()
		require.NotNil(t, srv)
		require.Equal(t, s.rest.Addr().String(), srv.RESTAddr(), "REST must serve on the handed-over listener")
		require.Equal(t, s.grpc.Addr().String(), srv.GRPCAddr(), "gRPC must serve on the handed-over listener")
		require.True(t, restHealthy(pool, srv.RESTAddr()), "REST must answer on the handed-over listener")
		conn, err := net.Dial("tcp", srv.GRPCAddr())
		require.NoError(t, err, "dial gRPC")
		_ = conn.Close()
	}
}

// TestNode_RaftPortZeroRefused pins the one socket that cannot be pre-bound:
// go-quicraft binds the Raft transport itself and peers dial the advertised
// address, so a multi-node replica on port 0 is refused rather than started
// advertising ":0".
func TestNode_RaftPortZeroRefused(t *testing.T) {
	cfg := tuningTestConfig(t)
	for _, tc := range []struct {
		field        string
		raft, listen string
	}{
		{field: "raft.raft_address", raft: "127.0.0.1:0", listen: "127.0.0.1:63901"},
		{field: "raft.listen_address", raft: "127.0.0.1:63901", listen: "0.0.0.0:0"},
	} {
		cfg.Raft.RaftAddress = tc.raft
		cfg.Raft.ListenAddress = tc.listen
		_, _, _, err := buildClusteredRaftConfigs(cfg, threeMembers(), nil)
		var cfgErr *ConfigError
		require.True(t, errors.As(err, &cfgErr), "err = %v, want *ConfigError", err)
		require.Equal(t, tc.field, cfgErr.Field)
	}

	// A single-member cluster has no transport, so its address is never
	// bound and port 0 there is not refused.
	cfg.Raft.RaftAddress = "127.0.0.1:0"
	cfg.Raft.ListenAddress = ""
	_, _, _, err := buildClusteredRaftConfigs(cfg, singleMember(), nil)
	require.NoError(t, err)
}

// TestBuildNodeServerConfig_PortZeroIsTheFixedDefault pins the documented
// Port=0 behaviour: without a pre-bound listener, a zero admin or gRPC port
// selects the fixed default, NOT a kernel-assigned port. A caller that wants a
// kernel-assigned port pre-binds it (server.WithRESTListener /
// server.WithGRPCListener).
func TestBuildNodeServerConfig_PortZeroIsTheFixedDefault(t *testing.T) {
	cfg := &qrdbconfig.Config{}
	got := buildNodeServerConfig(cfg)
	require.Equal(t, 63001, got.Server.AdminPort)
	require.Equal(t, 63002, got.Server.GRPCPort)
	require.Equal(t, 0, cfg.Server.AdminPort, "the caller's config is not modified")
}

// preboundServerTLS issues a server certificate from a fresh test CA and
// returns the server's TLS config and a client pool that trusts it.
func preboundServerTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	pki := t.TempDir()
	ca, err := qrdbdharness.NewCA(pki)
	require.NoError(t, err)
	leaf, err := ca.Issue(filepath.Join(pki, "server"), qrdbdharness.WithSubject("node", "server"), qrdbdharness.WithServerAuth())
	require.NoError(t, err)
	cert, err := leaf.TLSCertificate()
	require.NoError(t, err)
	caPEM, err := os.ReadFile(ca.CertPath)
	require.NoError(t, err)
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM(caPEM))
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}, pool
}

// restHealthy reports whether GET /health answers 200 over HTTPS at addr.
func restHealthy(pool *x509.CertPool, addr string) bool {
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://" + addr + "/health")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// staleH3TimerWindow is how long HTTP/3 is re-checked after the migration. It
// is NOT a budget a correct build must meet — a correct build answers for any
// length — but the window the defect needs: the ended server's QUIC stack
// moves the socket's read deadline about three probe timeouts after it closed
// the connection held across the migration, well under a second on loopback.
const staleH3TimerWindow = 1500 * time.Millisecond

// TestNode_PreboundListenersSurviveMigration is the review's B2: a system
// encryption migration tears the data plane down and builds a NEW server from
// the same options. The pre-bound sockets must still be open for it — a
// server's Stop ends its use of them without closing them — so REST answers on
// the very same port afterwards. Node.Close then really closes them.
func TestNode_PreboundListenersSurviveMigration(t *testing.T) {
	tlsCfg, pool := preboundServerTLS(t)
	restLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	h3, err := net.ListenPacket("udp", restLn.Addr().String())
	require.NoError(t, err, "bind HTTP/3 beside the REST listener")
	grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	cfg := preboundNodeConfig(t)
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{
		server.WithTLS(tlsCfg),
		server.WithRESTListener(restLn),
		server.WithRESTPacketConn(h3),
		server.WithGRPCListener(grpcLn),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })

	addr := restLn.Addr().String()
	require.True(t, restHealthy(pool, addr), "REST must answer before the migration")
	require.True(t, h3Healthy(pool, addr), "HTTP/3 must answer before the migration")
	require.NoError(t, grpcAnswers(pool, grpcLn.Addr().String()), "gRPC must answer before the migration")

	// An HTTP/3 connection held open across the migration: the old server's
	// QUIC stack keeps a timer for it after its lifetime ends, and the timer
	// must not reach the socket the rebuilt server is serving on.
	live := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	defer func() { _ = live.Close() }()
	liveClient := &http.Client{Timeout: 5 * time.Second, Transport: live}
	resp, err := liveClient.Get("https://" + addr + "/health")
	require.NoError(t, err, "open the HTTP/3 connection held across the migration")
	_ = resp.Body.Close()

	require.NoError(t, n.EnableEncryption(context.Background(), service.BarrierScope{Kind: service.ScopeSystem}, seal.MigrationOptions{
		TargetStrategy: seal.StrategyPassphrase,
		Mechanism:      seal.MechanismSwap,
		Availability:   seal.AvailabilityQuiesced,
		Credentials:    seal.MustCredentials(context.Background(), []byte("prebound-survives-migration")),
	}))

	srv := n.Server()
	require.NotNil(t, srv, "the migration must rebuild the server")
	require.Equal(t, addr, srv.RESTAddr(), "the rebuilt server must serve on the same pre-bound listener")
	require.Equal(t, grpcLn.Addr().String(), srv.GRPCAddr())
	require.True(t, restHealthy(pool, addr), "REST must still answer on the same port after the migration")
	require.True(t, h3Healthy(pool, addr), "HTTP/3 must still answer on the same pre-bound socket after the migration")
	// Keep asking for longer than the ended lifetime's closed-connection
	// timer runs: HTTP/3 must keep answering after it fires.
	for start := time.Now(); time.Since(start) < staleH3TimerWindow; time.Sleep(100 * time.Millisecond) {
		require.True(t, h3Healthy(pool, addr), "HTTP/3 stopped answering %v after the migration", time.Since(start))
	}
	require.NoError(t, grpcAnswers(pool, grpcLn.Addr().String()), "gRPC must still answer on the same port after the migration")

	require.NoError(t, n.Close())
	_, err = restLn.Accept()
	require.ErrorIs(t, err, net.ErrClosed, "Node.Close must close the pre-bound REST listener")
	_, err = grpcLn.Accept()
	require.ErrorIs(t, err, net.ErrClosed, "Node.Close must close the pre-bound gRPC listener")
	_, err = h3.WriteTo([]byte{0}, h3.LocalAddr())
	require.ErrorIs(t, err, net.ErrClosed, "Node.Close must close the pre-bound HTTP/3 socket")
}

// TestNode_ConstructorErrorClosesPreboundSockets pins that a constructor that
// refuses to build a node closes the sockets its caller handed over: the
// caller gave them up, and no Node exists whose Close would.
func TestNode_ConstructorErrorClosesPreboundSockets(t *testing.T) {
	t.Run("clustered, refused for a Raft port 0", func(t *testing.T) {
		restLn, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		cfg := preboundNodeConfig(t)
		cfg.Raft.RaftAddress = "127.0.0.1:0"
		members := map[uint64]string{1: "127.0.0.1:0", 2: "127.0.0.2:63001"}
		_, err = NewClusteredNode(context.Background(), cfg, members, []server.ServerOption{server.WithRESTListener(restLn)})
		require.Error(t, err)
		require.ErrorIs(t, acceptErr(restLn), net.ErrClosed, "a refused NewClusteredNode must close the pre-bound listener")
	})
	t.Run("standalone, refused for a missing data directory", func(t *testing.T) {
		grpcLn, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		cfg := preboundNodeConfig(t)
		cfg.Raft.DataDir = ""
		cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
		_, err = NewStandaloneNode(context.Background(), cfg, []server.ServerOption{server.WithGRPCListener(grpcLn)})
		require.Error(t, err)
		require.ErrorIs(t, acceptErr(grpcLn), net.ErrClosed, "a refused NewStandaloneNode must close the pre-bound listener")
	})
}

// acceptErr returns the error Accept gives on ln: net.ErrClosed for a closed
// listener, and a timeout — rather than a hang — for one left open.
func acceptErr(ln net.Listener) error {
	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
	conn, err := ln.Accept()
	if conn != nil {
		_ = conn.Close()
	}
	return err
}

// h3Healthy reports whether GET /health answers 200 over HTTP/3 at addr.
func h3Healthy(pool *x509.CertPool, addr string) bool {
	rt := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	defer func() { _ = rt.Close() }()
	client := &http.Client{Timeout: 5 * time.Second, Transport: rt}
	resp, err := client.Get("https://" + addr + "/health")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// grpcAnswers makes a real Health RPC over gRPC at addr and returns nil when
// the server ANSWERED it. The client presents no certificate, so the node's
// auth interceptor answers Unauthenticated: that status is produced by the
// server's RPC stack on the served socket, which is what is under test (a
// dead or unbound listener yields Unavailable instead).
func grpcAnswers(pool *x509.CertPool, addr string) error {
	err := grpcHealthy(pool, addr)
	if status.Code(err) == codes.Unauthenticated {
		return nil
	}
	return err
}

// grpcHealthy makes a Health RPC over gRPC at addr.
func grpcHealthy(pool *x509.CertPool, addr string) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(
		credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13})))
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = pb.NewQRDBServiceClient(conn).Health(ctx, &emptypb.Empty{})
	return err
}
