package node

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// localDestination is a backup.destination on the go-objstore local
// provider, rooted in a directory outside every node's data dir.
func localDestination(path string) *qrdbconfig.ObjstoreConfig {
	return &qrdbconfig.ObjstoreConfig{
		Provider: "local",
		Settings: map[string]string{"path": path},
	}
}

// TestBackupDestination_NodeWritesAndSecondNodeReads pins the config-driven
// SDK route: a node opened over cfg.Backup.Destination writes its backups
// to that repository, and a fresh node (new data dir) over the same
// destination lists, reads and verifies them.
func TestBackupDestination_NodeWritesAndSecondNodeReads(t *testing.T) {
	ctx := context.Background()
	destination := filepath.Join(t.TempDir(), "repo")

	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = localDestination(destination)
	first, err := NewStandaloneNode(ctx, cfg, nil)
	require.NoError(t, err)
	info, err := first.Services().Backup.CreateBackup(ctx, service.BackupOptions{
		Destination: "backup-destination-sdk",
		Compression: service.CompressionNone,
		Encryption:  service.EncryptionNone,
	})
	if err != nil {
		_ = first.Close()
		t.Fatalf("CreateBackup over backup.destination: %v", err)
	}
	require.NoError(t, first.Close())

	cfg2 := newTenantIsolationConfig(t)
	cfg2.Backup.Destination = localDestination(destination)
	second, err := NewStandaloneNode(ctx, cfg2, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })

	listed, err := second.Services().Backup.ListBackups(ctx)
	require.NoError(t, err)
	ids := make([]string, 0, len(listed))
	for _, b := range listed {
		ids = append(ids, b.ID)
	}
	require.Contains(t, ids, info.ID)

	got, err := second.Services().Backup.GetBackup(ctx, info.ID)
	require.NoError(t, err)
	require.Equal(t, info.ID, got.ID)

	verified, err := second.Services().Backup.VerifyBackup(ctx, info.ID)
	require.NoError(t, err)
	require.True(t, verified.Valid, "%v", verified.Errors)
}

// TestBackupDestination_ConflictWithWithBackupSink pins that a composer's
// WithBackupSink and a configured backup.destination are exclusive: the
// constructor refuses rather than silently picking one.
func TestBackupDestination_ConflictWithWithBackupSink(t *testing.T) {
	sinks := []struct {
		name string
		opt  NodeOption
	}{
		{"sink", WithBackupSink(listingSink{})},
		{"nil sink", WithBackupSink(nil)},
	}
	for _, s := range sinks {
		t.Run("standalone "+s.name, func(t *testing.T) {
			cfg := newTenantIsolationConfig(t)
			cfg.Backup.Destination = localDestination(filepath.Join(t.TempDir(), "repo"))
			n, err := NewStandaloneNode(context.Background(), cfg, nil, s.opt)
			if n != nil {
				_ = n.Close()
			}
			var conflict *BackupDestinationConflictError
			require.True(t, errors.As(err, &conflict), "want *BackupDestinationConflictError, got %v", err)
			require.Nil(t, n)
		})
		t.Run("clustered "+s.name, func(t *testing.T) {
			cfg := &qrdbconfig.Config{}
			cfg.Backup.Destination = localDestination(filepath.Join(t.TempDir(), "repo"))
			n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: "127.0.0.1:1"}, nil, s.opt)
			if n != nil {
				_ = n.Close()
			}
			var conflict *BackupDestinationConflictError
			require.True(t, errors.As(err, &conflict), "want *BackupDestinationConflictError, got %v", err)
			require.Nil(t, n)
		})
	}
}

func TestBackupDestination_ConflictWithServerWithBackupSink(t *testing.T) {
	sinks := []struct {
		name string
		opt  server.ServerOption
	}{
		{"sink", server.WithBackupSink(listingSink{})},
		{"nil sink", server.WithBackupSink(nil)},
	}
	for _, s := range sinks {
		t.Run("standalone "+s.name, func(t *testing.T) {
			cfg := newTenantIsolationConfig(t)
			cfg.Backup.Destination = localDestination(filepath.Join(t.TempDir(), "repo"))
			n, err := NewStandaloneNode(context.Background(), cfg, []server.ServerOption{s.opt})
			if n != nil {
				_ = n.Close()
			}
			var conflict *BackupDestinationConflictError
			require.True(t, errors.As(err, &conflict), "want *BackupDestinationConflictError, got %v", err)
			require.Nil(t, n)
		})
		t.Run("clustered "+s.name, func(t *testing.T) {
			cfg := &qrdbconfig.Config{}
			cfg.Backup.Destination = localDestination(filepath.Join(t.TempDir(), "repo"))
			n, err := NewClusteredNode(context.Background(), cfg, map[uint64]string{1: "127.0.0.1:1"}, []server.ServerOption{s.opt})
			if n != nil {
				_ = n.Close()
			}
			var conflict *BackupDestinationConflictError
			require.True(t, errors.As(err, &conflict), "want *BackupDestinationConflictError, got %v", err)
			require.Nil(t, n)
		})
	}
}

// TestBackupDestination_UnknownProvider pins that a destination naming no
// registered go-objstore provider fails the boot with the lifecycle error.
func TestBackupDestination_UnknownProvider(t *testing.T) {
	cfg := newTenantIsolationConfig(t)
	cfg.Backup.Destination = &qrdbconfig.ObjstoreConfig{
		Provider: "no-such-provider",
		Settings: map[string]string{"path": t.TempDir()},
	}
	n, err := NewStandaloneNode(context.Background(), cfg, nil)
	if n != nil {
		_ = n.Close()
	}
	var destErr *lifecycle.BackupDestinationError
	require.True(t, errors.As(err, &destErr), "want *lifecycle.BackupDestinationError, got %v", err)
	require.Nil(t, n)
}

// TestBackupDestination_AbsentKeepsWithBackupSink guards the nil,nil path:
// with no backup.destination the composer's sink still reaches the Backup
// service.
func TestBackupDestination_AbsentKeepsWithBackupSink(t *testing.T) {
	cfg := newTenantIsolationConfig(t)
	require.Nil(t, cfg.Backup.Destination)
	n, err := NewStandaloneNode(context.Background(), cfg, nil, WithBackupSink(listingSink{}))
	require.NoError(t, err)
	t.Cleanup(func() { _ = n.Close() })
	got, err := n.Services().Backup.ListBackups(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "from-the-composers-sink", got[0].ID)
}
