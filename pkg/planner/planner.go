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

// Planner selects optimal execution strategies for queries by inspecting
// predicates against available indexes in an IndexCatalog.
//
// The selection algorithm evaluates each predicate against every available
// index, computes a heuristic cost for the resulting plan, and picks the
// single lowest-cost option. Predicates not covered by the chosen index
// are returned as PostFilters for subsequent in-memory evaluation.
//
// Multi-index intersection is reserved for future work.
type Planner struct {
	catalog IndexCatalog
}

// New creates a Planner that consults the provided IndexCatalog when
// building query plans.
func New(catalog IndexCatalog) *Planner {
	return &Planner{catalog: catalog}
}

// candidate holds a tentative plan together with the predicate index that
// produced it, so we can collect the remaining predicates as post-filters.
type candidate struct {
	plan         Plan
	predicateIdx int
	// consumedPredicates tracks which predicates are covered by a compound
	// index plan. nil for single-field plans (use predicateIdx instead).
	consumedPredicates map[int]bool
	// postFilterCount is the number of predicates not covered by this plan.
	// Used by betterCandidate to compute effective cost.
	postFilterCount int
}

// Plan produces an execution plan for the given set of predicates.
//
// Algorithm:
//  1. No predicates → PlanFullScan at costFullScan.
//  2. For each predicate, find the first index whose FieldName matches
//     the predicate's Field, then derive the plan type and cost:
//     - OpEq  → PlanIndexLookup (costUniqueLookup if unique, costIndexLookup otherwise)
//     - OpIn  → PlanIndexLookup (treated as a set of point lookups; non-unique cost)
//     - OpGt, OpGte, OpLt, OpLte, OpBetween, OpPrefix → PlanIndexScan
//     - OpNeq → no index usable; skip
//  3. Select the candidate with the lowest estimated cost whose cost is
//     below costFullScan.
//  4. All predicates except the winning one become PostFilters.
//  5. If no candidate beats the full-scan cost, return PlanFullScan with
//     all predicates as PostFilters.
func (p *Planner) Plan(predicates []Predicate) *Plan {
	if len(predicates) == 0 {
		return &Plan{
			Type:          PlanFullScan,
			EstimatedCost: costFullScan,
		}
	}

	indexes := p.catalog.AvailableIndexes()

	// Phase 1: Evaluate compound indexes. A compound index covering 2+
	// predicates is typically cheaper than any single-field index.
	compoundBest := p.planCompoundIndex(predicates, indexes)

	// Phase 2: Evaluate single-field indexes (existing logic).
	n := len(predicates)
	var singleBest *candidate

	for i, pred := range predicates {
		idx, ok := findIndex(indexes, pred.Field)
		if !ok {
			continue
		}

		c := buildCandidate(pred, idx, i)
		if c == nil {
			continue
		}
		c.postFilterCount = n - candidateCoverage(c)

		if singleBest == nil || c.plan.EstimatedCost < singleBest.plan.EstimatedCost {
			singleBest = c
		}
	}

	// Phase 3: Try multi-index intersection. When 2+ predicates each
	// have a usable single-field index, intersecting their results can
	// be cheaper than picking one index and post-filtering the rest.
	intersectionBest := p.planIndexIntersection(predicates, indexes)

	// Phase 4: Pick the best plan across compound, single-field, and intersection.
	// Primary sort key: number of predicates covered (more = better).
	// Secondary sort key: estimated cost (lower = better).
	best := singleBest
	if compoundBest != nil && betterCandidate(compoundBest, best) {
		best = compoundBest
	}
	if intersectionBest != nil && betterCandidate(intersectionBest, best) {
		best = intersectionBest
	}

	if best == nil || best.plan.EstimatedCost >= costFullScan {
		return &Plan{
			Type:          PlanFullScan,
			PostFilters:   append([]Predicate(nil), predicates...),
			EstimatedCost: costFullScan,
		}
	}

	// Collect post-filters: all predicates not consumed by the chosen plan.
	if best.consumedPredicates != nil {
		// Compound plan -- consumed multiple predicates.
		postFilters := make([]Predicate, 0, len(predicates))
		for i, pred := range predicates {
			if !best.consumedPredicates[i] {
				postFilters = append(postFilters, pred)
			}
		}
		best.plan.PostFilters = postFilters
	} else {
		// Single-field plan -- consumed one predicate.
		postFilters := make([]Predicate, 0, len(predicates)-1)
		for i, pred := range predicates {
			if i != best.predicateIdx {
				postFilters = append(postFilters, pred)
			}
		}
		best.plan.PostFilters = postFilters
	}

	return &best.plan
}

// planIndexIntersection attempts to build a plan that uses 2+ single-field
// indexes and intersects their result sets. This is beneficial when no
// compound index covers the predicates, but multiple single-field indexes
// exist. The cost is max(sub-plan costs) + intersection overhead.
func (p *Planner) planIndexIntersection(predicates []Predicate, indexes []IndexInfo) *candidate {
	if len(predicates) < 2 {
		return nil
	}

	var subPlans []*Plan
	consumed := make(map[int]bool)
	maxSubCost := float64(0)

	for i, pred := range predicates {
		idx, ok := findIndex(indexes, pred.Field)
		if !ok {
			continue
		}

		c := buildCandidate(pred, idx, i)
		if c == nil {
			continue
		}

		subPlans = append(subPlans, &c.plan)
		consumed[i] = true
		if c.plan.EstimatedCost > maxSubCost {
			maxSubCost = c.plan.EstimatedCost
		}
	}

	// Need at least 2 sub-plans to justify intersection.
	if len(subPlans) < 2 {
		return nil
	}

	totalCost := maxSubCost + costIntersectionOverhead

	// Collect remaining predicates as post-filters.
	var postFilters []Predicate
	for i, pred := range predicates {
		if !consumed[i] {
			postFilters = append(postFilters, pred)
		}
	}

	return &candidate{
		plan: Plan{
			Type:          PlanIndexIntersection,
			SubPlans:      subPlans,
			PostFilters:   postFilters,
			EstimatedCost: totalCost,
		},
		consumedPredicates: consumed,
		postFilterCount:    len(postFilters),
	}
}

// findIndex returns the first single-field IndexInfo whose FieldName equals field.
func findIndex(indexes []IndexInfo, field string) (IndexInfo, bool) {
	for _, idx := range indexes {
		if !idx.IsCompound() && idx.FieldName == field {
			return idx, true
		}
	}
	return IndexInfo{}, false
}

// planCompoundIndex evaluates all compound indexes in the catalog and
// returns the best compound plan, or nil if no compound index covers 2+
// predicates. A compound index (A, B, C) is usable when predicates cover
// a contiguous prefix of the fields: A=? covers 1, A=? AND B=? covers 2,
// etc. The last matched field may use a range operator.
func (p *Planner) planCompoundIndex(predicates []Predicate, indexes []IndexInfo) *candidate {
	var best *candidate

	for _, idx := range indexes {
		if !idx.IsCompound() {
			continue
		}

		// Build a map of predicate field → predicate index for fast lookup.
		predByField := make(map[string]int, len(predicates))
		for i, pred := range predicates {
			predByField[pred.Field] = i
		}

		// Walk the compound index fields in order and match predicates.
		var exactValues []interface{}
		consumed := make(map[int]bool)
		fieldsMatched := 0
		hasRange := false
		var rangePred *Predicate
		var rangePredIdx int

	fieldLoop:
		for _, field := range idx.FieldNames {
			predIdx, ok := predByField[field]
			if !ok {
				break // No predicate for this field; stop prefix matching.
			}
			pred := predicates[predIdx]

			switch pred.Op {
			case OpEq, OpIn:
				// Exact match on this field — extends the prefix.
				exactValues = append(exactValues, pred.Value)
				consumed[predIdx] = true
				fieldsMatched++
			case OpNeq:
				break fieldLoop // Inequality cannot use index.
			default:
				// Range operator (Gt, Gte, Lt, Lte, Between, Prefix) on
				// this field. This is valid only as the LAST matched field.
				hasRange = true
				rangePred = &predicates[predIdx]
				rangePredIdx = predIdx
				consumed[predIdx] = true
				fieldsMatched++
				break fieldLoop // Range terminates prefix matching.
			}
		}

		// Need at least 2 matched fields to justify a compound index
		// over a single-field index.
		if fieldsMatched < 2 {
			continue
		}

		planType := PlanCompoundIndexLookup
		if hasRange {
			planType = PlanCompoundIndexScan
		}

		cost := estimateCompoundCost(planType, idx.Unique, fieldsMatched)

		plan := Plan{
			Type:          planType,
			IndexName:     idx.Name,
			IndexFields:   idx.FieldNames,
			IsUnique:      idx.Unique,
			ExactValues:   exactValues,
			EstimatedCost: cost,
		}

		if hasRange && rangePred != nil {
			applyRangeBounds(&plan, *rangePred)
		}

		c := &candidate{
			plan:               plan,
			predicateIdx:       rangePredIdx,
			consumedPredicates: consumed,
			postFilterCount:    len(predicates) - len(consumed),
		}

		if best == nil || cost < best.plan.EstimatedCost {
			best = c
		}
	}

	return best
}

// buildCandidate derives a candidate plan from a predicate and a matching
// index. Returns nil when the operator cannot benefit from an index (OpNeq).
func buildCandidate(pred Predicate, idx IndexInfo, predicateIdx int) *candidate {
	// opPlanType maps each Op to its associated PlanType. OpNeq is absent
	// because inequality predicates cannot use an index efficiently.
	opPlanType := map[Op]PlanType{
		OpEq:      PlanIndexLookup,
		OpIn:      PlanIndexLookup,
		OpGt:      PlanIndexScan,
		OpGte:     PlanIndexScan,
		OpLt:      PlanIndexScan,
		OpLte:     PlanIndexScan,
		OpBetween: PlanIndexScan,
		OpPrefix:  PlanIndexScan,
	}

	pt, ok := opPlanType[pred.Op]
	if !ok {
		return nil
	}

	// OpIn always uses non-unique cost even on a unique index, because the
	// caller may supply multiple values requiring multiple lookups.
	isUnique := idx.Unique && pred.Op != OpIn

	plan := Plan{
		Type:          pt,
		IndexName:     idx.Name,
		IndexField:    idx.FieldName,
		IsUnique:      idx.Unique,
		EstimatedCost: estimateCost(pt, isUnique),
	}

	switch pt {
	case PlanIndexLookup:
		plan.ExactValue = pred.Value
	case PlanIndexScan:
		applyRangeBounds(&plan, pred)
	}

	return &candidate{plan: plan, predicateIdx: predicateIdx}
}

// betterCandidate reports whether challenger is a better plan than current.
//
// Plans are ranked by effective cost, defined as:
//
//	effective_cost = plan.EstimatedCost + costPostFilterPenalty * uncovered_predicates
//
// where uncovered_predicates = totalPredicates - candidateCoverage(c).
// This penalises plans that leave more predicates as post-filters, while still
// allowing a cheap unique-lookup to beat a high-cost multi-predicate plan.
//
// A nil current is always beaten by any non-nil challenger.
func betterCandidate(challenger, current *candidate) bool {
	if current == nil {
		return true
	}
	return effectiveCost(challenger) <= effectiveCost(current)
}

// effectiveCost computes the effective cost of a candidate plan by adding a
// penalty for each predicate that must be handled as a post-filter.
// The penalty is calculated using the stored totalPredicates field so that
// coverage can be measured without re-counting at call time.
func effectiveCost(c *candidate) float64 {
	return c.plan.EstimatedCost + costPostFilterPenalty*float64(c.postFilterCount)
}

// candidateCoverage returns the number of predicates consumed by the candidate.
// Single-field plans always cover exactly 1 predicate; compound and
// intersection plans track their coverage in consumedPredicates.
func candidateCoverage(c *candidate) int {
	if c.consumedPredicates != nil {
		return len(c.consumedPredicates)
	}
	return 1
}

// applyRangeBounds sets the RangeStart/RangeEnd fields of plan based on the
// predicate operator and its values.
func applyRangeBounds(plan *Plan, pred Predicate) {
	switch pred.Op {
	case OpGt, OpGte:
		plan.RangeStart = pred.Value
	case OpLt, OpLte:
		plan.RangeEnd = pred.Value
	case OpBetween:
		plan.RangeStart = pred.Value
		plan.RangeEnd = pred.ValueEnd
	case OpPrefix:
		plan.RangeStart = pred.Value
	}
}
