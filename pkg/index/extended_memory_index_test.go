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

package index

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewExtendedMemoryIndex_NilConfig(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(nil)
	assert.Error(t, err)
	assert.Nil(t, idx)
	assert.Contains(t, err.Error(), "config is required")
}

func TestExtendedMemoryIndex_BasicOperations(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(&IndexConfig{Name: "field", Type: IndexTypeNormal})
	require.NoError(t, err)

	// Put
	require.NoError(t, idx.Put([]byte("entity-1"), []byte("a")))
	require.NoError(t, idx.Put([]byte("entity-2"), []byte("a")))
	require.NoError(t, idx.Put([]byte("entity-3"), []byte("b")))

	// Get
	keys, err := idx.Get([]byte("a"))
	require.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.True(t, containsKey(keys, []byte("entity-1")))
	assert.True(t, containsKey(keys, []byte("entity-2")))

	// Scan
	scan, err := idx.Scan([]byte("a"), []byte("z"))
	require.NoError(t, err)
	assert.Contains(t, scan, "a")

	// GetAll
	all, err := idx.GetAll()
	require.NoError(t, err)
	assert.Len(t, all, 3)

	// Delete
	require.NoError(t, idx.Delete([]byte("entity-2"), []byte("a")))
	keys, _ = idx.Get([]byte("a"))
	assert.Len(t, keys, 1)
	assert.True(t, containsKey(keys, []byte("entity-1")))

	// Clear
	require.NoError(t, idx.Clear())
	keys, _ = idx.Get([]byte("a"))
	assert.Empty(t, keys)

	// Close
	require.NoError(t, idx.Close())
}

func TestExtendedMemoryIndex_PutWithContext(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	ctx := context.Background()
	err = idx.PutWithContext(ctx, []byte("entity-1"), []byte("value1"))
	assert.NoError(t, err)

	keys, err := idx.Get([]byte("value1"))
	assert.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.True(t, bytes.Equal(keys[0], []byte("entity-1")))
}

func TestExtendedMemoryIndex_PutWithContext_Canceled(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = idx.PutWithContext(ctx, []byte("entity-1"), []byte("value1"))
	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)
}

func TestExtendedMemoryIndex_GetWithContext(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	err = idx.Put([]byte("entity-1"), []byte("value1"))
	require.NoError(t, err)

	ctx := context.Background()
	keys, err := idx.GetWithContext(ctx, []byte("value1"))
	assert.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.True(t, bytes.Equal(keys[0], []byte("entity-1")))
}

func TestExtendedMemoryIndex_GetWithContext_Canceled(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	keys, err := idx.GetWithContext(ctx, []byte("value1"))
	assert.Error(t, err)
	assert.Nil(t, keys)
	assert.Equal(t, context.Canceled, err)
}

func TestExtendedMemoryIndex_DeleteWithContext(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	err = idx.Put([]byte("entity-1"), []byte("value1"))
	require.NoError(t, err)

	ctx := context.Background()
	err = idx.DeleteWithContext(ctx, []byte("entity-1"), []byte("value1"))
	assert.NoError(t, err)

	keys, err := idx.Get([]byte("value1"))
	assert.NoError(t, err)
	assert.Empty(t, keys)
}

func TestExtendedMemoryIndex_DeleteWithContext_Canceled(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = idx.DeleteWithContext(ctx, []byte("entity-1"), []byte("value1"))
	assert.Error(t, err)
	assert.Equal(t, context.Canceled, err)
}

func TestExtendedMemoryIndex_ScanWithContext(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	err = idx.Put([]byte("entity-1"), []byte("value1"))
	require.NoError(t, err)
	err = idx.Put([]byte("entity-2"), []byte("value2"))
	require.NoError(t, err)

	ctx := context.Background()
	results, err := idx.ScanWithContext(ctx, []byte("value1"), []byte("value3"))
	assert.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestExtendedMemoryIndex_ScanWithContext_Canceled(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results, err := idx.ScanWithContext(ctx, []byte("value1"), []byte("value3"))
	assert.Error(t, err)
	assert.Nil(t, results)
	assert.Equal(t, context.Canceled, err)
}

func TestExtendedMemoryIndex_WithContextTimeout(t *testing.T) {
	config := &IndexConfig{
		Name: "test_index",
		Type: IndexTypeNormal,
	}
	idx, err := NewExtendedMemoryIndex(config)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = idx.PutWithContext(ctx, []byte("entity-1"), []byte("value1"))
	assert.NoError(t, err)

	keys, err := idx.GetWithContext(ctx, []byte("value1"))
	assert.NoError(t, err)
	assert.Len(t, keys, 1)
	assert.True(t, bytes.Equal(keys[0], []byte("entity-1")))
}

func TestExtendedMemoryIndex_ContextCancellation(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(&IndexConfig{Name: "field", Type: IndexTypeNormal})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.Error(t, idx.PutWithContext(ctx, []byte("entity-1"), []byte("a")))
	_, err = idx.GetWithContext(ctx, []byte("a"))
	assert.Error(t, err)
	assert.Error(t, idx.DeleteWithContext(ctx, []byte("entity-1"), []byte("a")))

	// ScanWithContext timeout
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel2()
	time.Sleep(time.Millisecond)
	_, err = idx.ScanWithContext(ctx2, nil, nil)
	assert.Error(t, err)
}

func TestExtendedMemoryIndex_PutNilFieldValue(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(&IndexConfig{Name: "test", Type: IndexTypeNormal})
	require.NoError(t, err)

	err = idx.Put([]byte("entity-1"), nil)
	assert.Error(t, err)
}

func TestExtendedMemoryIndex_GetNilFieldValue(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(&IndexConfig{Name: "test", Type: IndexTypeNormal})
	require.NoError(t, err)

	_, err = idx.Get(nil)
	assert.Error(t, err)
}

func TestExtendedMemoryIndex_DeleteNilFieldValue(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(&IndexConfig{Name: "test", Type: IndexTypeNormal})
	require.NoError(t, err)

	err = idx.Delete([]byte("entity-1"), nil)
	assert.Error(t, err)
}

func TestExtendedMemoryIndex_DuplicatePut(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(&IndexConfig{Name: "test", Type: IndexTypeNormal})
	require.NoError(t, err)

	err = idx.Put([]byte("entity-1"), []byte("value"))
	require.NoError(t, err)

	// Put same entity key again should be no-op
	err = idx.Put([]byte("entity-1"), []byte("value"))
	require.NoError(t, err)

	keys, err := idx.Get([]byte("value"))
	require.NoError(t, err)
	assert.Len(t, keys, 1)
}

func TestExtendedMemoryIndex_DeleteNonExistent(t *testing.T) {
	idx, err := NewExtendedMemoryIndex(&IndexConfig{Name: "test", Type: IndexTypeNormal})
	require.NoError(t, err)

	// Deleting non-existent entry should not error
	err = idx.Delete([]byte("entity-1"), []byte("nonexistent"))
	assert.NoError(t, err)
}
