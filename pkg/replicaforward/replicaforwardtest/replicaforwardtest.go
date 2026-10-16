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

// Package replicaforwardtest is the fixture every transport's replica-forwarding
// tests share: a cluster CA issuing node and caller certificates, and a
// fixed member-record source. It is for tests only.
package replicaforwardtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cluster"
)

// TrustDomain is the SPIFFE trust domain of the node certificates CA issues.
const TrustDomain = "qrdb.cluster"

// CA is a test cluster CA.
type CA struct {
	cert   *x509.Certificate
	key    *ecdsa.PrivateKey
	serial atomic.Int64

	// Pool holds the CA certificate.
	Pool *x509.CertPool
}

// NewCA returns a fresh CA.
func NewCA(t testing.TB) *CA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "forwardtest-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	ca := &CA{cert: cert, key: key, Pool: pool}
	ca.serial.Store(1)
	return ca
}

// TLS returns a listener TLS configuration valid for 127.0.0.1 -- a node's,
// with the SPIFFE node SAN for nodeID, or, with nodeID empty, a caller's --
// that requires client certificates from the CA and trusts it as a client.
func (ca *CA) TLS(t testing.TB, nodeID string) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(ca.serial.Add(1)), Subject: pkix.Name{CommonName: "forwardtest-peer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	if nodeID != "" {
		tmpl.URIs = []*url.URL{{Scheme: "spiffe", Host: TrustDomain, Path: "/node/" + nodeID}}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		RootCAs:      ca.Pool,
		ClientCAs:    ca.Pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}
}

// Members is a fixed set of member records.
type Members []cluster.Member

// ListMembers returns the records.
func (m Members) ListMembers(context.Context) ([]cluster.Member, error) { return m, nil }

// StalledMembers is a member-record source whose read never completes: it
// answers only when the caller's context ends, with the context's error --
// a system-shard read that cannot reach a quorum.
type StalledMembers struct{}

// ListMembers blocks until ctx is done.
func (StalledMembers) ListMembers(ctx context.Context) ([]cluster.Member, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// ClosedAddr returns a 127.0.0.1 address nothing listens on.
func ClosedAddr(t testing.TB) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// HandshakeRefusedError is the refusal RefuseHandshakes answers a refused
// handshake with.
type HandshakeRefusedError struct{}

func (*HandshakeRefusedError) Error() string {
	return "replicaforwardtest: handshake refused"
}

// RefuseHandshakes returns a copy of cfg that refuses its first n
// handshakes, as a target that is up but restarting or overloaded fails its
// first connections, and a count of the handshakes it was offered.
func RefuseHandshakes(cfg *tls.Config, n int32) (*tls.Config, *atomic.Int32) {
	offered := new(atomic.Int32)
	refusing := cfg.Clone()
	refusing.GetConfigForClient = func(*tls.ClientHelloInfo) (*tls.Config, error) {
		if offered.Add(1) <= n {
			return nil, &HandshakeRefusedError{}
		}
		return nil, nil
	}
	return refusing, offered
}
