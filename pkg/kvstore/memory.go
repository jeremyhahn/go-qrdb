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

package kvstore

import (
	"bytes"
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
)

// Compile-time interface checks. The atomic one is the point: a store that
// exists for consumers to test SaveIfAbsent against must actually offer the
// capability, and must never be the degraded check-then-put it is standing in
// for.
var (
	_ KVStore       = (*MemoryKVStore)(nil)
	_ AtomicKVStore = (*MemoryKVStore)(nil)
	_ FencedCreator = (*MemoryKVStore)(nil)
)

// MemoryKVStore is a complete in-process KVStore: atomic conditional writes
// and REAL secondary-index maintenance, with no node, no Raft, and no disk.
//
// It exists because the alternative is worse. Under the DAO/Entity-first
// ruling every consumer repo builds DAO-backed stores, and until now the only
// concrete KVStore implementations were unexported, so each consumer had to
// hand-write a twelve-method index-aware fake to unit-test its DAOs. A fake
// whose index semantics are subtly wrong produces tests that prove nothing —
// and go-qrdb's own pkg/dao tests demonstrate the failure mode: their fake is
// populated by a test helper that inserts index entries by hand, so it never
// exercised index MAINTENANCE and never noticed that the production stores
// did none.
//
// This store maintains its indexes the way a real one does, on the write
// path, from the value's own JSON fields. Consumers get a store whose index
// behaviour they can trust, and qrdb gets one implementation to keep correct
// instead of N copies in N repos.
//
// Scope, stated honestly: single-process only. Its atomicity is a mutex, not
// consensus, so it is a correct AtomicKVStore for one process and is not a
// stand-in for cluster-wide uniqueness. Everything is held in memory and
// nothing survives the process.
//
// The zero value is not usable; construct with NewMemoryKVStore.
type MemoryKVStore struct {
	mu   sync.RWMutex
	data map[string][]byte

	// indexes maps the resolved index name to everything needed to maintain
	// and query it. One struct rather than parallel maps: the write, delete,
	// backfill, unique-check and query paths all have to agree on the field,
	// the prefix AND the value normalization, and five maps is five chances
	// for one path to miss one.
	indexes map[string]*memIndex
}

// memIndex is one registered index plus the settings every path must apply.
type memIndex struct {
	idx index.Index
	// field is the JSON field name whose value is indexed.
	field string
	// prefix is the "{entityType}/" key prefix this index covers, so a write
	// only touches indexes belonging to its own entity type.
	prefix string
	// unique carries the uniqueness constraint. index.ExtendedMemoryIndex
	// appends rather than enforcing, so the check lives in this store: an
	// accepted duplicate on an index declared unique is a constraint the
	// caller relies on and the store silently does not provide.
	unique bool
	// sparse omits entities whose indexed field is empty, so "missing" and
	// "empty" are not the same query.
	sparse bool
	// cfg drives index.NormalizeFieldValue, which is what makes a
	// case-insensitive index actually case-insensitive. Skipping it would
	// make a lookup for "Alpha" miss a record stored as "alpha" and return
	// empty with a nil error — the silent-empty class this store exists to
	// be trustworthy about.
	cfg index.IndexConfig
}

// term returns the normalized index term for a raw field VALUE, and whether
// the value is indexable at all. Every write, delete, backfill, unique-check
// and exact-match query path goes through it.
func (m *memIndex) term(value []byte) ([]byte, bool) {
	if value == nil {
		return nil, false
	}
	if m.sparse && len(value) == 0 {
		return nil, false
	}
	return index.NormalizeFieldValue(value, m.cfg)
}

// bound returns the normalized form of a range-scan BOUND. It is deliberately
// not term(): the two have different semantics and conflating them is a bug.
//
// A nil bound means "open" by contract, and must stay nil. An explicit empty
// bound is a real bound — the half-open range [min, "") is empty — and must
// NOT be turned into nil, which the underlying index reads as unbounded and
// answers with every entry. Sparse is a statement about which values get
// INDEXED, not about how to read a bound, so it does not apply here at all:
// running an explicit empty upper bound through term() on a sparse index
// returns (nil, false), and discarding that flag turns "match nothing" into
// "match everything".
//
// Case and numeric normalization DO apply, or the bounds and the stored terms
// are in different spaces and the range covers the wrong entries.
//
// A bound that cannot be normalized is an ERROR, not an empty range. That
// asymmetry is deliberate and matches index.InvalidNumericBoundError: an
// unparseable field VALUE means "this entry has no representable value" and
// is skipped, while an unparseable scan BOUND is a malformed query, and
// reporting it as an empty-but-valid result set is the silent-empty failure
// this whole change exists to remove. Returning the raw bytes instead would
// compare an ASCII bound against encoded terms and answer nonsense quietly.
func (m *memIndex) bound(name, op string, value []byte) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	normalized, ok := index.NormalizeFieldValue(value, m.cfg)
	if !ok {
		return nil, &index.InvalidNumericBoundError{
			IndexName: name,
			Op:        op,
			Value:     value,
			Err:       ErrInvalidScanBound,
		}
	}
	return normalized, nil
}

// NewMemoryKVStore returns an empty in-process KVStore that supports atomic
// conditional writes and maintains registered secondary indexes.
func NewMemoryKVStore() *MemoryKVStore {
	return &MemoryKVStore{
		data:    make(map[string][]byte),
		indexes: make(map[string]*memIndex),
	}
}

// Put stores value under key and updates every index registered for the
// key's entity type, removing the entries derived from the previous value
// first so a changed field does not leave a stale entry behind.
func (s *MemoryKVStore) Put(ctx context.Context, key string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putLocked(key, value)
}

// PutIfAbsent stores value under key only when key is absent, reporting
// whether this call created it. The check and the write happen under one
// lock, so concurrent callers in this process see exactly one winner.
func (s *MemoryKVStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[key]; exists {
		return false, nil
	}
	if err := s.putLocked(key, value); err != nil {
		return false, err
	}
	return true, nil
}

// PutIfAllAbsent stores value under key only when key and every fence key are
// absent. The mutex makes every absence check and the write one indivisible
// step; see FencedCreator.
func (s *MemoryKVStore) PutIfAllAbsent(ctx context.Context, key string, value []byte, fences ...string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.data[key]; exists {
		return false, nil
	}
	for _, f := range fences {
		if _, exists := s.data[f]; exists {
			return false, nil
		}
	}
	if err := s.putLocked(key, value); err != nil {
		return false, err
	}
	return true, nil
}

// CompareAndDelete removes key only when its stored value still equals
// expected. The mutex makes the compare-then-delete indivisible, which is
// what a read followed by a delete is not: two releasers would both see the
// same value and the second would remove whatever a third party had claimed
// in between (#41).
func (s *MemoryKVStore) CompareAndDelete(ctx context.Context, key string, expected []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.data[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	// A guarded delete is a DELETE: it must retract the index entries the
	// value had, exactly as Delete does. Removing only the primary leaves
	// QueryIndex and ScanIndex returning a key that is gone, and leaves a
	// stale UNIQUE entry rejecting a later legitimate insert of the same
	// value — the guard changes WHEN the delete happens, never what a delete
	// means.
	if err := s.deindexLocked(key, current); err != nil {
		return false, err
	}
	delete(s.data, key)
	return true, nil
}

// CompareAndSwap replaces key's value with next only when its stored value
// still equals expected, reporting whether the swap happened.
//
// An absent key is left absent: a swap moves a record from one state to
// another, which is not a statement about a record that does not exist. Use
// PutIfAbsent to create.
//
// The write goes through putLocked, so the index reconciliation is the SAME
// code an unguarded Put uses — the guard changes when the write happens, never
// what a write means. Reconciling by hand here would be a second definition of
// "what it means to store a value", and the two would drift.
func (s *MemoryKVStore) CompareAndSwap(ctx context.Context, key string, expected, next []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.data[key]
	if !ok || !bytes.Equal(current, expected) {
		return false, nil
	}
	if err := s.putLocked(key, next); err != nil {
		return false, err
	}
	return true, nil
}

// putLocked writes the value and reconciles the indexes. The caller holds
// the write lock.
func (s *MemoryKVStore) putLocked(key string, value []byte) error {
	if err := s.checkIndexableLocked(key, value); err != nil {
		return err
	}
	if err := s.checkUniqueLocked(key, value); err != nil {
		return err
	}
	if prev, exists := s.data[key]; exists {
		if err := s.deindexLocked(key, prev); err != nil {
			return err
		}
	}
	stored := make([]byte, len(value))
	copy(stored, value)
	s.data[key] = stored
	return s.indexLocked(key, stored)
}

// checkIndexableLocked refuses a write whose value cannot be decoded when an
// index covers its key prefix.
//
// Skipping such a value — which is what "not a JSON object, so no entries"
// used to do — reports successful index maintenance that did not happen. The
// record is then invisible to every index query and outside every unique
// constraint, both answering with nil errors. A DAO configured with the
// MsgPack or CBOR codec produces exactly these values, so this is reachable
// without doing anything unusual.
//
// A value under a prefix NO index covers is unconstrained: this store holds
// opaque blobs happily, it just will not pretend to index them.
func (s *MemoryKVStore) checkIndexableLocked(key string, value []byte) error {
	if _, ok := decodeJSONFields(value); ok {
		return nil
	}
	for name, mi := range s.indexes {
		if strings.HasPrefix(key, mi.prefix) {
			return &UnindexableValueError{Key: key, IndexName: name}
		}
	}
	return nil
}

// checkUniqueLocked refuses a write that would give a second entity a value
// already claimed on a unique index. It runs BEFORE any mutation, so a
// rejected write leaves both the data and the indexes untouched — a partial
// application is a state the caller cannot reason about.
func (s *MemoryKVStore) checkUniqueLocked(key string, value []byte) error {
	fields, ok := decodeJSONFields(value)
	if !ok {
		return nil
	}
	for name, mi := range s.indexes {
		if !mi.unique || !strings.HasPrefix(key, mi.prefix) {
			continue
		}
		raw, present := fields[mi.field]
		if !present {
			continue
		}
		term, ok := mi.term(unquoteJSON(raw))
		if !ok {
			continue
		}
		holders, err := mi.idx.Get(term)
		if err != nil {
			return err
		}
		for _, holder := range holders {
			if string(holder) != key {
				return &index.UniqueViolationError{
					IndexName:   name,
					FieldValue:  term,
					ExistingKey: holder,
					NewKey:      []byte(key),
				}
			}
		}
	}
	return nil
}

// indexLocked adds index entries for value under every index covering key's
// entity type. A value that is not a JSON object, or that omits the indexed
// field, simply produces no entry — the same sparse behaviour the engine has.
func (s *MemoryKVStore) indexLocked(key string, value []byte) error {
	fields, ok := decodeJSONFields(value)
	if !ok {
		return nil
	}
	for name, mi := range s.indexes {
		if !strings.HasPrefix(key, mi.prefix) {
			continue
		}
		raw, present := fields[mi.field]
		if !present {
			continue
		}
		term, ok := mi.term(unquoteJSON(raw))
		if !ok {
			continue
		}
		if err := mi.idx.Put([]byte(key), term); err != nil {
			return &IndexRegistrationError{EntityType: name, Err: err}
		}
	}
	return nil
}

// deindexLocked removes the index entries derived from value.
func (s *MemoryKVStore) deindexLocked(key string, value []byte) error {
	fields, ok := decodeJSONFields(value)
	if !ok {
		return nil
	}
	for name, mi := range s.indexes {
		if !strings.HasPrefix(key, mi.prefix) {
			continue
		}
		raw, present := fields[mi.field]
		if !present {
			continue
		}
		term, ok := mi.term(unquoteJSON(raw))
		if !ok {
			continue
		}
		if err := mi.idx.Delete([]byte(key), term); err != nil {
			return &IndexRegistrationError{EntityType: name, Err: err}
		}
	}
	return nil
}

// Get returns the value for key, or (nil, nil) when it does not exist.
func (s *MemoryKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.data[key]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(value))
	copy(out, value)
	return out, nil
}

// Delete removes key and the index entries derived from its value. Deleting
// an absent key is not an error.
func (s *MemoryKVStore) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prev, exists := s.data[key]
	if !exists {
		return nil
	}
	if err := s.deindexLocked(key, prev); err != nil {
		return err
	}
	delete(s.data, key)
	return nil
}

// Scan invokes fn for every key-value pair whose key has the given prefix,
// in ascending key order, stopping at the first error fn returns.
func (s *MemoryKVStore) Scan(ctx context.Context, prefix string, fn func(key string, value []byte) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.RLock()
	pairs := make(map[string][]byte, len(s.data))
	keys := make([]string, 0, len(s.data))
	for key, value := range s.data {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out := make([]byte, len(value))
		copy(out, value)
		pairs[key] = out
		keys = append(keys, key)
	}
	s.mu.RUnlock()

	sort.Strings(keys)
	for _, key := range keys {
		if err := fn(key, pairs[key]); err != nil {
			return err
		}
	}
	return nil
}

// List returns the keys matching prefix in ascending order.
func (s *MemoryKVStore) List(ctx context.Context, prefix string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for key := range s.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// Exists reports whether key is present.
func (s *MemoryKVStore) Exists(ctx context.Context, key string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[key]
	return ok, nil
}

// RegisterEntityIndexes creates one in-memory index per single-field
// definition, named "{entityType}.{FieldName}" to match the name pkg/dao
// looks up. Registration is idempotent and backfills from the keys already
// stored under "{entityType}/".
//
// A definition marked Unique is enforced on the write path: a second entity
// claiming a value another already holds is refused with
// *index.UniqueViolationError. The underlying index appends rather than
// enforcing, so the check lives in this store — accepting the duplicate would
// make the store report a constraint it does not provide.
//
// Compound definitions are rejected with ErrIndexUnsupported rather than
// accepted and left unmaintained: a caller that registers a compound index
// must learn here that this store cannot serve it, not discover it later as
// an empty query result.
func (s *MemoryKVStore) RegisterEntityIndexes(entityType string, indexes []EntityIndex) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ei := range indexes {
		if ei.IsCompound() {
			return ErrIndexUnsupported
		}
		name := ei.ResolvedName(entityType)
		if existing, exists := s.indexes[name]; exists {
			// Idempotent for an identical definition. A CONFLICTING one is
			// refused rather than silently keeping the first: sparse-then-
			// non-sparse loses the empty-string entries the second caller
			// expects, and non-sparse-then-sparse exposes entries the second
			// caller expects to be absent. Either way one of the two callers
			// gets answers that do not match the index it asked for.
			if existing.field != ei.JSONFieldName ||
				existing.unique != ei.Unique ||
				existing.sparse != ei.Sparse ||
				existing.cfg.CaseSensitive != ei.CaseSensitive ||
				existing.prefix != ei.ResolvedPrefix(entityType) {
				return &IndexRegistrationError{
					EntityType: entityType,
					Err:        &ConflictingIndexError{IndexName: name},
				}
			}
			continue
		}
		cfg := index.IndexConfig{
			Name:          name,
			CaseSensitive: ei.CaseSensitive,
		}
		if ei.Unique {
			cfg.Type = index.IndexTypeUnique
			cfg.Unique = true
		}
		idx, err := index.NewExtendedMemoryIndex(&cfg)
		if err != nil {
			return &IndexRegistrationError{EntityType: entityType, Err: err}
		}
		mi := &memIndex{
			idx:    idx,
			field:  ei.JSONFieldName,
			prefix: ei.ResolvedPrefix(entityType),
			unique: ei.Unique,
			sparse: ei.Sparse,
			cfg:    cfg,
		}

		// Backfill BEFORE publishing. A backfill can fail — a unique
		// definition over existing duplicate data is the obvious case — and
		// publishing first would leave a partially populated index visible,
		// which every later registration then skips as "already exists".
		// Queries against it would answer incompletely with a nil error:
		// exactly the silent-empty class this change exists to remove.
		if err := backfillIndex(mi, name, s.data); err != nil {
			return &IndexRegistrationError{EntityType: entityType, Err: err}
		}

		s.indexes[name] = mi
	}
	return nil
}

// backfillIndex populates a not-yet-published index from the values already
// in data. Without it, registering an index after the first write would
// answer queries about existing records with nothing — the same silent-empty
// failure one level down.
//
// It takes the index rather than reading it out of the store's map so a
// failure can be reported before anything is published: a half-built index
// that is visible is worse than no index, because it answers.
//
// A unique index rejects pre-existing duplicates. Accepting them would create
// an index whose declared constraint the data already violates, and the
// violation would only surface on some later unrelated write.
func backfillIndex(mi *memIndex, name string, data map[string][]byte) error {
	seen := make(map[string]string)
	for key, value := range data {
		if !strings.HasPrefix(key, mi.prefix) {
			continue
		}
		fields, ok := decodeJSONFields(value)
		if !ok {
			// Same rule as the write path: a value this store cannot decode
			// must not be silently left out of an index that covers it.
			return &UnindexableValueError{Key: key, IndexName: name}
		}
		raw, present := fields[mi.field]
		if !present {
			continue
		}
		term, indexable := mi.term(unquoteJSON(raw))
		if !indexable {
			continue
		}
		if mi.unique {
			if holder, dup := seen[string(term)]; dup {
				return &index.UniqueViolationError{
					IndexName:   name,
					FieldValue:  term,
					ExistingKey: []byte(holder),
					NewKey:      []byte(key),
				}
			}
			seen[string(term)] = key
		}
		if err := mi.idx.Put([]byte(key), term); err != nil {
			return err
		}
	}
	return nil
}

// QueryIndex returns the entity keys whose indexed field equals value.
// An unregistered index name is ErrIndexUnsupported, never an empty result.
func (s *MemoryKVStore) QueryIndex(ctx context.Context, indexName string, value []byte) ([][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The read lock is held through the WHOLE lookup, not just long enough to
	// fetch the index pointer.
	//
	// A Put updates an index in several steps under the write lock: remove
	// the previous value's term, replace the data, add the new term. The
	// index's own mutex prevents a data race, but it does not make those
	// steps one operation — a reader that took only the pointer could run
	// between them and find the key under NEITHER the old term nor the new
	// one, and report that as "no matches" with a nil error. That is the
	// silent-empty failure again, arriving through concurrency instead of
	// through a missing capability.
	s.mu.RLock()
	defer s.mu.RUnlock()

	mi, ok := s.indexes[indexName]
	if !ok {
		return nil, ErrIndexUnsupported
	}
	// The probe is normalized the same way the stored term was, or a
	// case-insensitive index answers "Alpha" with nothing while holding
	// "alpha".
	term, indexable := mi.term(value)
	if !indexable {
		// This (nil, nil) is NOT the forbidden kind. The index IS maintained;
		// the probe is simply a value it never stores — nil, or the empty
		// string on a sparse index — so "no matches" is the true answer, not
		// a refusal wearing an empty result. The forbidden case is a store
		// that cannot perform the lookup at all, which returns
		// ErrIndexUnsupported above.
		return nil, nil
	}
	return mi.idx.Get(term)
}

// ScanIndex returns the entity keys for indexed values in [start, end).
// An unregistered index name is ErrIndexUnsupported.
func (s *MemoryKVStore) ScanIndex(ctx context.Context, indexName string, start, end []byte) (map[string][][]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The read lock is held through the whole scan; see QueryIndex for why
	// taking only the index pointer lets a reader observe a Put's index
	// update half-applied.
	s.mu.RLock()
	defer s.mu.RUnlock()

	mi, ok := s.indexes[indexName]
	if !ok {
		return nil, ErrIndexUnsupported
	}
	// Both bounds are normalized like the stored terms, so the range covers
	// what it appears to cover on a case-insensitive index. bound() rather
	// than term(): a bound is not a value, and an explicit empty bound must
	// stay empty rather than becoming an open one.
	lo, err := mi.bound(indexName, "start", start)
	if err != nil {
		return nil, err
	}
	hi, err := mi.bound(indexName, "end", end)
	if err != nil {
		return nil, err
	}
	return mi.idx.Scan(lo, hi)
}

// QueryCompoundIndex refuses: this store maintains single-field indexes
// only, and RegisterEntityIndexes rejects compound definitions for the same
// reason.
func (s *MemoryKVStore) QueryCompoundIndex(_ context.Context, _ string, _ [][]byte) ([][]byte, error) {
	return nil, ErrIndexUnsupported
}

// ScanCompoundIndex refuses; see QueryCompoundIndex.
func (s *MemoryKVStore) ScanCompoundIndex(_ context.Context, _ string, _ [][]byte, _, _ []byte) (map[string][][]byte, error) {
	return nil, ErrIndexUnsupported
}

// decodeJSONFields returns the top-level fields of a JSON object value.
// ok is false when the value is not a JSON OBJECT — including the document
// `null`, which unmarshals into a nil map without error.
func decodeJSONFields(value []byte) (map[string]json.RawMessage, bool) {
	if len(value) == 0 {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(value, &fields); err != nil {
		return nil, false
	}
	// The document `null` unmarshals into a map WITHOUT error and leaves it
	// nil. Reporting that as a decodable object is fail-open: the write is
	// accepted under an indexed prefix and produces no entries, so the record
	// exists and is invisible to every index query, with no error anywhere.
	//
	// An empty OBJECT (`{}`) is different and must stay acceptable — it
	// decodes to a non-nil empty map, and simply carries no value for the
	// indexed field, which is the ordinary sparse case.
	if fields == nil {
		return nil, false
	}
	return fields, true
}

// unquoteJSON renders a JSON scalar as the raw bytes the index stores:
// strings lose their quotes and escapes, every other scalar keeps its
// literal form. It returns nil for JSON null and for values that are not
// scalars, so neither is indexed.
func unquoteJSON(raw json.RawMessage) []byte {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		return []byte(s)
	}
	if raw[0] == '{' || raw[0] == '[' {
		return nil
	}
	return []byte(raw)
}
