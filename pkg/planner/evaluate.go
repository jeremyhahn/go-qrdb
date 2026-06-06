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
	"reflect"
	"strings"
)

// EntityEvaluator evaluates a set of predicates against entity values using
// pre-computed struct field indexes for efficient repeated access.
//
// EntityEvaluator is safe for concurrent read-only use after construction.
// It is not safe to call NewEntityEvaluator on the same entityType concurrently.
type EntityEvaluator struct {
	entityType reflect.Type
	fieldIdx   map[string]int // struct field name → field index within the struct
}

// NewEntityEvaluator builds an EntityEvaluator for the given struct type.
// entityType must be a reflect.Type whose Kind is reflect.Struct; a pointer
// to a struct is also accepted and unwrapped automatically.
//
// Returns FieldNotFoundError if entityType is not a struct or pointer-to-struct.
func NewEntityEvaluator(entityType reflect.Type) (*EntityEvaluator, error) {
	// Unwrap a single level of pointer.
	if entityType.Kind() == reflect.Pointer {
		entityType = entityType.Elem()
	}
	if entityType.Kind() != reflect.Struct {
		return nil, &FieldNotFoundError{
			Field:      "<type>",
			EntityType: entityType.String(),
		}
	}

	fieldIdx := make(map[string]int, entityType.NumField())
	for i := 0; i < entityType.NumField(); i++ {
		fieldIdx[entityType.Field(i).Name] = i
	}

	return &EntityEvaluator{
		entityType: entityType,
		fieldIdx:   fieldIdx,
	}, nil
}

// Matches returns true if entity satisfies all predicates (AND semantics).
// entity must be a struct or pointer-to-struct whose dynamic type matches
// the type used to construct EntityEvaluator. If any predicate's field is
// not present on the entity, that predicate evaluates to false.
func (e *EntityEvaluator) Matches(entity interface{}, predicates []Predicate) bool {
	if len(predicates) == 0 {
		return true
	}

	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return false
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return false
	}

	for _, pred := range predicates {
		if !e.matchPredicate(v, pred) {
			return false
		}
	}
	return true
}

// matchPredicate evaluates a single predicate against a struct value.
func (e *EntityEvaluator) matchPredicate(v reflect.Value, pred Predicate) bool {
	idx, ok := e.fieldIdx[pred.Field]
	if !ok {
		return false
	}

	fieldVal := v.Field(idx).Interface()

	switch pred.Op {
	case OpEq:
		return reflect.DeepEqual(fieldVal, pred.Value)

	case OpNeq:
		return !reflect.DeepEqual(fieldVal, pred.Value)

	case OpGt:
		return compareValues(fieldVal, pred.Value) > 0

	case OpGte:
		return compareValues(fieldVal, pred.Value) >= 0

	case OpLt:
		return compareValues(fieldVal, pred.Value) < 0

	case OpLte:
		return compareValues(fieldVal, pred.Value) <= 0

	case OpBetween:
		return compareValues(fieldVal, pred.Value) >= 0 &&
			compareValues(fieldVal, pred.ValueEnd) <= 0

	case OpPrefix:
		s, ok1 := fieldVal.(string)
		prefix, ok2 := pred.Value.(string)
		if !ok1 || !ok2 {
			return false
		}
		return strings.HasPrefix(s, prefix)

	case OpIn:
		for _, candidate := range pred.Values {
			if reflect.DeepEqual(fieldVal, candidate) {
				return true
			}
		}
		return false
	}

	return false
}

// compareValues compares two values of compatible types and returns:
//
//	-1 if a < b
//	 0 if a == b
//	+1 if a > b
//
// Supported types are int64, uint64, float64, and string.
// Mixed-type comparisons where both values can be coerced to float64 are
// handled via float64 promotion. Incomparable or unrecognized types return 0.
func compareValues(a, b interface{}) int {
	// Try string comparison first since it does not require numeric promotion.
	as, aIsStr := a.(string)
	bs, bIsStr := b.(string)
	if aIsStr && bIsStr {
		return strings.Compare(as, bs)
	}

	af, aOk := toFloat64(a)
	bf, bOk := toFloat64(b)
	if !aOk || !bOk {
		return 0
	}

	switch {
	case af < bf:
		return -1
	case af > bf:
		return 1
	default:
		return 0
	}
}

// toFloat64 converts numeric types to float64 for ordered comparison.
// Returns (value, true) on success, (0, false) for unsupported types.
func toFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}
