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

//go:build azureblob || gcpstorage || awss3

package node

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// newRaftBackupNode starts the single-member clustered node the raft round
// trip runs on, with dest as its backup.destination.
func newRaftBackupNode(t *testing.T, dest *qrdbconfig.ObjstoreConfig) *Node {
	t.Helper()
	cfg := newRaftIsolationConfig(t)
	cfg.Backup.Destination = dest
	n, err := NewClusteredNode(context.Background(), cfg, raftMembers(), nil,
		WithTenantIsolation(),
		WithDefaultTenantEntitlements(raftEntitlements()),
		WithNoSyncWrites(),
	)
	require.NoError(t, err)
	return n
}

// testRaftBackupDestinationRoundTrip backs a tenant's dedicated raft shard up
// to the destination dest returns, verifies and restores it, reads every key
// back, and has a second node over the same destination list and verify it.
// objects returns the names the destination's stub holds.
func testRaftBackupDestinationRoundTrip(t *testing.T, dest func() *qrdbconfig.ObjstoreConfig, objects func() []string) {
	t.Helper()
	ctx := context.Background()

	first := newRaftBackupNode(t, dest())
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = first.Close()
		}
	})
	createDefaultPool(t, first)
	createRaftTenant(t, first, "acme", "Acme Corp")
	db, err := first.Services().Database.CreateForTenant(tenant.WithTenantID(ctx, "acme"), database.DatabaseConfig{Name: "orders"})
	require.NoError(t, err)
	require.NotZero(t, db.ShardID, "the dedicated-raft database has a shard")

	svc, err := first.raftBackend.Services(ctx, "acme", "orders")
	require.NoError(t, err)
	want := make(map[string]string, 20)
	for i := range 20 {
		k, v := fmt.Sprintf("backup/key/%02d", i), fmt.Sprintf("value-%d", i)
		require.NoErrorf(t, svc.KV.Put(ctx, k, []byte(v)), "Put(%q)", k)
		want[k] = v
	}

	backup := first.Services().Backup
	info, err := backup.CreateBackup(ctx, service.BackupOptions{
		Destination: "backup-destination-raft",
		ShardIDs:    []uint64{db.ShardID},
		Compression: service.CompressionNone,
		Encryption:  service.EncryptionNone,
	})
	require.NoError(t, err)
	require.Equal(t, 1, info.ShardCount)
	require.Positive(t, info.Size)
	require.NotEmpty(t, objects(), "the destination holds no objects after the backup")

	verified, err := backup.VerifyBackup(ctx, info.ID)
	require.NoError(t, err)
	require.True(t, verified.Valid, "%v", verified.Errors)

	restored, err := backup.RestoreBackup(ctx, service.RestoreOptions{
		BackupID:       info.ID,
		TargetShards:   []uint64{db.ShardID},
		AllowPlaintext: true,
	})
	require.NoError(t, err)
	require.Empty(t, restored.Errors)
	require.Equal(t, 1, restored.ShardsRestored)

	svc, err = first.raftBackend.Services(ctx, "acme", "orders")
	require.NoError(t, err)
	for k, v := range want {
		got := readAfterRestore(t, svc.KV, k)
		require.Equalf(t, v, string(got), "value for %q after the restore", k)
	}
	closed = true
	require.NoError(t, first.Close())

	second := newRaftBackupNode(t, dest())
	t.Cleanup(func() { _ = second.Close() })
	listed, err := second.Services().Backup.ListBackups(ctx)
	require.NoError(t, err)
	ids := make([]string, 0, len(listed))
	for _, b := range listed {
		ids = append(ids, b.ID)
	}
	require.Contains(t, ids, info.ID)
	verified, err = second.Services().Backup.VerifyBackup(ctx, info.ID)
	require.NoError(t, err)
	require.True(t, verified.Valid, "%v", verified.Errors)
}

// kvGetter is the read the restore check makes.
type kvGetter interface {
	Get(ctx context.Context, key string) ([]byte, error)
}

// readAfterRestore reads key from a shard a restore has just restarted.
func readAfterRestore(t *testing.T, kv kvGetter, key string) []byte {
	t.Helper()
	got, err := kv.Get(context.Background(), key)
	require.NoErrorf(t, err, "Get(%q) after the restore", key)
	return got
}
