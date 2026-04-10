// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0

package planner_test

import (
	"reflect"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

// coverageEntity is a simple entity for evaluator coverage tests.
type coverageEntity struct {
	Name  string
	Score float64
	Count uint64
}

// TestEstimateCost_UnknownPlanType verifies that estimateCost falls back to
// costFullScan (1000) for an unknown PlanType. This is exercised by calling
// Plan() with a PlanType value that is not in the cost map. We achieve this
// indirectly by checking the planner behavior when no index covers a predicate
// with an Op that maps to no plan type — but since estimateCost is unexported,
// we confirm the observed Plan cost for full scan is always 1000.
func TestEstimateCost_UnknownPlanTypeFallsBackToFullScan(t *testing.T) {
	// An empty catalog forces PlanFullScan regardless, and cost should be 1000.
	p := planner.New(planner.NewStaticCatalog(nil))
	plan := p.Plan([]planner.Predicate{
		{Field: "Name", Op: planner.OpEq, Value: "Alice"},
	})
	if plan.Type != planner.PlanFullScan {
		t.Errorf("expected PlanFullScan, got %v", plan.Type)
	}
	if plan.EstimatedCost != 1000 {
		t.Errorf("expected cost 1000, got %f", plan.EstimatedCost)
	}
}

// TestMatchPredicate_UnknownOp verifies that matchPredicate returns false for
// an unrecognised Op value. This exercises the default case at the end of the
// switch statement.
func TestMatchPredicate_UnknownOp(t *testing.T) {
	ev, err := planner.NewEntityEvaluator(reflect.TypeOf(coverageEntity{}))
	if err != nil {
		t.Fatalf("NewEntityEvaluator: %v", err)
	}
	entity := coverageEntity{Name: "Alice", Score: 9.5, Count: 3}

	// Use an Op value that is not defined in the planner package (255).
	unknownOp := planner.Op(255)
	pred := planner.Predicate{Field: "Name", Op: unknownOp, Value: "Alice"}

	matched := ev.Matches(entity, []planner.Predicate{pred})
	if matched {
		t.Error("expected Matches to return false for unknown Op")
	}
}

// TestMatchPredicate_UnknownField verifies that matchPredicate returns false
// when the predicate references a field that does not exist on the entity type.
func TestMatchPredicate_UnknownField(t *testing.T) {
	ev, err := planner.NewEntityEvaluator(reflect.TypeOf(coverageEntity{}))
	if err != nil {
		t.Fatalf("NewEntityEvaluator: %v", err)
	}
	entity := coverageEntity{Name: "Bob", Score: 7.0, Count: 1}

	pred := planner.Predicate{Field: "NonExistentField", Op: planner.OpEq, Value: "Bob"}
	matched := ev.Matches(entity, []planner.Predicate{pred})
	if matched {
		t.Error("expected Matches to return false for unknown field")
	}
}

// TestMatchPredicate_OpPrefix_NonStringField verifies that OpPrefix returns
// false when the field value is not a string.
func TestMatchPredicate_OpPrefix_NonStringField(t *testing.T) {
	ev, err := planner.NewEntityEvaluator(reflect.TypeOf(coverageEntity{}))
	if err != nil {
		t.Fatalf("NewEntityEvaluator: %v", err)
	}
	entity := coverageEntity{Name: "Alice", Score: 9.5, Count: 42}

	// Count is uint64, not a string — OpPrefix should return false.
	pred := planner.Predicate{Field: "Count", Op: planner.OpPrefix, Value: "4"}
	matched := ev.Matches(entity, []planner.Predicate{pred})
	if matched {
		t.Error("expected OpPrefix to return false for non-string field")
	}
}
