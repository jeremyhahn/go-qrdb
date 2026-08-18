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

package service

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/dek"
)

// ---------------------------------------------------------------------------
// Mock types for testing
// ---------------------------------------------------------------------------

// mockClusterDEKManager wraps a real ClusterDEKManager but is only used
// to satisfy the constructor. For tests that exercise cluster operations
// we use a real manager backed by a mock barrier.
type mockBarrier struct {
	sealed    bool
	encrypted []byte
}

func (m *mockBarrier) Encrypt(plaintext []byte) ([]byte, error) {
	if m.sealed {
		return nil, &dek.SealedError{Op: "mockBarrier.Encrypt"}
	}
	// Simple "encryption": append a marker byte.
	out := make([]byte, len(plaintext)+1)
	copy(out, plaintext)
	out[len(plaintext)] = 0xFF
	m.encrypted = out
	return out, nil
}

func (m *mockBarrier) Decrypt(ciphertext []byte) ([]byte, error) {
	if m.sealed {
		return nil, &dek.SealedError{Op: "mockBarrier.Decrypt"}
	}
	if len(ciphertext) < 1 {
		return nil, &dek.CryptoError{Op: "mockBarrier.Decrypt", Detail: "too short"}
	}
	return ciphertext[:len(ciphertext)-1], nil
}

func (m *mockBarrier) IsSealed() bool {
	return m.sealed
}

// mockTenantDEKManager implements dek.TenantDEKManager for testing.
type mockTenantDEKManager struct {
	tenants map[string]*dek.TenantDEKStatus
	err     error
}

func newMockTenantDEKManager() *mockTenantDEKManager {
	return &mockTenantDEKManager{
		tenants: make(map[string]*dek.TenantDEKStatus),
	}
}

func (m *mockTenantDEKManager) GetOrCreateTenantDEK(_ context.Context, tenantID string) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	if _, ok := m.tenants[tenantID]; !ok {
		m.tenants[tenantID] = &dek.TenantDEKStatus{
			TenantID:  tenantID,
			Version:   1,
			Algorithm: "AES-256-GCM",
			KeyBytes:  32,
			CreatedAt: time.Now(),
		}
	}
	return []byte("mock-dek-" + tenantID), nil
}

func (m *mockTenantDEKManager) GetTenantDEK(_ context.Context, tenantID string) ([]byte, error) {
	if m.err != nil {
		return nil, m.err
	}
	if _, ok := m.tenants[tenantID]; !ok {
		return nil, &dek.NotFoundError{Resource: "tenant DEK", ID: tenantID}
	}
	return []byte("mock-dek-" + tenantID), nil
}

func (m *mockTenantDEKManager) RotateTenantDEK(_ context.Context, tenantID string) error {
	if m.err != nil {
		return m.err
	}
	status, ok := m.tenants[tenantID]
	if !ok {
		return &dek.NotFoundError{Resource: "tenant DEK", ID: tenantID}
	}
	status.Version++
	status.RotatedAt = time.Now()
	return nil
}

func (m *mockTenantDEKManager) DeleteTenantDEK(_ context.Context, tenantID string) error {
	if m.err != nil {
		return m.err
	}
	delete(m.tenants, tenantID)
	return nil
}

func (m *mockTenantDEKManager) GetTenantDEKStatus(_ context.Context, tenantID string) (*dek.TenantDEKStatus, error) {
	if m.err != nil {
		return nil, m.err
	}
	status, ok := m.tenants[tenantID]
	if !ok {
		return nil, &dek.NotFoundError{Resource: "tenant DEK", ID: tenantID}
	}
	return status, nil
}

func (m *mockTenantDEKManager) ListTenantDEKs(_ context.Context) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	ids := make([]string, 0, len(m.tenants))
	for id := range m.tenants {
		ids = append(ids, id)
	}
	return ids, nil
}

func (m *mockTenantDEKManager) Close() error {
	return nil
}

// ---------------------------------------------------------------------------
// Constructor tests
// ---------------------------------------------------------------------------

func TestNewDEKService_NilCluster(t *testing.T) {
	logger := slog.Default()
	_, err := NewDEKService(nil, newMockTenantDEKManager(), logger)
	if err == nil {
		t.Fatal("expected error for nil cluster, got nil")
	}
	var nilErr *dek.NilInputError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected *dek.NilInputError, got %T: %v", err, err)
	}
	if nilErr.Field != "cluster" {
		t.Fatalf("expected field 'cluster', got %q", nilErr.Field)
	}
}

func TestNewDEKService_NilLogger(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error creating cluster manager: %v", err)
	}
	_, err = NewDEKService(cluster, newMockTenantDEKManager(), nil)
	if err == nil {
		t.Fatal("expected error for nil logger, got nil")
	}
	var nilErr *dek.NilInputError
	if !errors.As(err, &nilErr) {
		t.Fatalf("expected *dek.NilInputError, got %T: %v", err, err)
	}
	if nilErr.Field != "logger" {
		t.Fatalf("expected field 'logger', got %q", nilErr.Field)
	}
}

func TestNewDEKService_NilTenantAllowed(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error creating cluster manager: %v", err)
	}
	svc, err := NewDEKService(cluster, nil, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestNewDEKService_Success(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error creating cluster manager: %v", err)
	}
	svc, err := NewDEKService(cluster, newMockTenantDEKManager(), slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

// ---------------------------------------------------------------------------
// Cluster operation tests
// ---------------------------------------------------------------------------

func newTestDEKService(t *testing.T) (*DEKService, *mockBarrier, *mockTenantDEKManager) {
	t.Helper()
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error creating cluster manager: %v", err)
	}
	tenantMgr := newMockTenantDEKManager()
	svc, err := NewDEKService(cluster, tenantMgr, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error creating DEK service: %v", err)
	}
	return svc, barrier, tenantMgr
}

func TestDEKService_Initialize(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	err := svc.Initialize(ctx, "AES-256-GCM", 32)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDEKService_Initialize_SealedBarrier(t *testing.T) {
	svc, barrier, _ := newTestDEKService(t)
	barrier.sealed = true
	ctx := context.Background()

	err := svc.Initialize(ctx, "AES-256-GCM", 32)
	if err == nil {
		t.Fatal("expected error for sealed barrier, got nil")
	}
	var sealedErr *dek.SealedError
	if !errors.As(err, &sealedErr) {
		t.Fatalf("expected *dek.SealedError, got %T: %v", err, err)
	}
}

func TestDEKService_GetStatus_Uninitialized(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	status, err := svc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.Initialized {
		t.Fatal("expected uninitialized status")
	}
}

func TestDEKService_GetStatus_Initialized(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	if err := svc.Initialize(ctx, "AES-256-GCM", 32); err != nil {
		t.Fatalf("initialize error: %v", err)
	}

	status, err := svc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !status.Initialized {
		t.Fatal("expected initialized status")
	}
	if status.Version != 1 {
		t.Fatalf("expected version 1, got %d", status.Version)
	}
	if status.Algorithm != "AES-256-GCM" {
		t.Fatalf("expected AES-256-GCM, got %s", status.Algorithm)
	}
}

func TestDEKService_Rotate(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	if err := svc.Initialize(ctx, "AES-256-GCM", 32); err != nil {
		t.Fatalf("initialize error: %v", err)
	}

	if err := svc.Rotate(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status, err := svc.GetStatus(ctx)
	if err != nil {
		t.Fatalf("status error: %v", err)
	}
	if status.Version != 2 {
		t.Fatalf("expected version 2 after rotation, got %d", status.Version)
	}
}

func TestDEKService_Rotate_NotInitialized(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	err := svc.Rotate(ctx)
	if err == nil {
		t.Fatal("expected error for uninitialized rotation, got nil")
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}

func TestDEKService_CurrentDEKVersion(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	if err := svc.Initialize(ctx, "AES-256-GCM", 32); err != nil {
		t.Fatalf("initialize error: %v", err)
	}

	version, err := svc.CurrentDEKVersion(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if version != 1 {
		t.Fatalf("expected version 1, got %d", version)
	}

	if err := svc.Rotate(ctx); err != nil {
		t.Fatalf("rotate error: %v", err)
	}
	version, err = svc.CurrentDEKVersion(ctx)
	if err != nil {
		t.Fatalf("unexpected error after rotate: %v", err)
	}
	if version != 2 {
		t.Fatalf("expected version 2 after rotate, got %d", version)
	}
}

func TestDEKService_CurrentDEKVersion_NotInitialized(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	version, err := svc.CurrentDEKVersion(ctx)
	if err == nil {
		t.Fatal("expected error for uninitialized CurrentDEKVersion, got nil")
	}
	if version != 0 {
		t.Fatalf("expected version 0 on error, got %d", version)
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}

func TestDEKService_SyncToNode(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	if err := svc.Initialize(ctx, "AES-256-GCM", 32); err != nil {
		t.Fatalf("initialize error: %v", err)
	}

	if err := svc.SyncToNode(ctx, 42); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDEKService_SyncToNode_NotInitialized(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	err := svc.SyncToNode(ctx, 42)
	if err == nil {
		t.Fatal("expected error for uninitialized SyncToNode, got nil")
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Tenant operation tests
// ---------------------------------------------------------------------------

func TestDEKService_TenantGetOrCreate(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	dekBytes, err := svc.TenantGetOrCreate(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(dekBytes) == 0 {
		t.Fatal("expected non-empty DEK bytes")
	}
}

func TestDEKService_TenantGetOrCreate_NilTenantManager(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc, err := NewDEKService(cluster, nil, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.TenantGetOrCreate(context.Background(), "tenant-1")
	if err == nil {
		t.Fatal("expected error for nil tenant manager, got nil")
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}

func TestDEKService_TenantRotate(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	// Create tenant DEK first.
	if _, err := svc.TenantGetOrCreate(ctx, "tenant-1"); err != nil {
		t.Fatalf("create error: %v", err)
	}

	if err := svc.TenantRotate(ctx, "tenant-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDEKService_TenantRotate_NilTenantManager(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc, err := NewDEKService(cluster, nil, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = svc.TenantRotate(context.Background(), "tenant-1")
	if err == nil {
		t.Fatal("expected error for nil tenant manager, got nil")
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}

func TestDEKService_TenantDelete(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	// Create and then delete.
	if _, err := svc.TenantGetOrCreate(ctx, "tenant-1"); err != nil {
		t.Fatalf("create error: %v", err)
	}

	if err := svc.TenantDelete(ctx, "tenant-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDEKService_TenantDelete_NilTenantManager(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc, err := NewDEKService(cluster, nil, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = svc.TenantDelete(context.Background(), "tenant-1")
	if err == nil {
		t.Fatal("expected error for nil tenant manager, got nil")
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}

func TestDEKService_TenantList(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	// Create a couple tenants.
	if _, err := svc.TenantGetOrCreate(ctx, "tenant-1"); err != nil {
		t.Fatalf("create error: %v", err)
	}
	if _, err := svc.TenantGetOrCreate(ctx, "tenant-2"); err != nil {
		t.Fatalf("create error: %v", err)
	}

	ids, err := svc.TenantList(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 tenant IDs, got %d", len(ids))
	}
}

func TestDEKService_TenantList_NilTenantManager(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc, err := NewDEKService(cluster, nil, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.TenantList(context.Background())
	if err == nil {
		t.Fatal("expected error for nil tenant manager, got nil")
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}

func TestDEKService_TenantGetStatus(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	// Create tenant DEK first.
	if _, err := svc.TenantGetOrCreate(ctx, "tenant-1"); err != nil {
		t.Fatalf("create error: %v", err)
	}

	status, err := svc.TenantGetStatus(ctx, "tenant-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status.TenantID != "tenant-1" {
		t.Fatalf("expected tenant-1, got %s", status.TenantID)
	}
	if status.Version != 1 {
		t.Fatalf("expected version 1, got %d", status.Version)
	}
}

func TestDEKService_TenantGetStatus_NotFound(t *testing.T) {
	svc, _, _ := newTestDEKService(t)
	ctx := context.Background()

	_, err := svc.TenantGetStatus(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent tenant, got nil")
	}
	var notFound *dek.NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected *dek.NotFoundError, got %T: %v", err, err)
	}
}

func TestDEKService_TenantGetStatus_NilTenantManager(t *testing.T) {
	barrier := &mockBarrier{}
	cluster, err := dek.NewClusterDEKManager(barrier, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	svc, err := NewDEKService(cluster, nil, slog.Default())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = svc.TenantGetStatus(context.Background(), "tenant-1")
	if err == nil {
		t.Fatal("expected error for nil tenant manager, got nil")
	}
	var notInit *dek.NotInitializedError
	if !errors.As(err, &notInit) {
		t.Fatalf("expected *dek.NotInitializedError, got %T: %v", err, err)
	}
}
