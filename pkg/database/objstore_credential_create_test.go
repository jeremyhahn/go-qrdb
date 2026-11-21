package database

import (
	"context"
	"errors"
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

// refuseSecretKey is a faithful double of the objstore seam's settings
// validator (objstoredb.ValidateSettings): it refuses the inline credential
// setting secret_key, naming the setting and never echoing its value.
func refuseSecretKey(settings map[string]string) error {
	if _, ok := settings["secret_key"]; ok {
		return &ObjstoreCredentialSettingError{Setting: "secret_key", Err: errors.New("inline credential setting")}
	}
	return nil
}

// newCredentialTestManager builds a manager over a create-recording catalog
// whose embedded factory carries opts.
func newCredentialTestManager(t *testing.T, opts ...EmbeddedBackendFactoryOption) (*Manager, *createRecordingCatalog) {
	t.Helper()
	inner, _ := newTestCatalog(t, nil)
	cat := &createRecordingCatalog{Catalog: inner}
	mgr := NewManager(nil, t.TempDir(), WithBackendFactory(NewEmbeddedBackendFactory(opts...)))
	mgr.SetCatalog(cat)
	t.Cleanup(func() { _ = mgr.Close() })
	return mgr, cat
}

// TestManager_Create_RefusesInlineObjstoreCredentialBeforeCatalog checks an
// inline credential in an Objstore block is refused by the injected settings
// validator before any catalog write, whatever the backend: the catalog
// record replicates the settings, so a secret reaching it is a secret written
// to every node.
func TestManager_Create_RefusesInlineObjstoreCredentialBeforeCatalog(t *testing.T) {
	for _, backend := range []BackendType{BackendObjstore, BackendMemory} {
		t.Run(string(backend), func(t *testing.T) {
			mgr, cat := newCredentialTestManager(t, WithObjectStorageValidator(refuseSecretKey))

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

// TestManager_Create_ObjstoreBlockWithoutValidatorRefused checks a factory
// with no settings validator wired (a lean embedder) refuses any Objstore
// block before the catalog write, with the not-wired error.
func TestManager_Create_ObjstoreBlockWithoutValidatorRefused(t *testing.T) {
	for _, backend := range []BackendType{BackendObjstore, BackendMemory} {
		t.Run(string(backend), func(t *testing.T) {
			mgr, cat := newCredentialTestManager(t)

			_, err := mgr.Create(DatabaseConfig{Name: "blobs", Backend: backend, Objstore: &DatabaseObjstoreConfig{
				Provider: "local",
				Settings: map[string]string{"path": t.TempDir()},
			}})
			var notWired *ObjectStorageNotWiredError
			require.ErrorAs(t, err, &notWired)
			assert.Equal(t, "blobs", notWired.Name)
			assert.Zero(t, cat.creates.Load(), "an unvalidated Objstore block reached Catalog.Create")
		})
	}
}

// TestManager_Create_ValidatedObjstoreSettingsReachCatalog checks settings
// the validator accepts are written to the catalog.
func TestManager_Create_ValidatedObjstoreSettingsReachCatalog(t *testing.T) {
	mgr, cat := newCredentialTestManager(t, WithObjectStorageValidator(refuseSecretKey))

	_, err := mgr.Create(DatabaseConfig{Name: "blobs", Backend: BackendMemory, Objstore: &DatabaseObjstoreConfig{
		Provider: "s3", Settings: map[string]string{"bucket": "b", "region": "r"},
		KeyFile: "/run/key", SecretFile: "/run/secret",
	}})
	require.NoError(t, err)
	assert.Equal(t, int32(1), cat.creates.Load())
}
