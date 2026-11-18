package database

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createRecordingCatalog counts the records written through Create, so a
// test can prove a refused config never reached the catalog.
type createRecordingCatalog struct {
	Catalog
	creates atomic.Int32
}

func (c *createRecordingCatalog) Create(ctx context.Context, meta DatabaseMeta) error {
	c.creates.Add(1)
	return c.Catalog.Create(ctx, meta)
}

// TestManager_Create_RefusesInlineObjstoreCredentialBeforeCatalog checks an
// inline credential in an Objstore block is refused before any catalog
// write, whatever the backend: the catalog record replicates the settings,
// so a secret reaching it is a secret written to every node.
func TestManager_Create_RefusesInlineObjstoreCredentialBeforeCatalog(t *testing.T) {
	for _, backend := range []BackendType{BackendObjstore, BackendMemory} {
		t.Run(string(backend), func(t *testing.T) {
			inner, _ := newTestCatalog(t, nil)
			cat := &createRecordingCatalog{Catalog: inner}
			mgr := NewManager(nil, t.TempDir())
			mgr.SetCatalog(cat)
			t.Cleanup(func() { _ = mgr.Close() })

			_, err := mgr.Create(DatabaseConfig{Name: "blobs", Backend: backend, Objstore: &DatabaseObjstoreConfig{
				Provider: "local",
				Settings: map[string]string{"path": t.TempDir(), "secret_key": "s3cr3t-value"},
			}})
			var credErr *ObjstoreCredentialSettingError
			require.ErrorAs(t, err, &credErr)
			assert.Equal(t, "secret_key", credErr.Setting)
			assert.NotContains(t, err.Error(), "s3cr3t-value", "the refusal must not echo the secret")
			assert.Zero(t, cat.creates.Load(), "a refused credential reached Catalog.Create")
		})
	}
}

// TestDatabaseObjstoreConfig_Validate checks the non-secret settings and
// file-named credentials are accepted.
func TestDatabaseObjstoreConfig_Validate(t *testing.T) {
	ok := &DatabaseObjstoreConfig{Provider: "s3", Settings: map[string]string{"bucket": "b", "region": "r"},
		KeyFile: "/run/key", SecretFile: "/run/secret"}
	require.NoError(t, ok.Validate())
	var nilCfg *DatabaseObjstoreConfig
	require.NoError(t, nilCfg.Validate())
}
