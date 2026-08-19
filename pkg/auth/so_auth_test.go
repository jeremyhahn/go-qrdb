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

package auth_test

import (
	"context"
	"crypto/fips140"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/auth"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// In-memory KV store
// ---------------------------------------------------------------------------

type soMemKV struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newSOMemKV() *soMemKV { return &soMemKV{data: make(map[string][]byte)} }

func (m *soMemKV) Put(_ context.Context, k string, v []byte) error {
	m.mu.Lock()
	m.data[k] = append([]byte(nil), v...)
	m.mu.Unlock()
	return nil
}
func (m *soMemKV) Get(_ context.Context, k string) ([]byte, error) {
	m.mu.RLock()
	v := m.data[k]
	m.mu.RUnlock()
	return v, nil
}
func (m *soMemKV) Delete(_ context.Context, k string) error {
	m.mu.Lock()
	delete(m.data, k)
	m.mu.Unlock()
	return nil
}
func (m *soMemKV) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			if err := fn(k, v); err != nil {
				return err
			}
		}
	}
	return nil
}
func (m *soMemKV) List(_ context.Context, prefix string) ([]string, error) {
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
func (m *soMemKV) Exists(_ context.Context, k string) (bool, error) {
	m.mu.RLock()
	_, ok := m.data[k]
	m.mu.RUnlock()
	return ok, nil
}
func (m *soMemKV) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (m *soMemKV) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (m *soMemKV) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *soMemKV) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (m *soMemKV) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// No-op audit logger
// ---------------------------------------------------------------------------

type soNoopAudit struct{}

func (n *soNoopAudit) Emit(_ context.Context, _ audit.AuditEvent) {}
func (n *soNoopAudit) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (n *soNoopAudit) Close() error { return nil }

// recordingAudit captures emitted audit events for assertions.
type recordingAudit struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (r *recordingAudit) Emit(_ context.Context, e audit.AuditEvent) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}
func (r *recordingAudit) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (r *recordingAudit) Close() error { return nil }

// outcomes returns the recorded outcomes for events with the given action.
func (r *recordingAudit) outcomes(action string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, e := range r.events {
		if e.Action == action {
			out = append(out, e.Outcome)
		}
	}
	return out
}

// failingPutKV wraps soMemKV and fails Put once armed, to exercise store-failure
// paths. All other operations delegate to the embedded in-memory store.
type failingPutKV struct {
	*soMemKV
	failPut bool
}

func (f *failingPutKV) Put(ctx context.Context, k string, v []byte) error {
	if f.failPut {
		return errors.New("simulated store put failure")
	}
	return f.soMemKV.Put(ctx, k, v)
}

func hasOutcome(outcomes []string, want string) bool {
	for _, o := range outcomes {
		if o == want {
			return true
		}
	}
	return false
}

// TestRotateSOPIN_StoreFailureEmitsAudit proves a failed commit during rotation
// emits a :store failure audit event (regression: the rotate failure paths
// previously emitted nothing, unlike SetSOPIN).
func TestRotateSOPIN_StoreFailureEmitsAudit(t *testing.T) {
	ctx := context.Background()
	store := &failingPutKV{soMemKV: newSOMemKV()}
	rec := &recordingAudit{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := auth.NewSOAuthenticator(store, rec, logger)
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}

	if err := a.SetSOPIN(ctx, "tenant-1", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}

	store.failPut = true
	if err := a.RotateSOPIN(ctx, "tenant-1", []byte("correct-horse-battery-staple"), []byte("new-pin-value-abcdef")); err == nil {
		t.Fatal("RotateSOPIN should fail when the store Put fails")
	}

	if !hasOutcome(rec.outcomes("rotate"), audit.OutcomeFailure+":store") {
		t.Fatalf("expected a rotate :store failure audit event, got outcomes %v", rec.outcomes("rotate"))
	}
}

// TestRotateSOPIN_SuccessEmitsAudit proves the success path still emits a
// success audit event after the failure-path additions.
func TestRotateSOPIN_SuccessEmitsAudit(t *testing.T) {
	ctx := context.Background()
	store := newSOMemKV()
	rec := &recordingAudit{}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := auth.NewSOAuthenticator(store, rec, logger)
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}

	if err := a.SetSOPIN(ctx, "tenant-1", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	if err := a.RotateSOPIN(ctx, "tenant-1", []byte("correct-horse-battery-staple"), []byte("new-pin-value-abcdef")); err != nil {
		t.Fatalf("RotateSOPIN: %v", err)
	}
	if !hasOutcome(rec.outcomes("rotate"), audit.OutcomeSuccess) {
		t.Fatalf("expected a rotate success audit event, got outcomes %v", rec.outcomes("rotate"))
	}
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newSOAuth(t *testing.T) *auth.SOAuthenticator {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := auth.NewSOAuthenticator(newSOMemKV(), &soNoopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	return a
}

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

func TestNewSOAuthenticator_NilStore(t *testing.T) {
	logger := slog.Default()
	_, err := auth.NewSOAuthenticator(nil, &soNoopAudit{}, logger)
	if err == nil {
		t.Fatal("expected error for nil store")
	}
}

func TestNewSOAuthenticator_NilAudit(t *testing.T) {
	logger := slog.Default()
	_, err := auth.NewSOAuthenticator(newSOMemKV(), nil, logger)
	if err == nil {
		t.Fatal("expected error for nil audit logger")
	}
}

func TestNewSOAuthenticator_NilLogger(t *testing.T) {
	_, err := auth.NewSOAuthenticator(newSOMemKV(), &soNoopAudit{}, nil)
	if err == nil {
		t.Fatal("expected error for nil logger")
	}
}

// ---------------------------------------------------------------------------
// SetSOPIN
// ---------------------------------------------------------------------------

func TestSetSOPIN_Success(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	if err := a.SetSOPIN(ctx, "tenant-1", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
}

func TestSetSOPIN_EmptyTenant(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	err := a.SetSOPIN(ctx, "", []byte("pin"))
	if !errors.Is(err, auth.ErrSOPINEmptyTenant) {
		t.Fatalf("expected ErrSOPINEmptyTenant, got %v", err)
	}
}

func TestSetSOPIN_EmptyPIN(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	err := a.SetSOPIN(ctx, "tenant-1", []byte{})
	if !errors.Is(err, auth.ErrSOPINEmptyPIN) {
		t.Fatalf("expected ErrSOPINEmptyPIN, got %v", err)
	}
}

func TestSetSOPIN_AlreadySet(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	if err := a.SetSOPIN(ctx, "tenant-1", []byte("first-pin")); err != nil {
		t.Fatalf("first SetSOPIN: %v", err)
	}
	err := a.SetSOPIN(ctx, "tenant-1", []byte("second-pin"))
	if !errors.Is(err, auth.ErrSOPINAlreadySet) {
		t.Fatalf("expected ErrSOPINAlreadySet, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// VerifySOPIN
// ---------------------------------------------------------------------------

func TestVerifySOPIN_CorrectPIN(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	pin := []byte("correct-horse-battery-staple")
	if err := a.SetSOPIN(ctx, "tenant-1", pin); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	if err := a.VerifySOPIN(ctx, "tenant-1", []byte("correct-horse-battery-staple")); err != nil {
		t.Fatalf("VerifySOPIN: %v", err)
	}
}

func TestVerifySOPIN_WrongPIN(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	if err := a.SetSOPIN(ctx, "tenant-1", []byte("correct-pin")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	err := a.VerifySOPIN(ctx, "tenant-1", []byte("wrong-pin"))
	if !errors.Is(err, auth.ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid, got %v", err)
	}
}

func TestVerifySOPIN_NotSet(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	err := a.VerifySOPIN(ctx, "no-such-tenant", []byte("any-pin"))
	if !errors.Is(err, auth.ErrSOPINNotSet) {
		t.Fatalf("expected ErrSOPINNotSet, got %v", err)
	}
}

func TestVerifySOPIN_EmptyTenant(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	err := a.VerifySOPIN(ctx, "", []byte("pin"))
	if !errors.Is(err, auth.ErrSOPINEmptyTenant) {
		t.Fatalf("expected ErrSOPINEmptyTenant, got %v", err)
	}
}

func TestVerifySOPIN_EmptyPIN(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	err := a.VerifySOPIN(ctx, "tenant-1", nil)
	if !errors.Is(err, auth.ErrSOPINEmptyPIN) {
		t.Fatalf("expected ErrSOPINEmptyPIN, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// RotateSOPIN
// ---------------------------------------------------------------------------

func TestRotateSOPIN_Success(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	oldPIN := []byte("old-pin-value")
	newPIN := []byte("new-pin-value")

	if err := a.SetSOPIN(ctx, "tenant-1", oldPIN); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	if err := a.RotateSOPIN(ctx, "tenant-1", []byte("old-pin-value"), newPIN); err != nil {
		t.Fatalf("RotateSOPIN: %v", err)
	}
	// Old PIN should no longer work.
	if err := a.VerifySOPIN(ctx, "tenant-1", []byte("old-pin-value")); err == nil {
		t.Fatal("old PIN should be rejected after rotation")
	}
	// New PIN should work.
	if err := a.VerifySOPIN(ctx, "tenant-1", []byte("new-pin-value")); err != nil {
		t.Fatalf("new PIN verification: %v", err)
	}
}

func TestRotateSOPIN_WrongOldPIN(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	if err := a.SetSOPIN(ctx, "tenant-1", []byte("correct-old")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	err := a.RotateSOPIN(ctx, "tenant-1", []byte("wrong-old"), []byte("new-pin"))
	if !errors.Is(err, auth.ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// FIPS vs non-FIPS KDF selection
// ---------------------------------------------------------------------------

func TestVerifySOPIN_ArgonHashPrefix(t *testing.T) {
	// GOFIPS140=off is an explicit request for non-FIPS operation, so the
	// record must be Argon2id ('A'). A binary built with the FIPS 140-3
	// module active is in FIPS mode whatever the environment says, so the
	// expectation folds that in rather than skipping the case.
	t.Setenv("GOFIPS140", "off")
	want := byte('A')
	if fips140.Enabled() {
		want = 'P'
	}

	ctx := context.Background()
	store := newSOMemKV()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	a, _ := auth.NewSOAuthenticator(store, &soNoopAudit{}, logger)

	if err := a.SetSOPIN(ctx, "t1", []byte("pin-argon")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}

	// Inspect stored record; the KDF tag must match the detected mode.
	record, _ := store.Get(ctx, "__sys:so-pin-commit/t1")
	if len(record) == 0 || record[0] != want {
		t.Errorf("expected record with KDF tag %q, got: %q", want, record)
	}
}

func TestVerifySOPIN_PBKDF2HashPrefix(t *testing.T) {
	// With GOFIPS140 requesting FIPS, the record starts with 'P' (PBKDF2-SHA256).
	t.Setenv("GOFIPS140", "v1.0.0")

	ctx := context.Background()
	store := newSOMemKV()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	a, _ := auth.NewSOAuthenticator(store, &soNoopAudit{}, logger)

	if err := a.SetSOPIN(ctx, "t1", []byte("pin-pbkdf2")); err != nil {
		t.Fatalf("SetSOPIN FIPS: %v", err)
	}

	record, _ := store.Get(ctx, "__sys:so-pin-commit/t1")
	if len(record) == 0 || record[0] != 'P' {
		t.Errorf("expected PBKDF2 record starting with 'P', got: %q", record)
	}

	// Verify still works.
	if err := a.VerifySOPIN(ctx, "t1", []byte("pin-pbkdf2")); err != nil {
		t.Fatalf("VerifySOPIN FIPS: %v", err)
	}
}

// TestVerifySOPIN_ConstantTimeViaDistinctTiming ensures constant-time path
// is exercised (wrong PIN must not short-circuit before hmac compare).
// This is a behavioral smoke test; timing is not measured.
func TestVerifySOPIN_ConstantTimeSmoke(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	if err := a.SetSOPIN(ctx, "t2", []byte("my-secret-pin")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	// Incorrect PIN of same length.
	err := a.VerifySOPIN(ctx, "t2", []byte("my-secret-pim"))
	if err == nil {
		t.Fatal("expected verification failure")
	}
}

// TestVerifySOPIN_BufferContract verifies that VerifySOPIN accepts pin bytes
// passed from a caller that manages their own buffer (ensuring the secure.Buffer
// wrap inside VerifySOPIN does not interfere with caller ownership).
func TestVerifySOPIN_BufferContract(t *testing.T) {
	ctx := context.Background()
	a := newSOAuth(t)
	pin := []byte("caller-managed-buffer-pin")
	if err := a.SetSOPIN(ctx, "t3", append([]byte(nil), pin...)); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}
	// The presented bytes are owned by the caller and should survive.
	if err := a.VerifySOPIN(ctx, "t3", pin); err != nil {
		t.Fatalf("VerifySOPIN: %v", err)
	}
}
