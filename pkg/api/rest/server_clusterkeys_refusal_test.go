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

package rest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clusterkeys"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
	"github.com/jeremyhahn/go-qrdb/pkg/keydelivery"
	"github.com/jeremyhahn/go-qrdb/pkg/placement"
	"github.com/jeremyhahn/go-qrdb/pkg/seal"
)

// refusingPlacements answers every lookup with an error: a request that gets
// past the node-certificate check is refused at the placement gate (403), so
// a 403 proves the request was authenticated and a 401 that it was not.
type refusingPlacements struct{}

func (refusingPlacements) Get(context.Context, string, string) (*placement.Placement, error) {
	return nil, errors.New("no placement")
}

// keyDeliveryFixture is a REST server serving a real clusterkeys.Receiver,
// and the cluster CA its senders' certificates must chain to.
type keyDeliveryFixture struct {
	srv    *Server
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
}

func newKeyDeliveryFixture(t *testing.T) *keyDeliveryFixture {
	t.Helper()
	_, _, caCert, caKey, err := clustertls.GenerateCA(time.Hour)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	dec, err := keydelivery.PrivateKeyFor(keydelivery.KEMP256, nodeKey)
	if err != nil {
		t.Fatalf("PrivateKeyFor: %v", err)
	}
	kd, err := keydelivery.NewReceiver(dec, "follower-node-1")
	if err != nil {
		t.Fatalf("keydelivery.NewReceiver: %v", err)
	}
	// Every request these tests send is answered before the barrier
	// registry is reached (401 at the certificate, 403 at the placement
	// gate), so an empty registry stands in for one.
	rcv, err := clusterkeys.NewReceiver(kd, new(seal.BarrierRegistry), refusingPlacements{},
		"cluster.local", "follower-node-1", pool, nil, []byte("delivery cert"), nil)
	if err != nil {
		t.Fatalf("clusterkeys.NewReceiver: %v", err)
	}
	return &keyDeliveryFixture{
		srv:    NewServer(nil, nil, "127.0.0.1:0", WithClusterKeyReceiver(rcv)),
		caCert: caCert,
		caKey:  caKey,
	}
}

// deliver POSTs a delivery through the REST server's dispatch, over a TLS
// connection carrying peer as its client certificate (none when nil).
func (f *keyDeliveryFixture) deliver(peer *x509.Certificate) int {
	req := httptest.NewRequest(http.MethodPost, "/v1/cluster/keydelivery", strings.NewReader("{}"))
	req.TLS = &tls.ConnectionState{}
	if peer != nil {
		req.TLS.PeerCertificates = []*x509.Certificate{peer}
	}
	rr := httptest.NewRecorder()
	f.srv.dispatch(rr, req)
	return rr.Code
}

func parseCertPEM(t *testing.T, certPEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("no PEM block")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

// TestKeyDelivery_NoClientCertificate_401: a caller with no client
// certificate is refused as unauthenticated. HPKE base mode authenticates no
// sender, so the route admits only a cluster node.
func TestKeyDelivery_NoClientCertificate_401(t *testing.T) {
	t.Parallel()
	f := newKeyDeliveryFixture(t)
	if code := f.deliver(nil); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

// TestKeyDelivery_ClientCertificateNotANode_401: a client certificate the
// cluster CA issued to a tenant client names no node, and is refused as
// unauthenticated.
func TestKeyDelivery_ClientCertificateNotANode_401(t *testing.T) {
	t.Parallel()
	f := newKeyDeliveryFixture(t)
	certPEM, _, err := clustertls.GenerateClientCert(f.caCert, f.caKey, "cluster.local", "tenant-a", "alice", time.Hour)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if code := f.deliver(parseCertPEM(t, certPEM)); code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

// TestKeyDelivery_NodeCertificate_Authenticated: a node certificate the
// cluster CA issued passes the check and reaches the placement gate.
func TestKeyDelivery_NodeCertificate_Authenticated(t *testing.T) {
	t.Parallel()
	f := newKeyDeliveryFixture(t)
	certPEM, _, err := clustertls.GenerateNodeCert(f.caCert, f.caKey, time.Hour, "spiffe://cluster.local/node/leader-node-1")
	if err != nil {
		t.Fatalf("GenerateNodeCert: %v", err)
	}
	if code := f.deliver(parseCertPEM(t, certPEM)); code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 from the placement gate", code)
	}
}
