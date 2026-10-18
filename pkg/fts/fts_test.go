//go:build qrdb_fts

package fts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	fileStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/file"
	"github.com/jeremyhahn/go-qrdb/pkg/storage/memory"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// ftsBackends opens each storage.Storage implementation the FTSIndex runs
// on, so every assertion below holds on all of them rather than on Pebble
// alone.
var ftsBackends = []struct {
	name string
	open func(t *testing.T) storage.Storage
}{
	{
		name: "memory",
		open: func(t *testing.T) storage.Storage { return memory.NewStorage() },
	},
	{
		name: "file",
		open: func(t *testing.T) storage.Storage {
			s, err := fileStorage.New(t.TempDir())
			require.NoError(t, err)
			return s
		},
	},
	{
		name: "pebble",
		open: func(t *testing.T) storage.Storage {
			s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
			require.NoError(t, err)
			return s
		},
	},
}

func newBackendFTSIndex(t *testing.T, open func(t *testing.T) storage.Storage) *FTSIndex {
	t.Helper()
	s := open(t)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return NewFTSIndex(s, "messages.body")
}

func TestFTSIndex_PerBackend_SearchAndRemove(t *testing.T) {
	for _, backend := range ftsBackends {
		t.Run(backend.name, func(t *testing.T) {
			index := newBackendFTSIndex(t, backend.open)
			require.NoError(t, index.IndexDocument([]byte("msg/1"), "hello world"))
			require.NoError(t, index.IndexDocument([]byte("msg/2"), "hello there friend"))

			andKeys, err := index.Search("hello world")
			require.NoError(t, err)
			assert.Equal(t, [][]byte{[]byte("msg/1")}, andKeys)

			orKeys, err := index.SearchOr("hello world")
			require.NoError(t, err)
			assert.Len(t, orKeys, 2)

			require.NoError(t, index.RemoveDocument([]byte("msg/1")))
			afterRemove, err := index.Search("hello")
			require.NoError(t, err)
			assert.Equal(t, [][]byte{[]byte("msg/2")}, afterRemove)
		})
	}
}

func TestFTSIndex_PerBackend_ReindexReplacesPreviousEntries(t *testing.T) {
	for _, backend := range ftsBackends {
		t.Run(backend.name, func(t *testing.T) {
			index := newBackendFTSIndex(t, backend.open)
			require.NoError(t, index.IndexDocument([]byte("msg/1"), "original coffee text"))
			require.NoError(t, index.IndexDocument([]byte("msg/1"), "replacement tea text"))

			stale, err := index.Search("coffee")
			require.NoError(t, err)
			assert.Empty(t, stale, "a reindexed document keeps no term of its previous text")

			fresh, err := index.Search("tea")
			require.NoError(t, err)
			assert.Equal(t, [][]byte{[]byte("msg/1")}, fresh)
		})
	}
}
