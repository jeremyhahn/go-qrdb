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
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/logger"
	pebbleStorage "github.com/jeremyhahn/go-qrdb/pkg/storage/pebble"
)

// newTestCompoundIndex creates a CompoundIndex backed by temporary PebbleDB.
func newTestCompoundIndex(t *testing.T, config IndexConfig) *CompoundIndex {
	t.Helper()
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	log := testCompoundLogger(t)
	idx, err := NewCompoundIndex(s, config, log)
	require.NoError(t, err)
	return idx
}

func testCompoundLogger(t *testing.T) *logger.Logger {
	t.Helper()
	log, err := logger.New(logger.LogConfig{
		Level:  "error",
		Format: "json",
		Output: "stdout",
	})
	require.NoError(t, err)
	return log
}

func TestCompoundIndex_PutAndGetCompound(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.messages.ConversationID_Timestamp",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	err := idx.PutCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")})
	require.NoError(t, err)

	err = idx.PutCompound([]byte("messages/2"), [][]byte{[]byte("conv-1"), []byte("2000")})
	require.NoError(t, err)

	err = idx.PutCompound([]byte("messages/3"), [][]byte{[]byte("conv-2"), []byte("1500")})
	require.NoError(t, err)

	// Exact match on both fields.
	keys, err := idx.GetCompound([][]byte{[]byte("conv-1"), []byte("1000")})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "messages/1", string(keys[0]))

	// Exact match returns empty for non-existent compound value.
	keys, err = idx.GetCompound([][]byte{[]byte("conv-1"), []byte("9999")})
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestCompoundIndex_GetPrefix(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.messages.ConversationID_Timestamp",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	_ = idx.PutCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")})
	_ = idx.PutCompound([]byte("messages/2"), [][]byte{[]byte("conv-1"), []byte("2000")})
	_ = idx.PutCompound([]byte("messages/3"), [][]byte{[]byte("conv-2"), []byte("1500")})

	// Prefix on first field: get all messages in conv-1.
	keys, err := idx.GetPrefix([][]byte{[]byte("conv-1")})
	require.NoError(t, err)
	require.Len(t, keys, 2)

	keyStrs := []string{string(keys[0]), string(keys[1])}
	sort.Strings(keyStrs)
	assert.Equal(t, []string{"messages/1", "messages/2"}, keyStrs)

	// Prefix on conv-2: single result.
	keys, err = idx.GetPrefix([][]byte{[]byte("conv-2")})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "messages/3", string(keys[0]))

	// Non-existent prefix.
	keys, err = idx.GetPrefix([][]byte{[]byte("conv-99")})
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestCompoundIndex_DeleteCompound(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.messages.ConversationID_Timestamp",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	_ = idx.PutCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")})
	_ = idx.PutCompound([]byte("messages/2"), [][]byte{[]byte("conv-1"), []byte("2000")})

	// Delete one entry.
	err := idx.DeleteCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")})
	require.NoError(t, err)

	// Only messages/2 should remain.
	keys, err := idx.GetPrefix([][]byte{[]byte("conv-1")})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "messages/2", string(keys[0]))
}

func TestCompoundIndex_UniqueConstraint(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.reactions.MessageID_AuthorUUID",
		FieldNames: []string{"MessageID", "AuthorUUID"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
		Unique:     true,
		Type:       IndexTypeUnique,
	})

	err := idx.PutCompound([]byte("reactions/1"), [][]byte{[]byte("msg-1"), []byte("user-a")})
	require.NoError(t, err)

	// Same entity key can update.
	err = idx.PutCompound([]byte("reactions/1"), [][]byte{[]byte("msg-1"), []byte("user-a")})
	require.NoError(t, err)

	// Different entity key with same compound value should fail.
	err = idx.PutCompound([]byte("reactions/2"), [][]byte{[]byte("msg-1"), []byte("user-a")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unique constraint violation")
}

func TestCompoundIndex_BatchOperations(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	log := testCompoundLogger(t)
	idx, err := NewCompoundIndex(s, IndexConfig{
		Name:       "test.messages.ConversationID_Timestamp",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	}, log)
	require.NoError(t, err)

	batch := s.Batch()
	defer func() { _ = batch.Close() }()

	err = idx.PutCompoundBatch(batch, []byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")})
	require.NoError(t, err)

	err = idx.PutCompoundBatch(batch, []byte("messages/2"), [][]byte{[]byte("conv-1"), []byte("2000")})
	require.NoError(t, err)

	err = batch.Commit()
	require.NoError(t, err)

	// Verify entries persisted.
	keys, err := idx.GetPrefix([][]byte{[]byte("conv-1")})
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

func TestCompoundIndex_Clear(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.messages.ConversationID_Timestamp",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	_ = idx.PutCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")})
	_ = idx.PutCompound([]byte("messages/2"), [][]byte{[]byte("conv-2"), []byte("2000")})

	err := idx.Clear()
	require.NoError(t, err)

	// All entries should be gone.
	keys, err := idx.GetPrefix([][]byte{[]byte("conv-1")})
	require.NoError(t, err)
	assert.Empty(t, keys)

	keys, err = idx.GetPrefix([][]byte{[]byte("conv-2")})
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestCompoundIndex_Close(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.messages.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})
	err := idx.Close()
	assert.NoError(t, err)
}

func TestCompoundIndex_ThreeFields(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.jobs.QueueType_Status_NextRunAt",
		FieldNames: []string{"QueueType", "Status", "NextRunAt"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString, IndexValueTypeString},
	})

	_ = idx.PutCompound([]byte("jobs/1"), [][]byte{[]byte("send"), []byte("pending"), []byte("1000")})
	_ = idx.PutCompound([]byte("jobs/2"), [][]byte{[]byte("send"), []byte("pending"), []byte("2000")})
	_ = idx.PutCompound([]byte("jobs/3"), [][]byte{[]byte("send"), []byte("running"), []byte("500")})
	_ = idx.PutCompound([]byte("jobs/4"), [][]byte{[]byte("download"), []byte("pending"), []byte("1500")})

	// Prefix on first field only.
	keys, err := idx.GetPrefix([][]byte{[]byte("send")})
	require.NoError(t, err)
	assert.Len(t, keys, 3)

	// Prefix on first two fields.
	keys, err = idx.GetPrefix([][]byte{[]byte("send"), []byte("pending")})
	require.NoError(t, err)
	assert.Len(t, keys, 2)

	// Exact match all three.
	keys, err = idx.GetCompound([][]byte{[]byte("send"), []byte("pending"), []byte("1000")})
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, "jobs/1", string(keys[0]))
}

func TestNewCompoundIndex_Validation(t *testing.T) {
	s, err := pebbleStorage.New(pebbleStorage.TestConfig(t.TempDir()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	log := testCompoundLogger(t)

	// Missing storage.
	_, err = NewCompoundIndex(nil, IndexConfig{
		Name:       "test",
		FieldNames: []string{"A", "B"},
	}, log)
	require.Error(t, err)

	// Missing logger.
	_, err = NewCompoundIndex(s, IndexConfig{
		Name:       "test",
		FieldNames: []string{"A", "B"},
	}, nil)
	require.Error(t, err)

	// Missing name.
	_, err = NewCompoundIndex(s, IndexConfig{
		FieldNames: []string{"A", "B"},
	}, log)
	require.Error(t, err)

	// Less than 2 fields.
	_, err = NewCompoundIndex(s, IndexConfig{
		Name:       "test",
		FieldNames: []string{"A"},
	}, log)
	require.Error(t, err)
}

func TestIndexConfig_IsCompound(t *testing.T) {
	tests := []struct {
		name   string
		fields []string
		want   bool
	}{
		{"single field", []string{"Field1"}, false},
		{"multi field", []string{"Field1", "Field2"}, true},
		{"no fields", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := IndexConfig{Name: "test.compound", FieldNames: tt.fields}
			assert.Equal(t, tt.want, cfg.IsCompound())
		})
	}
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

// scanEntityKeys flattens a ScanCompound result into sorted entity keys.
func scanEntityKeys(result map[string][][]byte) []string {
	var keys []string
	for _, entityKeys := range result {
		for _, k := range entityKeys {
			keys = append(keys, string(k))
		}
	}
	sort.Strings(keys)
	return keys
}

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

func TestCompoundIndex_ScanCompound_PrefixOnly(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.scanmsg.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	require.NoError(t, idx.PutCompound([]byte("messages/1"), [][]byte{[]byte("conv-1"), []byte("1000")}))
	require.NoError(t, idx.PutCompound([]byte("messages/2"), [][]byte{[]byte("conv-1"), []byte("2000")}))
	require.NoError(t, idx.PutCompound([]byte("messages/3"), [][]byte{[]byte("conv-2"), []byte("1500")}))

	result, err := idx.ScanCompound([][]byte{[]byte("conv-1")}, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"messages/1", "messages/2"}, scanEntityKeys(result))
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

	// [1000, 2500) holds 1000 and 2000 but not 3000.
	result, err := idx.ScanCompound([][]byte{[]byte("conv-1")}, []byte("1000"), []byte("2500"))
	require.NoError(t, err)
	assert.Equal(t, []string{"messages/1", "messages/2"}, scanEntityKeys(result))
}

func TestCompoundIndex_DeleteCompoundBatch(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.delbatch.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	values := [][]byte{[]byte("conv-A"), []byte("100")}
	require.NoError(t, idx.PutCompound([]byte("messages/10"), values))
	keys, err := idx.GetCompound(values)
	require.NoError(t, err)
	require.NotEmpty(t, keys)

	batch := idx.storage.Batch()
	require.NoError(t, idx.DeleteCompoundBatch(batch, []byte("messages/10"), values))
	require.NoError(t, batch.Commit())
	require.NoError(t, batch.Close())

	keys, err = idx.GetCompound(values)
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func TestCompoundIndex_ValueTypes_PadsWithDefaultValueType(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:            "test.vtypes.pad",
		FieldNames:      []string{"A", "B", "C"},
		ValueTypes:      []IndexValueType{IndexValueTypeNumeric},
		ValueType:       IndexValueTypeString,
		EncodingVersion: NumericEncodingVersion,
	})

	assert.Equal(t,
		[]IndexValueType{IndexValueTypeNumeric, IndexValueTypeString, IndexValueTypeString},
		idx.valueTypes())
}

func TestCompoundIndex_ValueTypes_NoPerFieldTypes(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.vtypes.none",
		FieldNames: []string{"A", "B"},
		ValueType:  IndexValueTypeString,
	})

	assert.Equal(t, []IndexValueType{IndexValueTypeString, IndexValueTypeString}, idx.valueTypes())
}

func TestCompoundIndex_PutCompoundWithContext_Success(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.putctx.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	values := [][]byte{[]byte("conv-B"), []byte("200")}
	require.NoError(t, idx.PutCompoundWithContext(context.Background(), []byte("messages/20"), values))

	keys, err := idx.GetCompound(values)
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("messages/20")}, keys)
}

func TestCompoundIndex_PutCompoundWithContext_CanceledContext(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.putctxcancel.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	values := [][]byte{[]byte("conv-X"), []byte("999")}
	err := idx.PutCompoundWithContext(ctx, []byte("messages/99"), values)
	require.ErrorIs(t, err, context.Canceled)

	keys, err := idx.GetCompound(values)
	require.NoError(t, err)
	assert.Empty(t, keys, "a canceled put must not write the entry")
}

func TestCompoundIndex_GetCompoundWithContext_Success(t *testing.T) {
	idx := newTestCompoundIndex(t, IndexConfig{
		Name:       "test.getctx.F1_F2",
		FieldNames: []string{"ConversationID", "Timestamp"},
		ValueTypes: []IndexValueType{IndexValueTypeString, IndexValueTypeString},
	})

	require.NoError(t, idx.PutCompound([]byte("messages/30"), [][]byte{[]byte("conv-C"), []byte("300")}))

	keys, err := idx.GetCompoundWithContext(context.Background(), [][]byte{[]byte("conv-C"), []byte("300")})
	require.NoError(t, err)
	assert.Equal(t, [][]byte{[]byte("messages/30")}, keys)
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
	require.ErrorIs(t, err, context.Canceled)
}
