// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ceremony

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"slices"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// TestServerTLSConfig_ProvisionsTLS13LeafChainedToCA verifies the happy path:
// the returned config is pinned to TLS 1.3, carries exactly one leaf, and that
// leaf is signed by the CA and covers localhost plus any extra SAN supplied.
func TestServerTLSConfig_ProvisionsTLS13LeafChainedToCA(t *testing.T) {
	ca := newTestLocalCA(t)

	cfg, err := ServerTLSConfig(context.Background(), ca, newDeviceSigner(t), "node.internal")
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %#x, want TLS 1.3", cfg.MinVersion)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("Certificates length = %d, want 1", len(cfg.Certificates))
	}

	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	caCert, err := ca.CACertificate(t.Context())
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		t.Fatalf("leaf not signed by CA: %v", err)
	}

	if err := leaf.VerifyHostname("localhost"); err != nil {
		t.Errorf("leaf does not cover localhost: %v", err)
	}
	if err := leaf.VerifyHostname("node.internal"); err != nil {
		t.Errorf("leaf does not cover the extra SAN node.internal: %v", err)
	}
}

// A device-held key serves the leaf end to end: the CA certifies its public
// half, and a client that trusts the CA completes a TLS 1.3 handshake whose
// CertificateVerify the device signed. No private key bytes are involved.
func TestServerTLSConfig_DeviceHeldKeyServesHandshake(t *testing.T) {
	ca := newTestLocalCA(t)
	key := newDeviceSigner(t)

	cfg, err := ServerTLSConfig(context.Background(), ca, key)
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	if cfg.Certificates[0].PrivateKey != key {
		t.Fatalf("leaf private key = %T, want the device signer", cfg.Certificates[0].PrivateKey)
	}

	caCert, err := ca.CACertificate(t.Context())
	if err != nil {
		t.Fatalf("CA certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(caCert)

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	serverErr := make(chan error, 1)
	go func() { serverErr <- tls.Server(serverConn, cfg).HandshakeContext(t.Context()) }()

	client := tls.Client(clientConn, &tls.Config{RootCAs: roots, ServerName: "localhost", MinVersion: tls.VersionTLS13})
	if err := client.HandshakeContext(t.Context()); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

// TestServerTLSConfig_NilCA verifies a nil CA fails closed with ErrNilCA rather
// than returning a usable (unauthenticated) config.
func TestServerTLSConfig_NilCA(t *testing.T) {
	cfg, err := ServerTLSConfig(context.Background(), nil, newDeviceSigner(t))
	if cfg != nil {
		t.Fatal("expected nil config when CA is nil")
	}
	if !errors.Is(err, ErrNilCA) {
		t.Fatalf("expected ErrNilCA, got %v", err)
	}
}

// A nil key fails closed: ServerTLSConfig never generates one in its place.
func TestServerTLSConfig_NilKey(t *testing.T) {
	cfg, err := ServerTLSConfig(context.Background(), newTestLocalCA(t), nil)
	if cfg != nil {
		t.Fatal("expected nil config when key is nil")
	}
	var keyErr *IssueCertKeyError
	if !errors.As(err, &keyErr) {
		t.Fatalf("expected *IssueCertKeyError, got %T: %v", err, err)
	}
}

// TestServerTLSConfig_IssueFailureWrapped verifies an issuance failure surfaces
// as a typed *ServerTLSError.
func TestServerTLSConfig_IssueFailureWrapped(t *testing.T) {
	_, err := ServerTLSConfig(context.Background(), failingCA{}, newDeviceSigner(t))
	var provErr *ServerTLSError
	if !errors.As(err, &provErr) {
		t.Fatalf("expected *ServerTLSError, got %T: %v", err, err)
	}
}

// A composed CA that certifies some other key, or returns no parsed
// certificate, is refused before the leaf reaches a listener.
func TestServerTLSConfig_IssuedKeyMismatch(t *testing.T) {
	ca := newTestLocalCA(t)
	other := newDeviceSigner(t)
	cases := map[string]CeremonyCA{
		"other key":      swappingCA{LocalCA: ca, key: other},
		"no certificate": swappingCA{LocalCA: ca},
	}
	for name, composed := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ServerTLSConfig(context.Background(), composed, newDeviceSigner(t))
			var encErr *CertEncodeError
			if !errors.As(err, &encErr) {
				t.Fatalf("expected *CertEncodeError, got %T: %v", err, err)
			}
		})
	}
}

// TestBaseServerSANs_SameSetAsJoinedNodeLeaf pins node parity: a founder's
// server certificate and a joined node's leaf carry the same loopback names.
func TestBaseServerSANs_SameSetAsJoinedNodeLeaf(t *testing.T) {
	tmpl, err := clustertls.IdentityLeafTemplate(time.Hour)
	if err != nil {
		t.Fatalf("IdentityLeafTemplate: %v", err)
	}
	joiner := slices.Clone(tmpl.DNSNames)
	for _, ip := range tmpl.IPAddresses {
		joiner = append(joiner, ip.String())
	}
	founder := slices.Clone(baseServerSANs)
	slices.Sort(joiner)
	slices.Sort(founder)
	if !slices.Equal(founder, joiner) {
		t.Fatalf("founder SANs = %v, joiner SANs = %v, want the same set", founder, joiner)
	}
}

// TestServerSANs_CarriesLoopbackNames covers the founder side: every
// loopback name is in the certificate's SANs, once, ahead of the extras.
func TestServerSANs_CarriesLoopbackNames(t *testing.T) {
	got := serverSANs([]string{"localhost", "db.example.com"})
	for i, want := range clustertls.LoopbackSANs() {
		if got[i] != want {
			t.Fatalf("serverSANs = %v, want %v first", got, clustertls.LoopbackSANs())
		}
	}
	if n := len(slices.DeleteFunc(slices.Clone(got), func(s string) bool { return s != "localhost" })); n != 1 {
		t.Fatalf("serverSANs = %v, want localhost once", got)
	}
	if !slices.Contains(got, "db.example.com") {
		t.Fatalf("serverSANs = %v, want the extra SAN", got)
	}
}

// swappingCA certifies key in place of the requested public key, or returns
// an IssuedCert without its parsed Certificate when key is unset.
type swappingCA struct {
	*LocalCA
	key deviceSigner
}

func (c swappingCA) IssueCertificate(ctx context.Context, req *IssueCertRequest) (*IssuedCert, error) {
	if c.key.key == nil {
		issued, err := c.LocalCA.IssueCertificate(ctx, req)
		if err != nil {
			return nil, err
		}
		return &IssuedCert{CertPEM: issued.CertPEM}, nil
	}
	swapped := *req
	swapped.PublicKey = c.key.Public()
	return c.LocalCA.IssueCertificate(ctx, &swapped)
}

// failingCA is a CeremonyCA whose IssueCertificate always fails, exercising the
// error-wrapping path of ServerTLSConfig.
type failingCA struct{}

func (failingCA) SignCSR(context.Context, []byte, *SignCSROptions) (*x509.Certificate, error) {
	return nil, errAlwaysFail
}
func (failingCA) CACertificate(context.Context) (*x509.Certificate, error) { return nil, errAlwaysFail }
func (failingCA) CABundle() ([]byte, error)                                { return nil, errAlwaysFail }
func (failingCA) IssueCertificate(context.Context, *IssueCertRequest) (*IssuedCert, error) {
	return nil, errAlwaysFail
}

var errAlwaysFail = errors.New("ceremony: test issuance failure")
