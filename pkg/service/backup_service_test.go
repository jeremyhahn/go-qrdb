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
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock snapshot provider
// ---------------------------------------------------------------------------

type mockSnapshotProvider struct {
	shards       map[uint64][]byte
	restoreErr   error
	snapshotErr  error
	availableErr error
	restoreCalls atomic.Int32
}

func newMockSnapshotProvider() *mockSnapshotProvider {
	return &mockSnapshotProvider{
		shards: map[uint64][]byte{
			1: []byte("shard-1-data"),
			2: []byte("shard-2-data"),
			3: []byte("shard-3-data"),
		},
	}
}

func (m *mockSnapshotProvider) AvailableShards(_ context.Context) ([]uint64, error) {
	if m.availableErr != nil {
		return nil, m.availableErr
	}
	ids := make([]uint64, 0, len(m.shards))
	for id := range m.shards {
		ids = append(ids, id)
	}
	return ids, nil
}

func (m *mockSnapshotProvider) SnapshotShard(_ context.Context, shardID uint64) ([]byte, error) {
	if m.snapshotErr != nil {
		return nil, m.snapshotErr
	}
	data, ok := m.shards[shardID]
	if !ok {
		return nil, errors.New("shard not found")
	}
	return data, nil
}

func (m *mockSnapshotProvider) RestoreShard(_ context.Context, _ uint64, _ []byte) error {
	m.restoreCalls.Add(1)
	return m.restoreErr
}

// mockOnlineSnapshotProvider extends mockSnapshotProvider with
// OnlineShardRestorer, so BackupService.RestoreBackup's per-shard loop
// prefers RestoreShardOnline over the plain RestoreShard.
type mockOnlineSnapshotProvider struct {
	mockSnapshotProvider
	onlineErr   error
	onlineCalls atomic.Int32
}

func newMockOnlineSnapshotProvider() *mockOnlineSnapshotProvider {
	return &mockOnlineSnapshotProvider{mockSnapshotProvider: *newMockSnapshotProvider()}
}

func (m *mockOnlineSnapshotProvider) RestoreShardOnline(_ context.Context, _ uint64, _ []byte) error {
	m.onlineCalls.Add(1)
	return m.onlineErr
}

var _ OnlineShardRestorer = (*mockOnlineSnapshotProvider)(nil)

// ---------------------------------------------------------------------------
// CreateBackup tests
// ---------------------------------------------------------------------------

func TestBackupService_CreateBackup_Success(t *testing.T) {
	provider := newMockSnapshotProvider()
	idCounter := 0
	svc := NewBackupService(provider, WithIDGenerator(func() string {
		idCounter++
		return "backup-" + string(rune('0'+idCounter))
	}))

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/daily",
		Encryption:  EncryptionNone,
		Metadata:    map[string]string{"env": "test"},
	})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}
	if info.ID == "" {
		t.Fatal("expected non-empty backup ID")
	}
	if info.Status != BackupStatusComplete {
		t.Fatalf("expected status complete, got %s", info.Status)
	}
	if info.ShardCount != 3 {
		t.Fatalf("expected 3 shards, got %d", info.ShardCount)
	}
	if info.Size == 0 {
		t.Fatal("expected non-zero backup size")
	}
	if info.Checksum == "" {
		t.Fatal("expected non-empty checksum")
	}
	if info.Destination != "/backups/daily" {
		t.Fatalf("expected destination /backups/daily, got %s", info.Destination)
	}
	if info.Compression != CompressionNone {
		t.Fatalf("expected compression none, got %s", info.Compression)
	}
	if info.Encryption != EncryptionNone {
		t.Fatalf("expected encryption none, got %s", info.Encryption)
	}
	if info.Metadata["env"] != "test" {
		t.Fatalf("expected metadata env=test, got %s", info.Metadata["env"])
	}
	if info.Duration == 0 {
		t.Fatal("expected non-zero duration")
	}
}

func TestBackupService_CreateBackup_EmptyDestination(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.CreateBackup(ctx, BackupOptions{})
	if err == nil {
		t.Fatal("expected error for empty destination")
	}
	var empty *BackupEmptyDestinationError
	if !errors.As(err, &empty) {
		t.Fatalf("expected BackupEmptyDestinationError, got %T: %v", err, err)
	}
}

func TestBackupService_CreateBackup_InvalidCompression(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/test",
		Compression: "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid compression")
	}
	var invalidComp *BackupInvalidCompressionError
	if !errors.As(err, &invalidComp) {
		t.Fatalf("expected BackupInvalidCompressionError, got %T: %v", err, err)
	}
}

func TestBackupService_CreateBackup_InvalidEncryption(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/test",
		Encryption:  "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid encryption")
	}
	var invalidEnc *BackupInvalidEncryptionError
	if !errors.As(err, &invalidEnc) {
		t.Fatalf("expected BackupInvalidEncryptionError, got %T: %v", err, err)
	}
}

func TestBackupService_CreateBackup_EncryptionWithoutKey(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/test",
		Encryption:  EncryptionAES256,
	})
	if err == nil {
		t.Fatal("expected error for encryption without key")
	}
	var keyRequired *BackupEncryptionKeyRequiredError
	if !errors.As(err, &keyRequired) {
		t.Fatalf("expected BackupEncryptionKeyRequiredError, got %T: %v", err, err)
	}
}

func TestBackupService_CreateBackup_WithEncryption(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination:   "/backups/encrypted",
		Encryption:    EncryptionAES256,
		EncryptionKey: []byte("secret-key-32-bytes-long-12345!"),
	})
	if err != nil {
		t.Fatalf("CreateBackup with encryption returned error: %v", err)
	}
	if info.Encryption != EncryptionAES256 {
		t.Fatalf("expected encryption aes256, got %s", info.Encryption)
	}
}

func TestBackupService_CreateBackup_WithCompression(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/compressed",
		Compression: CompressionZstd,
		Encryption:  EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup with compression returned error: %v", err)
	}
	if info.Compression != CompressionZstd {
		t.Fatalf("expected compression zstd, got %s", info.Compression)
	}
}

func TestBackupService_CreateBackup_SpecificShards(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/partial",
		ShardIDs:    []uint64{1, 3},
		Encryption:  EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup with specific shards returned error: %v", err)
	}
	if info.ShardCount != 2 {
		t.Fatalf("expected 2 shards, got %d", info.ShardCount)
	}
}

func TestBackupService_CreateBackup_SnapshotError(t *testing.T) {
	provider := newMockSnapshotProvider()
	provider.snapshotErr = errors.New("snapshot failed")
	svc := NewBackupService(provider)

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/fail",
		Encryption:  EncryptionNone,
	})
	if err == nil {
		t.Fatal("expected error when snapshot fails")
	}
	var createErr *BackupCreateError
	if !errors.As(err, &createErr) {
		t.Fatalf("expected BackupCreateError, got %T: %v", err, err)
	}
	if info.Status != BackupStatusFailed {
		t.Fatalf("expected status failed, got %s", info.Status)
	}
}

func TestBackupService_CreateBackup_AvailableShardsError(t *testing.T) {
	provider := newMockSnapshotProvider()
	provider.availableErr = errors.New("cannot list shards")
	svc := NewBackupService(provider)

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/fail",
		Encryption:  EncryptionNone,
	})
	if err == nil {
		t.Fatal("expected error when AvailableShards fails")
	}
	var createErr *BackupCreateError
	if !errors.As(err, &createErr) {
		t.Fatalf("expected BackupCreateError, got %T: %v", err, err)
	}
	if info.Status != BackupStatusFailed {
		t.Fatalf("expected status failed, got %s", info.Status)
	}
}

func TestBackupService_CreateBackup_CanceledContext(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/canceled",
		Encryption:  EncryptionNone,
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	var createErr *BackupCreateError
	if !errors.As(err, &createErr) {
		t.Fatalf("expected BackupCreateError, got %T: %v", err, err)
	}
	if info.Status != BackupStatusFailed {
		t.Fatalf("expected status failed, got %s", info.Status)
	}
}

// ---------------------------------------------------------------------------
// ListBackups tests
// ---------------------------------------------------------------------------

func TestBackupService_ListBackups_Empty(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	backups, err := svc.ListBackups(ctx)
	if err != nil {
		t.Fatalf("ListBackups returned error: %v", err)
	}
	if len(backups) != 0 {
		t.Fatalf("expected 0 backups, got %d", len(backups))
	}
}

func TestBackupService_ListBackups_Ordered(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()

	_, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/first", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("first CreateBackup returned error: %v", err)
	}
	_, err = svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/second", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("second CreateBackup returned error: %v", err)
	}

	backups, err := svc.ListBackups(ctx)
	if err != nil {
		t.Fatalf("ListBackups returned error: %v", err)
	}
	if len(backups) != 2 {
		t.Fatalf("expected 2 backups, got %d", len(backups))
	}

	// Newest first.
	if backups[0].Timestamp.Before(backups[1].Timestamp) {
		t.Fatal("expected backups ordered newest first")
	}
}

// ---------------------------------------------------------------------------
// GetBackup tests
// ---------------------------------------------------------------------------

func TestBackupService_GetBackup_Success(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/test", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	info, err := svc.GetBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetBackup returned error: %v", err)
	}
	if info.ID != created.ID {
		t.Fatalf("expected ID %s, got %s", created.ID, info.ID)
	}
}

func TestBackupService_GetBackup_NotFound(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.GetBackup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent backup")
	}
	var notFound *BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestBackupService_GetBackup_EmptyID(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.GetBackup(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var emptyID *BackupEmptyIDError
	if !errors.As(err, &emptyID) {
		t.Fatalf("expected BackupEmptyIDError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// DeleteBackup tests
// ---------------------------------------------------------------------------

func TestBackupService_DeleteBackup_Success(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/delete-me", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	err = svc.DeleteBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("DeleteBackup returned error: %v", err)
	}

	_, err = svc.GetBackup(ctx, created.ID)
	var notFound *BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError after delete, got %T: %v", err, err)
	}
}

func TestBackupService_DeleteBackup_NotFound(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	err := svc.DeleteBackup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent backup")
	}
	var notFound *BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestBackupService_DeleteBackup_EmptyID(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	err := svc.DeleteBackup(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var emptyID *BackupEmptyIDError
	if !errors.As(err, &emptyID) {
		t.Fatalf("expected BackupEmptyIDError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// RestoreBackup tests
// ---------------------------------------------------------------------------

func TestBackupService_RestoreBackup_Success(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/restore-test", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID: created.ID,
	})
	if err != nil {
		t.Fatalf("RestoreBackup returned error: %v", err)
	}
	if result.BackupID != created.ID {
		t.Fatalf("expected BackupID %s, got %s", created.ID, result.BackupID)
	}
	if result.ShardsRestored != 3 {
		t.Fatalf("expected 3 shards restored, got %d", result.ShardsRestored)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected 0 errors, got %d", len(result.Errors))
	}
	if result.Duration == 0 {
		t.Fatal("expected non-zero duration")
	}
}

func TestBackupService_RestoreBackup_TargetShards(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/partial-restore", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:     created.ID,
		TargetShards: []uint64{1},
	})
	if err != nil {
		t.Fatalf("RestoreBackup returned error: %v", err)
	}
	if result.ShardsRestored != 1 {
		t.Fatalf("expected 1 shard restored, got %d", result.ShardsRestored)
	}
}

func TestBackupService_RestoreBackup_NotFound(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID: "nonexistent",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent backup")
	}
	var notFound *BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestBackupService_RestoreBackup_EmptyID(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.RestoreBackup(ctx, RestoreOptions{})
	if err == nil {
		t.Fatal("expected error for empty backup ID")
	}
	var emptyID *BackupEmptyIDError
	if !errors.As(err, &emptyID) {
		t.Fatalf("expected BackupEmptyIDError, got %T: %v", err, err)
	}
}

func TestBackupService_RestoreBackup_WithValidation(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/validate", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:      created.ID,
		ValidateFirst: true,
	})
	if err != nil {
		t.Fatalf("RestoreBackup with validation returned error: %v", err)
	}
	if result.ShardsRestored != 3 {
		t.Fatalf("expected 3 shards restored, got %d", result.ShardsRestored)
	}
}

func TestBackupService_RestoreBackup_RestoreError(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/restore-fail", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	provider.restoreErr = errors.New("restore failed")
	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID: created.ID,
	})
	if err != nil {
		t.Fatalf("RestoreBackup should not return error for per-shard failures: %v", err)
	}
	if result.ShardsRestored != 0 {
		t.Fatalf("expected 0 shards restored, got %d", result.ShardsRestored)
	}
	if len(result.Errors) != 3 {
		t.Fatalf("expected 3 shard errors, got %d", len(result.Errors))
	}
}

// TestBackupService_RestoreBackup_PrefersOnlineShardRestorer verifies that
// RestoreBackup calls RestoreShardOnline (not RestoreShard) whenever the
// configured provider implements OnlineShardRestorer.
func TestBackupService_RestoreBackup_PrefersOnlineShardRestorer(t *testing.T) {
	provider := newMockOnlineSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/online", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err != nil {
		t.Fatalf("RestoreBackup returned error: %v", err)
	}
	if result.ShardsRestored != 3 {
		t.Fatalf("expected 3 shards restored, got %d", result.ShardsRestored)
	}
	if provider.onlineCalls.Load() != 3 {
		t.Fatalf("expected RestoreShardOnline called 3 times, got %d", provider.onlineCalls.Load())
	}
	if provider.restoreCalls.Load() != 0 {
		t.Fatalf("expected the plain RestoreShard to never be called, got %d calls", provider.restoreCalls.Load())
	}
}

// TestBackupService_RestoreBackup_OnlineShardRestorerError verifies that a
// RestoreShardOnline failure (e.g. one of the guard errors) is embedded as a
// per-shard ShardError rather than aborting the whole restore, matching the
// plain RestoreShard failure-handling contract.
func TestBackupService_RestoreBackup_OnlineShardRestorerError(t *testing.T) {
	provider := newMockOnlineSnapshotProvider()
	provider.onlineErr = &OnlineRestoreSystemShardError{ShardID: 1}
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/online-fail", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err != nil {
		t.Fatalf("RestoreBackup should not return a top-level error for per-shard guard failures: %v", err)
	}
	if result.ShardsRestored != 0 {
		t.Fatalf("expected 0 shards restored, got %d", result.ShardsRestored)
	}
	if len(result.Errors) != 3 {
		t.Fatalf("expected 3 shard errors, got %d", len(result.Errors))
	}
	for _, se := range result.Errors {
		if se.Message == "" {
			t.Fatalf("expected a non-empty guard error message for shard %d", se.ShardID)
		}
	}
}

func TestBackupService_RestoreBackup_ShardNotInBackup(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/partial", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:     created.ID,
		TargetShards: []uint64{999},
	})
	if err != nil {
		t.Fatalf("RestoreBackup should not return error for missing shard: %v", err)
	}
	if result.ShardsRestored != 0 {
		t.Fatalf("expected 0 shards restored, got %d", result.ShardsRestored)
	}
	if len(result.Errors) != 1 {
		t.Fatalf("expected 1 shard error, got %d", len(result.Errors))
	}
	if result.Errors[0].ShardID != 999 {
		t.Fatalf("expected shard error for ID 999, got %d", result.Errors[0].ShardID)
	}
}

func TestBackupService_RestoreBackup_DecryptionKeyRequired(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination:   "/backups/encrypted",
		Encryption:    EncryptionAES256,
		EncryptionKey: []byte("secret-key-32-bytes-long-12345!"),
	})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	_, err = svc.RestoreBackup(ctx, RestoreOptions{
		BackupID: created.ID,
	})
	if err == nil {
		t.Fatal("expected error when decryption key is missing")
	}
	var dkr *BackupDecryptionKeyRequiredError
	if !errors.As(err, &dkr) {
		t.Fatalf("expected BackupDecryptionKeyRequiredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// VerifyBackup tests
// ---------------------------------------------------------------------------

func TestBackupService_VerifyBackup_Success(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/verify", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.VerifyBackup(ctx, created.ID)
	if err != nil {
		t.Fatalf("VerifyBackup returned error: %v", err)
	}
	if !result.Valid {
		t.Fatal("expected valid backup")
	}
	if !result.ChecksumMatch {
		t.Fatal("expected checksum match")
	}
	if result.ExpectedChecksum != result.ActualChecksum {
		t.Fatalf("expected checksums to match: %s vs %s",
			result.ExpectedChecksum, result.ActualChecksum)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected 0 errors, got %d: %v", len(result.Errors), result.Errors)
	}
}

func TestBackupService_VerifyBackup_NotFound(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.VerifyBackup(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent backup")
	}
	var notFound *BackupNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("expected BackupNotFoundError, got %T: %v", err, err)
	}
}

func TestBackupService_VerifyBackup_EmptyID(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := NewBackupService(provider)

	ctx := context.Background()
	_, err := svc.VerifyBackup(ctx, "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
	var emptyID *BackupEmptyIDError
	if !errors.As(err, &emptyID) {
		t.Fatalf("expected BackupEmptyIDError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Error message tests
// ---------------------------------------------------------------------------

func TestBackupErrors_Messages(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "BackupNotFoundError",
			err:      &BackupNotFoundError{ID: "abc123"},
			expected: "service: backup not found: abc123",
		},
		{
			name:     "BackupEmptyDestinationError",
			err:      &BackupEmptyDestinationError{},
			expected: "service: backup destination must not be empty",
		},
		{
			name:     "BackupEmptyIDError",
			err:      &BackupEmptyIDError{},
			expected: "service: backup ID must not be empty",
		},
		{
			name:     "BackupEncryptionKeyRequiredError",
			err:      &BackupEncryptionKeyRequiredError{},
			expected: "service: encryption key is required when encryption is enabled",
		},
		{
			name:     "BackupDecryptionKeyRequiredError",
			err:      &BackupDecryptionKeyRequiredError{},
			expected: "service: decryption key is required for encrypted backups",
		},
		{
			name:     "BackupCreateError",
			err:      &BackupCreateError{Err: errors.New("disk full")},
			expected: "service: backup creation failed: disk full",
		},
		{
			name:     "BackupRestoreError",
			err:      &BackupRestoreError{BackupID: "abc", Err: errors.New("corrupted")},
			expected: "service: backup restore failed for abc: corrupted",
		},
		{
			name:     "BackupChecksumMismatchError",
			err:      &BackupChecksumMismatchError{Expected: "aaa", Actual: "bbb"},
			expected: "service: backup checksum mismatch: expected aaa, got bbb",
		},
		{
			name:     "BackupInvalidCompressionError",
			err:      &BackupInvalidCompressionError{Compression: "lz77"},
			expected: "service: invalid backup compression type: lz77",
		},
		{
			name:     "BackupInvalidEncryptionError",
			err:      &BackupInvalidEncryptionError{Encryption: "rot13"},
			expected: "service: invalid backup encryption type: rot13",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.expected {
				t.Fatalf("expected %q, got %q", tt.expected, got)
			}
		})
	}
}

func TestBackupErrors_Unwrap(t *testing.T) {
	inner := errors.New("inner error")

	tests := []struct {
		name string
		err  error
	}{
		{name: "BackupCreateError", err: &BackupCreateError{Err: inner}},
		{name: "BackupRestoreError", err: &BackupRestoreError{BackupID: "x", Err: inner}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, inner) {
				t.Fatalf("expected Unwrap to return inner error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SetShardRestarter tests
// ---------------------------------------------------------------------------

// TestSetShardRestarter_EnablesOnlineRestoreOnHostBackedProvider proves the
// end-to-end late-binding wiring path the daemon and SDK node builders use:
// NewBackupService(NewHostShardSnapshotProvider(host)) is constructed first
// (as it is at lifecycle.BuildServices time, before the IsolationRaft
// factory exists), and SetShardRestarter is called afterward once the
// restarter exists — exactly mirroring cmd/qrdbd/start.go and
// sdk/go/node/node.go.
func TestSetShardRestarter_EnablesOnlineRestoreOnHostBackedProvider(t *testing.T) {
	host := newMockSnapshotHost()
	provider := NewHostShardSnapshotProvider(host)
	svc := NewBackupService(provider)

	restarter := &fakeShardRestarter{}
	SetShardRestarter(svc, restarter)

	ctx := context.Background()

	// A direct RestoreShardOnline call (as RestoreBackup would make once a
	// backup exists) must now route through the wired restarter.
	online, ok := provider.(OnlineShardRestorer)
	if !ok {
		t.Fatal("expected the host-backed provider to implement OnlineShardRestorer")
	}
	if err := online.RestoreShardOnline(ctx, 1000, []byte("data")); err != nil {
		t.Fatalf("RestoreShardOnline returned error: %v", err)
	}
	if restarter.stopCalls.Load() != 1 || restarter.restartCalls.Load() != 1 {
		t.Fatalf("expected the wired restarter to be used (stop=%d, restart=%d)",
			restarter.stopCalls.Load(), restarter.restartCalls.Load())
	}
}

// TestSetShardRestarter_NoopForNonBackupService verifies SetShardRestarter
// tolerates a BackupServicer implementation other than *BackupService (e.g.
// a test double) without panicking.
func TestSetShardRestarter_NoopForNonBackupService(t *testing.T) {
	var svc BackupServicer = &noopBackupServicer{}
	SetShardRestarter(svc, &fakeShardRestarter{})
}

// TestSetShardRestarter_NoopForNonHostBackedProvider verifies
// SetShardRestarter tolerates a *BackupService whose provider is not the
// host-backed hostShardSnapshotProvider (e.g. mockSnapshotProvider in tests).
func TestSetShardRestarter_NoopForNonHostBackedProvider(t *testing.T) {
	svc := NewBackupService(newMockSnapshotProvider())
	SetShardRestarter(svc, &fakeShardRestarter{})
}

// TestSetShardRestarter_NilRestarterIsNoop verifies a nil restarter never
// clears an already-wired one.
func TestSetShardRestarter_NilRestarterIsNoop(t *testing.T) {
	host := newMockSnapshotHost()
	provider := NewHostShardSnapshotProvider(host)
	svc := NewBackupService(provider)

	restarter := &fakeShardRestarter{}
	SetShardRestarter(svc, restarter)
	SetShardRestarter(svc, nil)

	online := provider.(OnlineShardRestorer)
	if err := online.RestoreShardOnline(context.Background(), 1000, []byte("data")); err != nil {
		t.Fatalf("RestoreShardOnline returned error: %v", err)
	}
	if restarter.stopCalls.Load() != 1 {
		t.Fatalf("expected the previously wired restarter to remain wired after a nil SetShardRestarter call, got %d stop calls", restarter.stopCalls.Load())
	}
}

// noopBackupServicer is a minimal BackupServicer double (not *BackupService)
// used to prove SetShardRestarter degrades to a no-op rather than panicking.
type noopBackupServicer struct{}

func (*noopBackupServicer) CreateBackup(context.Context, BackupOptions) (*BackupInfo, error) {
	return nil, nil
}
func (*noopBackupServicer) ListBackups(context.Context) ([]BackupInfo, error) { return nil, nil }
func (*noopBackupServicer) GetBackup(context.Context, string) (*BackupInfo, error) {
	return nil, nil
}
func (*noopBackupServicer) DeleteBackup(context.Context, string) error { return nil }
func (*noopBackupServicer) RestoreBackup(context.Context, RestoreOptions) (*RestoreResult, error) {
	return nil, nil
}
func (*noopBackupServicer) VerifyBackup(context.Context, string) (*VerifyResult, error) {
	return nil, nil
}

var _ BackupServicer = (*noopBackupServicer)(nil)
