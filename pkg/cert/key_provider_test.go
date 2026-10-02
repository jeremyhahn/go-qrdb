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

package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
)

// ---------------------------------------------------------------------------
// WithKeyProvider / the injected cakey.Provider path
//
// These tests close the "injected HSM-style provider double" requirement:
// an embedder's cakey.Provider (shaped like go-xkms's CASigningIdentity +
// CACertificateAccessor, composed through cakey.Adapter -- see
// docs/certificate-authority/architecture.md, "cakey.Adapter") signs every
// Key B certificate, and the built-in per-tenant software path is never
// touched: no wrapped key or cert PEM is ever written under prefixCertCA.
// ---------------------------------------------------------------------------

// hsmDouble is a same-shaped-as-xkms signing identity double: it holds its
// own in-memory key and certificate and counts Sign calls, so tests can
// prove every signature actually went through it.
type hsmDouble struct {
	key       *ecdsa.PrivateKey
	cert      *x509.Certificate
	signCalls int
	certCalls int
	signErr   error
	certErr   error
}

func newHSMDouble(t *testing.T) *hsmDouble {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(42),
		Subject:               pkix.Name{CommonName: "hsm-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return &hsmDouble{key: key, cert: cert}
}

// provider returns a cakey.Adapter over this double, exactly the "no glue
// code beyond cakey.Adapter" shape docs/certificate-authority/architecture.md
// describes for an xkms-backed CA.
func (d *hsmDouble) provider() cakey.Adapter {
	return cakey.Adapter{
		SignerFunc: func(context.Context) (crypto.Signer, error) {
			d.signCalls++
			if d.signErr != nil {
				return nil, d.signErr
			}
			return d.key, nil
		},
		CertificateFunc: func(context.Context) (*x509.Certificate, error) {
			d.certCalls++
			if d.certErr != nil {
				return nil, d.certErr
			}
			return d.cert, nil
		},
	}
}

func TestDefaultCertProvider_WithKeyProvider_SignsThroughInjectedProvider(t *testing.T) {
	hsm := newHSMDouble(t)
	kv := newMockKVStore()
	barrier := newMockBarrier()
	p := NewDefaultProvider(kv, barrier, WithKeyProvider(hsm.provider()))
	ctx := context.Background()

	bundle, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: "acme"})
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if hsm.signCalls == 0 {
		t.Error("expected the injected provider's Signer to be called")
	}
	if hsm.certCalls == 0 {
		t.Error("expected the injected provider's Certificate to be called")
	}

	leaf := parsePEMCert(t, bundle.CertPEM)
	if err := leaf.CheckSignatureFrom(hsm.cert); err != nil {
		t.Errorf("leaf was not signed by the injected provider's certificate: %v", err)
	}

	// No software key or cert material was ever written under prefixCertCA:
	// the built-in per-tenant default is never constructed when a provider
	// is injected.
	keys, err := kv.List(ctx, prefixCertCA)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected no software CA material stored, got keys: %v", keys)
	}
}

func TestDefaultCertProvider_WithKeyProvider_InitializeAndUnseal(t *testing.T) {
	hsm := newHSMDouble(t)
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProvider(hsm.provider()))
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if got := p.CACertPEM(); len(got) == 0 {
		t.Error("expected CACertPEM to be warmed from the injected provider after Initialize")
	}

	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := p.Unseal(ctx); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
}

func TestDefaultCertProvider_WithKeyProvider_SignerError(t *testing.T) {
	hsm := newHSMDouble(t)
	hsm.signErr = errors.New("device unavailable")
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProvider(hsm.provider()))

	_, err := p.GenerateClientCert(context.Background(), &GenerateRequest{Username: "bob", TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error when the injected provider's Signer fails")
	}
	var cge *CertGenerateError
	if !errors.As(err, &cge) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_WithKeyProvider_CertificateError(t *testing.T) {
	hsm := newHSMDouble(t)
	hsm.certErr = errors.New("device unavailable")
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProvider(hsm.provider()))

	_, err := p.GenerateClientCert(context.Background(), &GenerateRequest{Username: "bob", TenantID: "acme"})
	if err == nil {
		t.Fatal("expected error when the injected provider's Certificate fails")
	}
}

// ---------------------------------------------------------------------------
// CACertPEMs
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_CACertPEMs_BuiltInMultiTenant(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()

	for _, tenantID := range []string{"acme", "globex"} {
		if _, err := p.GenerateClientCert(ctx, &GenerateRequest{Username: "alice", TenantID: tenantID}); err != nil {
			t.Fatalf("GenerateClientCert(%s): %v", tenantID, err)
		}
	}

	pems, err := p.CACertPEMs(ctx)
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 2 {
		t.Fatalf("CACertPEMs returned %d entries, want 2", len(pems))
	}
	for _, certPEM := range pems {
		if parsePEMCert(t, certPEM) == nil {
			t.Error("CACertPEMs entry did not parse as a certificate")
		}
	}
}

func TestDefaultCertProvider_CACertPEMs_Empty(t *testing.T) {
	p, _, _ := newTestProvider()
	pems, err := p.CACertPEMs(context.Background())
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 0 {
		t.Errorf("expected no entries before any tenant's CA is established, got %d", len(pems))
	}
}

func TestDefaultCertProvider_CACertPEMs_ScanError(t *testing.T) {
	p := NewDefaultProvider(&errorKVStore{err: errors.New("disk read failed")}, newMockBarrier())
	_, err := p.CACertPEMs(context.Background())
	if err == nil {
		t.Fatal("expected error when the KVStore scan fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_CACertPEMs_InjectedProvider(t *testing.T) {
	hsm := newHSMDouble(t)
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProvider(hsm.provider()))

	pems, err := p.CACertPEMs(context.Background())
	if err != nil {
		t.Fatalf("CACertPEMs: %v", err)
	}
	if len(pems) != 1 {
		t.Fatalf("CACertPEMs returned %d entries, want 1", len(pems))
	}
	if parsePEMCert(t, pems[0]).Subject.CommonName != "hsm-ca" {
		t.Error("CACertPEMs did not return the injected provider's certificate")
	}
}

func TestDefaultCertProvider_CACertPEMs_InjectedProviderError(t *testing.T) {
	hsm := newHSMDouble(t)
	hsm.certErr = errors.New("device unavailable")
	p := NewDefaultProvider(newMockKVStore(), newMockBarrier(), WithKeyProvider(hsm.provider()))

	_, err := p.CACertPEMs(context.Background())
	if err == nil {
		t.Fatal("expected error when the injected provider's Certificate fails")
	}
}
