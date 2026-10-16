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
	"bytes"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// sortedKeys returns every stored key in byte order; List promises none.
func sortedKeys(t *testing.T, e *Engine) [][]byte {
	t.Helper()
	keys, err := e.List(nil)
	require.NoError(t, err)
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i], keys[j]) < 0 })
	return keys
}

// TestDeleteRange covers [start, end) over direct and hashed names alike: the
// long key starting with "n" is stored under a hashed name, which says nothing
// about where its key sorts.
func TestDeleteRange(t *testing.T) {
	long := patterned(directThreshold()+1, 'n')
	a, m, ma, z := []byte("a"), []byte("m"), []byte("ma"), []byte("z")
	cases := map[string]struct {
		start, end []byte
		want       [][]byte
	}{
		"bounded keeps its exclusive end":  {m, z, [][]byte{a, z}},
		"bounded around the hashed key":    {[]byte("n"), []byte("o"), [][]byte{a, m, ma, z}},
		"nil end reaches the keyspace end": {ma, nil, [][]byte{a, m}},
		"empty range is a no-op":           {[]byte("b"), []byte("c"), [][]byte{a, m, ma, long, z}},
	}
	for provider := range engines(t) {
		for name, tc := range cases {
			t.Run(provider+"/"+name, func(t *testing.T) {
				e := engines(t)[provider]
				for _, k := range [][]byte{a, m, ma, long, z} {
					require.NoError(t, e.Set(k, k))
				}
				require.NoError(t, e.DeleteRange(tc.start, tc.end))
				assert.Equal(t, tc.want, sortedKeys(t, e))
			})
		}
	}
}

func TestDeleteRange_Refusals(t *testing.T) {
	e := newMemEngine(t)
	require.NoError(t, e.Set([]byte("a"), []byte("1")))

	assert.ErrorIs(t, e.DeleteRange([]byte("b"), []byte("a")), storage.ErrInvalidRange)
	assert.ErrorIs(t, e.DeleteRange(nil, []byte("b")), storage.ErrEmptyKey)
	assert.Equal(t, [][]byte{[]byte("a")}, sortedKeys(t, e))

	require.NoError(t, e.Close())
	assert.ErrorIs(t, e.DeleteRange([]byte("a"), nil), storage.ErrStorageClosed)
}

func TestDeleteRange_ProviderFaults(t *testing.T) {
	t.Run("listing fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, e.Set([]byte("a"), []byte("1")))
		fs.listFails[hashedPrefix] = true
		assert.ErrorIs(t, e.DeleteRange([]byte("a"), nil), errProvider)
	})
	t.Run("delete fails", func(t *testing.T) {
		e, fs := newFaultEngine(t)
		require.NoError(t, e.Set([]byte("a"), []byte("1")))
		fs.deleteFails = true
		assert.ErrorIs(t, e.DeleteRange([]byte("a"), nil), errProvider)
	})
}

// TestBatch_DeleteRangeAppliesInOrder checks a committed batch applies its
// range delete between its neighbours: it removes a key the batch wrote
// earlier and leaves one the batch wrote later.
func TestBatch_DeleteRangeAppliesInOrder(t *testing.T) {
	e := newMemEngine(t)
	for _, k := range []string{"a", "b", "c", "d"} {
		require.NoError(t, e.Set([]byte(k), []byte(k)))
	}

	b := e.Batch()
	defer func() { _ = b.Close() }()
	require.NoError(t, b.Set([]byte("bb"), []byte("earlier")))
	require.NoError(t, b.DeleteRange([]byte("b"), []byte("d")))
	require.NoError(t, b.Set([]byte("c"), []byte("later")))
	require.NoError(t, b.Commit())

	assert.Equal(t, [][]byte{[]byte("a"), []byte("c"), []byte("d")}, sortedKeys(t, e))
	v, err := e.Get([]byte("c"))
	require.NoError(t, err)
	assert.Equal(t, []byte("later"), v)
}

func TestBatch_DeleteRangeFaultFailsCommit(t *testing.T) {
	e, fs := newFaultEngine(t)
	require.NoError(t, e.Set([]byte("a"), []byte("1")))

	b := e.Batch()
	defer func() { _ = b.Close() }()
	require.NoError(t, b.DeleteRange([]byte("a"), nil))
	fs.deleteFails = true
	assert.ErrorIs(t, b.Commit(), errProvider)
}
