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

// package transport_test, not transport: this file pulls in pkg/cert
// (through pkg/clienttrust's own test fixtures) to drive a real TLS
// handshake end to end, and pkg/cert reaches back into this module via
// pkg/service and pkg/user (see pkg/user/cert_binding.go's wire-type
// projections) -- an internal "package transport" test file closes that
// into an import cycle the production code never has. An external test
// package has no such cycle, since nothing needs to import it back.
package transport_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
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

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// genKeyBHandshakeCA generates a throwaway self-signed CA, returning its
// PEM encoding alongside the parsed certificate and signing key.
func genKeyBHandshakeCA(t *testing.T, cn string) ([]byte, *x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), c, key
}

// genKeyBHandshakeLeaf signs a client-auth leaf under caCert/caKey and
// writes its cert/key PEM to files BuildTLSConfig can load.
func genKeyBHandshakeLeaf(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) (certFile, keyFile string) {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (leaf): %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "tenant-alice", OrganizationalUnit: []string{"acme"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "leaf-cert.pem")
	keyFile = filepath.Join(dir, "leaf-key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certFile, keyFile
}

// genKeyBHandshakeLeafCert signs a client-auth leaf under caCert/caKey and
// returns it as an in-memory tls.Certificate, for a test exercising
// TLSBuildOptions.Custom directly rather than the CertFile/KeyFile path.
func genKeyBHandshakeLeafCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey (leaf): %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "tenant-alice", OrganizationalUnit: []string{"acme"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("CreateCertificate (leaf): %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der, caCert.Raw}, PrivateKey: leafKey}
}

// fakeKeyBCertSource is the minimal clienttrust.CertSource double this test
// needs: one tenant's Key B CA certificate, nothing else.
type fakeKeyBCertSource struct {
	cert.CertProvider
	pems [][]byte
}

func (f *fakeKeyBCertSource) CACertPEMs(context.Context) ([][]byte, error) { return f.pems, nil }
func (f *fakeKeyBCertSource) IsTenantIssuer(context.Context, string, *x509.Certificate) (bool, error) {
	return false, nil
}

// TestBuildTLSConfig_KeyBCertPresentedOnEveryTransport is go-qrdb#591's
// review red/green proof, brought in from lane/tenant-onboarding-v2's
// discovery: B2 makes every listener pkg/clienttrust.Pool wires advertise
// ONLY its static baseline in CertificateRequest.AcceptableCAs, never a
// tenant's own Key B CA (the whole point of B2 -- see
// clienttrust.Pool.configForClient's own doc comment). Before this fix,
// BuildTLSConfig carried a loaded client certificate via the static
// Certificates field, and crypto/tls's default client-side selector for
// that field only offers an entry whose issuer appears in
// AcceptableCAs -- silently sending NO certificate at all otherwise. Every
// one of the five remote transports (gRPC, Unix, QUIC, MCP, REST) builds
// its client TLS config through this ONE shared function, so a bug here
// broke a Key B login identically on all five; proving it once, against
// the shared function, covers every one of them exactly as would proving
// it five times against each transport's own thin wrapper around it.
func TestBuildTLSConfig_KeyBCertPresentedOnEveryTransport(t *testing.T) {
	// Key A: the static baseline the server's ClientCAs advertises --
	// never the tenant's Key B CA, by B2's own design.
	_, keyACert, keyAKey := genKeyBHandshakeCA(t, "key-a")
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)

	serverLeafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, keyACert, &keyAKey.PublicKey, keyAKey)
	if err != nil {
		t.Fatalf("CreateCertificate (server leaf): %v", err)
	}
	serverLeaf := tls.Certificate{Certificate: [][]byte{serverLeafDER, keyACert.Raw}, PrivateKey: keyAKey}

	// Key B: the tenant's own login CA, never in the server's advertised
	// ClientCAs, trusted only out-of-band through pkg/clienttrust.Pool's
	// own VerifyConnection hook.
	keyBPEM, keyBCert, keyBKey := genKeyBHandshakeCA(t, "key-b-acme")
	leafCertFile, leafKeyFile := genKeyBHandshakeLeaf(t, keyBCert, keyBKey)

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{serverLeaf},
		ClientCAs:    keyAPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	pool := clienttrust.New()
	pool.Wire(serverCfg)
	pool.SetKeyBProvider(&fakeKeyBCertSource{pems: [][]byte{keyBPEM}})

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type acceptResult struct {
		cs  tls.ConnectionState
		err error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			accepted <- acceptResult{err: aerr}
			return
		}
		tc := conn.(*tls.Conn)
		defer func() { _ = tc.Close() }()
		hErr := tc.Handshake()
		accepted <- acceptResult{cs: tc.ConnectionState(), err: hErr}
	}()

	// The client side: exactly what every remote transport's own
	// constructor does -- BuildTLSConfig loads the tenant's Key B leaf,
	// which must be PRESENTED even though it will never appear in the
	// server's AcceptableCAs list.
	clientCfg, err := transport.BuildTLSConfig(transport.TLSBuildOptions{
		Enabled:  true,
		CertFile: leafCertFile,
		KeyFile:  leafKeyFile,
	})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	clientCfg.InsecureSkipVerify = true // this test is about the CLIENT CERT presented, not server verification
	clientCfg.MinVersion = tls.VersionTLS13

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rawConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	clientTLS := tls.Client(rawConn, clientCfg)
	defer func() { _ = clientTLS.Close() }()
	if err := clientTLS.HandshakeContext(ctx); err != nil {
		t.Fatalf("client handshake (Key B leaf should have been presented and accepted): %v", err)
	}

	select {
	case res := <-accepted:
		if res.err != nil {
			t.Fatalf("server handshake: %v", res.err)
		}
		if len(res.cs.PeerCertificates) == 0 {
			t.Fatal("expected the server to see the Key B leaf's PeerCertificates; none were presented")
		}
		// VerifiedChains is expected to stay empty: Pool.configForClient
		// remaps ClientAuth and moves verification to its own
		// VerifyConnection hook, which crypto/tls never populates
		// VerifiedChains for (see clienttrust.Pool.VerifyPeerChain's own
		// doc comment). A successful handshake with PeerCertificates
		// populated already proves the leaf was presented AND that
		// VerifyConnection accepted it -- a rejected leaf would have
		// failed the handshake, caught by the error checks above.
	case <-ctx.Done():
		t.Fatal("timed out waiting for the server-side handshake")
	}
}

// TestBuildTLSConfig_CustomConfigKeyBCertPresented is
// TestBuildTLSConfig_KeyBCertPresentedOnEveryTransport's counterpart for
// the TLSBuildOptions.Custom path (go-qrdb#591's review, non-blocking 5):
// an embedder supplying its OWN *tls.Config with a static Certificates
// entry -- rather than CertFile/KeyFile -- hits the identical silent-drop
// hazard against a pkg/clienttrust.Pool-wired server, since
// BuildTLSConfig previously passed a Custom config's Certificates field
// through completely unwrapped.
func TestBuildTLSConfig_CustomConfigKeyBCertPresented(t *testing.T) {
	_, keyACert, keyAKey := genKeyBHandshakeCA(t, "key-a-custom")
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)

	serverLeafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, keyACert, &keyAKey.PublicKey, keyAKey)
	if err != nil {
		t.Fatalf("CreateCertificate (server leaf): %v", err)
	}
	serverLeaf := tls.Certificate{Certificate: [][]byte{serverLeafDER, keyACert.Raw}, PrivateKey: keyAKey}

	keyBPEM, keyBCert, keyBKey := genKeyBHandshakeCA(t, "key-b-acme-custom")
	clientLeaf := genKeyBHandshakeLeafCert(t, keyBCert, keyBKey)

	serverCfg := &tls.Config{
		Certificates: []tls.Certificate{serverLeaf},
		ClientCAs:    keyAPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
	pool := clienttrust.New()
	pool.Wire(serverCfg)
	pool.SetKeyBProvider(&fakeKeyBCertSource{pems: [][]byte{keyBPEM}})

	ln, err := tls.Listen("tcp", "127.0.0.1:0", serverCfg)
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	type acceptResult struct {
		cs  tls.ConnectionState
		err error
	}
	accepted := make(chan acceptResult, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			accepted <- acceptResult{err: aerr}
			return
		}
		tc := conn.(*tls.Conn)
		defer func() { _ = tc.Close() }()
		hErr := tc.Handshake()
		accepted <- acceptResult{cs: tc.ConnectionState(), err: hErr}
	}()

	// The client side: a Custom config carrying the Key B leaf directly
	// in Certificates, exactly as an embedder wiring its own *tls.Config
	// would, rather than through CertFile/KeyFile.
	clientCfg, err := transport.BuildTLSConfig(transport.TLSBuildOptions{
		Custom: &tls.Config{Certificates: []tls.Certificate{clientLeaf}},
	})
	if err != nil {
		t.Fatalf("BuildTLSConfig: %v", err)
	}
	if clientCfg.GetClientCertificate == nil {
		t.Fatal("expected BuildTLSConfig to wrap the Custom config's lone static certificate in GetClientCertificate")
	}
	clientCfg.InsecureSkipVerify = true // this test is about the CLIENT CERT presented, not server verification
	clientCfg.MinVersion = tls.VersionTLS13

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rawConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	clientTLS := tls.Client(rawConn, clientCfg)
	defer func() { _ = clientTLS.Close() }()
	if err := clientTLS.HandshakeContext(ctx); err != nil {
		t.Fatalf("client handshake (Key B leaf should have been presented and accepted): %v", err)
	}

	select {
	case res := <-accepted:
		if res.err != nil {
			t.Fatalf("server handshake: %v", res.err)
		}
		if len(res.cs.PeerCertificates) == 0 {
			t.Fatal("expected the server to see the Key B leaf's PeerCertificates; none were presented")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the server-side handshake")
	}
}
