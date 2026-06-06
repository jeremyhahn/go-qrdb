//go:build codec_json && raft

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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// IsCompound
// ---------------------------------------------------------------------------

func TestIndexConfig_IsCompound_SingleField(t *testing.T) {
	cfg := IndexConfig{Name: "test.Field1", FieldNames: []string{"Field1"}}
	assert.False(t, cfg.IsCompound())
}

func TestIndexConfig_IsCompound_MultiField(t *testing.T) {
	cfg := IndexConfig{Name: "test.F1_F2", FieldNames: []string{"Field1", "Field2"}}
	assert.True(t, cfg.IsCompound())
}

func TestIndexConfig_IsCompound_EmptyFields(t *testing.T) {
	cfg := IndexConfig{Name: "test.empty"}
	assert.False(t, cfg.IsCompound())
}

// ---------------------------------------------------------------------------
// Close and Config
// ---------------------------------------------------------------------------

func TestCompoundIndex_Close(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.messages.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})
	err := idx.Close()
	assert.NoError(t, err)
}

func TestCompoundIndex_Config(t *testing.T) {
	cfg := IndexConfig{
		Name:       "test.messages.ConvID_TS",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	}
	idx := newTestCompoundIndex(t, cfg)
	got := idx.Config()
	assert.Equal(t, cfg.Name, got.Name)
	assert.Equal(t, cfg.FieldNames, got.FieldNames)
}

// ---------------------------------------------------------------------------
// ScanCompound
// ---------------------------------------------------------------------------

func TestCompoundIndex_ScanCompound_Empty(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.msg.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	result, err := idx.ScanCompound([][]byte{[]byte("conv-1")}, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestCompoundIndex_ScanCompound_WithData(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.scanmsg.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	// Put some entries.
	require.NoError(t, idx.PutCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")}))
	require.NoError(t, idx.PutCompound([]byte("messages/2"), [][]byte{[]byte("conv-1"), []byte("2000")}))
	require.NoError(t, idx.PutCompound([]byte("messages/3"), [][]byte{[]byte("conv-2"), []byte("1500")}))

	// Scan by prefix conv-1.
	result, err := idx.ScanCompound([][]byte{[]byte("conv-1")}, nil, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, result)

	// Collect all entity keys.
	var keys []string
	for _, entityKeys := range result {
		for _, k := range entityKeys {
			keys = append(keys, string(k))
		}
	}
	assert.Contains(t, keys, "messages/1")
	assert.Contains(t, keys, "messages/2")
}

func TestCompoundIndex_ScanCompound_WithRangeBounds(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.rangescan.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	require.NoError(t, idx.PutCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")}))
	require.NoError(t, idx.PutCompound([]byte("messages/2"), [][]byte{[]byte("conv-1"), []byte("2000")}))
	require.NoError(t, idx.PutCompound([]byte("messages/3"), [][]byte{[]byte("conv-1"), []byte("3000")}))

	// Scan conv-1 with range [1000, 2500).
	result, err := idx.ScanCompound(
		[][]byte{[]byte("conv-1")},
		[]byte("1000"),
		[]byte("2500"),
	)
	require.NoError(t, err)
	// Should include 1000 and 2000 but not 3000.
	var keys []string
	for _, entityKeys := range result {
		for _, k := range entityKeys {
			keys = append(keys, string(k))
		}
	}
	assert.Contains(t, keys, "messages/1")
	assert.Contains(t, keys, "messages/2")
}

// ---------------------------------------------------------------------------
// DeleteCompoundBatch
// ---------------------------------------------------------------------------

func TestCompoundIndex_DeleteCompoundBatch(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.delbatch.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	require.NoError(t, idx.PutCompound([]byte("messages/10"), [][]byte{[]byte("conv-A"), []byte("100")}))

	// Confirm it's there.
	keys, err := idx.GetCompound([][]byte{[]byte("conv-A"), []byte("100")})
	require.NoError(t, err)
	require.NotEmpty(t, keys)

	// Delete via batch.
	batch := idx.storage.Batch()
	err = idx.DeleteCompoundBatch(batch, []byte("messages/10"), [][]byte{[]byte("conv-A"), []byte("100")})
	require.NoError(t, err)
	require.NoError(t, batch.Commit())
	require.NoError(t, batch.Close())

	// Should be gone now.
	keys, err = idx.GetCompound([][]byte{[]byte("conv-A"), []byte("100")})
	require.NoError(t, err)
	assert.Empty(t, keys)
}

// ---------------------------------------------------------------------------
// PutCompoundWithContext
// ---------------------------------------------------------------------------

func TestCompoundIndex_PutCompoundWithContext_Success(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.putctx.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	ctx := context.Background()
	err := idx.PutCompoundWithContext(ctx, []byte("messages/20"), [][]byte{[]byte("conv-B"), []byte("200")})
	require.NoError(t, err)

	keys, err := idx.GetCompound([][]byte{[]byte("conv-B"), []byte("200")})
	require.NoError(t, err)
	assert.NotEmpty(t, keys)
}

func TestCompoundIndex_PutCompoundWithContext_CanceledContext(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.putctxcancel.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	err := idx.PutCompoundWithContext(ctx, []byte("messages/99"), [][]byte{[]byte("conv-X"), []byte("999")})
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// GetCompoundWithContext
// ---------------------------------------------------------------------------

func TestCompoundIndex_GetCompoundWithContext_Success(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.getctx.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	require.NoError(t, idx.PutCompound([]byte("messages/30"), [][]byte{[]byte("conv-C"), []byte("300")}))

	ctx := context.Background()
	keys, err := idx.GetCompoundWithContext(ctx, [][]byte{[]byte("conv-C"), []byte("300")})
	require.NoError(t, err)
	assert.NotEmpty(t, keys)
	assert.Equal(t, "messages/30", string(keys[0]))
}

func TestCompoundIndex_GetCompoundWithContext_CanceledContext(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.getctxcancel.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := idx.GetCompoundWithContext(ctx, [][]byte{[]byte("conv-Z"), []byte("999")})
	require.Error(t, err)
}
