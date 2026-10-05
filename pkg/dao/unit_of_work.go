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
	// deterministic is non-nil for a save whose ID came from a
	// DeterministicIDGenerator (EntityID() == 0 at RegisterSave time). It
	// carries what Commit needs to decide -- against the record actually at
	// this key when the batch is built, not against whatever RegisterSave
	// observed -- whether this write is a create (the key is absent), an
	// update of the SAME logical entity (the existing record's natural key
	// matches), or a genuine collision with a DIFFERENT one. See
	// deterministicSaveInfo.
	deterministic *deterministicSaveInfo
}

// deterministicSaveInfo captures what Commit needs to resolve one
// deterministic-ID save's guard against the store, and to name the
// entity/ID in a refusal. Captured as a closure rather than a field holding
// the entity's own type, because UnitOfWork queues operations from many
// different GenericDAO[E] instantiations into one untyped slice.
type deterministicSaveInfo struct {
	entityType string
	id         uint64

	// sameNaturalKey decodes existingData with the owning DAO's codec and
	// entity type and reports whether it carries the SAME natural-key value
	// the entity being saved carries -- the coordinator ruling (go-qrdb#589
	// review): the SAME logical entity is an update, a DIFFERENT one is a
	// collision. same is false, with a nil error, when the entity being
	// saved has no natural-key value of its own to compare (nothing to
	// verify sameness against, so the occupied key is treated as a
	// collision candidate). A non-nil error means existingData itself could
	// not be decoded at all -- a real failure, not a mismatch -- and is
	// propagated rather than silently treated as either outcome.
	sameNaturalKey func(existingData []byte) (same bool, err error)
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
//
// When the DAO's configured IDGenerator is a DeterministicIDGenerator
// (FieldHashGenerator, CryptoFieldHashGenerator, DynamicFieldHashGenerator,
// CryptoDynamicFieldHashGenerator) and entity has no ID, the derived ID can
// already name a record -- a hash-derived ID is drawn from a 64-bit space
// shared by every natural-key value the generator ever hashes
// (go-qrdb#589). RegisterSave captures what Commit needs to apply the
// coordinator ruling for this save at commit time: a record at that ID
// carrying the SAME natural-key value is the SAME logical entity (an
// update), a DIFFERENT one is a genuine collision (refused with
// *IDCollisionError, nothing in the batch applies). See Commit.
func RegisterSave[E Entity](uow *UnitOfWork, dao GenericDAO[E], entity E) error {
	if uow.closed.Load() {
		return UnitOfWorkClosedError{Op: "RegisterSave"}
	}

	gDAO, err := extractGenericDAO(dao)
	if err != nil {
		return err
	}

	var det *deterministicSaveInfo
	if entity.EntityID() == 0 {
		if gen, deterministic := gDAO.idGen.(DeterministicIDGenerator); deterministic {
			entity.SetEntityID(gen.NextID(entity))
			expectedValue, hasNaturalKey := extractNaturalKeyValue(entity, gen.FieldName())
			det = &deterministicSaveInfo{
				entityType: gDAO.entityType,
				id:         entity.EntityID(),
				sameNaturalKey: func(existingData []byte) (bool, error) {
					if !hasNaturalKey {
						return false, nil
					}
					existing := gDAO.newEntity()
					if unmarshalErr := gDAO.codec.Unmarshal(existingData, &existing); unmarshalErr != nil {
						return false, DeserializationFailedError{EntityType: gDAO.entityType, Cause: unmarshalErr}
					}
					return VerifyNaturalKey(existing, gDAO.entityType, gen.FieldName(), expectedValue) == nil, nil
				},
			}
		} else {
			entity.SetEntityID(gDAO.idGen.NextID(entity))
		}
	}

	// Serialize the entity
	data, marshalErr := gDAO.codec.Marshal(entity)
	if marshalErr != nil {
		return SerializationFailedError{EntityType: gDAO.entityType, Cause: marshalErr}
	}

	key := gDAO.makeKey(entity.EntityID())
	uow.ops = append(uow.ops, unitOp{
		key:           key,
		value:         data,
		isDelete:      false,
		deterministic: det,
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
// (PutAllIfAllUnchanged): no reader can observe the batch partially
// applied, which matters for a move/rename that deletes one key and puts
// another and must never be visible with the delete done and the put not
// yet landed, or the reverse.
//
// A RegisterSave whose ID came from a DeterministicIDGenerator carries its
// own guard into this batch (go-qrdb#589 review, the coordinator ruling on
// re-saving the same natural key -- see RegisterSave): Commit reads the
// record actually at that key when the batch is built and decides whether
// it is absent (guarded on absence: a create), the SAME logical entity
// (guarded on the exact bytes read: an update via CompareAndSwap), or a
// DIFFERENT one (guarded on absence too, so the server's own evaluation --
// not this read -- is what refuses it). Every OTHER queued operation
// (a non-deterministic save, a delete) carries no guard and is applied
// unconditionally within the same atomic proposal. If the batch is refused
// because of a confirmed collision, Commit returns *IDCollisionError and
// NOTHING in the batch applies -- not even the operations that carried no
// guard of their own.
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
// See Commit's doc for how a deterministic-ID save's own guard is decided,
// and for why an empty guard list still means atomic, not
// unconditional-and-unordered: PutAllIfAllUnchanged proposes the whole batch
// as a single state-machine transaction regardless of how many guards it
// carries.
func (uow *UnitOfWork) commitAtomic(ctx context.Context, store kvstore.MultiGuardedBatchKVStore) error {
	writes := make([]kvstore.KV, len(uow.ops))
	var guards []kvstore.Guard

	for i, op := range uow.ops {
		writes[i] = kvstore.KV{Key: op.key, Value: op.value, Delete: op.isDelete}
		if op.deterministic == nil {
			continue
		}

		existingData, getErr := store.Get(ctx, op.key)
		if getErr != nil {
			return getErr
		}
		if existingData == nil {
			// Still absent: a create, guarded on absence so a concurrent
			// create landing between this read and the commit is caught by
			// the server's own evaluation, not assumed away.
			guards = append(guards, kvstore.Guard{Key: op.key, Value: nil})
			continue
		}

		same, verifyErr := op.deterministic.sameNaturalKey(existingData)
		if verifyErr != nil {
			return verifyErr
		}
		if same {
			// The SAME logical entity: an update, guarded on the exact
			// bytes just read so the write stays atomic against a
			// concurrent writer instead of blindly overwriting whatever is
			// there by the time the batch commits.
			guards = append(guards, kvstore.Guard{Key: op.key, Value: existingData})
			continue
		}

		// A DIFFERENT entity occupies the key: a collision candidate.
		// Guarded on absence, which the server refuses unless the
		// collision has since resolved itself (the other record was
		// deleted before this batch commits) -- the guard the server
		// evaluates, not this read, is the one that actually decides.
		guards = append(guards, kvstore.Guard{Key: op.key, Value: nil})
	}

	applied, err := store.PutAllIfAllUnchanged(ctx, guards, writes)
	if err != nil {
		if errors.Is(err, kvstore.ErrAtomicWriteUnsupported) {
			return &AtomicBatchUnsupportedError{Err: err}
		}
		return err
	}
	if !applied {
		if collisionErr := uow.detectDeterministicCollision(ctx, store); collisionErr != nil {
			return collisionErr
		}
		// Refused by contention alone (a concurrent writer winning the race
		// on what would otherwise have been a same-key update, or a create
		// whose absence guard lost to a concurrent create) -- not a
		// confirmed collision. Reported honestly as outcome-unknown-refused
		// rather than guessed at.
		return kvstore.ErrNotApplied
	}
	return nil
}

// detectDeterministicCollision runs after a refused batch to decide whether
// the refusal was caused by a CONFIRMED collision on one of the batch's
// deterministic-ID saves. It is best-effort, exactly as
// kvstore.CompareAndSwapWithCurrent's follow-up read is: the batch's own
// refusal is already final, and this only decides which typed error to
// report for it. Returns the first confirmed collision found, or nil when
// none of the deterministic saves' keys currently hold a different entity
// -- in which case the refusal was contention alone, not a collision.
func (uow *UnitOfWork) detectDeterministicCollision(ctx context.Context, store kvstore.KVStore) error {
	for _, op := range uow.ops {
		if op.deterministic == nil {
			continue
		}
		data, err := store.Get(ctx, op.key)
		if err != nil || data == nil {
			continue
		}
		same, verifyErr := op.deterministic.sameNaturalKey(data)
		if verifyErr != nil || !same {
			return &IDCollisionError{EntityType: op.deterministic.entityType, ID: op.deterministic.id}
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
