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

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	entitypkg "github.com/jeremyhahn/go-qrdb/pkg/entity"
	dberrors "github.com/jeremyhahn/go-qrdb/pkg/errors"
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kvstore"
)

// genericDAO implements GenericDAO using kvstore.KVStore.
// It works transparently with all QRDB storage backends.
// Every KVStore implementation provides index methods uniformly.
type genericDAO[E Entity] struct {
	store      kvstore.KVStore
	entityType string
	newEntity  func() E
	codec      Codec[E]
	timeout    time.Duration
	idGen      IDGenerator
	// database is the optional database namespace prefix used to scope
	// secondary index lookups. When non-empty, index names are resolved
	// as "{database}.{entityType}.{fieldName}".
	database string
}

// Ensure genericDAO implements GenericDAO.
var _ GenericDAO[Entity] = (*genericDAO[Entity])(nil)

// New creates a new GenericDAO for the given entity type.
//
// Parameters:
//   - store: The underlying KVStore (from server.GetKVStore(shardID))
//   - entityType: The namespace/partition for this entity type (e.g., "users", "sessions")
//   - newEntity: Factory function that creates new empty entity instances
//   - opts: Optional configuration (codec, timeout, IDGenerator)
//
// Example:
//
//	kvStore := server.GetKVStore(1)
//	userDAO, err := dao.New(kvStore, "users", func() *User { return &User{} })
func New[E Entity](
	store kvstore.KVStore,
	entityType string,
	newEntity func() E,
	opts ...Option,
) (GenericDAO[E], error) {
	if store == nil {
		return nil, InvalidConfigError{Field: "store", Reason: "cannot be nil"}
	}
	if entityType == "" {
		return nil, InvalidConfigError{Field: "entityType", Reason: "cannot be empty"}
	}
	if newEntity == nil {
		return nil, InvalidConfigError{Field: "newEntity", Reason: "cannot be nil"}
	}

	cfg := ParseOptions(opts...)

	codec, err := NewCodec[E](cfg.CodecType)
	if err != nil {
		return nil, err
	}

	idGen := cfg.IDGen
	if idGen == nil {
		idGen = &UnixNanoGenerator{}
	}

	dao := &genericDAO[E]{
		store:      store,
		entityType: entityType,
		newEntity:  newEntity,
		codec:      codec,
		timeout:    cfg.Timeout,
		idGen:      idGen,
		database:   cfg.Database,
	}

	// Parse struct tags for index definitions and register them.
	entity := newEntity()
	eType := reflect.TypeOf(entity)
	if eType.Kind() == reflect.Pointer {
		eType = eType.Elem()
	}

	indexMeta, parseErr := index.ParseIndexTags(eType)
	hasCompoundIndexes := false
	if cr, ok := any(entity).(index.CompoundIndexRegisterer); ok {
		hasCompoundIndexes = len(cr.CompoundIndexes()) > 0
	}
	if (parseErr == nil && len(indexMeta) > 0) || hasCompoundIndexes {
		// Convert IndexMetadata to EntityIndex definitions.
		entityIndexes := make([]kvstore.EntityIndex, 0, len(indexMeta))
		for fieldName, meta := range indexMeta {
			jsonName := jsonFieldName(eType, fieldName)
			entityIndexes = append(entityIndexes, kvstore.EntityIndex{
				FieldName:     fieldName,
				JSONFieldName: jsonName,
				Unique:        meta.Config.Unique || meta.Config.Type == index.IndexTypeUnique,
				Sparse:        meta.Config.Sparse,
				CaseSensitive: meta.Config.CaseSensitive,
			})
		}
		// Register compound indexes from CompoundIndexRegisterer interface.
		if cr, ok := any(entity).(index.CompoundIndexRegisterer); ok {
			for _, def := range cr.CompoundIndexes() {
				jsonNames := make([]string, len(def.Fields))
				for i, fieldName := range def.Fields {
					jsonNames[i] = jsonFieldName(eType, fieldName)
				}
				entityIndexes = append(entityIndexes, kvstore.EntityIndex{
					FieldNames:     def.Fields,
					JSONFieldNames: jsonNames,
					Unique:         def.Unique,
					Sparse:         def.Sparse,
					CaseSensitive:  def.CaseSensitive,
				})
			}
		}

		// Register indexes at the engine level (idempotent).
		_ = store.RegisterEntityIndexes(entityType, entityIndexes)
	}

	// Register entity type in global entity registry for SQL bridge.
	// The MarshalFunc/UnmarshalFunc closures capture the DAO's codec
	// so the EntityTable serializes/deserializes identically regardless
	// of which codec was selected (JSON, MsgPack, CBOR, etc.).
	idJSON, idGo := detectIDField(newEntity, eType)
	regFields := buildRegistrationFields(eType, indexMeta, idGo)
	_ = entitypkg.Global().Register(&entitypkg.Registration{
		EntityType:  entityType,
		StructType:  eType,
		Fields:      regFields,
		IDFieldJSON: idJSON,
		IDFieldGo:   idGo,
		Database:    cfg.Database,
		CodecType:   string(cfg.CodecType),
		NewFunc:     func() any { return newEntity() },
		MarshalFunc: func(v any) ([]byte, error) {
			e, ok := v.(E)
			if !ok {
				return nil, InvalidEntityError{Reason: "type mismatch"}
			}
			return codec.Marshal(e)
		},
		UnmarshalFunc: func(data []byte) (any, error) {
			e := newEntity()
			if err := codec.Unmarshal(data, &e); err != nil {
				return nil, err
			}
			return e, nil
		},
	})

	return dao, nil
}

// Save persists an entity to storage.
func (d *genericDAO[E]) Save(ctx context.Context, entity E) error {
	// Auto-generate ID if not set
	if entity.EntityID() == 0 {
		entity.SetEntityID(d.idGen.NextID(entity))
	}

	// Serialize entity
	data, err := d.codec.Marshal(entity)
	if err != nil {
		return SerializationFailedError{EntityType: d.entityType, Cause: err}
	}

	// Apply timeout if not already set
	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	// Store in KVStore
	key := d.makeKey(entity.EntityID())
	return d.store.Put(ctx, key, data)
}

// SaveIfAbsent persists an entity only when its key is absent.
//
// The condition is delegated to the KVStore's atomic primitive rather than
// evaluated here. Checking with Get and then calling Put would reintroduce the
// window the operation exists to close: two callers would both observe the key
// missing and both write, so a single-use record could be created twice.
//
// A store that cannot perform an atomic conditional write is refused rather
// than emulated — see kvstore.AtomicKVStore for why that capability is optional
// and why silently degrading would be worse than failing.
func (d *genericDAO[E]) SaveIfAbsent(ctx context.Context, entity E) (bool, error) {
	atomicStore, ok := d.store.(kvstore.AtomicKVStore)
	if !ok {
		return false, ErrAtomicWriteUnsupported
	}

	if entity.EntityID() == 0 {
		entity.SetEntityID(d.idGen.NextID(entity))
	}

	data, err := d.codec.Marshal(entity)
	if err != nil {
		return false, SerializationFailedError{EntityType: d.entityType, Cause: err}
	}

	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	return atomicStore.PutIfAbsent(ctx, d.makeKey(entity.EntityID()), data)
}

// SaveWithCondition persists an entity only if the given condition is satisfied.
// It wraps the operation in a single-operation transaction for atomicity.
func (d *genericDAO[E]) SaveWithCondition(ctx context.Context, entity E, cond Condition) error {
	tx, err := d.beginTx(ctx)
	if err != nil {
		return err
	}
	if err := tx.SaveWithCondition(entity, cond); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit(ctx)
}

// beginTx creates a DAOTransaction for this genericDAO.
// This is used internally by SaveWithCondition to wrap single-operation
// conditional saves in a transaction.
func (d *genericDAO[E]) beginTx(_ context.Context) (DAOTransaction[E], error) {
	txDAO := &transactionalDAO[E]{
		genericDAO: d,
	}
	return newDAOTransaction(txDAO), nil
}

// Get retrieves an entity by ID.
func (d *genericDAO[E]) Get(ctx context.Context, id uint64) (E, error) {
	var zero E

	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	key := d.makeKey(id)
	data, err := d.store.Get(ctx, key)
	if err != nil {
		// Check if it's a not found error
		if isNotFoundError(err) {
			return zero, NotFoundError{EntityType: d.entityType, ID: id}
		}
		return zero, err
	}

	entity := d.newEntity()
	if err := d.codec.Unmarshal(data, &entity); err != nil {
		return zero, DeserializationFailedError{EntityType: d.entityType, Cause: err}
	}

	return entity, nil
}

// Delete removes an entity from storage.
func (d *genericDAO[E]) Delete(ctx context.Context, entity E) error {
	id := entity.EntityID()
	if id == 0 {
		return InvalidEntityIDError{EntityType: d.entityType, Reason: "cannot delete entity with ID 0"}
	}

	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	key := d.makeKey(id)
	return d.store.Delete(ctx, key)
}

// Count returns the total number of entities.
func (d *genericDAO[E]) Count(ctx context.Context) (int, error) {
	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	count := 0
	err := d.store.Scan(ctx, d.prefixKey(), func(_ string, _ []byte) error {
		count++
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// Page retrieves a page of entities.
func (d *genericDAO[E]) Page(ctx context.Context, query PageQuery) (PageResult[E], error) {
	// Normalize query parameters
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 25
	}

	result := PageResult[E]{
		Entities: make([]E, 0, query.PageSize),
		Page:     query.Page,
		PageSize: query.PageSize,
	}

	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	// First, count total entities for pagination metadata
	total, err := d.Count(ctx)
	if err != nil {
		return result, err
	}
	result.Total = total

	offset := (query.Page - 1) * query.PageSize
	current := 0
	collected := 0

	err = d.store.Scan(ctx, d.prefixKey(), func(_ string, value []byte) error {
		// Skip until we reach the offset
		if current < offset {
			current++
			return nil
		}

		// Check if we've collected enough for this page
		if collected >= query.PageSize {
			// We found one more, so there's more data
			result.HasMore = true
			return ErrStopIteration
		}

		entity := d.newEntity()
		if err := d.codec.Unmarshal(value, &entity); err != nil {
			// Log and skip malformed entities
			return nil
		}

		result.Entities = append(result.Entities, entity)
		collected++
		current++
		return nil
	})

	if err != nil && !errors.Is(err, ErrStopIteration) {
		return result, err
	}

	// Apply SortField if specified.
	if query.SortField != "" && len(result.Entities) > 1 {
		sortEntitiesByField(result.Entities, query.SortField, query.SortOrder)
	}

	return result, nil
}

// ForEachPage iterates over all entities page by page.
// The callback receives the full PageResult including pagination metadata.
// The callback is always called at least once, even for empty result sets.
func (d *genericDAO[E]) ForEachPage(ctx context.Context, query PageQuery, fn PagerFunc[E]) error {
	currentPage := query.Page
	if currentPage < 1 {
		currentPage = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 25
	}

	for {
		q := PageQuery{
			Page:      currentPage,
			PageSize:  query.PageSize,
			SortOrder: query.SortOrder,
			SortField: query.SortField,
		}

		result, err := d.Page(ctx, q)
		if err != nil {
			return err
		}

		// Always call the callback, even for empty results
		if err := fn(result); err != nil {
			if errors.Is(err, ErrStopIteration) {
				return nil
			}
			return err
		}

		if !result.HasMore {
			break
		}

		currentPage++
	}

	return nil
}

// EntityType returns the entity type name.
func (d *genericDAO[E]) EntityType() string {
	return d.entityType
}

// makeKey constructs a storage key for an entity ID.
// Format: <entityType>/<id>.
func (d *genericDAO[E]) makeKey(id uint64) string {
	return fmt.Sprintf("%s/%d", d.entityType, id)
}

// prefixKey returns the prefix for scanning all entities of this type.
func (d *genericDAO[E]) prefixKey() string {
	return d.entityType + "/"
}

// indexName constructs the storage index name for a field. When a database
// namespace is configured, the name is "{database}.{entityType}.{fieldName}"
// for per-database isolation; otherwise it uses the legacy
// "{entityType}.{fieldName}" format for backward compatibility.
func (d *genericDAO[E]) indexName(fieldName string) string {
	if d.database != "" {
		return d.database + "." + d.entityType + "." + fieldName
	}
	return d.entityType + "." + fieldName
}

// ensureTimeout returns a context with the DAO's timeout applied.
// If the context already has a deadline, it is returned unchanged.
func (d *genericDAO[E]) ensureTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, d.timeout) // #nosec G118 -- cancel returned to caller
}

// QueryByIndex returns all entities matching the given field value.
func (d *genericDAO[E]) QueryByIndex(ctx context.Context, fieldName string, value interface{}) ([]E, error) {
	indexName := d.indexName(fieldName)
	fieldValueBytes := []byte(fmt.Sprint(value))

	keys, err := d.store.QueryIndex(ctx, indexName, fieldValueBytes)
	if err != nil {
		return nil, err
	}

	entities := make([]E, 0, len(keys))
	prefix := d.prefixKey()
	for _, k := range keys {
		keyStr := string(k)
		if !strings.HasPrefix(keyStr, prefix) {
			continue
		}
		idStr := keyStr[len(prefix):]
		id, parseErr := strconv.ParseUint(idStr, 10, 64)
		if parseErr != nil {
			continue
		}
		entity, getErr := d.Get(ctx, id)
		if getErr != nil {
			if isNotFoundError(getErr) || IsNotFound(getErr) {
				continue
			}
			return nil, getErr
		}
		entities = append(entities, entity)
	}

	return entities, nil
}

// QueryOneByIndex returns a single entity matching the given field value.
// Returns NotFoundError if no matching entity exists.
func (d *genericDAO[E]) QueryOneByIndex(ctx context.Context, fieldName string, value interface{}) (E, error) {
	var zero E

	entities, err := d.QueryByIndex(ctx, fieldName, value)
	if err != nil {
		return zero, err
	}

	if len(entities) == 0 {
		return zero, NotFoundError{EntityType: d.entityType, ID: 0}
	}

	return entities[0], nil
}

// ScanByIndex returns all entities with indexed field values in the
// range [start, end).
func (d *genericDAO[E]) ScanByIndex(ctx context.Context, fieldName string, start, end interface{}) ([]E, error) {
	indexName := d.indexName(fieldName)
	startBytes := []byte(fmt.Sprint(start))
	endBytes := []byte(fmt.Sprint(end))

	scanResult, err := d.store.ScanIndex(ctx, indexName, startBytes, endBytes)
	if err != nil {
		return nil, err
	}

	// Collect unique entity keys to avoid duplicates across scan buckets.
	seen := make(map[string]struct{})
	var entities []E
	prefix := d.prefixKey()

	for _, keys := range scanResult {
		for _, k := range keys {
			keyStr := string(k)
			if _, exists := seen[keyStr]; exists {
				continue
			}
			seen[keyStr] = struct{}{}

			if !strings.HasPrefix(keyStr, prefix) {
				continue
			}
			idStr := keyStr[len(prefix):]
			id, parseErr := strconv.ParseUint(idStr, 10, 64)
			if parseErr != nil {
				continue
			}
			entity, getErr := d.Get(ctx, id)
			if getErr != nil {
				if isNotFoundError(getErr) || IsNotFound(getErr) {
					continue
				}
				return nil, getErr
			}
			entities = append(entities, entity)
		}
	}

	return entities, nil
}

// PageByIndex returns a page of entities matching the given index field value.
func (d *genericDAO[E]) PageByIndex(ctx context.Context, fieldName string, value interface{}, query PageQuery) (PageResult[E], error) {
	if query.Page < 1 {
		query.Page = 1
	}
	if query.PageSize < 1 {
		query.PageSize = 25
	}

	result := PageResult[E]{
		Entities: make([]E, 0, query.PageSize),
		Page:     query.Page,
		PageSize: query.PageSize,
	}

	indexName := d.indexName(fieldName)
	fieldValueBytes := []byte(fmt.Sprint(value))

	allKeys, err := d.store.QueryIndex(ctx, indexName, fieldValueBytes)
	if err != nil {
		return result, err
	}

	result.Total = len(allKeys)

	offset := (query.Page - 1) * query.PageSize
	if offset >= len(allKeys) {
		return result, nil
	}

	end := offset + query.PageSize
	if end > len(allKeys) {
		end = len(allKeys)
	}
	result.HasMore = end < len(allKeys)

	prefix := d.prefixKey()
	for _, k := range allKeys[offset:end] {
		keyStr := string(k)
		if !strings.HasPrefix(keyStr, prefix) {
			continue
		}
		idStr := keyStr[len(prefix):]
		id, parseErr := strconv.ParseUint(idStr, 10, 64)
		if parseErr != nil {
			continue
		}
		entity, getErr := d.Get(ctx, id)
		if getErr != nil {
			if isNotFoundError(getErr) || IsNotFound(getErr) {
				continue
			}
			return result, getErr
		}
		result.Entities = append(result.Entities, entity)
	}

	if query.SortField != "" && len(result.Entities) > 1 {
		sortEntitiesByField(result.Entities, query.SortField, query.SortOrder)
	}

	return result, nil
}

// CountByIndex returns the count of entities matching the given index value
// without loading entities.
func (d *genericDAO[E]) CountByIndex(ctx context.Context, fieldName string, value interface{}) (int, error) {
	indexName := d.indexName(fieldName)
	keys, err := d.store.QueryIndex(ctx, indexName, []byte(fmt.Sprint(value)))
	if err != nil {
		return 0, err
	}
	return len(keys), nil
}

// QueryByCompoundIndex returns all entities matching the given compound
// index field values. All values must match exactly.
func (d *genericDAO[E]) QueryByCompoundIndex(ctx context.Context, indexName string, fieldValues []interface{}) ([]E, error) {
	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	values := make([][]byte, len(fieldValues))
	for i, v := range fieldValues {
		values[i] = []byte(fmt.Sprint(v))
	}

	keys, err := d.store.QueryCompoundIndex(ctx, indexName, values)
	if err != nil {
		return nil, err
	}

	return d.loadEntities(ctx, keys)
}

// ScanByCompoundIndex returns entities matching a prefix of compound index
// fields with an optional range on the next field.
func (d *genericDAO[E]) ScanByCompoundIndex(ctx context.Context, indexName string, prefixValues []interface{}, rangeStart, rangeEnd interface{}) ([]E, error) {
	ctx, cancel := d.ensureTimeout(ctx)
	defer cancel()

	prefix := make([][]byte, len(prefixValues))
	for i, v := range prefixValues {
		prefix[i] = []byte(fmt.Sprint(v))
	}

	var start, end []byte
	if rangeStart != nil {
		start = []byte(fmt.Sprint(rangeStart))
	}
	if rangeEnd != nil {
		end = []byte(fmt.Sprint(rangeEnd))
	}

	result, err := d.store.ScanCompoundIndex(ctx, indexName, prefix, start, end)
	if err != nil {
		return nil, err
	}

	// Collect all entity keys from the scan result.
	var allKeys [][]byte
	seen := make(map[string]struct{})
	for _, entityKeys := range result {
		for _, key := range entityKeys {
			keyStr := string(key)
			if _, exists := seen[keyStr]; !exists {
				allKeys = append(allKeys, key)
				seen[keyStr] = struct{}{}
			}
		}
	}

	return d.loadEntities(ctx, allKeys)
}

// loadEntities loads entities by their storage keys using the same
// pattern as QueryByIndex: parse the key to extract the entity ID,
// then use Get for type-safe retrieval.
func (d *genericDAO[E]) loadEntities(ctx context.Context, keys [][]byte) ([]E, error) {
	entities := make([]E, 0, len(keys))
	prefix := d.prefixKey()
	for _, k := range keys {
		keyStr := string(k)
		if !strings.HasPrefix(keyStr, prefix) {
			continue
		}
		idStr := keyStr[len(prefix):]
		id, parseErr := strconv.ParseUint(idStr, 10, 64)
		if parseErr != nil {
			continue
		}
		entity, getErr := d.Get(ctx, id)
		if getErr != nil {
			if isNotFoundError(getErr) || IsNotFound(getErr) {
				continue
			}
			return nil, getErr
		}
		entities = append(entities, entity)
	}
	return entities, nil
}

// sortEntitiesByField sorts a slice of entities by the given struct field name.
func sortEntitiesByField[E any](entities []E, fieldName string, order int) {
	sort.SliceStable(entities, func(i, j int) bool {
		vi := fieldValue(entities[i], fieldName)
		vj := fieldValue(entities[j], fieldName)
		if order == SortDescending {
			return vi > vj
		}
		return vi < vj
	})
}

// fieldValue extracts a string representation of a struct field value via reflection.
func fieldValue(entity interface{}, fieldName string) string {
	v := reflect.ValueOf(entity)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	f := v.FieldByName(fieldName)
	if !f.IsValid() {
		return ""
	}
	return fmt.Sprint(f.Interface())
}

// jsonFieldName returns the JSON tag name for a struct field.
// Falls back to the Go field name if no json tag is set.
func jsonFieldName(structType reflect.Type, fieldName string) string {
	field, ok := structType.FieldByName(fieldName)
	if !ok {
		return fieldName
	}
	tag := field.Tag.Get("json")
	if tag == "" || tag == "-" {
		return fieldName
	}
	// Parse the tag; JSON tags can be "name,omitempty".
	if idx := strings.IndexByte(tag, ','); idx != -1 {
		tag = tag[:idx]
	}
	if tag == "" {
		return fieldName
	}
	return tag
}

// isNotFoundError checks if an error indicates "not found".
// Uses errors.As to properly handle wrapped errors.
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	// Check for QRDB's error type using errors.As for wrapped errors
	var dbErr *dberrors.QRDBError
	if errors.As(err, &dbErr) {
		return dbErr.Code == dberrors.ErrNotFound
	}
	return false
}

// buildRegistrationFields converts struct reflection data and index metadata
// into entity.FieldMeta for the entity registry. Each exported field is
// included; index metadata enriches fields that have index tags.
func buildRegistrationFields(eType reflect.Type, indexMeta map[string]*index.IndexMetadata, idFieldGo string) []entitypkg.FieldMeta {
	fields := make([]entitypkg.FieldMeta, 0, eType.NumField())
	for i := 0; i < eType.NumField(); i++ {
		sf := eType.Field(i)
		if !sf.IsExported() {
			continue
		}
		fm := entitypkg.FieldMeta{
			GoName:       sf.Name,
			JSONName:     jsonFieldName(eType, sf.Name),
			GoType:       sf.Type,
			GoKind:       sf.Type.Kind(),
			IsPrimaryKey: sf.Name == idFieldGo,
		}
		if meta, ok := indexMeta[sf.Name]; ok {
			fm.Indexed = true
			fm.Unique = meta.Config.Unique || meta.Config.Type == index.IndexTypeUnique
			fm.Sparse = meta.Config.Sparse
			fm.CaseSensitive = meta.Config.CaseSensitive
		}
		fields = append(fields, fm)
	}
	return fields
}

// idSentinel is the value used to detect which struct field holds the
// entity ID. SetEntityID sets this value, then we scan all uint64 fields
// to find the one that matches.
const idSentinel = uint64(0xDEADBEEFCAFEBABE)

// detectIDField identifies the Go and JSON names of the entity ID field
// by using a sentinel value. It creates a temporary entity, calls
// SetEntityID with the sentinel, then scans struct fields to find the
// matching uint64 field.
func detectIDField[E Entity](newEntity func() E, eType reflect.Type) (jsonName, goName string) {
	e := newEntity()
	e.SetEntityID(idSentinel)
	v := reflect.ValueOf(e)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	for i := 0; i < eType.NumField(); i++ {
		sf := eType.Field(i)
		if !sf.IsExported() {
			continue
		}
		fv := v.Field(i)
		if fv.Kind() == reflect.Uint64 && fv.Uint() == idSentinel {
			return jsonFieldName(eType, sf.Name), sf.Name
		}
	}
	// Fallback: assume first uint64 field or conventional names.
	return "id", "ID"
}
