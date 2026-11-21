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
