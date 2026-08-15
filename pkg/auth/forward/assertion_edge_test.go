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
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net/url"
	"testing"
	"time"
)

// errSigner is a crypto.Signer whose Sign always fails, standing in for a
// hardware or remote signer that is unavailable at forwarding time.
type errSigner struct {
	pub crypto.PublicKey
}

func (s errSigner) Public() crypto.PublicKey { return s.pub }

func (s errSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("signer unavailable")
}

// rsaNodeCert builds a certificate carrying a valid node SPIFFE SAN but an RSA
// key, so it passes the cluster-peer check and fails at the key-type check.
func rsaNodeCert(t *testing.T, trustDomain, node string) *x509.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	uri := &url.URL{Scheme: "spiffe", Host: trustDomain, Path: "/node/" + node}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "qrdb-node-rsa"},
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
	return cert
}

// TestNewCallerAssertionDefaultsTheTTL covers the ttl <= 0 branch. A zero TTL
// reaching the struct verbatim would stamp ExpiresAtUnix equal to IssuedAtUnix
// and every assertion would be born expired.
func TestNewCallerAssertionDefaultsTheTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)

	for _, ttl := range []time.Duration{0, -time.Minute} {
		a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(nil), nodeID, now, ttl)
		if want := now.Add(DefaultTTL).Unix(); a.ExpiresAtUnix != want {
			t.Errorf("ttl %v: ExpiresAtUnix = %d, want %d (DefaultTTL)", ttl, a.ExpiresAtUnix, want)
		}
	}
}

// TestNewCallerAssertionHonoursAPositiveTTL is the other half: an explicit TTL
// must not be silently replaced by the default.
func TestNewCallerAssertionHonoursAPositiveTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	ttl := 90 * time.Second

	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(nil), nodeID, now, ttl)
	if want := now.Add(ttl).Unix(); a.ExpiresAtUnix != want {
		t.Fatalf("ExpiresAtUnix = %d, want %d", a.ExpiresAtUnix, want)
	}
}

// TestSignReportsASignerFailure covers the signer-error path. The forwarding
// node must surface this as a typed error rather than emitting an assertion
// with an empty signature, which the receiving node would reject as a forgery
// and make the real fault (an unavailable signer) impossible to diagnose.
func TestSignReportsASignerFailure(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	a := NewCallerAssertion("alice", "acme", method, path, BodyDigest(nil), nodeID, now, DefaultTTL)

	ab, sb, sErr := Sign(a, errSigner{pub: &key.PublicKey})
	if sErr == nil {
		t.Fatal("Sign must fail when the signer fails")
	}
	var want *ForwardSigningError
	if !errors.As(sErr, &want) {
		t.Fatalf("error type = %T, want *ForwardSigningError", sErr)
	}
	if ab != "" || sb != "" {
		t.Fatalf("a failed Sign must return no assertion material, got %q/%q", ab, sb)
	}
}

// TestVerifyRejectsANilPeerCertificate covers the guard that runs before any
// parsing. A nil peer means the connection was not mTLS-authenticated, so
// there is no identity to bind an assertion to.
func TestVerifyRejectsANilPeerCertificate(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, key := nodeCert(t, tdom, nodeID)
	ab, sb := signValid(t, key, nil, now)

	_, err := Verify(ab, sb, nil, tdom, now, method, path, BodyDigest(nil))
	var want *ForwardAssertionNotClusterPeerError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *ForwardAssertionNotClusterPeerError", err, err)
	}
}

// TestVerifyRejectsANonECDSAPeerKey covers the key-type assertion. The
// certificate here carries a genuine node SAN, so it clears the cluster-peer
// check and fails only on the key type -- proving the branch is reachable by a
// real certificate rather than only by a malformed one.
func TestVerifyRejectsANonECDSAPeerKey(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	_, key := nodeCert(t, tdom, nodeID)
	ab, sb := signValid(t, key, nil, now)

	_, err := Verify(ab, sb, rsaNodeCert(t, tdom, nodeID), tdom, now, method, path, BodyDigest(nil))
	var want *ForwardAssertionSignatureError
	if !errors.As(err, &want) {
		t.Fatalf("error = %v (%T), want *ForwardAssertionSignatureError", err, err)
	}
}

// TestVerifyRejectsMissingRequiredFields covers the post-decode field checks.
// These run AFTER the signature verifies, so they catch a node that correctly
// signed a structurally invalid assertion -- a bug on the forwarding side, not
// an attack, and one that must not be allowed to authorize a request with an
// empty subject.
func TestVerifyRejectsMissingRequiredFields(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cert, key := nodeCert(t, tdom, nodeID)
	digest := BodyDigest(nil)

	tests := []struct {
		name  string
		build func() CallerAssertion
		field string
	}{
		{
			name: "empty subject",
			build: func() CallerAssertion {
				a := NewCallerAssertion("", "acme", method, path, digest, nodeID, now, DefaultTTL)
				return a
			},
			field: "subject",
		},
		{
			name: "whitespace-only subject",
			build: func() CallerAssertion {
				return NewCallerAssertion("   ", "acme", method, path, digest, nodeID, now, DefaultTTL)
			},
			field: "subject",
		},
		{
			name: "zero issued-at",
			build: func() CallerAssertion {
				a := NewCallerAssertion("alice", "acme", method, path, digest, nodeID, now, DefaultTTL)
				a.IssuedAtUnix = 0
				return a
			},
			field: "validity",
		},
		{
			name: "zero expiry",
			build: func() CallerAssertion {
				a := NewCallerAssertion("alice", "acme", method, path, digest, nodeID, now, DefaultTTL)
				a.ExpiresAtUnix = 0
				return a
			},
			field: "validity",
		},
		{
			name: "expiry before issue",
			build: func() CallerAssertion {
				a := NewCallerAssertion("alice", "acme", method, path, digest, nodeID, now, DefaultTTL)
				a.ExpiresAtUnix = a.IssuedAtUnix - 1
				return a
			},
			field: "validity",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ab, sb, err := Sign(tc.build(), key)
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			_, vErr := Verify(ab, sb, cert, tdom, now, method, path, digest)
			var want *ForwardAssertionFieldError
			if !errors.As(vErr, &want) {
				t.Fatalf("error = %v (%T), want *ForwardAssertionFieldError", vErr, vErr)
			}
			if want.Field != tc.field {
				t.Fatalf("Field = %q, want %q", want.Field, tc.field)
			}
		})
	}
}
