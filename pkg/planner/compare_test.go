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

// numericEntity exercises every integer and float width that toFloat64 handles.
type numericEntity struct {
	VInt    int
	VInt8   int8
	VInt16  int16
	VInt32  int32
	VInt64  int64
	VUint   uint
	VUint8  uint8
	VUint16 uint16
	VUint32 uint32
	VUint64 uint64
	VF32    float32
	VF64    float64
}

func newNumericEvaluator(t *testing.T) *planner.EntityEvaluator {
	t.Helper()
	ev, err := planner.NewEntityEvaluator(reflect.TypeOf(numericEntity{}))
	if err != nil {
		t.Fatalf("NewEntityEvaluator: %v", err)
	}
	return ev
}

// TestCompareValuesNumericWidths exercises every numeric type that toFloat64
// handles by feeding values through the OpGt predicate path.
func TestCompareValuesNumericWidths(t *testing.T) {
	ev := newNumericEvaluator(t)

	entity := numericEntity{
		VInt:    10,
		VInt8:   10,
		VInt16:  10,
		VInt32:  10,
		VInt64:  10,
		VUint:   10,
		VUint8:  10,
		VUint16: 10,
		VUint32: 10,
		VUint64: 10,
		VF32:    10.0,
		VF64:    10.0,
	}

	tests := []struct {
		field string
		value interface{} // the comparison value; entity field is 10, value is 5 → should match OpGt
	}{
		{"VInt", int(5)},
		{"VInt8", int8(5)},
		{"VInt16", int16(5)},
		{"VInt32", int32(5)},
		{"VInt64", int64(5)},
		{"VUint", uint(5)},
		{"VUint8", uint8(5)},
		{"VUint16", uint16(5)},
		{"VUint32", uint32(5)},
		{"VUint64", uint64(5)},
		{"VF32", float32(5.0)},
		{"VF64", float64(5.0)},
	}

	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			pred := planner.Predicate{Field: tc.field, Op: planner.OpGt, Value: tc.value}
			if !ev.Matches(entity, []planner.Predicate{pred}) {
				t.Errorf("%s: expected entity value 10 > %v to be true", tc.field, tc.value)
			}
		})
	}
}

// TestCompareValuesIncomparableTypes verifies that comparing two incompatible
// non-numeric non-string types falls back gracefully (returns no match for OpGt).
func TestCompareValuesIncomparableTypes(t *testing.T) {
	type withBool struct {
		Active bool
	}
	ev, err := planner.NewEntityEvaluator(reflect.TypeOf(withBool{}))
	if err != nil {
		t.Fatalf("NewEntityEvaluator: %v", err)
	}

	entity := withBool{Active: true}
	// bool is neither string nor numeric; compareValues should return 0,
	// so OpGt should not match.
	pred := planner.Predicate{Field: "Active", Op: planner.OpGt, Value: false}
	if ev.Matches(entity, []planner.Predicate{pred}) {
		t.Error("expected no match for incomparable bool type via OpGt")
	}
}

// TestMatchPredicateNonStructEntity verifies that passing a non-struct value
// to Matches returns false without panicking.
func TestMatchPredicateNonStructEntity(t *testing.T) {
	ev := newEvaluator(t) // built for testEntity struct
	pred := planner.Predicate{Field: "Name", Op: planner.OpEq, Value: "x"}

	// Pass a plain string (not a struct) as entity.
	got := ev.Matches("not-a-struct", []planner.Predicate{pred})
	if got {
		t.Error("expected Matches=false for non-struct entity")
	}
}
