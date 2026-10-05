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

import "context"

// Pager provides pagination operations for entity collections.
//
// Neither method verifies a natural key: both return whatever is stored,
// scanned by key prefix rather than looked up by a specific natural-key
// value, so there is no "expected" value to check a record against. Only
// Save, SaveIfAbsent, UnitOfWork's queued saves and GetByNaturalKey verify
// (see GenericDAO.GetByNaturalKey's doc for the full list of which
// operations do and do not).
type Pager[E any] interface {
	// Page retrieves a single page of entities.
	// The PageQuery specifies page number, size, and sort order.
	// Returns a PageResult containing the entities and pagination metadata.
	Page(ctx context.Context, query PageQuery) (PageResult[E], error)

	// ForEachPage iterates over all entities page by page.
	// The callback function is invoked for each page of entities.
	// Return nil from the callback to continue, or an error to stop.
	// Iteration stops when HasMore is false or the callback returns an error.
	ForEachPage(ctx context.Context, query PageQuery, fn PagerFunc[E]) error
}

// GenericDAO provides type-safe CRUD operations for entities.
// It abstracts the underlying storage mechanism (Raft, PebbleDB, Memory)
// and provides consistent operations across all QRDB modes.
//
// The DAO is parameterized by entity type E, which must implement Entity.
// All operations are context-aware for cancellation and timeout support.
//
// Example usage:
//
//	type User struct {
//	    ID    uint64 `json:"id"`
//	    Name  string `json:"name"`
//	    Email string `json:"email"`
//	}
//
//	func (u *User) EntityID() uint64      { return u.ID }
//	func (u *User) SetEntityID(id uint64) { u.ID = id }
//
//	// Create DAO
//	userDAO, err := dao.New(kvStore, "users", func() *User { return &User{} })
//
//	// Save entity
//	user := &User{Name: "Alice", Email: "alice@example.com"}
//	err = userDAO.Save(ctx, user)  // ID auto-generated
//
//	// Get by ID
//	user, err = userDAO.Get(ctx, user.EntityID())
//
//	// Delete
//	err = userDAO.Delete(ctx, user)
//
//	// Count all
//	count, err := userDAO.Count(ctx)
//
//	// Paginate
//	result, err := userDAO.Page(ctx, dao.PageQuery{Page: 1, PageSize: 25})
//
//	// Conditional save. NOT optimistic locking: the condition is evaluated
//	// with a read followed by a write, and User must implement Versioned for
//	// ConditionVersion to be evaluable at all. See SaveWithCondition.
//	err = userDAO.SaveWithCondition(ctx, user, dao.Condition{
//	    Target: dao.ConditionVersion, Operator: dao.OpEqual, Value: user.GetVersion(),
//	})
type GenericDAO[E Entity] interface {
	// Save persists an entity to storage.
	// If EntityID() returns 0, a new ID is generated using the configured IDGenerator.
	// Existing entities with the same ID are overwritten.
	Save(ctx context.Context, entity E) error

	// SaveIfAbsent persists an entity only when no entity with its ID exists,
	// reporting whether this call created it.
	//
	// stored=false with a nil error means the entity already existed and
	// storage is unchanged. A non-nil error means the outcome is unknown.
	//
	// This is the only write on this interface with a defined outcome under
	// concurrency, and it is not interchangeable with Get-then-Save:
	// SaveWithCondition evaluates its condition with a read followed by a
	// write, so two callers can both pass and both save. Anything that must
	// happen exactly once — consuming a single-use token, claiming a unique
	// name — requires this.
	//
	// It requires the underlying KVStore to implement kvstore.AtomicKVStore
	// and returns *AtomicWriteUnsupportedError (matching
	// kvstore.ErrAtomicWriteUnsupported) when it does not, rather than
	// silently degrading to a racy read-then-write.
	SaveIfAbsent(ctx context.Context, entity E) (stored bool, err error)

	// SaveWithCondition persists an entity only if the given condition is
	// satisfied.
	//
	// The condition is evaluated with a READ FOLLOWED BY A WRITE, not as one
	// guarded store operation. Two callers can therefore both observe the same
	// version, both pass, and both save — the lost update the condition reads
	// as preventing. This is NOT optimistic locking, and it is not a substitute
	// for SaveIfAbsent above, which is the only write on this interface with a
	// defined outcome under concurrency.
	//
	// It said "enables optimistic locking patterns" and "wraps the operation in
	// a single-operation transaction for atomicity" until go-qrdb#59; that
	// transaction sequences the read and the write but does not make them
	// atomic against another caller, and a consumer reading the old text
	// believed they held a guarantee the code does not offer.
	//
	// ConditionVersion requires the entity to implement Versioned (re-exported
	// as sdk/go.DAOVersioned) and returns ConditionFailedError when it does
	// not, rather than comparing against a fabricated version.
	SaveWithCondition(ctx context.Context, entity E, cond Condition) error

	// Get retrieves an entity by ID.
	// Returns ErrNotFound if the entity doesn't exist.
	//
	// Get does NOT verify a natural key: it takes a bare id, never the
	// natural-key value a mismatch would be checked against, so there is
	// nothing for it to verify. A caller that computed id itself from a
	// DeterministicIDGenerator and must not silently accept a DIFFERENT
	// entity that happened to hash to the same id calls VerifyNaturalKey
	// explicitly afterward, or uses GetByNaturalKey instead, which does
	// this automatically. See GetByNaturalKey's doc for the full list of
	// which operations verify and which do not.
	Get(ctx context.Context, id uint64) (E, error)

	// GetByNaturalKey retrieves the entity whose natural-key field equals
	// value, deriving the lookup ID from the DAO's configured IDGenerator
	// the same way Save does, and verifying the result against value with
	// VerifyNaturalKey before returning it.
	//
	// This is the read-side half of the write-side guarantee Save and
	// UnitOfWork's queued saves provide for a DeterministicIDGenerator
	// (FieldHashGenerator, CryptoFieldHashGenerator,
	// DynamicFieldHashGenerator, CryptoDynamicFieldHashGenerator): a
	// hash-derived ID is drawn from a 64-bit space shared by every
	// natural-key value the generator ever hashes, so Get(ctx, id) alone
	// cannot tell a collision from a correct lookup -- by the time it has
	// an ID it no longer has the value the caller meant to look up by.
	// GetByNaturalKey cannot be called without the verification, unlike the
	// manual Get-then-VerifyNaturalKey pattern, which a caller can forget
	// half of (go-qrdb#589).
	//
	// Only the create/save paths verify the natural key, and only
	// GetByNaturalKey verifies on read: Save, SaveIfAbsent, UnitOfWork's
	// queued saves and GetByNaturalKey all call VerifyNaturalKey (directly,
	// or through the guarded conditional write the coordinator ruling on
	// re-saving the same natural key requires -- see Save's doc). Get(id),
	// Page, ForEachPage and Delete do NOT verify: each takes an id or an
	// already-loaded entity, never the natural-key value a mismatch would
	// be checked against.
	//
	// Returns *UnsupportedIDGeneratorError when the configured IDGenerator
	// is not a DeterministicIDGenerator, since there is then no
	// deterministic way to derive an ID from a bare value. Returns
	// NaturalKeyMismatchError when the derived ID names a DIFFERENT entity.
	// Returns NotFoundError when it names no entity at all.
	GetByNaturalKey(ctx context.Context, value string) (E, error)

	// Delete removes an entity from storage.
	//
	// Delete does NOT verify a natural key: it takes an already-loaded
	// entity and removes whatever is stored under its EntityID(), with no
	// "expected" natural-key value to check that record against. See
	// GetByNaturalKey's doc for the full list of which operations verify.
	// Returns ErrInvalidEntityID if entity.EntityID() is 0.
	// Returns nil if the entity doesn't exist (idempotent).
	Delete(ctx context.Context, entity E) error

	// CompareAndSwap persists next only while the entity currently stored
	// under next's ID still marshals to EXACTLY the bytes expected last
	// marshaled to, reporting whether this call wrote it.
	//
	// Unlike SaveWithCondition, the comparison and the write are ONE guarded
	// store operation (kvstore.AtomicKVStore.CompareAndSwap): no caller can
	// observe expected's bytes, decide to write, and lose a race to another
	// caller doing the same between its read and its write. swapped=false
	// with a nil error means the stored bytes had already changed and
	// storage is unchanged; a non-nil error means the outcome is unknown.
	//
	// expected and next must share an EntityID; InvalidEntityIDError is
	// returned otherwise, since a CAS that compared one key and wrote
	// another would not be comparing anything the caller thought it was.
	//
	// It requires the underlying KVStore to implement kvstore.AtomicKVStore
	// and returns *AtomicWriteUnsupportedError (matching
	// kvstore.ErrAtomicWriteUnsupported) when it does not, exactly as
	// SaveIfAbsent does: a store that cannot evaluate the compare and the
	// write together must not be allowed to silently decay into a racy
	// Get-then-Save (go-qrdb#589).
	//
	// The codec must serialize the same logical value to the same bytes
	// every time for this to mean anything -- encoding/json does for a
	// struct (field order is declaration order) and for a map (keys are
	// sorted), but a codec that is not byte-deterministic across equal
	// values makes every CompareAndSwap against a value read earlier fail
	// spuriously.
	CompareAndSwap(ctx context.Context, expected, next E) (swapped bool, err error)

	// DeleteIfMatch removes entity only while the value currently stored
	// under its ID still marshals to exactly entity's own bytes, reporting
	// whether this call deleted it.
	//
	// It is the delete that completes CompareAndSwap and SaveIfAbsent: Delete
	// above is unconditional, so a caller that read entity, decided it was
	// safe to remove, and called Delete can race a concurrent writer that
	// replaced it in between -- the unconditional Delete then destroys the
	// WINNER's record with no signal. DeleteIfMatch closes that window the
	// same way CompareAndSwap closes SaveWithCondition's: the compare and the
	// delete are ONE guarded store operation
	// (kvstore.AtomicKVStore.CompareAndDelete).
	//
	// Requires kvstore.AtomicKVStore; refuses with *AtomicWriteUnsupportedError
	// exactly as SaveIfAbsent and CompareAndSwap do.
	DeleteIfMatch(ctx context.Context, entity E) (deleted bool, err error)

	// Count returns the total number of entities of this type.
	Count(ctx context.Context) (int, error)

	// Pager provides pagination operations.
	Pager[E]

	// EntityType returns the entity type name used for key prefixing.
	EntityType() string

	// QueryByIndex returns all entities matching the given field value.
	// The fieldName must correspond to an indexed field (struct tag `index:"..."`).
	QueryByIndex(ctx context.Context, fieldName string, value interface{}) ([]E, error)

	// QueryOneByIndex returns a single entity matching the given field value.
	// Returns NotFoundError if no matching entity exists.
	QueryOneByIndex(ctx context.Context, fieldName string, value interface{}) (E, error)

	// ScanByIndex returns all entities with indexed field values in the
	// range [start, end).
	ScanByIndex(ctx context.Context, fieldName string, start, end interface{}) ([]E, error)

	// PageByIndex returns a page of entities matching the given index field value.
	PageByIndex(ctx context.Context, fieldName string, value interface{}, query PageQuery) (PageResult[E], error)

	// CountByIndex returns the count of entities matching the given index value
	// without loading entities.
	CountByIndex(ctx context.Context, fieldName string, value interface{}) (int, error)

	// QueryByCompoundIndex returns all entities matching the given compound
	// index field values. The indexName must be the fully-qualified compound
	// index name (e.g., "entityType.Field1_Field2"). The fieldValues must
	// match all fields of the compound index in order.
	QueryByCompoundIndex(ctx context.Context, indexName string, fieldValues []interface{}) ([]E, error)

	// ScanByCompoundIndex returns entities matching a prefix of compound
	// index fields with an optional range on the next field. prefixValues
	// are exact-match values for the leading N fields; rangeStart and
	// rangeEnd define a half-open range on field N+1.
	ScanByCompoundIndex(ctx context.Context, indexName string, prefixValues []interface{}, rangeStart, rangeEnd interface{}) ([]E, error)

	// Find starts a fluent query chain with automatic index selection.
	// The returned QueryBuilder allows chaining Where, OrderBy, Limit,
	// and Page methods before executing with Execute, First, or Count.
	//
	// Example:
	//
	//	user, err := userDAO.Find(ctx).Where("Email = ?", "alice@example.com").First()
	//	users, err := userDAO.Find(ctx).Where("Age > ?", 30).OrderBy("Name").Execute()
	Find(ctx context.Context) *QueryBuilder[E]
}
