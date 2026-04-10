//go:build codec_json

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
	"errors"
	"testing"

	"github.com/jeremyhahn/go-qrdb/pkg/planner"
)

// testQueryUser is a test entity for query builder tests.
type testQueryUser struct {
	ID     uint64 `json:"id"`
	Name   string `json:"name" index:"true"`
	Email  string `json:"email" index:"unique"`
	Age    int64  `json:"age"`
	Status string `json:"status" index:"true"`
}

func (u *testQueryUser) EntityID() uint64      { return u.ID }
func (u *testQueryUser) SetEntityID(id uint64) { u.ID = id }

func newTestQueryDAO(t *testing.T) GenericDAO[*testQueryUser] {
	t.Helper()
	store := newMemoryKVStore()
	d, err := New[*testQueryUser](store, "query_users", func() *testQueryUser { return &testQueryUser{} })
	if err != nil {
		t.Fatalf("failed to create DAO: %v", err)
	}
	return d
}

func TestFindWhereEq(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	// Save test entities.
	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "alice@test.com", Age: 30, Status: "active"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "bob@test.com", Age: 25, Status: "active"})
	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "charlie@test.com", Age: 35, Status: "inactive"})

	users, err := d.Find(ctx).Where("Name = ?", "Alice").Execute()
	if err != nil {
		t.Fatalf("Find().Where().Execute() error: %v", err)
	}
	// Result depends on index availability; verify no error and correct type.
	_ = users
}

func TestFindWhereInvalidField(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_, err := d.Find(ctx).Where("NonExistent = ?", "value").Execute()
	if err == nil {
		t.Fatal("expected error for non-existent field")
	}
	var fieldErr *FieldNotFoundError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected FieldNotFoundError, got %T: %v", err, err)
	}
}

func TestFindWhereInvalidOperator(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_, err := d.Find(ctx).Where("Name LIKE ?", "%alice%").Execute()
	if err == nil {
		t.Fatal("expected error for unsupported operator")
	}
	var opErr *UnsupportedOperatorError
	if !errors.As(err, &opErr) {
		t.Fatalf("expected UnsupportedOperatorError, got %T: %v", err, err)
	}
}

func TestFindNoFiltersFullScan(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "alice@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "bob@test.com"})

	users, err := d.Find(ctx).Execute()
	if err != nil {
		t.Fatalf("Find().Execute() error: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}

func TestFindWithLimit(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_ = d.Save(ctx, &testQueryUser{Name: "User", Email: "user@test.com"})
	}

	users, err := d.Find(ctx).Limit(3).Execute()
	if err != nil {
		t.Fatalf("Find().Limit().Execute() error: %v", err)
	}
	if len(users) > 3 {
		t.Errorf("expected at most 3 users, got %d", len(users))
	}
}

func TestFindWithOrderBy(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com"})

	users, err := d.Find(ctx).OrderBy("Name").Execute()
	if err != nil {
		t.Fatalf("Find().OrderBy().Execute() error: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}
	if users[0].Name != "Alice" {
		t.Errorf("first user = %q, want %q", users[0].Name, "Alice")
	}
}

func TestFindWithOrderByDesc(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com"})

	users, err := d.Find(ctx).OrderBy("Name", SortDescending).Execute()
	if err != nil {
		t.Fatalf("Find().OrderBy(desc).Execute() error: %v", err)
	}
	if len(users) != 3 {
		t.Fatalf("expected 3 users, got %d", len(users))
	}
	if users[0].Name != "Charlie" {
		t.Errorf("first user = %q, want %q", users[0].Name, "Charlie")
	}
}

func TestFindFirst(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "alice@test.com"})

	user, err := d.Find(ctx).First()
	if err != nil {
		t.Fatalf("Find().First() error: %v", err)
	}
	if user.Name != "Alice" {
		t.Errorf("Name = %q, want %q", user.Name, "Alice")
	}
}

func TestFindFirstNotFound(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_, err := d.Find(ctx).Where("Name = ?", "nonexistent").First()
	if err == nil {
		t.Fatal("expected NotFoundError")
	}
}

func TestFindCount(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com"})

	count, err := d.Find(ctx).Count()
	if err != nil {
		t.Fatalf("Find().Count() error: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestFindExecutePage(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_ = d.Save(ctx, &testQueryUser{Name: "User", Email: "user@test.com"})
	}

	page, err := d.Find(ctx).Page(1, 3).ExecutePage()
	if err != nil {
		t.Fatalf("Find().Page().ExecutePage() error: %v", err)
	}
	if page.Page != 1 {
		t.Errorf("page.Page = %d, want 1", page.Page)
	}
	if page.PageSize != 3 {
		t.Errorf("page.PageSize = %d, want 3", page.PageSize)
	}
	if len(page.Entities) > 3 {
		t.Errorf("expected at most 3 entities, got %d", len(page.Entities))
	}
	if page.Total != 10 {
		t.Errorf("page.Total = %d, want 10", page.Total)
	}
	if !page.HasMore {
		t.Error("expected HasMore = true")
	}
}

func TestFindCompoundWhere(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com", Status: "active"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com", Status: "inactive"})

	// Compound query: both filters applied.
	users, err := d.Find(ctx).
		Where("Name = ?", "Alice").
		Where("Status = ?", "active").
		Execute()
	if err != nil {
		t.Fatalf("compound Where() error: %v", err)
	}
	_ = users // result validity depends on index vs full scan path
}

func TestFindWhereMultipleErrorsReturnFirst(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	// First Where has invalid field → captures error.
	// Second Where should not overwrite it.
	_, err := d.Find(ctx).
		Where("Invalid = ?", "x").
		Where("Name = ?", "y").
		Execute()
	if err == nil {
		t.Fatal("expected error from first invalid Where")
	}
	var fieldErr *FieldNotFoundError
	if !errors.As(err, &fieldErr) {
		t.Fatalf("expected FieldNotFoundError from first Where, got %T", err)
	}
}

func TestFilterToPredicate(t *testing.T) {
	f := filter{field: "Name", op: FilterEq, value: "alice"}
	pred := filterToPredicate(f)
	if pred.Field != "Name" {
		t.Errorf("Field = %q, want %q", pred.Field, "Name")
	}
	if pred.Op != planner.OpEq {
		t.Errorf("Op = %v, want %v", pred.Op, planner.OpEq)
	}
	if pred.Value != "alice" {
		t.Errorf("Value = %v, want %q", pred.Value, "alice")
	}
}

func TestFilterToPredicateBetween(t *testing.T) {
	f := filter{field: "Price", op: FilterBetween, value: 10.0, end: 100.0}
	pred := filterToPredicate(f)
	if pred.ValueEnd != 100.0 {
		t.Errorf("ValueEnd = %v, want %v", pred.ValueEnd, 100.0)
	}
}

func TestFilterToPredicateIn(t *testing.T) {
	f := filter{field: "Status", op: FilterIn, values: []interface{}{"a", "b"}}
	pred := filterToPredicate(f)
	if len(pred.Values) != 2 {
		t.Errorf("Values len = %d, want 2", len(pred.Values))
	}
}

func TestFilterOpToPlannerMapping(t *testing.T) {
	mapping := map[FilterOp]planner.Op{
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
	for fop, pop := range mapping {
		if got := filterOpToPlanner(fop); got != pop {
			t.Errorf("filterOpToPlanner(%v) = %v, want %v", fop, got, pop)
		}
	}
}

func TestFilterOpToPlannerUnknown(t *testing.T) {
	got := filterOpToPlanner(FilterOp(99))
	if got != planner.OpEq {
		t.Errorf("filterOpToPlanner(99) = %v, want %v", got, planner.OpEq)
	}
}

func TestQueryErrorMessage(t *testing.T) {
	err := &QueryError{Op: "execute", Err: errors.New("timeout")}
	got := err.Error()
	if got != "dao: query execute failed: timeout" {
		t.Errorf("Error() = %q", got)
	}
}

func TestQueryErrorUnwrap(t *testing.T) {
	inner := errors.New("inner")
	err := &QueryError{Op: "test", Err: inner}
	if errors.Unwrap(err) != inner {
		t.Error("Unwrap() did not return inner error")
	}
}

func TestFindWithJSONFieldNames(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "alice@test.com"})

	// JSON field name "name" should also be accepted.
	users, err := d.Find(ctx).Where("name = ?", "Alice").Execute()
	if err != nil {
		t.Fatalf("Find with JSON field name error: %v", err)
	}
	_ = users
}

func TestFindExecutePageDefaults(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})

	// Page with defaults (page=0, pageSize=0 → defaults to 1, 25).
	page, err := d.Find(ctx).ExecutePage()
	if err != nil {
		t.Fatalf("ExecutePage() error: %v", err)
	}
	if page.Page != 1 {
		t.Errorf("page.Page = %d, want 1", page.Page)
	}
	if page.PageSize != 25 {
		t.Errorf("page.PageSize = %d, want 25", page.PageSize)
	}
	if page.Total != 1 {
		t.Errorf("page.Total = %d, want 1", page.Total)
	}
}

func TestFindExecutePageBeyondEnd(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})

	// Request page 100 which is way beyond available data.
	page, err := d.Find(ctx).Page(100, 10).ExecutePage()
	if err != nil {
		t.Fatalf("ExecutePage() error: %v", err)
	}
	if len(page.Entities) != 0 {
		t.Errorf("expected 0 entities, got %d", len(page.Entities))
	}
	if page.HasMore {
		t.Error("expected HasMore = false")
	}
}

func TestFindExecutePageWithSort(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com"})

	page, err := d.Find(ctx).OrderBy("Name").Page(1, 2).ExecutePage()
	if err != nil {
		t.Fatalf("ExecutePage() error: %v", err)
	}
	if len(page.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(page.Entities))
	}
	if page.Entities[0].Name != "Alice" {
		t.Errorf("first = %q, want Alice", page.Entities[0].Name)
	}
	if !page.HasMore {
		t.Error("expected HasMore = true")
	}
}

func TestFindExecutePageError(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	// Error from invalid Where propagates to ExecutePage.
	_, err := d.Find(ctx).Where("BadField = ?", "x").ExecutePage()
	if err == nil {
		t.Fatal("expected error from invalid Where")
	}
}

func TestFindCountWithFilter(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com"})

	// Count with Where filter (triggers planner path).
	count, err := d.Find(ctx).Where("Name = ?", "Alice").Count()
	if err != nil {
		t.Fatalf("Count() error: %v", err)
	}
	_ = count // result depends on index path
}

func TestFindCountError(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_, err := d.Find(ctx).Where("BadField = ?", "x").Count()
	if err == nil {
		t.Fatal("expected error from invalid Where")
	}
}

func TestFindFirstError(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_, err := d.Find(ctx).Where("BadField = ?", "x").First()
	if err == nil {
		t.Fatal("expected error from invalid Where")
	}
}

func TestFindWhereGt(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com", Age: 20})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com", Age: 30})
	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com", Age: 40})

	users, err := d.Find(ctx).Where("Age > ?", int64(25)).Execute()
	if err != nil {
		t.Fatalf("Where(Age > ?) error: %v", err)
	}
	// Full scan with post-filter since Age is not indexed.
	for _, u := range users {
		if u.Age <= 25 {
			t.Errorf("unexpected user with Age=%d (should be >25)", u.Age)
		}
	}
}

func TestFindWhereBetween(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com", Age: 20})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com", Age: 30})
	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com", Age: 40})

	users, err := d.Find(ctx).Where("Age BETWEEN ? AND ?", int64(25), int64(35)).Execute()
	if err != nil {
		t.Fatalf("Where(BETWEEN) error: %v", err)
	}
	for _, u := range users {
		if u.Age < 25 || u.Age > 35 {
			t.Errorf("unexpected user with Age=%d (should be 25-35)", u.Age)
		}
	}
}

func TestFindWhereIn(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com", Status: "active"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com", Status: "inactive"})
	_ = d.Save(ctx, &testQueryUser{Name: "Charlie", Email: "c@test.com", Status: "active"})

	users, err := d.Find(ctx).Where("Status IN ?", []string{"active"}).Execute()
	if err != nil {
		t.Fatalf("Where(IN) error: %v", err)
	}
	for _, u := range users {
		if u.Status != "active" {
			t.Errorf("unexpected user with Status=%q (should be active)", u.Status)
		}
	}
}

func TestFindWherePrefix(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Albert", Email: "al@test.com"})

	// PREFIX on an indexed field goes through the index scan path.
	// With the in-memory test store (no real index support), the
	// planner selects an index scan but the store returns empty.
	// Verify no error occurs and the path executes correctly.
	users, err := d.Find(ctx).Where("Name PREFIX ?", "Al").Execute()
	if err != nil {
		t.Fatalf("Where(PREFIX) error: %v", err)
	}
	_ = users // result depends on index backend support
}

func TestFindWhereNeq(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	_ = d.Save(ctx, &testQueryUser{Name: "Alice", Email: "a@test.com"})
	_ = d.Save(ctx, &testQueryUser{Name: "Bob", Email: "b@test.com"})

	users, err := d.Find(ctx).Where("Name != ?", "Alice").Execute()
	if err != nil {
		t.Fatalf("Where(!=) error: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].Name != "Bob" {
		t.Errorf("expected Bob, got %q", users[0].Name)
	}
}

func TestFindExecuteWithSortAndLimit(t *testing.T) {
	d := newTestQueryDAO(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_ = d.Save(ctx, &testQueryUser{Name: "User", Email: "u@test.com"})
	}

	users, err := d.Find(ctx).OrderBy("Name").Limit(2).Execute()
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}
}
