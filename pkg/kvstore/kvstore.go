// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package kvstore defines the context-aware key-value store interface used
// by the DAO and service layers. Unlike storage.Storage (used by state
// machines), KVStore supports context propagation for timeouts, cancellation,
// and distributed tracing.
package kvstore

import "context"

// ReservedPrefixSystem is the key prefix reserved for qrdb's own internal
// system stores — users, roles, permission scopes and the other RBAC records.
//
// It lives here, at the layer every store shares, because two things need it
// and neither can import the other: pkg/service enforces the reserved-prefix
// discovery guard over it, and pkg/tenant refuses a tenant id that begins
// with it. pkg/service re-exports it as InternalKeyPrefixSystem so its
// existing callers are unchanged.
//
// It is NOT an exemption from tenant namespacing. A namespaced store confines
// reserved keys exactly like any other; the prefix marks records as owned by
// an internal subsystem, not as globally addressable.
const ReservedPrefixSystem = "__sys:"

// EntityIndex defines an index to be created for an entity type's field.
// For single-field indexes, FieldName and JSONFieldName are used.
// For compound indexes, FieldNames and JSONFieldNames are used instead.
type EntityIndex struct {
	// IndexName is the fully resolved name the index must be created under.
	//
	// When empty a store composes "{entityType}.{FieldName}". When set it is
	// authoritative and the store MUST use it verbatim, because the caller is
	// the only party that knows the name it will later look the index up by:
	// a DAO configured with WithDatabase resolves
	// "{database}.{entityType}.{FieldName}", and a store that composed the
	// unqualified name would create an index that every query then misses —
	// advertising an index, planning a lookup, and querying a name nobody
	// created.
	//
	// It is a separate field rather than a qualified EntityType because the
	// entity type is also the "{entityType}/" key prefix a store filters
	// writes by, and those two must not be conflated.
	IndexName string

	// KeyPrefix is the key prefix this index covers. When empty a store
	// assumes "{entityType}/", which is correct only while the entity type is
	// the LEADING segment of every key.
	//
	// A namespaced store breaks that assumption: its keys are
	// "{tenant}/{entityType}/{id}", so a store inferring "{entityType}/"
	// matches nothing and silently indexes no rows. Stating the prefix
	// explicitly is what lets an index be maintained over keys the store did
	// not choose the layout of.
	KeyPrefix string

	// FieldName is the Go struct field name (used as the index name component).
	// Used for single-field indexes. Ignored when FieldNames is non-empty.
	FieldName string

	// JSONFieldName is the JSON tag name (used for field extraction from serialized bytes).
	// Used for single-field indexes. Ignored when JSONFieldNames is non-empty.
	JSONFieldName string

	// FieldNames is an ordered list of Go struct field names for compound indexes.
	// When non-empty, this index is compound and FieldName is ignored.
	FieldNames []string

	// JSONFieldNames is an ordered list of JSON tag names for compound indexes.
	// When non-empty, this index is compound and JSONFieldName is ignored.
	JSONFieldNames []string

	// Unique enforces that each indexed value maps to at most one key.
	Unique bool

	// Sparse skips indexing keys where the indexed field is absent or empty.
	Sparse bool

	// CaseSensitive indicates whether string comparisons are case-sensitive.
	CaseSensitive bool
}

// IsCompound reports whether the index covers multiple fields.
func (ei EntityIndex) IsCompound() bool {
	return len(ei.FieldNames) > 1
}

// ResolvedPrefix returns the key prefix this index covers: KeyPrefix when the
// caller supplied one, otherwise "{entityType}/".
func (ei EntityIndex) ResolvedPrefix(entityType string) string {
	if ei.KeyPrefix != "" {
		return ei.KeyPrefix
	}
	return entityType + "/"
}

// ResolvedName returns the name this index must be created and queried under:
// IndexName when the caller supplied one, otherwise "{entityType}.{FieldName}".
//
// Every store uses this rather than composing the name itself, so a caller
// that resolves index names differently — a DAO with a database namespace —
// cannot end up querying a name the store never created.
func (ei EntityIndex) ResolvedName(entityType string) string {
	if ei.IndexName != "" {
		return ei.IndexName
	}
	return entityType + "." + ei.FieldName
}

// EntityRegisterer allows entity types to register their index definitions.
type EntityRegisterer interface {
	RegisterEntityType(entityType string, indexes []EntityIndex) error
}

// LookupFunc performs a query against the engine's index subsystem.
// The query parameter is an index.IndexQueryRequest or
// index.IndexScanRequest. The return value depends on the request
// type: [][]byte for queries, map[string][][]byte for scans.
type LookupFunc func(ctx context.Context, query interface{}) (interface{}, error)

// KVStore is a context-aware key-value store interface for service-layer
// and DAO operations. Implementations route through the Raft consensus
// layer for strong consistency, or read from local replicas for
// eventual consistency.
type KVStore interface {
	// Put stores a key-value pair. The context controls timeout and
	// cancellation for Raft proposals.
	Put(ctx context.Context, key string, value []byte) error

	// Get retrieves a value by key. Returns nil, nil when key does not exist.
	// The context controls timeout and cancellation for Raft reads.
	Get(ctx context.Context, key string) ([]byte, error)

	// Delete removes a key. The context controls timeout and cancellation
	// for Raft proposals.
	Delete(ctx context.Context, key string) error

	// Scan iterates over keys with the given prefix, calling fn for each
	// key-value pair. Return a non-nil error from fn to stop iteration.
	// The context controls timeout and cancellation for the scan.
	Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error

	// List returns all keys matching the given prefix. The context controls
	// timeout and cancellation for the listing operation.
	List(ctx context.Context, prefix string) ([]string, error)

	// Exists checks whether a key exists in the store. The context controls
	// timeout and cancellation for the lookup.
	Exists(ctx context.Context, key string) (bool, error)

	// RegisterEntityIndexes registers index definitions for an entity type.
	// The engine creates storage-backed indexes and backfills them from
	// existing data.
	//
	// Registration is idempotent and ACCUMULATES: repeated calls for the same
	// entity type add definitions rather than replacing the set, and a
	// definition already registered is a no-op. Callers rely on this —
	// pkg/dao registers single-field and compound definitions in separate
	// calls, because a store can serve one kind and not the other, and a
	// store that replaced the set would drop the first kind.
	//
	// A store that cannot maintain secondary indexes MUST return
	// ErrIndexUnsupported here rather than reporting success it will not
	// deliver. Registration is the only point at which a caller can learn
	// the capability before it depends on it.
	RegisterEntityIndexes(entityType string, indexes []EntityIndex) error

	// QueryIndex performs an exact-match query on the named index.
	// Returns the entity keys matching the given value.
	//
	// A store that cannot serve index queries MUST return
	// ErrIndexUnsupported. Returning (nil, nil) is forbidden: it is
	// indistinguishable from a maintained index with no matches, which
	// turns a broken query into a silent "no records" answer. The same
	// rule binds ScanIndex, QueryCompoundIndex and ScanCompoundIndex.
	QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error)

	// ScanIndex performs a range scan on the named index.
	// Returns a map of field values to entity key slices for values
	// in the half-open interval [start, end).
	ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error)

	// QueryCompoundIndex performs an exact-match query on a compound index.
	// All field values must match. Returns the entity keys matching the
	// given compound value.
	QueryCompoundIndex(ctx context.Context, indexName string, values [][]byte) ([][]byte, error)

	// ScanCompoundIndex performs a prefix + range scan on a compound index.
	// prefixValues are exact-match values for the leading fields of the
	// compound index. rangeStart and rangeEnd define a half-open range on
	// the next field after the prefix. Pass nil for either bound to leave
	// it open.
	ScanCompoundIndex(ctx context.Context, indexName string, prefixValues [][]byte, rangeStart, rangeEnd []byte) (map[string][][]byte, error)
}

// KV is one key-value pair in a guarded multi-key write.
type KV struct {
	Key   string
	Value []byte
}

// GuardedBatchKVStore is a KVStore that can apply several writes atomically,
// conditional on one key still holding an expected value.
//
// It is a CAPABILITY interface, detected by type assertion exactly as
// AtomicKVStore is, because only a store backed by a transaction plane can
// honour it: every engine resolves the compare and the writes inside its state
// machine, in one revision, so replicas cannot diverge and no other writer can
// interleave between the compare and the last write.
//
// The single-key guarded verbs are not a substitute, and the difference is not
// theoretical. A caller that owns a claim and writes three keys one at a time,
// checking ownership before each, still has a window between each check and its
// write: lose the claim inside one of those windows and the write lands anyway,
// on top of the winner's. For an enrolment that means the winner's published
// index pointing at the loser's record — a lost update with no signal
// (go-qrdb#21, and the Codex finding on PR #111). Checking more often narrows
// the window; only evaluating the guard and the writes together closes it.
//
// The guard is deliberately ONE key for the common case, where every write in
// the batch is fenced by a single ownership token. MultiGuardedBatchKVStore
// below generalizes this to more than one independent guard, for the batches
// that must fence on two or more uniqueness indexes at once (go-qrdb#555: a
// user create claims BOTH a generated id and a caller-chosen username, and
// neither guard alone is sufficient — see that interface's doc).
type GuardedBatchKVStore interface {
	KVStore

	// PutAllIfUnchanged applies every write atomically, and only while
	// guardKey still holds guardValue. It reports applied=true when the writes
	// were made, and applied=false with a nil error when the guard did not
	// hold — in which case the store is unchanged. A non-nil error means the
	// outcome is unknown and the caller must not assume either way.
	//
	// An empty writes slice is applied as a no-op that still reports whether
	// the guard held, which is a cheap ownership test with the same atomicity.
	PutAllIfUnchanged(ctx context.Context, guardKey string, guardValue []byte, writes []KV) (applied bool, err error)
}

// Guard is one absent/expected-value condition evaluated as part of a
// PutAllIfAllUnchanged batch. A nil Value means the key must be absent,
// exactly as PutAllIfUnchanged's guardValue does. A non-nil Value means the
// key must be PRESENT and hold exactly Value — so a non-nil empty Value is
// refused by an absent key, never treated as equal to it.
type Guard struct {
	Key   string
	Value []byte
}

// MultiGuardedBatchKVStore generalizes GuardedBatchKVStore to more than one
// independent guard key, evaluated and applied in ONE atomic proposal.
//
// It exists because a single guard cannot express two uniqueness fences at
// once: go-qrdb#555's review found that a boot-time user create guarded only
// on the username key, so a Snowflake id repeated by an independently seeded
// node (the exact failure #555 evidenced for role ids) could overwrite an
// existing primary record and identity claim, since nothing guarded the id
// itself. Sequencing two single-key guarded batches would reopen the same
// window this whole design exists to close (the leadership-change race
// between two separate proposals), so the primitive is extended instead of
// working around it with a non-atomic fallback.
//
// Every production GuardedBatchKVStore in this repository also implements
// this interface: the underlying transaction plane already accepts a slice of
// compares, so PutAllIfUnchanged is implemented in terms of
// PutAllIfAllUnchanged with a single-element guard list wherever both are
// provided.
type MultiGuardedBatchKVStore interface {
	GuardedBatchKVStore

	// PutAllIfAllUnchanged applies every write atomically, and only while
	// EVERY guard's key still holds its expected value. The applied/err
	// contract matches PutAllIfUnchanged: applied=true when the writes were
	// made, applied=false with a nil error when at least one guard did not
	// hold (the store is unchanged), and a non-nil error means the outcome is
	// unknown.
	PutAllIfAllUnchanged(ctx context.Context, guards []Guard, writes []KV) (applied bool, err error)
}

// AtomicKVStore is the optional capability of performing a write whose outcome
// is defined under concurrency.
//
// It is a separate interface, discovered by type assertion, rather than a
// method on KVStore. Every KVStore would otherwise have to grow it, and the
// implementations do not all sit somewhere that can honour it: the Raft-backed
// service store would need a new consensus command, while a barrier-backed or
// partition-backed store already has the primitive one call away. Forcing the
// method everywhere would mean stubs that either lie about atomicity or return
// "unsupported" — and a caller cannot tell those apart from a real refusal.
//
// Callers that need exactly-once semantics assert for this and fail closed when
// it is absent:
//
//	atomic, ok := store.(kvstore.AtomicKVStore)
//	if !ok {
//	    return ErrAtomicWriteUnsupported
//	}
//
// A Get followed by a Put is NOT a substitute. Two callers can both observe
// "absent" in the window between the two operations and both write, which for a
// single-use token means it is redeemed twice.
type AtomicKVStore interface {
	KVStore

	// PutIfAbsent atomically stores value under key only if key does not
	// already exist. It reports stored=true when this call created the key and
	// stored=false, with a nil error, when the key was already present — in
	// which case the store is unchanged. A non-nil error means the outcome is
	// unknown and the caller must not assume either way.
	//
	// Atomicity is per consistency domain: a single-process store serialises
	// the check-then-write internally, and a replicated store must use its
	// native compare-and-set so the guarantee holds across nodes.
	PutIfAbsent(ctx context.Context, key string, value []byte) (stored bool, err error)

	// CompareAndDelete atomically removes key only when its stored value
	// still equals expected. It reports deleted=true when this call removed
	// the key, and deleted=false with a nil error when the key was absent or
	// held a different value — in which case the store is unchanged. A
	// non-nil error means the outcome is unknown and the caller must not
	// assume either way.
	//
	// It is the release half of PutIfAbsent, and exists because a guarded
	// create paired with an UNGUARDED delete is half a primitive. Read the
	// value, compare it, then delete, and two releasers both observe the same
	// holder: the first deletes, a third party legitimately takes the
	// now-absent key with PutIfAbsent, and the second delete removes THEIR
	// value (go-qrdb#41). No amount of re-reading closes that window, because
	// the window is between the read and the delete.
	//
	// Atomicity is per consistency domain, exactly as for PutIfAbsent: a
	// single-process store serialises the compare-then-delete internally, and
	// a replicated store must evaluate it inside its state machine so every
	// replica decides identically from the same log position.
	CompareAndDelete(ctx context.Context, key string, expected []byte) (deleted bool, err error)

	// CompareAndSwap atomically replaces key's value with next only when its
	// stored value still equals expected. It reports swapped=true when this
	// call replaced the value, and swapped=false with a nil error when the key
	// was absent or held a different value — in which case the store is
	// unchanged. A non-nil error means the outcome is unknown and the caller
	// must not assume either way.
	//
	// An absent key is NOT created. That is the distinction from PutIfAbsent,
	// and it is deliberate: a swap says "this record should move from A to B",
	// which is not a statement anyone can make about a record that does not
	// exist.
	//
	// It completes what PutIfAbsent and CompareAndDelete began. A guarded
	// create and a guarded release with no guarded EDIT between them is two
	// thirds of a primitive: the endpoints are decided against replicated
	// state and the transition in the middle is decided nowhere. Composing one
	// from the existing pair does not work — CompareAndDelete followed by
	// PutIfAbsent leaves the key briefly ABSENT, so readers see "not found"
	// for a live record, and it reopens the delete-then-recreate window that
	// lets a stalled contender take the key as a fresh create (go-qrdb#56).
	//
	// Atomicity is per consistency domain, exactly as for the other two: a
	// single-process store serialises the compare-then-write internally, and a
	// replicated store must evaluate it inside its state machine so every
	// replica decides identically from the same log position.
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (swapped bool, err error)
}

// FencedCreator is the optional capability of creating a key only while it
// AND a set of other keys -- its fences -- are all absent, decided as one
// atomic step.
//
// It exists for a create that another operation retires through a DIFFERENT
// key. A tenant id is retired by a tombstone written beside, not over, the
// tenant's metadata record; PutIfAbsent on the metadata key alone cannot see
// the tombstone, and checking the tombstone first and then PutIfAbsent-ing
// leaves the window between the two open: a delete that writes the tombstone
// and removes the metadata inside it lets the create commit a live record for
// an id retired forever (go-qrdb#484). Evaluating every absence and the write
// together is the only thing that closes it.
//
// It is a separate interface, discovered by type assertion like AtomicKVStore
// and GuardedBatchKVStore, and deliberately NOT embedding KVStore: both
// kvstore.KVStore and seal.StorageBackend implementations carry it, and a
// caller that needs it asserts for it and fails closed when it is absent. A
// store that offered it by reading each fence and then writing would be
// reporting an atomicity it does not have.
type FencedCreator interface {
	// PutIfAllAbsent stores value under key only when key and every key in
	// fences are absent. It reports created=true when this call wrote the
	// key, and created=false with a nil error when any of them was present
	// -- in which case the store is unchanged. A non-nil error means the
	// outcome is unknown and the caller must not assume either way.
	//
	// Atomicity is per consistency domain, exactly as for PutIfAbsent: a
	// single-process store evaluates every absence and the write under one
	// lock, and a replicated store evaluates them inside its state machine so
	// every replica decides identically from the same log position.
	PutIfAllAbsent(ctx context.Context, key string, value []byte, fences ...string) (created bool, err error)
}
