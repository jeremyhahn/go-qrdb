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

package main

import (
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"testing"
	"time"

	quicraftconfig "github.com/jeremyhahn/go-quicraft/pkg/config"

	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// TestPeerTransport_TrustsClusterCAOnly_KeyBLeafRefused closes the "separate
// trust domains" ruling at the transport layer, through the REAL
// composition function (go-qrdb#591's review, item 4): buildHostConfig and
// the mtlsCfg literal runStart builds immediately before calling it
// (CACert: bundle.CACertPEM, Cert: bundle.CertPEM, Key: bundle.KeyPEM --
// the cluster CA and this node's own leaf, nothing else). This test used to
// hand-rolled a *tls.Config shaped like what go-quicraft's transport is
// documented to build from an MTLSConfig, never touching buildHostConfig
// itself, which meant a future change feeding it the wrong bundle fields
// (a tenant's Key B material, say) would not have been caught here.
//
// A client certificate signed by a SEPARATE CA (standing in for a tenant's
// Key B) is refused by a listener trusting only the MTLSConfig
// buildHostConfig was actually given; a certificate the cluster CA itself
// signed (a node leaf) is accepted. go-quicraft's own suite is responsible
// for proving its transport is built from an MTLSConfig this way; this test
// is responsible for proving go-qrdb's own composition code never feeds it
// anything but Key A.
func TestPeerTransport_TrustsClusterCAOnly_KeyBLeafRefused(t *testing.T) {
	// The REAL bundle construction path (generateCATLS), and the REAL
	// mtlsCfg literal + buildHostConfig call runStart makes, rather than
	// hand-built material parallel to, but disconnected from, that code.
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	mtlsCfg := &quicraftconfig.MTLSConfig{
		CACert: bundle.CACertPEM,
		Cert:   bundle.CertPEM,
		Key:    bundle.KeyPEM,
	}
	f := &startFlags{
		dataDir:      t.TempDir(),
		raftAddress:  "127.0.0.1:0",
		listen:       "127.0.0.1:0",
		deploymentID: 1,
	}
	hostCfg := buildHostConfig(f, mtlsCfg)
	got := hostCfg.TransportConfig.MTLSConfig
	if got == nil {
		t.Fatal("buildHostConfig returned a nil TransportConfig.MTLSConfig")
	}

	// Parse the cluster CA back out of bundle.CAKeyPEM/.CACertPEM (needed to
	// mint an ADDITIONAL node leaf for the "accepted" sub-test below) --
	// the SAME material buildHostConfig was just given, confirmed by
	// comparing got's fields to it.
	if string(got.CACert) != string(bundle.CACertPEM) || string(got.Cert) != string(bundle.CertPEM) || string(got.Key) != string(bundle.KeyPEM) {
		t.Fatal("buildHostConfig did not pass the MTLSConfig fields through unchanged")
	}
	keyACert, keyAKey := parseTestCA(t, bundle.CACertPEM, bundle.CAKeyPEM)

	nodeCert, err := tls.X509KeyPair(got.Cert, got.Key)
	if err != nil {
		t.Fatalf("X509KeyPair (node leaf from buildHostConfig's own MTLSConfig): %v", err)
	}
	keyAPool := x509.NewCertPool()
	if !keyAPool.AppendCertsFromPEM(got.CACert) {
		t.Fatal("AppendCertsFromPEM (buildHostConfig's own MTLSConfig.CACert)")
	}

	// Key B: a tenant's login CA, standing in for the real
	// cakey.SoftwareProvider-backed one. It signs a leaf the way
	// cert.DefaultCertProvider.GenerateClientCert does, with the tenant in
	// the Subject OU -- a credential that legitimately authenticates on the
	// client-facing listeners and must NOT also authenticate as a cluster
	// node. TLSBundle carries no field this material could ever leak in
	// through; it is generated independently here only to prove the
	// listener refuses it.
	_, _, keyBCert, keyBKey, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA (Key B): %v", err)
	}
	leafCertPEM, leafKeyPEM, err := clustertls.GenerateClientCert(keyBCert, keyBKey, "alice", "acme", clustertls.ClientValidity)
	if err != nil {
		t.Fatalf("GenerateClientCert (Key B leaf): %v", err)
	}
	keyBLeaf, err := tls.X509KeyPair(leafCertPEM, leafKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair (Key B leaf): %v", err)
	}

	// The peer listener: the node leaf buildHostConfig's own MTLSConfig
	// carries as its identity, that SAME MTLSConfig's CA as the ONLY
	// trusted client-auth CA -- exactly the shape go-quicraft's transport
	// builds from TransportConfig.MTLSConfig.
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{nodeCert},
		ClientCAs:    keyAPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	})
	if err != nil {
		t.Fatalf("tls.Listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	dial := func(cert tls.Certificate) error {
		conn, dialErr := tls.Dial("tcp", listener.Addr().String(), &tls.Config{
			Certificates: []tls.Certificate{cert},
			RootCAs:      keyAPool,
			ServerName:   "127.0.0.1",
			MinVersion:   tls.VersionTLS13,
		})
		if conn != nil {
			defer func() { _ = conn.Close() }()
		}
		return dialErr
	}

	t.Run("KeyBLeafRefused", func(t *testing.T) {
		serverErrCh := acceptOnce(listener)
		clientErr := dial(keyBLeaf)
		serverErr := waitForAccept(t, serverErrCh)
		if clientErr == nil && serverErr == nil {
			t.Fatal("expected the peer transport to refuse a Key B (tenant login CA) leaf")
		}
	})

	t.Run("KeyANodeLeafAccepted", func(t *testing.T) {
		nodeClientCertPEM, nodeClientKeyPEM, err := clustertls.GenerateNodeCert(keyACert, keyAKey, clustertls.NodeValidity, "node-2")
		if err != nil {
			t.Fatalf("GenerateNodeCert (dialing node): %v", err)
		}
		nodeClientCert, err := tls.X509KeyPair(nodeClientCertPEM, nodeClientKeyPEM)
		if err != nil {
			t.Fatalf("X509KeyPair: %v", err)
		}

		serverErrCh := acceptOnce(listener)
		clientErr := dial(nodeClientCert)
		serverErr := waitForAccept(t, serverErrCh)
		if clientErr != nil {
			t.Errorf("expected a Key-A-signed node leaf to be accepted, client error: %v", clientErr)
		}
		if serverErr != nil {
			t.Errorf("expected a Key-A-signed node leaf to be accepted, server error: %v", serverErr)
		}
	})
}

// acceptOnce performs ONE server-side TLS handshake on ln in a background
// goroutine and reports its outcome over the returned channel. It never
// calls t.Fatal (or anything else on *testing.T) itself: that must only
// ever run on the goroutine executing the test function, never a spawned
// one (go-qrdb#591's review, item 4) -- waitForAccept is the half of this
// pair that is allowed to.
func acceptOnce(ln net.Listener) <-chan error {
	resultCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			resultCh <- err
			return
		}
		defer func() { _ = conn.Close() }()
		resultCh <- conn.(*tls.Conn).Handshake()
	}()
	return resultCh
}

// waitForAccept waits for acceptOnce's result, or fails the test (on the
// CALLING goroutine, which must be the one running the (sub)test function)
// if it never arrives.
func waitForAccept(t *testing.T, resultCh <-chan error) error {
	t.Helper()
	select {
	case err := <-resultCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the server-side handshake")
		return nil
	}
}

// parseTestCA parses a CA certificate and its SEC1-encoded EC private key
// (clustertls.GenerateCA's own encoding) back out of PEM, for a test that
// needs to mint additional material signed by an already-generated CA.
func parseTestCA(t *testing.T, certPEM, keyPEM []byte) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	certBlock, _ := pem.Decode(certPEM)
	if certBlock == nil {
		t.Fatal("parseTestCA: certPEM did not decode")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatalf("parseTestCA: ParseCertificate: %v", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil {
		t.Fatal("parseTestCA: keyPEM did not decode")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatalf("parseTestCA: ParseECPrivateKey: %v", err)
	}
	return cert, key
}
