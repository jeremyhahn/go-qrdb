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
	"reflect"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// DAOTransaction provides entity-aware transaction operations.
// It queues operations for atomic commit and provides read-your-own-writes
// semantics within the transaction boundary.
//
// Usage:
//
//	tx, err := dao.BeginTx(ctx)
//	if err != nil { return err }
//	defer tx.Rollback() // Safe to call even after commit
//
//	// Queue operations
//	tx.Save(entity1)
//	tx.Save(entity2)
//	tx.Delete(entity3)
//
//	// Commit atomically
//	return tx.Commit(ctx)
type DAOTransaction[E Entity] interface {
	// Save queues an entity for insert/update.
	// If EntityID() returns 0, a new ID is generated.
	// The entity is visible to subsequent Get calls within this transaction.
	Save(entity E) error

	// Delete queues an entity for deletion.
	// The entity will no longer be visible to Get calls within this transaction.
	Delete(entity E) error

	// Get retrieves an entity within transaction snapshot.
	// Returns entities from the write cache first (read-your-own-writes),
	// then falls back to storage.
	Get(id uint64) (E, error)

	// SaveWithCondition saves only if condition is met.
	// Useful for optimistic locking patterns.
	SaveWithCondition(entity E, cond Condition) error

	// Commit atomically applies all queued operations.
	// After commit, the transaction is closed and cannot be reused.
	//
	// The ops go to the store as ONE kvstore.MultiGuardedBatchKVStore
	// PutAllIfAllUnchanged batch: all are applied or none. Refusals, each
	// leaving the store unchanged: a failed condition (ConditionFailedError),
	// a record changed after its condition was evaluated
	// (TransactionCommitError wrapping kvstore.ErrNotApplied), an ID
	// collision (*IDCollisionError) and a store without the batch interface
	// (*AtomicBatchUnsupportedError; there is no sequential fallback).
	Commit(ctx context.Context) error

	// Rollback discards all queued operations.
	// Safe to call multiple times and after commit (becomes a no-op).
	Rollback() error

	// IsClosed returns true if the transaction has been committed or rolled back.
	IsClosed() bool
}

// Condition specifies a precondition for conditional operations.
// Used with SaveWithCondition for optimistic locking patterns.
type Condition struct {
	Target   ConditionTarget
	Operator ConditionOp
	Value    interface{}
}

// ConditionTarget specifies what property to check in a condition.
type ConditionTarget int

const (
	// ConditionVersion checks the entity's version field.
	ConditionVersion ConditionTarget = iota
	// ConditionCreate checks the entity's create timestamp.
	ConditionCreate
	// ConditionMod checks the entity's modification timestamp.
	ConditionMod
	// ConditionValue checks the entity's serialized value.
	ConditionValue
)

// ConditionOp specifies the comparison operator for a condition.
type ConditionOp int

const (
	// OpEqual checks for equality (==).
	OpEqual ConditionOp = iota
	// OpNotEqual checks for inequality (!=).
	OpNotEqual
	// OpGreater checks if actual > expected.
	OpGreater
	// OpLess checks if actual < expected.
	OpLess
	// OpGreaterEqual checks if actual >= expected.
	OpGreaterEqual
	// OpLessEqual checks if actual <= expected.
	OpLessEqual
)

// String returns a human-readable representation of the condition target.
func (t ConditionTarget) String() string {
	switch t {
	case ConditionVersion:
		return "version"
	case ConditionCreate:
		return "create"
	case ConditionMod:
		return "mod"
	case ConditionValue:
		return "value"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// String returns a human-readable representation of the condition operator.
func (o ConditionOp) String() string {
	switch o {
	case OpEqual:
		return "=="
	case OpNotEqual:
		return "!="
	case OpGreater:
		return ">"
	case OpLess:
		return "<"
	case OpGreaterEqual:
		return ">="
	case OpLessEqual:
		return "<="
	default:
		return fmt.Sprintf("unknown(%d)", o)
	}
}

// String returns a human-readable representation of the condition.
func (c Condition) String() string {
	return fmt.Sprintf("%s %s %v", c.Target.String(), c.Operator.String(), c.Value)
}

// Validate checks if the condition is well-formed.
func (c Condition) Validate() error {
	// Validate target
	switch c.Target {
	case ConditionVersion, ConditionCreate, ConditionMod:
		// These require int64 values
		switch c.Value.(type) {
		case int, int32, int64, uint, uint32, uint64:
			// Valid numeric types
		default:
			return InvalidConditionError{
				Reason: fmt.Sprintf("target %s requires numeric value, got %T", c.Target.String(), c.Value),
			}
		}
	case ConditionValue:
		// Value target accepts string or []byte
		switch c.Value.(type) {
		case string, []byte, nil:
			// Valid
		default:
			return InvalidConditionError{
				Reason: fmt.Sprintf("target value requires string or []byte, got %T", c.Value),
			}
		}
	default:
		return InvalidConditionError{
			Reason: fmt.Sprintf("unknown condition target: %d", c.Target),
		}
	}

	// Validate operator
	switch c.Operator {
	case OpEqual, OpNotEqual, OpGreater, OpLess, OpGreaterEqual, OpLessEqual:
		// Valid
	default:
		return InvalidConditionError{
			Reason: fmt.Sprintf("unknown condition operator: %d", c.Operator),
		}
	}

	return nil
}

// txnOpType specifies the type of transaction operation.
type txnOpType int

const (
	txnOpSave txnOpType = iota
	txnOpDelete
	txnOpSaveWithCondition
)

// txnOp represents a queued operation in a transaction.
type txnOp[E Entity] struct {
	opType    txnOpType
	entity    E
	condition *Condition
	// deterministic is non-nil for a save whose ID came from a
	// DeterministicIDGenerator at queue time; Commit guards that write
	// exactly as UnitOfWork does (see deterministicSaveInfo).
	deterministic *deterministicSaveInfo
}

// daoTransaction provides entity-aware transaction implementation.
type daoTransaction[E Entity] struct {
	dao       *transactionalDAO[E]
	ops       []txnOp[E]
	cache     map[uint64]cachedEntity[E] // Read-your-own-writes cache
	closed    atomic.Bool
	startTime time.Time
}

// cachedEntity holds an entity and its state in the transaction cache.
type cachedEntity[E Entity] struct {
	entity  E
	deleted bool
}

// newDAOTransaction creates a new transaction for the given DAO.
func newDAOTransaction[E Entity](dao *transactionalDAO[E]) *daoTransaction[E] {
	return &daoTransaction[E]{
		dao:       dao,
		ops:       make([]txnOp[E], 0, 8),
		cache:     make(map[uint64]cachedEntity[E]),
		startTime: time.Now(),
	}
}

// isNil checks if an entity is nil using reflection.
// This is needed because Go generics don't allow direct nil comparison with type parameters.
func isNil[E any](entity E) bool {
	v := reflect.ValueOf(entity)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	default:
		return false
	}
}

// Save queues an entity for insert/update.
func (tx *daoTransaction[E]) Save(entity E) error {
	if tx.closed.Load() {
		return TransactionClosedError{Op: "Save"}
	}

	if isNil(entity) {
		return InvalidEntityError{Reason: "entity cannot be nil"}
	}

	// Assign the ID from the DAO's own generator, as genericDAO.Save does.
	det := assignSaveID(tx.dao.genericDAO, entity)

	// Add to write cache
	tx.cache[entity.EntityID()] = cachedEntity[E]{
		entity:  entity,
		deleted: false,
	}

	// Queue the operation
	tx.ops = append(tx.ops, txnOp[E]{
		opType:        txnOpSave,
		entity:        entity,
		deterministic: det,
	})

	return nil
}

// Delete queues an entity for deletion.
func (tx *daoTransaction[E]) Delete(entity E) error {
	if tx.closed.Load() {
		return TransactionClosedError{Op: "Delete"}
	}

	if isNil(entity) {
		return InvalidEntityError{Reason: "entity cannot be nil"}
	}

	id := entity.EntityID()
	if id == 0 {
		return InvalidEntityIDError{
			EntityType: tx.dao.entityType,
			Reason:     "cannot delete entity with ID 0",
		}
	}

	// Mark as deleted in cache
	tx.cache[id] = cachedEntity[E]{
		entity:  entity,
		deleted: true,
	}

	// Queue the operation
	tx.ops = append(tx.ops, txnOp[E]{
		opType: txnOpDelete,
		entity: entity,
	})

	return nil
}

// Get retrieves an entity within transaction snapshot.
func (tx *daoTransaction[E]) Get(id uint64) (E, error) {
	var zero E

	if tx.closed.Load() {
		return zero, TransactionClosedError{Op: "Get"}
	}

	// Check write cache first (read-your-own-writes)
	if cached, ok := tx.cache[id]; ok {
		if cached.deleted {
			return zero, NotFoundError{EntityType: tx.dao.entityType, ID: id}
		}
		return cached.entity, nil
	}

	// Fall back to underlying storage
	ctx, cancel := context.WithTimeout(context.Background(), tx.dao.timeout)
	defer cancel()

	return tx.dao.Get(ctx, id)
}

// SaveWithCondition saves only if condition is met.
func (tx *daoTransaction[E]) SaveWithCondition(entity E, cond Condition) error {
	if tx.closed.Load() {
		return TransactionClosedError{Op: "SaveWithCondition"}
	}

	if isNil(entity) {
		return InvalidEntityError{Reason: "entity cannot be nil"}
	}

	// Validate condition
	if err := cond.Validate(); err != nil {
		return err
	}

	// Assign the ID from the DAO's own generator, as genericDAO.Save does.
	det := assignSaveID(tx.dao.genericDAO, entity)

	// Add to write cache (optimistically)
	tx.cache[entity.EntityID()] = cachedEntity[E]{
		entity:  entity,
		deleted: false,
	}

	// Queue the operation with condition
	tx.ops = append(tx.ops, txnOp[E]{
		opType:        txnOpSaveWithCondition,
		entity:        entity,
		condition:     &cond,
		deterministic: det,
	})

	return nil
}

// Commit applies all queued operations as ONE guarded batch
// (kvstore.MultiGuardedBatchKVStore.PutAllIfAllUnchanged): every write
// lands, or none does.
//
// A save is a put; a save whose ID came from a DeterministicIDGenerator
// carries the create/update/collision guard UnitOfWork uses. A delete is a
// delete. A SaveWithCondition is evaluated against the record stored at
// commit time, before anything is written, and its put is guarded on the
// exact bytes (or absence) it was evaluated against, so a concurrent change
// between evaluation and the batch refuses the whole batch.
//
// Refusals, none of which write anything: ConditionFailedError (a condition
// did not hold), *AtomicBatchUnsupportedError (the store has no atomic
// multi-guard batch; there is no sequential fallback), and
// TransactionCommitError wrapping kvstore.ErrNotApplied (a guard lost to a
// concurrent writer), *IDCollisionError, or the store's error.
func (tx *daoTransaction[E]) Commit(ctx context.Context) error {
	if tx.closed.Load() {
		return TransactionClosedError{Op: "Commit"}
	}

	// Mark as closed
	tx.closed.Store(true)

	// No operations to commit
	if len(tx.ops) == 0 {
		return nil
	}

	multi, ok := tx.dao.store.(kvstore.MultiGuardedBatchKVStore)
	if !ok {
		return &AtomicBatchUnsupportedError{}
	}

	ctx, cancel := tx.dao.ensureTimeout(ctx)
	defer cancel()

	ops := make([]unitOp, 0, len(tx.ops))
	var guards []kvstore.Guard
	for _, op := range tx.ops {
		key := tx.dao.makeKey(op.entity.EntityID())
		if op.opType == txnOpDelete {
			ops = append(ops, unitOp{key: key, isDelete: true})
			continue
		}

		if op.opType == txnOpSaveWithCondition {
			existing, getErr := multi.Get(ctx, key)
			if getErr != nil && !isNotFoundError(getErr) {
				return TransactionCommitError{Cause: getErr}
			}
			if condErr := tx.evaluateCondition(existing, op.condition); condErr != nil {
				return condErr
			}
			guards = append(guards, kvstore.Guard{Key: key, Value: existing})
		}

		data, marshalErr := tx.dao.codec.Marshal(op.entity)
		if marshalErr != nil {
			return TransactionCommitError{Cause: SerializationFailedError{EntityType: tx.dao.entityType, Cause: marshalErr}}
		}
		ops = append(ops, unitOp{key: key, value: data, deterministic: op.deterministic})
	}

	if err := commitGuardedOps(ctx, multi, ops, guards); err != nil {
		var unsupported *AtomicBatchUnsupportedError
		if errors.As(err, &unsupported) {
			return err
		}
		return TransactionCommitError{Cause: err}
	}
	return nil
}

// evaluateCondition checks cond against existingData, the record's stored
// bytes (nil when absent).
func (tx *daoTransaction[E]) evaluateCondition(existingData []byte, cond *Condition) error {
	if existingData == nil {
		// Entity doesn't exist - a new entity has version and timestamps 0
		// and an empty value.
		switch cond.Target {
		case ConditionVersion, ConditionCreate, ConditionMod:
			return tx.evaluateNumericCondition(0, cond)
		case ConditionValue:
			return tx.evaluateValueCondition(nil, cond)
		}
		return ConditionFailedError{
			Condition: *cond,
			Reason:    "entity does not exist",
		}
	}

	existing := tx.dao.newEntity()
	if err := tx.dao.codec.Unmarshal(existingData, &existing); err != nil {
		return DeserializationFailedError{EntityType: tx.dao.entityType, Cause: err}
	}
	defer tx.dao.discard(existing)

	// Entity exists - evaluate condition based on target
	switch cond.Target {
	case ConditionVersion:
		// An entity that carries no version has none to compare. Substituting
		// a constant here made the guard decide on a value the stored record
		// never held: OpEqual 1 passed on the first update and on the
		// ten-thousandth, and no other expectation could ever pass. A caller
		// following data-access.md's read-modify-write rule held a guard that
		// guarded nothing, which is the lost update the condition exists to
		// prevent. Refuse instead, exactly as ConditionCreate and ConditionMod
		// below refuse an entity that does not implement Timestamped
		// (go-qrdb#59).
		versioned, ok := any(existing).(Versioned)
		if !ok {
			return ConditionFailedError{
				Condition: *cond,
				Reason:    "entity does not implement Versioned interface",
			}
		}
		return tx.evaluateNumericCondition(versioned.GetVersion(), cond)

	case ConditionCreate:
		if timestamped, ok := any(existing).(Timestamped); ok {
			return tx.evaluateNumericCondition(timestamped.GetCreatedAt().UnixNano(), cond)
		}
		return ConditionFailedError{
			Condition: *cond,
			Reason:    "entity does not implement Timestamped interface",
		}

	case ConditionMod:
		if timestamped, ok := any(existing).(Timestamped); ok {
			return tx.evaluateNumericCondition(timestamped.GetUpdatedAt().UnixNano(), cond)
		}
		return ConditionFailedError{
			Condition: *cond,
			Reason:    "entity does not implement Timestamped interface",
		}

	case ConditionValue:
		// Serialize existing entity and compare
		data, err := tx.dao.codec.Marshal(existing)
		if err != nil {
			return SerializationFailedError{EntityType: tx.dao.entityType, Cause: err}
		}
		return tx.evaluateValueCondition(data, cond)
	}

	return ConditionFailedError{
		Condition: *cond,
		Reason:    "unknown condition target",
	}
}

// evaluateNumericCondition evaluates a numeric comparison.
func (tx *daoTransaction[E]) evaluateNumericCondition(actual int64, cond *Condition) error {
	expected := toInt64(cond.Value)

	var result bool
	switch cond.Operator {
	case OpEqual:
		result = actual == expected
	case OpNotEqual:
		result = actual != expected
	case OpGreater:
		result = actual > expected
	case OpLess:
		result = actual < expected
	case OpGreaterEqual:
		result = actual >= expected
	case OpLessEqual:
		result = actual <= expected
	}

	if !result {
		return ConditionFailedError{
			Condition: *cond,
			Reason:    fmt.Sprintf("actual value %d does not satisfy condition", actual),
		}
	}

	return nil
}

// evaluateValueCondition evaluates a value comparison.
func (tx *daoTransaction[E]) evaluateValueCondition(actual []byte, cond *Condition) error {
	var expected []byte
	switch v := cond.Value.(type) {
	case []byte:
		expected = v
	case string:
		expected = []byte(v)
	case nil:
		expected = nil
	}

	var result bool
	switch cond.Operator {
	case OpEqual:
		result = bytesEqual(actual, expected)
	case OpNotEqual:
		result = !bytesEqual(actual, expected)
	case OpGreater:
		result = bytesCompare(actual, expected) > 0
	case OpLess:
		result = bytesCompare(actual, expected) < 0
	case OpGreaterEqual:
		result = bytesCompare(actual, expected) >= 0
	case OpLessEqual:
		result = bytesCompare(actual, expected) <= 0
	}

	if !result {
		return ConditionFailedError{
			Condition: *cond,
			Reason:    "value does not satisfy condition",
		}
	}

	return nil
}

// Rollback discards all queued operations.
func (tx *daoTransaction[E]) Rollback() error {
	if tx.closed.Load() {
		// Already closed, no-op
		return nil
	}

	tx.closed.Store(true)
	tx.ops = nil
	tx.cache = nil

	return nil
}

// IsClosed returns true if the transaction has been committed or rolled back.
func (tx *daoTransaction[E]) IsClosed() bool {
	return tx.closed.Load()
}

// Versioned is an optional interface for entities that support versioning.
type Versioned interface {
	GetVersion() int64
	SetVersion(version int64)
}

// Timestamped is an optional interface for entities that track creation/modification times.
type Timestamped interface {
	GetCreatedAt() time.Time
	GetUpdatedAt() time.Time
}

// toInt64 converts various numeric types to int64.
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case int64:
		return n
	case uint:
		return int64(n) //nolint:gosec // G115: conversion checked at validation
	case uint32:
		return int64(n)
	case uint64:
		return int64(n) //nolint:gosec // G115: conversion checked at validation
	default:
		return 0
	}
}

// bytesEqual compares two byte slices for equality.
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// bytesCompare compares two byte slices lexicographically.
func bytesCompare(a, b []byte) int {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}
