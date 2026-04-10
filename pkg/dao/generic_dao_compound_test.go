//go:build codec_json

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

package dao

import (
	"context"
	encodingJSON "encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test entity with compound indexes
// ---------------------------------------------------------------------------

type compoundTestMessage struct {
	ID             uint64 `json:"id"`
	ConversationID string `json:"conversationId" index:"true"`
	Timestamp      int64  `json:"timestamp" index:"true"`
	IsRead         bool   `json:"isRead" index:"true"`
	Body           string `json:"body"`
}

func (m *compoundTestMessage) EntityID() uint64      { return m.ID }
func (m *compoundTestMessage) SetEntityID(id uint64) { m.ID = id }

func (m compoundTestMessage) CompoundIndexes() []index.CompoundIndexDef {
	return []index.CompoundIndexDef{
		{Fields: []string{"ConversationID", "Timestamp"}},
		{Fields: []string{"ConversationID", "IsRead"}},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCompoundDAO_EntityImplementsCompoundIndexRegisterer(t *testing.T) {
	msg := &compoundTestMessage{}
	registerer, ok := any(msg).(index.CompoundIndexRegisterer)
	require.True(t, ok, "compoundTestMessage should implement CompoundIndexRegisterer")

	defs := registerer.CompoundIndexes()
	require.Len(t, defs, 2)
	assert.Equal(t, []string{"ConversationID", "Timestamp"}, defs[0].Fields)
	assert.Equal(t, []string{"ConversationID", "IsRead"}, defs[1].Fields)
}

func TestCompoundDAO_BuildCatalog_IncludesCompoundIndexes(t *testing.T) {
	store := newCompoundTestStore()

	dao, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	// Access the internal genericDAO to call buildCatalog.
	gDAO := dao.(*genericDAO[*compoundTestMessage])
	catalog := gDAO.buildCatalog()

	indexes := catalog.AvailableIndexes()

	// Should have: 3 single-field (ConversationID, Timestamp, IsRead)
	// + 2 compound (ConversationID_Timestamp, ConversationID_IsRead)
	assert.GreaterOrEqual(t, len(indexes), 5, "expected at least 5 indexes in catalog")

	// Find compound indexes.
	var compoundNames []string
	for _, idx := range indexes {
		if idx.IsCompound() {
			compoundNames = append(compoundNames, idx.Name)
		}
	}
	assert.Contains(t, compoundNames, "messages.ConversationID_Timestamp")
	assert.Contains(t, compoundNames, "messages.ConversationID_IsRead")
}

func TestCompoundDAO_Registration_PassesCompoundIndexesToStore(t *testing.T) {
	store := newCompoundTestStore()

	_, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	// Verify that compound EntityIndexes were passed to RegisterEntityIndexes.
	store.mu.RLock()
	defer store.mu.RUnlock()

	regs, ok := store.registrations["messages"]
	require.True(t, ok, "messages should be registered")

	var compoundCount int
	for _, ei := range regs {
		if ei.IsCompound() {
			compoundCount++
			assert.Len(t, ei.FieldNames, 2)
			assert.Len(t, ei.JSONFieldNames, 2)
		}
	}
	assert.Equal(t, 2, compoundCount, "expected 2 compound indexes registered")
}

func TestCompoundDAO_Aggregation_CountByGroup(t *testing.T) {
	store := newCompoundTestStore()

	dao, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	// Insert test messages.
	messages := []*compoundTestMessage{
		{ConversationID: "conv-1", Timestamp: 1000, IsRead: false, Body: "hello"},
		{ConversationID: "conv-1", Timestamp: 2000, IsRead: false, Body: "world"},
		{ConversationID: "conv-1", Timestamp: 3000, IsRead: true, Body: "read msg"},
		{ConversationID: "conv-2", Timestamp: 1500, IsRead: false, Body: "other"},
	}
	for _, m := range messages {
		require.NoError(t, dao.Save(ctx, m))
	}

	// Count unread messages per conversation.
	result, err := dao.Find(ctx).
		Where("IsRead = ?", false).
		GroupBy("ConversationID").
		Aggregate(Count())
	require.NoError(t, err)

	assert.Len(t, result.Groups, 2, "expected 2 groups (conv-1 and conv-2)")

	// Build a map for assertion.
	groupMap := make(map[string]int64)
	for _, g := range result.Groups {
		groupMap[g.Key.(string)] = g.Values["count"].(int64)
	}
	assert.Equal(t, int64(2), groupMap["conv-1"])
	assert.Equal(t, int64(1), groupMap["conv-2"])
}

func TestCompoundDAO_Aggregation_TotalCount(t *testing.T) {
	store := newCompoundTestStore()

	dao, err := New[*compoundTestMessage](
		store,
		"messages",
		func() *compoundTestMessage { return &compoundTestMessage{} },
	)
	require.NoError(t, err)

	ctx := context.Background()

	_ = dao.Save(ctx, &compoundTestMessage{ConversationID: "conv-1", Timestamp: 1000, Body: "a"})
	_ = dao.Save(ctx, &compoundTestMessage{ConversationID: "conv-1", Timestamp: 2000, Body: "b"})
	_ = dao.Save(ctx, &compoundTestMessage{ConversationID: "conv-2", Timestamp: 3000, Body: "c"})

	result, err := dao.Find(ctx).Aggregate(Count())
	require.NoError(t, err)

	require.Len(t, result.Groups, 1)
	assert.Equal(t, int64(3), result.Groups[0].Values["count"].(int64))
}

// ---------------------------------------------------------------------------
// Compound-aware test store (extends memoryKVStore with stub compound methods)
// ---------------------------------------------------------------------------

type compoundTestStore struct {
	*indexAwareMemoryKVStore
}

func newCompoundTestStore() *compoundTestStore {
	return &compoundTestStore{
		indexAwareMemoryKVStore: newIndexAwareMemoryKVStore(),
	}
}

// QueryIndex performs an in-memory index scan over the KV data.
// The index name has the form "{entityType}.{fieldName}". The registrations
// map is consulted to resolve the JSON field name used during serialization.
// This allows the planner's PlanIndexLookup path to return real results
// without requiring a production storage backend.
func (s *compoundTestStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	// Parse entity type and field name from the index name.
	dotIdx := strings.LastIndex(indexName, ".")
	if dotIdx < 0 {
		return nil, nil
	}
	entityType := indexName[:dotIdx]
	fieldName := indexName[dotIdx+1:]

	// Resolve JSON field name from registrations.
	s.mu.RLock()
	regs := s.registrations[entityType]
	s.mu.RUnlock()

	jsonField := fieldName // fallback: use field name as-is
	for _, reg := range regs {
		if reg.FieldName == fieldName {
			if reg.JSONFieldName != "" {
				jsonField = reg.JSONFieldName
			}
			break
		}
	}

	wantStr := string(value)

	// Scan all entries under the entity type prefix.
	prefix := entityType + "/"
	var keys [][]byte
	scanErr := s.memoryKVStore.Scan(ctx, prefix, func(k string, v []byte) error {
		var m map[string]interface{}
		if err := encodingJSON.Unmarshal(v, &m); err != nil {
			return nil // skip unparseable entries
		}
		fieldVal, ok := m[jsonField]
		if !ok {
			return nil
		}
		// Normalize the field value to string for comparison.
		if fmt.Sprint(fieldVal) == wantStr {
			keys = append(keys, []byte(k))
		}
		return nil
	})
	if scanErr != nil {
		return nil, scanErr
	}
	return keys, nil
}

// QueryCompoundIndex stub -- compound index queries are not exercised by
// aggregation tests; the planner selects PlanIndexLookup for single-field
// predicates and falls through to QueryIndex above.
func (s *compoundTestStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, nil
}

// ScanCompoundIndex stub.
func (s *compoundTestStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, nil
}
