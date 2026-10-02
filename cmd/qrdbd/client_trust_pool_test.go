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
	"github.com/jeremyhahn/go-qrdb/pkg/clienttrust"
	"github.com/jeremyhahn/go-qrdb/pkg/clustertls"
)

// These tests prove TLSBundle wires pkg/clienttrust correctly -- the actual
// trust-pool widening/fallback behavior is pkg/clienttrust's own test
// suite's job (pkg/clienttrust/pool_test.go). qrdbd, sdk/go/node, and the
// qrdb_embedded composer all share that ONE implementation; this file only
// proves qrdbd's wiring of it.

// fakeKeyBCertProvider implements cert.CertProvider (every method a no-op)
// plus CACertPEMs, so it satisfies clienttrust.CertSource. It never
// persists or generates real key material: it only hands back whatever
// pems/err a test configured.
type fakeKeyBCertProvider struct {
	pems [][]byte
	err  error
}

var _ cert.CertProvider = (*fakeKeyBCertProvider)(nil)
var _ clienttrust.CertSource = (*fakeKeyBCertProvider)(nil)

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

func testBundle(t *testing.T) *TLSBundle {
	t.Helper()
	bundle, err := generateCATLS()
	if err != nil {
		t.Fatalf("generateCATLS: %v", err)
	}
	// Matches the production sequence in runStart: ClientCAs carries Key A
	// before wireClientTrustPool installs the hook that reads it fresh on
	// every connection.
	bundle.TLSConfig.ClientCAs = bundle.ClientCACertPool
	bundle.wireClientTrustPool()
	return bundle
}

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

func TestWireClientTrustPool_InstallsHook(t *testing.T) {
	bundle := testBundle(t)
	if cfg := getConfigForClient(t, bundle); cfg != nil {
		t.Fatalf("expected (nil, nil) before SetKeyBProvider, got %+v", cfg)
	}
}

func TestWireClientTrustPool_SetKeyBProviderWidensPool(t *testing.T) {
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

func TestWireClientTrustPool_ProviderErrorFallsBackToStatic(t *testing.T) {
	bundle := testBundle(t)
	bundle.SetKeyBProvider(&fakeKeyBCertProvider{err: errors.New("barrier is sealed")})

	if cfg := getConfigForClient(t, bundle); cfg != nil {
		t.Fatalf("expected (nil, nil) when CACertPEMs fails (simulating sealed), got %+v", cfg)
	}
}
