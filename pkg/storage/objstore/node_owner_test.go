package objstore

import (
	"bytes"
	"errors"
	"sync"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objmemory "github.com/jeremyhahn/go-objstore/pkg/memory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedBucketCreator returns a creator whose stores are shared per
// "bucket" setting, as two clients of one real bucket are.
func sharedBucketCreator() func(map[string]string, objcommon.Credentials) (objcommon.Storage, error) {
	var mu sync.Mutex
	buckets := map[string]objcommon.Storage{}
	return func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
		mu.Lock()
		defer mu.Unlock()
		if s, ok := buckets[settings["bucket"]]; ok {
			return s, nil
		}
		s := objmemory.New()
		if err := s.Configure(settings, creds); err != nil {
			return nil, err
		}
		buckets[settings["bucket"]] = s
		return s, nil
	}
}

// ownerRefusal reports the owner an open was refused for, whether the
// refusal came from the namespace's owner marker (NamespaceOwnedError) or
// from another owner holding it open in this process (NamespaceInUseError).
func ownerRefusal(err error) (want string, refused bool) {
	var owned *NamespaceOwnedError
	if errors.As(err, &owned) {
		return owned.Want, true
	}
	var inUse *NamespaceInUseError
	if errors.As(err, &inUse) {
		return inUse.Want, true
	}
	return "", false
}

// TestNodeStoreRefusesDatabaseOpen proves a database open onto the node's
// own store is refused, on one local path and on one bucket's settings,
// while the node store is open and after it closed, and the node's objects
// are untouched.
func TestNodeStoreRefusesDatabaseOpen(t *testing.T) {
	const db = `database:"t"/"d"`
	provider := "nodeownerfake"
	require.NoError(t, RegisterProvider(provider, ProviderCredentials{}, sharedBucketCreator()))

	cases := []struct {
		name     string
		provider string
		settings map[string]string
	}{
		{"local path", "local", map[string]string{"path": t.TempDir()}},
		{"bucket settings", provider, map[string]string{"bucket": "b1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			node, err := New(tc.provider, tc.settings, WithOwner(NodeStoreOwner))
			require.NoError(t, err)
			require.NoError(t, node.Set([]byte("k"), []byte("node")))

			// Open in this process by the node: a database owner and an
			// unowned open are both refused, not shared.
			for _, owner := range []string{db, ""} {
				_, err = New(tc.provider, tc.settings, WithOwner(owner))
				var inUse *NamespaceInUseError
				require.ErrorAs(t, err, &inUse, "owner %q", owner)
				assert.Equal(t, NodeStoreOwner, inUse.Held)
				assert.Equal(t, owner, inUse.Want)
			}
			// The node itself shares its own namespace.
			again, err := New(tc.provider, tc.settings, WithOwner(NodeStoreOwner))
			require.NoError(t, err)
			require.NoError(t, again.Close())

			got, err := node.Get([]byte("k"))
			require.NoError(t, err)
			assert.True(t, bytes.Equal([]byte("node"), got))
			require.NoError(t, node.Close())

			// Closed, the namespace's owner marker still refuses a database.
			_, err = New(tc.provider, tc.settings, WithOwner(db))
			var inUse *NamespaceInUseError
			require.ErrorAs(t, err, &inUse)
			assert.Equal(t, NodeStoreOwner, inUse.Held)

			node, err = New(tc.provider, tc.settings, WithOwner(NodeStoreOwner))
			require.NoError(t, err)
			got, err = node.Get([]byte("k"))
			require.NoError(t, err)
			assert.True(t, bytes.Equal([]byte("node"), got))
			require.NoError(t, node.Close())
		})
	}
}

// TestNamespaceSharedOnlyByOneOwner proves two databases cannot hold one
// local root open at once, and a closed engine releases its hold.
func TestNamespaceSharedOnlyByOneOwner(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}
	a, err := New("local", settings)
	require.NoError(t, err)
	_, err = New("local", settings, WithOwner("database:a"))
	var inUse *NamespaceInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, "", inUse.Held)
	require.NoError(t, a.Close())

	b, err := New("local", settings, WithOwner("database:a"))
	require.NoError(t, err)
	require.NoError(t, b.Close())
}

// TestNew_MemoryWithSettingsIsNotShared: the memory provider builds an
// independent store on every open, so settings do not make two engines of
// it one namespace: both owners open, and neither sees the other's keys.
func TestNew_MemoryWithSettingsIsNotShared(t *testing.T) {
	settings := map[string]string{"multipartExpiry": "1h"}
	a, err := New("memory", settings, WithOwner("database:a"))
	require.NoError(t, err)
	b, err := New("memory", settings, WithOwner("database:b"))
	require.NoError(t, err)

	require.NoError(t, a.Set([]byte("k"), []byte("v")))
	ok, err := b.Exists([]byte("k"))
	require.NoError(t, err)
	assert.False(t, ok)
	require.NoError(t, a.Close())
	require.NoError(t, b.Close())
}
