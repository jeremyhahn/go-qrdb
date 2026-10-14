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

// Package pebbleindex is the index.Index implementation over a caller-owned
// *pebble.DB. It is a package of its own so that pkg/index, which every
// entity/DAO application compiles, does not import pebble.
package pebbleindex

import (
	"bytes"
	"encoding/binary"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"

	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/index/internal/indexkey"
)

// entityKeyLenSize is the byte length of the uint32 big-endian length suffix
// that trails every entity key in a composite index key.
const entityKeyLenSize = 4

// Index is a PebbleDB-backed implementation of the index.Index interface.
// Keys are stored with the composite format:
//
//	"idx:" + name + ":" + fieldValue + ":" + entityKey + uint32BE(len(entityKey))
//
// The trailing 4-byte length suffix allows the parser to read backward and
// extract the variable-length entity key. Index does not own the PebbleDB
// instance and will not close it.
//
// When valueType is index.IndexValueTypeNumeric, field values are encoded as
// 8-byte big-endian fixed-width integers (with sign-bit flip) so that byte
// ordering matches numeric ordering. This makes range scans like [1, 222)
// correct — lexicographic comparison of raw strings would incorrectly include
// 1234.
type Index struct {
	db        *pebble.DB
	name      string
	unique    bool
	valueType index.IndexValueType
	// caseInsensitive folds string field values inside encodeValue and
	// resolveScanBound, so writes, lookups, deletes and scan bounds all live
	// in the same folded domain. Folding on only some of those paths is worse
	// than not folding at all: a lookup would answer empty with a nil error
	// over an index holding the match.
	caseInsensitive bool
	closed          atomic.Bool
}

// New creates a new PebbleDB-backed index with the given name. The caller
// retains ownership of the PebbleDB instance. Returns an index.NilDBError if
// db is nil or an index.EmptyNameError if name is empty.
//
// When valueType is index.IndexValueTypeNumeric, field values are encoded as
// fixed-width 8-byte big-endian integers with a sign-bit flip so that byte
// ordering matches numeric ordering.
func New(db *pebble.DB, name string, unique bool, valueType index.IndexValueType) (*Index, error) {
	return NewWithConfig(db, name, index.IndexConfig{
		Unique:        unique,
		ValueType:     valueType,
		CaseSensitive: true,
	})
}

// NewWithConfig creates a new PebbleDB-backed index configured by config.
// Unique, ValueType and CaseSensitive are the meaningful fields.
//
// Note config.CaseSensitive is named for the DEFAULT, so its zero value folds
// case. Callers that mean "the ordinary index" must set it to true, which is
// what New does.
//
// CaseSensitive is ignored for a numeric index: its values are parsed as
// numbers and encoded to a fixed-width form in which case has no meaning.
func NewWithConfig(db *pebble.DB, name string, config index.IndexConfig) (*Index, error) {
	if db == nil {
		return nil, &index.NilDBError{}
	}
	if name == "" {
		return nil, &index.EmptyNameError{}
	}
	return &Index{
		db:              db,
		name:            name,
		unique:          config.Unique,
		valueType:       config.ValueType,
		caseInsensitive: !config.CaseSensitive && config.ValueType != index.IndexValueTypeNumeric,
	}, nil
}

// encodeValue encodes a raw field value according to the index value type.
// Returns the encoded value and true on success. Returns nil and false when
// the value cannot be encoded (e.g. a non-numeric string for a numeric index);
// callers must skip the index entry in that case.
func (p *Index) encodeValue(raw []byte) ([]byte, bool) {
	if p.valueType != index.IndexValueTypeNumeric {
		if p.caseInsensitive {
			return []byte(strings.ToLower(string(raw))), true
		}
		return raw, true
	}
	encoded, err := index.EncodeNumericValue(string(raw))
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// resolveScanBound encodes a Scan boundary value produced by the query
// planner. For a case-insensitive string index the bound is folded the same
// way encodeValue folds a stored term, so the two are compared in one domain;
// without that an upper bound of "M" would exclude every folded term, since
// 'm' > 'M' in byte order. For other string indexes the raw bytes are used
// unchanged: this also covers
// the exclusivity marker convention used by pkg/sql/provider/range_translator.go,
// where a trailing 0x00 byte is appended to a bound to advance it past the
// exact value it was derived from — lexicographic byte comparison already
// makes this correct for raw string keys.
//
// For numeric indexes that same trailing-0x00 marker cannot be applied to
// the decimal text before encoding, because encoding re-parses the text as
// a float and a trailing NUL byte is not valid float syntax. Instead the
// marker is resolved AFTER encoding: the decimal text (with the marker
// stripped, if present) is parsed and encoded to its fixed-width 8-byte
// form, and if the marker was present, the exact successor of the encoded
// bytes is returned so exclusivity is expressed correctly in the encoded
// numeric byte domain — including across the sign-flip boundary used for
// negative numbers — rather than being corrupted by re-parsing mangled text.
//
// Returns an index.InvalidNumericBoundError when a numeric bound cannot be parsed.
// This must be treated as a hard failure by callers, never silently mapped
// to an empty result: a malformed bound is a caller error, not "no matches".
func (p *Index) resolveScanBound(raw []byte, op string) ([]byte, error) {
	if p.valueType != index.IndexValueTypeNumeric {
		if p.caseInsensitive {
			return []byte(strings.ToLower(string(raw))), nil
		}
		return raw, nil
	}
	return index.ResolveNumericScanBound(raw, p.name, op)
}

// decodeValue reverses the encoding applied by encodeValue. For numeric
// indexes it converts the 8-byte representation back to a decimal string.
func (p *Index) decodeValue(encoded []byte) string {
	if p.valueType != index.IndexValueTypeNumeric {
		return string(encoded)
	}
	s, err := index.DecodeNumericValue(encoded)
	if err != nil {
		return string(encoded)
	}
	return s
}

// Name returns the index name.
func (p *Index) Name() string {
	return p.name
}

// IsUnique returns true if the index enforces a unique constraint.
func (p *Index) IsUnique() bool {
	return p.unique
}

// Put adds an index entry mapping fieldValue to entityKey. If the index
// enforces uniqueness and a different entity already maps to the same
// field value, an index.UniqueViolationError is returned. The entry is stored
// with an empty value; the composite key encodes all information.
// For numeric indexes, fieldValue is encoded as a fixed-width 8-byte
// big-endian integer. Non-numeric values are silently skipped.
func (p *Index) Put(entityKey []byte, fieldValue []byte) error {
	if p.closed.Load() {
		return &index.ClosedError{Name: p.name}
	}
	if fieldValue == nil {
		return &index.NilFieldValueError{Op: "put"}
	}

	encoded, ok := p.encodeValue(fieldValue)
	if !ok {
		return nil // non-numeric value; skip silently
	}

	if p.unique {
		existing, err := p.getEntityKeys(encoded)
		if err != nil {
			return err
		}
		if len(existing) > 0 && bytes.Equal(existing[0], entityKey) {
			// Same entity key already indexed; idempotent put.
			return nil
		}
		if len(existing) > 0 {
			return &index.UniqueViolationError{
				IndexName:   p.name,
				FieldValue:  fieldValue,
				ExistingKey: existing[0],
				NewKey:      entityKey,
			}
		}
	}

	key := p.buildKey(encoded, entityKey)
	return p.db.Set(key, nil, pebble.Sync)
}

// Get retrieves all entity keys associated with the given field value.
// Returns an empty slice if no entities are found. For numeric indexes,
// fieldValue is encoded before the lookup.
func (p *Index) Get(fieldValue []byte) ([][]byte, error) {
	if p.closed.Load() {
		return nil, &index.ClosedError{Name: p.name}
	}
	if fieldValue == nil {
		return nil, &index.NilFieldValueError{Op: "get"}
	}

	encoded, ok := p.encodeValue(fieldValue)
	if !ok {
		return [][]byte{}, nil // non-numeric value; nothing to find
	}

	keys, err := p.getEntityKeys(encoded)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return [][]byte{}, nil
	}
	return keys, nil
}

// Delete removes the index entry for the given entity key and field value.
// For numeric indexes, fieldValue is encoded before building the delete key.
func (p *Index) Delete(entityKey []byte, fieldValue []byte) error {
	if p.closed.Load() {
		return &index.ClosedError{Name: p.name}
	}
	if fieldValue == nil {
		return &index.NilFieldValueError{Op: "delete"}
	}

	encoded, ok := p.encodeValue(fieldValue)
	if !ok {
		return nil // non-numeric value; nothing to delete
	}

	key := p.buildKey(encoded, entityKey)
	return p.db.Delete(key, pebble.Sync)
}

// Scan retrieves all index entries with field values in the half-open
// interval [start, end). Returns a map of field value strings to entity
// key slices. For numeric indexes, start and end are encoded before use
// so that the byte-range comparison is numerically correct; the result
// map keys are decoded back to decimal strings. A nil start or end means
// unbounded on that side (matching the [start, end) semantics used by
// pkg/sql/provider/range_translator.go for open-ended range predicates
// such as "> x" or "< x"). Returns an index.InvalidNumericBoundError if a
// non-nil bound cannot be parsed as a number on a numeric index.
func (p *Index) Scan(start, end []byte) (map[string][][]byte, error) {
	if p.closed.Load() {
		return nil, &index.ClosedError{Name: p.name}
	}

	var lower, upper []byte
	if start != nil {
		encodedStart, err := p.resolveScanBound(start, "scan start")
		if err != nil {
			return nil, err
		}
		lower = p.buildScanBound(encodedStart)
	} else {
		lower = p.buildIndexPrefix()
	}

	if end != nil {
		encodedEnd, err := p.resolveScanBound(end, "scan end")
		if err != nil {
			return nil, err
		}
		upper = p.buildScanBound(encodedEnd)
	} else {
		upper = indexkey.Successor(p.buildIndexPrefix())
	}

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: lower,
		UpperBound: upper,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	result := make(map[string][][]byte)
	for iter.First(); iter.Valid(); iter.Next() {
		fv, ek, parseErr := p.parseKey(iter.Key())
		if parseErr != nil {
			return nil, parseErr
		}
		key := p.decodeValue(fv)
		result[key] = append(result[key], ek)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	return result, nil
}

// GetAll retrieves all entity keys in the index, deduplicated and sorted
// for deterministic output.
func (p *Index) GetAll() ([][]byte, error) {
	if p.closed.Load() {
		return nil, &index.ClosedError{Name: p.name}
	}

	prefix := p.buildIndexPrefix()
	upper := indexkey.Successor(prefix)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	seen := make(map[string]struct{})
	var keys [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		ek, extractErr := extractEntityKey(iter.Key())
		if extractErr != nil {
			return nil, extractErr
		}
		s := string(ek)
		if _, exists := seen[s]; !exists {
			seen[s] = struct{}{}
			keyCopy := make([]byte, len(ek))
			copy(keyCopy, ek)
			keys = append(keys, keyCopy)
		}
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	sort.Slice(keys, func(i, j int) bool {
		return bytes.Compare(keys[i], keys[j]) < 0
	})

	return keys, nil
}

// Clear removes all entries from the index using a batch deletion over
// the entire index prefix range.
func (p *Index) Clear() error {
	if p.closed.Load() {
		return &index.ClosedError{Name: p.name}
	}

	prefix := p.buildIndexPrefix()
	upper := indexkey.Successor(prefix)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return err
	}
	defer func() { _ = iter.Close() }()

	batch := p.db.NewBatch()
	defer func() { _ = batch.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		k := make([]byte, len(iter.Key()))
		copy(k, iter.Key())
		if err := batch.Delete(k, pebble.Sync); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return err
	}

	return batch.Commit(pebble.Sync)
}

// Close marks the index as closed. Subsequent operations return a
// index.ClosedError. Index does not own the PebbleDB instance, so
// the database is not closed.
func (p *Index) Close() error {
	p.closed.Store(true)
	return nil
}

// getEntityKeys scans all entity keys for a specific field value using
// a prefix iterator.
func (p *Index) getEntityKeys(fieldValue []byte) ([][]byte, error) {
	prefix := p.buildValuePrefix(fieldValue)
	upper := indexkey.Successor(prefix)

	iter, err := p.db.NewIter(&pebble.IterOptions{
		LowerBound: prefix,
		UpperBound: upper,
	})
	if err != nil {
		return nil, err
	}
	defer func() { _ = iter.Close() }()

	var keys [][]byte
	for iter.First(); iter.Valid(); iter.Next() {
		ek, extractErr := extractEntityKey(iter.Key())
		if extractErr != nil {
			return nil, extractErr
		}
		keyCopy := make([]byte, len(ek))
		copy(keyCopy, ek)
		keys = append(keys, keyCopy)
	}
	if err := iter.Error(); err != nil {
		return nil, err
	}

	return keys, nil
}

// buildKey constructs the full composite index key:
// "idx:" + name + ":" + fieldValue + ":" + entityKey + uint32BE(len(entityKey)).
func (p *Index) buildKey(fieldValue []byte, entityKey []byte) []byte {
	prefix := p.buildValuePrefix(fieldValue)
	key := make([]byte, len(prefix)+len(entityKey)+entityKeyLenSize)
	n := copy(key, prefix)
	n += copy(key[n:], entityKey)
	binary.BigEndian.PutUint32(key[n:], uint32(len(entityKey)))
	return key
}

// buildValuePrefix constructs the prefix for a specific field value:
// "idx:" + name + ":" + fieldValue + ":".
func (p *Index) buildValuePrefix(fieldValue []byte) []byte {
	indexPrefix := p.buildIndexPrefix()
	prefix := make([]byte, len(indexPrefix)+len(fieldValue)+len(indexkey.Separator))
	n := copy(prefix, indexPrefix)
	n += copy(prefix[n:], fieldValue)
	copy(prefix[n:], indexkey.Separator)
	return prefix
}

// buildScanBound constructs a range bound for scan operations without
// a trailing ':' separator. The ':' separator (0x3A) sorts after digits
// (0x30-0x39), which breaks lexicographic range scans when values share
// common prefixes of different lengths.
// Format: "idx:" + name + ":" + fieldValue.
func (p *Index) buildScanBound(fieldValue []byte) []byte {
	indexPrefix := p.buildIndexPrefix()
	bound := make([]byte, len(indexPrefix)+len(fieldValue))
	n := copy(bound, indexPrefix)
	copy(bound[n:], fieldValue)
	return bound
}

// buildIndexPrefix constructs the prefix for the entire index:
// "idx:" + name + ":".
func (p *Index) buildIndexPrefix() []byte {
	prefix := make([]byte, len(indexkey.Prefix)+len(p.name)+len(indexkey.Separator))
	n := copy(prefix, indexkey.Prefix)
	n += copy(prefix[n:], p.name)
	copy(prefix[n:], indexkey.Separator)
	return prefix
}

// extractEntityKey extracts the variable-length entity key from the end
// of a composite index key. The last 4 bytes encode the entity key length
// as a uint32 big-endian value. The entity key occupies the preceding
// keyLen bytes.
func extractEntityKey(key []byte) ([]byte, error) {
	if len(key) < entityKeyLenSize {
		return nil, &index.KeyTooShortError{}
	}
	keyLen := binary.BigEndian.Uint32(key[len(key)-entityKeyLenSize:])
	if uint32(len(key)) < keyLen+entityKeyLenSize {
		return nil, &index.KeyTooShortError{}
	}
	start := len(key) - entityKeyLenSize - int(keyLen)
	return key[start : start+int(keyLen)], nil
}

// parseKey parses a full composite index key into its field value and
// entity key components. The key must begin with the index prefix. After
// the prefix, the remaining bytes are:
//
//	fieldValue + ":" + entityKey + uint32BE(len(entityKey))
//
// The parser reads the trailing length suffix backward to locate the
// entity key, then expects a ":" separator before it. Everything before
// the separator is the field value.
func (p *Index) parseKey(key []byte) (fieldValue []byte, entityKey []byte, err error) {
	prefix := p.buildIndexPrefix()
	if len(key) < len(prefix) {
		return nil, nil, &index.KeyPrefixMismatchError{}
	}

	for i := range prefix {
		if key[i] != prefix[i] {
			return nil, nil, &index.KeyPrefixMismatchError{}
		}
	}

	// After the index prefix, the remaining bytes are:
	// fieldValue + ":" + entityKey + uint32BE(len(entityKey))
	rest := key[len(prefix):]
	// Minimum: 1 byte separator + 0 byte entityKey + 4 bytes length = 5
	if len(rest) < len(indexkey.Separator)+entityKeyLenSize {
		return nil, nil, &index.KeyTooShortError{}
	}

	// Read the trailing 4-byte length suffix.
	keyLen := binary.BigEndian.Uint32(rest[len(rest)-entityKeyLenSize:])

	// Validate there are enough bytes for separator + entityKey + length suffix.
	if uint32(len(rest)) < uint32(len(indexkey.Separator))+keyLen+entityKeyLenSize {
		return nil, nil, &index.KeyTooShortError{}
	}

	// Extract the entity key (keyLen bytes before the length suffix).
	ekStart := len(rest) - entityKeyLenSize - int(keyLen)
	entityKey = make([]byte, keyLen)
	copy(entityKey, rest[ekStart:ekStart+int(keyLen)])

	// The byte immediately before the entity key must be the separator ":".
	sepIdx := ekStart - len(indexkey.Separator)
	if sepIdx < 0 {
		return nil, nil, &index.KeyTooShortError{}
	}

	// Extract the field value (everything before the separator).
	fieldValue = make([]byte, sepIdx)
	copy(fieldValue, rest[:sepIdx])

	return fieldValue, entityKey, nil
}

// Compile-time assertion.
var _ index.Index = (*Index)(nil)
