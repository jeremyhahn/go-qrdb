package objstore

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNew_OwnerClaimsNamespace drives WithOwner through New: the first owner
// claims the provider namespace, the same owner reopens it, and a different
// owner is refused with *NamespaceOwnedError.
func TestNew_OwnerClaimsNamespace(t *testing.T) {
	settings := map[string]string{"path": t.TempDir()}

	first, err := New("local", settings, WithOwner("db-a"))
	require.NoError(t, err)
	require.NoError(t, first.Close())

	tests := []struct {
		name    string
		owner   string
		wantErr bool
	}{
		{name: "same owner reopens", owner: "db-a"},
		{name: "no owner skips the claim", owner: ""},
		{name: "other owner refused", owner: "db-b", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e, err := New("local", settings, WithOwner(tc.owner))
			if !tc.wantErr {
				require.NoError(t, err)
				require.NoError(t, e.Close())
				return
			}
			var owned *NamespaceOwnedError
			require.True(t, errors.As(err, &owned), "got %v", err)
			assert.Equal(t, "local", owned.Provider)
			assert.Equal(t, "db-a", owned.Owner)
			assert.Equal(t, "db-b", owned.Want)
			assert.Contains(t, owned.Error(), "db-a")
			assert.Contains(t, owned.Error(), "db-b")
		})
	}
}

// TestNew_FreshlyClaimed pins the flag a failed create reads to decide
// whether it may release the namespace: true only for an owned open that
// wrote the owner marker onto a namespace holding nothing before.
func TestNew_FreshlyClaimed(t *testing.T) {
	open := func(t *testing.T, path, owner string) *Engine {
		t.Helper()
		e, err := New("local", map[string]string{"path": path}, WithOwner(owner))
		require.NoError(t, err)
		t.Cleanup(func() { _ = e.Close() })
		return e
	}
	t.Run("new namespace", func(t *testing.T) {
		assert.True(t, open(t, t.TempDir(), "db-a").FreshlyClaimed())
	})
	t.Run("reopen by the same owner", func(t *testing.T) {
		path := t.TempDir()
		first := open(t, path, "db-a")
		require.NoError(t, first.Close())
		assert.False(t, open(t, path, "db-a").FreshlyClaimed())
	})
	t.Run("unowned data claimed", func(t *testing.T) {
		path := t.TempDir()
		unowned := open(t, path, "")
		require.NoError(t, unowned.Set([]byte("k"), []byte("v")))
		require.NoError(t, unowned.Close())
		assert.False(t, open(t, path, "db-a").FreshlyClaimed())
	})
	t.Run("unowned open", func(t *testing.T) {
		assert.False(t, open(t, t.TempDir(), "").FreshlyClaimed())
	})
}

// TestClaimOwner_ProviderFaults covers the provider failures on the claim:
// the marker read, the marker body and the marker write.
func TestClaimOwner_ProviderFaults(t *testing.T) {
	t.Run("marker read fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		fs.getFails[ownerMarkerName] = true
		assert.ErrorIs(t, e.claimOwner("db-a"), errProvider)
	})
	t.Run("marker body unreadable", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, e.claimOwner("db-a"))
		fs.readFails[ownerMarkerName] = true
		assert.ErrorIs(t, e.claimOwner("db-a"), errProvider)
	})
	t.Run("claim is idempotent", func(t *testing.T) {
		e, _ := newFaultEngine(t)
		require.NoError(t, e.claimOwner("db-a"))
		require.NoError(t, e.claimOwner("db-a"))
		var owned *NamespaceOwnedError
		assert.True(t, errors.As(e.claimOwner("db-b"), &owned))
	})
}
