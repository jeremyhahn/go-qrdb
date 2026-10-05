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
	"errors"
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

// UnitOfWork coordinates operations across multiple DAOs, collecting
// serialized operations from multiple DAOs at the raw key-value level and
// applying them in a single, genuinely atomic batch at Commit.
//
// Commit requires the store to implement kvstore.MultiGuardedBatchKVStore and
// refuses with *AtomicBatchUnsupportedError otherwise (go-qrdb#589's
// coordinator ruling): there is no sequential-apply-and-compensate fallback.
// An earlier version of this type had one, applying operations one at a time
// and best-effort rolling back already-applied operations on failure; that
// path was NOT atomic against a concurrent reader -- a mid-batch failure
// could leave a move half-applied for exactly as long as compensation takes
// to run, which is precisely the non-atomic window a unit of work exists to
// close. A store that genuinely cannot do atomic batches must say so, not
// emulate one.
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

// NewUnitOfWork creates a new UnitOfWork backed by the given KVStore. The
// UnitOfWork collects operations from multiple DAOs and commits them in one
// atomic batch; see Commit's doc for what store supports that.
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

// Commit applies all queued operations to the underlying KVStore as ONE
// atomic batch, in registration order within that batch.
//
// The store MUST implement kvstore.MultiGuardedBatchKVStore; Commit refuses
// with *AtomicBatchUnsupportedError otherwise -- there is no sequential,
// non-atomic fallback (go-qrdb#589's coordinator ruling; see the type doc).
// Every operation is applied as one guarded batch write
// (PutAllIfAllUnchanged with no guards -- every operation is unconditional,
// but the whole batch is still ONE atomic step): no reader can observe the
// batch partially applied, which matters for a move/rename that deletes one
// key and puts another and must never be visible with the delete done and
// the put not yet landed, or the reverse.
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

	multi, ok := uow.store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		return &AtomicBatchUnsupportedError{}
	}

	// Apply timeout if the context has no deadline.
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, uow.timeout)
		defer cancel()
	}

	return uow.commitAtomic(ctx, multi)
}

// commitAtomic applies every queued operation as one guarded batch write.
// See Commit's doc for why an empty guard list still means atomic, not
// unconditional-and-unordered: PutAllIfAllUnchanged proposes the whole batch
// as a single state-machine transaction regardless of how many guards it
// carries.
func (uow *UnitOfWork) commitAtomic(ctx context.Context, store kvstore.MultiGuardedBatchKVStore) error {
	writes := make([]kvstore.KV, len(uow.ops))
	for i, op := range uow.ops {
		writes[i] = kvstore.KV{Key: op.key, Value: op.value, Delete: op.isDelete}
	}

	applied, err := store.PutAllIfAllUnchanged(ctx, nil, writes)
	if err != nil {
		if errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
			return &AtomicBatchUnsupportedError{Err: err}
		}
		return err
	}
	if !applied {
		// Reachable only if a future MultiGuardedBatchKVStore implementation
		// can refuse a batch carrying zero guards for some reason other than
		// an error; today none does, since there is nothing for zero guards
		// to fail. Reported honestly rather than assumed impossible.
		return kvstore.ErrNotApplied
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
