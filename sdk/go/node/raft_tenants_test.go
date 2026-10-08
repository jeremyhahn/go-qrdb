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

package node

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// A node is BYO-TLS only when it is handed its cluster identity and no
// cluster CA key of its own.
func TestByoTLS(t *testing.T) {
	id := &RaftMTLSConfig{CACert: []byte("ca")}
	for name, tc := range map[string]struct {
		n    *Node
		want bool
	}{
		"a cluster identity and no CA key": {n: &Node{clusterIdentity: id}, want: true},
		"no cluster identity":              {n: &Node{}},
		"a CA key provider":                {n: &Node{clusterIdentity: id, clusterCAKeyProvider: nopCAProvider{}}},
		"CA material to adopt":             {n: &Node{clusterIdentity: id, clusterCACertPEM: []byte("cert")}},
	} {
		if got := tc.n.byoTLS(); got != tc.want {
			t.Errorf("%s: byoTLS = %v, want %v", name, got, tc.want)
		}
	}
}

// nopCAProvider is a cluster CA key provider the test never calls.
type nopCAProvider struct{ cakey.Provider }

// A node composes the raft tenant cluster only when it is clustered: a
// standalone node, one with no Raft host, or no configuration composes
// nothing.
func TestRaftTenantClusterOptions_NothingOffACluster(t *testing.T) {
	n := &Node{logger: slog.New(slog.DiscardHandler)}
	cfg := qrdbconfig.DefaultConfig()
	for name, tc := range map[string]struct {
		cfg        *qrdbconfig.Config
		standalone bool
	}{
		"standalone":       {cfg: cfg, standalone: true},
		"no Raft host":     {cfg: cfg},
		"no configuration": {},
	} {
		if opts := n.raftTenantClusterOptions(&lifecycle.Node{}, tc.cfg, nil, tc.standalone); opts != nil {
			t.Errorf("%s: composed %d server options; want none", name, len(opts))
		}
	}
}

// deviceSigner stands in for a node key held on a device: it signs, counting
// the signatures, and nothing else, so an identity built on it carries no
// key bytes.
type deviceSigner struct {
	key   *ecdsa.PrivateKey
	signs atomic.Int64
}

var _ crypto.Signer = (*deviceSigner)(nil)

func (d *deviceSigner) Public() crypto.PublicKey { return d.key.Public() }
func (d *deviceSigner) Sign(r io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	d.signs.Add(1)
	return d.key.Sign(r, digest, opts)
}

// selfSignedIdentity returns a self-signed P-256 node certificate and its
// key.
func selfSignedIdentity(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "node-1"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

// A composer's signer-backed Certificate is the node's identity as given --
// no key bytes are needed -- and takes precedence over PEM, as it does for
// the Raft transport.
func TestClusterIdentityCert_DeviceHeldCertificate(t *testing.T) {
	der, key := selfSignedIdentity(t)
	device := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: &deviceSigner{key: key}}

	got, err := clusterIdentityCert(&RaftMTLSConfig{CACert: []byte("ca"), Certificate: device, Cert: []byte("ignored")})
	if err != nil {
		t.Fatalf("clusterIdentityCert: %v", err)
	}
	if got != device {
		t.Fatal("clusterIdentityCert did not return the composer's certificate")
	}
	if _, ok := got.PrivateKey.(*deviceSigner); !ok {
		t.Fatalf("identity key = %T; want the device signer", got.PrivateKey)
	}
}

// Without a Certificate the identity is the Cert/Key PEM pair, and a pair
// that does not parse is an error.
func TestClusterIdentityCert_PEMPair(t *testing.T) {
	der, key := selfSignedIdentity(t)
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	got, err := clusterIdentityCert(&RaftMTLSConfig{Cert: certPEM, Key: keyPEM})
	if err != nil {
		t.Fatalf("clusterIdentityCert: %v", err)
	}
	if len(got.Certificate) != 1 || !bytes.Equal(got.Certificate[0], der) {
		t.Fatal("clusterIdentityCert did not carry the PEM certificate")
	}
	if _, err := clusterIdentityCert(&RaftMTLSConfig{Cert: certPEM}); err == nil {
		t.Fatal("clusterIdentityCert accepted a certificate with no key")
	}
}

// The node's own certificate -- for its member identity and its cluster
// join -- comes from the signer-backed Certificate when there is one, else
// from Cert.
func TestIdentityCertPEM(t *testing.T) {
	der, key := selfSignedIdentity(t)
	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	device := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: &deviceSigner{key: key}}
	for name, tc := range map[string]struct {
		id   *RaftMTLSConfig
		want []byte
	}{
		"no identity":                     {},
		"a PEM certificate":               {id: &RaftMTLSConfig{Cert: []byte("pem")}, want: []byte("pem")},
		"a signer-backed certificate":     {id: &RaftMTLSConfig{Certificate: device}, want: leafPEM},
		"Certificate takes precedence":    {id: &RaftMTLSConfig{Cert: []byte("pem"), Certificate: device}, want: leafPEM},
		"an empty Certificate falls back": {id: &RaftMTLSConfig{Cert: []byte("pem"), Certificate: &tls.Certificate{}}, want: []byte("pem")},
	} {
		if got := identityCertPEM(tc.id); !bytes.Equal(got, tc.want) {
			t.Errorf("%s: identityCertPEM = %q, want %q", name, got, tc.want)
		}
	}
}
