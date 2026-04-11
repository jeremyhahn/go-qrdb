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
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// Default unit of work timeout.
const defaultUnitOfWorkTimeout = 30 * time.Second

// unitOp represents a single queued key-value operation within a UnitOfWork.
type unitOp struct {
	key      string
	value    []byte // serialized entity value; nil for delete operations
	isDelete bool
}

// opSnapshot holds the previous state of a key for compensation during rollback.
type opSnapshot struct {
	key     string
	value   []byte
	existed bool
}

// UnitOfWork coordinates atomic operations across multiple DAOs.
// All registered operations are committed atomically or rolled back together.
// It operates at the raw key-value level, collecting serialized operations
// from multiple DAOs and applying them in a single batch.
//
// Example:
//
//	uow := dao.NewUnitOfWork(store)
//	dao.RegisterSave(uow, userDAO, user)
//	dao.RegisterSave(uow, orderDAO, order)
//	dao.RegisterDelete(uow, sessionDAO, expiredSession)
//	err := uow.Commit(ctx)
type UnitOfWork struct {
	store   kvstore.KVStore
	ops     []unitOp
	closed  atomic.Bool
	timeout time.Duration
}

// UnitOfWorkOption configures a UnitOfWork.
type UnitOfWorkOption func(*UnitOfWork)

// WithUnitOfWorkTimeout sets the timeout for the UnitOfWork commit operation.
func WithUnitOfWorkTimeout(d time.Duration) UnitOfWorkOption {
	return func(uow *UnitOfWork) {
		uow.timeout = d
	}
}

// NewUnitOfWork creates a new UnitOfWork backed by the given KVStore.
// The UnitOfWork collects operations from multiple DAOs and commits them
// atomically. If any operation fails during commit, already-applied
// operations are compensated (best-effort rollback).
func NewUnitOfWork(store kvstore.KVStore, opts ...UnitOfWorkOption) *UnitOfWork {
	uow := &UnitOfWork{
		store:   store,
		ops:     make([]unitOp, 0, 8),
		timeout: defaultUnitOfWorkTimeout,
	}
	for _, opt := range opts {
		opt(uow)
	}
	return uow
}

// RegisterSave queues a typed entity save operation within the UnitOfWork.
// The entity is serialized immediately and the resulting key-value pair is
// queued for batch commit. If the entity has no ID, one is auto-generated.
func RegisterSave[E Entity](uow *UnitOfWork, dao GenericDAO[E], entity E) error {
	if uow.closed.Load() {
		return UnitOfWorkClosedError{Op: "RegisterSave"}
	}

	gDAO, err := extractGenericDAO(dao)
	if err != nil {
		return err
	}

	// Auto-generate ID if not set
	if entity.EntityID() == 0 {
		entity.SetEntityID(gDAO.idGen.NextID(entity))
	}

	// Serialize the entity
	data, marshalErr := gDAO.codec.Marshal(entity)
	if marshalErr != nil {
		return SerializationFailedError{EntityType: gDAO.entityType, Cause: marshalErr}
	}

	key := gDAO.makeKey(entity.EntityID())
	uow.ops = append(uow.ops, unitOp{
		key:      key,
		value:    data,
		isDelete: false,
	})

	return nil
}

// RegisterDelete queues a typed entity delete operation within the UnitOfWork.
// The entity's key is computed immediately and queued for batch deletion.
func RegisterDelete[E Entity](uow *UnitOfWork, dao GenericDAO[E], entity E) error {
	if uow.closed.Load() {
		return UnitOfWorkClosedError{Op: "RegisterDelete"}
	}

	if entity.EntityID() == 0 {
		return InvalidEntityIDError{
			EntityType: dao.EntityType(),
			Reason:     "cannot delete entity with ID 0",
		}
	}

	gDAO, err := extractGenericDAO(dao)
	if err != nil {
		return err
	}

	key := gDAO.makeKey(entity.EntityID())
	uow.ops = append(uow.ops, unitOp{
		key:      key,
		isDelete: true,
	})

	return nil
}

// Commit atomically applies all queued operations to the underlying KVStore.
// Operations are applied in registration order. If any operation fails,
// a best-effort compensating rollback is attempted for already-applied
// operations: saves are compensated by restoring the previous value (or
// deleting if the key did not exist), and deletes are compensated by
// restoring the previously read value.
//
// After Commit (success or failure), the UnitOfWork is closed and cannot
// be reused.
func (uow *UnitOfWork) Commit(ctx context.Context) error {
	if uow.closed.Load() {
		return UnitOfWorkClosedError{Op: "Commit"}
	}
	uow.closed.Store(true)

	// Empty UnitOfWork is a no-op.
	if len(uow.ops) == 0 {
		return nil
	}

	// Apply timeout if the context has no deadline.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, uow.timeout)
		defer cancel()
	}

	// Collect snapshots of existing values for compensation on failure.
	snapshots := make([]opSnapshot, 0, len(uow.ops))

	for i, op := range uow.ops {
		// Snapshot the current state of this key before applying the operation.
		existing, getErr := uow.store.Get(ctx, op.key)
		if getErr != nil {
			// Key does not exist; record that for compensation.
			snapshots = append(snapshots, opSnapshot{key: op.key, existed: false})
		} else {
			snapshots = append(snapshots, opSnapshot{key: op.key, value: existing, existed: true})
		}

		// Apply the operation.
		var applyErr error
		if op.isDelete {
			applyErr = uow.store.Delete(ctx, op.key)
		} else {
			applyErr = uow.store.Put(ctx, op.key, op.value)
		}

		if applyErr != nil {
			// Compensate all previously applied operations.
			uow.compensate(ctx, snapshots[:i])
			return UnitOfWorkCommitError{
				OpIndex: i,
				Key:     op.key,
				Cause:   applyErr,
			}
		}
	}

	return nil
}

// Rollback discards all queued operations without applying them.
// After Rollback, the UnitOfWork is closed and cannot be reused.
// It is safe to call Rollback multiple times or after Commit.
func (uow *UnitOfWork) Rollback() {
	uow.closed.Store(true)
	uow.ops = nil
}

// Len returns the number of queued operations.
func (uow *UnitOfWork) Len() int {
	return len(uow.ops)
}

// IsClosed returns true if the UnitOfWork has been committed or rolled back.
func (uow *UnitOfWork) IsClosed() bool {
	return uow.closed.Load()
}

// compensate performs best-effort rollback for already-applied operations.
// It processes snapshots in reverse order, restoring previous values or
// deleting keys that did not previously exist.
func (uow *UnitOfWork) compensate(ctx context.Context, snapshots []opSnapshot) {
	for i := len(snapshots) - 1; i >= 0; i-- {
		snap := snapshots[i]
		if snap.existed {
			_ = uow.store.Put(ctx, snap.key, snap.value)
		} else {
			_ = uow.store.Delete(ctx, snap.key)
		}
	}
}

// extractGenericDAO extracts the underlying *genericDAO from a GenericDAO interface.
// It handles direct genericDAO and transactionalDAO wrapper types.
func extractGenericDAO[E Entity](dao GenericDAO[E]) (*genericDAO[E], error) {
	switch d := dao.(type) {
	case *genericDAO[E]:
		return d, nil
	case *transactionalDAO[E]:
		return d.genericDAO, nil
	default:
		return nil, InvalidConfigError{
			Field:  "dao",
			Reason: fmt.Sprintf("unsupported DAO type %T for UnitOfWork", dao),
		}
	}
}
