// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package objstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	objcommon "github.com/jeremyhahn/go-objstore/pkg/common"
	objfactory "github.com/jeremyhahn/go-objstore/pkg/factory"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// reclaimStore is an in-memory provider whose List and per-object Delete can
// be made to fail, the way a provider refusing a request does.
type reclaimStore struct {
	objcommon.Storage
	listFails   bool
	deleteFails map[string]bool // object name -> Delete fails
}

func (r *reclaimStore) List(prefix string) ([]string, error) {
	if r.listFails {
		return nil, errProvider
	}
	return r.Storage.List(prefix)
}

func (r *reclaimStore) Delete(name string) error {
	if r.deleteFails[name] {
		return errProvider
	}
	return r.Storage.Delete(name)
}

func newReclaimStore(t *testing.T) *reclaimStore {
	t.Helper()
	inner, err := objfactory.NewStorage("memory", map[string]string{}, objcommon.Credentials{})
	require.NoError(t, err)
	return &reclaimStore{Storage: inner, deleteFails: map[string]bool{}}
}

// newReclaimEngine is an engine over a dedicated store and a separate journal
// store, holding two keys and a pending commit journal.
func newReclaimEngine(t *testing.T) (*Engine, *reclaimStore, *reclaimStore) {
	t.Helper()
	store, journal := newReclaimStore(t), newReclaimStore(t)
	e := &Engine{store: store, journal: journal, provider: "memory"}
	require.NoError(t, e.ensureFormatMarker())
	require.NoError(t, e.Set([]byte("a"), []byte("1")))
	require.NoError(t, e.Set([]byte("b"), []byte("2")))
	require.NoError(t, journal.Storage.Put(journalName, strings.NewReader("pending")))
	e.journalPending.Store(true)
	return e, store, journal
}

func names(t *testing.T, s *reclaimStore) []string {
	t.Helper()
	got, err := s.Storage.List("")
	require.NoError(t, err)
	return got
}

var _ storage.Reclaimer = (*Engine)(nil)

func TestReclaim_DeletesEveryObject(t *testing.T) {
	e, store, journal := newReclaimEngine(t)

	require.NoError(t, e.Reclaim())
	require.Empty(t, names(t, store))
	require.Empty(t, names(t, journal))
	require.False(t, e.journalPending.Load())

	// The engine stays open and empty.
	keys, err := e.List(nil)
	require.NoError(t, err)
	require.Empty(t, keys)
	require.NoError(t, e.Close())
}

func TestReclaim_EmptyStoreWithoutJournal(t *testing.T) {
	store, journal := newReclaimStore(t), newReclaimStore(t)
	e := &Engine{store: store, journal: journal, provider: "memory"}
	require.NoError(t, e.ensureFormatMarker())

	// No journal object exists: its not-found delete is not an error.
	require.NoError(t, e.Reclaim())
	require.Empty(t, names(t, store))
	// Reclaiming an already empty store succeeds again.
	require.NoError(t, e.Reclaim())
}

func TestReclaim_ClosedEngine(t *testing.T) {
	e, store, _ := newReclaimEngine(t)
	require.NoError(t, e.Close())

	_, closedErr := e.Get([]byte("a"))
	require.Error(t, closedErr)
	require.Equal(t, closedErr, e.Reclaim())
	require.NotEmpty(t, names(t, store))
}

// TestReclaim_RefusesLaterWrites pins that no write lands between a
// reclamation and Close: every write path is refused once Reclaim has run, so
// the reclaimed namespace stays empty.
func TestReclaim_RefusesLaterWrites(t *testing.T) {
	e, store, journal := newReclaimEngine(t)

	require.NoError(t, e.Reclaim())
	var closedErr *storage.ClosedError

	require.ErrorAs(t, e.Set([]byte("k"), []byte("v")), &closedErr)
	require.ErrorAs(t, e.Delete([]byte("k")), &closedErr)
	require.ErrorAs(t, e.DeleteRange([]byte("a"), nil), &closedErr)
	_, err := e.CompareAndSwap(context.Background(), []byte("k"), nil, []byte("v"))
	require.ErrorAs(t, err, &closedErr)
	b := e.Batch()
	require.NoError(t, b.Set([]byte("k"), []byte("v")))
	require.ErrorAs(t, b.Commit(), &closedErr)

	require.Empty(t, names(t, store))
	require.Empty(t, names(t, journal))
	// A retry of the reclamation still runs, and Close releases the engine.
	require.NoError(t, e.Reclaim())
	require.NoError(t, e.Close())
}

func TestReclaim_ProviderFaults(t *testing.T) {
	cases := map[string]struct {
		fault func(t *testing.T, e *Engine, store, journal *reclaimStore)
		// markerKept: the layout mark is still in place after the failure.
		markerKept bool
		// pending: the engine still records a pending journal.
		pending bool
	}{
		"list": {
			fault:      func(_ *testing.T, _ *Engine, store, _ *reclaimStore) { store.listFails = true },
			markerKept: true, pending: true,
		},
		"key delete": {
			fault: func(_ *testing.T, _ *Engine, store, _ *reclaimStore) {
				name, _ := objectName([]byte("a"))
				store.deleteFails[name] = true
			},
			markerKept: true, pending: true,
		},
		"journal delete": {
			fault: func(_ *testing.T, _ *Engine, _, journal *reclaimStore) {
				journal.deleteFails[journalName] = true
			},
			markerKept: true, pending: true,
		},
		"marker delete": {
			fault: func(_ *testing.T, _ *Engine, store, _ *reclaimStore) {
				store.deleteFails[formatMarkerName] = true
			},
			markerKept: true, pending: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e, store, journal := newReclaimEngine(t)
			tc.fault(t, e, store, journal)

			require.ErrorIs(t, e.Reclaim(), errProvider)
			require.Equal(t, tc.markerKept, contains(names(t, store), formatMarkerName))
			require.Equal(t, tc.pending, e.journalPending.Load())

			// A retry once the provider recovers deletes the rest.
			store.listFails = false
			clear(store.deleteFails)
			clear(journal.deleteFails)
			require.NoError(t, e.Reclaim())
			require.Empty(t, names(t, store))
			require.Empty(t, names(t, journal))
		})
	}
}

// TestReclaim_InterruptedReopensForCleanup covers a reclamation that stops
// after the format marker is gone but before the owner marker is: by a failed
// owner-marker delete, or by a crash between the two deletes, which leaves a
// namespace holding only the owner marker. Its owner reopens it and reclaims
// the rest; another owner is still refused.
func TestReclaim_InterruptedReopensForCleanup(t *testing.T) {
	cases := map[string]func(t *testing.T) (*reclaimStore, *reclaimStore){
		// Reclaim keeps the owner marker: the drop releases it only after
		// the catalog record is gone (ReleaseOwner), so a drop that stops
		// between the two leaves the namespace its owner's.
		"drop stops after the reclamation": func(t *testing.T) (*reclaimStore, *reclaimStore) {
			e, store, journal := newReclaimEngine(t)
			require.NoError(t, e.claimOwner("db-a"))
			require.NoError(t, e.Reclaim())
			return store, journal
		},
		"owner marker delete fails": func(t *testing.T) (*reclaimStore, *reclaimStore) {
			e, store, journal := newReclaimEngine(t)
			e.owner = "db-a"
			require.NoError(t, e.claimOwner("db-a"))
			store.deleteFails[ownerMarkerName] = true
			require.ErrorIs(t, e.ReleaseOwner(), errProvider)
			clear(store.deleteFails)
			return store, journal
		},
		"crash between the marker deletes": func(t *testing.T) (*reclaimStore, *reclaimStore) {
			store, journal := newReclaimStore(t), newReclaimStore(t)
			require.NoError(t, store.Storage.Put(ownerMarkerName, strings.NewReader("db-a")))
			return store, journal
		},
	}
	for name, interrupt := range cases {
		t.Run(name, func(t *testing.T) {
			store, journal := interrupt(t)
			require.Equal(t, []string{ownerMarkerName}, names(t, store))

			other := &Engine{store: store, journal: journal, provider: "memory", owner: "db-b"}
			var owned *NamespaceOwnedError
			require.True(t, errors.As(other.claimNamespace("db-b"), &owned))
			assert.Equal(t, "db-a", owned.Owner)

			reopened := &Engine{store: store, journal: journal, provider: "memory", owner: "db-a"}
			require.NoError(t, reopened.claimNamespace("db-a"))
			require.NoError(t, reopened.Reclaim())
			require.Equal(t, []string{ownerMarkerName}, names(t, store))
			require.NoError(t, reopened.ReleaseOwner())
			require.Empty(t, names(t, store))
			require.Empty(t, names(t, journal))

			// The released namespace is free for another owner.
			next := &Engine{store: store, journal: journal, provider: "memory", owner: "db-b"}
			require.NoError(t, next.claimNamespace("db-b"))
		})
	}
}

// TestReleaseOwner_Rules pins what ReleaseOwner deletes: this owner's
// records and markers, and the owner marker only when it names this owner
// and nothing foreign remains in the namespace.
func TestReleaseOwner_Rules(t *testing.T) {
	t.Run("own namespace with records", func(t *testing.T) {
		e, store, journal := newReclaimEngine(t)
		e.owner = "db-a"
		require.NoError(t, e.claimOwner("db-a"))
		require.NoError(t, e.ReleaseOwner())
		require.Empty(t, names(t, store))
		require.Empty(t, names(t, journal))
		// Nothing is written after the release.
		require.Error(t, e.Set([]byte("c"), []byte("3")))
	})
	t.Run("foreign object keeps the owner marker", func(t *testing.T) {
		e, store, _ := newReclaimEngine(t)
		e.owner = "db-a"
		require.NoError(t, e.claimOwner("db-a"))
		require.NoError(t, store.Storage.Put("foreign/x", strings.NewReader("x")))
		require.NoError(t, e.ReleaseOwner())
		assert.ElementsMatch(t, []string{"foreign/x", ownerMarkerName}, names(t, store))
	})
	t.Run("another owner's marker stays", func(t *testing.T) {
		e, store, _ := newReclaimEngine(t)
		e.owner = "db-a"
		require.NoError(t, store.Storage.Put(ownerMarkerName, strings.NewReader("db-b")))
		require.NoError(t, e.ReleaseOwner())
		require.Equal(t, []string{ownerMarkerName}, names(t, store))
	})
	t.Run("unowned engine leaves the marker", func(t *testing.T) {
		e, store, _ := newReclaimEngine(t)
		require.NoError(t, store.Storage.Put(ownerMarkerName, strings.NewReader("db-a")))
		require.NoError(t, e.ReleaseOwner())
		require.Equal(t, []string{ownerMarkerName}, names(t, store))
	})
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
