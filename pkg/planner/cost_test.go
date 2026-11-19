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

package planner_test

import (
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

// TestPlanEqWithUniqueIndex_CostOne verifies the full-scan cost via the Plan
// output for a unique index lookup. The cost model is validated indirectly
// through the public API since cost constants are unexported.
func TestCostUniqueIndexLookup(t *testing.T) {
	p := planner.New(planner.NewStaticCatalog([]planner.IndexInfo{
		{Name: "idx.Email", FieldName: "Email", Unique: true, FieldType: "string"},
	}))
	plan := p.Plan([]planner.Predicate{
		{Field: "Email", Op: planner.OpEq, Value: "x@example.com"},
	})
	if plan.EstimatedCost != 1 {
		t.Errorf("unique lookup cost: want 1, got %f", plan.EstimatedCost)
	}
}

func TestCostNonUniqueIndexLookup(t *testing.T) {
	p := planner.New(planner.NewStaticCatalog([]planner.IndexInfo{
		{Name: "idx.Email", FieldName: "Email", Unique: false, FieldType: "string"},
	}))
	plan := p.Plan([]planner.Predicate{
		{Field: "Email", Op: planner.OpEq, Value: "x@example.com"},
	})
	if plan.EstimatedCost != 10 {
		t.Errorf("non-unique lookup cost: want 10, got %f", plan.EstimatedCost)
	}
}

func TestCostIndexScan(t *testing.T) {
	p := planner.New(planner.NewStaticCatalog([]planner.IndexInfo{
		{Name: "idx.Age", FieldName: "Age", Unique: false, FieldType: "uint64"},
	}))
	plan := p.Plan([]planner.Predicate{
		{Field: "Age", Op: planner.OpGt, Value: uint64(18)},
	})
	if plan.EstimatedCost != 50 {
		t.Errorf("index scan cost: want 50, got %f", plan.EstimatedCost)
	}
}

func TestCostFullScan(t *testing.T) {
	p := planner.New(planner.NewStaticCatalog([]planner.IndexInfo{}))
	plan := p.Plan([]planner.Predicate{
		{Field: "Age", Op: planner.OpEq, Value: uint64(30)},
	})
	if plan.EstimatedCost != 1000 {
		t.Errorf("full scan cost: want 1000, got %f", plan.EstimatedCost)
	}
}

func TestCostUniqueIndexScanNotCheaperThanLookup(t *testing.T) {
	// A range scan on a unique index should use the scan cost (50), not the
	// unique lookup cost (1), because the range may span multiple values.
	p := planner.New(planner.NewStaticCatalog([]planner.IndexInfo{
		{Name: "idx.Email", FieldName: "Email", Unique: true, FieldType: "string"},
	}))
	plan := p.Plan([]planner.Predicate{
		{Field: "Email", Op: planner.OpPrefix, Value: "alice"},
	})
	if plan.EstimatedCost != 50 {
		t.Errorf("unique index scan cost: want 50, got %f", plan.EstimatedCost)
	}
}

func TestCostOpInUsesNonUniqueCost(t *testing.T) {
	// OpIn on a unique index still uses non-unique cost (10) because the set
	// may contain multiple values requiring multiple lookups.
	p := planner.New(planner.NewStaticCatalog([]planner.IndexInfo{
		{Name: "idx.Email", FieldName: "Email", Unique: true, FieldType: "string"},
	}))
	plan := p.Plan([]planner.Predicate{
		{
			Field:  "Email",
			Op:     planner.OpIn,
			Values: []interface{}{"a@x.com", "b@x.com"},
		},
	})
	if plan.EstimatedCost != 10 {
		t.Errorf("OpIn cost: want 10, got %f", plan.EstimatedCost)
	}
}
