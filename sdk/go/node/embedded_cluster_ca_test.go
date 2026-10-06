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
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth/spiffe"
	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/clusterenroll"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// embeddedCABudget bounds the test's waits on the single-node cluster.
const embeddedCABudget = 2 * time.Minute

// operatorCA is the CA an embedder's operator issues node identities under,
// and whose key an HSM or KMS (here: memory) holds.
type operatorCA struct {
	cert    *x509.Certificate
	certPEM []byte
	key     *ecdsa.PrivateKey
}

func newOperatorCA(t *testing.T) operatorCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "operator-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return operatorCA{cert: cert, certPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key: key}
}

// nodeIdentity issues the node certificate for raftAddr: a cluster node SAN
// under the default trust domain.
func (ca operatorCA) nodeIdentity(t *testing.T, raftAddr string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := url.Parse(spiffe.NodeURI(qrdbconfig.DefaultClusterTrustDomain, raftAddr))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "node-1", OrganizationalUnit: []string{"raft"}},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		URIs:        []*url.URL{uri}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// An embedded node on operator-supplied TLS with the operator CA's key
// injected (WithClusterCAKeyProvider, as go-xkms injects an HSM- or
// KMS-backed key) signs with it: its cluster CA is the injected one, a join
// token is minted and refused for the node's own identity, and a joiner's
// CSR is signed by the operator CA through the join listener.
func TestEmbeddedNode_InjectedClusterCAKeySignsAndMintsJoinTokens(t *testing.T) {
	ca := newOperatorCA(t)
	raftPorts, err := qrdbdharness.ReserveUDPPorts(1)
	if err != nil {
		t.Fatal(err)
	}
	joinPort, err := qrdbdharness.ReserveTCPPort()
	if err != nil {
		t.Fatal(err)
	}
	raftAddr := fmt.Sprintf("127.0.0.1:%d", raftPorts[0])
	certPEM, keyPEM := ca.nodeIdentity(t, raftAddr)
	provider, err := cakey.NewStaticProvider(ca.key, ca.cert)
	if err != nil {
		t.Fatal(err)
	}

	cfg := qrdbconfig.DefaultConfig()
	cfg.Raft.DataDir = t.TempDir()
	cfg.Raft.ReplicaID = 1
	cfg.Raft.RaftAddress = raftAddr
	cfg.Raft.ListenAddress = raftAddr
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendPebble)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC, cfg.Protocols.Unix, cfg.Protocols.MCP = false, false, false, false, false
	cfg.ClusterJoin.Enabled = true
	cfg.ClusterJoin.ListenHost = "127.0.0.1"
	cfg.ClusterJoin.ListenPort = joinPort

	n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: raftAddr}, nil,
		WithRaftMTLS(&RaftMTLSConfig{CACert: ca.certPEM, Cert: certPEM, Key: keyPEM}),
		WithClusterCAKeyProvider(provider))
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), embeddedCABudget)
	defer cancel()

	if n.byoTLS() {
		t.Fatal("a node with an injected cluster CA key is BYO-TLS")
	}
	if n.Services().ClusterCAProv != provider {
		t.Fatal("the node's cluster CA is not the injected provider")
	}
	if !n.trustPool.IsClusterCA(ctx, ca.cert) {
		t.Fatal("the trust pool's cluster CA is not the injected provider's certificate")
	}

	minter, ok := n.Services().Admin.(service.ClusterJoinTokenMinter)
	if !ok {
		t.Fatalf("admin service %T mints no join tokens", n.Services().Admin)
	}
	var existing *clusterenroll.ExistingMemberError
	if _, _, err := minter.MintJoinToken(ctx, raftAddr, time.Minute); !errors.As(err, &existing) {
		t.Fatalf("MintJoinToken for the node's own identity = %v; want *clusterenroll.ExistingMemberError", err)
	}
	const joiner = "127.0.0.1:1"
	var token string
	if err := retryUntil(ctx, func() (err error) {
		token, _, err = minter.MintJoinToken(ctx, joiner, time.Minute)
		return err
	}); err != nil || token == "" {
		t.Fatalf("MintJoinToken = %q, %v; want a token", token, err)
	}

	nodeURI := spiffe.NodeURI(qrdbconfig.DefaultClusterTrustDomain, joiner)
	csrDER, err := joinerCSR(nodeURI)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := clusterenroll.JoinCSR(ctx, &clusterenroll.JoinClientOptions{
		ServerAddr: n.clusterJoin.Addr(), ServerKey: n.clusterJoin.PublicKey(), Token: token,
	}, csrDER, nodeURI)
	if err != nil {
		t.Fatalf("JoinCSR: %v", err)
	}
	block, _ := pem.Decode(signed)
	if block == nil {
		t.Fatalf("JoinCSR returned no certificate: %q", signed)
	}
	leaf, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.CheckSignatureFrom(ca.cert); err != nil {
		t.Fatalf("the joiner's certificate is not signed by the injected CA: %v", err)
	}
}

// joinerCSR is a joining node's certificate request.
func joinerCSR(nodeURI string) ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(nodeURI)
	if err != nil {
		return nil, err
	}
	return x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "joiner"},
		URIs:    []*url.URL{u},
	}, key)
}

// retryUntil calls f until it succeeds or ctx ends.
func retryUntil(ctx context.Context, f func() error) error {
	for {
		err := f()
		if err == nil || ctx.Err() != nil {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}
