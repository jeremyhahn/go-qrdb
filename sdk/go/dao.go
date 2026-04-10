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

package sdk

import (
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// ---------------------------------------------------------------------------
// KVStore re-exports
// ---------------------------------------------------------------------------

// KVStore is the low-level key-value store interface used as a backend
// for DAOs and other storage abstractions.
type KVStore = kvstore.KVStore

// ---------------------------------------------------------------------------
// DAO re-exports
// ---------------------------------------------------------------------------

// Entity is the interface that all DAO-managed entities must implement.
type Entity = dao.Entity

// GenericDAO provides typed CRUD operations over a KVStore for a given entity type.
type GenericDAO[E dao.Entity] = dao.GenericDAO[E]

// DAOOption configures a GenericDAO at construction time.
type DAOOption = dao.Option

// PageQuery specifies pagination parameters for DAO list operations.
type PageQuery = dao.PageQuery

// PageResult holds a page of entities returned by a DAO list operation.
type PageResult[E any] = dao.PageResult[E]

// PagerFunc is a callback function invoked for each page during pagination.
type PagerFunc[E any] = dao.PagerFunc[E]

// QueryBuilder provides a fluent query interface for DAO entities.
type QueryBuilder[E dao.Entity] = dao.QueryBuilder[E]

// ---------------------------------------------------------------------------
// DAO Condition types
// ---------------------------------------------------------------------------

// Condition represents a conditional check for DAO write operations.
type Condition = dao.Condition

// ConditionTarget specifies what property to check in a condition.
type ConditionTarget = dao.ConditionTarget

// ConditionOp specifies the comparison operator for a condition.
type ConditionOp = dao.ConditionOp

// Condition target constants.
const (
	ConditionVersion = dao.ConditionVersion
	ConditionCreate  = dao.ConditionCreate
	ConditionMod     = dao.ConditionMod
	ConditionValue   = dao.ConditionValue
)

// Condition operator constants.
const (
	OpEqual        = dao.OpEqual
	OpNotEqual     = dao.OpNotEqual
	OpGreater      = dao.OpGreater
	OpLess         = dao.OpLess
	OpGreaterEqual = dao.OpGreaterEqual
	OpLessEqual    = dao.OpLessEqual
)

// ErrDAOStopIteration is returned to signal early termination of page iteration.
var ErrDAOStopIteration = dao.ErrStopIteration

// NewDAO creates a new GenericDAO backed by the given KVStore.
func NewDAO[E dao.Entity](store KVStore, entityType string, newEntity func() E, opts ...DAOOption) (GenericDAO[E], error) {
	return dao.New(store, entityType, newEntity, opts...)
}

// IsDAONotFound returns true if the error indicates an entity was not found.
func IsDAONotFound(err error) bool {
	return dao.IsNotFound(err)
}

// ---------------------------------------------------------------------------
// Database error re-exports
// ---------------------------------------------------------------------------

// DatabaseError is a structured database error with error code, operation
// context, and optional retry semantics.
type DatabaseError = dberrors.DragonError

// DatabaseErrorCode represents a database error classification.
type DatabaseErrorCode = dberrors.ErrorCode

// Database error code constants.
const (
	ErrNotFound    = dberrors.ErrNotFound
	ErrInternal    = dberrors.ErrInternal
	ErrConflict    = dberrors.ErrConflict
	ErrTimeout     = dberrors.ErrTimeout
	ErrUnavailable = dberrors.ErrUnavailable
)

// NewDatabaseError creates a new DatabaseError with the given code and operation.
func NewDatabaseError(code DatabaseErrorCode, op string, message string) *DatabaseError {
	return dberrors.New(code, op, message)
}

// IsDatabaseError extracts a DatabaseError from the error chain if present.
func IsDatabaseError(err error, target **DatabaseError) bool {
	return dberrors.As(err, target)
}

// IsDatabaseErrorCode checks if the error matches the given database error code.
func IsDatabaseErrorCode(err error, code DatabaseErrorCode) bool {
	return dberrors.Is(err, code)
}
