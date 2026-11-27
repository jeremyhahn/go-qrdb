// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package objstore

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openLocalStore opens the local provider's store on path for ClaimNamespace.
func openLocalStore(t *testing.T, path string) (map[string]string, objcommon.Storage) {
	t.Helper()
	settings := map[string]string{"path": path}
	store, err := OpenStore("local", settings)
	require.NoError(t, err)
	return settings, store
}

// claimLocal claims the local namespace at path for owner; the claim is
// released at cleanup.
func claimLocal(t *testing.T, path, owner string) (*NamespaceClaim, error) {
	t.Helper()
	settings, store := openLocalStore(t, path)
	c, err := ClaimNamespace("local", settings, store, owner)
	t.Cleanup(c.Release)
	return c, err
}

func TestClaimNamespace_NilStore(t *testing.T) {
	c, err := ClaimNamespace("local", map[string]string{"path": t.TempDir()}, nil, "db-a")
	var invalid *InvalidRemoteError
	require.ErrorAs(t, err, &invalid)
	assert.Nil(t, c)
}

func TestClaimNamespace_CanonicalNamespaceError(t *testing.T) {
	_, store := openLocalStore(t, t.TempDir())
	missing := filepath.Join(t.TempDir(), "absent")
	c, err := ClaimNamespace("local", map[string]string{"path": missing}, store, "db-a")
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, c)
}

func TestClaimNamespace_SecondOwnerInProcessRefused(t *testing.T) {
	path := t.TempDir()
	_, err := claimLocal(t, path, "db-a")
	require.NoError(t, err)
	c, err := claimLocal(t, path, "db-b")
	var inUse *NamespaceInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Nil(t, c)
	assert.Equal(t, `objstore: "local" namespace is in use by owner "db-a", refused for "db-b"`, inUse.Error())
}

func TestClaimNamespace_UnresolvableJournalRootRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data")
	require.NoError(t, os.Mkdir(path, 0o700))
	journal := path + journalTreeSuffix
	require.NoError(t, os.Symlink(journal, journal), "a self-referencing journal root never resolves")
	c, err := claimLocal(t, path, "db-a")
	require.Error(t, err)
	require.NotErrorIs(t, err, os.ErrNotExist)
	assert.Nil(t, c)
	require.NoError(t, os.Remove(journal))
	_, err = claimLocal(t, path, "db-b")
	require.NoError(t, err, "the refused claim dropped its in-process namespace hold")
}

func TestClaimNamespace_OpenEngineHoldsNamespace(t *testing.T) {
	path := t.TempDir()
	e, err := New("local", map[string]string{"path": path}, WithOwner("db-a"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	_, err = claimLocal(t, path, NodeStoreOwner)
	var inUse *NamespaceInUseError
	require.ErrorAs(t, err, &inUse)
}

func TestClaimNamespace_WalkedRootOverlapRefused(t *testing.T) {
	path := t.TempDir()
	e, err := New("local", map[string]string{"path": path}, WithOwner("db-a"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	nested := filepath.Join(path, "nested")
	require.NoError(t, os.Mkdir(nested, 0o700))
	_, err = claimLocal(t, nested, "db-b")
	var overlap *OverlappingRootError
	require.ErrorAs(t, err, &overlap)
	require.NoError(t, e.Close())
	// The refused claim dropped its in-process namespace hold.
	_, err = claimLocal(t, nested, "db-c")
	require.NoError(t, err)
}

func TestClaimNamespace_MarkerNamingAnotherOwnerRefused(t *testing.T) {
	path := t.TempDir()
	settings, store := openLocalStore(t, path)
	require.NoError(t, store.Put(ownerMarkerName, strings.NewReader("db-b")))
	c, err := ClaimNamespace("local", settings, store, "db-a")
	var owned *NamespaceOwnedError
	require.ErrorAs(t, err, &owned)
	assert.Nil(t, c)
	// Neither the namespace nor the roots stay held by the refused claim.
	_, err = claimLocal(t, path, "db-b")
	require.NoError(t, err)
}

func TestClaimNamespace_NodeStoreMarkerRefusesADatabase(t *testing.T) {
	path := t.TempDir()
	first, err := claimLocal(t, path, NodeStoreOwner)
	require.NoError(t, err)
	first.Release()
	_, err = claimLocal(t, path, "db-a")
	var inUse *NamespaceInUseError
	require.ErrorAs(t, err, &inUse)
	assert.Equal(t, NodeStoreOwner, inUse.Held)
}

func TestClaimNamespace_FailedMarkerPutRolledBack(t *testing.T) {
	t.Run("rolled back", func(t *testing.T) {
		provider, raw, _, _, putFails := claimUndoProvider(t)
		settings := map[string]string{"bucket": "fresh"}
		store, err := OpenStore(provider, settings)
		require.NoError(t, err)
		putFails.Store(true)
		c, err := ClaimNamespace(provider, settings, store, "db-a")
		require.ErrorIs(t, err, errProvider)
		assert.Nil(t, c)
		assert.NotContains(t, bucketNames(t, raw("fresh")), ownerMarkerName)
		putFails.Store(false)
		c, err = ClaimNamespace(provider, settings, store, "db-b")
		require.NoError(t, err, "the failed claim released its in-process hold")
		c.Release()
	})
	t.Run("rollback fails too", func(t *testing.T) {
		provider, raw, _, ownerDeleteFails, putFails := claimUndoProvider(t)
		settings := map[string]string{"bucket": "stuck"}
		store, err := OpenStore(provider, settings)
		require.NoError(t, err)
		putFails.Store(true)
		ownerDeleteFails.Store(true)
		_, err = ClaimNamespace(provider, settings, store, "db-a")
		require.ErrorIs(t, err, errProvider)
		assert.Contains(t, bucketNames(t, raw("stuck")), ownerMarkerName)
	})
}

func TestNamespaceClaim_ReleaseKeepsTheDurableMarker(t *testing.T) {
	path := t.TempDir()
	c, err := claimLocal(t, path, "db-a")
	require.NoError(t, err)
	c.Release()
	c.Release()
	_, err = claimLocal(t, path, "db-b")
	var owned *NamespaceOwnedError
	require.ErrorAs(t, err, &owned)
	again, err := claimLocal(t, path, "db-a")
	require.NoError(t, err, "the owner reclaims its namespace after Release")
	again.Release()
}

func TestNamespaceClaim_ReleaseNil(t *testing.T) {
	var c *NamespaceClaim
	c.Release()
	(&NamespaceClaim{}).Release()
}

func TestNamespaceClaim_ConcurrentRelease(t *testing.T) {
	path := t.TempDir()
	c, err := claimLocal(t, path, "db-a")
	require.NoError(t, err)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 8 {
		wg.Go(func() {
			<-start
			c.Release()
		})
	}
	close(start)
	wg.Wait()
	again, err := claimLocal(t, path, "db-a")
	require.NoError(t, err, "concurrent Releases dropped the hold")
	again.Release()
}

// listFaultStore fails every List: the namespace cannot be walked.
type listFaultStore struct {
	objcommon.Storage
}

func (listFaultStore) List(string) ([]string, error) { return nil, errProvider }

func TestEngine_ReleaseOwnerErrors(t *testing.T) {
	t.Run("closed engine", func(t *testing.T) {
		e, err := New("local", map[string]string{"path": t.TempDir()}, WithOwner("db-a"))
		require.NoError(t, err)
		require.NoError(t, e.Close())
		require.Error(t, e.ReleaseOwner())
	})
	t.Run("namespace walk fails", func(t *testing.T) {
		e, err := New("local", map[string]string{"path": t.TempDir()}, WithOwner("db-a"))
		require.NoError(t, err)
		inner := e.store
		e.store = listFaultStore{Storage: inner}
		require.ErrorIs(t, e.ReleaseOwner(), errProvider)
		e.store = inner
		require.NoError(t, e.Close())
	})
}
