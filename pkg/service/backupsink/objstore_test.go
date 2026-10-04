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

package backupsink_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// fakeProvider is an in-memory ShardSnapshotProvider for tests.
type fakeProvider struct {
	mu       sync.Mutex
	shards   map[uint64][]byte
	restored map[uint64][]byte
}

func newFakeProvider(shards map[uint64][]byte) *fakeProvider {
	return &fakeProvider{shards: shards, restored: map[uint64][]byte{}}
}

func (f *fakeProvider) AvailableShards(_ context.Context) ([]uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]uint64, 0, len(f.shards))
	for id := range f.shards {
		ids = append(ids, id)
	}
	return ids, nil
}

func (f *fakeProvider) SnapshotShard(_ context.Context, shardID uint64) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.shards[shardID], nil
}

func (f *fakeProvider) RestoreShard(_ context.Context, shardID uint64, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restored[shardID] = data
	return nil
}

func newMemSink(t *testing.T) *backupsink.Sink {
	t.Helper()
	store, err := objstore.New("memory", map[string]string{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return backupsink.New(store)
}

// TestBackupSink_DurableRoundTrip proves a backup persists to the object store
// and survives a "restart" (a fresh BackupService with an empty in-memory map
// but the same sink) — covering list, get, verify, restore, and delete.
func TestBackupSink_DurableRoundTrip(t *testing.T) {
	ctx := context.Background()
	sink := newMemSink(t)

	shards := map[uint64][]byte{1: []byte("shard-one"), 2: []byte("shard-two")}
	provider := newFakeProvider(shards)

	svc := service.NewBackupService(provider,
		service.WithBackupSink(sink),
		service.WithIDGenerator(func() string { return "backup-123" }),
	)

	info, err := svc.CreateBackup(ctx, service.BackupOptions{Destination: "objstore://repo", Encryption: service.EncryptionNone})
	require.NoError(t, err)
	require.Equal(t, "backup-123", info.ID)
	assert.Equal(t, service.BackupStatusComplete, info.Status)
	assert.Equal(t, 2, info.ShardCount)

	// Simulate a process restart: a brand-new service with the SAME sink but a
	// fresh (empty) in-memory registry and a fresh restore target.
	freshProvider := newFakeProvider(map[uint64][]byte{})
	restarted := service.NewBackupService(freshProvider, service.WithBackupSink(sink))

	// List sees the durably-persisted backup.
	list, err := restarted.ListBackups(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "backup-123", list[0].ID)

	// Get returns metadata read back from the sink.
	got, err := restarted.GetBackup(ctx, "backup-123")
	require.NoError(t, err)
	assert.Equal(t, info.Checksum, got.Checksum)

	// Verify recomputes the checksum from the persisted payloads.
	vr, err := restarted.VerifyBackup(ctx, "backup-123")
	require.NoError(t, err)
	assert.True(t, vr.Valid)
	assert.True(t, vr.ChecksumMatch)

	// Restore pulls payloads from the sink and applies them to the provider.
	rr, err := restarted.RestoreBackup(ctx, service.RestoreOptions{BackupID: "backup-123"})
	require.NoError(t, err)
	assert.Equal(t, 2, rr.ShardsRestored)
	assert.Empty(t, rr.Errors)
	assert.Equal(t, []byte("shard-one"), freshProvider.restored[1])
	assert.Equal(t, []byte("shard-two"), freshProvider.restored[2])

	// Delete removes it from the durable store.
	require.NoError(t, restarted.DeleteBackup(ctx, "backup-123"))
	list, err = restarted.ListBackups(ctx)
	require.NoError(t, err)
	assert.Empty(t, list)

	_, err = restarted.GetBackup(ctx, "backup-123")
	var nf *service.BackupNotFoundError
	assert.ErrorAs(t, err, &nf)
}

// TestBackupSink_EncryptedBackup_DurableRoundTrip proves an encrypted
// backup's envelope (wrapped DEK, key reference, provider ID) persists to
// the object store alongside its ciphertext shard data and metadata, and
// that a "restarted" service (fresh in-memory registry, same sink)
// resolves it correctly on restore -- the sink-backed counterpart of
// pkg/service's own in-memory envelope tests.
func TestBackupSink_EncryptedBackup_DurableRoundTrip(t *testing.T) {
	ctx := context.Background()
	sink := newMemSink(t)

	keyPath := filepath.Join(t.TempDir(), "backup.key")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	require.NoError(t, os.WriteFile(keyPath, key, 0o600))

	shards := map[uint64][]byte{1: []byte("shard-one"), 2: []byte("shard-two")}
	provider := newFakeProvider(shards)

	svc := service.NewBackupService(provider,
		service.WithBackupSink(sink),
		service.WithIDGenerator(func() string { return "backup-enc-1" }),
	)

	info, err := svc.CreateBackup(ctx, service.BackupOptions{Destination: "objstore://repo", KeyRef: keyPath})
	require.NoError(t, err)
	require.Equal(t, service.EncryptionAES256, info.Encryption)
	require.Equal(t, "software", info.KeyProviderID)

	// The envelope persisted to the sink is readable directly.
	envelope, err := sink.ReadEnvelope(ctx, "backup-enc-1")
	require.NoError(t, err)
	require.NotNil(t, envelope)
	assert.Equal(t, "software", envelope.ProviderID)
	assert.Equal(t, keyPath, envelope.KeyRef)
	assert.NotEmpty(t, envelope.WrappedDEK)

	// Simulate a process restart: a brand-new service, same sink.
	freshProvider := newFakeProvider(map[uint64][]byte{})
	restarted := service.NewBackupService(freshProvider, service.WithBackupSink(sink))

	rr, err := restarted.RestoreBackup(ctx, service.RestoreOptions{BackupID: "backup-enc-1"})
	require.NoError(t, err)
	assert.Equal(t, 2, rr.ShardsRestored)
	assert.Empty(t, rr.Errors)
	assert.Equal(t, []byte("shard-one"), freshProvider.restored[1])
	assert.Equal(t, []byte("shard-two"), freshProvider.restored[2])
}

// TestBackupSink_GetMissing verifies a missing backup reports not-found.
func TestBackupSink_GetMissing(t *testing.T) {
	sink := newMemSink(t)
	_, err := sink.ReadInfo(context.Background(), "nope")
	var nf *service.BackupNotFoundError
	assert.ErrorAs(t, err, &nf)
}
