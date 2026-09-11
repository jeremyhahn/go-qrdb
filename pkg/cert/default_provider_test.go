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
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/fips140"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

// mockKVStore is an in-memory KVStore for testing.
type mockKVStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMockKVStore() *mockKVStore {
	return &mockKVStore{data: make(map[string][]byte)}
}

func (m *mockKVStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := make([]byte, len(value))
	copy(stored, value)
	m.data[key] = stored
	return nil
}

func (m *mockKVStore) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, nil
}

func (m *mockKVStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *mockKVStore) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	m.mu.RLock()
	entries := make(map[string][]byte)
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			entries[k] = v
		}
	}
	m.mu.RUnlock()

	for k, v := range entries {
		if err := fn(k, v); err != nil {
			return err
		}
	}
	return nil
}

func (m *mockKVStore) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var keys []string
	for k := range m.data {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

func (m *mockKVStore) Exists(_ context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.data[key]
	return ok, nil
}

func (m *mockKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (m *mockKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (m *mockKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (m *mockKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (m *mockKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// mockBarrier implements seal.EncryptionBarrier using AES-GCM for real
// encrypt/decrypt round-trips in tests.
type mockBarrier struct {
	mu     sync.RWMutex
	sealed bool
	key    []byte
}

func newMockBarrier() *mockBarrier {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		panic("failed to generate test key: " + err.Error())
	}
	return &mockBarrier{
		sealed: false,
		key:    key,
	}
}

// mockBarrierNonceSize is the nonce NewGCMWithRandomNonce prepends; the AEAD
// reports NonceSize() == 0 because callers never supply one.
const mockBarrierNonceSize = 12

func (b *mockBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.sealed {
		return nil, &BarrierSealedError{}
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	// NewGCMWithRandomNonce is the FIPS-approved construction: it samples the
	// nonce itself and prepends it, which is the layout this double already used.
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	// #nosec G407 -- the nonce argument MUST be nil here: this AEAD is cipher.NewGCMWithRandomNonce, which samples a fresh 96-bit nonce inside the FIPS module on every Seal and prepends it to the ciphertext. gosec reads the nil as a hardcoded IV; passing anything else makes Seal panic.
	return gcm.Seal(nil, nil, plaintext, nil), nil
}

func (b *mockBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.sealed {
		return nil, &BarrierSealedError{}
	}
	block, err := aes.NewCipher(b.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < mockBarrierNonceSize {
		return nil, errors.New("ciphertext too short")
	}
	return gcm.Open(nil, nil, ciphertext, nil)
}

func (b *mockBarrier) IsSealed() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sealed
}

func (b *mockBarrier) seal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sealed = true
}

func (b *mockBarrier) unseal() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sealed = false
}

// errorKVStore is a KVStore that returns errors for all operations.
type errorKVStore struct {
	err error
}

func (e *errorKVStore) Put(_ context.Context, _ string, _ []byte) error {
	return e.err
}

func (e *errorKVStore) Get(_ context.Context, _ string) ([]byte, error) {
	return nil, e.err
}

func (e *errorKVStore) Delete(_ context.Context, _ string) error {
	return e.err
}

func (e *errorKVStore) Scan(_ context.Context, _ string, _ func(key string, value []byte) error) error {
	return e.err
}

func (e *errorKVStore) List(_ context.Context, _ string) ([]string, error) {
	return nil, e.err
}

func (e *errorKVStore) Exists(_ context.Context, _ string) (bool, error) {
	return false, e.err
}

func (e *errorKVStore) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error {
	return nil
}

func (e *errorKVStore) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}

func (e *errorKVStore) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

func (e *errorKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

func (e *errorKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// newTestProvider creates a DefaultCertProvider with mocks, ready for
// Initialize.
func newTestProvider(opts ...ProviderOption) (*DefaultCertProvider, *mockKVStore, *mockBarrier) {
	kv := newMockKVStore()
	barrier := newMockBarrier()
	return NewDefaultProvider(kv, barrier, opts...), kv, barrier
}

// initializeProvider creates and initializes a provider for tests.
func initializeProvider(t *testing.T, opts ...ProviderOption) (*DefaultCertProvider, *mockKVStore, *mockBarrier) {
	t.Helper()
	p, kv, barrier := newTestProvider(opts...)
	ctx := context.Background()
	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return p, kv, barrier
}

// ---------------------------------------------------------------------------
// Initialize tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_Initialize_NewCA(t *testing.T) {
	p, kv, _ := newTestProvider()
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Verify wrapped key was stored.
	wrappedKey, err := kv.Get(ctx, "__sys:cert_ca/system/wrapped_key")
	if err != nil {
		t.Fatalf("Get wrapped_key: %v", err)
	}
	if wrappedKey == nil {
		t.Fatal("wrapped_key should exist after Initialize")
	}

	// Verify CA cert PEM was stored.
	certPEM, err := kv.Get(ctx, "__sys:cert_ca/system/cert_pem")
	if err != nil {
		t.Fatalf("Get cert_pem: %v", err)
	}
	if certPEM == nil {
		t.Fatal("cert_pem should exist after Initialize")
	}

	// Verify CA cert PEM is valid.
	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("cert_pem should be valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if !cert.IsCA {
		t.Error("certificate should be a CA")
	}
	if cert.Subject.CommonName != "qrdb-ca" {
		t.Errorf("CN: got %q, want %q", cert.Subject.CommonName, "qrdb-ca")
	}
}

func TestDefaultCertProvider_Initialize_ExistingCA(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()

	// Initialize creates CA.
	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}

	// Capture state.
	firstCACertPEM := p.CACertPEM()

	// Re-initialize should load existing CA, not create new.
	p2, _, _ := newTestProvider()
	// Use the same KVStore so it sees the persisted CA.
	p2.kvStore = p.kvStore
	p2.barrier = p.barrier

	if err := p2.Initialize(ctx); err != nil {
		t.Fatalf("second Initialize: %v", err)
	}

	// Should have the same CA cert.
	if !bytes.Equal(p2.CACertPEM(), firstCACertPEM) {
		t.Error("second Initialize should load the same CA cert")
	}
}

func TestDefaultCertProvider_Initialize_BarrierSealed(t *testing.T) {
	p, _, barrier := newTestProvider()
	barrier.seal()

	err := p.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Initialize_TenantFromContext(t *testing.T) {
	p, kv, _ := newTestProvider()
	ctx := tenant.WithTenantID(context.Background(), "org-42")

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Verify wrapped key was stored under the correct tenant prefix.
	wrappedKey, err := kv.Get(ctx, "__sys:cert_ca/org-42/wrapped_key")
	if err != nil {
		t.Fatalf("Get wrapped_key: %v", err)
	}
	if wrappedKey == nil {
		t.Fatal("wrapped_key should exist under tenant org-42")
	}
}

func TestDefaultCertProvider_Initialize_StoreError(t *testing.T) {
	barrier := newMockBarrier()
	storeErr := errors.New("disk full")
	ekv := &errorKVStore{err: storeErr}
	p := NewDefaultProvider(ekv, barrier)

	err := p.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error when store fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Unseal / Seal tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_Unseal(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	// Seal the provider.
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Verify CA key is wiped.
	p.mu.RLock()
	keyNil := p.caKey == nil
	p.mu.RUnlock()
	if !keyNil {
		t.Error("CA key should be nil after Seal")
	}

	// Unseal should reload the CA key.
	if err := p.Unseal(ctx); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	// Verify CA key is loaded.
	p.mu.RLock()
	keyNil = p.caKey == nil
	p.mu.RUnlock()
	if keyNil {
		t.Error("CA key should be loaded after Unseal")
	}
}

func TestDefaultCertProvider_Unseal_BarrierSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	err := p.Unseal(context.Background())
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Seal_Idempotent(t *testing.T) {
	p, _, _ := initializeProvider(t)

	if err := p.Seal(); err != nil {
		t.Fatalf("first Seal: %v", err)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("second Seal: %v", err)
	}
}

func TestDefaultCertProvider_SealUnsealCycle(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	ctx := context.Background()

	// Generate a cert before sealing.
	req := &GenerateRequest{Username: "alice", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}
	if bundle.Serial == "" {
		t.Error("bundle should have a serial")
	}

	// Seal both provider and barrier.
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	barrier.seal()

	// Should not be able to generate certs while barrier is sealed.
	_, err = p.GenerateClientCert(ctx, req)
	if err == nil {
		t.Fatal("expected error when generating cert after Seal")
	}

	// Unseal barrier, then provider.
	barrier.unseal()
	if err := p.Unseal(ctx); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	// Should be able to generate certs again.
	bundle2, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert after Unseal: %v", err)
	}
	if bundle2.Serial == "" {
		t.Error("bundle should have a serial after Unseal")
	}
}

// ---------------------------------------------------------------------------
// GenerateClientCert tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_GenerateClientCert_Success(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{
		Username: "alice",
		TenantID: "system",
		TTL:      24 * time.Hour,
	}

	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	if bundle.Serial == "" {
		t.Error("bundle.Serial should not be empty")
	}
	if len(bundle.CertPEM) == 0 {
		t.Error("bundle.CertPEM should not be empty")
	}
	if len(bundle.KeyPEM) == 0 {
		t.Error("bundle.KeyPEM should not be empty")
	}

	// Verify the certificate can be parsed.
	block, _ := pem.Decode(bundle.CertPEM)
	if block == nil {
		t.Fatal("CertPEM should be valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	if cert.Subject.CommonName != "alice" {
		t.Errorf("CN: got %q, want %q", cert.Subject.CommonName, "alice")
	}
	if len(cert.Subject.OrganizationalUnit) == 0 || cert.Subject.OrganizationalUnit[0] != "system" {
		t.Errorf("OU: got %v, want [system]", cert.Subject.OrganizationalUnit)
	}

	// Verify key PEM is valid.
	keyBlock, _ := pem.Decode(bundle.KeyPEM)
	if keyBlock == nil {
		t.Fatal("KeyPEM should be valid PEM")
	}
	if keyBlock.Type != "EC PRIVATE KEY" {
		t.Errorf("key type: got %q, want %q", keyBlock.Type, "EC PRIVATE KEY")
	}
}

func TestDefaultCertProvider_GenerateClientCert_Fields(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{
		Username: "bob",
		TenantID: "tenant-x",
		TTL:      48 * time.Hour,
	}

	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	block, _ := pem.Decode(bundle.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	// ExtKeyUsage should only have ClientAuth.
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Errorf("ExtKeyUsage: got %v, want [ClientAuth]", cert.ExtKeyUsage)
	}

	// Verify it is NOT a CA.
	if cert.IsCA {
		t.Error("client cert should not be a CA")
	}

	// Verify TTL.
	expectedDuration := 48 * time.Hour
	actualDuration := cert.NotAfter.Sub(cert.NotBefore)
	if actualDuration != expectedDuration {
		t.Errorf("TTL: got %v, want %v", actualDuration, expectedDuration)
	}
}

func TestDefaultCertProvider_GenerateClientCert_DefaultTTL(t *testing.T) {
	customTTL := 720 * time.Hour
	p, _, _ := initializeProvider(t, WithDefaultTTL(customTTL))
	ctx := context.Background()

	req := &GenerateRequest{
		Username: "charlie",
		TenantID: "system",
		// TTL zero = use default.
	}

	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	block, _ := pem.Decode(bundle.CertPEM)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	actualDuration := cert.NotAfter.Sub(cert.NotBefore)
	if actualDuration != customTTL {
		t.Errorf("TTL: got %v, want %v", actualDuration, customTTL)
	}
}

func TestDefaultCertProvider_GenerateClientCert_WhenSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	req := &GenerateRequest{Username: "sealed-user", TenantID: "system"}
	_, err := p.GenerateClientCert(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_GenerateClientCert_NilRequest(t *testing.T) {
	p, _, _ := initializeProvider(t)

	_, err := p.GenerateClientCert(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_GenerateClientCert_EmptyUsername(t *testing.T) {
	p, _, _ := initializeProvider(t)

	req := &GenerateRequest{Username: "", TenantID: "system"}
	_, err := p.GenerateClientCert(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for empty username")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "username" {
		t.Errorf("Field: got %q, want %q", ve.Field, "username")
	}
}

func TestDefaultCertProvider_GenerateClientCert_EmptyTenantID(t *testing.T) {
	p, _, _ := initializeProvider(t)

	req := &GenerateRequest{Username: "alice", TenantID: ""}
	_, err := p.GenerateClientCert(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for empty tenantID")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "tenantID" {
		t.Errorf("Field: got %q, want %q", ve.Field, "tenantID")
	}
}

func TestDefaultCertProvider_GenerateClientCert_NegativeTTL(t *testing.T) {
	p, _, _ := initializeProvider(t)

	req := &GenerateRequest{Username: "alice", TenantID: "system", TTL: -1 * time.Hour}
	_, err := p.GenerateClientCert(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for negative TTL")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
	if ve.Field != "ttl" {
		t.Errorf("Field: got %q, want %q", ve.Field, "ttl")
	}
}

func TestDefaultCertProvider_GenerateClientCert_CAKeyNotAvailable(t *testing.T) {
	p, _, barrier := initializeProvider(t)

	// Seal both provider and barrier to clear the CA key and prevent
	// auto-initialization from reloading it.
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	barrier.seal()

	req := &GenerateRequest{Username: "alice", TenantID: "system"}
	_, err := p.GenerateClientCert(context.Background(), req)
	if err == nil {
		t.Fatal("expected error when CA key is not available")
	}
	var sealedErr *BarrierSealedError
	if !errors.As(err, &sealedErr) {
		t.Errorf("expected BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_GenerateClientCert_VerifyRecord(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{
		Username: "record-user",
		TenantID: "system",
		TTL:      12 * time.Hour,
	}

	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Verify the record was persisted.
	recordKey := "__sys:certs/system/" + bundle.Serial
	recordBytes, err := kv.Get(ctx, recordKey)
	if err != nil {
		t.Fatalf("Get record: %v", err)
	}
	if recordBytes == nil {
		t.Fatal("cert record should exist in KVStore")
	}
}

func TestDefaultCertProvider_GenerateClientCert_MultipleCerts(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	serials := make(map[string]bool)
	for i := 0; i < 5; i++ {
		req := &GenerateRequest{Username: "user", TenantID: "system"}
		bundle, err := p.GenerateClientCert(ctx, req)
		if err != nil {
			t.Fatalf("GenerateClientCert #%d: %v", i, err)
		}
		if serials[bundle.Serial] {
			t.Fatalf("duplicate serial: %s", bundle.Serial)
		}
		serials[bundle.Serial] = true
	}
}

// ---------------------------------------------------------------------------
// ExportPKCS12 tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_ExportPKCS12_Success(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "pkcs12-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	p12Data, err := p.ExportPKCS12(ctx, bundle.CertPEM, bundle.KeyPEM, "test-password")

	// PKCS#12 stamps a SHA-1 certificate fingerprint into its localKeyId
	// attribute, which crypto/sha1 refuses to compute under
	// GODEBUG=fips140=only. Both branches are real assertions: the FIPS CI job
	// proves the refusal is clean, the ordinary test job proves the export
	// still works.
	if fips140.Enforced() {
		var unavailable *PKCS12FIPSUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("expected *PKCS12FIPSUnavailableError, got %T: %v", err, err)
		}
		// The refusal keeps the error shape callers already match on.
		var exportErr *CertExportError
		if !errors.As(err, &exportErr) {
			t.Fatalf("expected *CertExportError, got %T: %v", err, err)
		}
		if p12Data != nil {
			t.Error("no PKCS12 data should be returned when the export is refused")
		}
		return
	}

	if err != nil {
		t.Fatalf("ExportPKCS12: %v", err)
	}
	if len(p12Data) == 0 {
		t.Error("PKCS12 data should not be empty")
	}
}

func TestDefaultCertProvider_ExportPKCS12_WhenSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	_, err := p.ExportPKCS12(context.Background(), []byte("cert"), []byte("key"), "pw")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_ExportPKCS12_InvalidCertPEM(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "bad-cert-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	_, err = p.ExportPKCS12(ctx, []byte("not-pem"), bundle.KeyPEM, "pw")
	if err == nil {
		t.Fatal("expected error for invalid cert PEM")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_ExportPKCS12_InvalidKeyPEM(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "bad-key-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	_, err = p.ExportPKCS12(ctx, bundle.CertPEM, []byte("not-pem"), "pw")
	if err == nil {
		t.Fatal("expected error for invalid key PEM")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// RevokeCert tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_RevokeCert(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "revoke-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	revoked, err := p.IsRevoked(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Error("cert should be revoked")
	}
}

func TestDefaultCertProvider_RevokeCert_EmptySerial(t *testing.T) {
	p, _, _ := initializeProvider(t)

	_, err := p.RevokeCert(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty serial")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_RevokeCert_WhenSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	_, err := p.RevokeCert(context.Background(), "some-serial")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_RevokeCert_Idempotent(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "idempotent-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("first RevokeCert: %v", err)
	}
	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("second RevokeCert: %v", err)
	}

	revoked, err := p.IsRevoked(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Error("cert should still be revoked")
	}
}

// ---------------------------------------------------------------------------
// IsRevoked tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_IsRevoked_NotRevoked(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "not-revoked", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	revoked, err := p.IsRevoked(ctx, bundle.Serial)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Error("newly generated cert should not be revoked")
	}
}

func TestDefaultCertProvider_IsRevoked_EmptySerial(t *testing.T) {
	p, _, _ := initializeProvider(t)

	_, err := p.IsRevoked(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty serial")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_IsRevoked_WhenSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	_, err := p.IsRevoked(context.Background(), "some-serial")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_IsRevoked_NonexistentSerial(t *testing.T) {
	p, _, _ := initializeProvider(t)

	revoked, err := p.IsRevoked(context.Background(), "does-not-exist")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Error("nonexistent serial should not be revoked")
	}
}

// ---------------------------------------------------------------------------
// ListCerts tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_ListCerts(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	// Generate multiple certs for the same tenant.
	for i := 0; i < 3; i++ {
		req := &GenerateRequest{Username: "list-user", TenantID: "list-tenant"}
		if _, err := p.GenerateClientCert(ctx, req); err != nil {
			t.Fatalf("GenerateClientCert #%d: %v", i, err)
		}
	}

	records, err := p.ListCerts(ctx, "list-tenant")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("expected 3 records, got %d", len(records))
	}
	for _, rec := range records {
		if rec.TenantID != "list-tenant" {
			t.Errorf("TenantID: got %q, want %q", rec.TenantID, "list-tenant")
		}
		if rec.Username != "list-user" {
			t.Errorf("Username: got %q, want %q", rec.Username, "list-user")
		}
	}
}

func TestDefaultCertProvider_ListCerts_EmptyTenant(t *testing.T) {
	p, _, _ := initializeProvider(t)

	_, err := p.ListCerts(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty tenantID")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_ListCerts_NoRecords(t *testing.T) {
	p, _, _ := initializeProvider(t)

	records, err := p.ListCerts(context.Background(), "no-such-tenant")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	if len(records) != 0 {
		t.Errorf("expected 0 records, got %d", len(records))
	}
}

func TestDefaultCertProvider_ListCerts_WhenSealed(t *testing.T) {
	p, _, barrier := initializeProvider(t)
	barrier.seal()

	_, err := p.ListCerts(context.Background(), "system")
	if err == nil {
		t.Fatal("expected error when barrier is sealed")
	}
	var bsErr *BarrierSealedError
	if !errors.As(err, &bsErr) {
		t.Fatalf("expected *BarrierSealedError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_ListCerts_CrossTenantIsolation(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	// Certs for tenant-a.
	for i := 0; i < 2; i++ {
		req := &GenerateRequest{Username: "user-a", TenantID: "tenant-a"}
		if _, err := p.GenerateClientCert(ctx, req); err != nil {
			t.Fatalf("GenerateClientCert tenant-a #%d: %v", i, err)
		}
	}

	// Certs for tenant-b.
	for i := 0; i < 3; i++ {
		req := &GenerateRequest{Username: "user-b", TenantID: "tenant-b"}
		if _, err := p.GenerateClientCert(ctx, req); err != nil {
			t.Fatalf("GenerateClientCert tenant-b #%d: %v", i, err)
		}
	}

	recordsA, err := p.ListCerts(ctx, "tenant-a")
	if err != nil {
		t.Fatalf("ListCerts tenant-a: %v", err)
	}
	if len(recordsA) != 2 {
		t.Errorf("tenant-a: expected 2 records, got %d", len(recordsA))
	}

	recordsB, err := p.ListCerts(ctx, "tenant-b")
	if err != nil {
		t.Fatalf("ListCerts tenant-b: %v", err)
	}
	if len(recordsB) != 3 {
		t.Errorf("tenant-b: expected 3 records, got %d", len(recordsB))
	}
}

// ---------------------------------------------------------------------------
// CACertPEM tests
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_CACertPEM(t *testing.T) {
	p, _, _ := initializeProvider(t)

	certPEM := p.CACertPEM()
	if len(certPEM) == 0 {
		t.Fatal("CACertPEM should not be empty after Initialize")
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("CACertPEM should be valid PEM")
	}
	if block.Type != "CERTIFICATE" {
		t.Errorf("PEM type: got %q, want %q", block.Type, "CERTIFICATE")
	}
}

func TestDefaultCertProvider_CACertPEM_ReturnsCopy(t *testing.T) {
	p, _, _ := initializeProvider(t)

	pem1 := p.CACertPEM()
	pem2 := p.CACertPEM()

	if !bytes.Equal(pem1, pem2) {
		t.Error("CACertPEM should return equal values")
	}

	// Mutating one should not affect the other.
	pem1[0] = 0xFF
	pem3 := p.CACertPEM()
	if pem3[0] == 0xFF {
		t.Error("CACertPEM should return independent copies")
	}
}

func TestDefaultCertProvider_CACertPEM_BeforeInitialize(t *testing.T) {
	p, _, _ := newTestProvider()
	certPEM := p.CACertPEM()
	if len(certPEM) != 0 {
		t.Error("CACertPEM should be empty before Initialize")
	}
}

// ---------------------------------------------------------------------------
// WithDefaultTTL option test
// ---------------------------------------------------------------------------

func TestWithDefaultTTL(t *testing.T) {
	customTTL := 30 * 24 * time.Hour
	p, _, _ := newTestProvider(WithDefaultTTL(customTTL))
	if p.defaultTTL != customTTL {
		t.Errorf("defaultTTL: got %v, want %v", p.defaultTTL, customTTL)
	}
}

func TestDefaultTTL_WhenNotSet(t *testing.T) {
	p, _, _ := newTestProvider()
	if p.defaultTTL != defaultTTL {
		t.Errorf("defaultTTL: got %v, want %v", p.defaultTTL, defaultTTL)
	}
}

// ---------------------------------------------------------------------------
// Validation helper tests
// ---------------------------------------------------------------------------

func TestValidateGenerateRequest_Valid(t *testing.T) {
	req := &GenerateRequest{Username: "alice", TenantID: "system"}
	if err := validateGenerateRequest(req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateGenerateRequest_Nil(t *testing.T) {
	err := validateGenerateRequest(nil)
	if err == nil {
		t.Fatal("expected error for nil request")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
}

// ---------------------------------------------------------------------------
// hasSerialSuffix helper tests
// ---------------------------------------------------------------------------

func TestHasSerialSuffix_Match(t *testing.T) {
	if !hasSerialSuffix("__sys:certs/system/abc123", "abc123") {
		t.Error("should match when key ends with /serial")
	}
}

func TestHasSerialSuffix_NoMatch(t *testing.T) {
	if hasSerialSuffix("__sys:certs/system/abc123", "xyz789") {
		t.Error("should not match when serial differs")
	}
}

func TestHasSerialSuffix_KeyTooShort(t *testing.T) {
	if hasSerialSuffix("a", "abc123") {
		t.Error("should not match when key is shorter than suffix")
	}
}

// ---------------------------------------------------------------------------
// Compile-time interface check
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_ImplementsCertProvider(t *testing.T) {
	var _ CertProvider = (*DefaultCertProvider)(nil)
}

// ---------------------------------------------------------------------------
// Unseal without existing CA
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_Unseal_NoCA(t *testing.T) {
	p, _, _ := newTestProvider()
	ctx := context.Background()

	err := p.Unseal(ctx)
	if err == nil {
		t.Fatal("expected error when no CA exists")
	}
	var caErr *CAKeyNotAvailableError
	if !errors.As(err, &caErr) {
		t.Fatalf("expected *CAKeyNotAvailableError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// RevokeCert updates record
// ---------------------------------------------------------------------------

func TestDefaultCertProvider_RevokeCert_UpdatesRecord(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "revoke-record-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	if _, err := p.RevokeCert(ctx, bundle.Serial); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}

	// List certs and verify the record is marked as revoked.
	records, err := p.ListCerts(ctx, "system")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}

	found := false
	for _, rec := range records {
		if rec.Serial == bundle.Serial {
			found = true
			if !rec.Revoked {
				t.Error("cert record should be marked as revoked")
			}
		}
	}
	if !found {
		t.Error("revoked cert not found in ListCerts")
	}
}

// ---------------------------------------------------------------------------
// Store error paths
// ---------------------------------------------------------------------------

// partialErrorKVStore wraps a mockKVStore but fails specific operations.
type partialErrorKVStore struct {
	*mockKVStore
	failPutN int // fail the Nth Put call (1-based)
	putCount int
	failScan bool
	failGet  bool
	getCount int
	failGetN int // fail the Nth Get call (1-based)
}

func (p *partialErrorKVStore) Put(ctx context.Context, key string, value []byte) error {
	p.putCount++
	if p.failPutN > 0 && p.putCount == p.failPutN {
		return errors.New("injected put error")
	}
	return p.mockKVStore.Put(ctx, key, value)
}

func (p *partialErrorKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if p.failScan {
		return errors.New("injected scan error")
	}
	return p.mockKVStore.Scan(ctx, prefix, fn)
}

func (p *partialErrorKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	p.getCount++
	if p.failGet {
		return nil, errors.New("injected get error")
	}
	if p.failGetN > 0 && p.getCount == p.failGetN {
		return nil, errors.New("injected get error")
	}
	return p.mockKVStore.Get(ctx, key)
}

func (p *partialErrorKVStore) Exists(ctx context.Context, key string) (bool, error) {
	return p.mockKVStore.Exists(ctx, key)
}

func TestDefaultCertProvider_Initialize_PutWrappedKeyError(t *testing.T) {
	barrier := newMockBarrier()
	pkv := &partialErrorKVStore{
		mockKVStore: newMockKVStore(),
		failPutN:    1, // Fail the first Put (wrapped key).
	}
	p := NewDefaultProvider(pkv, barrier)

	err := p.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error when Put fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Initialize_PutCertPEMError(t *testing.T) {
	barrier := newMockBarrier()
	pkv := &partialErrorKVStore{
		mockKVStore: newMockKVStore(),
		failPutN:    2, // Fail the second Put (cert PEM).
	}
	p := NewDefaultProvider(pkv, barrier)

	err := p.Initialize(context.Background())
	if err == nil {
		t.Fatal("expected error when Put cert PEM fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_RevokeCert_ScanError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	pkv := &partialErrorKVStore{
		mockKVStore: inner,
		failScan:    true,
	}
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	req := &GenerateRequest{Username: "scan-err-user", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Replace the kvStore with the error-injecting one.
	p.kvStore = pkv

	_, err = p.RevokeCert(ctx, bundle.Serial)
	if err == nil {
		t.Fatal("expected error when scan fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_RevokeCert_CorruptRecord(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	// Inject a corrupt cert record.
	_ = kv.Put(ctx, "__sys:certs/system/bad-serial", []byte("not-json{{{"))

	// Revoke should still succeed (corrupt records are skipped).
	if _, err := p.RevokeCert(ctx, "bad-serial"); err != nil {
		t.Fatalf("RevokeCert: %v", err)
	}
}

func TestDefaultCertProvider_IsRevoked_StoreError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Replace with error store for the IsRevoked call.
	p.kvStore = &errorKVStore{err: errors.New("disk read failed")}

	_, err := p.IsRevoked(ctx, "some-serial")
	if err == nil {
		t.Fatal("expected error when Get fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_ListCerts_ScanError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	p.kvStore = &errorKVStore{err: errors.New("scan failure")}

	_, err := p.ListCerts(ctx, "system")
	if err == nil {
		t.Fatal("expected error when Scan fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_ListCerts_CorruptRecord(t *testing.T) {
	p, kv, _ := initializeProvider(t)
	ctx := context.Background()

	// Inject a corrupt cert record alongside a valid one.
	_ = kv.Put(ctx, "__sys:certs/corrupt-tenant/bad", []byte("not-json"))

	records, err := p.ListCerts(ctx, "corrupt-tenant")
	if err != nil {
		t.Fatalf("ListCerts: %v", err)
	}
	// Corrupt records are skipped.
	if len(records) != 0 {
		t.Errorf("expected 0 valid records, got %d", len(records))
	}
}

func TestDefaultCertProvider_Unseal_LoadCA_CertPEMGetError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Remove the cert PEM but leave the wrapped key.
	_ = inner.Delete(ctx, "__sys:cert_ca/system/cert_pem")

	err := p.Unseal(ctx)
	if err == nil {
		t.Fatal("expected error when cert PEM is missing")
	}
	var caErr *CAKeyNotAvailableError
	if !errors.As(err, &caErr) {
		t.Fatalf("expected *CAKeyNotAvailableError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Unseal_LoadCA_DecryptError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Corrupt the wrapped key.
	_ = inner.Put(ctx, "__sys:cert_ca/system/wrapped_key", []byte("corrupt-data"))

	err := p.Unseal(ctx)
	if err == nil {
		t.Fatal("expected error when decrypt fails")
	}
	var genErr *CertGenerateError
	if !errors.As(err, &genErr) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Unseal_LoadCA_InvalidPEM(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Replace cert PEM with invalid PEM.
	_ = inner.Put(ctx, "__sys:cert_ca/system/cert_pem", []byte("not-pem-data"))

	err := p.Unseal(ctx)
	if err == nil {
		t.Fatal("expected error for invalid PEM")
	}
	var genErr *CertGenerateError
	if !errors.As(err, &genErr) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Unseal_LoadCA_InvalidCert(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Replace cert PEM with valid PEM structure but invalid certificate data.
	invalidPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("not-a-real-certificate"),
	})
	_ = inner.Put(ctx, "__sys:cert_ca/system/cert_pem", invalidPEM)

	err := p.Unseal(ctx)
	if err == nil {
		t.Fatal("expected error for invalid certificate")
	}
	var genErr *CertGenerateError
	if !errors.As(err, &genErr) {
		t.Fatalf("expected *CertGenerateError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_GenerateClientCert_RecordPutError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Replace with error store after Initialize.
	p.kvStore = &errorKVStore{err: errors.New("put failed")}

	req := &GenerateRequest{Username: "store-err-user", TenantID: "system"}
	_, err := p.GenerateClientCert(ctx, req)
	if err == nil {
		t.Fatal("expected error when record Put fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_RevokeCert_PutRevokedKeyError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Replace with error store for the revoke Put.
	p.kvStore = &errorKVStore{err: errors.New("put revoked failed")}

	_, err := p.RevokeCert(ctx, "some-serial")
	if err == nil {
		t.Fatal("expected error when Put revoked key fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Unseal_LoadCA_GetWrappedKeyError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Replace with error store for the Unseal call.
	p.kvStore = &errorKVStore{err: errors.New("get wrapped failed")}

	err := p.Unseal(ctx)
	if err == nil {
		t.Fatal("expected error when Get fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

func TestDefaultCertProvider_Unseal_LoadCA_GetCertPEMError(t *testing.T) {
	barrier := newMockBarrier()
	inner := newMockKVStore()
	p := NewDefaultProvider(inner, barrier)
	ctx := context.Background()

	if err := p.Initialize(ctx); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := p.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Replace with a KVStore that fails on the second Get (cert PEM).
	pkv := &partialErrorKVStore{
		mockKVStore: inner,
		failGetN:    2, // First Get (wrapped key) succeeds, second (cert PEM) fails.
	}
	p.kvStore = pkv

	err := p.Unseal(ctx)
	if err == nil {
		t.Fatal("expected error when cert PEM Get fails")
	}
	var se *StoreError
	if !errors.As(err, &se) {
		t.Fatalf("expected *StoreError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// PKCS12 with invalid CA cert PEM
// ---------------------------------------------------------------------------

func TestEncodePKCS12_InvalidCACertPEM(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "pkcs12-ca-err", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Inject invalid CA PEM.
	invalidCAPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("not-a-real-cert"),
	})
	p.mu.Lock()
	p.caCertPEM = invalidCAPEM
	p.mu.Unlock()

	_, err = p.ExportPKCS12(ctx, bundle.CertPEM, bundle.KeyPEM, "pw")
	if err == nil {
		t.Fatal("expected error for invalid CA cert PEM")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

func TestEncodePKCS12_NoCACertPEM(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "pkcs12-no-ca", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Clear the CA cert PEM.
	p.mu.Lock()
	p.caCertPEM = nil
	p.mu.Unlock()

	// Should still work without CA chain -- except under
	// GODEBUG=fips140=only, where the export is refused for every input
	// because of the format's SHA-1 localKeyId attribute.
	p12Data, err := p.ExportPKCS12(ctx, bundle.CertPEM, bundle.KeyPEM, "pw")
	if fips140.Enforced() {
		var unavailable *PKCS12FIPSUnavailableError
		if !errors.As(err, &unavailable) {
			t.Fatalf("expected *PKCS12FIPSUnavailableError, got %T: %v", err, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("ExportPKCS12: %v", err)
	}
	if len(p12Data) == 0 {
		t.Error("PKCS12 data should not be empty")
	}
}

// ---------------------------------------------------------------------------
// encodePKCS12 direct tests
// ---------------------------------------------------------------------------

func TestEncodePKCS12_InvalidKeyPEMDirect(t *testing.T) {
	_, err := encodePKCS12([]byte("cert"), []byte("bad-key"), nil, "pw")
	if err == nil {
		t.Fatal("expected error for invalid key PEM")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

func TestEncodePKCS12_InvalidCertPEMDirect(t *testing.T) {
	// Valid key PEM but invalid cert PEM.
	_, err := encodePKCS12([]byte("bad-cert"), []byte("bad-key"), nil, "pw")
	if err == nil {
		t.Fatal("expected error for invalid PEM data")
	}
}

func TestEncodePKCS12_ValidKeyBadCertDirect(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "direct-test", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Valid key PEM, invalid cert PEM.
	_, err = encodePKCS12([]byte("not-pem"), bundle.KeyPEM, nil, "pw")
	if err == nil {
		t.Fatal("expected error for invalid cert PEM")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

func TestEncodePKCS12_ValidKeyInvalidCertDER(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "invalid-der", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Valid PEM structure but invalid DER content.
	badCertPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: []byte("bad-der"),
	})

	_, err = encodePKCS12(badCertPEM, bundle.KeyPEM, nil, "pw")
	if err == nil {
		t.Fatal("expected error for invalid cert DER")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}

func TestEncodePKCS12_ValidKeyInvalidKeyDER(t *testing.T) {
	p, _, _ := initializeProvider(t)
	ctx := context.Background()

	req := &GenerateRequest{Username: "invalid-key-der", TenantID: "system"}
	bundle, err := p.GenerateClientCert(ctx, req)
	if err != nil {
		t.Fatalf("GenerateClientCert: %v", err)
	}

	// Valid PEM structure but invalid EC key DER content.
	badKeyPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: []byte("bad-key-der"),
	})

	_, err = encodePKCS12(bundle.CertPEM, badKeyPEM, nil, "pw")
	if err == nil {
		t.Fatal("expected error for invalid key DER")
	}
	var exportErr *CertExportError
	if !errors.As(err, &exportErr) {
		t.Fatalf("expected *CertExportError, got %T: %v", err, err)
	}
}
