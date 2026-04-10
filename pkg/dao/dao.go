// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-dragondb.
//
// go-dragondb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import "context"

// Pager provides pagination operations for entity collections.
type Pager[E any] interface {
	// Page retrieves a single page of entities.
	// The PageQuery specifies page number, size, and sort order.
	// Returns a PageResult containing the entities and pagination metadata.
	Page(ctx context.Context, query PageQuery) (PageResult[E], error)

	// ForEachPage iterates over all entities page by page.
	// The callback function is invoked for each page of entities.
	// Return nil from the callback to continue, or an error to stop.
	// Iteration stops when HasMore is false or the callback returns an error.
	ForEachPage(ctx context.Context, query PageQuery, fn PagerFunc[E]) error
}

// GenericDAO provides type-safe CRUD operations for entities.
// It abstracts the underlying storage mechanism (Raft, PebbleDB, Memory)
// and provides consistent operations across all DragonDB modes.
//
// The DAO is parameterized by entity type E, which must implement Entity.
// All operations are context-aware for cancellation and timeout support.
//
// Example usage:
//
//	type User struct {
//	    ID    uint64 `json:"id"`
//	    Name  string `json:"name"`
//	    Email string `json:"email"`
//	}
//
//	func (u *User) EntityID() uint64      { return u.ID }
//	func (u *User) SetEntityID(id uint64) { u.ID = id }
//
//	// Create DAO
//	userDAO, err := dao.New(kvStore, "users", func() *User { return &User{} })
//
//	// Save entity
//	user := &User{Name: "Alice", Email: "alice@example.com"}
//	err = userDAO.Save(ctx, user)  // ID auto-generated
//
//	// Get by ID
//	user, err = userDAO.Get(ctx, user.EntityID())
//
//	// Delete
//	err = userDAO.Delete(ctx, user)
//
//	// Count all
//	count, err := userDAO.Count(ctx)
//
//	// Paginate
//	result, err := userDAO.Page(ctx, dao.PageQuery{Page: 1, PageSize: 25})
//
//	// Conditional save (optimistic locking)
//	err = userDAO.SaveWithCondition(ctx, user, dao.Condition{
//	    Target: dao.ConditionVersion, Operator: dao.OpEqual, Value: int64(1),
//	})
type GenericDAO[E Entity] interface {
	// Save persists an entity to storage.
	// If EntityID() returns 0, a new ID is generated using the configured IDGenerator.
	// Existing entities with the same ID are overwritten.
	Save(ctx context.Context, entity E) error

	// SaveWithCondition persists an entity only if the given condition is satisfied.
	// This enables optimistic locking patterns at the GenericDAO level without
	// requiring callers to manage transactions directly for single-operation cases.
	// Internally, it wraps the operation in a single-operation transaction.
	SaveWithCondition(ctx context.Context, entity E, cond Condition) error

	// Get retrieves an entity by ID.
	// Returns ErrNotFound if the entity doesn't exist.
	Get(ctx context.Context, id uint64) (E, error)

	// Delete removes an entity from storage.
	// Returns ErrInvalidEntityID if entity.EntityID() is 0.
	// Returns nil if the entity doesn't exist (idempotent).
	Delete(ctx context.Context, entity E) error

	// Count returns the total number of entities of this type.
	Count(ctx context.Context) (int, error)

	// Pager provides pagination operations.
	Pager[E]

	// EntityType returns the entity type name used for key prefixing.
	EntityType() string

	// QueryByIndex returns all entities matching the given field value.
	// The fieldName must correspond to an indexed field (struct tag `index:"..."`).
	QueryByIndex(ctx context.Context, fieldName string, value interface{}) ([]E, error)

	// QueryOneByIndex returns a single entity matching the given field value.
	// Returns NotFoundError if no matching entity exists.
	QueryOneByIndex(ctx context.Context, fieldName string, value interface{}) (E, error)

	// ScanByIndex returns all entities with indexed field values in the
	// range [start, end).
	ScanByIndex(ctx context.Context, fieldName string, start, end interface{}) ([]E, error)

	// PageByIndex returns a page of entities matching the given index field value.
	PageByIndex(ctx context.Context, fieldName string, value interface{}, query PageQuery) (PageResult[E], error)

	// CountByIndex returns the count of entities matching the given index value
	// without loading entities.
	CountByIndex(ctx context.Context, fieldName string, value interface{}) (int, error)

	// QueryByCompoundIndex returns all entities matching the given compound
	// index field values. The indexName must be the fully-qualified compound
	// index name (e.g., "entityType.Field1_Field2"). The fieldValues must
	// match all fields of the compound index in order.
	QueryByCompoundIndex(ctx context.Context, indexName string, fieldValues []interface{}) ([]E, error)

	// ScanByCompoundIndex returns entities matching a prefix of compound
	// index fields with an optional range on the next field. prefixValues
	// are exact-match values for the leading N fields; rangeStart and
	// rangeEnd define a half-open range on field N+1.
	ScanByCompoundIndex(ctx context.Context, indexName string, prefixValues []interface{}, rangeStart, rangeEnd interface{}) ([]E, error)

	// Find starts a fluent query chain with automatic index selection.
	// The returned QueryBuilder allows chaining Where, OrderBy, Limit,
	// and Page methods before executing with Execute, First, or Count.
	//
	// Example:
	//
	//	user, err := userDAO.Find(ctx).Where("Email = ?", "alice@example.com").First()
	//	users, err := userDAO.Find(ctx).Where("Age > ?", 30).OrderBy("Name").Execute()
	Find(ctx context.Context) *QueryBuilder[E]
}
