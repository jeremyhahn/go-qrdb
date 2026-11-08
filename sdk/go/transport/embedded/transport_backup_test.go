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

package embedded

import (
	"context"
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/sdk/go/transport"
)

// ---------------------------------------------------------------------------
// Mock snapshot provider for embedded tests
// ---------------------------------------------------------------------------

type testSnapshotProvider struct {
	shards map[uint64][]byte
}

func newTestSnapshotProvider() *testSnapshotProvider {
	return &testSnapshotProvider{
		shards: map[uint64][]byte{
			1: []byte("shard-1-snapshot"),
			2: []byte("shard-2-snapshot"),
		},
	}
}

func (p *testSnapshotProvider) AvailableShards(_ context.Context) ([]uint64, error) {
	ids := make([]uint64, 0, len(p.shards))
	for id := range p.shards {
		ids = append(ids, id)
	}
	return ids, nil
}

func (p *testSnapshotProvider) SnapshotShard(_ context.Context, shardID uint64) ([]byte, error) {
	data, ok := p.shards[shardID]
	if !ok {
		return nil, errors.New("shard not found")
	}
	return data, nil
}

func (p *testSnapshotProvider) RestoreShard(_ context.Context, _ uint64, _ []byte) error {
	return nil
}

// newBackupTransport creates a Transport with a BackupServicer for testing.
func newBackupTransport(t *testing.T) *Transport {
	t.Helper()
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	provider := newTestSnapshotProvider()
	backupSvc, err := service.NewBackupService(provider)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}

	tr, err := New(kvSvc, adminSvc, WithBackupService(backupSvc))
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}
	return tr
}

// ---------------------------------------------------------------------------
// CreateBackup tests
// ---------------------------------------------------------------------------

func TestTransportBackup_CreateBackup_Success(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	info, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/embedded-test",
		Encryption:  transport.EncryptionNone,
		Metadata:    map[string]string{"test": "true"},
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if info.ID == "" {
		t.Fatal("expected non-empty backup ID")
	}
	if info.Status != transport.BackupStatusComplete {
		t.Fatalf("expected status complete, got %s", info.Status)
	}
	if info.ShardCount != 2 {
		t.Fatalf("expected 2 shards, got %d", info.ShardCount)
	}
	if info.Destination != "/backups/embedded-test" {
		t.Fatalf("expected destination /backups/embedded-test, got %s", info.Destination)
	}
}

func TestTransportBackup_CreateBackup_EmptyDestination(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	_, err := tr.CreateBackup(ctx, &transport.BackupRequest{})
	if err == nil {
		t.Fatal("expected error for empty destination")
	}
	var emptyDest *transport.BackupEmptyDestinationError
	if !errors.As(err, &emptyDest) {
		t.Fatalf("expected BackupEmptyDestinationError, got %T: %v", err, err)
	}
}

func TestTransportBackup_CreateBackup_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/test",
	})
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
	var nilSvc *NilServiceError
	if !errors.As(err, &nilSvc) {
		t.Fatalf("expected NilServiceError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// ListBackups tests
// ---------------------------------------------------------------------------

func TestTransportBackup_ListBackups_Empty(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	backups, err := tr.ListBackups(ctx)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected 0 backups, got %d", len(backups))
	}
}

func TestTransportBackup_ListBackups_AfterCreate(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	_, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/list-test",
		Encryption:  transport.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	backups, err := tr.ListBackups(ctx)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backups))
	}
}

func TestTransportBackup_ListBackups_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.ListBackups(ctx)
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
}

// ---------------------------------------------------------------------------
// GetBackup tests
// ---------------------------------------------------------------------------

func TestTransportBackup_GetBackup_Success(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	created, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/get-test",
		Encryption:  transport.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	info, err := tr.GetBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetBackup: %v", err)
	}
	if info.ID != created.ID {
		t.Fatalf("expected ID %s, got %s", created.ID, info.ID)
	}
}

func TestTransportBackup_GetBackup_NotFound(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	_, err := tr.GetBackup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	var notFound *transport.BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestTransportBackup_GetBackup_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.GetBackup(ctx, "any-id")
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
}

// ---------------------------------------------------------------------------
// DeleteBackup tests
// ---------------------------------------------------------------------------

func TestTransportBackup_DeleteBackup_Success(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	created, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/delete-test",
		Encryption:  transport.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	err = tr.DeleteBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}

	_, err = tr.GetBackup(ctx, created.ID)
	var notFound *transport.BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError after delete, got %T: %v", err, err)
	}
}

func TestTransportBackup_DeleteBackup_NotFound(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	err := tr.DeleteBackup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	var notFound *transport.BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestTransportBackup_DeleteBackup_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	err = tr.DeleteBackup(ctx, "any-id")
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
}

// ---------------------------------------------------------------------------
// RestoreBackup tests
// ---------------------------------------------------------------------------

func TestTransportBackup_RestoreBackup_Success(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	created, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/restore-test",
		Encryption:  transport.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	result, err := tr.RestoreBackup(ctx, &transport.RestoreRequest{
		BackupID:       created.ID,
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.BackupID != created.ID {
		t.Fatalf("expected BackupID %s, got %s", created.ID, result.BackupID)
	}
	if result.ShardsRestored != 2 {
		t.Fatalf("expected 2 shards restored, got %d", result.ShardsRestored)
	}
}

// TestTransportBackup_RestoreBackup_PlaintextRefusedWithoutOptIn proves a
// plaintext-stored backup is refused with
// *transport.BackupPlaintextRestoreNotAllowedError when the caller does
// not set RestoreRequest.AllowPlaintext (go-qrdb security review,
// backup-encrypt-default round 4).
func TestTransportBackup_RestoreBackup_PlaintextRefusedWithoutOptIn(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	created, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/restore-no-optin",
		Encryption:  transport.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	_, err = tr.RestoreBackup(ctx, &transport.RestoreRequest{BackupID: created.ID})
	if err == nil {
		t.Fatal("RestoreBackup: expected an error for a plaintext backup with no AllowPlaintext opt-in")
	}
	var notAllowed *transport.BackupPlaintextRestoreNotAllowedError
	if !errors.As(err, &notAllowed) {
		t.Fatalf("RestoreBackup: err = %T, want *transport.BackupPlaintextRestoreNotAllowedError: %v", err, err)
	}
}

func TestTransportBackup_RestoreBackup_NotFound(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	_, err := tr.RestoreBackup(ctx, &transport.RestoreRequest{
		BackupID: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var notFound *transport.BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestTransportBackup_RestoreBackup_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.RestoreBackup(ctx, &transport.RestoreRequest{
		BackupID: "any-id",
	})
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
}

// ---------------------------------------------------------------------------
// VerifyBackup tests
// ---------------------------------------------------------------------------

func TestTransportBackup_VerifyBackup_Success(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	created, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/verify-test",
		Encryption:  transport.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	result, err := tr.VerifyBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
	if !result.Valid {
		t.Fatal("expected valid backup")
	}
	if !result.ChecksumMatch {
		t.Fatal("expected checksum match")
	}
}

func TestTransportBackup_VerifyBackup_NotFound(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	_, err := tr.VerifyBackup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error")
	}
	var notFound *transport.BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestTransportBackup_VerifyBackup_NilService(t *testing.T) {
	kvSvc := newMockKVService()
	adminSvc := &mockAdminService{}
	tr, err := New(kvSvc, adminSvc)
	if err != nil {
		t.Fatalf("New transport: %v", err)
	}

	ctx := context.Background()
	_, err = tr.VerifyBackup(ctx, "any-id")
	if err == nil {
		t.Fatal("expected NilServiceError")
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle test (create -> list -> get -> verify -> restore -> delete)
// ---------------------------------------------------------------------------

func TestTransportBackup_FullLifecycle(t *testing.T) {
	tr := newBackupTransport(t)
	ctx := context.Background()

	// Create.
	created, err := tr.CreateBackup(ctx, &transport.BackupRequest{
		Destination: "/backups/lifecycle",
		Compression: transport.CompressionSnappy,
		Encryption:  transport.EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	if created.Compression != transport.CompressionSnappy {
		t.Fatalf("expected compression snappy, got %s", created.Compression)
	}

	// List.
	backups, err := tr.ListBackups(ctx)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backups))
	}

	// Get.
	info, err := tr.GetBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetBackup: %v", err)
	}
	if info.ID != created.ID {
		t.Fatalf("expected ID %s, got %s", created.ID, info.ID)
	}

	// Verify.
	verify, err := tr.VerifyBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("VerifyBackup: %v", err)
	}
	if !verify.Valid {
		t.Fatal("expected valid backup")
	}

	// Restore.
	result, err := tr.RestoreBackup(ctx, &transport.RestoreRequest{
		BackupID:       created.ID,
		ValidateFirst:  true,
		AllowPlaintext: true,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.ShardsRestored != 2 {
		t.Fatalf("expected 2 shards restored, got %d", result.ShardsRestored)
	}

	// Delete.
	err = tr.DeleteBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteBackup: %v", err)
	}

	// Verify deleted.
	_, err = tr.GetBackup(ctx, created.ID)
	var notFound *transport.BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

// translateBackupError maps every service backup error onto its transport
// counterpart and passes anything else through unchanged.
func TestEmbeddedTranslateBackupError(t *testing.T) {
	t.Run("Nil", func(t *testing.T) {
		if translateBackupError(nil) != nil {
			t.Fatal("expected nil")
		}
	})
	t.Run("NotFound", func(t *testing.T) {
		got := translateBackupError(&service.BackupNotFoundError{ID: "b1"})
		var want *transport.BackupNotFoundError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyDestination", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyDestinationError{})
		var want *transport.BackupEmptyDestinationError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EmptyID", func(t *testing.T) {
		got := translateBackupError(&service.BackupEmptyIDError{})
		var want *transport.BackupEmptyIDError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("CreateError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupCreateError{Err: inner})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("RestoreError", func(t *testing.T) {
		inner := errors.New("inner")
		got := translateBackupError(&service.BackupRestoreError{BackupID: "b1", Err: inner})
		var want *transport.BackupRestoreError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("ChecksumMismatch", func(t *testing.T) {
		got := translateBackupError(&service.BackupChecksumMismatchError{Expected: "a", Actual: "b"})
		var want *transport.BackupChecksumMismatchError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("EncryptionKeyRequired", func(t *testing.T) {
		got := translateBackupError(&service.BackupEncryptionKeyRequiredError{})
		var want *transport.BackupCreateError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
	})
	t.Run("DecryptionFailed", func(t *testing.T) {
		got := translateBackupError(&service.BackupDecryptionFailedError{BackupID: "b1", Err: errors.New("wrong key")})
		var want *transport.BackupDecryptionFailedError
		if !errors.As(got, &want) {
			t.Fatalf("got %T", got)
		}
		if want.BackupID != "b1" {
			t.Fatalf("BackupID = %q, want %q", want.BackupID, "b1")
		}
	})
	t.Run("Passthrough", func(t *testing.T) {
		want := errors.New("other")
		got := translateBackupError(want)
		if !errors.Is(got, want) {
			t.Fatalf("expected passthrough, got %v", got)
		}
	})
}
