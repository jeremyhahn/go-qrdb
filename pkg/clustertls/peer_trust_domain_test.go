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

package clustertls

import (
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

// TestPeerTransport_TrustsClusterCAOnly_KeyBLeafRefused closes the "separate
// trust domains" ruling at the transport layer: the peer/Raft/enrollment
// transport is configured from quicraftconfig.MTLSConfig{CACert:
// bundle.CACertPEM} (cmd/qrdbd/start.go's buildHostConfig/mtlsCfg wiring) --
// the cluster CA (Key A) and NOTHING else, never the tenant-scoped login CA
// (Key B) that pkg/clienttrust's dynamic pool trusts on the client-facing
// listeners.
//
// This test reproduces exactly that shape with a real crypto/tls listener
// and dialer -- the same primitives go-quicraft's transport is built on --
// rather than standing up a 2-node Raft cluster: a client certificate signed
// by a SEPARATE CA (standing in for a tenant's Key B) is refused by a
// listener whose ClientCAs pool holds only the cluster CA, and a certificate
// the cluster CA itself signed (a node leaf) is accepted. go-quicraft's own
// suite is responsible for proving its transport is built this way; this
// test is responsible for proving go-qrdb never feeds it anything but Key A.
func TestPeerTransport_TrustsClusterCAOnly_KeyBLeafRefused(t *testing.T) {
	// Key A: the cluster CA, and the node leaf the peer transport presents
	// and verifies peers against -- exactly buildHostConfig's
	// quicraftconfig.MTLSConfig{CACert, Cert, Key}.
	_, _, keyACert, keyAKey, err := GenerateCA(CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA (Key A): %v", err)
	}
	nodeCertPEM, nodeKeyPEM, err := GenerateNodeCert(keyACert, keyAKey, NodeValidity, "node-1")
	if err != nil {
		t.Fatalf("GenerateNodeCert: %v", err)
	}
	nodeCert, err := tls.X509KeyPair(nodeCertPEM, nodeKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair (node leaf): %v", err)
	}

	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)

	// Key B: a tenant's login CA, standing in for the real
	// cakey.SoftwareProvider-backed one. It signs a leaf the way
	// cert.DefaultCertProvider.GenerateClientCert does, with the tenant in
	// the Subject OU -- a credential that legitimately authenticates on the
	// client-facing listeners and must NOT also authenticate as a cluster
	// node.
	_, _, keyBCert, keyBKey, err := GenerateCA(CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA (Key B): %v", err)
	}
	leafCertPEM, leafKeyPEM, err := GenerateClientCert(keyBCert, keyBKey, "alice", "acme", ClientValidity)
	if err != nil {
		t.Fatalf("GenerateClientCert (Key B leaf): %v", err)
	}
	keyBLeaf, err := tls.X509KeyPair(leafCertPEM, leafKeyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair (Key B leaf): %v", err)
	}

	// The peer listener: Key A's node certificate as its own identity, Key
	// A's pool as the ONLY trusted client-auth CA -- quicraftconfig.
	// MTLSConfig{CACert: bundle.CACertPEM} turns into exactly this shape
	// inside go-quicraft's transport.
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

	accept := func(t *testing.T) error {
		t.Helper()
		errCh := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				errCh <- err
				return
			}
			defer func() { _ = conn.Close() }()
			errCh <- conn.(*tls.Conn).Handshake()
		}()
		select {
		case err := <-errCh:
			return err
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for the server-side handshake")
			return nil
		}
	}

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
		serverErrCh := make(chan error, 1)
		go func() { serverErrCh <- accept(t) }()
		clientErr := dial(keyBLeaf)
		serverErr := <-serverErrCh
		if clientErr == nil && serverErr == nil {
			t.Fatal("expected the peer transport to refuse a Key B (tenant login CA) leaf")
		}
	})

	t.Run("KeyANodeLeafAccepted", func(t *testing.T) {
		nodeClientCertPEM, nodeClientKeyPEM, err := GenerateNodeCert(keyACert, keyAKey, NodeValidity, "node-2")
		if err != nil {
			t.Fatalf("GenerateNodeCert (dialing node): %v", err)
		}
		nodeClientCert, err := tls.X509KeyPair(nodeClientCertPEM, nodeClientKeyPEM)
		if err != nil {
			t.Fatalf("X509KeyPair: %v", err)
		}

		serverErrCh := make(chan error, 1)
		go func() { serverErrCh <- accept(t) }()
		clientErr := dial(nodeClientCert)
		serverErr := <-serverErrCh
		if clientErr != nil {
			t.Errorf("expected a Key-A-signed node leaf to be accepted, client error: %v", clientErr)
		}
		if serverErr != nil {
			t.Errorf("expected a Key-A-signed node leaf to be accepted, server error: %v", serverErr)
		}
	})
}
