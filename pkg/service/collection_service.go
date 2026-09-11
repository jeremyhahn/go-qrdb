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
	"encoding/json"
	"errors"
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

	// DeleteDocument removes a document from a collection.
	DeleteDocument(ctx context.Context, req *DeleteDocumentRequest) (*DeleteDocumentResponse, error)

	// ListDocuments lists documents in a collection with pagination.
	ListDocuments(ctx context.Context, req *ListDocumentsRequest) (*ListDocumentsResponse, error)
}

// CollectionService implements CollectionServicer using the KV service
// layer for document and metadata storage, with DAO IDGenerators for
// document ID generation.
type CollectionService struct {
	kvSvc         kvService
	indexSvc      indexService                 // optional; nil when indexes are not available
	idGens        sync.Map                     // map[string]dao.IDGenerator keyed by "db:collection"
	dbCodecLookup func(database string) string // optional; returns per-database default codec name
	quotaChecker  quota.Checker                // optional; nil skips quota enforcement
	seqSvc        SequenceServicer             // optional; see WithSequenceService
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
type kvService interface {
	Put(ctx context.Context, key string, value []byte) error
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
// DropIndex is called during teardown, where "the index is not there" is the
// outcome being asked for. The canonical index.IndexServicer contract permits
// an implementation to report that as IndexNotFoundError -- the shipped
// IndexService does not, because the state machine's drop handler is
// idempotent so a cleanup proposal cannot stall Raft -- so DropCollection
// accepts EITHER answer as success. Any other error fails the drop.
type indexService interface {
	CreateIndex(ctx context.Context, name, fieldName string, config index.IndexConfig) error
	DropIndex(ctx context.Context, name string) error
}

// indexAlreadyGone reports whether an error from indexService.DropIndex means
// the index was not there to drop, which is success for a teardown.
func indexAlreadyGone(err error) bool {
	var notFound *IndexNotFoundError
	return errors.As(err, &notFound)
}

// Compile-time interface check.
var _ CollectionServicer = (*CollectionService)(nil)

// NewCollectionService creates a new CollectionService backed by the
// given KV service for storage.
func NewCollectionService(kvSvc kvService) *CollectionService {
	return &CollectionService{kvSvc: kvSvc}
}

// NewCollectionServiceWithIndex creates a new CollectionService backed
// by the given KV service for storage and an index service for managing
// secondary indexes on collection fields.
func NewCollectionServiceWithIndex(kvSvc kvService, indexSvc indexService) *CollectionService {
	return &CollectionService{
		kvSvc:    kvSvc,
		indexSvc: indexSvc,
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

// CreateCollection creates a new collection with the specified
// configuration. The collection metadata is stored under
// __sys:collections/{database}/{name} and documents are stored under
// __col:{database}:{collection}/{id}.
func (s *CollectionService) CreateCollection(ctx context.Context, req *CreateCollectionRequest) (*CollectionInfo, error) {
	if err := validateCollectionRequest(req.Database, req.Name); err != nil {
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

	// Check if collection already exists.
	metaKey := collectionMetadataKey(req.Database, req.Name)
	existing, err := s.kvSvc.Get(ctx, metaKey)
	if err == nil && existing != nil {
		return nil, &CollectionAlreadyExistsError{
			Database:   req.Database,
			Collection: req.Name,
		}
	}

	info := &CollectionInfo{
		Name:         req.Name,
		Database:     req.Database,
		IDStrategy:   strategy,
		FieldHash:    req.FieldHash,
		Schema:       make(map[string]string),
		Indexes:      req.Indexes,
		StorageCodec: req.StorageCodec,
		CreatedAt:    time.Now(),
	}

	data, err := json.Marshal(info)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal", Err: err}
	}

	if err := s.kvSvc.Put(ctx, metaKey, data); err != nil {
		return nil, &CollectionMetadataError{Op: "save", Err: err}
	}

	// Create secondary indexes when an index service is available.
	if s.indexSvc != nil && len(req.Indexes) > 0 {
		for _, def := range req.Indexes {
			idxName := collectionIndexName(req.Database, req.Name, def.FieldName)
			cfg := index.IndexConfig{
				Name:   idxName,
				Unique: def.Unique,
				Sparse: def.Sparse,
				// The collection's own document keyspace, and the only thing
				// this index may index. The index NAME says "{database}.
				// {collection}.{field}" but a name is a routing label, not a
				// key predicate: without this the apply path indexed every
				// JSON value in the state machine, another database's and
				// another tenant's included (go-qrdb#410).
				KeyPrefix: collectionDataEntityType(req.Database, req.Name) + "/",
				// A collection index definition has no case-sensitivity
				// field, so it keeps the behaviour it has always had. Stated
				// explicitly because index.IndexConfig names the field for the
				// DEFAULT and its zero value is case-INSENSITIVE, which the
				// engine now honours instead of hardcoding true (go-qrdb#34).
				CaseSensitive: true,
			}
			if err := s.indexSvc.CreateIndex(ctx, idxName, def.FieldName, cfg); err != nil {
				return nil, &CollectionMetadataError{Op: "create-index", Err: err}
			}
		}
	}

	// Initialize the process-local ID generator for this collection, unless
	// IDStrategyIncrement is routed through SequenceServicer instead (see
	// nextDocumentID), in which case there is nothing to prime: the
	// sequence's state lives in the replicated store, not in this process.
	if strategy != IDStrategyIncrement || s.seqSvc == nil {
		s.getOrCreateIDGen(info)
	}

	return info, nil
}

// DropCollection removes a collection: every document in it, every secondary
// index CreateCollection built for it, the replicated ID sequence backing
// IDStrategyIncrement, the process-local ID generator cache entry, and finally
// the metadata that makes the collection resolvable.
//
// The order -- documents, indexes, sequence, metadata -- is deliberate and
// mirrors Database.DropTable in pkg/sql/provider: dependent structures are
// reclaimed FIRST and the metadata key is deleted LAST. The metadata key is
// therefore the drop's own commit record, and every intermediate state is one
// a re-issued DropCollection completes.
//
// Documents go first because each delete retracts that document's entries from
// every index maintained over it, so the indexes are already empty by the time
// they are dropped.
//
// Sequence before documents would leave a LIVE, fully populated collection
// whose counter had restarted at 1 for as long as the document deletion ran,
// so a concurrent insert would reissue an ID an existing document already holds
// and silently overwrite it. That is the one ordering that turns a storage leak
// into data loss. Metadata before either would strand the dependents with
// nothing naming them: the retry that would have reclaimed them fails with
// CollectionNotFoundError first. The chosen order leaves at worst a live but
// EMPTY collection -- nothing remains for a restarted counter to collide with
// -- and the window is one metadata delete wide, closed by a retry.
//
// The cost is stated rather than hidden: interrupted between the indexes and
// the metadata, the collection still resolves and still accepts writes with its
// declared indexes -- and therefore its unique constraints -- no longer
// maintained. READS in that state fail rather than mislead (a query naming a
// dropped index returns "index not found"); writes are the unguarded side. It
// is still the SMALLEST window available, because every ordering has one.
// Closing it entirely needs a persisted "dropping" state that other verbs
// refuse, which changes the metadata format and is a design decision
// (go-qrdb#425), not an implementation one.
//
// A dependent that cannot be reclaimed FAILS the whole drop rather than being
// skipped, and for the same reason in both cases: the surviving metadata is
// what makes the drop retryable, and reporting success over an unreclaimed
// dependent is what produced go-qrdb#399 and go-qrdb#411. For indexes the
// engine's index-create handler is idempotent by NAME and the original
// definition wins, so an index left behind silently overrides the definition a
// later CreateCollection asks for on the same (database, collection, field) --
// a unique constraint the new collection requested then goes unenforced. A
// dependent reported as already absent is success: SequenceServicer.Delete
// requires that of implementations (see its doc comment), and indexAlreadyGone
// gives the index drop the same property, which is what lets a retry reach the
// metadata delete.
//
// Index ownership is derived from the NAME, not tracked. If an index called
// {database}.{collection}.{field} already existed when the collection was
// created -- through the explicit index API, or by a DAO -- then
// CreateCollection adopted it silently (its create was a name-idempotent
// no-op) and this drop destroys it. That is a real collision and it is
// deliberate rather than overlooked: the metadata declares the index as the
// collection's, and refusing to drop what the collection believes it owns
// re-opens the orphan. Distinguishing the two needs persisted ownership
// (go-qrdb#426), which is a separate capability.
func (s *CollectionService) DropCollection(ctx context.Context, req *DropCollectionRequest) error {
	if err := validateCollectionRequest(req.Database, req.Name); err != nil {
		return err
	}

	// The metadata carries the collection's index definitions, so the drop
	// reads it rather than merely probing for its existence: a drop that
	// cannot enumerate what the collection owns cannot finish correctly.
	// Unparseable metadata therefore fails the drop with a typed error
	// instead of half-dropping the collection -- every other verb on this
	// type already refuses a collection in that state.
	info, err := s.loadCollectionInfo(ctx, req.Database, req.Name)
	if err != nil {
		return err
	}

	// Delete all documents. Each delete retracts that document's entries
	// from every index maintained over it, so by the time the indexes are
	// dropped below they are already empty.
	dataPrefix := collectionDataEntityType(req.Database, req.Name) + "/"
	keys, err := s.kvSvc.List(ctx, dataPrefix)
	if err != nil {
		return &CollectionMetadataError{Op: "list-documents", Err: err}
	}
	for _, key := range keys {
		if err := s.kvSvc.Delete(ctx, key); err != nil {
			return &CollectionMetadataError{Op: "delete-document", Err: err}
		}
	}

	// Drop the secondary indexes CreateCollection built for this collection.
	if s.indexSvc != nil {
		for _, def := range info.Indexes {
			idxName := collectionIndexName(req.Database, req.Name, def.FieldName)
			if err := s.indexSvc.DropIndex(ctx, idxName); err != nil && !indexAlreadyGone(err) {
				return &CollectionMetadataError{Op: "drop-index", Err: err}
			}
		}
	}

	// Reclaim the replicated ID sequence. Issued unconditionally rather than
	// only for IDStrategyIncrement: the name is derived from (database,
	// collection) alone, so this does not depend on parsing the metadata,
	// and a collection with no counter costs one read and proposes nothing.
	if s.seqSvc != nil {
		if err := s.seqSvc.Delete(ctx, req.Database, collectionSequenceName(req.Name)); err != nil {
			return &CollectionMetadataError{Op: "delete-sequence", Err: err}
		}
	}

	// Delete metadata.
	metaKey := collectionMetadataKey(req.Database, req.Name)
	if err := s.kvSvc.Delete(ctx, metaKey); err != nil {
		return &CollectionMetadataError{Op: "delete-metadata", Err: err}
	}

	// Remove cached ID generator.
	cacheKey := req.Database + ":" + req.Name
	s.idGens.Delete(cacheKey)

	return nil
}

// ListCollections returns all collections in the specified database.
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
		// Refresh document count.
		info.DocumentCount = s.countDocuments(ctx, info.Database, info.Name)
		collections = append(collections, info)
	}

	return &ListCollectionsResponse{Collections: collections}, nil
}

// GetCollectionInfo returns metadata for a specific collection.
func (s *CollectionService) GetCollectionInfo(ctx context.Context, database, name string) (*CollectionInfo, error) {
	if err := validateCollectionRequest(database, name); err != nil {
		return nil, err
	}

	info, err := s.loadCollectionInfo(ctx, database, name)
	if err != nil {
		return nil, err
	}

	info.DocumentCount = s.countDocuments(ctx, database, name)
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
// The existence check is a read followed by a guarded-by-nothing write, so it
// closes the DETERMINISTIC overwrite -- the one a single caller reaches with
// no concurrency at all, which is the defect -- and not a race between two
// concurrent inserts claiming the same explicit ID. Closing that too means
// writing the document with KVServicer.PutIfAbsent, the only write with a
// defined outcome under concurrency; it is not used here because
// BackendKVService.PutIfAbsent refuses when the backend is not a
// database.AtomicBackend, and the dedicated-store bundle's StandaloneBackend
// is not one, so a guarded write would fail EVERY collection insert on that
// path. Tracked as its own change; the guard lives in this service either
// way, so moving it to a guarded write later changes no caller.
func (s *CollectionService) InsertDocument(ctx context.Context, req *InsertDocumentRequest) (*InsertDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if len(req.Document) == 0 {
		return nil, &CollectionEmptyDocumentError{}
	}

	info, err := s.loadCollectionInfo(ctx, req.Database, req.Collection)
	if err != nil {
		return nil, err
	}

	// Honour an explicit _id from the document when present and non-zero;
	// otherwise generate one using the collection's configured ID strategy.
	// Either way the "_id" key is consumed into the identity rather than
	// stored alongside the data -- see documentFields.
	fields := documentFields(req.Document)
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

	docKey := collectionDataEntityType(req.Database, req.Collection) + "/" + strconv.FormatUint(id, 10)
	if !mayOverwrite(info.IDStrategy, explicit) {
		occupied, err := s.documentExists(ctx, docKey)
		if err != nil {
			return nil, err
		}
		if occupied {
			return nil, &CollectionDocumentExistsError{
				Database:   req.Database,
				Collection: req.Collection,
				ID:         id,
				Explicit:   explicit,
			}
		}
	}

	// Serialize and store document using the resolved codec.
	c := s.resolveCodec(info)
	docData, err := marshalDocument(entity, c)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal-document", Err: err}
	}

	if req.WriteMode != 0 {
		if err := s.kvSvc.PutWithMode(ctx, docKey, docData, req.WriteMode); err != nil {
			return nil, &CollectionMetadataError{Op: "insert-document", Err: err}
		}
	} else {
		if err := s.kvSvc.Put(ctx, docKey, docData); err != nil {
			return nil, &CollectionMetadataError{Op: "insert-document", Err: err}
		}
	}

	// Update schema from document fields.
	s.updateSchema(ctx, info, fields)

	return &InsertDocumentResponse{ID: id}, nil
}

// GetDocument retrieves a document by ID.
func (s *CollectionService) GetDocument(ctx context.Context, req *GetDocumentRequest) (*GetDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if req.ID == 0 {
		return nil, &CollectionInvalidDocumentIDError{}
	}

	docKey := collectionDataEntityType(req.Database, req.Collection) + "/" + strconv.FormatUint(req.ID, 10)

	var (
		data []byte
		err  error
	)
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

	info, err := s.loadCollectionInfo(ctx, req.Database, req.Collection)
	if err != nil {
		return nil, err
	}

	var entity dao.DynamicEntity
	if err := unmarshalDocument(data, &entity, s.resolveCodec(info)); err != nil {
		return nil, &CollectionMetadataError{Op: "unmarshal-document", Err: err}
	}

	return &GetDocumentResponse{
		ID:       entity.ID,
		Document: entity.Fields,
	}, nil
}

// UpdateDocument updates an existing document in a collection.
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

	// Verify document exists.
	docKey := collectionDataEntityType(req.Database, req.Collection) + "/" + strconv.FormatUint(req.ID, 10)
	existing, err := s.kvSvc.Get(ctx, docKey)
	if err != nil || existing == nil {
		return nil, &CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
	}

	info, loadErr := s.loadCollectionInfo(ctx, req.Database, req.Collection)
	if loadErr != nil {
		return nil, loadErr
	}

	// The document's identity comes from the request, never from the body:
	// UpdateDocument addresses a document that already exists. A body that
	// also carries "_id" is carrying the identity key, not a field, so it is
	// consumed here for the same reason InsertDocument consumes it -- an
	// updated document must have the same shape as an inserted one.
	fields := documentFields(req.Document)
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

	// Update schema from new fields.
	s.updateSchema(ctx, info, fields)

	return &UpdateDocumentResponse{ID: req.ID}, nil
}

// DeleteDocument removes a document from a collection.
func (s *CollectionService) DeleteDocument(ctx context.Context, req *DeleteDocumentRequest) (*DeleteDocumentResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}
	if req.ID == 0 {
		return nil, &CollectionInvalidDocumentIDError{}
	}

	docKey := collectionDataEntityType(req.Database, req.Collection) + "/" + strconv.FormatUint(req.ID, 10)
	existing, err := s.kvSvc.Get(ctx, docKey)
	if err != nil || existing == nil {
		return nil, &CollectionDocumentNotFoundError{
			Database:   req.Database,
			Collection: req.Collection,
			ID:         req.ID,
		}
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

// ListDocuments lists documents in a collection with pagination.
func (s *CollectionService) ListDocuments(ctx context.Context, req *ListDocumentsRequest) (*ListDocumentsResponse, error) {
	if err := validateCollectionRequest(req.Database, req.Collection); err != nil {
		return nil, err
	}

	info, infoErr := s.loadCollectionInfo(ctx, req.Database, req.Collection)
	if infoErr != nil {
		return nil, infoErr
	}

	c := s.resolveCodec(info)
	dataPrefix := collectionDataEntityType(req.Database, req.Collection) + "/"

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
	sortStrings(keys)

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

// loadCollectionInfo loads collection metadata from storage.
func (s *CollectionService) loadCollectionInfo(ctx context.Context, database, name string) (*CollectionInfo, error) {
	metaKey := collectionMetadataKey(database, name)
	data, err := s.kvSvc.Get(ctx, metaKey)
	if err != nil || data == nil {
		return nil, &CollectionNotFoundError{
			Database:   database,
			Collection: name,
		}
	}

	var info CollectionInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, &CollectionMetadataError{Op: "unmarshal-metadata", Err: err}
	}

	return &info, nil
}

// countDocuments returns the number of documents in a collection.
func (s *CollectionService) countDocuments(ctx context.Context, database, collection string) int64 {
	dataPrefix := collectionDataEntityType(database, collection) + "/"
	keys, err := s.kvSvc.List(ctx, dataPrefix)
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
		if err := s.seqSvc.Advance(ctx, info.Database, collectionSequenceName(info.Name), id); err != nil {
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

// documentExists reports whether a document is stored under docKey.
//
// A read that FAILS is not an absent document: reporting absence on a
// transport error would let a failed read wave through the overwrite the
// caller is being protected from, so the error is returned and the insert
// fails. This is deliberately stricter than GetDocument, which treats any
// read failure as not-found because on a read path there is nothing to
// destroy.
func (s *CollectionService) documentExists(ctx context.Context, docKey string) (bool, error) {
	data, err := s.kvSvc.Get(ctx, docKey)
	if err != nil {
		if isKeyNotFound(err) {
			return false, nil
		}
		return false, &CollectionMetadataError{Op: "check-document", Err: err}
	}
	return data != nil, nil
}

// collectionSequenceName returns the SequenceServicer sequence name backing
// a collection's IDStrategyIncrement document IDs, namespaced by collection
// so multiple collections in the same database maintain independent
// counters.
func collectionSequenceName(collection string) string {
	return "__collection:" + collection
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
		id, err := s.seqSvc.Allocate(ctx, info.Database, collectionSequenceName(info.Name), 1)
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
	cacheKey := info.Database + ":" + info.Name
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
func (s *CollectionService) updateSchema(ctx context.Context, info *CollectionInfo, doc map[string]interface{}) {
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

	if !changed {
		return
	}

	data, err := json.Marshal(info)
	if err != nil {
		return
	}

	metaKey := collectionMetadataKey(info.Database, info.Name)
	_ = s.kvSvc.Put(ctx, metaKey, data)
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

// sortStrings sorts a string slice in place. Inlined to avoid importing
// the sort package for a single call site.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && strings.Compare(s[j-1], s[j]) > 0; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
