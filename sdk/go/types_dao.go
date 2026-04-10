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

// CodecType identifies a serialization format (JSON, CBOR, Avro, etc.).
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
