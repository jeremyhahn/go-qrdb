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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMemEngine returns an objstore Engine backed by the in-memory provider.
func newMemEngine(t *testing.T) *Engine {
	t.Helper()
	e, err := New("memory", map[string]string{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func TestNew_EmptyProvider(t *testing.T) {
	_, err := New("", nil)
	require.Error(t, err)
}

func TestNew_UnsupportedProvider(t *testing.T) {
	// Use a provider name that is never registered under any build tag, so the
	// test is valid regardless of which provider tags (e.g. awss3) are compiled
	// in. A real provider like "s3" would be supported under the awss3 tag and
	// fail config validation with a different error instead.
	const unknown = "no-such-provider"
	_, err := New(unknown, nil)
	require.Error(t, err)
	var upErr *UnsupportedProviderError
	require.ErrorAs(t, err, &upErr)
	assert.Equal(t, unknown, upErr.Provider)
}

func TestSetGetDelete(t *testing.T) {
	e := newMemEngine(t)

	require.NoError(t, e.Set([]byte("k1"), []byte("v1")))
	got, err := e.Get([]byte("k1"))
	require.NoError(t, err)
	assert.Equal(t, []byte("v1"), got)

	ok, err := e.Exists([]byte("k1"))
	require.NoError(t, err)
	assert.True(t, ok)

	require.NoError(t, e.Delete([]byte("k1")))
	got, err = e.Get([]byte("k1"))
	require.NoError(t, err)
	assert.Nil(t, got, "missing key must return nil, nil")

	ok, err = e.Exists([]byte("k1"))
	require.NoError(t, err)
	assert.False(t, ok)

	// Deleting a missing key is idempotent.
	require.NoError(t, e.Delete([]byte("k1")))
}

func TestEmptyKey(t *testing.T) {
	e := newMemEngine(t)
	_, err := e.Get(nil)
	assert.ErrorIs(t, err, storage.ErrEmptyKey)
	assert.ErrorIs(t, e.Set(nil, []byte("x")), storage.ErrEmptyKey)
}

func TestList_Prefix(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("a/1"), []byte("1")))
	require.NoError(t, e.Set([]byte("a/2"), []byte("2")))
	require.NoError(t, e.Set([]byte("b/1"), []byte("3")))

	all, err := e.List(nil)
	require.NoError(t, err)
	assert.Len(t, all, 3)

	aKeys, err := e.List([]byte("a/"))
	require.NoError(t, err)
	assert.Len(t, aKeys, 2)
	for _, k := range aKeys {
		assert.True(t, len(k) >= 2 && string(k[:2]) == "a/")
	}
}

func TestBatch_Commit(t *testing.T) {
	e := newMemEngine(t)
	b := e.Batch()
	require.NoError(t, b.Set([]byte("x"), []byte("1")))
	require.NoError(t, b.Set([]byte("y"), []byte("2")))
	require.NoError(t, b.Delete([]byte("x")))
	require.NoError(t, b.Commit())

	x, err := e.Get([]byte("x"))
	require.NoError(t, err)
	assert.Nil(t, x)
	y, err := e.Get([]byte("y"))
	require.NoError(t, err)
	assert.Equal(t, []byte("2"), y)
}

func TestBatch_Rollback(t *testing.T) {
	e := newMemEngine(t)
	b := e.Batch()
	require.NoError(t, b.Set([]byte("z"), []byte("9")))
	require.NoError(t, b.Rollback())

	z, err := e.Get([]byte("z"))
	require.NoError(t, err)
	assert.Nil(t, z, "rolled-back write must not be visible")
}

func TestIterator_Forward(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"c", "a", "b"} {
		require.NoError(t, e.Set([]byte(k), []byte(k)))
	}
	it := e.NewIterator(nil)
	defer func() { _ = it.Close() }()

	var got []string
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		got = append(got, string(it.Key()))
	}
	require.NoError(t, it.Error())
	assert.Equal(t, []string{"a", "b", "c"}, got, "iteration must be sorted ascending")
}

func TestIterator_Bounds(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "b", "c", "d"} {
		require.NoError(t, e.Set([]byte(k), []byte(k)))
	}
	it := e.NewIterator(&storage.IteratorOptions{LowerBound: []byte("b"), UpperBound: []byte("d")})
	defer func() { _ = it.Close() }()

	var got []string
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		got = append(got, string(it.Key()))
	}
	assert.Equal(t, []string{"b", "c"}, got, "upper bound is exclusive")
}

func TestIterator_Reverse(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "b", "c"} {
		require.NoError(t, e.Set([]byte(k), []byte(k)))
	}
	it := e.NewIterator(&storage.IteratorOptions{Reverse: true})
	defer func() { _ = it.Close() }()

	var got []string
	for ok := it.SeekToFirst(); ok; ok = it.Next() {
		got = append(got, string(it.Key()))
	}
	assert.Equal(t, []string{"c", "b", "a"}, got)
}

func TestSnapshot_Isolation(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("k"), []byte("v1")))

	snap, err := e.Snapshot()
	require.NoError(t, err)
	defer func() { _ = snap.Close() }()

	// Mutate after the snapshot.
	require.NoError(t, e.Set([]byte("k"), []byte("v2")))
	require.NoError(t, e.Set([]byte("new"), []byte("x")))

	v, err := snap.Get([]byte("k"))
	require.NoError(t, err)
	assert.Equal(t, []byte("v1"), v, "snapshot must see the value at capture time")

	n, err := snap.Get([]byte("new"))
	require.NoError(t, err)
	assert.Nil(t, n, "snapshot must not see post-capture writes")
}

func TestClosed_Errors(t *testing.T) {
	e, err := New("memory", map[string]string{})
	require.NoError(t, err)
	require.NoError(t, e.Close())

	_, err = e.Get([]byte("k"))
	var closedErr *storage.ClosedError
	assert.True(t, errors.As(err, &closedErr))
	assert.ErrorAs(t, e.Set([]byte("k"), []byte("v")), &closedErr)
}
