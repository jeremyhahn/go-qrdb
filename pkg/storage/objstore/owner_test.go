package objstore

import (
	"errors"
	"io"
	"strings"
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

// TestEngine_ReleaseClaim pins what a failed create gives back: a namespace
// this open claimed new is released whole; on a namespace that already held
// data only the owner marker this open wrote goes, the data and the format
// marker stay; a namespace this open did not claim is left alone.
func TestEngine_ReleaseClaim(t *testing.T) {
	open := func(t *testing.T, path, owner string) *Engine {
		t.Helper()
		e, err := New("local", map[string]string{"path": path}, WithOwner(owner))
		require.NoError(t, err)
		t.Cleanup(func() { _ = e.Close() })
		return e
	}
	objects := func(t *testing.T, e *Engine) []string {
		t.Helper()
		names, err := e.store.List("")
		require.NoError(t, err)
		return names
	}
	seedUnowned := func(t *testing.T, path string) {
		t.Helper()
		unowned := open(t, path, "")
		require.NoError(t, unowned.Set([]byte("k"), []byte("v")))
		require.NoError(t, unowned.Close())
	}
	t.Run("new namespace", func(t *testing.T) {
		e := open(t, t.TempDir(), "db-a")
		require.NoError(t, e.ReleaseClaim())
		assert.Empty(t, objects(t, e))
	})
	t.Run("reopen by the same owner", func(t *testing.T) {
		path := t.TempDir()
		first := open(t, path, "db-a")
		require.NoError(t, first.Close())
		e := open(t, path, "db-a")
		require.NoError(t, e.ReleaseClaim())
		assert.Contains(t, objects(t, e), ownerMarkerName)
	})
	t.Run("unowned data claimed", func(t *testing.T) {
		path := t.TempDir()
		seedUnowned(t, path)
		e := open(t, path, "db-a")
		before := objects(t, e)
		require.Contains(t, before, ownerMarkerName)
		require.NoError(t, e.ReleaseClaim())
		after := objects(t, e)
		assert.NotContains(t, after, ownerMarkerName)
		assert.Contains(t, after, formatMarkerName)
		assert.Len(t, after, len(before)-1, "only the owner marker is removed")
		got, err := e.Get([]byte("k"))
		require.NoError(t, err)
		assert.Equal(t, []byte("v"), got)
	})
	t.Run("marker naming another owner", func(t *testing.T) {
		path := t.TempDir()
		seedUnowned(t, path)
		e := open(t, path, "db-a")
		require.NoError(t, e.store.Put(ownerMarkerName, strings.NewReader("db-b")))
		require.NoError(t, e.ReleaseClaim())
		rc, err := e.store.Get(ownerMarkerName)
		require.NoError(t, err)
		found, err := io.ReadAll(rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)
		assert.Equal(t, "db-b", string(found))
	})
	t.Run("unowned open", func(t *testing.T) {
		e := open(t, t.TempDir(), "")
		require.NoError(t, e.Set([]byte("k"), []byte("v")))
		before := objects(t, e)
		require.NoError(t, e.ReleaseClaim())
		assert.Equal(t, before, objects(t, e))
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
