// Copyright 2026 Jeremy Hahn
// Apache 2.0 License

//go:build integration

package integration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	quicgo "github.com/quic-go/quic-go"
)

// deadAddr is a TCP/UDP endpoint nothing binds to: port 1 is privileged and,
// in this codebase's convention (pkg/api/grpc/grpc_coverage7_test.go,
// pkg/clusterenroll/join_test.go, pkg/api/rest/rest_test.go), is used
// wherever a test needs an address that is reachable-by-route but definitely
// unserved, as opposed to an address that does not resolve at all.
const deadAddr = "127.0.0.1:1"

// TestProbes_ReportUnavailable_ForDeadPort is go-qrdb#345's mutation guard:
// every one of these probes must return false against a target with
// certainly nothing behind it. Before this change, isQUICServerAvailable
// failed this test's QUIC case unconditionally — net.DialTimeout("udp", ...)
// performs no I/O and reported "available" for this exact address (measured
// in the issue). A regression back to that implementation, or any future
// change that once again answers without asking the peer, fails here instead
// of shipping green.
func TestProbes_ReportUnavailable_ForDeadPort(t *testing.T) {
	cfg := &TestConfig{
		RESTBaseURL: "https://" + deadAddr,
		GRPCAddr:    deadAddr,
		MCPAddr:     deadAddr,
		QUICAddr:    deadAddr,
		// Deliberately absent: loadCACertPool degrades to a nil pool (system
		// roots) on a read failure, and every path below is expected to fail
		// on the transport before TLS verification is ever reached.
		TLSCACert: filepath.Join(t.TempDir(), "does-not-exist-ca.pem"),
	}

	cases := []struct {
		name  string
		probe func(*testing.T, *TestConfig) bool
	}{
		{"rest", isServerAvailable},
		{"grpc", isGRPCServerAvailable},
		{"mcp", isMCPServerAvailable},
		{"quic", isQUICServerAvailable},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.probe(t, cfg) {
				t.Fatalf("%s probe reported %s available; nothing is listening on it", c.name, deadAddr)
			}
		})
	}
}

// TestIsUnixSocketAvailable_MissingPath covers isUnixSocketAvailable
// separately: unlike the network probes above, its "nothing is there"
// signal is a missing socket path, checked with os.Stat before any dial is
// attempted. A path inside a fresh t.TempDir() is guaranteed absent.
func TestIsUnixSocketAvailable_MissingPath(t *testing.T) {
	cfg := &TestConfig{
		UnixSocketPath: filepath.Join(t.TempDir(), "does-not-exist.sock"),
	}
	if isUnixSocketAvailable(t, cfg) {
		t.Fatalf("unix probe reported %s available; the socket file does not exist", cfg.UnixSocketPath)
	}
}

// selfSignedQUICCert generates a throwaway self-signed ECDSA certificate for
// 127.0.0.1, mirroring sdk/go/transport/quic/e2e_harness_test.go's e2eTLS.
// That helper is unexported in package quic and this package cannot import
// it, so the (small) generation logic is duplicated rather than the two
// packages sharing an internal dependency neither otherwise needs.
func selfSignedQUICCert(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatalf("generate ECDSA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qrdb-probe-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	certDER, err := x509.CreateCertificate(crand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	parsed, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{certDER},
		PrivateKey:  key,
		Leaf:        parsed,
	}
}

// TestIsQUICServerAvailable_LiveServer is the positive twin of the dead-port
// case: a probe that never reports true for a real, reachable server would
// be as useless as one that never reports false for a dead one. It starts a
// real QUIC listener (ALPN "qrdb-quic", matching pkg/api/quic/server.go and
// sdk/go/transport/quic/transport.go's buildTLSConfig) and asserts the probe
// completes a genuine handshake against it — not just a socket that happens
// to accept a SYN, since QUIC rides UDP and has no such thing.
func TestIsQUICServerAvailable_LiveServer(t *testing.T) {
	cert := selfSignedQUICCert(t)

	ln, err := quicgo.ListenAddr("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   []string{"qrdb-quic"},
	}, &quicgo.Config{
		MaxIdleTimeout:  5 * time.Second,
		KeepAlivePeriod: time.Second,
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	// Accept and immediately drop each incoming connection. The probe only
	// needs the handshake to complete; it does not exercise the wire
	// protocol (that is sdk/go/transport/quic's job, covered by
	// e2e_harness_test.go).
	go func() {
		for {
			conn, err := ln.Accept(t.Context())
			if err != nil {
				return
			}
			go func() { <-conn.Context().Done() }()
		}
	}()

	caFile := filepath.Join(t.TempDir(), "ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write CA file: %v", err)
	}

	cfg := &TestConfig{
		QUICAddr:  ln.Addr().String(),
		TLSCACert: caFile,
	}

	if !isQUICServerAvailable(t, cfg) {
		t.Fatal("quic probe reported a live, listening server as unavailable")
	}
}

// TestIsProtocolAvailable_DeadPort exercises the dispatch surface
// (IsProtocolAvailable) that every call site in this package actually uses,
// rather than the unexported probes directly, so a future change to the
// switch in IsProtocolAvailable itself (e.g. a protocol wired to the wrong
// probe) fails here too.
func TestIsProtocolAvailable_DeadPort(t *testing.T) {
	cfg := &TestConfig{
		RESTBaseURL:    "https://" + deadAddr,
		GRPCAddr:       deadAddr,
		MCPAddr:        deadAddr,
		QUICAddr:       deadAddr,
		UnixSocketPath: filepath.Join(t.TempDir(), "does-not-exist.sock"),
		TLSCACert:      filepath.Join(t.TempDir(), "does-not-exist-ca.pem"),
	}

	for _, proto := range AllProtocols() {
		t.Run(string(proto), func(t *testing.T) {
			if cfg.IsProtocolAvailable(t, proto) {
				t.Fatalf("protocol %s reported available against a dead endpoint", proto)
			}
		})
	}
}
