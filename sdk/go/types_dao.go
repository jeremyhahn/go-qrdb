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

// DeterministicIDGenerator is implemented by every hash-derived IDGenerator
// below (FieldHashGenerator, CryptoFieldHashGenerator,
// DynamicFieldHashGenerator, CryptoDynamicFieldHashGenerator): NextID's
// result depends only on one named natural-key value, so two different
// entities can land on the same ID through nothing worse than an unlucky
// draw from the 64-bit output space (see CryptoFieldHashGenerator's doc for
// the bound). GenericDAO.Save, DAOUnitOfWork's queued saves and
// GenericDAO.GetByNaturalKey use it to close the write- and read-side
// halves of that gap (go-qrdb#589). A record at the derived ID carrying the
// SAME natural-key value is the SAME logical entity -- an update, applied
// through a guarded conditional write so it stays atomic -- not a
// collision; only a DIFFERENT natural-key value at that ID is a genuine
// collision, refused with *DAOIDCollisionError. An embedder implementing a
// custom deterministic generator satisfies both write- and read-side
// guarantees by implementing this interface.
type DeterministicIDGenerator = dao.DeterministicIDGenerator

// FieldHashGenerator generates deterministic entity IDs by hashing a
// named struct field using FNV-1a.
type FieldHashGenerator = dao.FieldHashGenerator

// NewFieldHashGenerator creates a new FieldHashGenerator for the given
// struct field name.
var NewFieldHashGenerator = dao.NewFieldHashGenerator

// CryptoFieldHashGenerator is FieldHashGenerator's collision-resistant
// counterpart: it hashes the named struct field with SHA-256, truncated to
// 64 bits, instead of FNV-1a.
//
// The difference is resistance, not collision probability: both land in
// the same 64-bit ID space and share its bounds. An attacker who controls
// BOTH colliding natural-key values needs roughly 2^32 attempts to find
// any pair that collides (the birthday bound on a 64-bit space); an
// attacker who must hit one FIXED victim ID needs roughly 2^64 (a generic
// preimage search), and FNV-1a offers no resistance at all to that second
// case -- a value whose FNV-1a sum equals a chosen target's is
// constructible directly, not merely found by brute force (go-qrdb#589).
// SHA-256 does not change either bound; it closes the direct-construction
// attack FNV-1a is open to. A caller that must DETECT a collision that
// occurred despite this -- rather than merely resist one being engineered
// -- uses GetByNaturalKey, or calls VerifyNaturalKey after Get directly.
//
// Only the create/save paths verify the natural key: Save, SaveIfAbsent,
// DAOUnitOfWork's queued saves and GetByNaturalKey all call VerifyNaturalKey
// (directly or through the create-if-absent/guarded-update write). Get(id),
// Page, ForEachPage and Delete do NOT -- they take an id or an
// already-loaded entity, never the natural-key value a mismatch would be
// checked against, so there is nothing for them to verify. A caller reading
// by id after resolving it some other way (an index query, a cached value)
// that must not accept a collided record calls VerifyNaturalKey itself.
type CryptoFieldHashGenerator = dao.CryptoFieldHashGenerator

// NewCryptoFieldHashGenerator creates a new CryptoFieldHashGenerator for
// the given struct field name.
var NewCryptoFieldHashGenerator = dao.NewCryptoFieldHashGenerator

// NaturalKeyMismatchError is returned by VerifyNaturalKey when an entity
// read back by a hash-derived ID does not carry the natural-key value the
// caller looked it up by -- two distinct natural keys hashed to the same ID.
// It is a value type, not a pointer: match it with
// `var mismatch NaturalKeyMismatchError; errors.As(err, &mismatch)`.
type NaturalKeyMismatchError = dao.NaturalKeyMismatchError

// VerifyNaturalKey reads fieldName off entity and reports a
// NaturalKeyMismatchError when its string form does not equal expected.
//
// GenericDAO.GetByNaturalKey calls this on every read, so a caller using it
// gets the check with no separate step to remember. Call it directly after
// Get only when entity's ID was computed outside the DAO from a
// hash-derived generator (FieldHashGenerator, CryptoFieldHashGenerator,
// DynamicFieldHashGenerator, CryptoDynamicFieldHashGenerator) and silently
// accepting a DIFFERENT entity that happened to hash to the same ID is not
// acceptable.
var VerifyNaturalKey = dao.VerifyNaturalKey

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
// DAO secret entity fields
// ---------------------------------------------------------------------------

// DAOSecretBytes is an entity field holding secret key material in
// protected memory, carried by the DAO codec with no plaintext copy on the
// heap that cannot be wiped. Only DAOCodecMsgPack carries it; every other
// codec refuses the entity with DAOSecretCodecUnsupportedError. See
// dao.SecretBytes.
type DAOSecretBytes = dao.SecretBytes

// NewDAOSecretBytes moves src into protected memory, wiping src on every
// return path.
var NewDAOSecretBytes = dao.NewSecretBytes

// DAOSecretBytesFromBuffer wraps a protected secure.Buffer, taking ownership
// of it, for material that never touched the heap.
var DAOSecretBytesFromBuffer = dao.SecretBytesFromBuffer

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

// CryptoDynamicFieldHashGenerator is DynamicFieldHashGenerator's
// collision-resistant counterpart; see CryptoFieldHashGenerator for the
// resistance SHA-256 adds over FNV-1a, what it does not, and the bounds on
// both.
type CryptoDynamicFieldHashGenerator = dao.CryptoDynamicFieldHashGenerator

// NewCryptoDynamicFieldHashGenerator creates a new
// CryptoDynamicFieldHashGenerator for the given field name.
var NewCryptoDynamicFieldHashGenerator = dao.NewCryptoDynamicFieldHashGenerator

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

// DAOIDCollisionError is Save's and DAOUnitOfWork.Commit's refusal when a
// DeterministicIDGenerator derives an ID for a NEW entity that already
// names a stored record carrying a DIFFERENT natural-key value.
//
// It is reserved for a CONFIRMED collision -- never returned for a record
// at that ID carrying the SAME natural-key value, which is the same
// logical entity and is applied as an update instead (the coordinator
// ruling, go-qrdb#589 review), nor for a write refused by contention alone
// (see *DAOSaveContentionExhaustedError). Save and DAOUnitOfWork.Commit
// both route the write through the store's atomic conditional write and
// refuse with this error rather than silently overwrite a different
// entity's record.
type DAOIDCollisionError = dao.IDCollisionError

// DAOSaveContentionExhaustedError is Save's refusal when a deterministic
// ID's update path -- the existing record carries the SAME natural-key
// value, so the write is a CompareAndSwap, not a create -- lost its
// bounded retry budget to a concurrent writer repeatedly winning the race
// between the read and the swap. It is NOT a collision: the natural key
// matched every time it was checked, so this is never
// *DAOIDCollisionError's message.
type DAOSaveContentionExhaustedError = dao.SaveContentionExhaustedError

// DAOUnsupportedIDGeneratorError is GetByNaturalKey's refusal when the
// DAO's configured IDGenerator does not implement DeterministicIDGenerator,
// so there is no deterministic way to derive a lookup ID from a bare
// natural-key value.
type DAOUnsupportedIDGeneratorError = dao.UnsupportedIDGeneratorError

// DAOCodecCreationError is returned when codec creation fails.
type DAOCodecCreationError = dao.CodecCreationError

// DAOSecretCodecUnsupportedError is the refusal of a codec that cannot carry
// a DAOSecretBytes field without an unwipeable plaintext copy, wrapped in
// DAOCodecCreationError when a DAO or codec is built.
type DAOSecretCodecUnsupportedError = dao.SecretCodecUnsupportedError

// DAOSecretEncodingSizeError is the refusal of an entity holding a
// DAOSecretBytes whose encoding changed length while it was encoded, which
// only a concurrent mutation causes.
type DAOSecretEncodingSizeError = dao.SecretEncodingSizeError

// DAOSecretFieldIndexedError is the refusal of an index over a
// DAOSecretBytes field.
type DAOSecretFieldIndexedError = dao.SecretFieldIndexedError

// DAOInvalidConfigError is returned when DAO configuration is invalid.
type DAOInvalidConfigError = dao.InvalidConfigError

// DAODatabaseStoreUnresolvedError is NewDatabaseDAO's refusal when the
// DAOFactory cannot resolve the named database's store. Nothing was built.
type DAODatabaseStoreUnresolvedError = dao.DatabaseStoreUnresolvedError

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

// DAOAtomicBatchUnsupportedError is DAOUnitOfWork.Commit's refusal when the
// underlying store cannot apply the queued operations as one atomic batch:
// either its type offers no kvstore.MultiGuardedBatchKVStore at all, or it
// offers one and the backing state machine refused the proposal at call
// time (then reachable through Unwrap). There is no fallback: a store that
// cannot do atomic multi-key batches must say so, not emulate one with a
// non-atomic sequential apply.
type DAOAtomicBatchUnsupportedError = dao.AtomicBatchUnsupportedError

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
