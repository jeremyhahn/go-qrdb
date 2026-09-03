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
	"context"
	"crypto/tls"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
)

// TestAutoProvision_GRPC_ServesTLS13ChainedToCA proves that a gRPC transport
// started with NO explicit TLS config — only a CeremonyCA — comes up WITH TLS:
// a client dialing it negotiates TLS 1.3 and receives a leaf certificate that
// chains to (is issued by) the node's internal CA.
func TestAutoProvision_GRPC_ServesTLS13ChainedToCA(t *testing.T) {
	cfg := testConfig(t, false, true) // gRPC only
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn, err := tls.Dial("tcp", srv.GRPCAddr(), &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("TLS dial to auto-provisioned gRPC listener failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	state := conn.ConnectionState()
	if state.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS version = %#x, want TLS 1.3 (%#x)", state.Version, tls.VersionTLS13)
	}
	if len(state.PeerCertificates) == 0 {
		t.Fatal("server presented no certificate")
	}

	caCert, err := testServerCA.CACertificate()
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	leaf := state.PeerCertificates[0]
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("leaf certificate is not signed by the internal CA: %v", err)
	}
}

// TestAutoProvision_GRPC_RefusesPlaintext proves that once the gRPC transport
// is auto-provisioned with TLS, a plaintext (non-TLS) dial to the same listener
// is refused rather than served in the clear.
func TestAutoProvision_GRPC_RefusesPlaintext(t *testing.T) {
	cfg := testConfig(t, false, true) // gRPC only
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	conn, err := grpc.NewClient(srv.GRPCAddr(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client := pb.NewQRDBServiceClient(conn)
	if _, err := client.Health(ctx, nil); err == nil {
		t.Fatal("expected plaintext Health RPC to fail against a TLS-only gRPC listener")
	}
}

// TestAutoProvision_Unix_ServesTLS13ChainedToCA proves the same TLS-only,
// auto-provisioned posture for the Unix domain socket transport: the server is
// started with only a CeremonyCA, and a client that dials the socket and drives
// a TLS handshake negotiates TLS 1.3 and verifies a leaf chained to the CA.
func TestAutoProvision_Unix_ServesTLS13ChainedToCA(t *testing.T) {
	socketPath := tempUnixSocket(t)
	cfg := testConfigFull(t, protocolFlags{unix: true}, socketPath)
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	raw, err := net.Dial("unix", srv.UnixAddr())
	if err != nil {
		t.Fatalf("dial unix socket: %v", err)
	}
	defer func() { _ = raw.Close() }()

	tconn := tls.Client(raw, &tls.Config{
		RootCAs:    testServerCAPool(t),
		ServerName: "localhost",
		MinVersion: tls.VersionTLS13,
	})
	if err := tconn.Handshake(); err != nil {
		t.Fatalf("TLS handshake over auto-provisioned unix socket failed: %v", err)
	}

	state := tconn.ConnectionState()
	if state.Version != tls.VersionTLS13 {
		t.Fatalf("negotiated TLS version = %#x, want TLS 1.3 (%#x)", state.Version, tls.VersionTLS13)
	}
	caCert, err := testServerCA.CACertificate()
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	if len(state.PeerCertificates) == 0 {
		t.Fatal("server presented no certificate")
	}
	if err := state.PeerCertificates[0].CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("unix leaf certificate is not signed by the internal CA: %v", err)
	}
}

// TestAutoProvision_NoCA_FailsClosed proves that when neither an explicit TLS
// config nor a CeremonyCA is available, a transport start fails closed with a
// *TLSUnavailableError rather than binding a plaintext listener.
func TestAutoProvision_NoCA_FailsClosed(t *testing.T) {
	cfg := testConfig(t, false, true) // gRPC only, no TLS, no CeremonyCA
	srv := New(cfg, newMockKVService(), &mockAdminService{})
	err := srv.Start()
	var unavailable *TLSUnavailableError
	if !errors.As(err, &unavailable) {
		srv.Stop()
		t.Fatalf("expected *TLSUnavailableError, got %T: %v", err, err)
	}
}

// TestAutoProvision_WeakExplicitTLS_StillRejected proves that supplying an
// explicit TLS config with a MinVersion below TLS 1.3 still fails Start with the
// transport's *WeakTLSVersionError — auto-provision never relaxes the TLS 1.3
// floor for an operator-supplied config.
func TestAutoProvision_WeakExplicitTLS_StillRejected(t *testing.T) {
	cfg := testConfig(t, false, true) // gRPC only
	srv := New(cfg, newMockKVService(), &mockAdminService{},
		WithTLS(&tls.Config{MinVersion: tls.VersionTLS12}))
	err := srv.Start()
	var weak *grpcapi.WeakTLSVersionError
	if !errors.As(err, &weak) {
		srv.Stop()
		t.Fatalf("expected *grpcapi.WeakTLSVersionError, got %T: %v", err, err)
	}
}

// tempUnixSocket returns a unique unix socket path under the test's temp dir.
func tempUnixSocket(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "qrdb-autoprov.sock")
}
