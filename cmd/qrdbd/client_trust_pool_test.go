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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/cert"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// These tests close the "Key B trust on the client-facing listeners" gap
// (docs/certificate-authority/architecture.md): wireClientTrustPool's
// GetConfigForClient hook must widen the client-auth trust pool to include
// Key B while the provider is available, and transparently fall back to the
// static, Key-A-only config whenever it is not (pre-unseal, a reseal, or no
// CertProvider installed at all).

// fakeKeyBCertProvider implements cert.CertProvider (every method a no-op)
// plus CACertPEMs, so it satisfies keyBCertSource via the type assertion
// wireClientTrustPool performs. It never persists or generates real key
// material: it only hands back whatever pems/err a test configured.
type fakeKeyBCertProvider struct {
	pems [][]byte
	err  error
}

var _ cert.CertProvider = (*fakeKeyBCertProvider)(nil)
var _ keyBCertSource = (*fakeKeyBCertProvider)(nil)

func (f *fakeKeyBCertProvider) GenerateClientCert(context.Context, *cert.GenerateRequest) (*cert.CertBundle, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeKeyBCertProvider) ExportPKCS12(context.Context, []byte, []byte, string) ([]byte, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeKeyBCertProvider) RevokeCert(context.Context, string) (*cert.RevokeResult, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeKeyBCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (f *fakeKeyBCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakeKeyBCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakeKeyBCertProvider) CACertPEM() []byte { return nil }

func (f *fakeKeyBCertProvider) CACertPEMs(context.Context) ([][]byte, error) {
	return f.pems, f.err
}

// fakePlainCertProvider implements cert.CertProvider but NOT CACertPEMs --
// the shape of a CertProvider an embedder swapped in wholesale via
// server.WithCertProvider, which is not required to support multi-tenant
// enumeration. It deliberately does NOT embed fakeKeyBCertProvider: Go
// method promotion through embedding would hand it CACertPEMs anyway and
// defeat the point of this double.
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
func (f *fakePlainCertProvider) IsRevoked(context.Context, string) (bool, error) { return false, nil }
func (f *fakePlainCertProvider) TenantForSerial(context.Context, string) (string, bool, error) {
	return "", false, nil
}
func (f *fakePlainCertProvider) ListCerts(context.Context, string) ([]*cert.CertRecord, error) {
	return nil, nil
}
func (f *fakePlainCertProvider) CACertPEM() []byte { return nil }

// testBundle builds a minimal TLSBundle with real Key A material, wired
// exactly as runStart wires one, for wireClientTrustPool to operate on.
func testBundle(t *testing.T) *TLSBundle {
	t.Helper()
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	bundle.wireClientTrustPool()
	return bundle
}

// genClientCertPool generates a throwaway CA and returns its PEM alongside
// an x509.CertPool trusting only Key A, to prove the dynamic pool actually
// added something the static one does not already trust.
func genSecondCAPEM(t *testing.T) []byte {
	t.Helper()
	caCertPEM, _, _, _, err := clustertls.GenerateCA(clustertls.CAValidity)
	if err != nil {
		t.Fatalf("GenerateCA: %v", err)
	}
	return caCertPEM
}

func getConfigForClient(t *testing.T, bundle *TLSBundle) *tls.Config {
	t.Helper()
	if bundle.TLSConfig.GetConfigForClient == nil {
		t.Fatal("GetConfigForClient was not installed")
	}
	cfg, err := bundle.TLSConfig.GetConfigForClient(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetConfigForClient: %v", err)
	}
	return cfg
}

func TestWireClientTrustPool_NoProviderInstalled_FallsBackToStatic(t *testing.T) {
	bundle := testBundle(t)

	cfg := getConfigForClient(t, bundle)
	if cfg != nil {
		t.Fatalf("expected (nil, nil) before SetKeyBProvider, got %+v", cfg)
	}
}

func TestWireClientTrustPool_WidensPoolWithKeyBCerts(t *testing.T) {
	bundle := testBundle(t)
	keyB := genSecondCAPEM(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{pems: [][]byte{keyB}})

	cfg := getConfigForClient(t, bundle)
	if cfg == nil {
		t.Fatal("expected a widened config, got the static fallback (nil)")
	}
	if cfg.ClientCAs == nil {
		t.Fatal("expected ClientCAs to be set on the widened config")
	}

	block, _ := pem.Decode(keyB)
	if block == nil {
		t.Fatal("genSecondCAPEM did not produce valid PEM")
	}
	keyBCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if _, err := keyBCert.Verify(x509.VerifyOptions{Roots: cfg.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key B CA certificate was not added to the widened pool: %v", err)
	}

	// Key A (the static ClientCACertPool) must still be trusted too.
	block, _ = pem.Decode(bundle.CACertPEM)
	if block == nil {
		t.Fatal("bundle.CACertPEM did not decode as PEM")
	}
	keyACert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate (Key A): %v", err)
	}
	if _, err := keyACert.Verify(x509.VerifyOptions{Roots: cfg.ClientCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
		t.Errorf("Key A was dropped from the widened pool: %v", err)
	}
}

func TestWireClientTrustPool_PreservesOtherConfigFields(t *testing.T) {
	bundle := testBundle(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{pems: [][]byte{genSecondCAPEM(t)}})

	cfg := getConfigForClient(t, bundle)
	if cfg == nil {
		t.Fatal("expected a widened config")
	}
	if cfg.MinVersion != bundle.TLSConfig.MinVersion {
		t.Errorf("MinVersion = %v, want %v", cfg.MinVersion, bundle.TLSConfig.MinVersion)
	}
	if len(cfg.Certificates) != len(bundle.TLSConfig.Certificates) {
		t.Errorf("Certificates count = %d, want %d", len(cfg.Certificates), len(bundle.TLSConfig.Certificates))
	}
	if cfg.GetConfigForClient != nil {
		t.Error("expected GetConfigForClient to be cleared on the returned clone")
	}
}

func TestWireClientTrustPool_ProviderErrorFallsBackToStatic(t *testing.T) {
	bundle := testBundle(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{err: errors.New("barrier is sealed")})

	cfg := getConfigForClient(t, bundle)
	if cfg != nil {
		t.Fatalf("expected (nil, nil) when CACertPEMs fails (simulating sealed), got %+v", cfg)
	}
}

func TestWireClientTrustPool_NoKeyBCertsYetFallsBackToStatic(t *testing.T) {
	bundle := testBundle(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{})

	cfg := getConfigForClient(t, bundle)
	if cfg != nil {
		t.Fatalf("expected (nil, nil) when no tenant has established Key B yet, got %+v", cfg)
	}
}

func TestWireClientTrustPool_ProviderWithoutCACertPEMsFallsBackToStatic(t *testing.T) {
	bundle := testBundle(t)
	bundle.SetKeyBProvider(&fakePlainCertProvider{})

	cfg := getConfigForClient(t, bundle)
	if cfg != nil {
		t.Fatalf("expected (nil, nil) for a CertProvider that does not implement keyBCertSource, got %+v", cfg)
	}
}
