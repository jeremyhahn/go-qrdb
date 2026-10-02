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
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/auth/forward"
)

// requestWithCaller builds an *http.Request for the given method/target/body
// with an authenticated caller identity injected into its context (as the entry
// node's auth middleware would have done before forwarding).
func requestWithCaller(method, target string, body []byte, caller *auth.Identity) *http.Request {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	if caller != nil {
		r = r.WithContext(auth.WithIdentity(r.Context(), caller))
	}
	return r
}

// fwdNodeTLSCert builds a tls.Certificate carrying an ECDSA node SPIFFE SAN,
// with Leaf populated, standing in for a cluster node's identity certificate.
func fwdNodeTLSCert(t *testing.T, nodeID string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	uri := &url.URL{Scheme: "spiffe", Host: "qrdb.cluster", Path: "/node/" + nodeID}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(20),
		Subject:      pkix.Name{CommonName: "qrdb-node"},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("createcert: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parsecert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

const fwdTestNodeID = "127.0.0.1:63001"

// newForwardServer builds a Server whose forward identity is derived from a
// node TLS certificate. WithLeaderForwarding is required so NewServer runs the
// forward-identity derivation.
func newForwardServer(t *testing.T) (*Server, *x509.Certificate) {
	t.Helper()
	nodeCert := fwdNodeTLSCert(t, fwdTestNodeID)
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{nodeCert}}
	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithLeaderForwarding(reg), WithTLS(tlsCfg))
	return srv, nodeCert.Leaf
}

func TestDeriveForwardIdentity_FromNodeCert(t *testing.T) {
	srv, _ := newForwardServer(t)
	if srv.forwardSigner == nil {
		t.Fatal("forwardSigner not derived from node cert")
	}
	if srv.forwardNodeID != fwdTestNodeID {
		t.Fatalf("forwardNodeID = %q, want %q", srv.forwardNodeID, fwdTestNodeID)
	}
}

func TestDeriveForwardIdentity_NoTLS(t *testing.T) {
	if s, id := deriveForwardIdentity(nil); s != nil || id != "" {
		t.Fatalf("nil tls: got signer=%v id=%q", s, id)
	}
	// A cert without a node SAN yields a signer but empty node id.
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	cfg := &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}}}
	if s, id := deriveForwardIdentity(cfg); s == nil || id != "" {
		t.Fatalf("no-node-SAN cert: got signer=%v id=%q", s, id)
	}
}

// TestAttachOnBehalfAssertion_VerifiesOnLeader proves the forwarding node emits
// headers the leader can verify: it signs an assertion bound to the request,
// and forward.Verify (the leader's check) accepts it against the node cert and
// recovers the original caller identity.
func TestAttachOnBehalfAssertion_VerifiesOnLeader(t *testing.T) {
	srv, leafCert := newForwardServer(t)

	body := []byte(`{"value":"x"}`)
	// The original (already-authenticated) caller request at the entry node.
	orig := requestWithCaller(http.MethodPut, "/kv/foo?db=scaledb", body,
		&auth.Identity{Subject: "alice", TenantID: "acme", Roles: []string{"should-be-ignored"}})

	// The outbound forwarded request the entry node builds.
	fwd, err := http.NewRequest(http.MethodPut, "https://leader/kv/foo?db=scaledb", nil)
	if err != nil {
		t.Fatalf("new fwd req: %v", err)
	}

	if err := srv.attachOnBehalfAssertion(fwd, orig, body); err != nil {
		t.Fatalf("attachOnBehalfAssertion: %v", err)
	}

	ab := fwd.Header.Get(forward.HeaderAssertion)
	sb := fwd.Header.Get(forward.HeaderSignature)
	if ab == "" || sb == "" {
		t.Fatal("on-behalf headers not attached")
	}

	got, verr := forward.Verify(ab, sb, leafCert, "qrdb.cluster", time.Now(),
		orig.Method, orig.URL.RequestURI(), forward.BodyDigest(body))
	if verr != nil {
		t.Fatalf("leader verify failed: %v", verr)
	}
	if got.Subject != "alice" || got.TenantID != "acme" {
		t.Fatalf("assertion carried wrong identity: %+v", got)
	}
	if got.ForwarderNodeID != fwdTestNodeID {
		t.Fatalf("forwarder node id = %q, want %q", got.ForwarderNodeID, fwdTestNodeID)
	}
}

// TestAttachOnBehalfAssertion_SkipsWithoutCaller: no authenticated caller in
// context (e.g. auth disabled) -> no assertion attached, behavior unchanged.
func TestAttachOnBehalfAssertion_SkipsWithoutCaller(t *testing.T) {
	srv, _ := newForwardServer(t)
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"), nil)
	fwd, _ := http.NewRequest(http.MethodPut, "https://leader/kv/foo", nil)
	if err := srv.attachOnBehalfAssertion(fwd, orig, []byte("x")); err != nil {
		t.Fatalf("attachOnBehalfAssertion: %v", err)
	}
	if fwd.Header.Get(forward.HeaderAssertion) != "" {
		t.Fatal("assertion attached without a caller identity")
	}
}

// TestAttachOnBehalfAssertion_SkipsWithoutSigner: a server with no forward
// signer (no TLS identity) never attaches an assertion even with a caller.
func TestAttachOnBehalfAssertion_SkipsWithoutSigner(t *testing.T) {
	reg := NewPeerAdminRegistry("63002")
	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithLeaderForwarding(reg))
	if srv.forwardSigner != nil {
		t.Fatal("unexpected forward signer without TLS")
	}
	orig := requestWithCaller(http.MethodPut, "/kv/foo", []byte("x"),
		&auth.Identity{Subject: "alice", TenantID: "acme"})
	fwd, _ := http.NewRequest(http.MethodPut, "https://leader/kv/foo", nil)
	if err := srv.attachOnBehalfAssertion(fwd, orig, []byte("x")); err != nil {
		t.Fatalf("attachOnBehalfAssertion: %v", err)
	}
	if fwd.Header.Get(forward.HeaderAssertion) != "" {
		t.Fatal("assertion attached without a signer")
	}
}
