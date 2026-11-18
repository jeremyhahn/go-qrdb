package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	qrdbconfig "github.com/jeremyhahn/go-qrdb/pkg/config"
	"github.com/jeremyhahn/go-qrdb/pkg/database"
	"github.com/jeremyhahn/go-qrdb/pkg/database/databasetest"
	"github.com/jeremyhahn/go-qrdb/pkg/database/objstoredb"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/objstore"
)

// loadDatabasesYAML loads a daemon config file holding body as its
// databases list.
func loadDatabasesYAML(t *testing.T, body string) *qrdbconfig.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qrdb.yaml")
	require.NoError(t, os.WriteFile(path, []byte("databases:\n"+body), 0o600))
	cfg, err := qrdbconfig.Load(path)
	require.NoError(t, err)
	return cfg
}

// TestProvisionDatabases_ObjstoreDatabaseFromYAML provisions a
// `backend: objstore` database declared in the daemon config: its provider
// block reaches the database's config and its store is opened on that
// provider's namespace.
func TestProvisionDatabases_ObjstoreDatabaseFromYAML(t *testing.T) {
	root := t.TempDir()
	cfg := loadDatabasesYAML(t, "  - name: blobs\n    backend: objstore\n    objstore:\n      provider: local\n      settings:\n        path: "+root+"\n")
	f := &startFlags{databases: cfg.Databases, smType: "memory"}
	require.NoError(t, validateConfiguredDatabases(f))

	mgr := databasetest.NewManager(nil, t.TempDir(), database.WithBackendFactory(objstoredb.NewBackendFactory()))
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, provisionDatabases(context.Background(), f, mgr, electedLeader{}))

	db, err := mgr.Get("blobs")
	require.NoError(t, err)
	assert.Equal(t, database.BackendObjstore, db.BackendType())
	got := db.Config().Objstore
	require.NotNil(t, got, "the YAML objstore block did not reach the database")
	assert.Equal(t, "local", got.Provider)
	assert.Equal(t, map[string]string{"path": root}, got.Settings)

	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.NotEmpty(t, entries, "the database's store was not opened on the configured provider path")
}

// TestConfiguredDatabases_CarriesTheObjstoreCredentialFiles checks the
// credential files a YAML block names are carried to the database config.
func TestConfiguredDatabases_CarriesTheObjstoreCredentialFiles(t *testing.T) {
	cfg := loadDatabasesYAML(t, "  - name: blobs\n    backend: objstore\n    objstore:\n      provider: s3\n      settings:\n        bucket: b\n"+
		"      credentials:\n        key_file: /run/secrets/key\n        secret_file: /run/secrets/secret\n")
	cfgs := configuredDatabases(&startFlags{databases: cfg.Databases})
	require.Len(t, cfgs, 1)
	assert.Equal(t, &database.DatabaseObjstoreConfig{
		Provider:   "s3",
		Settings:   map[string]string{"bucket": "b"},
		KeyFile:    "/run/secrets/key",
		SecretFile: "/run/secrets/secret",
	}, cfgs[0].Objstore)
}

// TestRejectInlineObjstoreCredentials_RefusesADatabaseSetting checks a
// credential written into a database's objstore settings fails the load,
// naming the field and never the value.
func TestRejectInlineObjstoreCredentials_RefusesADatabaseSetting(t *testing.T) {
	cfg := loadDatabasesYAML(t, "  - name: blobs\n    backend: objstore\n    objstore:\n      provider: s3\n      settings:\n        bucket: b\n        secret_key: s3cr3t-value\n")
	err := rejectInlineObjstoreCredentials(cfg)
	var ve *qrdbconfig.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Equal(t, "databases[0].objstore.settings", ve.Field)
	var setting *objstore.CredentialSettingError
	require.ErrorAs(t, err, &setting)
	assert.NotContains(t, err.Error(), "s3cr3t-value")

	require.NoError(t, rejectInlineObjstoreCredentials(loadDatabasesYAML(t, "  - name: blobs\n    backend: memory\n")))
}
