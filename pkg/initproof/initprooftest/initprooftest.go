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

// Package initprooftest gives tests a live TLS 1.3 connection to run the
// tenant barrier init proof over: both ends of one in-memory handshake, so the
// server end can be placed in a request context the way a transport places it
// and the client end yields the matching exporter to sign.
//
// The keys it mints are throwaway test keys generated in memory; none is ever
// written anywhere.
package initprooftest

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/initproof"
)

// Conn is the two ends of one completed TLS 1.3 handshake.
type Conn struct {
	// Client is the state of the client end, whose exporter a caller signs.
	Client tls.ConnectionState
	// Server is the state of the server end, which a transport places in the
	// request context (auth.WithTLSState).
	Server tls.ConnectionState
}

// Proof is the init/complete proof of possession: the nonce, the DER
// SubjectPublicKeyInfo of the signing key, and its signature.
type Proof struct {
	Nonce     []byte
	PublicKey []byte
	Signature []byte
}

// Certificate mints a self-signed client certificate for commonName with a
// fresh P-256 key.
func Certificate(t testing.TB, commonName string) tls.Certificate {
	t.Helper()
	key, der := selfSigned(t, commonName, x509.ExtKeyUsageClientAuth)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// Dial completes a TLS 1.3 handshake over an in-memory pipe with client as the
// client's certificate (none when it carries no chain) and returns both ends.
// The pipe is closed when the test ends.
func Dial(t testing.TB, client tls.Certificate) *Conn {
	t.Helper()
	srvKey, srvDER := selfSigned(t, "node.test", x509.ExtKeyUsageServerAuth)
	srvCert, err := x509.ParseCertificate(srvDER)
	if err != nil {
		t.Fatalf("initprooftest: parse server certificate: %v", err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(srvCert)

	var clientCerts []tls.Certificate
	if len(client.Certificate) > 0 {
		clientCerts = []tls.Certificate{client}
	}
	cConn, sConn := net.Pipe()
	t.Cleanup(func() { _ = cConn.Close(); _ = sConn.Close() })
	srv := tls.Server(sConn, &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}},
		ClientAuth:   tls.RequestClientCert,
		MinVersion:   tls.VersionTLS13,
	})
	cli := tls.Client(cConn, &tls.Config{
		Certificates: clientCerts,
		RootCAs:      roots,
		ServerName:   "node.test",
		MinVersion:   tls.VersionTLS13,
	})
	errc := make(chan error, 1)
	go func() { errc <- srv.Handshake() }()
	if err := cli.Handshake(); err != nil {
		t.Fatalf("initprooftest: client handshake: %v", err)
	}
	if err := <-errc; err != nil {
		t.Fatalf("initprooftest: server handshake: %v", err)
	}
	return &Conn{Client: cli.ConnectionState(), Server: srv.ConnectionState()}
}

// Prove signs the init proof for tenantID and nonce over this connection's
// client exporter with signer.
func (c *Conn) Prove(t testing.TB, signer crypto.Signer, tenantID string, nonce []byte) *Proof {
	t.Helper()
	exporter, err := initproof.Exporter(&c.Client)
	if err != nil {
		t.Fatalf("initprooftest: client exporter: %v", err)
	}
	sig, err := initproof.Sign(signer, tenantID, nonce, exporter)
	if err != nil {
		t.Fatalf("initprooftest: sign: %v", err)
	}
	spki, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		t.Fatalf("initprooftest: marshal public key: %v", err)
	}
	return &Proof{Nonce: nonce, PublicKey: spki, Signature: sig}
}

// selfSigned mints a self-signed certificate for commonName with usage and a
// fresh P-256 key.
func selfSigned(t testing.TB, commonName string, usage x509.ExtKeyUsage) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("initprooftest: generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("initprooftest: serial: %v", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		DNSNames:     []string{commonName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{usage},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("initprooftest: create certificate: %v", err)
	}
	return key, der
}
