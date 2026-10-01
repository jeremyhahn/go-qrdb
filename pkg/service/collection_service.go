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

package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/codec"
	"github.com/jeremyhahn/go-qrdb/pkg/consistency"
	"github.com/jeremyhahn/go-qrdb/pkg/dao"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/quota"
	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
	"github.com/jeremyhahn/go-qrdb/pkg/writemode"
)

// CollectionServicer defines CRUD operations for document collections.
type CollectionServicer interface {
	// CreateCollection creates a new named collection in the given database.
	CreateCollection(ctx context.Context, req *CreateCollectionRequest) (*CollectionInfo, error)

	// DropCollection removes a collection and all its documents.
	DropCollection(ctx context.Context, req *DropCollectionRequest) error

	// ListCollections returns all collections in the given database.
	ListCollections(ctx context.Context, req *ListCollectionsRequest) (*ListCollectionsResponse, error)

	// GetCollectionInfo returns metadata about a specific collection.
	GetCollectionInfo(ctx context.Context, database, name string) (*CollectionInfo, error)

	// InsertDocument inserts a document into a collection.
	InsertDocument(ctx context.Context, req *InsertDocumentRequest) (*InsertDocumentResponse, error)

	// GetDocument retrieves a document by ID from a collection.
	GetDocument(ctx context.Context, req *GetDocumentRequest) (*GetDocumentResponse, error)

	// UpdateDocument updates an existing document in a collection.
	UpdateDocument(ctx context.Context, req *UpdateDocumentRequest) (*UpdateDocumentResponse, error)

	// UpsertDocument inserts a document under an explicit ID, or replaces the
	// one stored there, deciding which in the same guarded write that stores
	// it (go-qrdb#497).
	UpsertDocument(ctx context.Context, req *UpsertDocumentRequest) (*UpsertDocumentResponse, error)

	// DeleteDocument removes a document from a collection.
	DeleteDocument(ctx context.Context, req *DeleteDocumentRequest) (*DeleteDocumentResponse, error)

	// ListDocuments lists documents in a collection with pagination.
	ListDocuments(ctx context.Context, req *ListDocumentsRequest) (*ListDocumentsResponse, error)
}

// CollectionSweeper finishes collections an interrupted operation left in a
// transitional state. It is implemented by CollectionService and driven
// periodically by the server's collection sweeper, so an operator never has
// to find and re-issue a stuck drop by hand (go-qrdb#425).
type CollectionSweeper interface {
	// SweepStuckCollections finishes every collection in database that has
	// been CREATING or DROPPING for at least staleAfter, and reports how many
	// it finished and the document body bytes it removed doing so. The
	// result is meaningful when an error is also returned: it counts what was
	// finished and removed before and around the failures.
	SweepStuckCollections(ctx context.Context, database string, staleAfter time.Duration) (CollectionSweepResult, error)
}

// CollectionSweepResult is what one SweepStuckCollections call finished.
type CollectionSweepResult struct {
	// Swept is how many collections and orphaned index sets were finished.
	Swept int
	// FreedBytes is the document body bytes (DocumentBodyBytes) the sweep's
	// teardowns removed -- exactly the documents each removed itself, so a
	// storage quota can release them without releasing anything twice.
	FreedBytes int64
}

// CollectionDropMeter is a collection surface whose drop reports what it
// removed. A storage quota drops through it so it releases exactly the bytes
// the drop freed (go-qrdb#456): a measurement taken before the drop would
// also count documents a concurrent delete had already released.
type CollectionDropMeter interface {
	// DropCollectionMetered is DropCollection, returning the document body
	// bytes (DocumentBodyBytes) of every document the drop itself removed.
	// A drop that fails part-way still returns what it removed before it
	// failed.
	DropCollectionMetered(ctx context.Context, req *DropCollectionRequest) (int64, error)
}

// CollectionService implements CollectionServicer using the KV service
// layer for document and metadata storage, with DAO IDGenerators for
// document ID generation.
type CollectionService struct {
	kvSvc         kvService
	indexSvc      indexService                 // optional; nil when indexes are not available
	idGens        sync.Map                     // map[string]dao.IDGenerator keyed by collectionIDGenKey
	dbCodecLookup func(database string) string // optional; returns per-database default codec name
	quotaChecker  quota.Checker                // optional; nil skips quota enforcement
	seqSvc        SequenceServicer             // optional; see WithSequenceService
	journal       CollectionWorkJournal        // optional; see WithWorkJournal
	now           func() time.Time             // lifecycle-state clock; time.Now outside tests
}

// kvService is the minimal KV interface needed by CollectionService.
// It matches the subset of KVServicer used for collection operations.
//
// No List-only variant of the consistency reads is declared: every read
// CollectionService performs -- GetDocument, ListDocuments -- needs the
// document VALUES, never a keys-only listing, so GetWithConsistency and
// ScanWithConsistency are the pair actually reachable from this service's
// methods (go-qrdb#271: a prior ListWithConsistency here had zero callers,
// confirmed by tracing every kvSvc.* call site in this file -- ListDocuments
// always calls Scan/ScanWithConsistency for the values, never List).
//
// The guarded pair CompareAndSwap / CompareAndDelete is what the lifecycle
// state is written with: every metadata transition is a swap of the exact
// bytes the transition was decided from, so a create's activation cannot
// resurrect a collection a drop has marked, and a schema update cannot revert
// a drop's DROPPING mark (go-qrdb#415, go-qrdb#425).
type kvService interface {
	Put(ctx context.Context, key string, value []byte) error
	PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error)
	CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error)
	CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error)
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
	PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error
	DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error
	GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error)
	ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error)
}

// indexService is the minimal index interface needed by CollectionService
// to create and drop secondary indexes on collection fields.
//
// CreateIndex carries the collection as the index's recorded owner, and the
// apply path refuses the create when another owner already holds the name
// (index.OwnershipError), so a collection never adopts an index it did not
// create. DropOwnedIndex removes an index only when the claimed owner holds
// it (go-qrdb#426); a collection whose metadata predates ownership claims
// index.ExplicitOwner, the owner its ownerless indexes were created with
// (CollectionInfo.IndexOwner). No collection drop is unconditional.
//
// A drop is issued during teardown, where "the index is not there" is the
// outcome being asked for. The canonical index.IndexServicer contract permits
// an implementation to report that as IndexNotFoundError -- the shipped
// IndexService does not, because the state machine's drop handler is
// idempotent so a cleanup proposal cannot stall Raft -- so the teardown
// accepts EITHER answer as success. Any other error fails the drop.
type indexService interface {
	CreateIndex(ctx context.Context, name, fieldName string, config index.IndexConfig) error
	DropOwnedIndex(ctx context.Context, name, owner string) error
}

// indexAlreadyGone reports whether an error from an index drop means the index
// was not there to drop, which is success for a teardown.
func indexAlreadyGone(err error) bool {
	var notFound *IndexNotFoundError
	return errors.As(err, &notFound)
}

// indexOwnedElsewhere reports whether an error from an owner-checked index
// drop means the index under that name belongs to somebody else. A teardown
// leaves such an index alone: it is not the collection's to remove.
func indexOwnedElsewhere(err error) bool {
	var own *index.OwnershipError
	return errors.As(err, &own)
}

// Compile-time interface checks.
var (
	_ CollectionServicer  = (*CollectionService)(nil)
	_ CollectionSweeper   = (*CollectionService)(nil)
	_ CollectionDropMeter = (*CollectionService)(nil)
)

// metadataCASMaxAttempts bounds the read-and-swap loops that write collection
// metadata. A swap fails only when another writer changed the record between
// the read and the write -- a schema merge from a concurrent insert, a
// lifecycle transition -- so a handful of re-reads converges; running out is
// reported rather than looped on forever.
const metadataCASMaxAttempts = 8

// upsertMaxAttempts bounds the guarded upsert's decide-and-write loop. Each
// retry means a concurrent writer changed the document between this call's
// read and its guarded write; the bound turns sustained contention into
// CollectionDocumentConflictError instead of an unbounded spin.
const upsertMaxAttempts = 8

// NewCollectionService creates a new CollectionService backed by the
// given KV service for storage.
func NewCollectionService(kvSvc kvService) *CollectionService {
	return &CollectionService{kvSvc: kvSvc, now: time.Now}
}

// NewCollectionServiceWithIndex creates a new CollectionService backed
// by the given KV service for storage and an index service for managing
// secondary indexes on collection fields.
func NewCollectionServiceWithIndex(kvSvc kvService, indexSvc indexService) *CollectionService {
	return &CollectionService{
		kvSvc:    kvSvc,
		indexSvc: indexSvc,
		now:      time.Now,
	}
}

// WithDatabaseCodecLookup configures the CollectionService with a callback that
// resolves the default storage codec for a given database and returns the SAME
// service for chaining -- it mutates the receiver rather than copying it, like
// the two options below. (The comment here claimed a copy; nothing copied.)
// When the callback returns a non-empty string it is used as the fallback when
// a collection has no per-collection StorageCodec set.
func (s *CollectionService) WithDatabaseCodecLookup(fn func(database string) string) *CollectionService {
	s.dbCodecLookup = fn
	return s
}

// WithCollectionQuotaChecker sets the quota.Checker used to enforce collection
// limits before creation. Passing nil disables quota enforcement.
func (s *CollectionService) WithCollectionQuotaChecker(checker quota.Checker) *CollectionService {
	s.quotaChecker = checker
	return s
}

// WithSequenceService routes IDStrategyIncrement document ID generation
// through a SequenceServicer instead of the legacy process-local
// dao.AtomicIncrementGenerator.
//
// Without this, IDStrategyIncrement is generated by an in-memory counter
// that starts at 0 on every process restart and is independent on every
// node -- it produces duplicate document IDs across a restart or across
// replicas of the same collection. SequenceServicer allocates from a
// Raft-replicated counter advanced by compare-and-swap (see
// SequenceService), so a value it hands out is never handed out twice, at
// the cost of a Raft round-trip per document insert using this strategy
// instead of an in-process atomic increment. See nextDocumentID.
func (s *CollectionService) WithSequenceService(seq SequenceServicer) *CollectionService {
	s.seqSvc = seq
	return s
}

// CollectionWorkJournal records, somewhere a sweeper can enumerate without
// knowing which databases exist, that a collection is about to enter a
// transitional state (creating or dropping) and may need finishing.
//
// A collection's own metadata cannot serve: it is keyed inside its
// database's keyspace, and the set of databases is not replicated for every
// isolation mode -- a prefix tenant's named database is in the shared,
// replicated store while its catalog entry is on one node only. The journal
// is written into the replicated store the collection lives in, outside any
// database's keyspace, so the node that leads that store finds the work
// whichever node started it.
//
// Record is called before the transition is written, so a crash after the
// transition always leaves an entry. An entry outliving its collection's
// transition is harmless: the sweeper removes an entry once the collection
// it names is active or gone.
type CollectionWorkJournal interface {
	Record(ctx context.Context, database, name string) error
}

// WithWorkJournal sets the journal a transition into creating or dropping is
// recorded in (see CollectionWorkJournal). Without one, nothing is recorded,
// and the collection's database must be enumerable by the sweeper some other
// way.
func (s *CollectionService) WithWorkJournal(j CollectionWorkJournal) *CollectionService {
	s.journal = j
	return s
}

// recordWork records that database/name is about to enter a transitional
// state. A journal that cannot be written fails the operation: a transition
// the sweeper cannot find is one nobody finishes.
func (s *CollectionService) recordWork(ctx context.Context, database, name string) error {
	if s.journal == nil {
		return nil
	}
	if err := s.journal.Record(ctx, database, name); err != nil {
		return &CollectionMetadataError{Op: "journal", Err: err}
	}
	return nil
}

// clock returns the lifecycle-state clock.
func (s *CollectionService) clock() time.Time {
	if s.now == nil {
		return time.Now()
	}
	return s.now()
}

// CreateCollection creates a new collection with the specified
// configuration. The collection metadata is stored under
// __sys:collections/{database}/{name} and documents are stored under
// __col:{database}:{collection}#{incarnation}/{id}.
//
// The metadata is written FIRST, in the CREATING state, and made ACTIVE LAST,
// once every declared secondary index exists (go-qrdb#415). The first write is
// a PutIfAbsent, so it is also the claim on the name: two concurrent creates
// of one name give one winner and one CollectionAlreadyExistsError, and the
// loser builds nothing. While the collection is CREATING it is reported by
// GetCollectionInfo and ListCollections with its state and serves no
// documents, so no caller can write a document its declared indexes -- and
// their unique constraints -- do not see.
//
// Every create is a new INCARNATION of the name (CollectionInfo.Incarnation),
// and every piece of per-incarnation state lives in a keyspace named by it:
// the documents (CollectionInfo.documentPrefix), the ID sequence
// (collectionSequenceName), the process-local ID generator
// (collectionIDGenKey) and the secondary indexes, whose owner and key prefix
// both name it. Only the metadata key is shared by every incarnation of a
// name. A writer, create or teardown still working on an incarnation that has
// since been dropped and created again therefore reaches only the dead
// incarnation's keyspace: nothing it does can touch the live one, and the new
// incarnation starts from a keyspace no earlier one ever wrote.
//
// A create whose indexes cannot all be built unwinds what it built: it marks
// the collection DROPPING and tears it down through the same teardown a drop
// uses, so a failed create has created nothing and the name is free for a
// retry. A create interrupted before it can unwind -- a crash, or an unwind
// that itself fails -- leaves the collection CREATING or DROPPING, and the
// sweeper (SweepStuckCollections) finishes it once it is stale.
//
// Every index is created with this incarnation as its recorded owner
// (index.CollectionOwner). A name another owner already holds -- an index made
// through the explicit index API, or another collection's under a colliding
// "{database}.{collection}.{field}" name -- is refused by the apply path
// instead of being silently adopted (go-qrdb#426), and the create fails.
// Before building any index the create records the owner and the names it is
// about to build in an index ledger (collectionIndexLedgerPrefix), so an index
// orphaned by a creator that dies mid-way can still be found and removed by
// the sweeper once no metadata names its incarnation.
func (s *CollectionService) CreateCollection(ctx context.Context, req *CreateCollectionRequest) (*CollectionInfo, error) {
	if err := validateCollectionRequest(req.Database, req.Name); err != nil {
		return nil, err
	}
	if strings.Contains(req.Name, incarnationSeparator) {
		return nil, &CollectionInvalidNameError{Name: req.Name, Reason: "must not contain " + strconv.Quote(incarnationSeparator)}
	}
	if err := s.validateIndexDefinitions(req.Indexes); err != nil {
		return nil, err
	}

	if s.quotaChecker != nil {
		tenantID := tenant.FromContext(ctx)
		if err := s.quotaChecker.Check(ctx, tenantID, quota.ResourceCollections); err != nil {
			return nil, err
		}
	}

	strategy := req.IDStrategy
	if strategy == "" {
		strategy = IDStrategyUnixNano
	}

	if err := validateIDStrategy(strategy, req.FieldHash); err != nil {
		return nil, err
	}

	// Validate the per-collection codec when specified.
	if req.StorageCodec != "" {
		if _, err := codec.ResolveCodec(req.StorageCodec); err != nil {
			return nil, &CollectionInvalidCodecError{Codec: req.StorageCodec}
		}
	}

	incarnation, err := newCollectionIncarnation()
	if err != nil {
		return nil, &CollectionMetadataError{Op: "incarnation", Err: err}
	}
	now := s.clock()
	info := &CollectionInfo{
		Name:         req.Name,
		Database:     req.Database,
		IDStrategy:   strategy,
		FieldHash:    req.FieldHash,
		Schema:       make(map[string]string),
		Indexes:      req.Indexes,
		StorageCodec: req.StorageCodec,
		CreatedAt:    now,
		State:        CollectionStateCreating,
		StateSince:   now,
		Incarnation:  incarnation,
	}
	if len(req.Indexes) > 0 {
		info.IndexOwner = index.CollectionOwner(req.Database, req.Name, incarnation)
	}

	data, err := json.Marshal(info)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal", Err: err}
	}

	if err := s.recordWork(ctx, req.Database, req.Name); err != nil {
		return nil, err
	}
	metaKey := collectionMetadataKey(req.Database, req.Name)
	stored, err := s.kvSvc.PutIfAbsent(ctx, metaKey, data)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "save", Err: err}
	}
	if !stored {
		return nil, &CollectionAlreadyExistsError{
			Database:   req.Database,
			Collection: req.Name,
		}
	}

	if err := s.writeIndexLedger(ctx, info); err != nil {
		return nil, s.abandonCreate(ctx, info, data, err)
	}
	if err := s.createCollectionIndexes(ctx, info); err != nil {
		return nil, s.abandonCreate(ctx, info, data, err)
	}

	active := *info
	active.State = CollectionStateActive
	active.StateSince = s.clock()
	activeData, err := json.Marshal(&active)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal", Err: err}
	}
	// A swap, not a Put: if the collection was marked DROPPING while its
	// indexes were being built -- by a user's drop, or by the sweeper finding
	// it stale -- this activation must not resurrect it.
	swapped, err := s.kvSvc.CompareAndSwap(ctx, metaKey, data, activeData)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "activate", Err: err}
	}
	if !swapped {
		// The record was marked DROPPING -- and possibly already torn down
		// -- while the indexes were being built, so the teardown may have
		// run before some of them existed. This incarnation removes what it
		// built; the owner-checked drop cannot touch anything a later
		// incarnation of the name builds.
		conflict := &CollectionStateConflictError{Database: req.Database, Collection: req.Name, Op: "activate"}
		if err := s.releaseOwnIndexes(ctx, info); err != nil {
			return nil, errors.Join(conflict, err)
		}
		return nil, conflict
	}

	// Initialize the process-local ID generator for this collection, unless
	// IDStrategyIncrement is routed through SequenceServicer instead (see
	// nextDocumentID), in which case there is nothing to prime: the
	// sequence's state lives in the replicated store, not in this process.
	if strategy != IDStrategyIncrement || s.seqSvc == nil {
		s.getOrCreateIDGen(&active)
	}

	return &active, nil
}

// validateIndexDefinitions refuses index definitions the collection cannot
// build: one naming no field, two naming the same field -- they derive one
// index name, so the second would silently be the first -- or any at all when
// this service has no index service to build them with. The last used to be
// skipped silently, which left a collection whose metadata declared indexes --
// and unique constraints -- that did not exist.
func (s *CollectionService) validateIndexDefinitions(defs []IndexDefinition) error {
	if len(defs) == 0 {
		return nil
	}
	if s.indexSvc == nil {
		return &CollectionInvalidIndexError{Reason: "secondary indexes are not available on this database"}
	}
	seen := make(map[string]bool, len(defs))
	for _, def := range defs {
		if def.FieldName == "" {
			return &CollectionInvalidIndexError{Reason: "field_name is required"}
		}
		if seen[def.FieldName] {
			return &CollectionInvalidIndexError{Reason: "field " + strconv.Quote(def.FieldName) + " is declared more than once"}
		}
		seen[def.FieldName] = true
	}
	return nil
}

// createCollectionIndexes builds every secondary index info declares, each
// recorded as owned by the collection's incarnation.
func (s *CollectionService) createCollectionIndexes(ctx context.Context, info *CollectionInfo) error {
	if s.indexSvc == nil {
		return nil
	}
	for _, def := range info.Indexes {
		idxName := collectionIndexName(info.Database, info.Name, def.FieldName)
		cfg := index.IndexConfig{
			Name:   idxName,
			Unique: def.Unique,
			Sparse: def.Sparse,
			// This incarnation's own document keyspace, and the only thing
			// this index may index. The index NAME says "{database}.
			// {collection}.{field}" but a name is a routing label, not a
			// key predicate: without this the apply path indexed every
			// JSON value in the state machine, another database's and
			// another tenant's included (go-qrdb#410).
			KeyPrefix: info.documentPrefix(),
			// A collection index definition has no case-sensitivity
			// field, so it keeps the behaviour it has always had. Stated
			// explicitly because index.IndexConfig names the field for the
			// DEFAULT and its zero value is case-INSENSITIVE, which the
			// engine now honours instead of hardcoding true (go-qrdb#34).
			CaseSensitive: true,
			// This incarnation of the collection is the index's recorded
			// owner, so a name some other owner holds is refused instead of
			// adopted (go-qrdb#426).
			Owner: info.IndexOwner,
		}
		if err := s.indexSvc.CreateIndex(ctx, idxName, def.FieldName, cfg); err != nil {
			return err
		}
	}
	return nil
}

// abandonCreate unwinds a create whose setup failed, and returns the error the
// create reports.
//
// The unwind is the drop's own teardown, entered by marking the collection
// DROPPING: the indexes built so far are removed (owner-checked, so an index
// the refusal was ABOUT -- somebody else's -- is left alone), then the
// metadata. A failed create has then created nothing and a retry succeeds.
//
// When the mark loses its swap, somebody else -- a drop, or the sweeper --
// already moved the record on, and its teardown may have run before this
// create built some of its indexes; this incarnation removes its own. If the
// unwind cannot finish, the collection is left CREATING or DROPPING for the
// sweeper; the create still reports the original failure, joined with the
// unwind's.
func (s *CollectionService) abandonCreate(ctx context.Context, info *CollectionInfo, raw []byte, cause error) error {
	fail := func(err error) error {
		return &CollectionMetadataError{Op: "create-index", Err: errors.Join(cause, &CollectionMetadataError{Op: "rollback", Err: err})}
	}
	dropping := *info
	dropping.State = CollectionStateDropping
	dropping.StateSince = s.clock()
	data, err := json.Marshal(&dropping)
	if err != nil {
		return fail(err)
	}
	metaKey := collectionMetadataKey(info.Database, info.Name)
	swapped, err := s.kvSvc.CompareAndSwap(ctx, metaKey, raw, data)
	if err != nil {
		return fail(err)
	}
	if !swapped {
		if err := s.releaseOwnIndexes(ctx, info); err != nil {
			return fail(err)
		}
		return &CollectionMetadataError{Op: "create-index", Err: cause}
	}
	// A create's keyspace never had a writer -- the collection never served
	// -- so there is nothing for its teardown to free.
	if _, err := s.teardownCollection(ctx, &dropping, data); err != nil {
		return fail(err)
	}
	return &CollectionMetadataError{Op: "create-index", Err: cause}
}

// releaseOwnIndexes removes the indexes this incarnation created, and its
// ledger entry, when the record naming them is no longer this create's to
// tear down.
func (s *CollectionService) releaseOwnIndexes(ctx context.Context, info *CollectionInfo) error {
	if info.IndexOwner == "" {
		return nil
	}
	if err := s.dropCollectionIndexes(ctx, info); err != nil {
		return err
	}
	return s.deleteIndexLedger(ctx, info)
}

// DropCollection removes a collection: every document in it, every secondary
// index CreateCollection built for it, the replicated ID sequence backing
// IDStrategyIncrement, the process-local ID generator cache entry, and finally
// the metadata that makes the collection resolvable.
//
// It first marks the collection DROPPING -- a guarded swap of the metadata --
// and only then tears anything down (go-qrdb#425). From that write on, every
// document verb refuses the collection with CollectionNotActiveError, so no
// write can land in a collection whose indexes, and therefore whose unique
// constraints, are being removed. GetCollectionInfo and ListCollections
// report it with its DROPPING state: a crashed drop is visibly incomplete
// rather than silently degraded, and the sweeper (SweepStuckCollections)
// finishes it without an operator having to re-issue it. Re-issuing it by
// hand also works; every step is idempotent.
//
// The teardown order -- documents, indexes, sequence, metadata -- is
// deliberate and mirrors Database.DropTable in pkg/sql/provider: dependent
// structures are reclaimed FIRST and the metadata key is deleted LAST, so the
// metadata key is the drop's own commit record and every intermediate state is
// one a re-issued or swept drop completes. Documents go first because each
// delete retracts that document's entries from every index maintained over it.
//
// Everything the teardown reclaims lives in the dropped incarnation's own
// keyspace (see CreateCollection), so a teardown that resumes after the name
// was dropped by someone else and created again reclaims only what the dead
// incarnation owned, and the new incarnation's documents, counter and indexes
// are out of its reach by construction.
//
// A dependent that cannot be reclaimed FAILS the whole drop rather than being
// skipped: reporting success over an unreclaimed dependent is what produced
// go-qrdb#399 and go-qrdb#411. A dependent reported as already absent is
// success, which is what lets a retry reach the metadata delete.
//
// Only the indexes the collection OWNS are removed (go-qrdb#426). They are
// dropped with an owner-checked drop, so an index under one of the
// collection's derived names that belongs to somebody else is left in place.
// Metadata written before ownership existed (IndexOwner empty) names indexes
// that were created ownerless, and those are dropped claiming the explicit
// owner they were created with.
func (s *CollectionService) DropCollection(ctx context.Context, req *DropCollectionRequest) error {
	_, err := s.DropCollectionMetered(ctx, req)
	return err
}

// DropCollectionMetered is DropCollection, reporting the document body bytes
// the drop removed. See CollectionDropMeter.
func (s *CollectionService) DropCollectionMetered(ctx context.Context, req *DropCollectionRequest) (int64, error) {
	if err := validateCollectionRequest(req.Database, req.Name); err != nil {
		return 0, err
	}

	// The metadata carries the collection's index definitions, so the drop
	// reads it rather than merely probing for its existence: a drop that
	// cannot enumerate what the collection owns cannot finish correctly.
	// Unparseable metadata therefore fails the drop with a typed error
	// instead of half-dropping the collection.
	info, raw, err := s.markDropping(ctx, req.Database, req.Name)
	if err != nil {
		return 0, err
	}
	return s.teardownCollection(ctx, info, raw)
}

// markDropping moves a collection into the DROPPING state and returns its
// metadata as marked, with the exact stored bytes of the mark. A collection
// already DROPPING is returned as it is, so a re-issued drop resumes the
// teardown.
func (s *CollectionService) markDropping(ctx context.Context, database, name string) (*CollectionInfo, []byte, error) {
	metaKey := collectionMetadataKey(database, name)
	recorded := false
	for attempt := 0; attempt < metadataCASMaxAttempts; attempt++ {
		info, raw, err := s.loadCollectionRecord(ctx, database, name)
		if err != nil {
			return nil, nil, err
		}
		if info.State == CollectionStateDropping {
			return info, raw, nil
		}
		if !recorded {
			if err := s.recordWork(ctx, database, name); err != nil {
				return nil, nil, err
			}
			recorded = true
		}
		info.State = CollectionStateDropping
		info.StateSince = s.clock()
		data, err := json.Marshal(info)
		if err != nil {
			return nil, nil, &CollectionMetadataError{Op: "marshal", Err: err}
		}
		swapped, err := s.kvSvc.CompareAndSwap(ctx, metaKey, raw, data)
		if err != nil {
			return nil, nil, &CollectionMetadataError{Op: "mark-dropping", Err: err}
		}
		if swapped {
			return info, data, nil
		}
	}
	return nil, nil, &CollectionStateConflictError{Database: database, Collection: name, Op: "mark-dropping"}
}

// teardownMaxPasses bounds how many times a teardown re-scans its keyspace for
// documents written into it after its scan. Only a writer that passed its
// ACTIVE check before the DROPPING mark can still write there, and its own
// fence retracts what it wrote, so a second pass is already rare.
const teardownMaxPasses = 4

// teardownCollection reclaims everything one DROPPING incarnation of a
// collection owns, and returns the document body bytes it removed.
//
// Every piece of that state is scoped to the incarnation -- its document
// prefix, its ID sequence, its indexes' owner -- so the teardown can never
// reach another incarnation's data, however late it runs: a teardown that
// resumes after the name was dropped by someone else and created again
// reclaims only the dead incarnation's keyspace, which nothing live shares.
// That is why the reclamation needs no guard against another incarnation: no
// re-read of the metadata between batches, no check that the record is still
// the one it started from, and no ordering of the keys.
//
// The metadata key is the one thing incarnations share -- it is keyed by the
// name -- so it alone is deleted with a compare-and-delete of raw, the exact
// DROPPING record the teardown started from. A newer incarnation's record is
// never removed; the teardown then reports CollectionStateConflictError, which
// the sweeper treats as a drop somebody else finished.
func (s *CollectionService) teardownCollection(ctx context.Context, info *CollectionInfo, raw []byte) (int64, error) {
	freed, err := s.reclaimDocuments(ctx, info)
	if err != nil {
		return freed, err
	}
	if err := s.dropCollectionIndexes(ctx, info); err != nil {
		return freed, err
	}
	if err := s.deleteIndexLedger(ctx, info); err != nil {
		return freed, err
	}

	// Reclaim the incarnation's replicated ID sequence. Issued
	// unconditionally rather than only for IDStrategyIncrement: the name is
	// derived from the incarnation alone, and a collection with no counter
	// costs one read and proposes nothing.
	if s.seqSvc != nil {
		if err := s.seqSvc.Delete(ctx, info.Database, collectionSequenceName(info)); err != nil {
			return freed, &CollectionMetadataError{Op: "delete-sequence", Err: err}
		}
	}

	metaKey := collectionMetadataKey(info.Database, info.Name)
	deleted, err := s.kvSvc.CompareAndDelete(ctx, metaKey, raw)
	if err != nil {
		return freed, &CollectionMetadataError{Op: "delete-metadata", Err: err}
	}
	if !deleted {
		return freed, &CollectionStateConflictError{Database: info.Database, Collection: info.Name, Op: "teardown"}
	}

	s.idGens.Delete(collectionIDGenKey(info))
	return freed, nil
}

// reclaimDocuments deletes every document under a DROPPING incarnation's
// prefix and returns the body bytes of exactly the documents it removed. It
// visits the scanned keys in whatever order the scan produced -- the keyspace
// is the dead incarnation's alone, so no order is needed for safety -- and
// checks its context before every delete, so a caller bounded by a deadline
// -- the sweeper gives each database a bounded share of a pass -- stops
// before its next proposal and a later teardown resumes from what is left.
//
// Each delete is a CompareAndDelete of the value scanned. That is not a guard
// against another incarnation -- none shares the keyspace -- but the only way
// to know that THIS delete removed the document: a guarded DeleteDocument
// decided while the collection was still ACTIVE may remove the same document
// first, and it releases the document's bytes itself. Counting only the
// deletes that removed something is what keeps a storage quota from releasing
// one document twice. It costs the same single proposal a plain delete does.
// A document that changed after the scan is left for the next pass.
func (s *CollectionService) reclaimDocuments(ctx context.Context, info *CollectionInfo) (int64, error) {
	prefix := info.documentPrefix()
	c := s.resolveCodec(info)
	var freed int64
	for pass := 0; pass < teardownMaxPasses; pass++ {
		docs, err := s.kvSvc.Scan(ctx, prefix)
		if err != nil {
			return freed, &CollectionMetadataError{Op: "list-documents", Err: err}
		}
		if len(docs) == 0 {
			return freed, nil
		}
		for key, value := range docs {
			if err := ctx.Err(); err != nil {
				return freed, &CollectionMetadataError{Op: "delete-document", Err: err}
			}
			deleted, err := s.kvSvc.CompareAndDelete(ctx, key, value)
			if err != nil {
				return freed, &CollectionMetadataError{Op: "delete-document", Err: err}
			}
			if deleted {
				freed += storedDocumentBodyBytes(value, c)
			}
		}
	}
	return freed, &CollectionMetadataError{Op: "delete-document", Err: &CollectionDocumentsChangingError{Database: info.Database, Collection: info.Name}}
}

// storedDocumentBodyBytes measures a stored document the way a write is
// charged: the JSON encoding of its fields (see DocumentBodyBytes). A document
// that cannot be decoded is measured as its stored size.
func storedDocumentBodyBytes(stored []byte, c codec.Codec) int64 {
	var entity dao.DynamicEntity
	if err := unmarshalDocument(stored, &entity, c); err != nil {
		return int64(len(stored))
	}
	return DocumentBodyBytes(entity.Fields)
}

// DocumentBodyBytes is the byte cost of a document for storage quota: the
// JSON encoding of its fields, the "_id" identity key excluded. Writes are
// charged, and reclamations released, by this one measure.
func DocumentBodyBytes(doc map[string]interface{}) int64 {
	if len(doc) == 0 {
		return 0
	}
	data, err := json.Marshal(documentFields(doc))
	if err != nil {
		return 0
	}
	return int64(len(data))
}

// dropCollectionIndexes removes the secondary indexes the collection's
// metadata declares. Every drop is owner-checked: an incarnation that records
// an owner drops as that owner; metadata written before ownership existed
// names indexes that were created ownerless, so it drops as the explicit
// owner. Either way an index somebody else now holds under one of the names
// is left in place.
func (s *CollectionService) dropCollectionIndexes(ctx context.Context, info *CollectionInfo) error {
	if s.indexSvc == nil {
		return nil
	}
	owner := info.IndexOwner
	if owner == "" {
		owner = index.ExplicitOwner
	}
	for _, def := range info.Indexes {
		idxName := collectionIndexName(info.Database, info.Name, def.FieldName)
		err := s.indexSvc.DropOwnedIndex(ctx, idxName, owner)
		if err == nil || indexAlreadyGone(err) || indexOwnedElsewhere(err) {
			continue
		}
		return &CollectionMetadataError{Op: "drop-index", Err: err}
	}
	return nil
}

// newCollectionIncarnation returns a fresh identifier for one incarnation of
// a collection (see CollectionInfo.Incarnation).
func newCollectionIncarnation() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// collectionIndexLedger is the durable record of the indexes one collection
// incarnation is about to create, written before the first of them. It is what
// lets the sweeper find an index whose creator died after building it and
// after its metadata was removed: nothing else names such an index's owner.
type collectionIndexLedger struct {
	Database    string    `json:"database"`
	Name        string    `json:"name"`
	Incarnation string    `json:"incarnation"`
	Owner       string    `json:"owner"`
	Indexes     []string  `json:"indexes"`
	CreatedAt   time.Time `json:"created_at"`
}

// collectionIndexLedgerKey returns the ledger key of one incarnation's
// indexes.
func collectionIndexLedgerKey(database, incarnation string) string {
	return collectionIndexLedgerPrefix + database + "/" + incarnation
}

// writeIndexLedger records the owner and the index names a create is about to
// build. A collection with no indexes has nothing to record.
func (s *CollectionService) writeIndexLedger(ctx context.Context, info *CollectionInfo) error {
	if info.IndexOwner == "" || s.indexSvc == nil {
		return nil
	}
	entry := collectionIndexLedger{
		Database:    info.Database,
		Name:        info.Name,
		Incarnation: info.Incarnation,
		Owner:       info.IndexOwner,
		CreatedAt:   s.clock(),
	}
	for _, def := range info.Indexes {
		entry.Indexes = append(entry.Indexes, collectionIndexName(info.Database, info.Name, def.FieldName))
	}
	data, err := json.Marshal(&entry)
	if err != nil {
		return &CollectionMetadataError{Op: "index-ledger", Err: err}
	}
	if err := s.kvSvc.Put(ctx, collectionIndexLedgerKey(info.Database, info.Incarnation), data); err != nil {
		return &CollectionMetadataError{Op: "index-ledger", Err: err}
	}
	return nil
}

// deleteIndexLedger removes an incarnation's ledger entry once its indexes are
// gone. Deleting an absent entry is success.
func (s *CollectionService) deleteIndexLedger(ctx context.Context, info *CollectionInfo) error {
	if info.IndexOwner == "" || info.Incarnation == "" {
		return nil
	}
	if err := s.kvSvc.Delete(ctx, collectionIndexLedgerKey(info.Database, info.Incarnation)); err != nil {
		return &CollectionMetadataError{Op: "index-ledger", Err: err}
	}
	return nil
}

// SweepStuckCollections finishes every collection in database that an
// interrupted operation left in a transitional state for at least staleAfter,
// and removes indexes an interrupted create orphaned (go-qrdb#415,
// go-qrdb#425). It reports how many collections and orphaned index sets it
// finished and the document body bytes its teardowns removed.
//
// A DROPPING collection is torn down exactly as a re-issued DropCollection
// would. A CREATING collection is first marked DROPPING -- a guarded swap, so
// a create that is still running loses its activation rather than racing the
// sweeper -- and then torn down: a create that never finished is unwound, not
// completed, because the caller that issued it was never told it succeeded.
// A teardown that finds its record replaced has been finished by somebody
// else, which is not a failure.
//
// An index-ledger entry whose incarnation no metadata names any more is the
// trace of a creator that died after its collection was removed; its indexes
// are dropped, owner-checked against the incarnation, and the entry deleted.
//
// The sweeper acts on a stored record's word alone, with nobody having asked,
// so it first checks that the record is self-consistent: that its key, its
// database and name, its incarnation and the index owner it claims all derive
// from one another (collectionRecordConsistent, ledgerEntryConsistent). A
// record that does not is refused with CollectionInconsistentRecordError and
// left in place for an operator -- it is never torn down, and none of the
// indexes it names is dropped.
//
// staleAfter is what keeps the sweeper off an operation that is merely still
// in progress. Every step it takes is idempotent or guarded, so reaching one
// that is still running costs repeated work, never a wrong result.
//
// A collection that fails to sweep does not stop the others; the failures
// are returned together and the collection is tried again on the next pass.
func (s *CollectionService) SweepStuckCollections(ctx context.Context, database string, staleAfter time.Duration) (CollectionSweepResult, error) {
	var result CollectionSweepResult
	if database == "" {
		return result, &CollectionEmptyDatabaseError{}
	}
	prefix := collectionMetadataPrefix + database + "/"
	records, err := s.kvSvc.Scan(ctx, prefix)
	if err != nil {
		return result, &CollectionMetadataError{Op: "list", Err: err}
	}

	now := s.clock()
	var failures []error
	for key, raw := range records {
		if ctx.Err() != nil {
			failures = append(failures, ctx.Err())
			break
		}
		var info CollectionInfo
		if err := json.Unmarshal(raw, &info); err != nil {
			continue
		}
		normalizeCollectionState(&info)
		if info.State == CollectionStateActive || now.Sub(info.StateSince) < staleAfter {
			continue
		}
		if err := collectionRecordConsistent(key, database, &info); err != nil {
			failures = append(failures, err)
			continue
		}
		record := raw
		if info.State == CollectionStateCreating {
			info.State = CollectionStateDropping
			info.StateSince = now
			data, err := json.Marshal(&info)
			if err != nil {
				failures = append(failures, &CollectionMetadataError{Op: "marshal", Err: err})
				continue
			}
			swapped, err := s.kvSvc.CompareAndSwap(ctx, key, raw, data)
			if err != nil {
				failures = append(failures, &CollectionMetadataError{Op: "mark-dropping", Err: err})
				continue
			}
			if !swapped {
				// The record moved on since the scan; the next pass
				// sees its current state.
				continue
			}
			record = data
		}
		freed, err := s.teardownCollection(ctx, &info, record)
		result.FreedBytes += freed
		if err != nil {
			var moved *CollectionStateConflictError
			if !errors.As(err, &moved) {
				failures = append(failures, err)
			}
			continue
		}
		result.Swept++
	}

	orphans, err := s.sweepOrphanedIndexes(ctx, database, now, staleAfter)
	result.Swept += orphans
	if err != nil {
		failures = append(failures, err)
	}
	return result, errors.Join(failures...)
}

// collectionRecordConsistent reports whether a stored collection record may
// be acted on by the sweeper: it must be stored under the key its own
// database and name derive, in the database being swept, and the index owner
// it claims must be the one its incarnation derives -- none for a record
// without an incarnation or without indexes. A teardown drops indexes as that
// owner, so a record claiming someone else's would have the sweeper remove
// indexes the collection never built.
func collectionRecordConsistent(key, database string, info *CollectionInfo) error {
	inconsistent := func(reason string) error {
		return &CollectionInconsistentRecordError{Database: database, Collection: info.Name, Reason: reason}
	}
	if info.Database != database || info.Name == "" || key != collectionMetadataKey(info.Database, info.Name) {
		return inconsistent("the record is not stored under the key its database and name derive")
	}
	if info.IndexOwner != expectedIndexOwner(info) {
		return inconsistent("the index owner does not derive from the record's incarnation")
	}
	return nil
}

// expectedIndexOwner is the index owner a collection record must carry: the
// owner its incarnation derives when it has both an incarnation and indexes,
// and none otherwise.
func expectedIndexOwner(info *CollectionInfo) string {
	if info.Incarnation == "" || len(info.Indexes) == 0 {
		return ""
	}
	return index.CollectionOwner(info.Database, info.Name, info.Incarnation)
}

// ledgerEntryConsistent reports whether an index-ledger entry may be acted on
// by the sweeper: it must be stored under the key its own database and
// incarnation derive, in the database being swept, it must claim the owner
// its (database, name, incarnation) derives, and every index it names must be
// one of that collection's derived index names. The owner-checked drop would
// refuse any index the derived owner does not hold, so this is a second,
// independent refusal of an entry that was not written by CreateCollection.
func ledgerEntryConsistent(key, database string, entry *collectionIndexLedger) error {
	inconsistent := func(reason string) error {
		return &CollectionInconsistentRecordError{Database: database, Collection: entry.Name, Reason: reason}
	}
	if entry.Database != database || entry.Name == "" || entry.Incarnation == "" ||
		key != collectionIndexLedgerKey(entry.Database, entry.Incarnation) {
		return inconsistent("the index ledger entry is not stored under the key its database and incarnation derive")
	}
	if entry.Owner != index.CollectionOwner(entry.Database, entry.Name, entry.Incarnation) {
		return inconsistent("the index ledger entry's owner does not derive from its incarnation")
	}
	namePrefix := collectionIndexName(entry.Database, entry.Name, "")
	for _, name := range entry.Indexes {
		if len(name) <= len(namePrefix) || !strings.HasPrefix(name, namePrefix) {
			return inconsistent("the index ledger entry names an index that is not the collection's")
		}
	}
	return nil
}

// sweepOrphanedIndexes removes the indexes of every stale index-ledger entry
// in database whose incarnation no collection metadata names.
func (s *CollectionService) sweepOrphanedIndexes(ctx context.Context, database string, now time.Time, staleAfter time.Duration) (int, error) {
	if s.indexSvc == nil {
		return 0, nil
	}
	entries, err := s.kvSvc.Scan(ctx, collectionIndexLedgerPrefix+database+"/")
	if err != nil {
		return 0, &CollectionMetadataError{Op: "index-ledger", Err: err}
	}
	cleaned := 0
	var failures []error
	for key, raw := range entries {
		var entry collectionIndexLedger
		if err := json.Unmarshal(raw, &entry); err != nil || now.Sub(entry.CreatedAt) < staleAfter {
			continue
		}
		if err := ledgerEntryConsistent(key, database, &entry); err != nil {
			failures = append(failures, err)
			continue
		}
		info, _, err := s.loadCollectionRecord(ctx, entry.Database, entry.Name)
		if err == nil && info.Incarnation == entry.Incarnation {
			continue // live: its own teardown removes it
		}
		var notFound *CollectionNotFoundError
		if err != nil && !errors.As(err, &notFound) {
			failures = append(failures, err)
			continue
		}
		failed := false
		for _, name := range entry.Indexes {
			if err := s.indexSvc.DropOwnedIndex(ctx, name, entry.Owner); err != nil && !indexAlreadyGone(err) && !indexOwnedElsewhere(err) {
				failures = append(failures, &CollectionMetadataError{Op: "drop-index", Err: err})
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		if _, err := s.kvSvc.CompareAndDelete(ctx, key, raw); err != nil {
			failures = append(failures, &CollectionMetadataError{Op: "index-ledger", Err: err})
			continue
		}
		cleaned++
	}
	return cleaned, errors.Join(failures...)
}

// ListCollections returns all collections in the specified database, each
// with its lifecycle state. A collection that is CREATING or DROPPING is
// listed and marked, never omitted: an omitted crashed drop would be
// invisible.
func (s *CollectionService) ListCollections(ctx context.Context, req *ListCollectionsRequest) (*ListCollectionsResponse, error) {
	if req.Database == "" {
		return nil, &CollectionEmptyDatabaseError{}
	}

	prefix := collectionMetadataPrefix + req.Database + "/"
	data, err := s.kvSvc.Scan(ctx, prefix)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "list", Err: err}
	}

	collections := make([]CollectionInfo, 0, len(data))
	for _, v := range data {
		var info CollectionInfo
		if err := json.Unmarshal(v, &info); err != nil {
			continue
		}
		normalizeCollectionState(&info)
		// Refresh document count.
		info.DocumentCount = s.countDocuments(ctx, &info)
		collections = append(collections, info)
	}

	return &ListCollectionsResponse{Collections: collections}, nil
}

// GetCollectionInfo returns metadata for a specific collection, in whatever
// lifecycle state it is in.
func (s *CollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*CollectionInfo, error) {
	if err := validateCollectionRequest(database, name); err != nil {
		return nil, err
	}

	info, err := s.loadCollectionInfo(ctx, database, name)
	if err != nil {
		return nil, err
	}

	info.DocumentCount = s.countDocuments(ctx, info)
	return info, nil
}

// InsertDocument inserts a new document into a collection.
//
// The document's ID comes from one of two places, and which one it came from
// decides what the insert is allowed to do:
//
//   - An EXPLICIT `_id` carried in the document is a claim on a particular
//     identity. The insert reserves it against the collection's own allocator
//     (reserveExplicitDocumentID) and refuses if a document already holds it.
//   - A GENERATED ID comes from the collection's declared strategy
//     (nextDocumentID). It may write over an existing document only where the
//     strategy defines that as its purpose -- field_hash, whose ID is derived
//     from a natural key so a re-insert of the same key is the documented
//     idempotent update. See generatedIDMayOverwrite.
//
// Every other collision is refused with CollectionDocumentExistsError rather
// than overwritten. An insert that destroys a stored document and reports
// success is indistinguishable from one that stored a new one, which is what
// made go-qrdb#447 silent: two successful inserts, one surviving document, no
// error anywhere.
//
// A refusal is enforced by KVServicer.PutIfAbsent -- the only write with a
// defined outcome under concurrency -- rather than by a read followed by an
// unguarded write. The read-then-write closed only the DETERMINISTIC overwrite,
// the one a single caller reaches with no concurrency at all; two callers
// claiming the same explicit `_id` could both observe it free and both write,
// and the loser's document vanished with no error anywhere. It was written that
// way because the dedicated-store bundle's StandaloneBackend was not a
// database.AtomicBackend, so a guarded write would have failed EVERY collection
// insert on that path. It is one now (go-qrdb#466), and every other KVServicer
// -- the Raft KVService, MVCCKVService, the namespace and quota wrappers --
// already had the verb, so the guard is now the write itself.
//
// req.WriteMode is deliberately NOT honoured. Every insert is a guarded write,
// synchronous by construction because its whole purpose is to report an
// outcome: which caller won the claim, or -- for an overwriting insert (see
// generatedIDMayOverwrite) -- exactly which document it displaced, which the
// response carries in Previous. An asynchronous proposal has no answer to
// report, so there is nothing for a weaker durability mode to mean here.
//
// Only an ACTIVE collection accepts the insert, and the write is fenced
// against a concurrent drop (see fenceWrite).
func (s *CollectionService) InsertDocument(ctx context.Context, req *InsertDocumentRequest) (*InsertDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if len(req.Document) == 0 {
		return nil, &CollectionEmptyDocumentError{}
	}

	info, err := s.loadActiveCollection(ctx, req.Database, req.Collection)
	if err != nil {
		return nil, err
	}

	// Honour an explicit _id from the document when present and non-zero;
	// otherwise generate one using the collection's configured ID strategy.
	// Either way the "_id" key is consumed into the identity rather than
	// stored alongside the data -- see documentFields.
	fields := documentFields(req.Document)
	if err := validateStoredFields(fields); err != nil {
		return nil, err
	}
	entity := &dao.DynamicEntity{Fields: fields}
	id := extractExplicitID(req.Document)
	explicit := id != 0
	if explicit {
		if err := s.reserveExplicitDocumentID(ctx, info, id); err != nil {
			return nil, err
		}
	} else {
		id, err = s.nextDocumentID(ctx, info, entity)
		if err != nil {
			return nil, err
		}
	}
	entity.SetEntityID(id)

	docKey := collectionDocumentKey(info, id)

	// Serialize and store document using the resolved codec.
	c := s.resolveCodec(info)
	docData, err := marshalDocument(entity, c)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal-document", Err: err}
	}

	resp, err := s.storeNewDocument(ctx, info, req, docKey, docData, id, explicit, c)
	if err != nil {
		return nil, err
	}

	current, raw, err := s.fenceWrite(ctx, info, docKey, docData)
	if err != nil {
		return nil, err
	}

	// Update schema from document fields.
	s.updateSchema(ctx, current, raw, fields)

	resp.ID = id
	return resp, nil
}

// GetDocument retrieves a document by ID from an ACTIVE collection.
func (s *CollectionService) GetDocument(ctx context.Context, req *GetDocumentRequest) (*GetDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if req.ID == 0 {
		return nil, &CollectionInvalidDocumentIDError{}
	}

	info, err := s.loadActiveCollection(ctx, req.Database, req.Collection)
	if err != nil {
		return nil, err
	}

	docKey := collectionDocumentKey(info, req.ID)

	var data []byte
	if req.Consistency != 0 {
		opts := consistency.ReadOptions{Consistency: req.Consistency}
		data, err = s.kvSvc.GetWithConsistency(ctx, docKey, opts)
	} else {
		data, err = s.kvSvc.Get(ctx, docKey)
	}
	if err != nil || data == nil {
		return nil, &CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}

	var entity dao.DynamicEntity
	if err := unmarshalDocument(data, &entity, s.resolveCodec(info)); err != nil {
		return nil, &CollectionMetadataError{Op: "unmarshal-document", Err: err}
	}

	return &GetDocumentResponse{
		ID:       entity.ID,
		Document: entity.Fields,
		Revision: documentRevision(data),
	}, nil
}

// UpdateDocument updates an existing document in an ACTIVE collection. The
// write is fenced against a concurrent drop (see fenceWrite).
func (s *CollectionService) UpdateDocument(ctx context.Context, req *UpdateDocumentRequest) (*UpdateDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if req.ID == 0 {
		return nil, &CollectionInvalidDocumentIDError{}
	}
	if len(req.Document) == 0 {
		return nil, &CollectionEmptyDocumentError{}
	}

	info, err := s.loadActiveCollection(ctx, req.Database, req.Collection)
	if err != nil {
		return nil, err
	}

	// Verify document exists.
	docKey := collectionDocumentKey(info, req.ID)
	existing, err := s.kvSvc.Get(ctx, docKey)
	if err != nil || existing == nil {
		return nil, &CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}

	// The document's identity comes from the request, never from the body:
	// UpdateDocument addresses a document that already exists. A body that
	// also carries "_id" is carrying the identity key, not a field, so it is
	// consumed here for the same reason InsertDocument consumes it -- an
	// updated document must have the same shape as an inserted one.
	fields := documentFields(req.Document)
	if err := validateStoredFields(fields); err != nil {
		return nil, err
	}
	entity := &dao.DynamicEntity{
		ID:     req.ID,
		Fields: fields,
	}

	c := s.resolveCodec(info)
	docData, err := marshalDocument(entity, c)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal-document", Err: err}
	}

	if req.WriteMode != 0 {
		if err := s.kvSvc.PutWithMode(ctx, docKey, docData, req.WriteMode); err != nil {
			return nil, &CollectionMetadataError{Op: "update-document", Err: err}
		}
	} else {
		if err := s.kvSvc.Put(ctx, docKey, docData); err != nil {
			return nil, &CollectionMetadataError{Op: "update-document", Err: err}
		}
	}

	current, raw, err := s.fenceWrite(ctx, info, docKey, docData)
	if err != nil {
		return nil, err
	}

	// Update schema from new fields.
	s.updateSchema(ctx, current, raw, fields)

	return &UpdateDocumentResponse{ID: req.ID}, nil
}

// UpsertDocument stores a document under an explicit ID whether or not one is
// already stored there, and reports which happened (go-qrdb#497).
//
// It is the statement-level primitive behind SQL's REPLACE INTO and
// INSERT ... ON DUPLICATE KEY UPDATE on a collection table. Those were each an
// insert, a refusal and a second statement-level write with no lock across the
// three, so two concurrent statements on one `_id` could interleave and the
// loser's value could be lost with nobody told. Here the presence decision and
// the write are one guarded write: an absent document is stored with
// PutIfAbsent, a present one is replaced with CompareAndSwap against the exact
// bytes that were read, and a guard that loses to a concurrent writer is
// re-decided from the new state. The document the write displaced is returned
// in Previous, and it is exactly the one displaced -- not a separate read a
// concurrent writer could have changed.
//
// With IfRevision set it is instead a guarded REPLACE of the one document the
// caller read: it succeeds only while that document is still stored, and is
// refused with CollectionDocumentConflictError otherwise. That is what makes a
// read-compute-write -- ON DUPLICATE KEY UPDATE evaluating its expressions
// over the existing row, or an UPDATE -- lose loudly instead of overwriting a
// value it never saw.
//
// The ID is an explicit claim: on an increment collection it reserves the
// counter exactly as an insert's explicit `_id` does. The write is synchronous
// (the guard is its point) and fenced against a concurrent drop.
func (s *CollectionService) UpsertDocument(ctx context.Context, req *UpsertDocumentRequest) (*UpsertDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if req.ID == 0 {
		return nil, &CollectionInvalidDocumentIDError{}
	}
	if len(req.Document) == 0 {
		return nil, &CollectionEmptyDocumentError{}
	}
	if err := validateStoredFields(documentFields(req.Document)); err != nil {
		return nil, err
	}

	info, err := s.loadActiveCollection(ctx, req.Database, req.Collection)
	if err != nil {
		return nil, err
	}
	if err := s.reserveExplicitDocumentID(ctx, info, req.ID); err != nil {
		return nil, err
	}

	fields := documentFields(req.Document)
	entity := &dao.DynamicEntity{ID: req.ID, Fields: fields}
	c := s.resolveCodec(info)
	docData, err := marshalDocument(entity, c)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal-document", Err: err}
	}

	docKey := collectionDocumentKey(info, req.ID)
	conflict := &CollectionDocumentConflictError{Database: req.Database, Collection: req.Collection, ID: req.ID}
	resp, err := s.guardedUpsert(ctx, docKey, docData, req.IfRevision, c, conflict)
	if err != nil {
		return nil, err
	}
	resp.ID = req.ID

	current, raw, err := s.fenceWrite(ctx, info, docKey, docData)
	if err != nil {
		return nil, err
	}
	s.updateSchema(ctx, current, raw, fields)
	return resp, nil
}

// guardedUpsert decides presence and writes in one guarded write, re-deciding
// when a concurrent writer wins the guard. See UpsertDocument.
func (s *CollectionService) guardedUpsert(ctx context.Context, docKey string, docData []byte, ifRevision string, c codec.Codec, conflict error) (*UpsertDocumentResponse, error) {
	for attempt := 0; attempt < upsertMaxAttempts; attempt++ {
		current, err := s.kvSvc.Get(ctx, docKey)
		absent := current == nil && (err == nil || isKeyNotFound(err))
		if err != nil && !absent {
			return nil, &CollectionMetadataError{Op: "check-document", Err: err}
		}

		if absent {
			if ifRevision != "" {
				return nil, conflict
			}
			stored, err := s.kvSvc.PutIfAbsent(ctx, docKey, docData)
			if err != nil {
				return nil, &CollectionMetadataError{Op: "upsert-document", Err: err}
			}
			if stored {
				return &UpsertDocumentResponse{Revision: documentRevision(docData)}, nil
			}
			continue
		}

		if ifRevision != "" && documentRevision(current) != ifRevision {
			return nil, conflict
		}
		swapped, err := s.kvSvc.CompareAndSwap(ctx, docKey, current, docData)
		if err != nil {
			return nil, &CollectionMetadataError{Op: "upsert-document", Err: err}
		}
		if !swapped {
			if ifRevision != "" {
				return nil, conflict
			}
			continue
		}
		var previous dao.DynamicEntity
		if err := unmarshalDocument(current, &previous, c); err != nil {
			return nil, &CollectionMetadataError{Op: "unmarshal-document", Err: err}
		}
		return &UpsertDocumentResponse{
			Replaced: true,
			Previous: previous.Fields,
			Revision: documentRevision(docData),
		}, nil
	}
	return nil, conflict
}

// DeleteDocument removes a document from an ACTIVE collection. With
// IfRevision set it removes the document only while it still carries that
// revision (go-qrdb#497).
//
// A collection being dropped refuses it like every other document verb: the
// document's key lives in the incarnation's own keyspace, which only the
// metadata names, and the drop's teardown removes every document in it
// anyway -- counting exactly the ones it removed, so a delete that raced it
// and a teardown never both account for one document.
func (s *CollectionService) DeleteDocument(ctx context.Context, req *DeleteDocumentRequest) (*DeleteDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if req.ID == 0 {
		return nil, &CollectionInvalidDocumentIDError{}
	}

	info, err := s.loadActiveCollection(ctx, req.Database, req.Collection)
	if err != nil {
		return nil, err
	}
	docKey := collectionDocumentKey(info, req.ID)
	existing, err := s.kvSvc.Get(ctx, docKey)
	if err != nil || existing == nil {
		return nil, &CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}

	if req.IfRevision != "" {
		conflict := &CollectionDocumentConflictError{Database: req.Database, Collection: req.Collection, ID: req.ID}
		if documentRevision(existing) != req.IfRevision {
			return nil, conflict
		}
		deleted, err := s.kvSvc.CompareAndDelete(ctx, docKey, existing)
		if err != nil {
			return nil, &CollectionMetadataError{Op: "delete-document", Err: err}
		}
		if !deleted {
			return nil, conflict
		}
		return &DeleteDocumentResponse{Deleted: true}, nil
	}

	if req.WriteMode != 0 {
		if err := s.kvSvc.DeleteWithMode(ctx, docKey, req.WriteMode); err != nil {
			return nil, &CollectionMetadataError{Op: "delete-document", Err: err}
		}
	} else {
		if err := s.kvSvc.Delete(ctx, docKey); err != nil {
			return nil, &CollectionMetadataError{Op: "delete-document", Err: err}
		}
	}

	return &DeleteDocumentResponse{Deleted: true}, nil
}

// ListDocuments lists documents in an ACTIVE collection with pagination.
func (s *CollectionService) ListDocuments(ctx context.Context, req *ListDocumentsRequest) (*ListDocumentsResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}

	info, infoErr := s.loadActiveCollection(ctx, req.Database, req.Collection)
	if infoErr != nil {
		return nil, infoErr
	}

	c := s.resolveCodec(info)
	dataPrefix := info.documentPrefix()

	var (
		data map[string][]byte
		err  error
	)
	if req.Consistency != 0 {
		opts := consistency.ReadOptions{Consistency: req.Consistency}
		data, err = s.kvSvc.ScanWithConsistency(ctx, dataPrefix, opts)
	} else {
		data, err = s.kvSvc.Scan(ctx, dataPrefix)
	}
	if err != nil {
		return nil, &CollectionMetadataError{Op: "list-documents", Err: err}
	}

	pageSize := int(req.PageSize)
	if pageSize <= 0 {
		pageSize = 25
	}

	// Parse page token as offset.
	offset := 0
	if req.PageToken != "" {
		parsed, parseErr := strconv.Atoi(req.PageToken)
		if parseErr != nil {
			return nil, &InvalidPageTokenError{Token: req.PageToken}
		}
		offset = parsed
	}

	// Collect and sort keys for deterministic ordering.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	totalCount := int64(len(keys))

	// Apply pagination.
	var docs []GetDocumentResponse
	end := offset + pageSize
	if end > len(keys) {
		end = len(keys)
	}

	for i := offset; i < end; i++ {
		var entity dao.DynamicEntity
		if err := unmarshalDocument(data[keys[i]], &entity, c); err != nil {
			continue
		}
		docs = append(docs, GetDocumentResponse{
			ID:       entity.ID,
			Document: entity.Fields,
			Revision: documentRevision(data[keys[i]]),
		})
	}

	if docs == nil {
		docs = make([]GetDocumentResponse, 0)
	}

	var nextToken string
	if end < len(keys) {
		nextToken = strconv.Itoa(end)
	}

	return &ListDocumentsResponse{
		Documents:     docs,
		NextPageToken: nextToken,
		TotalCount:    totalCount,
	}, nil
}

// fenceWrite refuses a document write whose collection stopped being the
// ACTIVE incarnation it was decided against while the write was in flight
// (go-qrdb#425). It re-reads the collection's metadata AFTER the write; if the
// collection is no longer ACTIVE, no longer exists, or is no longer the
// INCARNATION the write was decided from, the caller is refused and the write
// is retracted.
//
// Neither the refusal nor the retraction is what keeps a stale write away
// from a live collection: the write went to the key its decided incarnation
// derives (collectionDocumentKey), which no other incarnation shares, so it
// could only ever land in a keyspace that is being or has been reclaimed.
// The fence is what makes such a write fail LOUDLY -- no write into a dropping
// or replaced collection is reported as a success -- and the retraction is
// what keeps it from outliving its incarnation's teardown: a write that
// committed after the teardown scanned the keyspace would otherwise be left
// there, unreachable, for good.
//
// The ordering is what makes the refusal sound. A drop's DROPPING mark is
// appended to the log before the teardown scans the documents to delete. A
// write appended AFTER that scan is therefore appended after the mark, so this
// re-read -- linearizable, and issued after the write committed -- observes
// the mark (or the metadata's absence, or a newer incarnation). A write
// appended BEFORE the scan is found by it and removed by the teardown.
//
// It returns the metadata it read and its raw bytes, which the schema merge
// swaps against. A re-read that fails for any reason other than absence is
// reported as CollectionMetadataError{Op: "fence"} and the write stands:
// retracting on an unknown answer would undo a write into a live collection.
func (s *CollectionService) fenceWrite(ctx context.Context, decided *CollectionInfo, docKey string, written []byte) (*CollectionInfo, []byte, error) {
	database, collection := decided.Database, decided.Name
	info, raw, err := s.loadCollectionRecord(ctx, database, collection)
	if err != nil {
		var notFound *CollectionNotFoundError
		if !errors.As(err, &notFound) {
			return nil, nil, &CollectionMetadataError{Op: "fence", Err: err}
		}
		return nil, nil, s.retractWrite(ctx, decided, docKey, written, err)
	}
	if info.Incarnation != decided.Incarnation {
		return nil, nil, s.retractWrite(ctx, decided, docKey, written, &CollectionStateConflictError{
			Database:   database,
			Collection: collection,
			Op:         "write",
		})
	}
	if info.State != CollectionStateActive {
		return nil, nil, s.retractWrite(ctx, decided, docKey, written, &CollectionNotActiveError{
			Database:   database,
			Collection: collection,
			State:      info.State,
		})
	}
	return info, raw, nil
}

// retractWrite removes a fenced-out write from its dead incarnation's
// keyspace and returns the refusal the caller reports.
//
// The retraction is a CompareAndDelete of the bytes this call wrote. When it
// removes nothing, somebody else already removed or replaced the document --
// the teardown reclaiming the keyspace, or a concurrent writer to the same
// key -- and accounted for it; the refusal is then reported as
// CollectionWriteSupersededError, which tells a storage quota that the write
// did store a document it must not release a second time.
//
// The write may also have recreated the dead incarnation's ID sequence -- an
// allocation or a reservation made after its teardown deleted it -- so that
// is reclaimed too, with the process-local generator cached for it. Both are
// the dead incarnation's alone. A retraction or reclamation that fails is
// reported with the refusal.
func (s *CollectionService) retractWrite(ctx context.Context, decided *CollectionInfo, docKey string, written []byte, refusal error) error {
	deleted, err := s.kvSvc.CompareAndDelete(ctx, docKey, written)
	if err != nil {
		return &CollectionMetadataError{Op: "retract-write", Err: errors.Join(refusal, err)}
	}
	if !deleted {
		refusal = &CollectionWriteSupersededError{Err: refusal}
	}
	s.idGens.Delete(collectionIDGenKey(decided))
	if idStrategyDrawsFromACounter[decided.IDStrategy] && s.seqSvc != nil {
		if err := s.seqSvc.Delete(ctx, decided.Database, collectionSequenceName(decided)); err != nil {
			return &CollectionMetadataError{Op: "retract-sequence", Err: errors.Join(refusal, err)}
		}
	}
	return refusal
}

// collectionDocumentKey returns the storage key of one document of one
// incarnation of a collection (see CollectionInfo.documentPrefix).
func collectionDocumentKey(info *CollectionInfo, id uint64) string {
	return info.documentPrefix() + strconv.FormatUint(id, 10)
}

// documentRevision identifies a stored document's exact bytes. It is the
// token GetDocumentResponse.Revision carries and the guarded writes compare
// against; the guarded write itself swaps against the bytes, so the digest
// only has to tell two different encodings apart.
func documentRevision(stored []byte) string {
	sum := sha256.Sum256(stored)
	return hex.EncodeToString(sum[:16])
}

// DocumentIDField is the document key a caller uses to name a document's
// identity. Every transport spells it this way -- the REST and MCP request
// bodies, the gRPC and QUIC document maps, the SDK -- and the SQL surface
// exposes it as column 0 of a collection table
// (pkg/sql/provider.buildCollectionSchema), which is why it is exported: there
// is one identity and it must not acquire a second spelling.
const DocumentIDField = "_id"

// documentFields returns the document's DATA -- everything except its
// identity.
//
// "_id" names the document; it is not one of the document's fields, and a
// collection that stores it as one ends up describing it twice. That is not
// theoretical: updateSchema records every key it is given, so a stored "_id"
// became an entry in CollectionInfo.Schema, and the SQL surface -- whose
// collection table already declares "_id" as column 0 -- then emitted a
// SECOND column with the same name, so `SELECT * FROM <collection>` returned
// two `_id` columns (go-qrdb#467). It also made a document's shape depend on
// how its ID was decided: an explicit insert carried an extra field a
// generated one did not.
//
// The identity survives the strip -- extractExplicitID has already read it,
// and it is written back out by marshalDocument as the entity's "id" -- so
// nothing is lost, and a document round-trips identically whichever verb and
// whichever transport wrote it.
//
// The input map is never mutated: it belongs to the caller, and on the
// transports that decode a request body directly into it the caller is the
// wire. A document without "_id" is returned as-is, so the common path
// allocates nothing.
func documentFields(doc map[string]interface{}) map[string]interface{} {
	if _, ok := doc[DocumentIDField]; !ok {
		return doc
	}
	fields := make(map[string]interface{}, len(doc)-1)
	for k, v := range doc {
		if k == DocumentIDField {
			continue
		}
		fields[k] = v
	}
	return fields
}

// storedIdentityField is the key a stored document carries its own identity
// under (dao.DynamicEntity's flat layout); a document field of that name
// cannot be stored beside it.
const storedIdentityField = "id"

// legacyFieldsWrapper is the key of the legacy {"id", "fields"} layout: a
// stored object whose only key besides the identity is an object under this
// name is read as that wrapper and unwrapped.
const legacyFieldsWrapper = "fields"

// validateStoredFields refuses a document whose fields the stored layout
// cannot hold as sent (see CollectionReservedFieldError). fields is the
// document without its "_id" identity key (documentFields).
func validateStoredFields(fields map[string]interface{}) error {
	if _, ok := fields[storedIdentityField]; ok {
		return &CollectionReservedFieldError{
			Field:  storedIdentityField,
			Reason: "it is the key a stored document keeps its identity under; name the identity with " + strconv.Quote(DocumentIDField) + ", or rename the field",
		}
	}
	if len(fields) == 1 {
		if _, isObject := fields[legacyFieldsWrapper].(map[string]interface{}); isObject {
			return &CollectionReservedFieldError{
				Field:  legacyFieldsWrapper,
				Reason: "an object under it as a document's only field reads back as the legacy wrapper and would be unwrapped; add another field or rename it",
			}
		}
	}
	return nil
}

// extractExplicitID returns the document's own "_id" field when it carries a
// positive numeric value, and 0 otherwise -- 0 meaning "no explicit ID", so
// the caller allocates one from the collection's ID strategy instead.
//
// Zero and negative values are read as absent rather than rejected because 0
// is not a document ID in this service (CollectionInvalidDocumentIDError
// refuses it on every other verb) and a negative one cannot become a uint64
// ID; both are MySQL's "generate one for me" convention, and the relational
// layer reads an explicit AUTO_INCREMENT value the same way.
//
// A non-numeric "_id" -- a string, say -- is also absent: the ID space is
// uint64 (see docs/collections/architecture.md on why string document IDs
// were rejected) and there is nothing to interpret.
func extractExplicitID(doc map[string]interface{}) uint64 {
	raw, ok := doc[DocumentIDField]
	if !ok {
		return 0
	}
	switch v := raw.(type) {
	case uint64:
		return v
	case int64:
		if v > 0 {
			return uint64(v)
		}
	case float64:
		if v > 0 {
			return uint64(v)
		}
	case int:
		if v > 0 {
			return uint64(v)
		}
	}
	return 0
}

// resolveCodec returns the codec used to encode documents in the given
// collection. Resolution order:
//  1. Per-collection StorageCodec (set at collection creation time)
//  2. Per-database default from dbCodecLookup (if configured)
//  3. JSON (always available, backward-compatible default)
func (s *CollectionService) resolveCodec(info *CollectionInfo) codec.Codec {
	name := info.StorageCodec
	if name == "" && s.dbCodecLookup != nil {
		name = s.dbCodecLookup(info.Database)
	}
	if name == "" {
		return &codec.JSONCodec{}
	}
	c, err := codec.ResolveCodec(name)
	if err != nil {
		// Codec name recorded in metadata is not available in this build;
		// fall back to JSON so existing data remains accessible.
		return &codec.JSONCodec{}
	}
	return c
}

// marshalDocument encodes a DynamicEntity as a flat map so that any codec
// (not just JSON) can serialize it correctly. The flat map representation
// is {"id": "<uint64>", "field1": v1, ...}.
func marshalDocument(entity *dao.DynamicEntity, c codec.Codec) ([]byte, error) {
	if c.Type() == codec.TypeJSON {
		// Use DynamicEntity's custom JSON marshaling for full compatibility.
		return json.Marshal(entity)
	}
	m := make(map[string]interface{}, len(entity.Fields)+1)
	m["id"] = strconv.FormatUint(entity.ID, 10)
	for k, v := range entity.Fields {
		if k != "id" {
			m[k] = v
		}
	}
	return c.Marshal(m)
}

// unmarshalDocument decodes raw bytes into a DynamicEntity. It first tries
// the collection's resolved codec and, on failure, falls back to JSON for
// backward compatibility with documents written before the codec was set.
func unmarshalDocument(data []byte, entity *dao.DynamicEntity, c codec.Codec) error {
	if c.Type() == codec.TypeJSON {
		return json.Unmarshal(data, entity)
	}
	// Try the configured codec first (decodes into a generic map).
	var m map[string]interface{}
	if err := c.Unmarshal(data, &m); err != nil {
		// Fall back to JSON for documents written before the codec was set.
		return json.Unmarshal(data, entity)
	}
	// Reconstruct entity from the decoded map.
	if idVal, ok := m["id"]; ok {
		switch v := idVal.(type) {
		case string:
			if parsed, parseErr := strconv.ParseUint(v, 10, 64); parseErr == nil {
				entity.ID = parsed
			}
		case uint64:
			entity.ID = v
		case int64:
			entity.ID = uint64(v)
		case float64:
			entity.ID = uint64(v)
		}
		delete(m, "id")
	}
	entity.Fields = m
	return nil
}

// loadCollectionInfo loads collection metadata from storage, in whatever
// lifecycle state it is in.
func (s *CollectionService) loadCollectionInfo(ctx context.Context, database, name string) (*CollectionInfo, error) {
	info, _, err := s.loadCollectionRecord(ctx, database, name)
	return info, err
}

// loadActiveCollection loads collection metadata and refuses a collection
// that is not ACTIVE: one still CREATING has not finished building its
// indexes, and one DROPPING is losing them (go-qrdb#415, go-qrdb#425).
func (s *CollectionService) loadActiveCollection(ctx context.Context, database, name string) (*CollectionInfo, error) {
	info, err := s.loadCollectionInfo(ctx, database, name)
	if err != nil {
		return nil, err
	}
	if info.State != CollectionStateActive {
		return nil, &CollectionNotActiveError{Database: database, Collection: name, State: info.State}
	}
	return info, nil
}

// loadCollectionRecord loads collection metadata together with the exact
// stored bytes it was decoded from, which a guarded metadata write swaps
// against.
//
// Only an ABSENT key is CollectionNotFoundError. A read that fails for any
// other reason is reported as the failure it is: the drop fence
// (fenceWrite) retracts a write when the collection is gone, and a transient
// read error mistaken for "gone" would undo a write into a live collection.
func (s *CollectionService) loadCollectionRecord(ctx context.Context, database, name string) (*CollectionInfo, []byte, error) {
	metaKey := collectionMetadataKey(database, name)
	data, err := s.kvSvc.Get(ctx, metaKey)
	if (err == nil && data == nil) || (err != nil && isKeyNotFound(err)) {
		return nil, nil, &CollectionNotFoundError{
			Database:   database,
			Collection: name,
		}
	}
	if err != nil {
		return nil, nil, &CollectionMetadataError{Op: "load-metadata", Err: err}
	}

	var info CollectionInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, nil, &CollectionMetadataError{Op: "unmarshal-metadata", Err: err}
	}
	normalizeCollectionState(&info)

	return &info, data, nil
}

// normalizeCollectionState reads metadata written before the lifecycle state
// existed as ACTIVE, which is what such a collection was.
func normalizeCollectionState(info *CollectionInfo) {
	if info.State == "" {
		info.State = CollectionStateActive
	}
}

// countDocuments returns the number of documents in one incarnation of a
// collection.
func (s *CollectionService) countDocuments(ctx context.Context, info *CollectionInfo) int64 {
	keys, err := s.kvSvc.List(ctx, info.documentPrefix())
	if err != nil {
		return 0
	}
	return int64(len(keys))
}

// idStrategyDrawsFromACounter records, per ID strategy, whether the
// collection's IDs come from a counter -- and therefore whether an
// explicitly supplied _id has anything to reserve itself against.
//
// A map rather than a switch so that adding a strategy is adding a row: a
// strategy absent from it reserves nothing, which is the correct answer for
// unix_nano (a clock reading, which no counter tracks) and for field_hash
// (a hash of a natural key, which no counter tracks either).
var idStrategyDrawsFromACounter = map[string]bool{
	IDStrategyIncrement: true,
}

// generatedIDMayOverwrite records, per ID strategy, whether an ID the
// collection GENERATED may be written over a document that already holds it.
//
// Exactly one strategy may: field_hash derives the ID from a named document
// field, so re-inserting a document with the same natural key resolves to
// the same ID on purpose -- docs/collections/architecture.md promises that
// re-insert is idempotent, and refusing it would break the property the
// strategy exists to provide.
//
// increment and unix_nano may not. For increment a generated collision is
// now impossible by construction (see reserveExplicitDocumentID), so a hit
// here means the allocator is broken and the caller is told rather than
// losing a document. For unix_nano it means two inserts read the same
// nanosecond, which is rare rather than impossible and is data loss when it
// happens.
//
// An EXPLICIT _id is never in this table: it is not a strategy's generated
// value, it is a caller naming an identity, and naming one that is taken is
// refused on every strategy. See mayOverwrite.
var generatedIDMayOverwrite = map[string]bool{
	IDStrategyFieldHash: true,
}

// mayOverwrite reports whether an insert resolving to an already-occupied
// document ID is a legitimate overwrite rather than data loss.
func mayOverwrite(strategy string, explicit bool) bool {
	return !explicit && generatedIDMayOverwrite[strategy]
}

// reserveExplicitDocumentID raises the collection's ID counter past an
// explicitly supplied _id, so no ID the collection generates afterwards can
// collide with it.
//
// This is the fix for go-qrdb#447 and it is the same answer the relational
// layer already gives for an explicit value in an AUTO_INCREMENT column (see
// pkg/sql/provider.RelationalTable.GetNextAutoIncrementValue and
// docs/sql/engineering.md): the counter is a high-water mark over every ID
// in use, not merely over the IDs it handed out itself. Without it, the
// caller and the collection are two allocators over one keyspace and the
// collection eventually reissues an ID the caller already used.
//
// Both implementations of IDStrategyIncrement are advanced, because they
// must not disagree about this any more than they may disagree about what a
// recreated collection does (go-qrdb#411): the replicated counter through
// SequenceServicer.Advance, the process-local fallback through
// dao.IDSequence.Advance. Both are defined as no-ops once the counter is at
// or past the target, so the steady-state cost of an explicit insert is one
// read and zero Raft proposals -- a proposal is made only when the counter
// actually has to move.
//
// A strategy whose IDs do not come from a counter has nothing to reserve and
// this returns without doing anything; see idStrategyDrawsFromACounter.
//
// The cost, stated rather than hidden: the counter can only move up, so an
// explicit _id far above the collection's current watermark permanently
// strands the range beneath it. That is MySQL's behaviour for an explicit
// AUTO_INCREMENT value and the relational layer's here, and it is the cheaper
// of the two failures -- a stranded range wastes IDs in a 64-bit space, while
// an unreserved one destroys documents.
func (s *CollectionService) reserveExplicitDocumentID(ctx context.Context, info *CollectionInfo, id uint64) error {
	if !idStrategyDrawsFromACounter[info.IDStrategy] {
		return nil
	}

	if s.seqSvc != nil {
		if err := s.seqSvc.Advance(ctx, info.Database, collectionSequenceName(info), id); err != nil {
			return &CollectionMetadataError{Op: "reserve-id", Err: err}
		}
		return nil
	}

	// The process-local fallback. The capability is discovered by assertion
	// because IDGenerator has no counter in general -- see dao.IDSequence --
	// and a generator without one had nothing to reserve in the first place.
	if gen, ok := s.getOrCreateIDGen(info).(dao.IDSequence); ok {
		gen.Advance(id)
	}
	return nil
}

// storeNewDocument writes an insert's document, choosing the write by whether
// the insert is allowed to overwrite, and returns the insert's response less
// its ID.
//
// Where it is not, the write is KVServicer.PutIfAbsent and the refusal is the
// backend declining to store, not a check this service performed first. That
// matters because the two are not equally strong: a read followed by a write
// leaves a window in which a second caller sees the same free ID, and both
// write, and one document is silently gone. The guarded write has no such
// window on any backend that offers it, and a backend that does not offer it
// refuses with database.ErrAtomicWriteUnsupported rather than degrading -- an
// insert that cannot promise the ID is the caller's fails, which is the whole
// point of the promise.
//
// Where it is -- a field_hash re-insert of an existing natural key -- the
// write is the guarded upsert (see UpsertDocument), so the response reports
// whether a document was displaced and exactly which one. A storage quota
// charges the new body and releases that one; an unguarded overwrite could
// only charge every re-insert again.
//
// A write that FAILS is never read as an absent document: the error is
// returned and the insert fails. This is deliberately stricter than
// GetDocument, which treats any read failure as not-found because on a read
// path there is nothing to destroy.
func (s *CollectionService) storeNewDocument(ctx context.Context, info *CollectionInfo, req *InsertDocumentRequest, docKey string, docData []byte, id uint64, explicit bool, c codec.Codec) (*InsertDocumentResponse, error) {
	if !mayOverwrite(info.IDStrategy, explicit) {
		stored, err := s.kvSvc.PutIfAbsent(ctx, docKey, docData)
		if err != nil {
			return nil, &CollectionMetadataError{Op: "insert-document", Err: err}
		}
		if !stored {
			return nil, &CollectionDocumentExistsError{
				Database:   req.Database,
				Collection: req.Collection,
				ID:         id,
				Explicit:   explicit,
			}
		}
		return &InsertDocumentResponse{Revision: documentRevision(docData)}, nil
	}

	conflict := &CollectionDocumentConflictError{Database: req.Database, Collection: req.Collection, ID: id}
	up, err := s.guardedUpsert(ctx, docKey, docData, "", c, conflict)
	if err != nil {
		return nil, err
	}
	return &InsertDocumentResponse{Revision: up.Revision, Replaced: up.Replaced, Previous: up.Previous}, nil
}

// collectionSequenceName returns the SequenceServicer sequence name backing
// one incarnation's IDStrategyIncrement document IDs,
// "__collection:{name}#{incarnation}". Scoping it by incarnation is what keeps
// a stale writer's allocation, and a resumed teardown's delete, off the
// counter of the incarnation that replaced it. A record that predates
// incarnations keeps the unsuffixed "__collection:{name}" its counter already
// has, so a legacy collection needs no migration.
func collectionSequenceName(info *CollectionInfo) string {
	return "__collection:" + info.Name + incarnationSuffix(info.Incarnation)
}

// collectionIDGenKey is the idGens cache key of one incarnation's
// process-local ID generator, "{database}:{name}#{incarnation}".
func collectionIDGenKey(info *CollectionInfo) string {
	return info.Database + ":" + info.Name + incarnationSuffix(info.Incarnation)
}

// nextDocumentID generates the next document ID for a collection using its
// configured ID strategy.
//
// IDStrategyIncrement is routed through SequenceServicer when one is
// configured (WithSequenceService): SequenceServicer.Allocate commits its
// counter through a Raft compare-and-swap, so a value it returns is never
// handed out twice, across a process restart or across replicas. When no
// SequenceServicer is configured, IDStrategyIncrement falls back to the
// legacy process-local generator from getOrCreateIDGen -- see that
// function's doc comment for why that fallback can produce duplicate IDs.
func (s *CollectionService) nextDocumentID(ctx context.Context, info *CollectionInfo, entity dao.Entity) (uint64, error) {
	if info.IDStrategy == IDStrategyIncrement && s.seqSvc != nil {
		id, err := s.seqSvc.Allocate(ctx, info.Database, collectionSequenceName(info), 1)
		if err != nil {
			return 0, &CollectionMetadataError{Op: "allocate-id", Err: err}
		}
		return id, nil
	}
	return s.getOrCreateIDGen(info).NextID(entity), nil
}

// getOrCreateIDGen returns the cached ID generator for a collection,
// creating one if it does not exist.
//
// This is the process-local fallback used when IDStrategyIncrement has no
// SequenceServicer configured (see WithSequenceService and
// nextDocumentID): dao.NewAtomicIncrementGenerator(0) restarts at 0 on
// every process restart and is independent per node, so in that
// configuration IDStrategyIncrement can produce duplicate document IDs
// across a restart or across replicas of the same collection. Wire
// WithSequenceService to eliminate this.
func (s *CollectionService) getOrCreateIDGen(info *CollectionInfo) dao.IDGenerator {
	cacheKey := collectionIDGenKey(info)
	if gen, ok := s.idGens.Load(cacheKey); ok {
		return gen.(dao.IDGenerator) //nolint:errcheck // type assertion on a value this container only ever holds
	}

	var gen dao.IDGenerator
	switch info.IDStrategy {
	case IDStrategyIncrement:
		gen = dao.NewAtomicIncrementGenerator(0)
	case IDStrategyFieldHash:
		gen = dao.NewDynamicFieldHashGenerator(info.FieldHash)
	default:
		gen = &dao.UnixNanoGenerator{}
	}

	actual, _ := s.idGens.LoadOrStore(cacheKey, gen)
	return actual.(dao.IDGenerator) //nolint:errcheck // type assertion on a value this container only ever holds
}

// updateSchema discovers field types from the document and merges them
// into the collection's schema metadata.
//
// The merge is a guarded swap of the metadata bytes it was computed from
// (raw), re-read and re-merged when a concurrent writer changed them. It used
// to be a plain Put of a copy read at the start of the insert, which could
// write back a record a drop had since marked DROPPING -- reverting the mark --
// or deleted -- resurrecting a dropped collection's metadata. A collection
// that is no longer ACTIVE is not merged into at all.
//
// It is best effort by design: the schema is descriptive, and a merge that
// cannot be written never fails the document write it describes.
func (s *CollectionService) updateSchema(ctx context.Context, info *CollectionInfo, raw []byte, doc map[string]interface{}) {
	metaKey := collectionMetadataKey(info.Database, info.Name)
	for attempt := 0; attempt < metadataCASMaxAttempts; attempt++ {
		if info.State != CollectionStateActive || !mergeSchema(info, doc) {
			return
		}
		data, err := json.Marshal(info)
		if err != nil {
			return
		}
		swapped, err := s.kvSvc.CompareAndSwap(ctx, metaKey, raw, data)
		if err != nil || swapped {
			return
		}
		info, raw, err = s.loadCollectionRecord(ctx, info.Database, info.Name)
		if err != nil {
			return
		}
	}
}

// mergeSchema records a type for every field of doc the schema does not yet
// name, and reports whether it recorded any.
func mergeSchema(info *CollectionInfo, doc map[string]interface{}) bool {
	if info.Schema == nil {
		info.Schema = make(map[string]string)
	}
	changed := false
	for k, v := range doc {
		if _, exists := info.Schema[k]; !exists {
			info.Schema[k] = inferSQLType(v)
			changed = true
		}
	}
	return changed
}

// inferSQLType returns a SQL type string for a Go value.
func inferSQLType(v interface{}) string {
	switch v.(type) {
	case string:
		return "VARCHAR"
	case float64:
		return "DOUBLE"
	case bool:
		return "BOOLEAN"
	case map[string]interface{}, []interface{}:
		return "JSON"
	default:
		return "VARCHAR"
	}
}

// validateCollectionRequest validates that database and collection names
// are non-empty.
func validateCollectionRequest(database, name string) error {
	if database == "" {
		return &CollectionEmptyDatabaseError{}
	}
	if name == "" {
		return &CollectionEmptyNameError{}
	}
	return nil
}

// validateIDStrategy validates the ID strategy and associated fields.
func validateIDStrategy(strategy, fieldHash string) error {
	switch strategy {
	case IDStrategyUnixNano, IDStrategyIncrement:
		return nil
	case IDStrategyFieldHash:
		if fieldHash == "" {
			return &CollectionMissingFieldHashError{}
		}
		return nil
	default:
		return &CollectionInvalidIDStrategyError{Strategy: strategy}
	}
}
