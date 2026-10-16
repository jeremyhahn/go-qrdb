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
	"time"

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

// NewDAOTransactional creates a new DAOTransactional backed by the given
// KVStore.
func NewDAOTransactional[E dao.Entity](store KVStore, entityType string, newEntity func() E, opts ...DAOOption) (DAOTransactional[E], error) {
	return dao.NewTransactional(store, entityType, newEntity, opts...)
}

// NewDatabaseDAO creates a GenericDAO for entityType in the named database,
// its store resolved through factory (DAOFactory.DatabaseKVStore). A
// database the factory cannot resolve is refused with
// DAODatabaseStoreUnresolvedError; the DAO is never built over the
// factory's default store instead. NewClientDAOFactory resolves database
// names against a node or remote client.
func NewDatabaseDAO[E dao.Entity](factory *DAOFactory, database, entityType string, newEntity func() E, opts ...DAOOption) (GenericDAO[E], error) {
	return dao.NewDatabaseDAO(factory, database, entityType, newEntity, opts...)
}

// NewDatabaseDAOTransactional is NewDatabaseDAO for a DAOTransactional.
func NewDatabaseDAOTransactional[E dao.Entity](factory *DAOFactory, database, entityType string, newEntity func() E, opts ...DAOOption) (DAOTransactional[E], error) {
	return dao.NewDatabaseTransactional(factory, database, entityType, newEntity, opts...)
}

// NewClientDAOFactory returns a DAOFactory over c: its default store is
// c.KVStore(), and a database name resolves to the store of
// c.DatabaseScoped(name), so NewDatabaseDAO places a DAO in that database of
// the caller's tenant. A database c cannot scope to resolves to no store,
// which NewDatabaseDAO refuses with DAODatabaseStoreUnresolvedError.
func NewClientDAOFactory(c *Client) *DAOFactory {
	return dao.NewFactoryWithConfig(&dao.FactoryConfig{
		KVStoreProvider: func(uint64) kvstore.KVStore { return c.KVStore() },
		PartitionedProvider: func(database string, _ uint64) kvstore.KVStore {
			scoped, err := c.DatabaseScoped(database)
			if err != nil {
				return nil
			}
			return scoped.KVStore()
		},
	})
}

// IsDAONotFound returns true if the error indicates an entity was not found.
func IsDAONotFound(err error) bool {
	return dao.IsNotFound(err)
}

// ---------------------------------------------------------------------------
// DAOUnitOfWork re-exports
//
// DAOUnitOfWork itself (types_dao.go) is a type alias, but dao.RegisterSave,
// dao.RegisterDelete and dao.NewUnitOfWork are GENERIC functions, which Go
// cannot re-export as a `var` the way a non-generic func is re-exported --
// only a wrapper function, declared with its own type parameter, crosses
// the boundary. Without these an SDK consumer could name *sdk.DAOUnitOfWork
// (the existing alias) but never construct or populate one.
// ---------------------------------------------------------------------------

// DAOUnitOfWorkOption configures a DAOUnitOfWork at construction time.
type DAOUnitOfWorkOption = dao.UnitOfWorkOption

// NewDAOUnitOfWork creates a new DAOUnitOfWork backed by the given KVStore.
// The unit of work collects operations from multiple DAOs and commits them
// as one atomic batch; see DAOUnitOfWork.Commit's doc (dao.UnitOfWork) for
// what store that requires.
func NewDAOUnitOfWork(store KVStore, opts ...DAOUnitOfWorkOption) *DAOUnitOfWork {
	return dao.NewUnitOfWork(store, opts...)
}

// WithDAOUnitOfWorkTimeout sets the commit timeout for a DAOUnitOfWork.
func WithDAOUnitOfWorkTimeout(d time.Duration) DAOUnitOfWorkOption {
	return dao.WithUnitOfWorkTimeout(d)
}

// RegisterDAOSave queues a typed entity save operation within uow. The
// entity is serialized immediately; if it has no ID, one is auto-generated.
func RegisterDAOSave[E dao.Entity](uow *DAOUnitOfWork, d GenericDAO[E], entity E) error {
	return dao.RegisterSave(uow, d, entity)
}

// RegisterDAODelete queues a typed entity delete operation within uow.
func RegisterDAODelete[E dao.Entity](uow *DAOUnitOfWork, d GenericDAO[E], entity E) error {
	return dao.RegisterDelete(uow, d, entity)
}

// ---------------------------------------------------------------------------
// Database error re-exports
// ---------------------------------------------------------------------------

// DatabaseError is a structured database error with error code, operation
// context, and optional retry semantics.
type DatabaseError = dberrors.QRDBError

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
