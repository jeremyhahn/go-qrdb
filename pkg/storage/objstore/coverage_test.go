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
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Engine: Provider, error type, closed-state, empty-key ---

func TestEngine_Provider(t *testing.T) {
	e := newMemEngine(t)
	assert.Equal(t, "memory", e.Provider())
}

func TestUnsupportedProviderError_Error(t *testing.T) {
	err := &UnsupportedProviderError{Provider: "badprovider"}
	msg := err.Error()
	assert.Contains(t, msg, "badprovider")
	assert.Contains(t, msg, "objstore:")
}

func TestEngine_ClosedStateErrors(t *testing.T) {
	e, err := New("memory", map[string]string{})
	require.NoError(t, err)
	require.NoError(t, e.Close())

	var closedErr *storage.ClosedError

	t.Run("Delete on closed engine", func(t *testing.T) {
		assert.ErrorAs(t, e.Delete([]byte("k")), &closedErr)
	})

	t.Run("Exists on closed engine", func(t *testing.T) {
		_, err := e.Exists([]byte("k"))
		assert.ErrorAs(t, err, &closedErr)
	})

	t.Run("Snapshot on closed engine", func(t *testing.T) {
		_, err := e.Snapshot()
		assert.ErrorAs(t, err, &closedErr)
	})

	t.Run("List on closed engine", func(t *testing.T) {
		_, err := e.List(nil)
		assert.ErrorAs(t, err, &closedErr)
	})
}

func TestEngine_EmptyKeyErrors(t *testing.T) {
	e := newMemEngine(t)

	t.Run("Delete nil key", func(t *testing.T) {
		assert.ErrorIs(t, e.Delete(nil), storage.ErrEmptyKey)
	})

	t.Run("Exists nil key", func(t *testing.T) {
		_, err := e.Exists(nil)
		assert.ErrorIs(t, err, storage.ErrEmptyKey)
	})
}

// --- Batch: Close, closed-state errors, empty-key Delete, engine errors ---

func TestBatch_Close(t *testing.T) {
	e := newMemEngine(t)

	t.Run("Close uncommitted batch discards ops", func(t *testing.T) {
		b := e.Batch()
		require.NoError(t, b.Set([]byte("bck"), []byte("val")))
		require.NoError(t, b.Close())

		v, err := e.Get([]byte("bck"))
		require.NoError(t, err)
		assert.Nil(t, v, "ops discarded on Close must not reach the engine")
	})

	t.Run("Closing an already-closed batch is a no-op", func(t *testing.T) {
		b := e.Batch()
		require.NoError(t, b.Close())
		require.NoError(t, b.Close()) // second Close must not error
	})
}

func TestBatch_ClosedStateErrors(t *testing.T) {
	e := newMemEngine(t)
	var bErr *storage.BatchError

	tests := []struct {
		name string
		op   func(b storage.Batch) error
	}{
		{"Set on closed batch", func(b storage.Batch) error { return b.Set([]byte("k"), []byte("v")) }},
		{"Delete on closed batch", func(b storage.Batch) error { return b.Delete([]byte("k")) }},
		{"Commit on closed batch", func(b storage.Batch) error { return b.Commit() }},
		{"Rollback on closed batch", func(b storage.Batch) error { return b.Rollback() }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := e.Batch()
			require.NoError(t, b.Rollback()) // closes the batch
			require.ErrorAs(t, tc.op(b), &bErr)
		})
	}
}

func TestBatch_EmptyKeyDelete(t *testing.T) {
	e := newMemEngine(t)
	b := e.Batch()
	defer func() { _ = b.Close() }()

	var bErr *storage.BatchError
	require.ErrorAs(t, b.Delete(nil), &bErr)
}

func TestBatch_CommitEngineError(t *testing.T) {
	t.Run("Set op fails when engine is closed before commit", func(t *testing.T) {
		e := newMemEngine(t)
		b := e.Batch()
		require.NoError(t, b.Set([]byte("k"), []byte("v")))
		require.NoError(t, e.Close())

		err := b.Commit()
		var bErr *storage.BatchError
		require.ErrorAs(t, err, &bErr)
	})

	t.Run("Delete op fails when engine is closed before commit", func(t *testing.T) {
		e := newMemEngine(t)
		require.NoError(t, e.Set([]byte("k"), []byte("v")))
		b := e.Batch()
		require.NoError(t, b.Delete([]byte("k")))
		require.NoError(t, e.Close())

		err := b.Commit()
		var bErr *storage.BatchError
		require.ErrorAs(t, err, &bErr)
	})
}

// --- Iterator: empty store, SeekToLast, Seek, Prev, Valid, Value, Key ---

func TestIterator_EmptyStore(t *testing.T) {
	e := newMemEngine(t)

	t.Run("SeekToFirst on empty returns false", func(t *testing.T) {
		it := e.NewIterator(nil)
		defer func() { _ = it.Close() }()
		assert.False(t, it.SeekToFirst())
		assert.False(t, it.Valid())
	})

	t.Run("SeekToLast on empty returns false", func(t *testing.T) {
		it := e.NewIterator(nil)
		defer func() { _ = it.Close() }()
		assert.False(t, it.SeekToLast())
		assert.False(t, it.Valid())
	})
}

func TestIterator_SeekToLast(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "b", "c"} {
		require.NoError(t, e.Set([]byte(k), []byte(k+"val")))
	}
	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()

	assert.True(t, it.SeekToLast())
	assert.True(t, it.Valid())
	assert.Equal(t, []byte("c"), it.Key())
	assert.Equal(t, []byte("cval"), it.Value())
}

func TestIterator_Seek(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "c", "e"} {
		require.NoError(t, e.Set([]byte(k), []byte(k+"val")))
	}
	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()

	t.Run("Seek to existing key", func(t *testing.T) {
		assert.True(t, it.Seek([]byte("c")))
		assert.True(t, it.Valid())
		assert.Equal(t, []byte("c"), it.Key())
	})

	t.Run("Seek to gap positions at next key", func(t *testing.T) {
		assert.True(t, it.Seek([]byte("b")))
		assert.True(t, it.Valid())
		assert.Equal(t, []byte("c"), it.Key())
	})

	t.Run("Seek past last returns false", func(t *testing.T) {
		assert.False(t, it.Seek([]byte("z")))
		assert.False(t, it.Valid())
	})
}

func TestIterator_Prev(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "b", "c"} {
		require.NoError(t, e.Set([]byte(k), []byte(k)))
	}

	t.Run("Prev before initialization returns false", func(t *testing.T) {
		it := e.NewIterator(nil)
		defer func() { _ = it.Close() }()
		assert.False(t, it.Prev())
	})

	t.Run("Prev moves backward through entries", func(t *testing.T) {
		it := e.NewIterator(nil)
		defer func() { _ = it.Close() }()
		require.True(t, it.SeekToLast())
		assert.Equal(t, []byte("c"), it.Key())

		assert.True(t, it.Prev())
		assert.True(t, it.Valid())
		assert.Equal(t, []byte("b"), it.Key())
	})

	t.Run("Prev past first returns false", func(t *testing.T) {
		it := e.NewIterator(nil)
		defer func() { _ = it.Close() }()
		require.True(t, it.SeekToFirst())
		assert.False(t, it.Prev())
		assert.False(t, it.Valid())
	})
}

func TestIterator_ValidValueKey_WhenInvalidReturnNil(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()

	require.True(t, it.SeekToFirst())
	assert.True(t, it.Valid())
	assert.Equal(t, []byte("k"), it.Key())
	assert.Equal(t, []byte("v"), it.Value())

	// Advance past end to invalidate.
	for it.Next() {
	}
	assert.False(t, it.Valid())
	assert.Nil(t, it.Key())
	assert.Nil(t, it.Value())
}

func TestIterator_NextUninitialized(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()

	// Next before any positioning returns false.
	assert.False(t, it.Next())
}

func TestIterator_OnlyKeys(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "b"} {
		require.NoError(t, e.Set([]byte(k), []byte(k+"val")))
	}
	it := e.NewIterator(&storage.IteratorOptions{OnlyKeys: true})
	defer func() { _ = it.Close() }()

	var keys []string
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		keys = append(keys, string(it.Key()))
	}
	require.NoError(t, it.Error())
	assert.Equal(t, []string{"a", "b"}, keys)
}

func TestIterator_LoadEntriesError(t *testing.T) {
	// Closing the engine after setting a key causes loadEntries to fail when
	// it calls e.Get for the entry (the underlying store is not closed, but the
	// engine's closed guard fires). newIterator stores the error on the iterator.
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	require.NoError(t, e.Close())

	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()
	assert.False(t, it.SeekToFirst())
	assert.Error(t, it.Error())
}

// --- Snapshot: NewIterator, closed-snapshot errors ---

func TestSnapshot_NewIterator(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "b", "c", "d"} {
		require.NoError(t, e.Set([]byte(k), []byte(k)))
	}
	snap, err := e.Snapshot()
	require.NoError(t, err)
	defer func() { _ = snap.Close() }()

	t.Run("nil opts returns all entries in order", func(t *testing.T) {
		it := snap.NewIterator(nil)
		defer func() { _ = it.Close() }()
		var got []string
		for ok := it.SeekToFirst(); ok; ok = it.Next() {
			got = append(got, string(it.Key()))
		}
		assert.Equal(t, []string{"a", "b", "c", "d"}, got)
	})

	t.Run("bounds filter entries", func(t *testing.T) {
		it := snap.NewIterator(&storage.IteratorOptions{
			LowerBound: []byte("b"),
			UpperBound: []byte("d"),
		})
		defer func() { _ = it.Close() }()
		var got []string
		for ok := it.SeekToFirst(); ok; ok = it.Next() {
			got = append(got, string(it.Key()))
		}
		assert.Equal(t, []string{"b", "c"}, got)
	})

	t.Run("Reverse iterates descending", func(t *testing.T) {
		it := snap.NewIterator(&storage.IteratorOptions{Reverse: true})
		defer func() { _ = it.Close() }()
		var got []string
		for ok := it.SeekToFirst(); ok; ok = it.Next() {
			got = append(got, string(it.Key()))
		}
		assert.Equal(t, []string{"d", "c", "b", "a"}, got)
	})
}

func TestSnapshot_NewIteratorClosed(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))

	snap, err := e.Snapshot()
	require.NoError(t, err)
	require.NoError(t, snap.Close())

	// NewIterator on a closed snapshot returns an empty iterator.
	it := snap.NewIterator(nil)
	defer func() { _ = it.Close() }()
	assert.False(t, it.SeekToFirst())
}

func TestSnapshot_GetErrors(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))

	snap, err := e.Snapshot()
	require.NoError(t, err)

	t.Run("empty key returns SnapshotError", func(t *testing.T) {
		_, err := snap.Get(nil)
		var snapErr *storage.SnapshotError
		assert.ErrorAs(t, err, &snapErr)
	})

	t.Run("missing key returns nil nil", func(t *testing.T) {
		v, err := snap.Get([]byte("nope"))
		require.NoError(t, err)
		assert.Nil(t, v)
	})

	require.NoError(t, snap.Close())

	t.Run("Get on closed snapshot returns SnapshotError", func(t *testing.T) {
		_, err := snap.Get([]byte("k"))
		var snapErr *storage.SnapshotError
		assert.ErrorAs(t, err, &snapErr)
	})
}

func TestSnapshot_LoadEntriesError(t *testing.T) {
	// Call newSnapshot directly on a closed engine that has a key. loadEntries
	// will fail when calling e.Get on the entry after successfully listing it.
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v")))
	require.NoError(t, e.Close())

	_, err := newSnapshot(e)
	require.Error(t, err)
}
