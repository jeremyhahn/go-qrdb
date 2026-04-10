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
	"fmt"
	"reflect"
	"strconv"
)

// AggType identifies an aggregation function.
type AggType uint8

const (
	// AggCount counts the number of entities in each group.
	AggCount AggType = iota + 1

	// AggSum computes the sum of a numeric field.
	AggSum

	// AggAvg computes the average of a numeric field.
	AggAvg

	// AggMin finds the minimum value of a field.
	AggMin

	// AggMax finds the maximum value of a field.
	AggMax
)

// AggFunc defines an aggregation function to apply during query execution.
type AggFunc struct {
	// Type is the aggregation function type.
	Type AggType

	// Field is the struct field name to aggregate. Empty for AggCount.
	Field string

	// Alias is the result key name in GroupResult.Values.
	Alias string
}

// Count creates an AggFunc that counts entities.
func Count() AggFunc {
	return AggFunc{Type: AggCount, Alias: "count"}
}

// Sum creates an AggFunc that sums a numeric field.
func Sum(field string) AggFunc {
	return AggFunc{Type: AggSum, Field: field, Alias: "sum_" + field}
}

// Avg creates an AggFunc that averages a numeric field.
func Avg(field string) AggFunc {
	return AggFunc{Type: AggAvg, Field: field, Alias: "avg_" + field}
}

// Min creates an AggFunc that finds the minimum of a field.
func Min(field string) AggFunc {
	return AggFunc{Type: AggMin, Field: field, Alias: "min_" + field}
}

// Max creates an AggFunc that finds the maximum of a field.
func Max(field string) AggFunc {
	return AggFunc{Type: AggMax, Field: field, Alias: "max_" + field}
}

// AggregateResult holds the results of an aggregation query.
type AggregateResult struct {
	// Groups contains one entry per group. When no GroupBy is specified,
	// there is a single group with a nil Key.
	Groups []GroupResult
}

// GroupResult holds the aggregated values for a single group.
type GroupResult struct {
	// Key is the group key value. nil when no GroupBy is specified.
	Key interface{}

	// Values maps aggregation aliases to their computed values.
	Values map[string]interface{}
}

// accumulator tracks running aggregation state for a single group.
type accumulator struct {
	count int64
	sum   float64
	min   *float64
	max   *float64
}

// GroupBy sets the grouping field for aggregation queries.
// Must be called before Aggregate.
func (q *QueryBuilder[E]) GroupBy(field string) *QueryBuilder[E] {
	q.groupByField = field
	return q
}

// Aggregate executes the query with aggregation functions applied.
// Returns AggregateResult containing grouped results. When GroupBy
// is set, results are grouped by the specified field; otherwise a
// single group contains the aggregate of all matching entities.
//
// Usage:
//
//	result, err := dao.Find(ctx).
//	    Where("IsRead = ?", false).
//	    GroupBy("ConversationID").
//	    Aggregate(Count())
func (q *QueryBuilder[E]) Aggregate(aggs ...AggFunc) (AggregateResult, error) {
	if q.err != nil {
		return AggregateResult{}, q.err
	}

	// Execute the base query to get matching entities.
	entities, err := q.executeQuery()
	if err != nil {
		return AggregateResult{}, err
	}

	// Build accumulators per group.
	groups := make(map[string]*accumulator)
	groupKeys := make(map[string]interface{})
	groupOrder := make([]string, 0) // preserve insertion order

	for _, entity := range entities {
		groupKey := ""
		var groupVal interface{}

		if q.groupByField != "" {
			groupVal = extractFieldValue(entity, q.groupByField)
			groupKey = fmt.Sprint(groupVal)
		}

		acc, exists := groups[groupKey]
		if !exists {
			acc = &accumulator{}
			groups[groupKey] = acc
			groupKeys[groupKey] = groupVal
			groupOrder = append(groupOrder, groupKey)
		}

		acc.count++

		// Update per-agg accumulators.
		for _, agg := range aggs {
			if agg.Field == "" {
				continue
			}
			val := extractFieldValue(entity, agg.Field)
			numVal, ok := toFloat64(val)
			if !ok {
				continue
			}

			switch agg.Type {
			case AggSum, AggAvg:
				acc.sum += numVal
			case AggMin:
				if acc.min == nil || numVal < *acc.min {
					v := numVal
					acc.min = &v
				}
			case AggMax:
				if acc.max == nil || numVal > *acc.max {
					v := numVal
					acc.max = &v
				}
			}
		}
	}

	// Build results.
	result := AggregateResult{
		Groups: make([]GroupResult, 0, len(groups)),
	}

	for _, gk := range groupOrder {
		acc := groups[gk]
		values := make(map[string]interface{}, len(aggs))

		for _, agg := range aggs {
			switch agg.Type {
			case AggCount:
				values[agg.Alias] = acc.count
			case AggSum:
				values[agg.Alias] = acc.sum
			case AggAvg:
				if acc.count > 0 {
					values[agg.Alias] = acc.sum / float64(acc.count)
				} else {
					values[agg.Alias] = float64(0)
				}
			case AggMin:
				if acc.min != nil {
					values[agg.Alias] = *acc.min
				}
			case AggMax:
				if acc.max != nil {
					values[agg.Alias] = *acc.max
				}
			}
		}

		result.Groups = append(result.Groups, GroupResult{
			Key:    groupKeys[gk],
			Values: values,
		})
	}

	return result, nil
}

// extractFieldValue extracts a field value from an entity using reflection.
func extractFieldValue(entity interface{}, fieldName string) interface{} {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	f := v.FieldByName(fieldName)
	if !f.IsValid() {
		return nil
	}
	return f.Interface()
}

// toFloat64 attempts to convert an interface value to float64.
func toFloat64(val interface{}) (float64, bool) {
	if val == nil {
		return 0, false
	}
	switch v := val.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	case bool:
		if v {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}
