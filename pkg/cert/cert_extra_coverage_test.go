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

	"github.com/jeremyhahn/go-qrdb/pkg/cakey"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// mapProviderError — every cakey error branch
// ---------------------------------------------------------------------------

// TestMapProviderError_Translates verifies that every cakey package error
// mapProviderError understands is translated to its pkg/cert equivalent,
// which is what lets CertProvider's callers see only this package's error
// vocabulary regardless of which cakey.Provider signed (the built-in
// software one or an injected HSM/TPM/KMS adapter).
func TestMapProviderError_Translates(t *testing.T) {
	underlying := errors.New("injected")

	t.Run("barrier sealed", func(t *testing.T) {
		var want *BarrierSealedError
		if err := mapProviderError(&cakey.BarrierSealedError{}); !errors.As(err, &want) {
			t.Fatalf("mapProviderError(*cakey.BarrierSealedError) = %T, want *BarrierSealedError", err)
		}
	})
	t.Run("not initialized", func(t *testing.T) {
		var want *CAKeyNotAvailableError
		if err := mapProviderError(&cakey.NotInitializedError{}); !errors.As(err, &want) {
			t.Fatalf("mapProviderError(*cakey.NotInitializedError) = %T, want *CAKeyNotAvailableError", err)
		}
	})
	t.Run("store error", func(t *testing.T) {
		var want *StoreError
		err := mapProviderError(&cakey.StoreError{Op: "get", Detail: "d", Err: underlying})
		if !errors.As(err, &want) {
			t.Fatalf("mapProviderError(*cakey.StoreError) = %T, want *StoreError", err)
		}
		if want.Op != "get" || want.Detail != "d" || !errors.Is(want.Err, underlying) {
			t.Errorf("StoreError fields not preserved: %+v", want)
		}
	})
	t.Run("generate error", func(t *testing.T) {
		var want *CertGenerateError
		err := mapProviderError(&cakey.GenerateError{Err: underlying})
		if !errors.As(err, &want) {
			t.Fatalf("mapProviderError(*cakey.GenerateError) = %T, want *CertGenerateError", err)
		}
		if !errors.Is(want.Err, underlying) {
			t.Errorf("GenerateError.Err not preserved: %+v", want)
		}
	})
	t.Run("unrecognised error", func(t *testing.T) {
		var want *CertGenerateError
		if err := mapProviderError(underlying); !errors.As(err, &want) {
			t.Fatalf("mapProviderError(unrecognised) = %T, want *CertGenerateError", err)
		}
	})
}

// ---------------------------------------------------------------------------
// GenerateClientCert — store error persisting the cert record
// ---------------------------------------------------------------------------

// TestGenerateClientCert_PutRecordError exercises the branch where the
// Put call for the cert record fails after the certificate has been created.
func TestGenerateClientCert_PutRecordError(t *testing.T) {
	p, _, _ := initializeProvider(t)

	// Replace the KV store with one that fails on Put so the record cannot
	// be persisted.
	p.kvStore = &errorKVStore{err: errors.New("disk full")}

	req := &GenerateRequest{Username: "record-fail-user", TenantID: "system"}
	_, err := p.GenerateClientCert(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when persisting cert record fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// RevokeCert — scan store error
// ---------------------------------------------------------------------------

// TestRevokeCert_ScanError exercises the branch where the Scan call
// during RevokeCert returns an error.
func TestRevokeCert_ScanError(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	// Put a valid revocation marker so the Put call succeeds.
	// Then replace the KV store with one that fails on Scan.
	p.kvStore = &putSucceedsScanFailsKVStore{inner: p.kvStore, scanErr: errors.New("scan error")}

	_, err := p.RevokeCert(ctx, "some-serial")
	if err == nil {
		t.Fatal("expected error when Scan fails during RevokeCert")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// encodePKCS12 — CA cert parse error
// ---------------------------------------------------------------------------

// TestEncodePKCS12_InvalidCACert exercises the branch in encodePKCS12 where
// the CA cert PEM block decodes successfully but contains invalid DER data.
func TestEncodePKCS12_InvalidCACert(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	// Generate a valid client cert bundle.
	req := &GenerateRequest{Username: "pkcs12-ca-bad", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Build a cert PEM with invalid DER wrapped in valid PEM headers.
	invalidCACertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("not-valid-der"),
	})

	// Inject the invalid CA cert PEM directly into the provider.
	p.mu.Lock()
	p.caCertPEM = invalidCACertPEM
	p.mu.Unlock()

	_, err = p.ExportPKCS12(ctx, bundle.CertPEM, bundle.KeyPEM, "pw")
	if err == nil {
		t.Fatal("expected error for invalid CA cert DER")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// generateSerial — indirect coverage via unit test for the success path
// ---------------------------------------------------------------------------

// TestGenerateSerial_Success verifies that generateSerial returns a
// non-nil, positive integer each time it is called.
func TestGenerateSerial_Success(t *testing.T) {
	for i := 0; i < 5; i++ {
		serial, err := generateSerial()
		if err != nil {
			t.Fatalf("generateSerial #%d: %v", i, err)
		}
		if serial == nil {
			t.Fatal("generateSerial returned nil serial")
		}
		if serial.Sign() <= 0 {
			t.Errorf("expected positive serial, got %v", serial)
		}
	}
}

// TestGenerateSerial_Unique verifies that two successive calls produce
// different values (collision probability is negligible for 128-bit numbers).
func TestGenerateSerial_Unique(t *testing.T) {
	s1, err := generateSerial()
	if err != nil {
		t.Fatalf("first generateSerial: %v", err)
	}
	s2, err := generateSerial()
	if err != nil {
		t.Fatalf("second generateSerial: %v", err)
	}
	if s1.Cmp(s2) == 0 {
		t.Error("expected different serial numbers")
	}
}

// ---------------------------------------------------------------------------
// validateGenerateRequest — TTL < 0 branch (already covered but kept for
// completeness alongside the nil-request and empty-field branches)
// ---------------------------------------------------------------------------

// TestValidateGenerateRequest_NilRequest covers the nil-request branch.
func TestValidateGenerateRequest_NilRequest(t *testing.T) {
	err := validateGenerateRequest(nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if ve.Field != "request" {
		t.Errorf("Field: got %q, want %q", ve.Field, "request")
	}
}

// ---------------------------------------------------------------------------
// Helper types
// ---------------------------------------------------------------------------

// putSucceedsScanFailsKVStore wraps a KV store, allowing Put but injecting
// Scan errors.
type putSucceedsScanFailsKVStore struct {
	inner   kvstore.MultiGuardedBatchKVStore
	scanErr error
}

func (s *putSucceedsScanFailsKVStore) Put(ctx context.Context, key string, value []byte) error {
	return s.inner.Put(ctx, key, value)
}

func (s *putSucceedsScanFailsKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *putSucceedsScanFailsKVStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *putSucceedsScanFailsKVStore) Scan(_ context.Context, _ string, _ func(string, []byte) error) error {
	return s.scanErr
}

func (s *putSucceedsScanFailsKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *putSucceedsScanFailsKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *putSucceedsScanFailsKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (s *putSucceedsScanFailsKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (s *putSucceedsScanFailsKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (s *putSucceedsScanFailsKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (s *putSucceedsScanFailsKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (s *putSucceedsScanFailsKVStore) PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []kvstore.KV) (bool, error) {
	return s.inner.PutAllIfUnchanged(ctx, guardKey, guardValue, writes)
}

func (s *putSucceedsScanFailsKVStore) PutAllIfAllUnchanged(ctx context.Context, guards []kvstore.Guard, writes []kvstore.KV) (bool, error) {
	return s.inner.PutAllIfAllUnchanged(ctx, guards, writes)
}

// ---------------------------------------------------------------------------
// encodePKCS12 — invalid cert DER in cert block
// ---------------------------------------------------------------------------

// TestEncodePKCS12_InvalidCertDER exercises the x509.ParseCertificate error
// path in encodePKCS12 when the cert block contains non-DER data.
func TestEncodePKCS12_InvalidCertDER(t *testing.T) {
	// Build a valid EC key PEM.
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(privKey)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	// Build a cert PEM with invalid DER.
	invalidCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("not-valid-der"),
	})

	_, err = encodePKCS12(invalidCertPEM, keyPEM, nil, "pw")
	if err == nil {
		t.Fatal("expected error for invalid cert DER")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

// TestEncodePKCS12_InvalidKeyDER exercises the x509.ParseECPrivateKey error
// path in encodePKCS12 when the key block contains non-DER data.
func TestEncodePKCS12_InvalidKeyDER(t *testing.T) {
	// Build a valid self-signed cert PEM to get past the cert parse.
	privKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &privKey.PublicKey, privKey)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	// Build a key PEM with invalid DER.
	invalidKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: []byte("not-valid-key-der"),
	})

	_, err = encodePKCS12(certPEM, invalidKeyPEM, nil, "pw")
	if err == nil {
		t.Fatal("expected error for invalid key DER")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}
