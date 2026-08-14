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
	"errors"
	"testing"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport/embedded"
)

// ---------------------------------------------------------------------------
// Mock backup service
// ---------------------------------------------------------------------------

// mockBackupService implements service.BackupServicer with configurable
// function fields for per-test behavior injection.
type mockBackupService struct {
	createFn  func(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error)
	listFn    func(ctx context.Context) ([]service.BackupInfo, error)
	getFn     func(ctx context.Context, id string) (*service.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error)
	verifyFn  func(ctx context.Context, id string) (*service.VerifyResult, error)
}

func (m *mockBackupService) CreateBackup(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, opts)
	}
	return &service.BackupInfo{
		ID:          "bkp-001",
		Timestamp:   time.Now(),
		Size:        1024,
		ShardCount:  2,
		Status:      service.BackupStatusComplete,
		Checksum:    "abc123",
		Destination: opts.Destination,
		Compression: opts.Compression,
		Encryption:  opts.Encryption,
		Duration:    100 * time.Millisecond,
	}, nil
}

func (m *mockBackupService) ListBackups(ctx context.Context) ([]service.BackupInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []service.BackupInfo{
		{
			ID:          "bkp-001",
			Timestamp:   time.Now(),
			Size:        1024,
			ShardCount:  2,
			Status:      service.BackupStatusComplete,
			Destination: "/backups/test",
		},
	}, nil
}

func (m *mockBackupService) GetBackup(ctx context.Context, id string) (*service.BackupInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	if id == "notfound" {
		return nil, &service.BackupNotFoundError{ID: id}
	}
	return &service.BackupInfo{
		ID:          id,
		Timestamp:   time.Now(),
		Size:        1024,
		ShardCount:  2,
		Status:      service.BackupStatusComplete,
		Destination: "/backups/test",
	}, nil
}

func (m *mockBackupService) DeleteBackup(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	if id == "notfound" {
		return &service.BackupNotFoundError{ID: id}
	}
	return nil
}

func (m *mockBackupService) RestoreBackup(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
	if m.restoreFn != nil {
		return m.restoreFn(ctx, opts)
	}
	return &service.RestoreResult{
		BackupID:       opts.BackupID,
		ShardsRestored: 2,
		Duration:       50 * time.Millisecond,
	}, nil
}

func (m *mockBackupService) VerifyBackup(ctx context.Context, id string) (*service.VerifyResult, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, id)
	}
	return &service.VerifyResult{
		Valid:            true,
		ChecksumMatch:    true,
		ExpectedChecksum: "abc123",
		ActualChecksum:   "abc123",
	}, nil
}

// newTestBackupBinding creates a BackupBinding wired to the given mock service.
func newTestBackupBinding(t *testing.T, svc service.BackupServicer) *BackupBinding {
	t.Helper()
	conn := newTestConnectionBinding(
		t,
		&noopKVService{},
		newMockAdminService(),
		embedded.WithBackupService(svc),
	)
	return NewBackupBinding(testLogger(t), conn)
}

// ---------------------------------------------------------------------------
// BackupBinding.Create tests
// ---------------------------------------------------------------------------

func TestBackupBinding_Create_Success(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	info, err := b.Create(transportBackupRequest("/backups/test"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if info.Destination != "/backups/test" {
		t.Fatalf("expected destination /backups/test, got %s", info.Destination)
	}
}

func TestBackupBinding_Create_ServiceError(t *testing.T) {
	svc := &mockBackupService{
		createFn: func(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) {
			return nil, &service.BackupEmptyDestinationError{}
		},
	}
	b := newTestBackupBinding(t, svc)

	_, err := b.Create(transportBackupRequest(""))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBackupBinding_Create_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewBackupBinding(testLogger(t), conn)

	_, err := b.Create(transportBackupRequest("/backups/test"))
	if err == nil {
		t.Fatal("expected NotConnectedError")
	}
	var notConn *NotConnectedError
	if !errors.As(err, &notConn) {
		t.Fatalf("expected NotConnectedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// BackupBinding.List tests
// ---------------------------------------------------------------------------

func TestBackupBinding_List_Success(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	backups, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(backups) == 0 {
		t.Fatal("expected at least one backup")
	}
}

func TestBackupBinding_List_Empty(t *testing.T) {
	svc := &mockBackupService{
		listFn: func(_ context.Context) ([]service.BackupInfo, error) {
			return nil, nil
		},
	}
	b := newTestBackupBinding(t, svc)

	backups, err := b.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected 0 backups, got %d", len(backups))
	}
}

func TestBackupBinding_List_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewBackupBinding(testLogger(t), conn)

	_, err := b.List()
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// BackupBinding.Get tests
// ---------------------------------------------------------------------------

func TestBackupBinding_Get_Success(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	info, err := b.Get("bkp-001")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if info.ID != "bkp-001" {
		t.Fatalf("expected ID bkp-001, got %s", info.ID)
	}
}

func TestBackupBinding_Get_NotFound(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	_, err := b.Get("notfound")
	if err == nil {
		t.Fatal("expected error for unknown backup")
	}
}

func TestBackupBinding_Get_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewBackupBinding(testLogger(t), conn)

	_, err := b.Get("bkp-001")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// BackupBinding.Delete tests
// ---------------------------------------------------------------------------

func TestBackupBinding_Delete_Success(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	if err := b.Delete("bkp-001"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
}

func TestBackupBinding_Delete_NotFound(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	err := b.Delete("notfound")
	if err == nil {
		t.Fatal("expected error for unknown backup")
	}
}

func TestBackupBinding_Delete_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewBackupBinding(testLogger(t), conn)

	if err := b.Delete("bkp-001"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// BackupBinding.Restore tests
// ---------------------------------------------------------------------------

func TestBackupBinding_Restore_Success(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	result, err := b.Restore(transportRestoreRequest("bkp-001"))
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if result.BackupID != "bkp-001" {
		t.Fatalf("expected BackupID bkp-001, got %s", result.BackupID)
	}
	if result.ShardsRestored != 2 {
		t.Fatalf("expected 2 shards restored, got %d", result.ShardsRestored)
	}
}

func TestBackupBinding_Restore_ServiceError(t *testing.T) {
	svc := &mockBackupService{
		restoreFn: func(_ context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
			return nil, &service.BackupNotFoundError{ID: opts.BackupID}
		},
	}
	b := newTestBackupBinding(t, svc)

	_, err := b.Restore(transportRestoreRequest("missing"))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestBackupBinding_Restore_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewBackupBinding(testLogger(t), conn)

	_, err := b.Restore(transportRestoreRequest("bkp-001"))
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// BackupBinding.Verify tests
// ---------------------------------------------------------------------------

func TestBackupBinding_Verify_Success(t *testing.T) {
	svc := &mockBackupService{}
	b := newTestBackupBinding(t, svc)

	result, err := b.Verify("bkp-001")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !result.Valid {
		t.Fatal("expected valid backup")
	}
	if !result.ChecksumMatch {
		t.Fatal("expected checksum match")
	}
}

func TestBackupBinding_Verify_Invalid(t *testing.T) {
	svc := &mockBackupService{
		verifyFn: func(_ context.Context, _ string) (*service.VerifyResult, error) {
			return &service.VerifyResult{
				Valid:            false,
				ChecksumMatch:    false,
				ExpectedChecksum: "abc",
				ActualChecksum:   "xyz",
				Errors:           []string{"checksum mismatch"},
			}, nil
		},
	}
	b := newTestBackupBinding(t, svc)

	result, err := b.Verify("bkp-corrupt")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if result.Valid {
		t.Fatal("expected invalid backup")
	}
}

func TestBackupBinding_Verify_NotConnected(t *testing.T) {
	conn := NewConnectionBinding(testLogger(t))
	b := NewBackupBinding(testLogger(t), conn)

	_, err := b.Verify("bkp-001")
	if err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// Convenience constructors for transport request types
// ---------------------------------------------------------------------------

func transportBackupRequest(dest string) *transport.BackupRequest {
	return &transport.BackupRequest{Destination: dest}
}

func transportRestoreRequest(backupID string) *transport.RestoreRequest {
	return &transport.RestoreRequest{BackupID: backupID}
}
