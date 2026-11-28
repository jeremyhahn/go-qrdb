package objstore

import (
	"os"
	"path/filepath"
	"strings"
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

// TestNamespaceKey_SymlinkAliasIsOneNamespace pins that the exported
// NamespaceKey names a local root and a symlink to it as one namespace,
// another root as another, and refuses a root that does not resolve.
func TestNamespaceKey_SymlinkAliasIsOneNamespace(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	require.NoError(t, os.Mkdir(target, 0o750))
	alias := filepath.Join(base, "alias")
	require.NoError(t, os.Symlink(target, alias))
	other := filepath.Join(base, "other")
	require.NoError(t, os.Mkdir(other, 0o750))
	key := func(root string) string {
		t.Helper()
		k, err := NamespaceKey("local", map[string]string{"path": root})
		require.NoError(t, err)
		return k
	}
	assert.Equal(t, key(target), key(alias), "a symlink alias and its target name two namespaces")
	assert.NotEqual(t, key(target), key(other), "two distinct roots name one namespace")
	_, err := NamespaceKey("local", map[string]string{"path": filepath.Join(base, "missing")})
	assert.Error(t, err, "a root that does not resolve has no namespace key")
}

// TestNamespaceKey_RefusesInlineCredentials pins that NamespaceKey refuses
// an inline credential setting as New does, without echoing its value.
func TestNamespaceKey_RefusesInlineCredentials(t *testing.T) {
	_, err := NamespaceKey("s3", map[string]string{"bucket": "b", "secret_key": inlineCredentialValue})
	require.Error(t, err)
	assert.False(t, strings.Contains(err.Error(), inlineCredentialValue), "the error echoes the credential value")
}
