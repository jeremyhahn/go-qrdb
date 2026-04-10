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
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

// testEntity is the entity type used throughout evaluator tests.
type testEntity struct {
	Name  string
	Age   uint64
	Score float64
	Rank  int64
}

func newEvaluator(t *testing.T) *planner.EntityEvaluator {
	t.Helper()
	ev, err := planner.NewEntityEvaluator(reflect.TypeOf(testEntity{}))
	if err != nil {
		t.Fatalf("NewEntityEvaluator failed: %v", err)
	}
	return ev
}

// TestEvaluatorOpEq covers equality matching for each supported field type.
func TestEvaluatorOpEq(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "Alice", Age: 30, Score: 9.5, Rank: int64(-1)}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "string match",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpEq, Value: "Alice"},
			match: true,
		},
		{
			name:  "string no match",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpEq, Value: "Bob"},
			match: false,
		},
		{
			name:  "uint64 match",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpEq, Value: uint64(30)},
			match: true,
		},
		{
			name:  "uint64 no match",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpEq, Value: uint64(31)},
			match: false,
		},
		{
			name:  "float64 match",
			pred:  planner.Predicate{Field: "Score", Op: planner.OpEq, Value: float64(9.5)},
			match: true,
		},
		{
			name:  "int64 match",
			pred:  planner.Predicate{Field: "Rank", Op: planner.OpEq, Value: int64(-1)},
			match: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpNeq covers inequality matching.
func TestEvaluatorOpNeq(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "Alice", Age: 30}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "string differs",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpNeq, Value: "Bob"},
			match: true,
		},
		{
			name:  "string same",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpNeq, Value: "Alice"},
			match: false,
		},
		{
			name:  "uint64 differs",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpNeq, Value: uint64(25)},
			match: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpGt covers greater-than matching.
func TestEvaluatorOpGt(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Age: 30, Score: 7.5, Rank: int64(5)}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "uint64 greater",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpGt, Value: uint64(20)},
			match: true,
		},
		{
			name:  "uint64 equal not greater",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpGt, Value: uint64(30)},
			match: false,
		},
		{
			name:  "float64 greater",
			pred:  planner.Predicate{Field: "Score", Op: planner.OpGt, Value: float64(5.0)},
			match: true,
		},
		{
			name:  "int64 not greater",
			pred:  planner.Predicate{Field: "Rank", Op: planner.OpGt, Value: int64(10)},
			match: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpGte covers greater-than-or-equal matching.
func TestEvaluatorOpGte(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Age: 30}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "greater",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpGte, Value: uint64(29)},
			match: true,
		},
		{
			name:  "equal",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpGte, Value: uint64(30)},
			match: true,
		},
		{
			name:  "less",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpGte, Value: uint64(31)},
			match: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpLt covers less-than matching.
func TestEvaluatorOpLt(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Age: 30}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "less",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpLt, Value: uint64(31)},
			match: true,
		},
		{
			name:  "equal not less",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpLt, Value: uint64(30)},
			match: false,
		},
		{
			name:  "greater not less",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpLt, Value: uint64(10)},
			match: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpLte covers less-than-or-equal matching.
func TestEvaluatorOpLte(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Age: 30}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "less",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpLte, Value: uint64(31)},
			match: true,
		},
		{
			name:  "equal",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpLte, Value: uint64(30)},
			match: true,
		},
		{
			name:  "greater",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpLte, Value: uint64(10)},
			match: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpBetween covers inclusive range matching.
func TestEvaluatorOpBetween(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Age: 30, Score: 7.5}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "uint64 inside range",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpBetween, Value: uint64(20), ValueEnd: uint64(40)},
			match: true,
		},
		{
			name:  "uint64 at lower bound",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpBetween, Value: uint64(30), ValueEnd: uint64(40)},
			match: true,
		},
		{
			name:  "uint64 at upper bound",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpBetween, Value: uint64(20), ValueEnd: uint64(30)},
			match: true,
		},
		{
			name:  "uint64 below range",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpBetween, Value: uint64(31), ValueEnd: uint64(50)},
			match: false,
		},
		{
			name:  "float64 inside range",
			pred:  planner.Predicate{Field: "Score", Op: planner.OpBetween, Value: float64(5.0), ValueEnd: float64(10.0)},
			match: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpPrefix covers string prefix matching.
func TestEvaluatorOpPrefix(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "AliceSmith"}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "matching prefix",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpPrefix, Value: "Alice"},
			match: true,
		},
		{
			name:  "full string match",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpPrefix, Value: "AliceSmith"},
			match: true,
		},
		{
			name:  "empty prefix",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpPrefix, Value: ""},
			match: true,
		},
		{
			name:  "non-matching prefix",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpPrefix, Value: "Bob"},
			match: false,
		},
		{
			name:  "prefix on non-string field returns false",
			pred:  planner.Predicate{Field: "Age", Op: planner.OpPrefix, Value: "1"},
			match: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorOpIn covers set membership matching.
func TestEvaluatorOpIn(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "Alice", Age: 30}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name: "string in set",
			pred: planner.Predicate{
				Field:  "Name",
				Op:     planner.OpIn,
				Values: []interface{}{"Alice", "Bob"},
			},
			match: true,
		},
		{
			name: "string not in set",
			pred: planner.Predicate{
				Field:  "Name",
				Op:     planner.OpIn,
				Values: []interface{}{"Charlie", "Dave"},
			},
			match: false,
		},
		{
			name: "uint64 in set",
			pred: planner.Predicate{
				Field:  "Age",
				Op:     planner.OpIn,
				Values: []interface{}{uint64(25), uint64(30), uint64(35)},
			},
			match: true,
		},
		{
			name: "empty set",
			pred: planner.Predicate{
				Field:  "Name",
				Op:     planner.OpIn,
				Values: []interface{}{},
			},
			match: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}

// TestEvaluatorMultiplePredicatesAND verifies that all predicates must be
// satisfied (AND semantics).
func TestEvaluatorMultiplePredicatesAND(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "Alice", Age: 30, Score: 9.5}

	// Both match → true.
	preds := []planner.Predicate{
		{Field: "Name", Op: planner.OpEq, Value: "Alice"},
		{Field: "Age", Op: planner.OpGt, Value: uint64(20)},
	}
	if !ev.Matches(entity, preds) {
		t.Error("expected Matches=true when all predicates satisfied")
	}

	// One fails → false.
	preds[1] = planner.Predicate{Field: "Age", Op: planner.OpGt, Value: uint64(50)}
	if ev.Matches(entity, preds) {
		t.Error("expected Matches=false when one predicate fails")
	}
}

// TestEvaluatorEmptyPredicates verifies that no predicates means every entity
// matches.
func TestEvaluatorEmptyPredicates(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "Alice"}

	if !ev.Matches(entity, nil) {
		t.Error("expected Matches=true for nil predicates")
	}
	if !ev.Matches(entity, []planner.Predicate{}) {
		t.Error("expected Matches=true for empty predicate slice")
	}
}

// TestEvaluatorMissingField verifies that a predicate on an absent field
// evaluates to false.
func TestEvaluatorMissingField(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "Alice"}

	pred := planner.Predicate{Field: "NonExistent", Op: planner.OpEq, Value: "x"}
	if ev.Matches(entity, []planner.Predicate{pred}) {
		t.Error("expected Matches=false for non-existent field")
	}
}

// TestEvaluatorNilPointerEntity verifies that a nil pointer entity does not
// panic and returns false.
func TestEvaluatorNilPointerEntity(t *testing.T) {
	ev, err := planner.NewEntityEvaluator(reflect.TypeOf((*testEntity)(nil)))
	if err != nil {
		t.Fatalf("NewEntityEvaluator failed: %v", err)
	}

	var entity *testEntity
	pred := planner.Predicate{Field: "Name", Op: planner.OpEq, Value: "Alice"}
	if ev.Matches(entity, []planner.Predicate{pred}) {
		t.Error("expected Matches=false for nil pointer entity")
	}
}

// TestEvaluatorPointerToStruct verifies that passing a pointer to struct works.
func TestEvaluatorPointerToStruct(t *testing.T) {
	ev := newEvaluator(t)
	entity := &testEntity{Name: "Alice", Age: 30}

	pred := planner.Predicate{Field: "Name", Op: planner.OpEq, Value: "Alice"}
	if !ev.Matches(entity, []planner.Predicate{pred}) {
		t.Error("expected Matches=true for pointer-to-struct entity")
	}
}

// TestNewEntityEvaluatorNonStructError verifies that a non-struct type is rejected.
func TestNewEntityEvaluatorNonStructError(t *testing.T) {
	_, err := planner.NewEntityEvaluator(reflect.TypeOf(""))
	if err == nil {
		t.Fatal("expected error for non-struct type, got nil")
	}
	if _, ok := err.(*planner.FieldNotFoundError); !ok {
		t.Errorf("expected FieldNotFoundError, got %T: %v", err, err)
	}
}

// TestEvaluatorStringComparison verifies ordered string comparisons.
func TestEvaluatorStringComparison(t *testing.T) {
	ev := newEvaluator(t)
	entity := testEntity{Name: "Bob"}

	tests := []struct {
		name  string
		pred  planner.Predicate
		match bool
	}{
		{
			name:  "name gt Alice",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpGt, Value: "Alice"},
			match: true,
		},
		{
			name:  "name lt Charlie",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpLt, Value: "Charlie"},
			match: true,
		},
		{
			name:  "name gte Bob",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpGte, Value: "Bob"},
			match: true,
		},
		{
			name:  "name lte Bob",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpLte, Value: "Bob"},
			match: true,
		},
		{
			name:  "name gt Charlie is false",
			pred:  planner.Predicate{Field: "Name", Op: planner.OpGt, Value: "Charlie"},
			match: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ev.Matches(entity, []planner.Predicate{tc.pred})
			if got != tc.match {
				t.Errorf("Matches() = %v, want %v", got, tc.match)
			}
		})
	}
}
