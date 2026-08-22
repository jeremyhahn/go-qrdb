// Copyright (c) 2025 Jeremy Hahn
// Copyright (c) 2025 Automate The Things, LLC
//
// This file is part of go-qrdb.
//
// go-qrdb is dual-licensed:
//
// 1. GNU Affero General Public License v3.0 (AGPL-3.0)
//    See LICENSE file or visit https://www.gnu.org/licenses/agpl-3.0.html
//
// 2. Commercial License
//    Contact licensing@automatethethings.com for commercial licensing options.

package dao

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// TransactionalDAO extends GenericDAO with transaction support.
// It provides all CRUD operations from GenericDAO plus the ability
// to begin transactions for atomic multi-operation commits.
//
// Example:
//
//	dao, err := NewTransactional[*User](kvStore, "users", func() *User { return &User{} })
//
//	// Begin a transaction
//	tx, err := dao.BeginTx(ctx)
//	if err != nil { return err }
//	defer tx.Rollback()
//
//	// Queue operations
//	tx.Save(user1)
//	tx.Save(user2)
//	tx.Delete(user3)
//
//	// Commit atomically
//	return tx.Commit(ctx)
type TransactionalDAO[E Entity] interface {
	GenericDAO[E]

	// BeginTx starts a new transaction.
	// The returned DAOTransaction must be committed or rolled back.
	BeginTx(ctx context.Context) (DAOTransaction[E], error)
}

// transactionalDAO embeds genericDAO and adds transaction support.
type transactionalDAO[E Entity] struct {
	*genericDAO[E]
}

// Ensure transactionalDAO implements TransactionalDAO.
var _ TransactionalDAO[Entity] = (*transactionalDAO[Entity])(nil)

// NewTransactional creates a new TransactionalDAO for the given entity type.
//
// Parameters:
//   - store: The underlying KVStore (from server.GetKVStore(shardID))
//   - entityType: The namespace/partition for this entity type (e.g., "users", "sessions")
//   - newEntity: Factory function that creates new empty entity instances
//   - opts: Optional configuration (codec, timeout)
//
// Example:
//
//	kvStore := server.GetKVStore(1)
//	userDAO, err := dao.NewTransactional(kvStore, "users", func() *User { return &User{} })
func NewTransactional[E Entity](
	store kvstore.KVStore,
	entityType string,
	newEntity func() E,
	opts ...Option,
) (TransactionalDAO[E], error) {
	// Create the base genericDAO
	baseDAO, err := New(store, entityType, newEntity, opts...)
	if err != nil {
		return nil, err
	}

	// Extract the concrete genericDAO
	gDAO, ok := baseDAO.(*genericDAO[E])
	if !ok {
		return nil, InvalidConfigError{
			Field:  "store",
			Reason: "unexpected DAO type returned from New",
		}
	}

	return &transactionalDAO[E]{
		genericDAO: gDAO,
	}, nil
}

// BeginTx starts a new transaction.
func (d *transactionalDAO[E]) BeginTx(_ context.Context) (DAOTransaction[E], error) {
	return newDAOTransaction(d), nil
}

// TransactionalDAOWithTxFunc is a helper type for running transactional operations.
type TransactionalDAOWithTxFunc[E Entity] func(tx DAOTransaction[E]) error

// WithTx executes a function within a transaction.
// If the function returns an error, the transaction is rolled back.
// If the function returns nil, the transaction is committed.
//
// Example:
//
//	err := dao.WithTx(ctx, func(tx DAOTransaction[*User]) error {
//	    tx.Save(user1)
//	    tx.Save(user2)
//	    return nil // commit
//	})
func WithTx[E Entity](ctx context.Context, dao TransactionalDAO[E], fn TransactionalDAOWithTxFunc[E]) error {
	tx, err := dao.BeginTx(ctx)
	if err != nil {
		return err
	}

	// Ensure rollback on panic or early return
	defer func() {
		if tx != nil && !tx.IsClosed() {
			_ = tx.Rollback()
		}
	}()

	// Execute the function
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}

	// Commit the transaction
	return tx.Commit(ctx)
}

// BatchSave saves multiple entities in a single transaction.
// This is a convenience method for atomic batch inserts/updates.
//
// Example:
//
//	users := []*User{user1, user2, user3}
//	err := dao.BatchSave(ctx, users)
func BatchSave[E Entity](ctx context.Context, dao TransactionalDAO[E], entities []E) error {
	if len(entities) == 0 {
		return nil
	}

	return WithTx(ctx, dao, func(tx DAOTransaction[E]) error {
		for _, entity := range entities {
			if err := tx.Save(entity); err != nil {
				return err
			}
		}
		return nil
	})
}

// BatchDelete deletes multiple entities in a single transaction.
// This is a convenience method for atomic batch deletions.
//
// Example:
//
//	users := []*User{user1, user2, user3}
//	err := dao.BatchDelete(ctx, users)
func BatchDelete[E Entity](ctx context.Context, dao TransactionalDAO[E], entities []E) error {
	if len(entities) == 0 {
		return nil
	}

	return WithTx(ctx, dao, func(tx DAOTransaction[E]) error {
		for _, entity := range entities {
			if err := tx.Delete(entity); err != nil {
				return err
			}
		}
		return nil
	})
}

// SaveWithOptimisticLock saves an entity only if its version matches the
// expected version.
//
// It does NOT implement optimistic concurrency control, whatever the name
// suggests: it delegates to SaveWithCondition, which evaluates the version with
// a read followed by a write. Two callers holding the same expectedVersion can
// both pass and both save. Use it to catch an obviously stale write, not to
// serialise contested ones — those need a guarded operation on the transaction
// plane (go-qrdb#59).
//
// E must implement Versioned; the constraint is Entity for compatibility, so a
// type that does not carry a version is refused at run time with
// ConditionFailedError rather than rejected by the compiler.
//
// Example:
//
//	// First read
//	user, _ := dao.Get(ctx, 123)
//	currentVersion := user.Version
//
//	// Modify
//	user.Name = "New Name"
//
//	// Save with version check
//	err := SaveWithOptimisticLock(ctx, dao, user, currentVersion)
//	if IsConditionFailed(err) {
//	    // Handle concurrent modification
//	}
func SaveWithOptimisticLock[E Entity](ctx context.Context, dao TransactionalDAO[E], entity E, expectedVersion int64) error {
	return WithTx(ctx, dao, func(tx DAOTransaction[E]) error {
		return tx.SaveWithCondition(entity, Condition{
			Target:   ConditionVersion,
			Operator: OpEqual,
			Value:    expectedVersion,
		})
	})
}

// VersionedEntity is a helper struct that can be embedded in entities
// to automatically track version information.
type VersionedEntity struct {
	Version int64 `json:"version"`
}

// GetVersion returns the entity's current version.
func (v *VersionedEntity) GetVersion() int64 {
	return v.Version
}

// SetVersion sets the entity's version.
func (v *VersionedEntity) SetVersion(version int64) {
	v.Version = version
}

// TimestampedEntity is a helper struct that can be embedded in entities
// to automatically track creation and modification times.
type TimestampedEntity struct {
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// GetCreatedAt returns the entity's creation time.
func (t *TimestampedEntity) GetCreatedAt() time.Time {
	return t.CreatedAt
}

// GetUpdatedAt returns the entity's last modification time.
func (t *TimestampedEntity) GetUpdatedAt() time.Time {
	return t.UpdatedAt
}

// SetCreatedAt sets the entity's creation time.
func (t *TimestampedEntity) SetCreatedAt(tm time.Time) {
	t.CreatedAt = tm
}

// SetUpdatedAt sets the entity's modification time.
func (t *TimestampedEntity) SetUpdatedAt(tm time.Time) {
	t.UpdatedAt = tm
}
