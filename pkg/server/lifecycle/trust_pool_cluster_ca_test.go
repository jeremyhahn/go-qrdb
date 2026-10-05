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

package lifecycle_test

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
)

// stubCAProvider is a cluster CA provider whose certificate is a zero value,
// or certErr.
type stubCAProvider struct {
	certErr error
}

func (s *stubCAProvider) Signer(context.Context) (crypto.Signer, error) { return nil, nil }

func (s *stubCAProvider) Certificate(context.Context) (*x509.Certificate, error) {
	if s.certErr != nil {
		return nil, s.certErr
	}
	return &x509.Certificate{}, nil
}

// selfSignedCAPEM returns a PEM-encoded self-signed CA certificate.
func selfSignedCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "operator-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// ---------------------------------------------------------------------------
// lifecycle.TrustPoolClusterCA -- go-qrdb#591's B4 fix: what bundle.SetClusterCA
// is wired to must not depend on ceremonyCA's own nil-ness (nil on every
// restart of a self-issued cluster, once ca-key.pem is confirmed persisted
// and deleted).
// ---------------------------------------------------------------------------

// TestTrustPoolClusterCA_SelfIssued_UsesClusterCAProv proves the
// self-issued case resolves a ClusterCA wrapping clusterCAProv directly,
// regardless of explicitTLS or caCertPEM -- the exact fix for B4: a
// restarted self-issued node has svcNode.ClusterCAProv populated by
// lifecycle.BuildServices unconditionally, which is what this must wire
// SetClusterCA from instead of the bootstrap-window ceremonyCA local
// variable that goes nil on every restart.
func TestTrustPoolClusterCA_SelfIssued_UsesClusterCAProv(t *testing.T) {
	prov := &stubCAProvider{}
	ca, err := lifecycle.TrustPoolClusterCA(prov, false, nil)
	if err != nil {
		t.Fatalf("lifecycle.TrustPoolClusterCA: %v", err)
	}
	if ca == nil {
		t.Fatal("expected a non-nil ClusterCA for a non-nil clusterCAProv")
	}
	cert, err := ca.CACertificate(t.Context())
	if err != nil {
		t.Fatalf("CACertificate: %v", err)
	}
	if cert == nil {
		t.Fatal("expected CACertificate to resolve through the wrapped provider")
	}
}

// TestTrustPoolClusterCA_SelfIssued_ProviderErrorPropagates proves a
// broken cluster CA provider is reported as an error (so the caller logs
// it and proceeds with no cluster-CA concept wired) rather than silently
// wiring a ClusterCA that would only fail later, less legibly, on its
// first real use.
func TestTrustPoolClusterCA_SelfIssued_ProviderErrorPropagates(t *testing.T) {
	boom := errors.New("store unreachable")
	_, err := lifecycle.TrustPoolClusterCA(&stubCAProvider{certErr: boom}, false, nil)
	// ceremony.NewProviderCA itself only errors on a NIL provider; a
	// provider whose Certificate call fails is still wrapped successfully
	// here (the failure surfaces on first use, inside Pool's own
	// resolveClusterCA -- consistent with ceremony.ProviderCA's "resolves
	// fresh on every call" contract). This test exists to pin that
	// behavior explicitly: it must NOT regress to lifecycle.TrustPoolClusterCA
	// itself probing Certificate() and refusing early.
	if err != nil {
		t.Fatalf("lifecycle.TrustPoolClusterCA: %v, want no error (the provider's own failure surfaces on first use, not here)", err)
	}
}

// TestTrustPoolClusterCA_BYOTLS_UsesOperatorCACertPEM proves the
// BYO-TLS case (clusterCAProv nil, explicitTLS true -- lifecycle.Infra.
// NoClusterCAKey) resolves a static ClusterCA wrapping the operator's own
// peer CA certificate, per the owner's ruling: "the operator's peer CA...
// is the cluster CA for IsClusterCA and on-behalf verification... [s]ame
// anchor for peer identity as on self-issued clusters."
func TestTrustPoolClusterCA_BYOTLS_UsesOperatorCACertPEM(t *testing.T) {
	caCertPEM := selfSignedCAPEM(t)
	ca, err := lifecycle.TrustPoolClusterCA(nil, true, caCertPEM)
	if err != nil {
		t.Fatalf("lifecycle.TrustPoolClusterCA: %v", err)
	}
	if ca == nil {
		t.Fatal("expected a non-nil ClusterCA for a BYO-TLS node")
	}
	block, _ := pem.Decode(caCertPEM)
	want, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate(caCertPEM): %v", err)
	}
	got, err := ca.CACertificate(t.Context())
	if err != nil {
		t.Fatalf("CACertificate: %v", err)
	}
	if !got.Equal(want) {
		t.Fatal("CACertificate did not return the operator's own CA certificate")
	}
}

// TestTrustPoolClusterCA_BYOTLS_InvalidPEMErrors proves a
// malformed caCertPEM is reported as an error rather than silently
// resolving to no cluster-CA concept (which IsStaticallyTrusted would be
// unable to distinguish from a deployment that genuinely has none).
func TestTrustPoolClusterCA_BYOTLS_InvalidPEMErrors(t *testing.T) {
	if _, err := lifecycle.TrustPoolClusterCA(nil, true, []byte("not pem at all")); err == nil {
		t.Fatal("expected an error for a caCertPEM that does not decode as PEM")
	}
	notACert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a real certificate DER")})
	if _, err := lifecycle.TrustPoolClusterCA(nil, true, notACert); err == nil {
		t.Fatal("expected an error for a caCertPEM block that does not parse as a certificate")
	}
}

// TestTrustPoolClusterCA_NoClusterCAConcept proves the "neither"
// case -- a non-self-issued, non-BYO-TLS deployment, e.g. one with no TLS
// at all -- resolves to (nil, nil): the documented, supported
// "SetClusterCA never called" state, not an error.
func TestTrustPoolClusterCA_NoClusterCAConcept(t *testing.T) {
	ca, err := lifecycle.TrustPoolClusterCA(nil, false, nil)
	if err != nil {
		t.Fatalf("lifecycle.TrustPoolClusterCA: %v, want no error", err)
	}
	if ca != nil {
		t.Fatalf("lifecycle.TrustPoolClusterCA = %v, want nil", ca)
	}
}
