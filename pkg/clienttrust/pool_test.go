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

package clienttrust

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
)

// pemEncode wraps encoding/pem.EncodeToMemory for a CERTIFICATE block.
func pemEncode(der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// fakeCertProvider implements cert.CertProvider (every method a no-op) plus
// CACertPEMs, so it satisfies CertSource via Pool's type assertion. It never
// persists or generates real key material: it only hands back whatever
// pems/err a test configured.
type fakeCertProvider struct {
	pems [][]byte
	err  error
}

var _ cert.CertProvider = (*fakeCertProvider)(nil)
var _ CertSource = (*fakeCertProvider)(nil)

func (f *fakeCertProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeCertProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (f *fakeCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakeCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakeCertProvider) CACertPEM() []byte { return nil }

func (f *fakeCertProvider) CACertPEMs(context.Context) ([][]byte, error) {
	return f.pems, f.err
}

// fakePlainCertProvider implements cert.CertProvider but NOT CACertPEMs --
// the shape of a CertProvider an embedder swapped in wholesale via
// server.WithCertProvider, which is not required to support multi-tenant
// enumeration.
type fakePlainCertProvider struct{}

var _ cert.CertProvider = (*fakePlainCertProvider)(nil)

func (f *fakePlainCertProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakePlainCertProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakePlainCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakePlainCertProvider) IsRevoked(context.Context, string) (bool, error) {
	return false, nil
}
func (f *fakePlainCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakePlainCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakePlainCertProvider) CACertPEM() []byte { return nil }

// genCAPEM generates a throwaway self-signed CA certificate and returns its
// PEM encoding.
func genCAPEM(t *testing.T) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pemEncode(der)
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return certPEM, cert
}

func baseConfig(t *testing.T, keyA *x509.CertPool) *tls.Config {
	t.Helper()
	return &tls.Config{
		ClientCAs:  keyA,
		ClientAuth: tls.VerifyClientCertIfGiven,
		MinVersion: tls.VersionTLS13,
	}
}

func getConfigForClient(t *testing.T, cfg *tls.Config) *tls.Config {
	t.Helper()
	if cfg.GetConfigForClient == nil {
		t.Fatal("GetConfigForClient was not installed")
	}
	got, err := cfg.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	return got
}

func TestPool_NoProviderInstalled_FallsBackToStatic(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	New().Wire(cfg)

	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) before SetKeyBProvider, got %+v", got)
	}
}

func TestPool_WidensPoolWithKeyBCerts(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	pool := New()
	pool.Wire(cfg)

	keyBPEM, keyBCert := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a widened config, got the static fallback (nil)")
	}
	if got.ClientCAs == nil {
		t.Fatal("expected ClientCAs to be set on the widened config")
	}
	if _, err := keyBCert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key B CA certificate was not added to the widened pool: %v", err)
	}
	if _, err := keyACert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key A was dropped from the widened pool: %v", err)
	}
}

func TestPool_PreservesOtherConfigFields(t *testing.T) {
	_, keyACert := genCAPEM(t)
	keyAPool := x509.NewCertPool()
	keyAPool.AddCert(keyACert)
	cfg := baseConfig(t, keyAPool)

	pool := New()
	pool.Wire(cfg)
	keyBPEM, _ := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a widened config")
	}
	if got.MinVersion != cfg.MinVersion {
		t.Errorf("MinVersion = %v, want %v", got.MinVersion, cfg.MinVersion)
	}
	if got.ClientAuth != cfg.ClientAuth {
		t.Errorf("ClientAuth = %v, want %v", got.ClientAuth, cfg.ClientAuth)
	}
	if got.GetConfigForClient != nil {
		t.Error("expected GetConfigForClient to be cleared on the returned clone")
	}
}

func TestPool_ProviderErrorFallsBackToStatic(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{err: errors.New("barrier is sealed")})

	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) when CACertPEMs fails (simulating sealed), got %+v", got)
	}
}

func TestPool_NoKeyBCertsYetFallsBackToStatic(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakeCertProvider{})

	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) when no tenant has established Key B yet, got %+v", got)
	}
}

func TestPool_ProviderWithoutCACertPEMsFallsBackToStatic(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	pool.SetKeyBProvider(&fakePlainCertProvider{})

	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) for a CertProvider that does not implement CertSource, got %+v", got)
	}
}

func TestPool_NilKeyBProviderRevertsToStatic(t *testing.T) {
	cfg := baseConfig(t, x509.NewCertPool())
	pool := New()
	pool.Wire(cfg)
	keyBPEM, _ := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})
	if got := getConfigForClient(t, cfg); got == nil {
		t.Fatal("expected a widened config before reverting")
	}

	pool.SetKeyBProvider(nil)
	if got := getConfigForClient(t, cfg); got != nil {
		t.Fatalf("expected (nil, nil) after SetKeyBProvider(nil), got %+v", got)
	}
}

func TestPool_NilStaticClientCAs(t *testing.T) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS13} // ClientCAs intentionally nil
	pool := New()
	pool.Wire(cfg)
	keyBPEM, keyBCert := genCAPEM(t)
	pool.SetKeyBProvider(&fakeCertProvider{pems: [][]byte{keyBPEM}})

	got := getConfigForClient(t, cfg)
	if got == nil {
		t.Fatal("expected a widened config even when the base ClientCAs was nil")
	}
	if _, err := keyBCert.Verify(x509.VerifyOptions{Roots: got.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key B CA certificate was not added to the pool: %v", err)
	}
}
