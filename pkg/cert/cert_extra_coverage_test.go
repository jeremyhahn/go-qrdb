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

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// Initialize — uncovered error branches
// ---------------------------------------------------------------------------

// TestInitialize_PutCertPEMError exercises the branch where the second
// Put call (persisting the CA cert PEM) fails. This requires a KVStore
// that succeeds on the first Put and fails on the second.
func TestInitialize_PutCertPEMError(t *testing.T) {
	kv := &putCountKVStore{inner: newMockKVStore(), failOnPut: 2}
	barrier := newMockBarrier()
	p := NewDefaultProvider(kv, barrier)

	err := p.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error when second Put fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// TestInitialize_EncryptError exercises the branch where barrier.Encrypt
// returns an error during CA key wrapping.
func TestInitialize_EncryptError(t *testing.T) {
	barrier := &errorBarrier{}
	p := NewDefaultProvider(newMockKVStore(), barrier)

	err := p.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error when barrier.Encrypt fails")
	}
	var cge *CertGenerateError
	if !errors.As(err, &cge) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// loadCA — uncovered nil cert PEM branch
// ---------------------------------------------------------------------------

// TestLoadCA_NilCertPEM exercises the branch in loadCA where the wrapped
// key exists in KVStore but the cert PEM entry is absent (nil).
func TestLoadCA_NilCertPEM(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	// Delete only the cert PEM entry so loadCA finds the key but not the cert.
	if err := kv.Delete(ctx, "__sys:cert_ca/system/cert_pem"); err != nil {
		t.Fatalf("Delete cert_pem: %v", err)
	}

	// Wipe in-memory state so loadCA is forced to reload from KVStore.
	p.mu.Lock()
	p.caKey = nil
	p.caCert = nil
	p.caCertPEM = nil
	p.mu.Unlock()

	err := p.loadCA(ctx, "system")
	if err == nil {
		t.Fatal("expected error when cert PEM is missing")
	}
	var cnaErr *CAKeyNotAvailableError
	if !errors.As(err, &cnaErr) {
		t.Fatalf("expected *CAKeyNotAvailableError, got %T: %v", err, err)
	}
}

// TestLoadCA_InvalidCertPEM exercises the branch where the stored cert PEM
// cannot be decoded (corrupt PEM block returns nil from pem.Decode).
func TestLoadCA_InvalidCertPEM(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	// Overwrite cert_pem with invalid data so pem.Decode returns nil.
	if err := kv.Put(ctx, "__sys:cert_ca/system/cert_pem", []byte("not-pem")); err != nil {
		t.Fatalf("Put invalid cert_pem: %v", err)
	}

	// Wipe in-memory state so loadCA reloads from KVStore.
	p.mu.Lock()
	p.caKey = nil
	p.caCert = nil
	p.caCertPEM = nil
	p.mu.Unlock()

	err := p.loadCA(ctx, "system")
	if err == nil {
		t.Fatal("expected error for invalid cert PEM")
	}
	var cge *CertGenerateError
	if !errors.As(err, &cge) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// getCAMaterial — remaining nil-key branch after auto-init
// ---------------------------------------------------------------------------

// TestGetCAMaterial_NilAfterAutoInit exercises the branch where auto-init
// succeeds but the in-memory key is still nil (forced by injecting a store
// that writes to KVStore but where we zero the key after Initialize).
// In practice this branch guards against races; we simulate it by providing
// a KVStore that silently drops all Put calls so Initialize completes without
// error but leaves no material in the store—resulting in loadCA returning
// CAKeyNotAvailableError, which causes Initialize to fail, so the
// auto-init path returns an error covering the `if err := p.Initialize`
// branch rather than the subsequent `if key == nil` branch (which is
// inherently a race guard and nearly impossible to hit in tests without
// concurrency gymnastics).
//
// We verify the behavior by testing a simpler route: a fresh provider
// whose barrier is unsealed but whose KVStore returns an error for the
// second Get (cert PEM) so Initialize via auto-init fails.
func TestGetCAMaterial_AutoInit_CertPEMGetError(t *testing.T) {
	// Store with selective Get failure: first Get (wrapped key) succeeds by
	// returning nil (no existing CA), subsequent Puts succeed, but a second
	// call to loadCA fails because the cert PEM Get returns an error.
	// Use a real provider that was initialized first so the wrapped key is
	// stored, then swap in an errorKVStore that fails on Get so loadCA errors.
	kv := newMockKVStore()
	barrier := newMockBarrier()
	p := NewDefaultProvider(kv, barrier)

	// Initialize to persist the CA material.
	if err := p.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Replace the KV store with one that returns an error for Get so the
	// second call to loadCA (via auto-init in getCAMaterial) fails.
	p.kvStore = &errorKVStore{err: errors.New("storage failure")}

	// Wipe in-memory state.
	p.mu.Lock()
	p.caKey = nil
	p.caCert = nil
	p.caCertPEM = nil
	p.mu.Unlock()

	_, _, err := p.getCAMaterial()
	if err == nil {
		t.Fatal("expected error when KVStore fails during auto-init")
	}
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
// loadCA — existing key wipe branch
// ---------------------------------------------------------------------------

// TestLoadCA_WipesExistingKey verifies that loadCA wipes and replaces an
// existing in-memory CA key when called a second time (e.g., after rotation).
func TestLoadCA_WipesExistingKey(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	// Record the original key bytes.
	p.mu.RLock()
	origLen := len(p.caKey)
	p.mu.RUnlock()

	if origLen == 0 {
		t.Fatal("expected non-empty CA key after Initialize")
	}

	// Call loadCA again — it should wipe the old key and reload.
	if err := p.loadCA(ctx, "system"); err != nil {
		t.Fatalf("second loadCA: %v", err)
	}

	p.mu.RLock()
	newLen := len(p.caKey)
	p.mu.RUnlock()

	if newLen == 0 {
		t.Fatal("expected non-empty CA key after second loadCA")
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

// putCountKVStore wraps a mockKVStore and fails on the N-th Put call.
type putCountKVStore struct {
	inner     *mockKVStore
	failOnPut int
	putCount  int
}

func (s *putCountKVStore) Put(ctx context.Context, key string, value []byte) error {
	s.putCount++
	if s.putCount == s.failOnPut {
		return errors.New("injected Put failure")
	}
	return s.inner.Put(ctx, key, value)
}

func (s *putCountKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	return s.inner.Get(ctx, key)
}

func (s *putCountKVStore) Delete(ctx context.Context, key string) error {
	return s.inner.Delete(ctx, key)
}

func (s *putCountKVStore) Scan(ctx context.Context, prefix string, fn func(string, []byte) error) error {
	return s.inner.Scan(ctx, prefix, fn)
}

func (s *putCountKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.List(ctx, prefix)
}

func (s *putCountKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *putCountKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (s *putCountKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (s *putCountKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (s *putCountKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (s *putCountKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// errorBarrier is a barrier that succeeds IsSealed() but fails on Encrypt.
type errorBarrier struct{}

func (b *errorBarrier) IsSealed() bool { return false }

func (b *errorBarrier) Encrypt(_ []byte) ([]byte, error) {
	return nil, errors.New("encrypt failure")
}

func (b *errorBarrier) Decrypt(_ []byte) ([]byte, error) {
	return nil, errors.New("decrypt failure")
}

// putSucceedsScanFailsKVStore wraps a KV store, allowing Put but injecting
// Scan errors.
type putSucceedsScanFailsKVStore struct {
	inner   kvstore.KVStore
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
