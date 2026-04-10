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
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

// SortDirection specifies the direction for query result ordering.
// Values are compatible with the existing SortAscending/SortDescending
// constants in types.go.
type SortDirection = int

// QueryBuilder provides a GORM-style fluent query API for building
// type-safe queries with automatic index selection. Filters are parsed
// and validated at Where() call time, not at Execute() time.
type QueryBuilder[E Entity] struct {
	dao          *genericDAO[E]
	ctx          context.Context
	filters      []filter
	sortField    string
	sortDir      SortDirection
	limit        int
	page         int
	pageSize     int
	groupByField string // field name for aggregation grouping
	err          error  // captures first error from Where() parsing
}

// Find starts a query chain on the DAO. The returned QueryBuilder
// allows chaining Where, OrderBy, Limit, Page, and terminal methods
// (Execute, ExecutePage, First, Count).
func (d *genericDAO[E]) Find(ctx context.Context) *QueryBuilder[E] {
	return &QueryBuilder[E]{
		dao: d,
		ctx: ctx,
	}
}

// Where adds a parameterized filter to the query. Field names are
// validated against entity struct fields. Operators are validated
// against the supported whitelist. Values are always parameterized
// and never string-interpolated.
//
// Supported expressions:
//
//	"field = ?"         — equality
//	"field != ?"        — inequality
//	"field > ?"         — greater than
//	"field >= ?"        — greater than or equal
//	"field < ?"         — less than
//	"field <= ?"        — less than or equal
//	"field IN ?"        — in list (arg must be a slice)
//	"field BETWEEN ? AND ?" — between (requires 2 args)
//	"field PREFIX ?"    — prefix match
func (q *QueryBuilder[E]) Where(expr string, args ...interface{}) *QueryBuilder[E] {
	if q.err != nil {
		return q
	}

	validFields := q.dao.validFieldMap()
	fieldName, op, argCount, err := parseExpression(expr, validFields)
	if err != nil {
		q.err = err
		return q
	}

	// Handle BETWEEN ... AND ... syntax: strip "AND ?" from argCount
	// (already handled by parseExpression returning argCount=2).
	f, buildErr := buildFilter(fieldName, op, argCount, args)
	if buildErr != nil {
		q.err = buildErr
		return q
	}

	q.filters = append(q.filters, *f)
	return q
}

// OrderBy sets the sort field and optional direction for query results.
// Default direction is ascending.
func (q *QueryBuilder[E]) OrderBy(field string, directions ...SortDirection) *QueryBuilder[E] {
	q.sortField = field
	if len(directions) > 0 {
		q.sortDir = directions[0]
	}
	return q
}

// Limit sets the maximum number of results to return.
func (q *QueryBuilder[E]) Limit(n int) *QueryBuilder[E] {
	q.limit = n
	return q
}

// Page sets pagination parameters (1-based page number and page size).
func (q *QueryBuilder[E]) Page(page, pageSize int) *QueryBuilder[E] {
	q.page = page
	q.pageSize = pageSize
	return q
}

// Execute runs the query and returns all matching entities.
func (q *QueryBuilder[E]) Execute() ([]E, error) {
	if q.err != nil {
		return nil, q.err
	}

	entities, err := q.executeQuery()
	if err != nil {
		return nil, err
	}

	// Apply sorting.
	if q.sortField != "" && len(entities) > 1 {
		order := SortAscending
		if q.sortDir == SortDescending {
			order = SortDescending
		}
		sortEntitiesByField(entities, q.sortField, order)
	}

	// Apply limit.
	if q.limit > 0 && len(entities) > q.limit {
		entities = entities[:q.limit]
	}

	return entities, nil
}

// ExecutePage runs a paginated query and returns a PageResult.
func (q *QueryBuilder[E]) ExecutePage() (PageResult[E], error) {
	result := PageResult[E]{
		Entities: make([]E, 0),
	}
	if q.err != nil {
		return result, q.err
	}

	page := q.page
	if page < 1 {
		page = 1
	}
	pageSize := q.pageSize
	if pageSize < 1 {
		pageSize = 25
	}

	entities, err := q.executeQuery()
	if err != nil {
		return result, err
	}

	// Apply sorting.
	if q.sortField != "" && len(entities) > 1 {
		order := SortAscending
		if q.sortDir == SortDescending {
			order = SortDescending
		}
		sortEntitiesByField(entities, q.sortField, order)
	}

	result.Total = len(entities)
	result.Page = page
	result.PageSize = pageSize

	offset := (page - 1) * pageSize
	if offset >= len(entities) {
		return result, nil
	}

	end := offset + pageSize
	if end > len(entities) {
		end = len(entities)
	}
	result.HasMore = end < len(entities)
	result.Entities = entities[offset:end]

	return result, nil
}

// First returns the first matching entity or a NotFoundError if no
// entities match.
func (q *QueryBuilder[E]) First() (E, error) {
	var zero E
	if q.err != nil {
		return zero, q.err
	}

	q.limit = 1
	entities, err := q.Execute()
	if err != nil {
		return zero, err
	}
	if len(entities) == 0 {
		return zero, NotFoundError{EntityType: q.dao.entityType, ID: 0}
	}
	return entities[0], nil
}

// Count returns the count of matching entities.
func (q *QueryBuilder[E]) Count() (int, error) {
	if q.err != nil {
		return 0, q.err
	}

	entities, err := q.executeQuery()
	if err != nil {
		return 0, err
	}
	return len(entities), nil
}

// executeQuery is the internal execution method that uses the planner
// to select the optimal execution strategy.
func (q *QueryBuilder[E]) executeQuery() ([]E, error) {
	if len(q.filters) == 0 {
		return q.fullScan()
	}

	// Build planner predicates from parsed filters.
	predicates := make([]planner.Predicate, len(q.filters))
	for i, f := range q.filters {
		predicates[i] = filterToPredicate(f)
	}

	// Build index catalog from registered indexes.
	catalog := q.dao.buildCatalog()
	p := planner.New(catalog)
	plan := p.Plan(predicates)

	switch plan.Type {
	case planner.PlanIndexLookup:
		return q.executeIndexLookup(plan)
	case planner.PlanIndexScan:
		return q.executeIndexScan(plan)
	case planner.PlanCompoundIndexLookup:
		return q.executeCompoundIndexLookup(plan)
	case planner.PlanCompoundIndexScan:
		return q.executeCompoundIndexScan(plan)
	case planner.PlanIndexIntersection:
		return q.executeIndexIntersection(plan)
	default:
		return q.executeFullScan(plan)
	}
}

// executeIndexLookup executes an exact-value index lookup and applies
// post-filters to the results.
func (q *QueryBuilder[E]) executeIndexLookup(plan *planner.Plan) ([]E, error) {
	entities, err := q.dao.QueryByIndex(q.ctx, plan.IndexField, plan.ExactValue)
	if err != nil {
		return nil, err
	}

	if len(plan.PostFilters) > 0 {
		entities = q.applyPostFilters(entities, plan.PostFilters)
	}

	return entities, nil
}

// executeIndexScan executes a range scan on an index and applies
// post-filters to the results.
func (q *QueryBuilder[E]) executeIndexScan(plan *planner.Plan) ([]E, error) {
	entities, err := q.dao.ScanByIndex(q.ctx, plan.IndexField, plan.RangeStart, plan.RangeEnd)
	if err != nil {
		return nil, err
	}

	if len(plan.PostFilters) > 0 {
		entities = q.applyPostFilters(entities, plan.PostFilters)
	}

	return entities, nil
}

// executeCompoundIndexLookup executes an exact-match compound index lookup
// and applies post-filters to the results.
func (q *QueryBuilder[E]) executeCompoundIndexLookup(plan *planner.Plan) ([]E, error) {
	entities, err := q.dao.QueryByCompoundIndex(q.ctx, plan.IndexName, plan.ExactValues)
	if err != nil {
		return nil, err
	}

	if len(plan.PostFilters) > 0 {
		entities = q.applyPostFilters(entities, plan.PostFilters)
	}

	return entities, nil
}

// executeCompoundIndexScan executes a compound index prefix + range scan
// and applies post-filters to the results.
func (q *QueryBuilder[E]) executeCompoundIndexScan(plan *planner.Plan) ([]E, error) {
	entities, err := q.dao.ScanByCompoundIndex(q.ctx, plan.IndexName, plan.ExactValues, plan.RangeStart, plan.RangeEnd)
	if err != nil {
		return nil, err
	}

	if len(plan.PostFilters) > 0 {
		entities = q.applyPostFilters(entities, plan.PostFilters)
	}

	return entities, nil
}

// executeIndexIntersection executes multiple sub-plans independently and
// intersects the resulting entity key sets. Only entities present in ALL
// sub-plan results are returned.
func (q *QueryBuilder[E]) executeIndexIntersection(plan *planner.Plan) ([]E, error) {
	if len(plan.SubPlans) == 0 {
		return q.fullScan()
	}

	// Execute each sub-plan to get entity sets.
	var sets []map[string]struct{}
	for _, sub := range plan.SubPlans {
		var entities []E
		var err error
		switch sub.Type {
		case planner.PlanIndexLookup:
			entities, err = q.executeIndexLookup(sub)
		case planner.PlanIndexScan:
			entities, err = q.executeIndexScan(sub)
		default:
			continue
		}
		if err != nil {
			return nil, err
		}

		keySet := make(map[string]struct{}, len(entities))
		for _, e := range entities {
			keySet[fmt.Sprintf("%v", any(e))] = struct{}{}
		}
		sets = append(sets, keySet)
	}

	if len(sets) == 0 {
		return q.fullScan()
	}

	// Intersect: start with the smallest set for efficiency.
	smallest := 0
	for i := 1; i < len(sets); i++ {
		if len(sets[i]) < len(sets[smallest]) {
			smallest = i
		}
	}

	// Re-execute the first sub-plan to get actual entities (we need the typed values).
	firstSub := plan.SubPlans[0]
	var baseEntities []E
	var err error
	switch firstSub.Type {
	case planner.PlanIndexLookup:
		baseEntities, err = q.executeIndexLookup(firstSub)
	case planner.PlanIndexScan:
		baseEntities, err = q.executeIndexScan(firstSub)
	}
	if err != nil {
		return nil, err
	}

	// Filter to entities present in ALL other sets.
	var result []E
	for _, e := range baseEntities {
		key := fmt.Sprintf("%v", any(e))
		inAll := true
		for i, set := range sets {
			if i == 0 {
				continue
			}
			if _, ok := set[key]; !ok {
				inAll = false
				break
			}
		}
		if inAll {
			result = append(result, e)
		}
	}

	if len(plan.PostFilters) > 0 {
		result = q.applyPostFilters(result, plan.PostFilters)
	}

	return result, nil
}

// executeFullScan performs a full entity scan and applies all predicates
// as post-filters.
func (q *QueryBuilder[E]) executeFullScan(plan *planner.Plan) ([]E, error) {
	entities, err := q.fullScan()
	if err != nil {
		return nil, err
	}

	if len(plan.PostFilters) > 0 {
		entities = q.applyPostFilters(entities, plan.PostFilters)
	}

	return entities, nil
}

// fullScan retrieves all entities of the DAO's type.
func (q *QueryBuilder[E]) fullScan() ([]E, error) {
	var entities []E

	ctx, cancel := q.dao.ensureTimeout(q.ctx)
	defer cancel()

	err := q.dao.store.Scan(ctx, q.dao.prefixKey(), func(_ string, value []byte) error {
		entity := q.dao.newEntity()
		if err := q.dao.codec.Unmarshal(value, &entity); err != nil {
			return nil // skip malformed entities
		}
		entities = append(entities, entity)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entities, nil
}

// applyPostFilters filters entities in memory using the planner's
// predicate evaluation.
func (q *QueryBuilder[E]) applyPostFilters(entities []E, predicates []planner.Predicate) []E {
	eType := reflect.TypeOf(q.dao.newEntity())
	if eType.Kind() == reflect.Ptr {
		eType = eType.Elem()
	}

	evaluator, err := planner.NewEntityEvaluator(eType)
	if err != nil {
		return entities // cannot build evaluator, return unfiltered
	}

	var filtered []E
	for _, e := range entities {
		if evaluator.Matches(e, predicates) {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

// validFieldMap returns a set of valid struct field names for the
// DAO's entity type. Built once per call from reflection. The map
// is used by parseExpression for O(1) field name validation.
func (d *genericDAO[E]) validFieldMap() map[string]struct{} {
	entity := d.newEntity()
	eType := reflect.TypeOf(entity)
	if eType.Kind() == reflect.Ptr {
		eType = eType.Elem()
	}

	fields := make(map[string]struct{})
	for i := 0; i < eType.NumField(); i++ {
		f := eType.Field(i)
		if !f.IsExported() {
			continue
		}
		fields[f.Name] = struct{}{}
		// Also add JSON field name for convenience.
		jsonTag := f.Tag.Get("json")
		if jsonTag != "" && jsonTag != "-" {
			if idx := strings.IndexByte(jsonTag, ','); idx != -1 {
				jsonTag = jsonTag[:idx]
			}
			if jsonTag != "" {
				fields[jsonTag] = struct{}{}
			}
		}
	}
	return fields
}

// buildCatalog constructs a planner.IndexCatalog from the DAO's
// entity struct tags. Index definitions are derived from struct fields
// tagged with `index:"true"` or `index:"unique"`, which were already
// parsed and registered at DAO construction time.
func (d *genericDAO[E]) buildCatalog() planner.IndexCatalog {
	entity := d.newEntity()
	eType := reflect.TypeOf(entity)
	if eType.Kind() == reflect.Ptr {
		eType = eType.Elem()
	}

	var infos []planner.IndexInfo

	// Single-field indexes from struct tags.
	for i := 0; i < eType.NumField(); i++ {
		f := eType.Field(i)
		indexTag := f.Tag.Get("index")
		if indexTag == "" {
			continue
		}

		unique := indexTag == "unique"
		indexName := d.entityType + "." + f.Name
		infos = append(infos, planner.IndexInfo{
			Name:      indexName,
			FieldName: f.Name,
			Unique:    unique,
			FieldType: f.Type.Kind().String(),
		})
	}

	// Compound indexes from CompoundIndexRegisterer interface.
	if cr, ok := any(entity).(index.CompoundIndexRegisterer); ok {
		for _, def := range cr.CompoundIndexes() {
			// Build the compound index name matching the engine's convention.
			joined := def.Fields[0]
			for _, f := range def.Fields[1:] {
				joined += "_" + f
			}
			idxName := d.entityType + "." + joined

			// Resolve field types from the struct.
			fieldTypes := make([]string, len(def.Fields))
			for i, fieldName := range def.Fields {
				if sf, found := eType.FieldByName(fieldName); found {
					fieldTypes[i] = sf.Type.Kind().String()
				} else {
					fieldTypes[i] = "string"
				}
			}

			infos = append(infos, planner.IndexInfo{
				Name:       idxName,
				FieldNames: def.Fields,
				Unique:     def.Unique,
				FieldTypes: fieldTypes,
			})
		}
	}

	return planner.NewStaticCatalog(infos)
}

// filterToPredicate converts a DAO filter to a planner.Predicate.
func filterToPredicate(f filter) planner.Predicate {
	pred := planner.Predicate{
		Field: f.field,
		Op:    filterOpToPlanner(f.op),
		Value: f.value,
	}
	if f.op == FilterBetween {
		pred.ValueEnd = f.end
	}
	if f.op == FilterIn {
		pred.Values = f.values
	}
	return pred
}

// filterOpToPlanner maps a FilterOp to a planner.Op.
var filterOpToPlannerMap = map[FilterOp]planner.Op{
	FilterEq:      planner.OpEq,
	FilterNeq:     planner.OpNeq,
	FilterGt:      planner.OpGt,
	FilterGte:     planner.OpGte,
	FilterLt:      planner.OpLt,
	FilterLte:     planner.OpLte,
	FilterIn:      planner.OpIn,
	FilterBetween: planner.OpBetween,
	FilterPrefix:  planner.OpPrefix,
}

// filterOpToPlanner converts a FilterOp to planner.Op using map dispatch.
func filterOpToPlanner(op FilterOp) planner.Op {
	if p, ok := filterOpToPlannerMap[op]; ok {
		return p
	}
	return planner.OpEq
}

// QueryError is returned when a query execution fails.
type QueryError struct {
	Op  string
	Err error
}

// Error returns a human-readable description.
func (e *QueryError) Error() string {
	return fmt.Sprintf("dao: query %s failed: %v", e.Op, e.Err)
}

// Unwrap returns the underlying error.
func (e *QueryError) Unwrap() error {
	return e.Err
}
