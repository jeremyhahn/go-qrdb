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

package forward

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"
)

// nodeCert builds a self-consistent ECDSA P-256 certificate carrying a
// node-scoped SPIFFE SAN and returns it with its signer.
func nodeCert(t *testing.T, trustDomain, nodeID string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := &url.URL{Scheme: "spiffe", Host: trustDomain, Path: "/node/" + nodeID}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "qrdb-node"},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert, key
}

// userCert builds an ECDSA certificate with a tenant-scoped SPIFFE SAN (NOT a
// node SAN), standing in for a non-cluster user/service certificate.
func userCert(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	uri := &url.URL{Scheme: "spiffe", Host: "acme", Path: "/user/alice"}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "alice"},
		URIs:         []*url.URL{uri},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert, key
}

const (
	tdom   = "qrdb.cluster"
	nodeID = "127.0.0.1:63001"
	method = "PUT"
	path   = "/kv/foo?db=x"
)

func signValid(t *testing.T, key *ecdsa.PrivateKey, body []byte, now time.Time) (string, string) {
	t.Helper()
	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(body), nodeID, now, DefaultTTL)
	ab, sb, err := Sign(a, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return ab, sb
}

func TestVerify_ValidRoundTrip(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	body := []byte(`{"v":1}`)
	ab, sb := signValid(t, key, body, now)

	got, err := Verify(ab, sb, cert, tdom, now, method, path, BodyDigest(body))
	if err != nil {
		t.Fatalf("Verify: unexpected error %v", err)
	}
	if got.Subject != "alice" || got.TenantID != "acme" {
		t.Fatalf("identity mismatch: %+v", got)
	}
	if got.ForwarderNodeID != nodeID {
		t.Fatalf("forwarder mismatch: %q", got.ForwarderNodeID)
	}
	// Empty pinned trust domain must also verify (TLS pins the CA).
	if _, err := Verify(ab, sb, cert, "", now, method, path, BodyDigest(body)); err != nil {
		t.Fatalf("Verify (no pin): %v", err)
	}
}

func TestVerify_ForgedSignatureRejected(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	body := []byte("hello")
	ab, _ := signValid(t, key, body, now)

	// Sign with a DIFFERENT key: signature will not verify against cert.
	_, otherKey := nodeCert(t, tdom, nodeID)
	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(body), nodeID, now, DefaultTTL)
	_, forgedSig, err := Sign(a, otherKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, verr := Verify(ab, forgedSig, cert, tdom, now, method, path, BodyDigest(body))
	var sigErr *ForwardAssertionSignatureError
	if !errors.As(verr, &sigErr) {
		t.Fatalf("expected signature error, got %v", verr)
	}
}

func TestVerify_NonClusterPeerRejected(t *testing.T) {
	// A user cert (tenant SAN, no node SAN) presenting an assertion is rejected
	// even if the signature is valid for that user cert's key.
	ucert, ukey := userCert(t)
	now := time.Now()
	body := []byte("x")
	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(body), nodeID, now, DefaultTTL)
	ab, sb, err := Sign(a, ukey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	_, verr := Verify(ab, sb, ucert, tdom, now, method, path, BodyDigest(body))
	var peerErr *ForwardAssertionNotClusterPeerError
	if !errors.As(verr, &peerErr) {
		t.Fatalf("expected not-cluster-peer error, got %v", verr)
	}
}

func TestVerify_ForwarderNodeMismatchRejected(t *testing.T) {
	// Signed by a node whose SPIFFE id differs from the asserted ForwarderNodeID.
	cert, key := nodeCert(t, tdom, "other-node")
	now := time.Now()
	body := []byte("x")
	// Assertion claims nodeID but the presenting cert is "other-node".
	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(body), nodeID, now, DefaultTTL)
	ab, sb, err := Sign(a, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	_, verr := Verify(ab, sb, cert, tdom, now, method, path, BodyDigest(body))
	var peerErr *ForwardAssertionNotClusterPeerError
	if !errors.As(verr, &peerErr) {
		t.Fatalf("expected not-cluster-peer error, got %v", verr)
	}
}

func TestVerify_ExpiredAndFutureRejected(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	issued := time.Now()
	body := []byte("x")
	ab, sb := signValid(t, key, body, issued)

	// Expired: now is well past issued+TTL+skew.
	expiredNow := issued.Add(DefaultTTL + 10*time.Second)
	_, verr := Verify(ab, sb, cert, tdom, expiredNow, method, path, BodyDigest(body))
	var expErr *ForwardAssertionExpiredError
	if !errors.As(verr, &expErr) {
		t.Fatalf("expected expired error, got %v", verr)
	}

	// Future: now is well before issued-skew.
	futureNow := issued.Add(-30 * time.Second)
	_, verr = Verify(ab, sb, cert, tdom, futureNow, method, path, BodyDigest(body))
	if !errors.As(verr, &expErr) {
		t.Fatalf("expected future (expired) error, got %v", verr)
	}
}

func TestVerify_BindingMismatchRejected(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	body := []byte("original-body")
	ab, sb := signValid(t, key, body, now)

	cases := []struct {
		name              string
		method, path      string
		bodyForDigest     []byte
		wantMismatchField string
	}{
		{"method-swap", "DELETE", path, body, "method"},
		{"path-swap", method, "/kv/other", body, "path"},
		{"body-swap", method, path, []byte("tampered-body"), "body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := Verify(ab, sb, cert, tdom, now, tc.method, tc.path, BodyDigest(tc.bodyForDigest))
			var bindErr *ForwardAssertionBindingMismatchError
			if !errors.As(verr, &bindErr) {
				t.Fatalf("expected binding mismatch, got %v", verr)
			}
			if bindErr.Field != tc.wantMismatchField {
				t.Fatalf("expected %s mismatch, got %s", tc.wantMismatchField, bindErr.Field)
			}
		})
	}
}

func TestVerify_MalformedInputsRejected(t *testing.T) {
	cert, key := nodeCert(t, tdom, nodeID)
	now := time.Now()
	body := []byte("x")
	ab, sb := signValid(t, key, body, now)
	dig := BodyDigest(body)

	if _, err := Verify("not!base64", sb, cert, tdom, now, method, path, dig); err == nil {
		t.Fatal("expected error for malformed assertion b64")
	}
	if _, err := Verify(ab, "not!base64", cert, tdom, now, method, path, dig); err == nil {
		t.Fatal("expected error for malformed sig b64")
	}
	if _, err := Verify(ab, sb, nil, tdom, now, method, path, dig); err == nil {
		t.Fatal("expected error for nil peer cert")
	}
}

func TestBodyDigest_EmptyStable(t *testing.T) {
	// nil and empty must produce the same, well-known SHA-256 of empty input.
	const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if BodyDigest(nil) != emptySHA {
		t.Fatalf("nil digest = %q", BodyDigest(nil))
	}
	if BodyDigest([]byte{}) != emptySHA {
		t.Fatalf("empty digest = %q", BodyDigest([]byte{}))
	}
}

func TestSign_DeterministicAssertionBytes(t *testing.T) {
	// The assertion bytes (not the ECDSA signature, which is randomized) must be
	// identical for identical inputs, proving canonical serialization.
	_, key := nodeCert(t, tdom, nodeID)
	now := time.Unix(1000, 0)
	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest([]byte("b")), nodeID, now, DefaultTTL)
	ab1, _, err := Sign(a, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	ab2, _, err := Sign(a, key)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if ab1 != ab2 {
		t.Fatalf("assertion bytes not deterministic:\n%s\n%s", ab1, ab2)
	}
}

// TestClusterPeer admits a node certificate and refuses a nil or non-node
// certificate with the typed not-a-cluster-peer error.
func TestClusterPeer(t *testing.T) {
	nc, _ := nodeCert(t, "qrdb.cluster", "127.0.0.1:63001")
	id, err := ClusterPeer(nc, "")
	if err != nil {
		t.Fatalf("ClusterPeer(node cert): %v", err)
	}
	if id.NodeID != "127.0.0.1:63001" {
		t.Errorf("node id = %q, want 127.0.0.1:63001", id.NodeID)
	}

	uc, _ := userCert(t)
	for name, cert := range map[string]*x509.Certificate{"nil": nil, "user": uc} {
		_, err := ClusterPeer(cert, "")
		var notPeer *ForwardAssertionNotClusterPeerError
		if !errors.As(err, &notPeer) {
			t.Errorf("ClusterPeer(%s) = %v, want *ForwardAssertionNotClusterPeerError", name, err)
		}
	}

	if _, err := ClusterPeer(nc, "other.domain"); err == nil {
		t.Error("ClusterPeer admitted a node certificate outside the pinned trust domain")
	}
}
