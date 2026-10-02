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

package wiring

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// fakeCeremonyCA implements ceremony.CeremonyCA; only CACertificate matters
// for issuerTenantBinding, so the rest are unreachable stubs.
type fakeCeremonyCA struct {
	cert *x509.Certificate
	err  error
}

var _ ceremony.CeremonyCA = (*fakeCeremonyCA)(nil)

func (f *fakeCeremonyCA) CACertificate() (*x509.Certificate, error) { return f.cert, f.err }
func (f *fakeCeremonyCA) CABundle() ([]byte, error)                 { return nil, nil }
func (f *fakeCeremonyCA) SignCSR(context.Context, []byte, *ceremony.SignCSROptions) (*x509.Certificate, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeCeremonyCA) IssueCertificate(context.Context, *ceremony.IssueCertRequest) (*ceremony.IssuedCert, error) {
	return nil, errors.New("not implemented")
}

// fakeTenantIssuerProvider implements cert.CertProvider (no-op) plus
// IsTenantIssuer, so it satisfies auth.TenantIssuerChecker.
type fakeTenantIssuerProvider struct{}

func (f *fakeTenantIssuerProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeTenantIssuerProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeTenantIssuerProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeTenantIssuerProvider) IsRevoked(context.Context, string) (bool, error) {
	return false, nil
}
func (f *fakeTenantIssuerProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakeTenantIssuerProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakeTenantIssuerProvider) CACertPEM() []byte { return nil }
func (f *fakeTenantIssuerProvider) IsTenantIssuer(context.Context, string, *x509.Certificate) (bool, error) {
	return true, nil
}

// plainCertProvider implements cert.CertProvider but NOT IsTenantIssuer --
// the shape of an embedder's CertProvider replacement that does not support
// the binding check. It deliberately does NOT embed fakeTenantIssuerProvider:
// Go method promotion through embedding would hand it IsTenantIssuer anyway
// and defeat the point of this double.
type plainCertProvider struct{}

func (f *plainCertProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *plainCertProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *plainCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *plainCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (f *plainCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *plainCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *plainCertProvider) CACertPEM() []byte { return nil }

var _ cert.CertProvider = (*plainCertProvider)(nil)

func genTestCA(t *testing.T) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return c
}

func TestIssuerTenantBinding_NilClusterCA(t *testing.T) {
	if b := issuerTenantBinding(nil, &fakeTenantIssuerProvider{}); b != nil {
		t.Fatalf("expected nil binding when clusterCA is nil, got %+v", b)
	}
}

func TestIssuerTenantBinding_ClusterCACertificateError(t *testing.T) {
	ca := &fakeCeremonyCA{err: errors.New("not established yet")}
	if b := issuerTenantBinding(ca, &fakeTenantIssuerProvider{}); b != nil {
		t.Fatalf("expected nil binding when CACertificate fails, got %+v", b)
	}
}

func TestIssuerTenantBinding_BuildsWithCapableProvider(t *testing.T) {
	caCert := genTestCA(t)
	ca := &fakeCeremonyCA{cert: caCert}
	b := issuerTenantBinding(ca, &fakeTenantIssuerProvider{})
	if b == nil {
		t.Fatal("expected a non-nil binding")
	}
	if b.ClusterCA != caCert {
		t.Error("expected ClusterCA to be the resolved cluster CA certificate")
	}
	if b.KeyB == nil {
		t.Error("expected KeyB to be set from a provider that implements TenantIssuerChecker")
	}
}

func TestIssuerTenantBinding_ProviderWithoutCheckerLeavesKeyBNil(t *testing.T) {
	caCert := genTestCA(t)
	ca := &fakeCeremonyCA{cert: caCert}
	b := issuerTenantBinding(ca, &plainCertProvider{})
	if b == nil {
		t.Fatal("expected a non-nil binding (the ClusterCA exemption still applies)")
	}
	if b.KeyB != nil {
		t.Error("expected KeyB to be nil for a provider that does not implement TenantIssuerChecker")
	}
}

func TestIssuerTenantBinding_NilProvider(t *testing.T) {
	caCert := genTestCA(t)
	ca := &fakeCeremonyCA{cert: caCert}
	b := issuerTenantBinding(ca, nil)
	if b == nil {
		t.Fatal("expected a non-nil binding even with a nil CertProvider")
	}
	if b.KeyB != nil {
		t.Error("expected KeyB to be nil when provider is nil")
	}
}
