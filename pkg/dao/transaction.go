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

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"
	"time"
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
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
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

	// Auto-generate ID if not set
	if entity.EntityID() == 0 {
		entity.SetEntityID(uint64(time.Now().UnixNano())) //nolint:gosec // G115: UnixNano always positive after 1970
	}

	// Add to write cache
	tx.cache[entity.EntityID()] = cachedEntity[E]{
		entity:  entity,
		deleted: false,
	}

	// Queue the operation
	tx.ops = append(tx.ops, txnOp[E]{
		opType: txnOpSave,
		entity: entity,
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

	// Auto-generate ID if not set
	if entity.EntityID() == 0 {
		entity.SetEntityID(uint64(time.Now().UnixNano())) //nolint:gosec // G115: UnixNano always positive after 1970
	}

	// Add to write cache (optimistically)
	tx.cache[entity.EntityID()] = cachedEntity[E]{
		entity:  entity,
		deleted: false,
	}

	// Queue the operation with condition
	tx.ops = append(tx.ops, txnOp[E]{
		opType:    txnOpSaveWithCondition,
		entity:    entity,
		condition: &cond,
	})

	return nil
}

// Commit atomically applies all queued operations.
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

	// Execute all operations
	for _, op := range tx.ops {
		switch op.opType {
		case txnOpSave:
			if err := tx.dao.Save(ctx, op.entity); err != nil {
				return TransactionCommitError{Cause: err}
			}

		case txnOpDelete:
			if err := tx.dao.Delete(ctx, op.entity); err != nil {
				return TransactionCommitError{Cause: err}
			}

		case txnOpSaveWithCondition:
			// Evaluate condition before saving
			if err := tx.evaluateCondition(ctx, op.entity, op.condition); err != nil {
				return err
			}
			if err := tx.dao.Save(ctx, op.entity); err != nil {
				return TransactionCommitError{Cause: err}
			}
		}
	}

	return nil
}

// evaluateCondition checks if the condition is satisfied.
func (tx *daoTransaction[E]) evaluateCondition(ctx context.Context, entity E, cond *Condition) error {
	id := entity.EntityID()

	// Try to get existing entity
	existing, err := tx.dao.Get(ctx, id)
	existsInStorage := err == nil

	// For version checks on new entities
	if !existsInStorage && cond.Target == ConditionVersion {
		// New entity should have version 0
		return tx.evaluateNumericCondition(0, cond)
	}

	if !existsInStorage {
		// Entity doesn't exist - condition may still be valid for create checks
		if cond.Target == ConditionCreate || cond.Target == ConditionMod {
			return tx.evaluateNumericCondition(0, cond)
		}
		if cond.Target == ConditionValue {
			// Check against nil/empty value
			return tx.evaluateValueCondition(nil, cond)
		}
		return ConditionFailedError{
			Condition: *cond,
			Reason:    "entity does not exist",
		}
	}

	// Entity exists - evaluate condition based on target
	switch cond.Target {
	case ConditionVersion:
		// For entities that implement Versioned interface
		if versioned, ok := any(existing).(Versioned); ok {
			return tx.evaluateNumericCondition(versioned.GetVersion(), cond)
		}
		// If not versioned, treat as version 1 for existing entities
		return tx.evaluateNumericCondition(1, cond)

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
