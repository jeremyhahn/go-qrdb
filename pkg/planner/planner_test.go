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

// helpers ----------------------------------------------------------------

func emailUniqueIndex() planner.IndexInfo {
	return planner.IndexInfo{Name: "mydb.users.Email", FieldName: "Email", Unique: true, FieldType: "string"}
}

func emailNonUniqueIndex() planner.IndexInfo {
	return planner.IndexInfo{Name: "mydb.users.Email", FieldName: "Email", Unique: false, FieldType: "string"}
}

func ageIndex() planner.IndexInfo {
	return planner.IndexInfo{Name: "mydb.users.Age", FieldName: "Age", Unique: false, FieldType: "uint64"}
}

func scoreIndex() planner.IndexInfo {
	return planner.IndexInfo{Name: "mydb.users.Score", FieldName: "Score", Unique: false, FieldType: "float64"}
}

func newPlanner(infos ...planner.IndexInfo) *planner.Planner {
	return planner.New(planner.NewStaticCatalog(infos))
}

// tests ------------------------------------------------------------------

func TestPlanNoPredicates(t *testing.T) {
	p := newPlanner(emailUniqueIndex())
	plan := p.Plan(nil)

	if plan.Type != planner.PlanFullScan {
		t.Fatalf("expected PlanFullScan, got %s", plan.Type)
	}
	if plan.EstimatedCost != 1000 {
		t.Errorf("expected cost 1000, got %f", plan.EstimatedCost)
	}
	if len(plan.PostFilters) != 0 {
		t.Errorf("expected no post-filters, got %d", len(plan.PostFilters))
	}
}

func TestPlanEqWithUniqueIndex(t *testing.T) {
	p := newPlanner(emailUniqueIndex())
	pred := planner.Predicate{Field: "Email", Op: planner.OpEq, Value: "alice@example.com"}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexLookup {
		t.Fatalf("expected PlanIndexLookup, got %s", plan.Type)
	}
	if plan.EstimatedCost != 1 {
		t.Errorf("expected cost 1 for unique lookup, got %f", plan.EstimatedCost)
	}
	if plan.IndexName != "mydb.users.Email" {
		t.Errorf("unexpected index name: %s", plan.IndexName)
	}
	if plan.IndexField != "Email" {
		t.Errorf("unexpected index field: %s", plan.IndexField)
	}
	if !plan.IsUnique {
		t.Error("expected IsUnique=true")
	}
	if plan.ExactValue != "alice@example.com" {
		t.Errorf("unexpected ExactValue: %v", plan.ExactValue)
	}
	if len(plan.PostFilters) != 0 {
		t.Errorf("unexpected post-filters: %v", plan.PostFilters)
	}
}

func TestPlanEqWithNonUniqueIndex(t *testing.T) {
	p := newPlanner(emailNonUniqueIndex())
	pred := planner.Predicate{Field: "Email", Op: planner.OpEq, Value: "bob@example.com"}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexLookup {
		t.Fatalf("expected PlanIndexLookup, got %s", plan.Type)
	}
	if plan.EstimatedCost != 10 {
		t.Errorf("expected cost 10 for non-unique lookup, got %f", plan.EstimatedCost)
	}
	if plan.IsUnique {
		t.Error("expected IsUnique=false")
	}
}

func TestPlanGtWithIndex(t *testing.T) {
	p := newPlanner(ageIndex())
	pred := planner.Predicate{Field: "Age", Op: planner.OpGt, Value: uint64(18)}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexScan {
		t.Fatalf("expected PlanIndexScan, got %s", plan.Type)
	}
	if plan.EstimatedCost != 50 {
		t.Errorf("expected cost 50, got %f", plan.EstimatedCost)
	}
	if plan.RangeStart != uint64(18) {
		t.Errorf("unexpected RangeStart: %v", plan.RangeStart)
	}
	if plan.RangeEnd != nil {
		t.Errorf("expected nil RangeEnd for OpGt, got %v", plan.RangeEnd)
	}
}

func TestPlanGteWithIndex(t *testing.T) {
	p := newPlanner(ageIndex())
	pred := planner.Predicate{Field: "Age", Op: planner.OpGte, Value: uint64(21)}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexScan {
		t.Fatalf("expected PlanIndexScan, got %s", plan.Type)
	}
	if plan.RangeStart != uint64(21) {
		t.Errorf("unexpected RangeStart: %v", plan.RangeStart)
	}
}

func TestPlanLtWithIndex(t *testing.T) {
	p := newPlanner(ageIndex())
	pred := planner.Predicate{Field: "Age", Op: planner.OpLt, Value: uint64(65)}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexScan {
		t.Fatalf("expected PlanIndexScan, got %s", plan.Type)
	}
	if plan.RangeEnd != uint64(65) {
		t.Errorf("unexpected RangeEnd: %v", plan.RangeEnd)
	}
	if plan.RangeStart != nil {
		t.Errorf("expected nil RangeStart for OpLt, got %v", plan.RangeStart)
	}
}

func TestPlanLteWithIndex(t *testing.T) {
	p := newPlanner(ageIndex())
	pred := planner.Predicate{Field: "Age", Op: planner.OpLte, Value: uint64(64)}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexScan {
		t.Fatalf("expected PlanIndexScan, got %s", plan.Type)
	}
	if plan.RangeEnd != uint64(64) {
		t.Errorf("unexpected RangeEnd: %v", plan.RangeEnd)
	}
}

func TestPlanBetweenWithIndex(t *testing.T) {
	p := newPlanner(ageIndex())
	pred := planner.Predicate{Field: "Age", Op: planner.OpBetween, Value: uint64(18), ValueEnd: uint64(65)}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexScan {
		t.Fatalf("expected PlanIndexScan, got %s", plan.Type)
	}
	if plan.RangeStart != uint64(18) {
		t.Errorf("unexpected RangeStart: %v", plan.RangeStart)
	}
	if plan.RangeEnd != uint64(65) {
		t.Errorf("unexpected RangeEnd: %v", plan.RangeEnd)
	}
}

func TestPlanPrefixWithIndex(t *testing.T) {
	p := newPlanner(emailUniqueIndex())
	pred := planner.Predicate{Field: "Email", Op: planner.OpPrefix, Value: "alice"}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexScan {
		t.Fatalf("expected PlanIndexScan, got %s", plan.Type)
	}
	if plan.RangeStart != "alice" {
		t.Errorf("unexpected RangeStart: %v", plan.RangeStart)
	}
	if plan.RangeEnd != nil {
		t.Errorf("a prefix scan has no upper bound, got RangeEnd %v", plan.RangeEnd)
	}
	// The scan reads past the prefix, so the predicate is re-checked.
	if len(plan.PostFilters) != 1 || !samePredicate(plan.PostFilters[0], pred) {
		t.Errorf("prefix must stay a post-filter, got %v", plan.PostFilters)
	}
}

func TestPlanPrefixStaysResidualBesideOtherPredicates(t *testing.T) {
	p := newPlanner(emailUniqueIndex())
	prefix := planner.Predicate{Field: "Email", Op: planner.OpPrefix, Value: "alice"}
	other := planner.Predicate{Field: "Name", Op: planner.OpEq, Value: "Alice"}

	plan := p.Plan([]planner.Predicate{other, prefix})

	if plan.Type != planner.PlanIndexScan || plan.IndexField != "Email" {
		t.Fatalf("expected an IndexScan on Email, got %s on %q", plan.Type, plan.IndexField)
	}
	if len(plan.PostFilters) != 2 ||
		!samePredicate(plan.PostFilters[0], other) || !samePredicate(plan.PostFilters[1], prefix) {
		t.Errorf("expected both predicates as post-filters in order, got %v", plan.PostFilters)
	}
}

// samePredicate compares the scalar parts of two predicates; the tests here
// never set Values.
func samePredicate(a, b planner.Predicate) bool {
	return a.Field == b.Field && a.Op == b.Op && a.Value == b.Value && a.ValueEnd == b.ValueEnd
}

// TestPlanRangeBoundInclusivity covers the flags an executor needs to answer
// the consumed predicate exactly, since it is not re-checked: only OpGt
// excludes its lower bound and only OpLt excludes its upper bound.
func TestPlanRangeBoundInclusivity(t *testing.T) {
	for _, tc := range []struct {
		op            planner.Op
		wantStartExcl bool
		wantEndExcl   bool
	}{
		{planner.OpGt, true, false},
		{planner.OpGte, false, false},
		{planner.OpLt, false, true},
		{planner.OpLte, false, false},
		{planner.OpBetween, false, false},
	} {
		t.Run(tc.op.String(), func(t *testing.T) {
			pred := planner.Predicate{Field: "Age", Op: tc.op, Value: uint64(18), ValueEnd: uint64(65)}
			plan := newPlanner(ageIndex()).Plan([]planner.Predicate{pred})

			if plan.Type != planner.PlanIndexScan {
				t.Fatalf("expected PlanIndexScan, got %s", plan.Type)
			}
			if plan.RangeStartExclusive != tc.wantStartExcl {
				t.Errorf("RangeStartExclusive = %v; want %v", plan.RangeStartExclusive, tc.wantStartExcl)
			}
			if plan.RangeEndExclusive != tc.wantEndExcl {
				t.Errorf("RangeEndExclusive = %v; want %v", plan.RangeEndExclusive, tc.wantEndExcl)
			}
			if len(plan.PostFilters) != 0 {
				t.Errorf("the range predicate must be consumed, got post-filters %v", plan.PostFilters)
			}
		})
	}
}

// TestPlanPrefixOnNumericFieldIsNotIndexed proves a text prefix is never
// answered from a numeric index, whose terms are numbers rather than text:
// the plan is a full scan with the predicate as its post-filter.
func TestPlanPrefixOnNumericFieldIsNotIndexed(t *testing.T) {
	for _, fieldType := range []string{"int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "float32", "float64"} {
		t.Run(fieldType, func(t *testing.T) {
			idx := planner.IndexInfo{Name: "mydb.users.N", FieldName: "N", FieldType: fieldType}
			pred := planner.Predicate{Field: "N", Op: planner.OpPrefix, Value: "1"}

			plan := newPlanner(idx).Plan([]planner.Predicate{pred})

			if plan.Type != planner.PlanFullScan {
				t.Fatalf("expected PlanFullScan for a prefix on %s, got %s", fieldType, plan.Type)
			}
			if len(plan.PostFilters) != 1 || plan.PostFilters[0].Op != planner.OpPrefix {
				t.Errorf("expected the prefix as the post-filter, got %v", plan.PostFilters)
			}
		})
	}
}

func TestPlanInWithIndex(t *testing.T) {
	p := newPlanner(emailUniqueIndex())
	pred := planner.Predicate{
		Field:  "Email",
		Op:     planner.OpIn,
		Values: []interface{}{"alice@example.com", "bob@example.com"},
	}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanIndexLookup {
		t.Fatalf("expected PlanIndexLookup for OpIn, got %s", plan.Type)
	}
	// OpIn always uses non-unique cost.
	if plan.EstimatedCost != 10 {
		t.Errorf("expected cost 10 for OpIn, got %f", plan.EstimatedCost)
	}
}

func TestPlanNeqNoIndex(t *testing.T) {
	p := newPlanner(emailUniqueIndex())
	pred := planner.Predicate{Field: "Email", Op: planner.OpNeq, Value: "spam@example.com"}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanFullScan {
		t.Fatalf("expected PlanFullScan for OpNeq, got %s", plan.Type)
	}
	if len(plan.PostFilters) != 1 {
		t.Fatalf("expected 1 post-filter, got %d", len(plan.PostFilters))
	}
	if plan.PostFilters[0].Op != planner.OpNeq {
		t.Errorf("unexpected post-filter op: %v", plan.PostFilters[0].Op)
	}
}

func TestPlanNoMatchingIndex(t *testing.T) {
	p := newPlanner(ageIndex())
	pred := planner.Predicate{Field: "Email", Op: planner.OpEq, Value: "alice@example.com"}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanFullScan {
		t.Fatalf("expected PlanFullScan when no index matches, got %s", plan.Type)
	}
	if len(plan.PostFilters) != 1 {
		t.Errorf("expected predicate as post-filter, got %d post-filters", len(plan.PostFilters))
	}
}

func TestPlanCompoundPredicates(t *testing.T) {
	// Email is unique (cost 1). Age is non-unique scan (cost 50).
	// Planner should pick Email, and Age becomes a post-filter.
	p := newPlanner(emailUniqueIndex(), ageIndex())

	preds := []planner.Predicate{
		{Field: "Age", Op: planner.OpGt, Value: uint64(18)},
		{Field: "Email", Op: planner.OpEq, Value: "alice@example.com"},
	}

	plan := p.Plan(preds)

	if plan.Type != planner.PlanIndexLookup {
		t.Fatalf("expected PlanIndexLookup, got %s", plan.Type)
	}
	if plan.IndexField != "Email" {
		t.Errorf("expected Email index to be chosen, got %s", plan.IndexField)
	}
	if len(plan.PostFilters) != 1 {
		t.Fatalf("expected 1 post-filter (Age), got %d", len(plan.PostFilters))
	}
	if plan.PostFilters[0].Field != "Age" {
		t.Errorf("expected Age as post-filter, got %s", plan.PostFilters[0].Field)
	}
}

func TestPlanSelectsLowestCost(t *testing.T) {
	// Two indexes cover different predicates: unique Email (cost 1) and
	// non-unique Score range scan (cost 50). The planner must pick Email.
	p := newPlanner(emailUniqueIndex(), scoreIndex())

	preds := []planner.Predicate{
		{Field: "Score", Op: planner.OpGte, Value: float64(90.0)},
		{Field: "Email", Op: planner.OpEq, Value: "alice@example.com"},
	}

	plan := p.Plan(preds)

	if plan.IndexField != "Email" {
		t.Errorf("planner should prefer unique Email index (cost 1) over Score scan (cost 50), got %s", plan.IndexField)
	}
	if plan.EstimatedCost != 1 {
		t.Errorf("expected cost 1, got %f", plan.EstimatedCost)
	}
}

func TestPlanNoIndexesDefined(t *testing.T) {
	p := newPlanner() // empty catalog
	pred := planner.Predicate{Field: "Email", Op: planner.OpEq, Value: "x@example.com"}

	plan := p.Plan([]planner.Predicate{pred})

	if plan.Type != planner.PlanFullScan {
		t.Fatalf("expected PlanFullScan with empty catalog, got %s", plan.Type)
	}
}
