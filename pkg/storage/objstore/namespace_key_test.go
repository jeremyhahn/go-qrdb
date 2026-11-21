package objstore

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNamespaceKey_LocationOnly covers the owner lock keyed by the settings
// that address the store: a setting that tunes the provider, not where its
// objects live, does not split one namespace into two locks.
func TestNamespaceKey_LocationOnly(t *testing.T) {
	t.Run("local lifecycle setting ignored", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "p")
		first, err := openLocalOwned(t, map[string]string{"path": p, "runLifecycle": "false"}, "db-x")
		require.NoError(t, err)
		defer func() { require.NoError(t, first.Close()) }()

		_, err = openLocalOwned(t, map[string]string{"path": p}, "db-y")
		_, refused := ownerRefusal(err)
		require.True(t, refused, "got %v", err)
	})
	t.Run("two local paths two keys", func(t *testing.T) {
		assert.NotEqual(t,
			namespaceKey("local", map[string]string{"path": "/one"}),
			namespaceKey("local", map[string]string{"path": "/two"}))
	})
	t.Run("s3 forcePathStyle ignored", func(t *testing.T) {
		base := map[string]string{"bucket": "b", "endpoint": "http://s3.invalid", "region": "r"}
		styled := map[string]string{"bucket": "b", "endpoint": "http://s3.invalid", "region": "r", "forcePathStyle": "true"}
		assert.Equal(t, namespaceKey("s3", base), namespaceKey("s3", styled))
		assert.NotEqual(t, namespaceKey("s3", base),
			namespaceKey("s3", map[string]string{"bucket": "c", "endpoint": "http://s3.invalid", "region": "r"}))
	})
	t.Run("unlisted provider keeps every setting", func(t *testing.T) {
		assert.NotEqual(t,
			namespaceKey("custom", map[string]string{"a": "1"}),
			namespaceKey("custom", map[string]string{"a": "1", "b": "2"}))
	})
}

func openLocalOwned(t *testing.T, settings map[string]string, owner string) (*Engine, error) {
	t.Helper()
	return New("local", settings, WithOwner(owner))
}
