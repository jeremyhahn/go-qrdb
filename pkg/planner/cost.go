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

// Heuristic cost constants for the query cost model.
// These are dimensionless relative weights used to compare execution
// strategies. Lower cost means a preferred strategy.
const (
	// costUniqueLookup is the cost of an O(1) unique-index point lookup.
	costUniqueLookup float64 = 1

	// costIndexLookup is the cost of a non-unique index point lookup
	// that may return a small number of entities.
	costIndexLookup float64 = 10

	// costIndexScan is the cost of a bounded range scan over an index.
	costIndexScan float64 = 50

	// costIntersectionOverhead is the additional cost for intersecting
	// multiple single-field index result sets.
	costIntersectionOverhead float64 = 15

	// costCompoundLookup is the cost of a compound index exact-match
	// lookup on all fields. Cheaper than a single-field non-unique
	// lookup because compound indexes are more selective.
	costCompoundLookup float64 = 3

	// costCompoundScan is the cost of a compound index prefix match
	// with an optional range on the next field.
	costCompoundScan float64 = 20

	// costFullScan is the cost of scanning every entity in the store.
	costFullScan float64 = 1000

	// costPostFilterPenalty is the per-predicate penalty added to a plan's
	// effective cost for each predicate that must be evaluated as a post-filter
	// rather than being resolved by the index. Setting this between
	// costIntersectionOverhead (15) and costCompoundScan (20) ensures that:
	//   - intersection covering all predicates beats a single-field plan with
	//     post-filters when both use non-unique indexes, and
	//   - a cheap compound or unique-index plan beats a high-cost intersection
	//     that covers one additional predicate.
	costPostFilterPenalty float64 = 20
)

// estimateCost returns the heuristic cost for a plan of the given type.
// For PlanIndexLookup the cost is further refined by whether the index
// enforces uniqueness: a unique lookup resolves to at most one entity,
// making it cheaper than a non-unique lookup. For compound plans,
// fieldsMatched controls the discount: more matched fields = cheaper.
func estimateCost(planType PlanType, isUnique bool) float64 {
	costMap := map[PlanType]float64{
		PlanFullScan:            costFullScan,
		PlanIndexScan:           costIndexScan,
		PlanIndexLookup:         costIndexLookup,
		PlanCompoundIndexLookup: costCompoundLookup,
		PlanCompoundIndexScan:   costCompoundScan,
	}
	cost, ok := costMap[planType]
	if !ok {
		return costFullScan
	}
	if planType == PlanIndexLookup && isUnique {
		return costUniqueLookup
	}
	if planType == PlanCompoundIndexLookup && isUnique {
		return costUniqueLookup
	}
	return cost
}

// estimateCompoundCost returns the cost for a compound index plan,
// discounted by the number of fields matched. More fields matched
// means higher selectivity and lower cost. Unique lookups are not
// discounted because they already represent the minimum possible cost.
func estimateCompoundCost(planType PlanType, isUnique bool, fieldsMatched int) float64 {
	base := estimateCost(planType, isUnique)
	if isUnique {
		return base
	}
	if fieldsMatched > 1 {
		return base / float64(fieldsMatched)
	}
	return base
}
