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
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/audit"
	"github.com/jeremyhahn/go-qrdb/pkg/keywrap"
)

// sharedKeyDir is the one directory every test's software-provider key
// files live in for this test binary's run (TestMain creates/removes it).
// SoftwareProvider confines every key file to ONE configured directory
// (go-qrdb security review, backup-encrypt-default #B4): KeyRef and
// DecryptionKeyRef are now bare file NAMES inside it, never paths, so
// every test in this file shares the directory via mustNewBackupService's
// automatic WithKeyDir rather than minting its own.
var sharedKeyDir string

// TestMain creates sharedKeyDir once for this package's test binary and
// removes it on exit. Safe to own here: this is pkg/service's only
// TestMain, and every other test file's behavior is unaffected by it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "qrdb-backup-keytest-")
	if err != nil {
		panic(err)
	}
	sharedKeyDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// mustNewBackupService calls NewBackupService, failing t on error, and
// always applies WithKeyDir(sharedKeyDir) in addition to opts (applied
// first, so an opts entry that supplies its own WithKeyWrapRegistry/
// WithKeyWrapProvider is unaffected -- WithKeyDir only matters to the
// DEFAULT registry's lazily-constructed "software" provider).
func mustNewBackupService(t *testing.T, provider ShardSnapshotProvider, opts ...BackupServiceOption) BackupServicer {
	t.Helper()
	allOpts := make([]BackupServiceOption, 0, len(opts)+1)
	allOpts = append(allOpts, WithKeyDir(sharedKeyDir))
	allOpts = append(allOpts, opts...)
	svc, err := NewBackupService(provider, allOpts...)
	if err != nil {
		t.Fatalf("NewBackupService: %v", err)
	}
	return svc
}

// writeTestKeyFile writes a fresh 32-byte key file into sharedKeyDir and
// returns its bare NAME, suitable for use as BackupOptions.KeyRef /
// RestoreOptions.DecryptionKeyRef against the "software" keywrap provider
// (go-qrdb security review, backup-encrypt-default #B4: KeyRef is a name
// inside the configured key directory, never a path).
func writeTestKeyFile(t *testing.T, fill byte) string {
	t.Helper()
	name := "key-" + strconv.Itoa(int(fill)) + ".key"
	data := make([]byte, 32)
	for i := range data {
		data[i] = fill + byte(i)
	}
	if err := os.WriteFile(filepath.Join(sharedKeyDir, name), data, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return name
}

// ---------------------------------------------------------------------------
// Mock snapshot provider
// ---------------------------------------------------------------------------

type mockSnapshotProvider struct {
	shards        map[uint64][]byte
	restoreErr    error
	restoreErrFor map[uint64]error // per-shard override, checked before restoreErr
	snapshotErr   error
	availableErr  error
	restoreCalls  atomic.Int32
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

func (m *mockSnapshotProvider) RestoreShard(_ context.Context, shardID uint64, _ []byte) error {
	m.restoreCalls.Add(1)
	if err, ok := m.restoreErrFor[shardID]; ok {
		return err
	}
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
	svc := mustNewBackupService(t, provider, WithIDGenerator(func() string {
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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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

// TestBackupService_CreateBackup_DefaultEncryptionWithoutKey is the
// OWNER-RULING conformance scenario "default with no key is refused":
// BackupOptions.Encryption left entirely EMPTY (never asked for encryption
// at all) and no key supplied must be refused with the typed error that
// names BOTH ways out, distinct from BackupEncryptionKeyRequiredError
// (which fires only when aes256 was named explicitly).
func TestBackupService_CreateBackup_DefaultEncryptionWithoutKey(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	_, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/test",
	})
	if err == nil {
		t.Fatal("expected error for the default encryption with no key")
	}
	var required *BackupEncryptionRequiredError
	if !errors.As(err, &required) {
		t.Fatalf("expected BackupEncryptionRequiredError, got %T: %v", err, err)
	}
}

func TestBackupService_CreateBackup_WithEncryption(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	keyPath := writeTestKeyFile(t, 0x01)

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/encrypted",
		Encryption:  EncryptionAES256,
		KeyRef:      keyPath,
	})
	if err != nil {
		t.Fatalf("CreateBackup with encryption returned error: %v", err)
	}
	if info.Encryption != EncryptionAES256 {
		t.Fatalf("expected encryption aes256, got %s", info.Encryption)
	}
	if info.KeyProviderID != keywrap.SoftwareProviderID {
		t.Fatalf("KeyProviderID = %q, want %q", info.KeyProviderID, keywrap.SoftwareProviderID)
	}
}

// TestBackupService_CreateBackup_DefaultEncryptionWithKeyRestores is the
// OWNER-RULING conformance scenario "default with a key produces an
// encrypted backup that restores": BackupOptions.Encryption left entirely
// EMPTY (the default applies) with a key supplied succeeds, records
// EncryptionAES256 (not merely "not none"), and the backup restores
// automatically -- RestoreOptions needs no key at all, because the
// envelope stores the key reference used at create time and the service
// resolves it itself.
func TestBackupService_CreateBackup_DefaultEncryptionWithKeyRestores(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()
	keyPath := writeTestKeyFile(t, 0x02)

	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/default-encrypted",
		KeyRef:      keyPath,
	})
	if err != nil {
		t.Fatalf("CreateBackup with the default encryption and a key: %v", err)
	}
	if created.Encryption != EncryptionAES256 {
		t.Fatalf("Encryption = %q, want %q: the default must resolve to aes256, not merely survive validation", created.Encryption, EncryptionAES256)
	}

	// Restoring with no override at all succeeds: the envelope's own
	// stored key reference is used automatically.
	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err != nil {
		t.Fatalf("RestoreBackup using the stored key reference: %v", err)
	}
	if result.ShardsRestored == 0 {
		t.Fatal("RestoreBackup restored 0 shards, want the backup's full shard set")
	}
}

func TestBackupService_CreateBackup_WithCompression(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/restore-test", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:       created.ID,
		AllowPlaintext: true,
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
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/partial-restore", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:       created.ID,
		TargetShards:   []uint64{1},
		AllowPlaintext: true,
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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/validate", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:       created.ID,
		ValidateFirst:  true,
		AllowPlaintext: true,
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
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/restore-fail", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	provider.restoreErr = errors.New("restore failed")
	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:       created.ID,
		AllowPlaintext: true,
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
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/online", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID, AllowPlaintext: true})
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
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/online-fail", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID, AllowPlaintext: true})
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

// TestBackupService_RestoreBackup_ShardNotInBackup proves a request for a
// shard this backup never captured is refused as a single, TOP-LEVEL
// *BackupVerificationFailedError wrapping *BackupShardNotFoundError --
// nothing is imported, including shards that WOULD have succeeded had
// they been requested (go-qrdb security review, backup-encrypt-default
// #B1: every target shard is verified before any is imported, so a
// missing one aborts the whole restore rather than reporting a tolerated
// per-shard error).
func TestBackupService_RestoreBackup_ShardNotInBackup(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/partial", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	result, err := svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:       created.ID,
		TargetShards:   []uint64{999},
		AllowPlaintext: true,
	})
	if err == nil {
		t.Fatal("RestoreBackup should return a top-level error for a target shard not in the backup")
	}
	if result != nil {
		t.Fatalf("RestoreBackup should return a nil result alongside a top-level error, got %+v", result)
	}
	var verifyErr *BackupVerificationFailedError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("err = %T, want *BackupVerificationFailedError", err)
	}
	if verifyErr.ShardID != 999 {
		t.Fatalf("verifyErr.ShardID = %d, want 999", verifyErr.ShardID)
	}
	var notFound *BackupShardNotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want it to wrap *BackupShardNotFoundError", err)
	}
	if provider.restoreCalls.Load() != 0 {
		t.Error("a refused restore must not have touched any shard")
	}
}

// TestBackupService_RestoreBackup_WrongKeyFileRefused proves a restore
// naming a DecryptionKeyRef override pointing at the WRONG key file is
// refused with the typed, fail-closed *BackupDecryptionFailedError -- not
// silently restoring garbage, and not distinguishing "wrong key" from
// "tampered" in what it returns.
func TestBackupService_RestoreBackup_WrongKeyFileRefused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	ctx := context.Background()
	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/encrypted",
		Encryption:  EncryptionAES256,
		KeyRef:      writeTestKeyFile(t, 0x10),
	})
	if err != nil {
		t.Fatalf("CreateBackup returned error: %v", err)
	}

	_, err = svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:         created.ID,
		DecryptionKeyRef: writeTestKeyFile(t, 0x20), // a DIFFERENT 32-byte key
	})
	if err == nil {
		t.Fatal("expected error when the wrong key file is supplied")
	}
	var decFailed *BackupDecryptionFailedError
	if !errors.As(err, &decFailed) {
		t.Fatalf("expected BackupDecryptionFailedError, got %T: %v", err, err)
	}
}

// TestBackupService_RestoreBackup_UnregisteredProviderRefused proves a
// backup whose envelope names a KeyProviderID this node's registry does
// not have registered is refused with *BackupDecryptionFailedError, the
// same typed error an Unwrap failure gets -- an environment/deployment
// problem, not a wrong-key one, but given the same fail-closed refusal
// rather than a distinguishable one.
func TestBackupService_RestoreBackup_UnregisteredProviderRefused(t *testing.T) {
	provider := newMockSnapshotProvider()

	registry := keywrap.NewRegistry() // deliberately empty -- no "software"
	svc := mustNewBackupService(t, provider, WithKeyWrapRegistry(registry))

	ctx := context.Background()
	_, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/no-provider",
		Encryption:  EncryptionAES256,
		KeyRef:      writeTestKeyFile(t, 0x30),
	})
	if err == nil {
		t.Fatal("expected error when the registry has no provider registered")
	}
	var decFailed *BackupDecryptionFailedError
	if !errors.As(err, &decFailed) {
		t.Fatalf("expected BackupDecryptionFailedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// VerifyBackup tests
// ---------------------------------------------------------------------------

func TestBackupService_VerifyBackup_Success(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, provider)

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
			name:     "BackupDecryptionFailedError",
			err:      &BackupDecryptionFailedError{BackupID: "abc", Err: errors.New("wrong key")},
			expected: "service: backup decryption failed for abc: wrong key",
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
		{name: "BackupDecryptionFailedError", err: &BackupDecryptionFailedError{BackupID: "x", Err: inner}},
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
	svc := mustNewBackupService(t, provider)

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
	svc := mustNewBackupService(t, newMockSnapshotProvider())
	SetShardRestarter(svc, &fakeShardRestarter{})
}

// TestSetShardRestarter_NilRestarterIsNoop verifies a nil restarter never
// clears an already-wired one.
func TestSetShardRestarter_NilRestarterIsNoop(t *testing.T) {
	host := newMockSnapshotHost()
	provider := NewHostShardSnapshotProvider(host)
	svc := mustNewBackupService(t, provider)

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

// ---------------------------------------------------------------------------
// Explicit-plaintext warn/audit (OWNER-RULING conformance scenario 3)
// ---------------------------------------------------------------------------

// TestBackupService_CreateBackup_ExplicitNoneAudits is the OWNER-RULING
// conformance scenario "explicit none produces plaintext and warns or
// audits": an explicit EncryptionNone choice must leave an audit record
// when an audit logger is configured, distinct from the WARN log line
// every such backup also gets regardless (that line is exercised
// incidentally by every other EncryptionNone test in this file; this one
// isolates the audit trail specifically).
func TestBackupService_CreateBackup_ExplicitNoneAudits(t *testing.T) {
	ring := audit.NewRingBuffer(0)
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider, WithAuditLogger(ring))

	ctx := context.Background()
	info, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/plaintext-audited",
		Encryption:  EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup with explicit EncryptionNone: %v", err)
	}

	// Two records, in order: a GATING "permitted" record BEFORE the backup
	// ran, then a post-hoc "success" record carrying the real outcome
	// (go-qrdb security review, backup-encrypt-default #B5) -- never a
	// single "success" record emitted before the backup had actually
	// happened.
	events := ring.Snapshot()
	if len(events) != 2 {
		t.Fatalf("audit log has %d events, want exactly 2 (gate + settle) for the one explicit-plaintext backup", len(events))
	}
	gate, settle := events[0], events[1]
	for _, ev := range []audit.AuditEvent{gate, settle} {
		if ev.Action != "backup.create" {
			t.Fatalf("audit event Action = %q, want %q", ev.Action, "backup.create")
		}
		// The audit record is emitted AFTER the backup has its ID and
		// carries it (go-qrdb security review, backup-encrypt-default
		// #B5) -- the resource names the backup, not merely its
		// destination.
		if ev.Resource != "backup:"+info.ID {
			t.Fatalf("audit event Resource = %q, want %q", ev.Resource, "backup:"+info.ID)
		}
		if plaintext, _ := ev.Attributes["plaintext"].(bool); !plaintext {
			t.Fatalf("audit event Attributes[plaintext] = %v, want true", ev.Attributes["plaintext"])
		}
		if encAttr, _ := ev.Attributes["encryption"].(string); encAttr != string(EncryptionNone) {
			t.Fatalf("audit event Attributes[encryption] = %v, want %q", ev.Attributes["encryption"], EncryptionNone)
		}
		if dest, _ := ev.Attributes["destination"].(string); dest != info.Destination {
			t.Fatalf("audit event Attributes[destination] = %v, want %q", ev.Attributes["destination"], info.Destination)
		}
	}
	if gate.Outcome != audit.OutcomePermitted {
		t.Fatalf("gate event Outcome = %q, want %q", gate.Outcome, audit.OutcomePermitted)
	}
	if settle.Outcome != audit.OutcomeSuccess {
		t.Fatalf("settle event Outcome = %q, want %q", settle.Outcome, audit.OutcomeSuccess)
	}
}

// TestBackupService_CreateBackup_EncryptedDoesNotAudit proves the audit
// record is specific to the explicit-plaintext choice: a backup using the
// default (encrypted) path must not emit one.
func TestBackupService_CreateBackup_EncryptedDoesNotAudit(t *testing.T) {
	ring := audit.NewRingBuffer(0)
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider, WithAuditLogger(ring))

	ctx := context.Background()
	if _, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/encrypted-not-audited",
		KeyRef:      writeTestKeyFile(t, 0x40),
	}); err != nil {
		t.Fatalf("CreateBackup with the default encryption and a key: %v", err)
	}

	if events := ring.Snapshot(); len(events) != 0 {
		t.Fatalf("audit log has %d events for an encrypted backup, want 0", len(events))
	}
}

// TestBackupService_CreateBackup_ExplicitNoneWithoutAuditLoggerStillSucceeds
// proves a nil audit logger (the default when WithAuditLogger is never
// applied) disables emission without affecting the backup itself -- the
// audit trail is an addition, never a precondition, for a surface with no
// compliance logger configured.
func TestBackupService_CreateBackup_ExplicitNoneWithoutAuditLoggerStillSucceeds(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)

	if _, err := svc.CreateBackup(context.Background(), BackupOptions{
		Destination: "/backups/plaintext-no-logger",
		Encryption:  EncryptionNone,
	}); err != nil {
		t.Fatalf("CreateBackup with explicit EncryptionNone and no audit logger configured: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Envelope encryption: the backup is actually encrypted, not merely labeled
// ---------------------------------------------------------------------------

// knownPlaintextFixtures are the shard payloads mockSnapshotProvider
// returns (see newMockSnapshotProvider): the "not the plaintext" assertions
// below check for their absence in stored bytes.
var knownPlaintextFixtures = [][]byte{
	[]byte("shard-1-data"),
	[]byte("shard-2-data"),
	[]byte("shard-3-data"),
}

// storedShardBytes reads a backup's raw stored shard bytes directly out of
// BackupService's in-memory registry, bypassing RestoreBackup entirely --
// exactly what "read the sink/registry's raw stored bytes directly" in
// docs/key-wrapping/engineering.md's test strategy calls for.
func storedShardBytes(t *testing.T, svc BackupServicer, id string) map[uint64][]byte {
	t.Helper()
	bs, ok := svc.(*BackupService)
	if !ok {
		t.Fatal("storedShardBytes: svc is not *BackupService")
	}
	bs.mu.RLock()
	entry, ok := bs.backups[id]
	bs.mu.RUnlock()
	if !ok {
		t.Fatalf("storedShardBytes: no entry for %q", id)
	}
	return entry.shards
}

// TestBackupService_EncryptedBackup_StoredBytesAreNotPlaintext proves an
// aes256 backup's persisted shard bytes are genuine ciphertext: none of the
// known plaintext fixtures mockSnapshotProvider hands CreateBackup appear
// anywhere in what is actually stored.
func TestBackupService_EncryptedBackup_StoredBytesAreNotPlaintext(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/ciphertext-check",
		KeyRef:      writeTestKeyFile(t, 0x50),
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	shards := storedShardBytes(t, svc, created.ID)
	if len(shards) == 0 {
		t.Fatal("expected at least one stored shard")
	}
	for shardID, stored := range shards {
		for _, fixture := range knownPlaintextFixtures {
			if bytes.Contains(stored, fixture) {
				t.Fatalf("shard %d's stored bytes contain the known plaintext fixture %q -- the backup was not actually encrypted", shardID, fixture)
			}
		}
	}

	// And it restores correctly with the matching key file.
	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if result.ShardsRestored != len(shards) {
		t.Fatalf("ShardsRestored = %d, want %d", result.ShardsRestored, len(shards))
	}
}

// TestBackupService_PlaintextBackup_StoredBytesArePlaintext is the negative
// control for the assertion above: an explicit EncryptionNone backup's
// stored bytes ARE exactly the plaintext fixtures, proving the "not the
// plaintext" check above is meaningful rather than vacuous.
func TestBackupService_PlaintextBackup_StoredBytesArePlaintext(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/plaintext-check",
		Encryption:  EncryptionNone,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	shards := storedShardBytes(t, svc, created.ID)
	for shardID, stored := range shards {
		found := false
		for _, fixture := range knownPlaintextFixtures {
			if bytes.Equal(stored, fixture) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("shard %d's stored bytes are not one of the known plaintext fixtures: %q", shardID, stored)
		}
	}
}

// TestBackupService_RestoreBackup_TamperedCiphertextGivesTypedError proves
// a single-bit flip in a stored shard's ciphertext is detected and the
// WHOLE restore aborts with a typed top-level error BEFORE any shard is
// imported -- not a per-shard ShardError that leaves every other shard
// already imported, which is exactly the defect go-qrdb's security review
// found here (backup-encrypt-default #B1): "a tampered shard 2 still
// leaves shard 1 imported with no top-level error."
func TestBackupService_RestoreBackup_TamperedCiphertextGivesTypedError(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/tamper-check",
		KeyRef:      writeTestKeyFile(t, 0x60),
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	shards := storedShardBytes(t, svc, created.ID)
	var tamperedShard uint64
	for id := range shards {
		tamperedShard = id
		break
	}
	shards[tamperedShard][len(shards[tamperedShard])-1] ^= 0xFF

	result, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err == nil {
		t.Fatal("RestoreBackup should return a top-level error for a tampered shard")
	}
	if result != nil {
		t.Fatalf("RestoreBackup should return a nil result alongside a top-level error, got %+v", result)
	}
	var verifyErr *BackupVerificationFailedError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("err = %T, want *BackupVerificationFailedError", err)
	}
	if verifyErr.ShardID != tamperedShard {
		t.Fatalf("verifyErr.ShardID = %d, want %d", verifyErr.ShardID, tamperedShard)
	}
	var integrityErr *BackupShardIntegrityError
	if !errors.As(err, &integrityErr) {
		t.Fatalf("err = %v, want it to wrap *BackupShardIntegrityError (not keywrap.UnwrapFailedError -- a shard tamper is not a wrong-KEK failure)", err)
	}
	if provider.restoreCalls.Load() != 0 {
		t.Fatal("a tampered shard must leave NOTHING imported, including shards that would otherwise have succeeded")
	}
}

// TestBackupService_RestoreBackup_UnregisteredProviderAtRestoreRefused
// proves a backup whose envelope names a provider this node's registry no
// longer has registered (e.g. an embedder removed it between create and
// restore) is refused with *BackupDecryptionFailedError at RESTORE time,
// before any shard is touched.
func TestBackupService_RestoreBackup_UnregisteredProviderAtRestoreRefused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/provider-removed",
		KeyRef:      writeTestKeyFile(t, 0x70),
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Simulate the provider having been removed from the registry between
	// create and restore by mutating the stored envelope's ProviderID.
	bs := svc.(*BackupService)
	bs.mu.Lock()
	bs.backups[created.ID].envelope.ProviderID = "no-longer-registered"
	bs.mu.Unlock()

	_, err = svc.RestoreBackup(ctx, RestoreOptions{BackupID: created.ID})
	if err == nil {
		t.Fatal("expected error when the envelope names an unregistered provider")
	}
	var decFailed *BackupDecryptionFailedError
	if !errors.As(err, &decFailed) {
		t.Fatalf("expected BackupDecryptionFailedError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// Fake KEK provider: key rotation, unwrap with an old key ID
// ---------------------------------------------------------------------------

// fakeKeyWrapProvider is a same-shaped keywrap.Provider test double: it
// treats keyRef itself as the KEK material (XOR), appending a 4-byte
// checksum of keyRef so Unwrap can detect a mismatched keyRef
// deterministically, mirroring go-xkms's own hardware providers without
// needing real key custody for this test.
type fakeKeyWrapProvider struct {
	id string
}

func (p *fakeKeyWrapProvider) ProviderID() string { return p.id }

func (p *fakeKeyWrapProvider) Wrap(_ context.Context, keyRef string, dek []byte) ([]byte, error) {
	wrapped := xorWithKeyRef(keyRef, dek)
	sum := keyRefChecksum(keyRef)
	return append(wrapped, sum[:]...), nil
}

func (p *fakeKeyWrapProvider) Unwrap(_ context.Context, keyRef string, wrapped []byte) ([]byte, error) {
	if len(wrapped) < 4 {
		return nil, &keywrap.UnwrapFailedError{}
	}
	data, tag := wrapped[:len(wrapped)-4], wrapped[len(wrapped)-4:]
	want := keyRefChecksum(keyRef)
	if !bytes.Equal(tag, want[:]) {
		return nil, &keywrap.UnwrapFailedError{}
	}
	return xorWithKeyRef(keyRef, data), nil
}

func keyRefChecksum(keyRef string) [4]byte {
	var sum [4]byte
	for i := 0; i < len(keyRef); i++ {
		sum[i%4] ^= keyRef[i]
	}
	return sum
}

func xorWithKeyRef(keyRef string, data []byte) []byte {
	out := make([]byte, len(data))
	for i, b := range data {
		out[i] = b ^ keyRef[i%len(keyRef)]
	}
	return out
}

var _ keywrap.Provider = (*fakeKeyWrapProvider)(nil)

// TestBackupService_FakeProvider_KeyRotation_OldKeyIDStillUnwraps proves
// that rotating which key reference NEW backups use does not break
// restoring an OLDER backup: each backup's own envelope pins the key
// reference it was wrapped under, so an old backup restores under its own
// (now "rotated away from") key reference even after a newer one has
// become the one new backups use.
func TestBackupService_FakeProvider_KeyRotation_OldKeyIDStillUnwraps(t *testing.T) {
	provider := newMockSnapshotProvider()
	fake := &fakeKeyWrapProvider{id: "fake-hsm"}
	svc := mustNewBackupService(t, provider, WithKeyWrapProvider(fake))
	ctx := context.Background()

	old, err := svc.CreateBackup(ctx, BackupOptions{
		Destination:   "/backups/rotation-old",
		KeyProviderID: "fake-hsm",
		KeyRef:        "key-id-v1",
	})
	if err != nil {
		t.Fatalf("CreateBackup (v1): %v", err)
	}

	// "Rotate": new backups now use a different key id.
	current, err := svc.CreateBackup(ctx, BackupOptions{
		Destination:   "/backups/rotation-current",
		KeyProviderID: "fake-hsm",
		KeyRef:        "key-id-v2",
	})
	if err != nil {
		t.Fatalf("CreateBackup (v2): %v", err)
	}

	// The OLD backup still restores under its OWN (now-superseded) key id --
	// nothing about creating the v2 backup touched it.
	oldResult, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: old.ID})
	if err != nil {
		t.Fatalf("RestoreBackup (v1, old key id): %v", err)
	}
	if oldResult.ShardsRestored == 0 {
		t.Fatal("expected the old backup to restore under its own key id")
	}

	currentResult, err := svc.RestoreBackup(ctx, RestoreOptions{BackupID: current.ID})
	if err != nil {
		t.Fatalf("RestoreBackup (v2, current key id): %v", err)
	}
	if currentResult.ShardsRestored == 0 {
		t.Fatal("expected the current backup to restore under its own key id")
	}

	// ListBackups confirms KeyProviderID on BackupInfo matches for both --
	// the service layer never special-cases "software" anywhere in its own
	// logic; a non-"software" provider round-trips through create, list,
	// and restore identically.
	list, err := svc.ListBackups(ctx)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	seen := map[string]string{}
	for _, info := range list {
		seen[info.ID] = info.KeyProviderID
	}
	if seen[old.ID] != "fake-hsm" {
		t.Fatalf("ListBackups KeyProviderID for %q = %q, want %q", old.ID, seen[old.ID], "fake-hsm")
	}
	if seen[current.ID] != "fake-hsm" {
		t.Fatalf("ListBackups KeyProviderID for %q = %q, want %q", current.ID, seen[current.ID], "fake-hsm")
	}
}

// TestBackupService_FakeProvider_WrongKeyIDRefused proves the fake
// provider's own Unwrap failure (a keyRef mismatch) surfaces as
// *BackupDecryptionFailedError through the service layer, the same as the
// software provider's.
func TestBackupService_FakeProvider_WrongKeyIDRefused(t *testing.T) {
	provider := newMockSnapshotProvider()
	fake := &fakeKeyWrapProvider{id: "fake-hsm"}
	svc := mustNewBackupService(t, provider, WithKeyWrapProvider(fake))
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination:   "/backups/wrong-key-id",
		KeyProviderID: "fake-hsm",
		KeyRef:        "key-id-v1",
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	_, err = svc.RestoreBackup(ctx, RestoreOptions{
		BackupID:         created.ID,
		DecryptionKeyRef: "key-id-WRONG",
	})
	if err == nil {
		t.Fatal("expected error for the wrong key id")
	}
	var decFailed *BackupDecryptionFailedError
	if !errors.As(err, &decFailed) {
		t.Fatalf("expected BackupDecryptionFailedError, got %T: %v", err, err)
	}
}

// TestBackupService_WithKeyWrapRegistry_ReplacesDefault proves
// WithKeyWrapRegistry installs the supplied registry wholesale (not merged
// with the default), matching its doc comment: a registry with no
// "software" entry means a plain KeyRef-only aes256 request (which
// defaults KeyProviderID to "software") fails to resolve a provider.
func TestBackupService_WithKeyWrapRegistry_ReplacesDefault(t *testing.T) {
	provider := newMockSnapshotProvider()
	registry := keywrap.NewRegistry()
	fake := &fakeKeyWrapProvider{id: "only-this-one"}
	if err := registry.Register("only-this-one", func() (keywrap.Provider, error) { return fake, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}
	svc := mustNewBackupService(t, provider, WithKeyWrapRegistry(registry))
	ctx := context.Background()

	// The default provider id ("software") is NOT in this registry.
	_, err := svc.CreateBackup(ctx, BackupOptions{
		Destination: "/backups/replaced-registry",
		KeyRef:      "some-key-ref",
	})
	if err == nil {
		t.Fatal("expected error: the default registry's \"software\" provider was replaced wholesale")
	}
	var decFailed *BackupDecryptionFailedError
	if !errors.As(err, &decFailed) {
		t.Fatalf("expected BackupDecryptionFailedError, got %T: %v", err, err)
	}

	// But the explicitly-named provider in the replacement registry works.
	created, err := svc.CreateBackup(ctx, BackupOptions{
		Destination:   "/backups/replaced-registry-named",
		KeyProviderID: "only-this-one",
		KeyRef:        "some-key-ref",
	})
	if err != nil {
		t.Fatalf("CreateBackup naming the replacement registry's own provider: %v", err)
	}
	if created.KeyProviderID != "only-this-one" {
		t.Fatalf("KeyProviderID = %q, want %q", created.KeyProviderID, "only-this-one")
	}
}

// ---------------------------------------------------------------------------
// DecryptBackupShard: the offline-restore (qrdbd boot-time) seam
// ---------------------------------------------------------------------------

// TestBackupService_DecryptBackupShard_EncryptedRoundTrips proves
// DecryptBackupShard returns the exact plaintext RestoreBackup itself
// would apply -- the seam cmd/qrdbd/restore.go's offline disaster-recovery
// boot path uses instead of reading raw (ciphertext, for an encrypted
// backup) shard bytes directly.
func TestBackupService_DecryptBackupShard_EncryptedRoundTrips(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()
	keyPath := writeTestKeyFile(t, 0x90)

	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/offline-restore", KeyRef: keyPath})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	for shardID, plaintext := range provider.shards {
		got, err := bs.DecryptBackupShard(ctx, created.ID, shardID, "", false)
		if err != nil {
			t.Fatalf("DecryptBackupShard(shard %d): %v", shardID, err)
		}
		if string(got) != string(plaintext) {
			t.Fatalf("DecryptBackupShard(shard %d) = %q, want %q", shardID, got, plaintext)
		}
	}
}

// TestBackupService_DecryptBackupShard_PlaintextBackupRefusedWithoutOptIn
// proves an EncryptionNone backup's shard is refused with
// *BackupPlaintextRestoreNotAllowedError when allowPlaintext is false --
// the caller's explicit opt-in, mirroring RestoreOptions.AllowPlaintext
// (go-qrdb security review, backup-encrypt-default round 4).
func TestBackupService_DecryptBackupShard_PlaintextBackupRefusedWithoutOptIn(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/offline-restore-plain-refused", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	var anyShard uint64
	for id := range provider.shards {
		anyShard = id
		break
	}
	_, err = bs.DecryptBackupShard(ctx, created.ID, anyShard, "", false)
	var notAllowed *BackupPlaintextRestoreNotAllowedError
	if !errors.As(err, &notAllowed) {
		t.Fatalf("DecryptBackupShard without opt-in: err = %T, want *BackupPlaintextRestoreNotAllowedError: %v", err, err)
	}
}

// TestBackupService_DecryptBackupShard_PlaintextBackupPassesThrough proves
// an EncryptionNone backup's shard comes back unchanged -- no envelope, no
// decryption attempted -- once the caller opts in with allowPlaintext.
func TestBackupService_DecryptBackupShard_PlaintextBackupPassesThrough(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/offline-restore-plain", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	for shardID, plaintext := range provider.shards {
		got, err := bs.DecryptBackupShard(ctx, created.ID, shardID, "", true)
		if err != nil {
			t.Fatalf("DecryptBackupShard(shard %d): %v", shardID, err)
		}
		if string(got) != string(plaintext) {
			t.Fatalf("DecryptBackupShard(shard %d) = %q, want %q", shardID, got, plaintext)
		}
	}
}

// TestBackupService_DecryptBackupShard_WrongKeyRefused proves a wrong
// DecryptBackupShard override gives *BackupDecryptionFailedError, matching
// RestoreBackup's own refusal for the same condition.
func TestBackupService_DecryptBackupShard_WrongKeyRefused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/offline-wrong-key", KeyRef: writeTestKeyFile(t, 0xA0)})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	var shardID uint64
	for id := range provider.shards {
		shardID = id
		break
	}

	_, err = bs.DecryptBackupShard(ctx, created.ID, shardID, writeTestKeyFile(t, 0xB0), false)
	var decFailed *BackupDecryptionFailedError
	if !errors.As(err, &decFailed) {
		t.Fatalf("expected BackupDecryptionFailedError, got %T: %v", err, err)
	}
}

// TestBackupService_DecryptBackupShard_EmptyIDRefused proves the same
// empty-ID guard every other id-taking method has.
func TestBackupService_DecryptBackupShard_EmptyIDRefused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	bs := svc.(*BackupService)

	_, err := bs.DecryptBackupShard(context.Background(), "", 1, "", false)
	var emptyID *BackupEmptyIDError
	if !errors.As(err, &emptyID) {
		t.Fatalf("expected BackupEmptyIDError, got %T: %v", err, err)
	}
}

// TestBackupService_DecryptBackupShard_UnknownShardRefused proves a shard
// ID not present in the backup's own artifact is refused rather than
// returning a zero-value plaintext.
func TestBackupService_DecryptBackupShard_UnknownShardRefused(t *testing.T) {
	provider := newMockSnapshotProvider()
	svc := mustNewBackupService(t, provider)
	ctx := context.Background()

	created, err := svc.CreateBackup(ctx, BackupOptions{Destination: "/backups/offline-unknown-shard", Encryption: EncryptionNone})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	bs := svc.(*BackupService)
	_, err = bs.DecryptBackupShard(ctx, created.ID, 99999, "", true)
	if err == nil {
		t.Fatal("expected error for an unknown shard ID")
	}
}
