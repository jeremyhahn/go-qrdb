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
// to the destination dest returns and verifies it, then closes that node and
// restores the backup onto a replacement shard with the same identity that
// has lost its data: a second node over the same destination lists, verifies
// and restores the backup, and every key must come back from it. A key written
// after the backup must stay absent, so the state is the backup's and not a
// live log's. objects returns the names the destination's stub holds.
func testRaftBackupDestinationRoundTrip(t *testing.T, dest func() *qrdbconfig.ObjstoreConfig, objects func() []string) {
	t.Helper()
	ctx := context.Background()
	acme := tenant.WithTenantID(ctx, "acme")

	first := newRaftBackupNode(t, dest())
	closed := false
	t.Cleanup(func() {
		if !closed {
			_ = first.Close()
		}
	})
	createDefaultPool(t, first)
	createRaftTenant(t, first, "acme", "Acme Corp")
	db, err := first.Services().Database.CreateForTenant(acme, database.DatabaseConfig{Name: "orders"})
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

	const sentinel = "backup/after"
	require.NoError(t, svc.KV.Put(ctx, sentinel, []byte("not-in-the-backup")))
	closed = true
	require.NoError(t, first.Close())

	// The replacement: fresh data and pool directories, the same stub
	// destination, and the same pool, tenant and database, so the ledger
	// issues the same shard ID the backup's manifest names.
	second := newRaftBackupNode(t, dest())
	t.Cleanup(func() { _ = second.Close() })
	createDefaultPool(t, second)
	createRaftTenant(t, second, "acme", "Acme Corp")
	db2, err := second.Services().Database.CreateForTenant(acme, database.DatabaseConfig{Name: "orders"})
	require.NoError(t, err)
	require.Equal(t, db.ShardID, db2.ShardID, "the replacement shard has the backed-up shard's identity")

	svc, err = second.raftBackend.Services(ctx, "acme", "orders")
	require.NoError(t, err)
	for k := range want {
		requireKeyAbsent(t, svc.KV, k, "before the restore")
	}
	requireKeyAbsent(t, svc.KV, sentinel, "before the restore")

	backup = second.Services().Backup
	listed, err := backup.ListBackups(ctx)
	require.NoError(t, err)
	ids := make([]string, 0, len(listed))
	for _, b := range listed {
		ids = append(ids, b.ID)
	}
	require.Contains(t, ids, info.ID)
	verified, err = backup.VerifyBackup(ctx, info.ID)
	require.NoError(t, err)
	require.True(t, verified.Valid, "%v", verified.Errors)

	restored, err := backup.RestoreBackup(ctx, service.RestoreOptions{
		BackupID:       info.ID,
		TargetShards:   []uint64{db2.ShardID},
		AllowPlaintext: true,
	})
	require.NoError(t, err)
	require.Empty(t, restored.Errors)
	require.Equal(t, 1, restored.ShardsRestored)

	// The restore restarts the shard; a pre-restart handle is invalid.
	svc, err = second.raftBackend.Services(ctx, "acme", "orders")
	require.NoError(t, err)
	for k, v := range want {
		got := readAfterRestore(t, svc.KV, k)
		require.Equalf(t, v, string(got), "value for %q after the restore", k)
	}
	requireKeyAbsent(t, svc.KV, sentinel, "after the restore")

	require.NoError(t, svc.KV.Put(ctx, "backup/new", []byte("post-restore-write")))
	got := readAfterRestore(t, svc.KV, "backup/new")
	require.Equal(t, "post-restore-write", string(got))
}

// requireKeyAbsent fails the test unless the shard holds no value for key.
func requireKeyAbsent(t *testing.T, kv kvGetter, key, when string) {
	t.Helper()
	_, err := kv.Get(context.Background(), key)
	var notFound *service.KeyNotFoundError
	require.ErrorAsf(t, err, &notFound, "Get(%q) %s", key, when)
	require.Equal(t, key, notFound.Key)
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
