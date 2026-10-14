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

package planner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// catalogWithCompound returns a catalog with both single-field and compound indexes.
func catalogWithCompound() IndexCatalog {
	return NewStaticCatalog([]IndexInfo{
		{Name: "messages.ConversationID", FieldName: "ConversationID", FieldType: "string"},
		{Name: "messages.Timestamp", FieldName: "Timestamp", FieldType: "int64"},
		{Name: "messages.IsRead", FieldName: "IsRead", FieldType: "bool"},
		{
			Name:       "messages.ConversationID_Timestamp",
			FieldNames: []string{"ConversationID", "Timestamp"},
			FieldTypes: []string{"string", "int64"},
		},
		{
			Name:       "messages.ConversationID_IsRead",
			FieldNames: []string{"ConversationID", "IsRead"},
			FieldTypes: []string{"string", "bool"},
		},
	})
}

func TestPlan_CompoundIndexSelected_TwoExactPredicates(t *testing.T) {
	p := New(catalogWithCompound())

	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "Timestamp", Op: OpEq, Value: int64(1000)},
	})

	require.NotNil(t, plan)
	assert.Equal(t, PlanCompoundIndexLookup, plan.Type)
	assert.Equal(t, "messages.ConversationID_Timestamp", plan.IndexName)
	assert.Len(t, plan.ExactValues, 2)
	assert.Empty(t, plan.PostFilters, "both predicates consumed by compound index")
}

func TestPlan_CompoundIndexScan_PrefixPlusRange(t *testing.T) {
	p := New(catalogWithCompound())

	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "Timestamp", Op: OpGt, Value: int64(1000)},
	})

	require.NotNil(t, plan)
	assert.Equal(t, PlanCompoundIndexScan, plan.Type)
	assert.Equal(t, "messages.ConversationID_Timestamp", plan.IndexName)
	assert.Len(t, plan.ExactValues, 1)
	assert.Equal(t, "conv-1", plan.ExactValues[0])
	assert.Equal(t, int64(1000), plan.RangeStart)
	assert.Empty(t, plan.PostFilters)
}

func TestPlan_CompoundIndexScan_ExclusiveRangeStart(t *testing.T) {
	p := New(catalogWithCompound())

	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "Timestamp", Op: OpGt, Value: int64(1000)},
	})

	require.NotNil(t, plan)
	require.Equal(t, PlanCompoundIndexScan, plan.Type)
	assert.True(t, plan.RangeStartExclusive, "OpGt must exclude its bound")
	assert.False(t, plan.RangeEndExclusive)
}

// TestPlan_CompoundNumericPrefixIsNotConsumed proves a text prefix over a
// numeric compound member is not answered from the index: the member's terms
// are numbers, which hold no text prefix. The predicate stays a post-filter
// and the compound index is not chosen on one matched field.
func TestPlan_CompoundNumericPrefixIsNotConsumed(t *testing.T) {
	p := New(catalogWithCompound())

	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "Timestamp", Op: OpPrefix, Value: "10"},
	})

	require.NotNil(t, plan)
	assert.NotEqual(t, PlanCompoundIndexScan, plan.Type)
	require.Len(t, plan.PostFilters, 1)
	assert.Equal(t, "Timestamp", plan.PostFilters[0].Field)
	assert.Equal(t, OpPrefix, plan.PostFilters[0].Op)
}

func TestPlan_CompoundPreferredOverSingleField(t *testing.T) {
	p := New(catalogWithCompound())

	// Both ConversationID and IsRead have single-field indexes, but the
	// compound index (ConversationID, IsRead) should be preferred because
	// it covers both predicates.
	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "IsRead", Op: OpEq, Value: false},
	})

	require.NotNil(t, plan)
	assert.Equal(t, PlanCompoundIndexLookup, plan.Type)
	assert.Equal(t, "messages.ConversationID_IsRead", plan.IndexName)
	assert.Empty(t, plan.PostFilters)
}

func TestPlan_CompoundNotUsable_NoLeadingPrefix(t *testing.T) {
	p := New(catalogWithCompound())

	// Only Timestamp predicate -- compound index (ConversationID, Timestamp)
	// requires ConversationID as leading field.
	plan := p.Plan([]Predicate{
		{Field: "Timestamp", Op: OpEq, Value: int64(1000)},
	})

	require.NotNil(t, plan)
	// Should fall back to single-field index on Timestamp.
	assert.Equal(t, PlanIndexLookup, plan.Type)
	assert.Equal(t, "messages.Timestamp", plan.IndexName)
}

func TestPlan_CompoundWithPostFilter(t *testing.T) {
	p := New(catalogWithCompound())

	// Three predicates: compound covers ConversationID+Timestamp,
	// IsRead becomes a post-filter.
	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "Timestamp", Op: OpGt, Value: int64(1000)},
		{Field: "IsRead", Op: OpEq, Value: false},
	})

	require.NotNil(t, plan)
	// The planner should pick either compound index. ConversationID_Timestamp
	// covers ConversationID+Timestamp; ConversationID_IsRead covers
	// ConversationID+IsRead. Both cover 2 predicates. The plan should have
	// exactly 1 post-filter.
	assert.Len(t, plan.PostFilters, 1)
}

func TestPlan_IntersectionFallback(t *testing.T) {
	// Catalog with only single-field indexes (no compound).
	catalog := NewStaticCatalog([]IndexInfo{
		{Name: "messages.ConversationID", FieldName: "ConversationID", FieldType: "string"},
		{Name: "messages.IsRead", FieldName: "IsRead", FieldType: "bool"},
	})
	p := New(catalog)

	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "IsRead", Op: OpEq, Value: false},
	})

	require.NotNil(t, plan)
	// Intersection should be used since both predicates have indexes
	// and no compound index covers them.
	assert.Equal(t, PlanIndexIntersection, plan.Type)
	assert.Len(t, plan.SubPlans, 2)
	assert.Empty(t, plan.PostFilters)
}

func TestPlan_CompoundBeatsIntersection(t *testing.T) {
	p := New(catalogWithCompound())

	plan := p.Plan([]Predicate{
		{Field: "ConversationID", Op: OpEq, Value: "conv-1"},
		{Field: "IsRead", Op: OpEq, Value: false},
	})

	require.NotNil(t, plan)
	// Compound index should beat intersection because compound cost (3/2=1.5)
	// is less than intersection cost (10+15=25).
	assert.Equal(t, PlanCompoundIndexLookup, plan.Type)
}

func TestEstimateCompoundCost_Discount(t *testing.T) {
	// 2 fields matched: cost = 3/2 = 1.5
	cost := estimateCompoundCost(PlanCompoundIndexLookup, false, 2)
	assert.Equal(t, 1.5, cost)

	// 3 fields matched: cost = 3/3 = 1.0
	cost = estimateCompoundCost(PlanCompoundIndexLookup, false, 3)
	assert.Equal(t, 1.0, cost)

	// Unique: cost = 1 regardless of fields
	cost = estimateCompoundCost(PlanCompoundIndexLookup, true, 2)
	assert.Equal(t, costUniqueLookup, cost)

	// Scan with 2 fields: cost = 20/2 = 10
	cost = estimateCompoundCost(PlanCompoundIndexScan, false, 2)
	assert.Equal(t, 10.0, cost)
}

func TestPlanType_String_Compound(t *testing.T) {
	assert.Equal(t, "CompoundIndexLookup", PlanCompoundIndexLookup.String())
	assert.Equal(t, "CompoundIndexScan", PlanCompoundIndexScan.String())
	assert.Equal(t, "IndexIntersection", PlanIndexIntersection.String())
}

func TestIndexInfo_IsCompound(t *testing.T) {
	single := IndexInfo{FieldName: "Email"}
	assert.False(t, single.IsCompound())

	compound := IndexInfo{FieldNames: []string{"A", "B"}}
	assert.True(t, compound.IsCompound())

	oneField := IndexInfo{FieldNames: []string{"A"}}
	assert.False(t, oneField.IsCompound())
}
