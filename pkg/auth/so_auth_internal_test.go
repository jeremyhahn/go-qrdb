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

// Internal tests for so_auth.go — runs in package auth so unexported
// symbols (verifyPIN, hashPIN, cryptoRandRead) are accessible.
package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// Minimal in-memory KV store (mirrors so_auth_test.go but in package auth)
// ---------------------------------------------------------------------------

type internalMemKV struct {
	mu     sync.RWMutex
	data   map[string][]byte
	getErr error // if non-nil, Get always returns this error
	putErr error // if non-nil, Put always returns this error
}

func newInternalMemKV() *internalMemKV {
	return &internalMemKV{data: make(map[string][]byte)}
}

func (m *internalMemKV) Put(_ context.Context, k string, v []byte) error {
	if m.putErr != nil {
		return m.putErr
	}
	m.mu.Lock()
	m.data[k] = append([]byte(nil), v...)
	m.mu.Unlock()
	return nil
}
func (m *internalMemKV) Get(_ context.Context, k string) ([]byte, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	m.mu.RLock()
	v := m.data[k]
	m.mu.RUnlock()
	return v, nil
}
func (m *internalMemKV) Delete(_ context.Context, k string) error {
	m.mu.Lock()
	delete(m.data, k)
	m.mu.Unlock()
	return nil
}
func (m *internalMemKV) Scan(_ context.Context, prefix string, fn func(key string, value []byte) error) error {
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
func (m *internalMemKV) List(_ context.Context, prefix string) ([]string, error) {
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
func (m *internalMemKV) Exists(_ context.Context, k string) (bool, error) {
	m.mu.RLock()
	_, ok := m.data[k]
	m.mu.RUnlock()
	return ok, nil
}
func (m *internalMemKV) RegisterEntityIndexes(_ string, _ []kvstore.EntityIndex) error { return nil }
func (m *internalMemKV) QueryIndex(_ context.Context, _ string, _ []byte) ([][]byte, error) {
	return nil, nil
}
func (m *internalMemKV) ScanIndex(_ context.Context, _ string, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
func (m *internalMemKV) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}
func (m *internalMemKV) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// No-op audit logger (internal package variant)
// ---------------------------------------------------------------------------

type internalNoopAudit struct{}

func (n *internalNoopAudit) Emit(_ context.Context, _ audit.AuditEvent) {}
func (n *internalNoopAudit) Query(_ context.Context, _ audit.Filter) (*audit.Page, error) {
	return &audit.Page{}, nil
}
func (n *internalNoopAudit) Close() error { return nil }

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func newInternalSOAuth(t *testing.T, store *internalMemKV) *SOAuthenticator {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	a, err := NewSOAuthenticator(store, &internalNoopAudit{}, logger)
	if err != nil {
		t.Fatalf("NewSOAuthenticator: %v", err)
	}
	return a
}

// ---------------------------------------------------------------------------
// verifyPIN — internal function tests
// ---------------------------------------------------------------------------

func TestVerifyPIN_RecordTooShort(t *testing.T) {
	err := verifyPIN([]byte("pin"), "AB")
	if !errors.Is(err, ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid, got %v", err)
	}
}

func TestVerifyPIN_MissingColon(t *testing.T) {
	// Record is > 3 chars but has no colon after the kdfTag.
	// kdfTag='A', rest="deadbeef" — no colon in rest.
	err := verifyPIN([]byte("pin"), "A:deadbeef")
	if !errors.Is(err, ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid, got %v", err)
	}
}

func TestVerifyPIN_BadSaltHex(t *testing.T) {
	// kdfTag='A', saltHex="ZZ" (invalid hex), hashHex="aabb".
	err := verifyPIN([]byte("pin"), "A:ZZ:aabb")
	if !errors.Is(err, ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid, got %v", err)
	}
}

func TestVerifyPIN_BadHashHex(t *testing.T) {
	// kdfTag='A', saltHex="aabb" (valid hex), hashHex="ZZ" (invalid hex).
	err := verifyPIN([]byte("pin"), "A:aabb:ZZ")
	if !errors.Is(err, ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid, got %v", err)
	}
}

func TestVerifyPIN_UnknownKDFTag(t *testing.T) {
	// kdfTag='X' is not 'A' or 'P'.
	err := verifyPIN([]byte("pin"), "X:aabb:ccdd")
	if !errors.Is(err, ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid, got %v", err)
	}
}

func TestVerifyPIN_WrongPIN_PBKDF2(t *testing.T) {
	// Use FIPS mode to cover the 'P' (PBKDF2) branch in verifyPIN.
	t.Setenv("GOFIPS140", "1")

	record, err := hashPIN([]byte("correct-pin"))
	if err != nil {
		t.Fatalf("hashPIN: %v", err)
	}
	// Wrong PIN against a PBKDF2 record.
	err = verifyPIN([]byte("wrong-pin"), record)
	if !errors.Is(err, ErrSOPINInvalid) {
		t.Fatalf("expected ErrSOPINInvalid for wrong PBKDF2 PIN, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// hashPIN — cryptoRandRead failure branch
// ---------------------------------------------------------------------------

func TestHashPIN_RandReadError(t *testing.T) {
	origRead := cryptoRandRead
	defer func() { cryptoRandRead = origRead }()

	randErr := fmt.Errorf("injected random failure")
	cryptoRandRead = func(b []byte) (int, error) {
		return 0, randErr
	}

	_, err := hashPIN([]byte("any-pin"))
	if err == nil {
		t.Fatal("expected error when cryptoRandRead fails")
	}
	if !strings.Contains(err.Error(), "generate salt") {
		t.Errorf("expected 'generate salt' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// SetSOPIN — store.Get error and store.Put error paths
// ---------------------------------------------------------------------------

func TestSetSOPIN_StoreGetError(t *testing.T) {
	store := newInternalMemKV()
	store.getErr = fmt.Errorf("injected get failure")
	a := newInternalSOAuth(t, store)

	err := a.SetSOPIN(context.Background(), "tenant-1", []byte("some-pin"))
	if err == nil {
		t.Fatal("expected error when store.Get fails")
	}
	if !strings.Contains(err.Error(), "store get") {
		t.Errorf("expected 'store get' in error, got: %v", err)
	}
}

func TestSetSOPIN_HashPINError(t *testing.T) {
	origRead := cryptoRandRead
	defer func() { cryptoRandRead = origRead }()

	cryptoRandRead = func(b []byte) (int, error) {
		return 0, fmt.Errorf("injected rand failure")
	}

	store := newInternalMemKV()
	a := newInternalSOAuth(t, store)

	err := a.SetSOPIN(context.Background(), "tenant-1", []byte("some-pin"))
	if err == nil {
		t.Fatal("expected error when hashPIN fails due to cryptoRandRead failure")
	}
}

func TestSetSOPIN_StorePutError(t *testing.T) {
	store := newInternalMemKV()
	a := newInternalSOAuth(t, store)

	// Pre-seed: we need Get to return nil (no existing PIN) but Put to fail.
	// We can't do both with a single flag, so use a custom approach:
	// Set putErr only — Get will return nil (no entry), then Put fails.
	store.putErr = fmt.Errorf("injected put failure")

	err := a.SetSOPIN(context.Background(), "tenant-1", []byte("some-pin"))
	if err == nil {
		t.Fatal("expected error when store.Put fails")
	}
	if !strings.Contains(err.Error(), "store put") {
		t.Errorf("expected 'store put' in error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RotateSOPIN — hashPIN error and store.Put error paths
// ---------------------------------------------------------------------------

func TestRotateSOPIN_HashPINError(t *testing.T) {
	store := newInternalMemKV()
	a := newInternalSOAuth(t, store)

	// First set the PIN normally.
	if err := a.SetSOPIN(context.Background(), "tenant-1", []byte("old-pin")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}

	// Now break the random source.
	origRead := cryptoRandRead
	defer func() { cryptoRandRead = origRead }()
	cryptoRandRead = func(b []byte) (int, error) {
		return 0, fmt.Errorf("injected rand failure")
	}

	err := a.RotateSOPIN(context.Background(), "tenant-1", []byte("old-pin"), []byte("new-pin"))
	if err == nil {
		t.Fatal("expected error when hashPIN fails during RotateSOPIN")
	}
}

func TestRotateSOPIN_StorePutError(t *testing.T) {
	store := newInternalMemKV()
	a := newInternalSOAuth(t, store)

	// Set PIN normally.
	if err := a.SetSOPIN(context.Background(), "tenant-1", []byte("old-pin")); err != nil {
		t.Fatalf("SetSOPIN: %v", err)
	}

	// Now inject Put failure — VerifySOPIN uses Get (ok), then RotateSOPIN calls Put.
	store.putErr = fmt.Errorf("injected put failure")

	err := a.RotateSOPIN(context.Background(), "tenant-1", []byte("old-pin"), []byte("new-pin"))
	if err == nil {
		t.Fatal("expected error when store.Put fails during RotateSOPIN")
	}
	if !strings.Contains(err.Error(), "store put") {
		t.Errorf("expected 'store put' in error, got: %v", err)
	}
}
