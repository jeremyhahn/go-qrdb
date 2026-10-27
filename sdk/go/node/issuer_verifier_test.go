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
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
	"github.com/jeremyhahn/go-qrdb/sdk/go/testutil/qrdbdharness"
)

// issuerVerifierServerName is the name the test listener's certificate
// carries and the client dials.
const issuerVerifierServerName = "db.example.com"

// newIssuerVerifierNode boots a single-node cluster whose trust pool's
// cluster CA is ca, and returns it with that pool's first snapshot built.
func newIssuerVerifierNode(t *testing.T, ca operatorCA) *Node {
	t.Helper()
	raftPorts, err := qrdbdharness.ReserveUDPPorts(1)
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
	cfg.Databases = []qrdbconfig.DatabaseConfig{{Name: "default", Backend: string(database.BackendMemory)}}
	cfg.Seal.Enabled = true
	cfg.Seal.Strategy = "none"
	cfg.Protocols.REST, cfg.Protocols.GRPC, cfg.Protocols.QUIC, cfg.Protocols.Unix, cfg.Protocols.MCP = false, false, false, false, false

	n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: raftAddr}, nil,
		WithRaftMTLS(&RaftMTLSConfig{CACert: ca.certPEM, Cert: certPEM, Key: keyPEM}),
		WithClusterCAKeyProvider(provider))
	if err != nil {
		t.Fatalf("NewClusteredNode: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return n
}

// issueLeaf issues a certificate under ca for the given extended key usage.
func (ca operatorCA) issueLeaf(t *testing.T, cn string, eku x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{eku},
		DNSNames: []string{issuerVerifierServerName}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// handshakeThroughPool completes an mTLS handshake against a listener
// configuration wired through the node's own trust pool, presenting client,
// and returns the server side's connection state.
func handshakeThroughPool(t *testing.T, n *Node, ca operatorCA, client tls.Certificate) tls.ConnectionState {
	t.Helper()
	srvCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{ca.issueLeaf(t, issuerVerifierServerName, x509.ExtKeyUsageServerAuth)},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    x509.NewCertPool(),
		// net.Pipe is synchronous: a ticket written after the handshake
		// would block on a client that never reads it.
		SessionTicketsDisabled: true,
	}
	n.trustPool.Load().Wire(srvCfg)
	roots := x509.NewCertPool()
	roots.AddCert(ca.cert)
	cliCfg := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      roots,
		ServerName:   issuerVerifierServerName,
		Certificates: []tls.Certificate{client},
	}

	srvConn, cliConn := net.Pipe()
	t.Cleanup(func() { _ = srvConn.Close(); _ = cliConn.Close() })
	srv, cli := tls.Server(srvConn, srvCfg), tls.Client(cliConn, cliCfg)
	cliErr := make(chan error, 1)
	go func() { cliErr <- cli.Handshake() }()
	if err := srv.Handshake(); err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	if err := <-cliErr; err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	return srv.ConnectionState()
}

// A connection the node's trust pool admitted carries no VerifiedChains --
// the pool turned RequireAndVerifyClientCert into RequireAnyClientCert and
// verified the peer itself -- and sdk.ResolveVerifiedChains, through the
// node's IssuerVerifier, resolves it to the presented leaf and the cluster
// CA it chains to (go-qrdb#1181).
func TestNode_IssuerVerifierResolvesAPoolVerifiedConnection(t *testing.T) {
	ca := newOperatorCA(t)
	n := newIssuerVerifierNode(t, ca)
	ctx, cancel := context.WithTimeout(context.Background(), embeddedCABudget)
	defer cancel()
	if err := n.trustPool.Load().Refresh(ctx); err != nil {
		t.Fatalf("trust pool Refresh: %v", err)
	}

	client := ca.issueLeaf(t, "admin", x509.ExtKeyUsageClientAuth)
	state := handshakeThroughPool(t, n, ca, client)
	if len(state.VerifiedChains) != 0 {
		t.Fatalf("VerifiedChains = %d chains; a pool-wired listener builds none", len(state.VerifiedChains))
	}

	verifier, err := n.IssuerVerifier()
	if err != nil {
		t.Fatalf("IssuerVerifier: %v", err)
	}
	chains, err := sdk.ResolveVerifiedChains(ctx, &state, verifier)
	if err != nil {
		t.Fatalf("ResolveVerifiedChains: %v", err)
	}
	if len(chains) != 1 || len(chains[0]) != 2 {
		t.Fatalf("chains = %v; want one chain of leaf and cluster CA", chains)
	}
	if !chains[0][0].Equal(client.Leaf) {
		t.Fatal("the resolved chain's leaf is not the presented certificate")
	}
	if !chains[0][1].Equal(ca.cert) {
		t.Fatal("the resolved chain does not end at the cluster CA")
	}
}

// The node's IssuerVerifier refuses what its trust pool does not trust, and
// a connection that presented no certificate, each with a typed error.
func TestNode_IssuerVerifierRefusesUntrustedAndAbsentCertificates(t *testing.T) {
	ca := newOperatorCA(t)
	n := newIssuerVerifierNode(t, ca)
	ctx, cancel := context.WithTimeout(context.Background(), embeddedCABudget)
	defer cancel()
	if err := n.trustPool.Load().Refresh(ctx); err != nil {
		t.Fatalf("trust pool Refresh: %v", err)
	}
	verifier, err := n.IssuerVerifier()
	if err != nil {
		t.Fatalf("IssuerVerifier: %v", err)
	}

	foreign := newOperatorCA(t).issueLeaf(t, "intruder", x509.ExtKeyUsageClientAuth)
	untrusted := &tls.ConnectionState{PeerCertificates: []*x509.Certificate{foreign.Leaf}}
	if _, err := sdk.ResolveVerifiedChains(ctx, untrusted, verifier); !errors.Is(err, sdk.ErrInvalidClientCertificate) {
		t.Fatalf("ResolveVerifiedChains(untrusted) = %v; want ErrInvalidClientCertificate", err)
	}

	for name, state := range map[string]*tls.ConnectionState{
		"no peer certificate": {},
		"no connection state": nil,
	} {
		if _, err := sdk.ResolveVerifiedChains(ctx, state, verifier); !errors.Is(err, sdk.ErrNoPeerCertificate) {
			t.Fatalf("ResolveVerifiedChains(%s) = %v; want ErrNoPeerCertificate", name, err)
		}
	}
}

// IssuerVerifier returns the very pool the node wired, and a typed error on a
// node whose boot has not built one.
func TestNode_IssuerVerifierIsTheWiredPool(t *testing.T) {
	if _, err := (&Node{}).IssuerVerifier(); !errors.Is(err, ErrTrustPoolNotWired) {
		t.Fatalf("IssuerVerifier before wiring = %v; want ErrTrustPoolNotWired", err)
	}

	n := newIssuerVerifierNode(t, newOperatorCA(t))
	verifier, err := n.IssuerVerifier()
	if err != nil {
		t.Fatalf("IssuerVerifier: %v", err)
	}
	if verifier != sdk.IssuerVerifier(n.trustPool.Load()) {
		t.Fatal("IssuerVerifier is not the pool the node wired onto its authenticator")
	}

	if err := n.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if verifier, err := n.IssuerVerifier(); verifier != nil || !errors.Is(err, ErrTrustPoolNotWired) {
		t.Fatalf("IssuerVerifier after Close = %v, %v; want nil, ErrTrustPoolNotWired", verifier, err)
	}
}
