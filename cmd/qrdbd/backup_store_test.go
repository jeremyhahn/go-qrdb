package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/server/lifecycle"
	"github.com/jeremyhahn/go-qrdb/pkg/service/backupsink"
)

func TestOpenBackupStore_LocalDestination(t *testing.T) {
	f := &startFlags{backupDestination: &qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": filepath.Join(t.TempDir(), "repo")}}}
	store, err := openBackupStore(f)
	if err != nil || store == nil {
		t.Fatalf("openBackupStore() = %v, %v, want the destination store", store, err)
	}
	defer func() { _ = store.Close() }()
	list, err := backupsink.New(store).List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("List() = %v, %v, want an empty repository", list, err)
	}
}

func TestOpenBackupStore_BackupDir(t *testing.T) {
	store, err := openBackupStore(&startFlags{backupDir: t.TempDir()})
	if err != nil || store == nil {
		t.Fatalf("openBackupStore() = %v, %v, want the file store", store, err)
	}
	_ = store.Close()
}

func TestOpenBackupStore_RefusesBothRepositories(t *testing.T) {
	f := &startFlags{backupDir: t.TempDir(), backupDestination: &qrdbconfig.ObjstoreConfig{Provider: "local", Settings: map[string]string{"path": filepath.Join(t.TempDir(), "repo")}}}
	store, err := openBackupStore(f)
	var conflict *BackupDestinationConflictError
	if store != nil || !errors.As(err, &conflict) {
		t.Fatalf("openBackupStore() = %v, %v, want a BackupDestinationConflictError", store, err)
	}
	if err := checkBackupRepository(f); !errors.As(err, &conflict) {
		t.Fatalf("checkBackupRepository() = %v, want a BackupDestinationConflictError", err)
	}
}

func TestOpenBackupStore_UnsupportedProvider(t *testing.T) {
	_, err := openBackupStore(&startFlags{backupDestination: &qrdbconfig.ObjstoreConfig{Provider: "no-such-provider"}})
	var de *lifecycle.BackupDestinationError
	if !errors.As(err, &de) {
		t.Fatalf("openBackupStore() = %v, want a lifecycle.BackupDestinationError", err)
	}
}

func TestOpenBackupStore_NeitherIsNil(t *testing.T) {
	store, err := openBackupStore(&startFlags{})
	if store != nil || err != nil {
		t.Fatalf("openBackupStore() = %v, %v, want nil, nil", store, err)
	}
}

// writeBackupDestinationConfig writes a start config carrying a
// backup.destination block.
func writeBackupDestinationConfig(t *testing.T, destination string) string {
	t.Helper()
	path := writeBindHostConfig(t, `  host: "127.0.0.1"
  admin_port: 19443`)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	_, err = f.WriteString("backup:\n  destination:\n" + destination)
	require.NoError(t, err)
	require.NoError(t, f.Close())
	return path
}

func TestResolveStartFlags_SetsTheBackupDestination(t *testing.T) {
	dir := t.TempDir()
	cmd, f := newStartCmdWithFlags()
	require.NoError(t, cmd.ParseFlags([]string{"--config", writeBackupDestinationConfig(t, "    provider: local\n    settings:\n      path: "+dir+"\n")}))
	require.NoError(t, resolveStartFlags(cmd, f))
	require.NotNil(t, f.backupDestination)
	assert.Equal(t, "local", f.backupDestination.Provider)
	assert.Equal(t, dir, f.backupDestination.Settings["path"])
}

func TestResolveStartFlags_RefusesBackupDirWithABackupDestination(t *testing.T) {
	cmd, f := newStartCmdWithFlags()
	require.NoError(t, cmd.ParseFlags([]string{
		"--config", writeBackupDestinationConfig(t, "    provider: local\n    settings:\n      path: "+t.TempDir()+"\n"),
		"--backup-dir", t.TempDir(),
	}))
	var conflict *BackupDestinationConflictError
	err := resolveStartFlags(cmd, f)
	require.True(t, errors.As(err, &conflict), "resolveStartFlags = %v, want a BackupDestinationConflictError", err)
}

func TestResolveStartFlags_RefusesAnInlineBackupDestinationCredential(t *testing.T) {
	const value = "inline-backup-credential-must-not-leak"
	cmd, f := newStartCmdWithFlags()
	require.NoError(t, cmd.ParseFlags([]string{"--config", writeBackupDestinationConfig(t, "    provider: s3\n    settings:\n      bucket: backups\n      secret_key: "+value+"\n")}))
	err := resolveStartFlags(cmd, f)
	var ve *qrdbconfig.ValidationError
	require.True(t, errors.As(err, &ve), "resolveStartFlags = %v, want a ValidationError", err)
	assert.Equal(t, "backup.destination.settings", ve.Field)
	assert.NotContains(t, err.Error(), value)
}
