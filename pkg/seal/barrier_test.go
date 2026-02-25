// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0

package seal

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
)

// mockAuditLogger captures audit events for test assertions.
type mockAuditLogger struct {
	mu     sync.Mutex
	events []*AuditEvent
}

func (m *mockAuditLogger) Log(_ context.Context, event *AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return nil
}

func (m *mockAuditLogger) Events() []*AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*AuditEvent, len(m.events))
	copy(out, m.events)
	return out
}

// mockHardwareEncryptor implements SymmetricEncrypter but is NOT a
// *softwareBarrierEncryptor, so GetMasterKey returns ErrHardwareEncryptorRequired.
type mockHardwareEncryptor struct{}

func (m *mockHardwareEncryptor) Encrypt(plaintext []byte) ([]byte, error) {
	return plaintext, nil
}

func (m *mockHardwareEncryptor) Decrypt(ciphertext []byte) ([]byte, error) {
	return ciphertext, nil
}

// mockHardwareStrategy is a strategy that returns a mockHardwareEncryptor.
type mockHardwareStrategy struct{}

func (s *mockHardwareStrategy) ID() StrategyID       { return "mock-hardware" }
func (s *mockHardwareStrategy) Available() bool      { return true }
func (s *mockHardwareStrategy) HardwareBacked() bool { return true }

func (s *mockHardwareStrategy) SealRootKey(ctx context.Context, rootKey []byte, creds Credentials) (*SealedRootKey, error) {
	sw := NewSoftwareStrategy()
	return sw.SealRootKey(ctx, rootKey, creds)
}

func (s *mockHardwareStrategy) UnsealRootKey(ctx context.Context, sealed *SealedRootKey, creds Credentials) ([]byte, error) {
	sw := NewSoftwareStrategy()
	return sw.UnsealRootKey(ctx, sealed, creds)
}

func (s *mockHardwareStrategy) BarrierEncryptor(_ context.Context, _ []byte) (SymmetricEncrypter, error) {
	return &mockHardwareEncryptor{}, nil
}

// mockUnavailableStrategy reports Available() = false.
type mockUnavailableStrategy struct{}

func (s *mockUnavailableStrategy) ID() StrategyID       { return "unavailable" }
func (s *mockUnavailableStrategy) Available() bool      { return false }
func (s *mockUnavailableStrategy) HardwareBacked() bool { return false }

func (s *mockUnavailableStrategy) SealRootKey(_ context.Context, _ []byte, _ Credentials) (*SealedRootKey, error) {
	return nil, ErrNoAvailableStrategy
}

func (s *mockUnavailableStrategy) UnsealRootKey(_ context.Context, _ *SealedRootKey, _ Credentials) ([]byte, error) {
	return nil, ErrNoAvailableStrategy
}

func (s *mockUnavailableStrategy) BarrierEncryptor(_ context.Context, _ []byte) (SymmetricEncrypter, error) {
	return nil, ErrNoAvailableStrategy
}

// mockFailingSealStrategy seals successfully but returns an error from
// BarrierEncryptor. This covers the error path at barrier.go line 142-145.
type mockFailingSealStrategy struct{}

func (s *mockFailingSealStrategy) ID() StrategyID       { return "failing-seal" }
func (s *mockFailingSealStrategy) Available() bool      { return true }
func (s *mockFailingSealStrategy) HardwareBacked() bool { return false }

func (s *mockFailingSealStrategy) SealRootKey(ctx context.Context, rootKey []byte, creds Credentials) (*SealedRootKey, error) {
	sw := NewSoftwareStrategy()
	return sw.SealRootKey(ctx, rootKey, creds)
}

func (s *mockFailingSealStrategy) UnsealRootKey(ctx context.Context, sealed *SealedRootKey, creds Credentials) ([]byte, error) {
	sw := NewSoftwareStrategy()
	return sw.UnsealRootKey(ctx, sealed, creds)
}

func (s *mockFailingSealStrategy) BarrierEncryptor(_ context.Context, _ []byte) (SymmetricEncrypter, error) {
	return nil, errors.New("mock: encryptor unavailable")
}

// mockFailingEncryptor always returns errors from Encrypt/Decrypt.
type mockFailingEncryptor struct{}

func (m *mockFailingEncryptor) Encrypt(_ []byte) ([]byte, error) {
	return nil, errors.New("mock: encrypt failure")
}

func (m *mockFailingEncryptor) Decrypt(_ []byte) ([]byte, error) {
	return nil, errors.New("mock: decrypt failure")
}

// failingPutBackend wraps a real backend but returns an error from Put
// for a specific key. This allows testing the error path at
// barrier.go line 136 where base.Put fails during Initialize.
type failingPutBackend struct {
	inner    StorageBackend
	failKey  string
	failErr  error
	putCalls int
}

func (f *failingPutBackend) Get(key string) ([]byte, error)    { return f.inner.Get(key) }
func (f *failingPutBackend) Delete(key string) error           { return f.inner.Delete(key) }
func (f *failingPutBackend) List(prefix string) ([]string, error) { return f.inner.List(prefix) }
func (f *failingPutBackend) Exists(key string) (bool, error)   { return f.inner.Exists(key) }
func (f *failingPutBackend) Close() error                      { return f.inner.Close() }

func (f *failingPutBackend) Put(key string, data []byte) error {
	f.putCalls++
	if key == f.failKey {
		return f.failErr
	}
	return f.inner.Put(key, data)
}

// fakeShamirStrategy is registered under StrategyShamir but is NOT a
// *ShamirStrategy, which triggers the second type assertion failure
// in Barrier.ShamirStrategy().
type fakeShamirStrategy struct{}

func (s *fakeShamirStrategy) ID() StrategyID       { return StrategyShamir }
func (s *fakeShamirStrategy) Available() bool      { return true }
func (s *fakeShamirStrategy) HardwareBacked() bool { return false }

func (s *fakeShamirStrategy) SealRootKey(ctx context.Context, rootKey []byte, creds Credentials) (*SealedRootKey, error) {
	sw := NewSoftwareStrategy()
	return sw.SealRootKey(ctx, rootKey, creds)
}

func (s *fakeShamirStrategy) UnsealRootKey(ctx context.Context, sealed *SealedRootKey, creds Credentials) ([]byte, error) {
	sw := NewSoftwareStrategy()
	return sw.UnsealRootKey(ctx, sealed, creds)
}

func (s *fakeShamirStrategy) BarrierEncryptor(ctx context.Context, rootKey []byte) (SymmetricEncrypter, error) {
	return newSoftwareBarrierEncryptor(rootKey)
}

// newBarrierWithAudit creates a barrier backed by MemoryBackend and
// SoftwareStrategy with an audit logger for barrier_test.go tests.
func newBarrierWithAudit(t *testing.T) (*Barrier, *mockAuditLogger) {
	t.Helper()
	audit := &mockAuditLogger{}
	cfg := BarrierConfig{
		RootKeyPath: "test/root-key",
		AuditLogger: audit,
	}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	return b, audit
}

// initBarrier initializes the barrier and returns the credentials used.
func initBarrier(t *testing.T, b *Barrier) Credentials {
	t.Helper()
	ctx := context.Background()
	creds := Credentials{Secret: "test-password-123"}
	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	return creds
}


// ─── NewBarrier ──────────────────────────────────────────────────────

func TestNewBarrier_Success(t *testing.T) {
	cfg := BarrierConfig{RootKeyPath: "root-key"}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if b == nil {
		t.Fatal("expected non-nil barrier")
	}
	if !b.IsSealed() {
		t.Error("expected barrier to start sealed")
	}
}

func TestNewBarrier_NoStrategies(t *testing.T) {
	cfg := BarrierConfig{RootKeyPath: "root-key"}
	_, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg)
	if err != ErrNoAvailableStrategy {
		t.Fatalf("expected ErrNoAvailableStrategy, got %v", err)
	}
}

func TestNewBarrier_DefaultPreferenceOrder(t *testing.T) {
	cfg := BarrierConfig{RootKeyPath: "root-key"}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(b.prefOrder) != len(DefaultPreferenceOrder) {
		t.Errorf("expected default preference order length %d, got %d",
			len(DefaultPreferenceOrder), len(b.prefOrder))
	}
}

func TestNewBarrier_CustomPreferenceOrder(t *testing.T) {
	customOrder := []StrategyID{StrategySoftware, StrategyTPM2}
	cfg := BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: customOrder,
	}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(b.prefOrder) != len(customOrder) {
		t.Errorf("expected preference order length %d, got %d",
			len(customOrder), len(b.prefOrder))
	}
	if b.prefOrder[0] != StrategySoftware {
		t.Errorf("expected first preference to be %s, got %s",
			StrategySoftware, b.prefOrder[0])
	}
}

// ─── Initialize ─────────────────────────────────────────────────────

func TestBarrier_Initialize_Success(t *testing.T) {
	b, audit := newBarrierWithAudit(t)
	ctx := context.Background()
	creds := Credentials{Secret: "strong-password"}

	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if b.IsSealed() {
		t.Error("expected barrier to be unsealed after Initialize")
	}
	if b.ActiveStrategy() != StrategySoftware {
		t.Errorf("expected active strategy %s, got %s",
			StrategySoftware, b.ActiveStrategy())
	}

	// Verify audit event.
	events := audit.Events()
	found := false
	for _, e := range events {
		if e.Action == "initialize" && e.Outcome == "allow" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected audit event for successful initialize")
	}
}

func TestBarrier_Initialize_AlreadyInitialized(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	ctx := context.Background()
	creds := Credentials{Secret: "test-password"}

	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("first Initialize: %v", err)
	}

	err := b.Initialize(ctx, creds)
	if err != ErrAlreadyInitialized {
		t.Fatalf("expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestBarrier_Initialize_NoAvailableStrategy(t *testing.T) {
	audit := &mockAuditLogger{}
	cfg := BarrierConfig{
		RootKeyPath: "root-key",
		AuditLogger: audit,
	}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, &mockUnavailableStrategy{})
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	err = b.Initialize(context.Background(), Credentials{Secret: "pw"})
	if err != ErrNoAvailableStrategy {
		t.Fatalf("expected ErrNoAvailableStrategy, got %v", err)
	}

	// Verify deny audit event.
	events := audit.Events()
	found := false
	for _, e := range events {
		if e.Action == "initialize" && e.Outcome == "deny" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected deny audit event for failed initialize")
	}
}

func TestBarrier_Initialize_BasePutFails(t *testing.T) {
	putErr := errors.New("disk full")
	backend := &failingPutBackend{
		inner:   NewMemoryBackend(),
		failKey: "test/root-key",
		failErr: putErr,
	}
	cfg := BarrierConfig{RootKeyPath: "test/root-key"}
	b, err := NewBarrier(testLogger(), backend, cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	err = b.Initialize(context.Background(), Credentials{Secret: "pw"})
	if err == nil {
		t.Fatal("expected error from base.Put failure")
	}
	if !errors.Is(err, putErr) {
		t.Fatalf("expected %v, got %v", putErr, err)
	}
	// Barrier should remain sealed after failed initialization.
	if !b.IsSealed() {
		t.Error("expected barrier to remain sealed after failed init")
	}
}

func TestBarrier_Initialize_BarrierEncryptorFails(t *testing.T) {
	cfg := BarrierConfig{
		RootKeyPath:     "test/root-key",
		PreferenceOrder: []StrategyID{"failing-seal"},
	}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, &mockFailingSealStrategy{})
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	err = b.Initialize(context.Background(), Credentials{Secret: "pw"})
	if err == nil {
		t.Fatal("expected error from BarrierEncryptor failure")
	}
	// Barrier should remain sealed.
	if !b.IsSealed() {
		t.Error("expected barrier to remain sealed")
	}
}

// ─── Unseal ─────────────────────────────────────────────────────────

func TestBarrier_Unseal_Success(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	ctx := context.Background()
	creds := Credentials{Secret: "unseal-password"}

	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Seal first, then unseal.
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !b.IsSealed() {
		t.Error("expected barrier to be sealed")
	}

	if err := b.Unseal(ctx, creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}
	if b.IsSealed() {
		t.Error("expected barrier to be unsealed")
	}
}

func TestBarrier_Unseal_AlreadyUnsealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	ctx := context.Background()
	creds := initBarrier(t, b)

	err := b.Unseal(ctx, creds)
	if err != ErrAlreadyUnsealed {
		t.Fatalf("expected ErrAlreadyUnsealed, got %v", err)
	}
}

func TestBarrier_Unseal_NotInitialized(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	err := b.Unseal(context.Background(), Credentials{Secret: "pw"})
	if err != ErrNotInitialized {
		t.Fatalf("expected ErrNotInitialized, got %v", err)
	}
}

func TestBarrier_Unseal_InvalidCredentials(t *testing.T) {
	b, audit := newBarrierWithAudit(t)
	ctx := context.Background()
	creds := Credentials{Secret: "correct-password"}

	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	wrongCreds := Credentials{Secret: "wrong-password"}
	err := b.Unseal(ctx, wrongCreds)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}

	// Verify deny audit event.
	events := audit.Events()
	found := false
	for _, e := range events {
		if e.Action == "unseal" && e.Outcome == "deny" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected deny audit event for invalid credentials")
	}
}

func TestBarrier_Unseal_CorruptRootKey(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	ctx := context.Background()

	// Write corrupt data directly to storage.
	if err := b.base.Put(b.config.RootKeyPath, []byte("not-valid-json")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := b.Unseal(ctx, Credentials{Secret: "pw"})
	if err != ErrCorruptRootKey {
		t.Fatalf("expected ErrCorruptRootKey, got %v", err)
	}
}

func TestBarrier_Unseal_StrategyNotFound(t *testing.T) {
	// Initialize with software strategy, then create a new barrier without it.
	backend := NewMemoryBackend()
	audit := &mockAuditLogger{}
	cfg := BarrierConfig{
		RootKeyPath: "root-key",
		AuditLogger: audit,
	}

	b1, err := NewBarrier(testLogger(), backend, cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	creds := Credentials{Secret: "password"}
	if err := b1.Initialize(context.Background(), creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	// Create new barrier with ONLY an unavailable/different strategy.
	// The sealed blob references StrategySoftware but this barrier only has "unavailable".
	b2, err := NewBarrier(testLogger(), backend, cfg, &mockUnavailableStrategy{})
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	err = b2.Unseal(context.Background(), creds)
	if err != ErrStrategyNotFound {
		t.Fatalf("expected ErrStrategyNotFound, got %v", err)
	}

	// Verify deny audit event.
	events := audit.Events()
	found := false
	for _, e := range events {
		if e.Action == "unseal" && e.Outcome == "deny" {
			if e.Details != nil && e.Details["error"] == ErrStrategyNotFound.Error() {
				found = true
				break
			}
		}
	}
	if !found {
		t.Error("expected deny audit event for strategy not found")
	}
}

func TestBarrier_Unseal_BarrierEncryptorFails(t *testing.T) {
	// Initialize with a normal strategy, then swap the strategy map entry to
	// one whose BarrierEncryptor fails on unseal.
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "test/root-key",
		PreferenceOrder: []StrategyID{StrategySoftware},
	}
	b, err := NewBarrier(testLogger(), backend, cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	creds := Credentials{Secret: "pw"}
	if err := b.Initialize(context.Background(), creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Replace the strategy with one that fails on BarrierEncryptor.
	b.strategies[StrategySoftware] = &mockFailingSealStrategy{}

	err = b.Unseal(context.Background(), creds)
	if err == nil {
		t.Fatal("expected error from BarrierEncryptor failure during unseal")
	}
	if b.state.Load() != 0 {
		t.Error("barrier should remain sealed when BarrierEncryptor fails")
	}
}

// ─── Seal ───────────────────────────────────────────────────────────

func TestBarrier_Seal_Success(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	if b.IsSealed() {
		t.Fatal("expected unsealed after Initialize")
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !b.IsSealed() {
		t.Error("expected barrier to be sealed")
	}

	// Operations should fail.
	_, err := b.Get("any-key")
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed from Get, got %v", err)
	}
}

func TestBarrier_Seal_Idempotent(t *testing.T) {
	b, _ := newBarrierWithAudit(t)

	// Barrier starts sealed, calling Seal again should be fine.
	if err := b.Seal(); err != nil {
		t.Fatalf("first Seal: %v", err)
	}
	if err := b.Seal(); err != nil {
		t.Fatalf("second Seal: %v", err)
	}
}

func TestBarrier_Seal_ZeroesDEK(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	// Get the DEK before seal.
	dek, err := b.GetMasterKey()
	if err != nil {
		t.Fatalf("GetMasterKey: %v", err)
	}
	if len(dek) != aesKeySize {
		t.Fatalf("expected DEK length %d, got %d", aesKeySize, len(dek))
	}

	// Grab a reference to the internal encryptor to verify zeroing.
	raw := b.encryptor.Load()
	sw, ok := raw.(*softwareBarrierEncryptor)
	if !ok {
		t.Fatal("expected softwareBarrierEncryptor")
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	// Verify the internal DEK is zeroed.
	allZero := true
	for _, v := range sw.dek {
		if v != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Error("expected DEK to be zeroed after Seal")
	}
}

func TestBarrier_Seal_WithHardwareEncryptor(t *testing.T) {
	// Exercise the Seal path where the encryptor is not a
	// *softwareBarrierEncryptor (hardware mock), ensuring it
	// does not panic and still transitions to sealed.
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []StrategyID{"mock-hardware"},
	}
	b, err := NewBarrier(testLogger(), backend, cfg, &mockHardwareStrategy{})
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), Credentials{Secret: "pw"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if !b.IsSealed() {
		t.Error("expected sealed after Seal")
	}
}

// ─── Get/Put/Delete/List/Exists ─────────────────────────────────────

func TestBarrier_PutGet_RoundTrip(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	key := "secrets/my-secret"
	value := []byte("super-secret-data")

	if err := b.Put(key, value); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := b.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("expected %q, got %q", value, got)
	}
}

func TestBarrier_Put_EncryptsData(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	key := "secrets/my-secret"
	value := []byte("plaintext-data-that-must-be-encrypted")

	if err := b.Put(key, value); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Read raw bytes from the underlying backend.
	raw, err := b.base.Get(key)
	if err != nil {
		t.Fatalf("base.Get: %v", err)
	}

	// Raw should differ from plaintext.
	if bytes.Equal(raw, value) {
		t.Error("expected raw backend data to differ from plaintext")
	}
	// Raw should have version byte prefix.
	if raw[0] != barrierVersion {
		t.Errorf("expected version byte %d, got %d", barrierVersion, raw[0])
	}
}

func TestBarrier_Get_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	_, err := b.Get("any")
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

func TestBarrier_Put_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	err := b.Put("any", []byte("data"))
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

func TestBarrier_Delete_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	err := b.Delete("any")
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

func TestBarrier_List_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	_, err := b.List("")
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

func TestBarrier_Exists_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	_, err := b.Exists("any")
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

func TestBarrier_Get_CorruptData(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	// Write corrupt data directly to backend (too short for barrier format).
	key := "corrupt-key"
	if err := b.base.Put(key, []byte{0x01}); err != nil {
		t.Fatalf("base.Put: %v", err)
	}

	_, err := b.Get(key)
	if err == nil {
		t.Fatal("expected error for corrupt data")
	}
}

func TestBarrier_Get_TamperedCiphertext(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	key := "secrets/tampered"
	if err := b.Put(key, []byte("original-data")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Tamper with raw ciphertext.
	raw, err := b.base.Get(key)
	if err != nil {
		t.Fatalf("base.Get: %v", err)
	}
	// Flip a byte in the ciphertext portion.
	if len(raw) > 5 {
		raw[len(raw)-1] ^= 0xFF
	}
	if err := b.base.Put(key, raw); err != nil {
		t.Fatalf("base.Put: %v", err)
	}

	_, err = b.Get(key)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials for tampered data, got %v", err)
	}
}

func TestBarrier_Get_KeyNotFound(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	_, err := b.Get("nonexistent-key")
	if err != ErrSecretNotFound {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestBarrier_Delete_Success(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	key := "secrets/to-delete"
	if err := b.Put(key, []byte("data")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := b.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := b.Get(key)
	if err != ErrSecretNotFound {
		t.Fatalf("expected ErrSecretNotFound after delete, got %v", err)
	}
}

func TestBarrier_List_WithPrefix(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	keys := []string{
		"secrets/alpha",
		"secrets/beta",
		"other/gamma",
	}
	for _, k := range keys {
		if err := b.Put(k, []byte("data")); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}

	list, err := b.List("secrets/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 keys with prefix 'secrets/', got %d", len(list))
	}
}

func TestBarrier_Exists_Success(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	key := "secrets/check"
	if err := b.Put(key, []byte("data")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err := b.Exists(key)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected key to exist")
	}

	missing, err := b.Exists("nonexistent")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if missing {
		t.Error("expected key to not exist")
	}
}

// ─── GetMasterKey ───────────────────────────────────────────────────

func TestBarrier_GetMasterKey_Success(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	dek, err := b.GetMasterKey()
	if err != nil {
		t.Fatalf("GetMasterKey: %v", err)
	}
	if len(dek) != aesKeySize {
		t.Fatalf("expected DEK length %d, got %d", aesKeySize, len(dek))
	}

	// Verify it returns a copy.
	dek2, err := b.GetMasterKey()
	if err != nil {
		t.Fatalf("GetMasterKey second call: %v", err)
	}
	if !bytes.Equal(dek, dek2) {
		t.Error("expected equal DEKs from consecutive calls")
	}

	// Modify the returned copy and verify internal state is unaffected.
	dek[0] ^= 0xFF
	dek3, err := b.GetMasterKey()
	if err != nil {
		t.Fatalf("GetMasterKey third call: %v", err)
	}
	if bytes.Equal(dek, dek3) {
		t.Error("modifying returned DEK should not affect internal state")
	}
}

func TestBarrier_GetMasterKey_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	_, err := b.GetMasterKey()
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

func TestBarrier_GetMasterKey_HardwareEncryptor(t *testing.T) {
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []StrategyID{"mock-hardware"},
	}
	hw := &mockHardwareStrategy{}
	b, err := NewBarrier(testLogger(), backend, cfg, hw)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	if err := b.Initialize(context.Background(), Credentials{Secret: "pw"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	_, err = b.GetMasterKey()
	if err != ErrHardwareEncryptorRequired {
		t.Fatalf("expected ErrHardwareEncryptorRequired, got %v", err)
	}
}

// ─── Status ─────────────────────────────────────────────────────────

func TestBarrier_Status_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	status := b.Status()
	if !status.Sealed {
		t.Error("expected sealed status")
	}
	if status.Strategy != "" {
		t.Errorf("expected empty strategy when not initialized, got %s", status.Strategy)
	}
}

func TestBarrier_Status_Unsealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	status := b.Status()
	if status.Sealed {
		t.Error("expected unsealed status")
	}
	if status.Strategy != StrategySoftware {
		t.Errorf("expected strategy %s, got %s", StrategySoftware, status.Strategy)
	}
	if status.HardwareBacked {
		t.Error("expected hardware_backed = false for software strategy")
	}
}

func TestBarrier_Status_HardwareBacked(t *testing.T) {
	backend := NewMemoryBackend()
	cfg := BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []StrategyID{"mock-hardware"},
	}
	b, err := NewBarrier(testLogger(), backend, cfg, &mockHardwareStrategy{})
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	if err := b.Initialize(context.Background(), Credentials{Secret: "pw"}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}

	status := b.Status()
	if status.Sealed {
		t.Error("expected unsealed")
	}
	if !status.HardwareBacked {
		t.Error("expected hardware_backed = true for mock hardware strategy")
	}
}

func TestBarrier_ActiveStrategy_Empty(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	if id := b.ActiveStrategy(); id != "" {
		t.Errorf("expected empty strategy, got %s", id)
	}
}

func TestBarrier_ActiveStrategy_AfterInit(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)
	if id := b.ActiveStrategy(); id != StrategySoftware {
		t.Errorf("expected %s, got %s", StrategySoftware, id)
	}
}

// ─── Close ──────────────────────────────────────────────────────────

func TestBarrier_Close_SealsAndCloses(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	if b.IsSealed() {
		t.Fatal("expected unsealed before Close")
	}

	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !b.IsSealed() {
		t.Error("expected sealed after Close")
	}
}

func TestBarrier_Close_AlreadySealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	// Barrier starts sealed.
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// ─── Encryptor ──────────────────────────────────────────────────────

func TestBarrier_Encryptor_Success(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)

	enc, err := b.Encryptor()
	if err != nil {
		t.Fatalf("Encryptor: %v", err)
	}
	if enc == nil {
		t.Fatal("expected non-nil encryptor")
	}
}

func TestBarrier_Encryptor_Sealed(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	_, err := b.Encryptor()
	if err != ErrSealed {
		t.Fatalf("expected ErrSealed, got %v", err)
	}
}

// ─── bestStrategy ───────────────────────────────────────────────────

func TestBarrier_BestStrategy_AllUnavailable(t *testing.T) {
	cfg := BarrierConfig{RootKeyPath: "root-key"}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, &mockUnavailableStrategy{})
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	_, bsErr := b.bestStrategy()
	if bsErr != ErrNoAvailableStrategy {
		t.Fatalf("expected ErrNoAvailableStrategy, got %v", bsErr)
	}
}

// ─── barrierEncrypt/barrierDecrypt ──────────────────────────────────

func TestBarrierEncryptDecrypt_RoundTrip(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	for i := range rootKey {
		rootKey[i] = byte(i)
	}
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	plaintext := []byte("round-trip test data")
	ciphertext, err := barrierEncrypt(enc, plaintext)
	if err != nil {
		t.Fatalf("barrierEncrypt: %v", err)
	}

	decrypted, err := barrierDecrypt(enc, ciphertext)
	if err != nil {
		t.Fatalf("barrierDecrypt: %v", err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("expected %q, got %q", plaintext, decrypted)
	}
}

func TestBarrierDecrypt_TooShort(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	_, err = barrierDecrypt(enc, []byte{0x01})
	if err != ErrCorruptRootKey {
		t.Fatalf("expected ErrCorruptRootKey for short data, got %v", err)
	}
}

func TestBarrierDecrypt_WrongVersion(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	// Wrong version byte.
	_, err = barrierDecrypt(enc, []byte{0xFF, 0x00})
	if err != ErrCorruptRootKey {
		t.Fatalf("expected ErrCorruptRootKey for wrong version, got %v", err)
	}
}

func TestBarrierDecrypt_InvalidCiphertext(t *testing.T) {
	rootKey := make([]byte, aesKeySize)
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	// Valid version byte but garbage ciphertext.
	garbage := make([]byte, 50)
	garbage[0] = barrierVersion
	for i := 1; i < len(garbage); i++ {
		garbage[i] = byte(i)
	}

	_, err = barrierDecrypt(enc, garbage)
	if err != ErrInvalidCredentials {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestBarrierEncrypt_EncryptorError(t *testing.T) {
	enc := &mockFailingEncryptor{}
	_, err := barrierEncrypt(enc, []byte("data"))
	if err == nil {
		t.Fatal("expected error from failing encryptor")
	}
}

func TestBarrierDecrypt_NilPlaintextNormalized(t *testing.T) {
	// Test the nil -> empty slice normalization at barrier.go line 469-471.
	// This path is exercised when gcm.Open returns nil plaintext for empty input.
	rootKey := make([]byte, aesKeySize)
	enc, err := newSoftwareBarrierEncryptor(rootKey)
	if err != nil {
		t.Fatalf("newSoftwareBarrierEncryptor: %v", err)
	}

	// Encrypt empty plaintext and verify round-trip returns empty (not nil).
	ciphertext, err := barrierEncrypt(enc, []byte{})
	if err != nil {
		t.Fatalf("barrierEncrypt: %v", err)
	}

	decrypted, err := barrierDecrypt(enc, ciphertext)
	if err != nil {
		t.Fatalf("barrierDecrypt: %v", err)
	}
	if decrypted == nil {
		t.Fatal("expected non-nil result for empty plaintext")
	}
	if len(decrypted) != 0 {
		t.Errorf("expected empty slice, got %v", decrypted)
	}
}

// ─── SetShamirConfig ────────────────────────────────────────────────

func TestBarrier_SetShamirConfig(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	cfg := &ShamirConfig{Threshold: 3, TotalShares: 5}
	b.SetShamirConfig(cfg)
	if b.config.Shamir == nil {
		t.Fatal("expected Shamir config to be set")
	}
	if b.config.Shamir.Threshold != 3 {
		t.Errorf("expected threshold 3, got %d", b.config.Shamir.Threshold)
	}
}

// ─── ShamirStrategy accessor ────────────────────────────────────────

func TestBarrier_ShamirStrategy_NotRegistered(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	if s := b.ShamirStrategy(); s != nil {
		t.Error("expected nil ShamirStrategy when not registered")
	}
}

func TestBarrier_ShamirStrategy_WrongType(t *testing.T) {
	// Register a strategy under StrategyShamir ID but it is not a *ShamirStrategy.
	// This exercises the second type assertion branch at barrier.go line 256.
	cfg := BarrierConfig{
		RootKeyPath:     "root-key",
		PreferenceOrder: []StrategyID{StrategyShamir},
	}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, &fakeShamirStrategy{})
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}
	if s := b.ShamirStrategy(); s != nil {
		t.Error("expected nil when strategy is not *ShamirStrategy")
	}
}

func TestBarrier_ShamirStrategy_Registered(t *testing.T) {
	shamirStore := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(shamirStore, 3, 5)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	cfg := BarrierConfig{
		RootKeyPath: "root-key",
		Shamir:      &ShamirConfig{Threshold: 3, TotalShares: 5},
	}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, NewSoftwareStrategy(), shamirStrat)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	s := b.ShamirStrategy()
	if s == nil {
		t.Fatal("expected non-nil ShamirStrategy")
	}
	if s.Threshold() != 3 {
		t.Errorf("expected threshold 3, got %d", s.Threshold())
	}
}

// ─── unsealDirect ───────────────────────────────────────────────────

func TestBarrier_UnsealDirect_Success(t *testing.T) {
	// Create barrier with ShamirStrategy and initialize via Shamir.
	backend := NewMemoryBackend()
	shamirStore := NewMemoryBackend()
	shamirStrat, err := NewShamirStrategy(shamirStore, 2, 3)
	if err != nil {
		t.Fatalf("NewShamirStrategy: %v", err)
	}
	cfg := BarrierConfig{
		RootKeyPath: "test/root-key",
		Shamir:      &ShamirConfig{Threshold: 2, TotalShares: 3},
	}
	b, err := NewBarrier(testLogger(), backend, cfg, NewSoftwareStrategy(), shamirStrat)
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	result, err := b.InitializeShamir(context.Background(), Credentials{})
	if err != nil {
		t.Fatalf("InitializeShamir: %v", err)
	}

	// Seal, then unseal with shares to trigger unsealDirect.
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	err = b.UnsealWithShares(context.Background(), result.Shares[:2])
	if err != nil {
		t.Fatalf("UnsealWithShares: %v", err)
	}
	if b.IsSealed() {
		t.Error("expected unsealed after UnsealWithShares")
	}
	if b.ActiveStrategy() != StrategyShamir {
		t.Errorf("expected active strategy %s, got %s", StrategyShamir, b.ActiveStrategy())
	}
}

func TestBarrier_UnsealDirect_NoShamirStrategy(t *testing.T) {
	// unsealDirect should return ErrShamirNotConfigured when no Shamir strategy.
	b, _ := newBarrierWithAudit(t)
	initBarrier(t, b)
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	rootKey := make([]byte, aesKeySize)
	err := b.unsealDirect(rootKey)
	if !errors.Is(err, ErrShamirNotConfigured) {
		t.Fatalf("expected ErrShamirNotConfigured, got %v", err)
	}
}

func TestBarrier_UnsealDirect_EncryptorError(t *testing.T) {
	// Register a strategy under StrategyShamir that fails on BarrierEncryptor.
	failStrat := &mockFailingSealStrategy{}
	cfg := BarrierConfig{
		RootKeyPath:     "test/root-key",
		PreferenceOrder: []StrategyID{StrategySoftware},
	}
	b, err := NewBarrier(testLogger(), NewMemoryBackend(), cfg, NewSoftwareStrategy())
	if err != nil {
		t.Fatalf("NewBarrier: %v", err)
	}

	// Manually register the failing strategy under StrategyShamir.
	b.strategies[StrategyShamir] = failStrat

	rootKey := make([]byte, aesKeySize)
	err = b.unsealDirect(rootKey)
	if err == nil {
		t.Fatal("expected error from BarrierEncryptor failure")
	}
}

// ─── Unseal then data round-trip ────────────────────────────────────

func TestBarrier_UnsealThenDataRoundTrip(t *testing.T) {
	b, _ := newBarrierWithAudit(t)
	ctx := context.Background()
	creds := Credentials{Secret: "round-trip-pw"}

	// Initialize and store data.
	if err := b.Initialize(ctx, creds); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if err := b.Put("key1", []byte("value1")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Seal, then unseal.
	if err := b.Seal(); err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := b.Unseal(ctx, creds); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	// Data should still be retrievable.
	got, err := b.Get("key1")
	if err != nil {
		t.Fatalf("Get after unseal: %v", err)
	}
	if !bytes.Equal(got, []byte("value1")) {
		t.Errorf("expected %q, got %q", "value1", got)
	}
}

// ─── MemoryBackend ──────────────────────────────────────────────────

func TestMemoryBackend_GetNotFound(t *testing.T) {
	m := NewMemoryBackend()
	_, err := m.Get("nonexistent")
	if err != ErrSecretNotFound {
		t.Fatalf("expected ErrSecretNotFound, got %v", err)
	}
}

func TestMemoryBackend_PutGet_RoundTrip(t *testing.T) {
	m := NewMemoryBackend()
	key := "test/key"
	value := []byte("test-value")

	if err := m.Put(key, value); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := m.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, value) {
		t.Errorf("expected %q, got %q", value, got)
	}

	// Verify data isolation (modifying returned value doesn't affect storage).
	got[0] = 0xFF
	got2, err := m.Get(key)
	if err != nil {
		t.Fatalf("Get second: %v", err)
	}
	if bytes.Equal(got, got2) {
		t.Error("expected stored data to be isolated from returned slice")
	}
}

func TestMemoryBackend_Delete(t *testing.T) {
	m := NewMemoryBackend()
	key := "test/del"

	if err := m.Put(key, []byte("data")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := m.Delete(key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err := m.Get(key)
	if err != ErrSecretNotFound {
		t.Fatalf("expected ErrSecretNotFound after delete, got %v", err)
	}
}

func TestMemoryBackend_Delete_NonExistent(t *testing.T) {
	m := NewMemoryBackend()
	// Deleting a key that doesn't exist should not error.
	if err := m.Delete("nonexistent"); err != nil {
		t.Fatalf("Delete nonexistent: %v", err)
	}
}

func TestMemoryBackend_List_WithPrefix(t *testing.T) {
	m := NewMemoryBackend()

	entries := map[string][]byte{
		"a/one":   []byte("1"),
		"a/two":   []byte("2"),
		"b/three": []byte("3"),
		"a/four":  []byte("4"),
	}
	for k, v := range entries {
		if err := m.Put(k, v); err != nil {
			t.Fatalf("Put(%s): %v", k, err)
		}
	}

	list, err := m.List("a/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 keys with prefix 'a/', got %d: %v", len(list), list)
	}

	// Results should be sorted.
	for i := 1; i < len(list); i++ {
		if list[i] < list[i-1] {
			t.Errorf("expected sorted results, got %v", list)
			break
		}
	}
}

func TestMemoryBackend_List_EmptyPrefix(t *testing.T) {
	m := NewMemoryBackend()
	if err := m.Put("key1", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := m.Put("key2", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	list, err := m.List("")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(list))
	}
}

func TestMemoryBackend_List_NoMatches(t *testing.T) {
	m := NewMemoryBackend()
	if err := m.Put("key", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	list, err := m.List("nonexistent/")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(list))
	}
}

func TestMemoryBackend_Exists(t *testing.T) {
	m := NewMemoryBackend()
	key := "test/exists"

	exists, err := m.Exists(key)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("expected key to not exist")
	}

	if err := m.Put(key, []byte("data")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	exists, err = m.Exists(key)
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !exists {
		t.Error("expected key to exist")
	}
}

func TestMemoryBackend_Close(t *testing.T) {
	m := NewMemoryBackend()
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestMemoryBackend_PutIsolatesInput(t *testing.T) {
	m := NewMemoryBackend()
	key := "isolation"
	value := []byte("original")

	if err := m.Put(key, value); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Modify the original slice.
	value[0] = 0xFF

	got, err := m.Get(key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got[0] == 0xFF {
		t.Error("expected stored data to be isolated from input slice")
	}
}
