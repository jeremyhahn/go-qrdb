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

import "github.com/jeremyhahn/go-qrdb/pkg/dao"

// ---------------------------------------------------------------------------
// DAO codec types
// ---------------------------------------------------------------------------

// Codec is the serialization interface used by DAOs to marshal and
// unmarshal entities.
type Codec[E any] = dao.Codec[E]

// CodecType identifies a serialization format (JSON, CBOR, MsgPack, etc.).
type CodecType = dao.CodecType

// SupportedCodecs returns the list of codec types compiled into the binary.
var SupportedCodecs = dao.SupportedCodecs

// IsCodecSupported reports whether the given codec type is available.
var IsCodecSupported = dao.IsCodecSupported

// NewCodec creates a new Codec for the given CodecType. This is a generic
// function; call it as sdk.NewCodec[MyEntity](codecType).
func NewCodec[E any](ct CodecType) (Codec[E], error) {
	return dao.NewCodec[E](ct)
}

// ---------------------------------------------------------------------------
// DAO ID generation
// ---------------------------------------------------------------------------

// IDGenerator generates unique entity IDs for DAO entities.
type IDGenerator = dao.IDGenerator

// FieldHashGenerator generates deterministic entity IDs by hashing a
// named struct field using FNV-1a.
type FieldHashGenerator = dao.FieldHashGenerator

// NewFieldHashGenerator creates a new FieldHashGenerator for the given
// struct field name.
var NewFieldHashGenerator = dao.NewFieldHashGenerator

// WithIDGenerator configures a GenericDAO to use a custom ID generator.
var WithIDGenerator = dao.WithIDGenerator

// ---------------------------------------------------------------------------
// DAO options
//
// These cross the SDK boundary under the DAOWith* name rather than the
// upstream WithX name because the SDK root already has ClientOption
// namesakes. sdk.WithTimeout is a ClientOption, so re-exporting the DAO
// option under the same identifier would either shadow it or produce a
// type-mismatch error at every call site — the confusing failure mode
// data-access.md records. The prefix makes which one you have unambiguous.
// ---------------------------------------------------------------------------

// DAOWithShard sets the Raft shard ID a DAO stores its entities in. Ignored
// in local and memory modes.
var DAOWithShard = dao.WithShard

// DAOWithCodec sets the serialization codec, one of the DAOCodec* constants.
var DAOWithCodec = dao.WithCodec

// DAOWithTimeout sets the per-operation timeout applied to a DAO's context
// deadlines. Distinct from sdk.WithTimeout, which is a ClientOption.
var DAOWithTimeout = dao.WithTimeout

// DAOWithPartition selects the storage partition, which must be registered
// with the DAOFactory.
var DAOWithPartition = dao.WithPartition

// DAOWithDatabase sets the database namespace prefix used to resolve index
// names, giving databases that share a shard their own index space.
var DAOWithDatabase = dao.WithDatabase

// ---------------------------------------------------------------------------
// DAO codec selection
// ---------------------------------------------------------------------------

// DAOCodecType identifies a DAO serialization codec.
type DAOCodecType = dao.CodecType

// DAOCodecJSON selects standard JSON encoding. Always available.
const DAOCodecJSON = dao.CodecJSON

// DAOCodecMsgPack selects MessagePack binary encoding, subject to build tags.
const DAOCodecMsgPack = dao.CodecMsgPack

// DAOCodecCBOR selects CBOR binary encoding, subject to build tags.
const DAOCodecCBOR = dao.CodecCBOR

// ---------------------------------------------------------------------------
// DAO sort order
// ---------------------------------------------------------------------------

// DAOSortDirection is the ordering passed to QueryBuilder.OrderBy and carried
// in PageQuery.SortOrder.
type DAOSortDirection = dao.SortDirection

// DAOSortAscending orders results ascending.
const DAOSortAscending = dao.SortAscending

// DAOSortDescending orders results descending.
const DAOSortDescending = dao.SortDescending

// ---------------------------------------------------------------------------
// DAO transaction and entity-trait types
//
// Only the ERRORS of these types crossed the boundary before, so a consumer
// could test for a failure it could not name the type of.
// ---------------------------------------------------------------------------

// DAOUnitOfWork batches entity mutations and applies them atomically.
type DAOUnitOfWork = dao.UnitOfWork

// DAOTransactional is a GenericDAO that can enlist in a transaction.
type DAOTransactional[E dao.Entity] = dao.TransactionalDAO[E]

// DAOVersioned is implemented by entities carrying an optimistic-concurrency
// version, which SaveWithCondition compares against.
type DAOVersioned = dao.Versioned

// DAOTimestamped is implemented by entities carrying create/modify times.
type DAOTimestamped = dao.Timestamped

// ---------------------------------------------------------------------------
// DAO Pager interface
// ---------------------------------------------------------------------------

// Pager provides paginated listing of entities.
type Pager[E any] = dao.Pager[E]

// ---------------------------------------------------------------------------
// DynamicEntity
// ---------------------------------------------------------------------------

// DynamicEntity is a schema-less entity backed by a map of fields.
type DynamicEntity = dao.DynamicEntity

// DynamicFieldHashGenerator generates entity IDs by hashing a named field.
type DynamicFieldHashGenerator = dao.DynamicFieldHashGenerator

// NewDynamicFieldHashGenerator creates a new DynamicFieldHashGenerator for
// the given field name.
var NewDynamicFieldHashGenerator = dao.NewDynamicFieldHashGenerator

// ---------------------------------------------------------------------------
// DAO Factory
// ---------------------------------------------------------------------------

// DAOFactory manages DAO creation with shared KVStore providers.
type DAOFactory = dao.Factory

// DAOFactoryConfig holds configuration for a DAOFactory including
// partitioned KVStore providers.
type DAOFactoryConfig = dao.FactoryConfig

// KVStoreProvider returns a KVStore for the given shard ID.
type KVStoreProvider = dao.KVStoreProvider

// PartitionedKVStoreProvider returns a KVStore for a named partition and shard.
type PartitionedKVStoreProvider = dao.PartitionedKVStoreProvider

// EntityPartitionMapping maps entity types to partition names.
type EntityPartitionMapping = dao.EntityPartitionMapping

// NewDAOFactory creates a new DAOFactory with a single KVStore provider.
var NewDAOFactory = dao.NewFactory

// NewDAOFactoryWithConfig creates a new DAOFactory from a FactoryConfig.
var NewDAOFactoryWithConfig = dao.NewFactoryWithConfig

// ---------------------------------------------------------------------------
// DAO error types
// ---------------------------------------------------------------------------

// DAONotFoundError is returned when a DAO entity lookup finds no result.
type DAONotFoundError = dao.NotFoundError

// DAOAtomicWriteUnsupportedError is SaveIfAbsent's refusal when the KVStore
// cannot perform an atomic conditional write: either it is not an
// AtomicKVStore at all, or it is one and refused the operation (the refusal is
// then reachable through Unwrap). Nothing was written, and retrying against
// the same store will not write it. It matches ErrKVAtomicWriteUnsupported,
// the one marker for the condition, under errors.Is.
//
// A consumer that needs exactly-once creation fails closed on it; it must not
// fall back to a Get-then-Save, which is the race SaveIfAbsent exists to close.
type DAOAtomicWriteUnsupportedError = dao.AtomicWriteUnsupportedError

// DAOSerializationFailedError is returned when entity serialization fails.
type DAOSerializationFailedError = dao.SerializationFailedError

// DAODeserializationFailedError is returned when entity deserialization fails.
type DAODeserializationFailedError = dao.DeserializationFailedError

// DAOInvalidEntityIDError is returned when an entity ID is invalid.
type DAOInvalidEntityIDError = dao.InvalidEntityIDError

// DAOCodecCreationError is returned when codec creation fails.
type DAOCodecCreationError = dao.CodecCreationError

// DAOInvalidConfigError is returned when DAO configuration is invalid.
type DAOInvalidConfigError = dao.InvalidConfigError

// DAOTransactionClosedError is returned when an operation is attempted on
// a closed transaction.
type DAOTransactionClosedError = dao.TransactionClosedError

// DAOTransactionCommitError is returned when a transaction commit fails.
type DAOTransactionCommitError = dao.TransactionCommitError

// DAOInvalidEntityError is returned when an entity fails validation.
type DAOInvalidEntityError = dao.InvalidEntityError

// DAOInvalidConditionError is returned when a condition expression is invalid.
type DAOInvalidConditionError = dao.InvalidConditionError

// DAOConditionFailedError is returned when a conditional write fails.
type DAOConditionFailedError = dao.ConditionFailedError

// DAOUnitOfWorkClosedError is returned when an operation is attempted on
// a closed unit of work.
type DAOUnitOfWorkClosedError = dao.UnitOfWorkClosedError

// DAOUnitOfWorkCommitError is returned when a unit of work commit fails.
type DAOUnitOfWorkCommitError = dao.UnitOfWorkCommitError

// IsDAOTransactionClosed reports whether the error is a TransactionClosedError.
var IsDAOTransactionClosed = dao.IsTransactionClosed

// IsDAOTransactionCommitError reports whether the error is a TransactionCommitError.
var IsDAOTransactionCommitError = dao.IsTransactionCommitError

// IsDAOInvalidEntity reports whether the error is an InvalidEntityError.
var IsDAOInvalidEntity = dao.IsInvalidEntity

// IsDAOInvalidCondition reports whether the error is an InvalidConditionError.
var IsDAOInvalidCondition = dao.IsInvalidCondition

// IsDAOConditionFailed reports whether the error is a ConditionFailedError.
var IsDAOConditionFailed = dao.IsConditionFailed

// IsDAOUnitOfWorkClosed reports whether the error is a UnitOfWorkClosedError.
var IsDAOUnitOfWorkClosed = dao.IsUnitOfWorkClosed

// IsDAOUnitOfWorkCommitError reports whether the error is a UnitOfWorkCommitError.
var IsDAOUnitOfWorkCommitError = dao.IsUnitOfWorkCommitError
