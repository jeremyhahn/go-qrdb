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
}

// kvService is the minimal KV interface needed by CollectionService.
// It matches the subset of KVServicer used for collection operations.
type kvService interface {
	Put(ctx context.Context, key string, value []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
	Scan(ctx context.Context, prefix string) (map[string][]byte, error)
	PutWithMode(ctx context.Context, key string, value []byte, mode writemode.Mode) error
	DeleteWithMode(ctx context.Context, key string, mode writemode.Mode) error
	GetWithConsistency(ctx context.Context, key string, opts consistency.ReadOptions) ([]byte, error)
	ListWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) ([]string, error)
	ScanWithConsistency(ctx context.Context, prefix string, opts consistency.ReadOptions) (map[string][]byte, error)
}

// indexService is the minimal index interface needed by CollectionService
// to create and drop secondary indexes on collection fields.
type indexService interface {
	CreateIndex(ctx context.Context, name, fieldName string, config index.IndexConfig) error
	DropIndex(ctx context.Context, name string) error
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

// WithDatabaseCodecLookup returns a copy of the CollectionService configured
// with a callback that resolves the default storage codec for a given database.
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
			idxName := req.Database + "." + req.Name + "." + def.FieldName
			cfg := index.IndexConfig{
				Name:   idxName,
				Unique: def.Unique,
				Sparse: def.Sparse,
			}
			if err := s.indexSvc.CreateIndex(ctx, idxName, def.FieldName, cfg); err != nil {
				return nil, &CollectionMetadataError{Op: "create-index", Err: err}
			}
		}
	}

	// Initialize ID generator for this collection.
	s.getOrCreateIDGen(info)

	return info, nil
}

// DropCollection removes a collection, its metadata, and all documents.
func (s *CollectionService) DropCollection(ctx context.Context, req *DropCollectionRequest) error {
	if err := validateCollectionRequest(req.Database, req.Name); err != nil {
		return err
	}

	metaKey := collectionMetadataKey(req.Database, req.Name)
	existing, err := s.kvSvc.Get(ctx, metaKey)
	if err != nil || existing == nil {
		return &CollectionNotFoundError{
			Database:   req.Database,
			Collection: req.Name,
		}
	}

	// Delete all documents.
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

	// Delete metadata.
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
	entity := &dao.DynamicEntity{Fields: req.Document}
	id := extractExplicitID(req.Document)
	if id == 0 {
		idGen := s.getOrCreateIDGen(info)
		id = idGen.NextID(entity)
	}
	entity.SetEntityID(id)

	// Serialize and store document using the resolved codec.
	c := s.resolveCodec(info)
	docData, err := marshalDocument(entity, c)
	if err != nil {
		return nil, &CollectionMetadataError{Op: "marshal-document", Err: err}
	}

	docKey := collectionDataEntityType(req.Database, req.Collection) + "/" + strconv.FormatUint(id, 10)
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
	s.updateSchema(ctx, info, req.Document)

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

	entity := &dao.DynamicEntity{
		ID:     req.ID,
		Fields: req.Document,
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
	s.updateSchema(ctx, info, req.Document)

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

// resolveCodec returns the codec to use for encoding documents in the given
// extractExplicitID checks if the document map contains an "_id" field with
// a non-zero numeric value and returns it. Returns 0 when no explicit ID is
// present, signalling that the caller should auto-generate one.
func extractExplicitID(doc map[string]interface{}) uint64 {
	raw, ok := doc["_id"]
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

// getOrCreateIDGen returns the cached ID generator for a collection,
// creating one if it does not exist.
func (s *CollectionService) getOrCreateIDGen(info *CollectionInfo) dao.IDGenerator {
	cacheKey := info.Database + ":" + info.Name
	if gen, ok := s.idGens.Load(cacheKey); ok {
		return gen.(dao.IDGenerator)
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
	return actual.(dao.IDGenerator)
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
