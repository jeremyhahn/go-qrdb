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

// Package planner provides a query planner for automatic index selection.
// Given a set of filter predicates and an index catalog, the planner selects
// the lowest-cost execution strategy: a full entity scan, an exact index
// lookup, or a bounded index range scan. Predicates not covered by the
// chosen index are returned as post-filters for in-memory evaluation.
package planner

// Op represents a comparison operator for query predicates.
type Op uint8

const (
	OpEq      Op = iota + 1 // = (equality)
	OpNeq                   // != (inequality)
	OpGt                    // > (greater than)
	OpGte                   // >= (greater than or equal)
	OpLt                    // < (less than)
	OpLte                   // <= (less than or equal)
	OpBetween               // BETWEEN (inclusive range)
	OpPrefix                // PREFIX (string prefix match)
	OpIn                    // IN (membership in a set)
)

// opStrings maps each Op to its canonical string representation.
var opStrings = map[Op]string{
	OpEq:      "=",
	OpNeq:     "!=",
	OpGt:      ">",
	OpGte:     ">=",
	OpLt:      "<",
	OpLte:     "<=",
	OpBetween: "BETWEEN",
	OpPrefix:  "PREFIX",
	OpIn:      "IN",
}

// String returns the string representation of the operator.
func (o Op) String() string {
	if s, ok := opStrings[o]; ok {
		return s
	}
	return "unknown"
}

// Predicate represents a single filter condition on a named field.
// Value is the primary comparison value. ValueEnd is the upper bound
// used only with OpBetween. Values is the membership set used only with OpIn.
type Predicate struct {
	Field    string
	Op       Op
	Value    interface{}
	ValueEnd interface{}   // upper bound; used with OpBetween
	Values   []interface{} // membership set; used with OpIn
}

// PlanType categorizes the execution strategy chosen by the planner.
type PlanType uint8

const (
	// PlanFullScan scans all entities without index assistance.
	PlanFullScan PlanType = iota

	// PlanIndexLookup performs an exact value lookup via a secondary index.
	PlanIndexLookup

	// PlanIndexScan performs a bounded range scan via a secondary index.
	PlanIndexScan

	// PlanCompoundIndexLookup performs an exact-match lookup on all fields
	// of a compound index.
	PlanCompoundIndexLookup

	// PlanCompoundIndexScan performs a prefix match on leading compound
	// index fields with an optional range on the next field.
	PlanCompoundIndexScan

	// PlanIndexIntersection executes multiple single-field index
	// lookups and intersects the resulting entity key sets.
	PlanIndexIntersection
)

// planTypeStrings maps each PlanType to its canonical string representation.
var planTypeStrings = map[PlanType]string{
	PlanFullScan:            "FullScan",
	PlanIndexLookup:         "IndexLookup",
	PlanIndexScan:           "IndexScan",
	PlanCompoundIndexLookup: "CompoundIndexLookup",
	PlanCompoundIndexScan:   "CompoundIndexScan",
	PlanIndexIntersection:   "IndexIntersection",
}

// String returns the string representation of the plan type.
func (pt PlanType) String() string {
	if s, ok := planTypeStrings[pt]; ok {
		return s
	}
	return "unknown"
}

// Plan describes how to execute a query against the store. The planner
// selects the strategy with the lowest estimated cost. Predicates not
// satisfied by the chosen index are placed in PostFilters for subsequent
// in-memory evaluation by EntityEvaluator.
type Plan struct {
	// Type is the selected execution strategy.
	Type PlanType

	// IndexName is the fully-qualified name of the chosen index.
	// Empty for PlanFullScan.
	IndexName string

	// IndexField is the struct field name that the chosen index covers.
	// Empty for PlanFullScan.
	IndexField string

	// IsUnique reports whether the chosen index enforces uniqueness.
	IsUnique bool

	// ExactValue is the lookup key for PlanIndexLookup.
	ExactValue interface{}

	// RangeStart is the lower bound (inclusive) for PlanIndexScan.
	RangeStart interface{}

	// RangeEnd is the upper bound (inclusive) for PlanIndexScan.
	RangeEnd interface{}

	// IndexFields is the ordered list of struct field names for compound
	// index plans. Empty for single-field plans.
	IndexFields []string

	// ExactValues holds per-field lookup values for PlanCompoundIndexLookup.
	ExactValues []interface{}

	// SubPlans holds the individual index plans for PlanIndexIntersection.
	// Each sub-plan is executed independently; their key sets are intersected.
	SubPlans []*Plan

	// PostFilters contains predicates that must be evaluated in memory
	// after the index access returns candidate entity keys.
	PostFilters []Predicate

	// EstimatedCost is the heuristic cost assigned to this plan.
	// Lower values indicate cheaper strategies.
	EstimatedCost float64
}
