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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcapi "github.com/jeremyhahn/go-qrdb/pkg/api/grpc"
	pb "github.com/jeremyhahn/go-qrdb/pkg/api/grpc/proto/qrdbv1"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
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

	caCert, err := testServerCA.CACertificate(t.Context())
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

// deviceTLSKey stands in for a server TLS key held on a device: it exposes
// only Public and Sign, so its private half cannot be marshalled, and it
// counts the signatures the handshake asks of it.
type deviceTLSKey struct {
	key   *ecdsa.PrivateKey
	signs *atomic.Int32
}

func (k deviceTLSKey) Public() crypto.PublicKey { return k.key.Public() }

func (k deviceTLSKey) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	k.signs.Add(1)
	return k.key.Sign(r, digest, opts)
}

// TestAutoProvision_WithServerTLSKey_DeviceSignerServesLeaf proves the
// WithServerTLSKey seam: the auto-provisioned leaf certifies the composed
// signer's public key and the handshake signs through it, with no private key
// bytes involved anywhere.
func TestAutoProvision_WithServerTLSKey_DeviceSignerServesLeaf(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate device key: %v", err)
	}
	device := deviceTLSKey{key: ecKey, signs: new(atomic.Int32)}
	if _, err := x509.MarshalPKCS8PrivateKey(device); err == nil {
		t.Fatal("device key marshalled; it must be opaque")
	}

	cfg := testConfig(t, false, true) // gRPC only
	srv := New(cfg, newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA), WithServerTLSKey(device))
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
		t.Fatalf("TLS dial: %v", err)
	}
	defer func() { _ = conn.Close() }()

	leaf := conn.ConnectionState().PeerCertificates[0]
	if !ecKey.PublicKey.Equal(leaf.PublicKey) {
		t.Fatal("leaf does not certify the composed signer's public key")
	}
	if device.signs.Load() == 0 {
		t.Fatal("handshake did not sign through the composed signer")
	}
}

// Without WithServerTLSKey each transport's leaf gets its own fresh software
// key: two listeners present different public keys.
func TestAutoProvision_DefaultKeyPerTransport(t *testing.T) {
	srv := New(testConfig(t, false, true), newMockKVService(), &mockAdminService{}, WithCeremonyCA(testServerCA))
	first, err := srv.resolveTransportTLS("gRPC")
	if err != nil {
		t.Fatalf("resolveTransportTLS: %v", err)
	}
	second, err := srv.resolveTransportTLS("REST")
	if err != nil {
		t.Fatalf("resolveTransportTLS: %v", err)
	}
	a := first.Certificates[0].Leaf.PublicKey.(*ecdsa.PublicKey)
	if a.Equal(second.Certificates[0].Leaf.PublicKey) {
		t.Fatal("two auto-provisioned leaves share a default key")
	}
}

// recordingTrust is a TLSClientTrust that records the configurations it
// was wired onto.
type recordingTrust struct{ wired []*tls.Config }

func (r *recordingTrust) Wire(cfg *tls.Config) { r.wired = append(r.wired, cfg) }

// TestAutoProvision_RequestsNodeClientCert proves an auto-provisioned
// listener takes the node posture with auth off: it requests a client
// certificate and verifies one if given against the ceremony CA, every
// transport shares one ClientCAs pool, and WithAutoTLSClientTrust is wired
// onto each configuration. A real handshake delivers a CA-issued client
// certificate to the server; a client without one still connects.
func TestAutoProvision_RequestsNodeClientCert(t *testing.T) {
	trust := &recordingTrust{}
	srv := New(testConfig(t, false, true), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithAutoTLSClientTrust(trust))
	first, err := srv.resolveTransportTLS("REST")
	if err != nil {
		t.Fatalf("resolveTransportTLS: %v", err)
	}
	second, err := srv.resolveTransportTLS("gRPC")
	if err != nil {
		t.Fatalf("resolveTransportTLS: %v", err)
	}
	if first.ClientAuth != tls.VerifyClientCertIfGiven {
		t.Fatalf("ClientAuth = %v, want VerifyClientCertIfGiven", first.ClientAuth)
	}
	if first.ClientCAs == nil || first.ClientCAs != second.ClientCAs {
		t.Fatal("auto-provisioned transports do not share one ClientCAs pool")
	}
	if len(trust.wired) != 2 || trust.wired[0] != first || trust.wired[1] != second {
		t.Fatalf("client trust wired onto %d configurations, want both", len(trust.wired))
	}

	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	issued, err := testServerCA.IssueCertificate(t.Context(), &ceremony.IssueCertRequest{
		PublicKey:    clientKey.Public(),
		CommonName:   "leader-node",
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		ValidityDays: 1,
	})
	if err != nil {
		t.Fatalf("issue client certificate: %v", err)
	}
	clientCert := tls.Certificate{Certificate: [][]byte{issued.Certificate.Raw}, PrivateKey: clientKey}

	handshake := func(certs []tls.Certificate) []*x509.Certificate {
		t.Helper()
		cConn, sConn := net.Pipe()
		defer func() { _ = cConn.Close(); _ = sConn.Close() }()
		server := tls.Server(sConn, first)
		done := make(chan error, 1)
		go func() { done <- server.Handshake() }()
		client := tls.Client(cConn, &tls.Config{
			RootCAs: testServerCAPool(t), ServerName: "localhost",
			MinVersion: tls.VersionTLS13, Certificates: certs,
		})
		if err := client.Handshake(); err != nil {
			t.Fatalf("client handshake: %v", err)
		}
		// TLS 1.3 verifies the client certificate after the client's
		// handshake returns; a read completes the server side.
		go func() { _, _ = client.Read(make([]byte, 1)) }()
		if err := <-done; err != nil {
			t.Fatalf("server handshake: %v", err)
		}
		return server.ConnectionState().PeerCertificates
	}
	if got := handshake([]tls.Certificate{clientCert}); len(got) == 0 || !got[0].Equal(issued.Certificate) {
		t.Fatal("server did not receive the client's CA-issued certificate")
	}
	if got := handshake(nil); len(got) != 0 {
		t.Fatalf("certificate-less client delivered %d certificates", len(got))
	}
}

// TestAutoProvision_ForwardingCAPoolNotClientTrust proves WithCACertPool,
// which only verifies peer servers for the REST forwarding client, does not
// let its CAs issue client identities on an auto-provisioned listener: a
// client certificate from a forwarding-only CA fails the handshake, while a
// ceremony-CA-issued one still verifies.
func TestAutoProvision_ForwardingCAPoolNotClientTrust(t *testing.T) {
	forwardingCA := mustTestCeremonyCA()
	forwardingPEM, err := forwardingCA.CABundle()
	if err != nil {
		t.Fatalf("forwarding CA bundle: %v", err)
	}
	forwardingPool := x509.NewCertPool()
	if !forwardingPool.AppendCertsFromPEM(forwardingPEM) {
		t.Fatal("forwarding CA bundle: no certificate appended to pool")
	}
	srv := New(testConfig(t, false, true), newMockKVService(), &mockAdminService{},
		WithCeremonyCA(testServerCA), WithCACertPool(forwardingPool),
		WithAutoTLSClientTrust(&recordingTrust{}))
	cfg, err := srv.resolveTransportTLS("REST")
	if err != nil {
		t.Fatalf("resolveTransportTLS: %v", err)
	}
	if cfg.ClientCAs == nil {
		t.Fatal("auto-provisioned listener has no ClientCAs pool")
	}

	issue := func(ca ceremony.CeremonyCA) tls.Certificate {
		t.Helper()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatalf("generate client key: %v", err)
		}
		issued, err := ca.IssueCertificate(t.Context(), &ceremony.IssueCertRequest{
			PublicKey:    key.Public(),
			CommonName:   "leader-node",
			KeyUsage:     x509.KeyUsageDigitalSignature,
			ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
			ValidityDays: 1,
		})
		if err != nil {
			t.Fatalf("issue client certificate: %v", err)
		}
		return tls.Certificate{Certificate: [][]byte{issued.Certificate.Raw}, PrivateKey: key}
	}

	// handshake returns the server-side handshake error and verified chains.
	handshake := func(cert tls.Certificate) ([][]*x509.Certificate, error) {
		t.Helper()
		cConn, sConn := net.Pipe()
		defer func() { _ = cConn.Close(); _ = sConn.Close() }()
		server := tls.Server(sConn, cfg)
		done := make(chan error, 1)
		go func() { done <- server.Handshake() }()
		client := tls.Client(cConn, &tls.Config{
			RootCAs: testServerCAPool(t), ServerName: "localhost",
			MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert},
		})
		if err := client.Handshake(); err != nil {
			// The server rejected the certificate before the client finished.
			return nil, <-done
		}
		// TLS 1.3 verifies the client certificate after the client's
		// handshake returns; a read completes the server side.
		go func() { _, _ = client.Read(make([]byte, 1)) }()
		if err := <-done; err != nil {
			return nil, err
		}
		return server.ConnectionState().VerifiedChains, nil
	}

	if chains, err := handshake(issue(forwardingCA)); err == nil || len(chains) != 0 {
		t.Fatalf("forwarding-only CA client certificate accepted: err=%v, %d verified chains", err, len(chains))
	}
	if chains, err := handshake(issue(testServerCA)); err != nil || len(chains) == 0 {
		t.Fatalf("ceremony CA client certificate not verified: err=%v, %d verified chains", err, len(chains))
	}
}

// TestListenerClientAuth pins the node listener posture shared by qrdbd and
// the auto-provisioned embedded listener.
func TestListenerClientAuth(t *testing.T) {
	cases := []struct {
		auth, identity bool
		want           tls.ClientAuthType
	}{
		{false, false, tls.NoClientCert},
		{false, true, tls.VerifyClientCertIfGiven},
		{true, false, tls.VerifyClientCertIfGiven},
		{true, true, tls.VerifyClientCertIfGiven},
	}
	for _, c := range cases {
		if got := ListenerClientAuth(c.auth, c.identity); got != c.want {
			t.Errorf("ListenerClientAuth(%v, %v) = %v, want %v", c.auth, c.identity, got, c.want)
		}
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
	caCert, err := testServerCA.CACertificate(t.Context())
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
