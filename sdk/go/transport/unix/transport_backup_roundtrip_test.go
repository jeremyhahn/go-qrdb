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

// Round-trip tests for the Backup surface (transport.BackupClient), closing
// the unix-vs-grpc SDK transport parity gap: the CLI's "qrdb backup"
// commands previously panicked over the default (unix) transport because
// BackupClient was implemented only by the gRPC and REST transports.

package unix

import (
	"context"
	"testing"
	"time"

	unixserver "github.com/jeremyhahn/go-qrdb/pkg/api/unix"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// mockBackupSvc
// ---------------------------------------------------------------------------

type mockBackupSvc struct {
	createFn  func(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error)
	listFn    func(ctx context.Context) ([]service.BackupInfo, error)
	getFn     func(ctx context.Context, id string) (*service.BackupInfo, error)
	deleteFn  func(ctx context.Context, id string) error
	restoreFn func(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error)
	verifyFn  func(ctx context.Context, id string) (*service.VerifyResult, error)
}

var _ service.BackupServicer = (*mockBackupSvc)(nil)

func (m *mockBackupSvc) CreateBackup(ctx context.Context, opts service.BackupOptions) (*service.BackupInfo, error) {
	if m.createFn != nil {
		return m.createFn(ctx, opts)
	}
	return &service.BackupInfo{ID: "backup-001", Timestamp: time.Now(), ShardCount: 2, Destination: opts.Destination}, nil
}

func (m *mockBackupSvc) ListBackups(ctx context.Context) ([]service.BackupInfo, error) {
	if m.listFn != nil {
		return m.listFn(ctx)
	}
	return []service.BackupInfo{{ID: "backup-001", Timestamp: time.Now()}}, nil
}

func (m *mockBackupSvc) GetBackup(ctx context.Context, id string) (*service.BackupInfo, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return &service.BackupInfo{ID: id, Timestamp: time.Now()}, nil
}

func (m *mockBackupSvc) DeleteBackup(ctx context.Context, id string) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, id)
	}
	return nil
}

func (m *mockBackupSvc) RestoreBackup(ctx context.Context, opts service.RestoreOptions) (*service.RestoreResult, error) {
	if m.restoreFn != nil {
		return m.restoreFn(ctx, opts)
	}
	return &service.RestoreResult{BackupID: opts.BackupID, ShardsRestored: 2, Duration: time.Second}, nil
}

func (m *mockBackupSvc) VerifyBackup(ctx context.Context, id string) (*service.VerifyResult, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, id)
	}
	return &service.VerifyResult{Valid: true, ChecksumMatch: true, ExpectedChecksum: "abc123", ActualChecksum: "abc123"}, nil
}

// ---------------------------------------------------------------------------
// CreateBackup
// ---------------------------------------------------------------------------

func TestUnixRT_CreateBackup(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(&mockBackupSvc{}))
	defer cleanup()

	info, err := tr.CreateBackup(context.Background(), &transport.BackupRequest{Destination: "s3://bucket/backup"})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info.ID != "backup-001" || info.Destination != "s3://bucket/backup" {
		t.Errorf("got %+v, want id=backup-001 destination=s3://bucket/backup", info)
	}
}

func TestUnixRT_CreateBackup_Error(t *testing.T) {
	svc := &mockBackupSvc{createFn: func(_ context.Context, _ service.BackupOptions) (*service.BackupInfo, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(svc))
	defer cleanup()

	if _, err := tr.CreateBackup(context.Background(), &transport.BackupRequest{}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// ListBackups
// ---------------------------------------------------------------------------

func TestUnixRT_ListBackups(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(&mockBackupSvc{}))
	defer cleanup()

	backups, err := tr.ListBackups(context.Background())
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 1 || backups[0].ID != "backup-001" {
		t.Errorf("got %+v, want 1 backup with id=backup-001", backups)
	}
}

func TestUnixRT_ListBackups_Error(t *testing.T) {
	svc := &mockBackupSvc{listFn: func(_ context.Context) ([]service.BackupInfo, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(svc))
	defer cleanup()

	if _, err := tr.ListBackups(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// GetBackup
// ---------------------------------------------------------------------------

func TestUnixRT_GetBackup(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(&mockBackupSvc{}))
	defer cleanup()

	info, err := tr.GetBackup(context.Background(), "backup-42")
	if err != nil {
		t.Fatalf("GetBackup: %v", err)
	}
	if info.ID != "backup-42" {
		t.Errorf("got %+v, want id=backup-42", info)
	}
}

func TestUnixRT_GetBackup_Error(t *testing.T) {
	svc := &mockBackupSvc{getFn: func(_ context.Context, _ string) (*service.BackupInfo, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(svc))
	defer cleanup()

	if _, err := tr.GetBackup(context.Background(), "missing"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// DeleteBackup
// ---------------------------------------------------------------------------

func TestUnixRT_DeleteBackup(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(&mockBackupSvc{}))
	defer cleanup()

	if err := tr.DeleteBackup(context.Background(), "backup-001"); err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}
}

func TestUnixRT_DeleteBackup_Error(t *testing.T) {
	svc := &mockBackupSvc{deleteFn: func(_ context.Context, _ string) error {
		return errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(svc))
	defer cleanup()

	if err := tr.DeleteBackup(context.Background(), "backup-001"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// RestoreBackup
// ---------------------------------------------------------------------------

func TestUnixRT_RestoreBackup(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(&mockBackupSvc{}))
	defer cleanup()

	result, err := tr.RestoreBackup(context.Background(), &transport.RestoreRequest{BackupID: "backup-001", TargetShards: []uint64{1, 2}})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.BackupID != "backup-001" || result.ShardsRestored != 2 {
		t.Errorf("got %+v, want backupID=backup-001 shardsRestored=2", result)
	}
}

func TestUnixRT_RestoreBackup_Error(t *testing.T) {
	svc := &mockBackupSvc{restoreFn: func(_ context.Context, _ service.RestoreOptions) (*service.RestoreResult, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(svc))
	defer cleanup()

	if _, err := tr.RestoreBackup(context.Background(), &transport.RestoreRequest{BackupID: "backup-001"}); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// VerifyBackup
// ---------------------------------------------------------------------------

func TestUnixRT_VerifyBackup(t *testing.T) {
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(&mockBackupSvc{}))
	defer cleanup()

	result, err := tr.VerifyBackup(context.Background(), "backup-001")
	if err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
	if !result.Valid || !result.ChecksumMatch {
		t.Errorf("got %+v, want valid+checksumMatch", result)
	}
}

func TestUnixRT_VerifyBackup_Error(t *testing.T) {
	svc := &mockBackupSvc{verifyFn: func(_ context.Context, _ string) (*service.VerifyResult, error) {
		return nil, errServiceFailed
	}}
	tr, cleanup := startAuthServer(t, unixserver.WithBackupService(svc))
	defer cleanup()

	if _, err := tr.VerifyBackup(context.Background(), "backup-001"); err == nil {
		t.Fatal("expected error")
	}
}

// ---------------------------------------------------------------------------
// protoToBackupInfo nil guard
// ---------------------------------------------------------------------------

func TestUnix_ProtoToBackupInfo_Nil(t *testing.T) {
	if got := protoToBackupInfo(nil); got != nil {
		t.Errorf("protoToBackupInfo(nil) = %+v, want nil", got)
	}
}
