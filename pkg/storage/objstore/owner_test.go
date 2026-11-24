package objstore

import (
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
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

// claimUndoStore is a shared in-memory bucket whose journal read and owner
// marker delete fail while armed: an open whose claim landed and whose
// journal recovery then failed, and the undo of that claim.
type claimUndoStore struct {
	objcommon.Storage
	journalGetFails  *atomic.Bool
	ownerDeleteFails *atomic.Bool
}

func (s claimUndoStore) Get(name string) (io.ReadCloser, error) {
	if name == journalName && s.journalGetFails.Load() {
		return nil, errProvider
	}
	return s.Storage.Get(name)
}

func (s claimUndoStore) Delete(name string) error {
	if name == ownerMarkerName && s.ownerDeleteFails.Load() {
		return errProvider
	}
	return s.Storage.Delete(name)
}

// claimUndoProvider registers a provider over claimUndoStore and returns its
// name, the raw bucket opener and the two fault switches.
func claimUndoProvider(t *testing.T) (string, func(bucket string) objcommon.Storage, *atomic.Bool, *atomic.Bool) {
	t.Helper()
	var journalGetFails, ownerDeleteFails atomic.Bool
	shared := sharedBucketCreator()
	provider := "owner-test-claim-undo-" + strings.ReplaceAll(t.Name(), "/", "-")
	require.NoError(t, RegisterProvider(provider, ProviderCredentials{},
		func(settings map[string]string, creds objcommon.Credentials) (objcommon.Storage, error) {
			inner, err := shared(settings, creds)
			if err != nil {
				return nil, err
			}
			return claimUndoStore{Storage: inner, journalGetFails: &journalGetFails, ownerDeleteFails: &ownerDeleteFails}, nil
		}))
	raw := func(bucket string) objcommon.Storage {
		s, err := shared(map[string]string{"bucket": bucket}, objcommon.Credentials{})
		require.NoError(t, err)
		return s
	}
	return provider, raw, &journalGetFails, &ownerDeleteFails
}

func bucketNames(t *testing.T, s objcommon.Storage) []string {
	t.Helper()
	names, err := s.List("")
	require.NoError(t, err)
	return names
}

// TestNew_JournalRecoveryFailureReleasesClaim: an open that claimed its
// namespace and then failed (journal recovery) gives the claim back, as
// ReleaseClaim does: a fresh namespace whole, and on data already there only
// the owner marker it wrote.
func TestNew_JournalRecoveryFailureReleasesClaim(t *testing.T) {
	t.Run("fresh namespace", func(t *testing.T) {
		provider, raw, journalGetFails, _ := claimUndoProvider(t)
		settings := map[string]string{"bucket": "fresh"}
		journalGetFails.Store(true)
		_, err := New(provider, settings, WithOwner("db-a"))
		require.ErrorIs(t, err, errProvider)
		assert.Empty(t, bucketNames(t, raw("fresh")), "the failed open left objects")
		journalGetFails.Store(false)
		e, err := New(provider, settings, WithOwner("db-b"))
		require.NoError(t, err, "another owner is refused a namespace no database holds")
		require.NoError(t, e.Close())
	})
	t.Run("pre-existing data", func(t *testing.T) {
		provider, raw, journalGetFails, _ := claimUndoProvider(t)
		settings := map[string]string{"bucket": "used"}
		unowned, err := New(provider, settings)
		require.NoError(t, err)
		require.NoError(t, unowned.Set([]byte("k"), []byte("v")))
		require.NoError(t, unowned.Close())
		before := bucketNames(t, raw("used"))
		journalGetFails.Store(true)
		_, err = New(provider, settings, WithOwner("db-a"))
		require.ErrorIs(t, err, errProvider)
		assert.ElementsMatch(t, before, bucketNames(t, raw("used")), "the failed open must remove only the owner marker it wrote")
		assert.NotContains(t, bucketNames(t, raw("used")), ownerMarkerName)
	})
}

// TestNew_ClaimUndoFailureIsReported: when giving the claim back fails too,
// New's error is *ClaimUndoError carrying both the open's cause and the
// undo's, so the caller can record the namespace for a later release.
func TestNew_ClaimUndoFailureIsReported(t *testing.T) {
	provider, raw, journalGetFails, ownerDeleteFails := claimUndoProvider(t)
	journalGetFails.Store(true)
	ownerDeleteFails.Store(true)
	_, err := New(provider, map[string]string{"bucket": "stuck"}, WithOwner("db-a"))
	var undo *ClaimUndoError
	require.ErrorAs(t, err, &undo)
	assert.ErrorIs(t, undo.Err, errProvider)
	assert.ErrorIs(t, undo.Undo, errProvider)
	assert.Contains(t, bucketNames(t, raw("stuck")), ownerMarkerName)
}

// ownerMarkerFaultStore fails a Get or a Delete of the owner marker with
// errProvider, as a provider fault does; every other call reaches the
// wrapped store.
type ownerMarkerFaultStore struct {
	objcommon.Storage
	getFails, deleteFails bool
}

func (s ownerMarkerFaultStore) Get(name string) (io.ReadCloser, error) {
	if s.getFails && name == ownerMarkerName {
		return nil, errProvider
	}
	return s.Storage.Get(name)
}

func (s ownerMarkerFaultStore) Delete(name string) error {
	if s.deleteFails && name == ownerMarkerName {
		return errProvider
	}
	return s.Storage.Delete(name)
}

// ownerMarkerEngine is an engine owned by owner on a memory store wrapped
// in faults, with the given owner marker written ("" writes none).
func ownerMarkerEngine(t *testing.T, owner, marker string, faults ownerMarkerFaultStore) *Engine {
	t.Helper()
	inner := newReclaimStore(t)
	if marker != "" {
		require.NoError(t, inner.Put(ownerMarkerName, strings.NewReader(marker)))
	}
	faults.Storage = inner
	return &Engine{store: faults, journal: newReclaimStore(t), provider: "memory", owner: owner}
}

func TestEngine_ReleaseOwnerMarker(t *testing.T) {
	open := func(t *testing.T, path string) *Engine {
		t.Helper()
		e, err := New("local", map[string]string{"path": path}, WithOwner("db-a"))
		require.NoError(t, err)
		t.Cleanup(func() { _ = e.Close() })
		return e
	}
	t.Run("own marker", func(t *testing.T) {
		e := open(t, t.TempDir())
		require.NoError(t, e.Set([]byte("k"), []byte("v")))
		before := bucketNames(t, e.store)
		require.Contains(t, before, ownerMarkerName)
		require.NoError(t, e.ReleaseOwnerMarker())
		after := bucketNames(t, e.store)
		assert.NotContains(t, after, ownerMarkerName)
		assert.Contains(t, after, formatMarkerName)
		assert.Len(t, after, len(before)-1, "only the owner marker is removed")
		got, err := e.Get([]byte("k"))
		require.NoError(t, err)
		assert.Equal(t, []byte("v"), got)
	})
	t.Run("marker naming another owner", func(t *testing.T) {
		e := open(t, t.TempDir())
		require.NoError(t, e.store.Put(ownerMarkerName, strings.NewReader("db-b")))
		require.NoError(t, e.ReleaseOwnerMarker())
		rc, err := e.store.Get(ownerMarkerName)
		require.NoError(t, err)
		found, err := io.ReadAll(rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)
		assert.Equal(t, "db-b", string(found))
	})
	t.Run("marker already gone", func(t *testing.T) {
		e := open(t, t.TempDir())
		require.NoError(t, e.ReleaseOwnerMarker())
		require.NoError(t, e.ReleaseOwnerMarker())
	})
	t.Run("closed engine", func(t *testing.T) {
		e := open(t, t.TempDir())
		require.NoError(t, e.Close())
		assert.Error(t, e.ReleaseOwnerMarker())
	})
	t.Run("get fault", func(t *testing.T) {
		e := ownerMarkerEngine(t, "db-a", "db-a", ownerMarkerFaultStore{getFails: true})
		assert.ErrorIs(t, e.ReleaseOwnerMarker(), errProvider)
	})
	t.Run("delete fault", func(t *testing.T) {
		e := ownerMarkerEngine(t, "db-a", "db-a", ownerMarkerFaultStore{deleteFails: true})
		assert.ErrorIs(t, e.ReleaseOwnerMarker(), errProvider)
	})
}

func TestEngine_FreshlyClaimed(t *testing.T) {
	fresh, err := New("local", map[string]string{"path": t.TempDir()}, WithOwner("db-a"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = fresh.Close() })
	assert.True(t, fresh.FreshlyClaimed(), "an empty namespace is claimed fresh")

	path := t.TempDir()
	unowned, err := New("local", map[string]string{"path": path})
	require.NoError(t, err)
	require.NoError(t, unowned.Set([]byte("k"), []byte("v")))
	require.NoError(t, unowned.Close())
	held, err := New("local", map[string]string{"path": path}, WithOwner("db-a"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = held.Close() })
	assert.False(t, held.FreshlyClaimed(), "a namespace holding data is not claimed fresh")
}

func TestAcceptReclaimedRemains_Rules(t *testing.T) {
	t.Run("unowned, only the owner marker", func(t *testing.T) {
		e := ownerMarkerEngine(t, "", "", ownerMarkerFaultStore{})
		assert.NoError(t, e.acceptReclaimedRemains([]string{ownerMarkerName}, ""))
	})
	t.Run("unowned, other objects", func(t *testing.T) {
		e := ownerMarkerEngine(t, "", "", ownerMarkerFaultStore{})
		var missing *FormatMarkerMissingError
		assert.ErrorAs(t, e.acceptReclaimedRemains([]string{ownerMarkerName, "x"}, ""), &missing)
	})
	t.Run("owned, no owner marker", func(t *testing.T) {
		e := ownerMarkerEngine(t, "db-a", "", ownerMarkerFaultStore{})
		var missing *FormatMarkerMissingError
		assert.ErrorAs(t, e.acceptReclaimedRemains([]string{"x"}, "db-a"), &missing)
	})
	t.Run("owner marker get fault", func(t *testing.T) {
		e := ownerMarkerEngine(t, "db-a", "db-a", ownerMarkerFaultStore{getFails: true})
		assert.ErrorIs(t, e.acceptReclaimedRemains([]string{ownerMarkerName}, "db-a"), errProvider)
	})
	t.Run("another owner's marker", func(t *testing.T) {
		e := ownerMarkerEngine(t, "db-a", "db-b", ownerMarkerFaultStore{})
		var owned *NamespaceOwnedError
		require.ErrorAs(t, e.acceptReclaimedRemains([]string{ownerMarkerName}, "db-a"), &owned)
		assert.Equal(t, "db-b", owned.Owner)
		assert.Equal(t, "db-a", owned.Want)
	})
	t.Run("own marker", func(t *testing.T) {
		e := ownerMarkerEngine(t, "db-a", "db-a", ownerMarkerFaultStore{})
		assert.NoError(t, e.acceptReclaimedRemains([]string{ownerMarkerName}, "db-a"))
	})
}

func TestOwnerErrors_Markers(t *testing.T) {
	assert.False(t, (&NamespaceInUseError{Provider: "memory", Held: "db-a", Want: "db-a"}).NamespaceOwnedByAnother())
	assert.True(t, (&NamespaceInUseError{Provider: "memory", Held: "db-a", Want: "db-b"}).NamespaceOwnedByAnother())

	owned := &NamespaceOwnedError{Provider: "memory", Owner: "db-b", Want: "db-a"}
	assert.Equal(t, `objstore: "memory" namespace is owned by "db-b", not "db-a"`, owned.Error())
	assert.True(t, owned.NamespaceOwnedByAnother())

	cause, undoCause := errors.New("open cause"), errors.New("undo cause")
	undo := &ClaimUndoError{Provider: "memory", Err: cause, Undo: undoCause, Fresh: true}
	msg := undo.Error()
	assert.Contains(t, msg, `"memory"`)
	assert.Contains(t, msg, "open cause")
	assert.Contains(t, msg, "undo cause")
	assert.ErrorIs(t, undo, cause)
	assert.ErrorIs(t, undo, undoCause)
	assert.True(t, undo.NamespaceClaimUndoFailed())
	assert.True(t, undo.NamespaceClaimFresh())
	assert.False(t, (&ClaimUndoError{Provider: "memory", Err: cause, Undo: undoCause}).NamespaceClaimFresh())
}
