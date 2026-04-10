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

package index

// IndexValueType specifies how field values are encoded in the index.
type IndexValueType int

const (
	// IndexValueTypeString stores values as raw bytes (lexicographic order).
	// This is the default and preserves existing behavior.
	IndexValueTypeString IndexValueType = 0

	// IndexValueTypeNumeric encodes values as 8-byte big-endian fixed-width
	// integers with a sign-bit flip so that byte order matches numeric order.
	// Use this for integer or floating-point fields to ensure range scans
	// like [1, 222) do not incorrectly include 1234.
	IndexValueTypeNumeric IndexValueType = 1
)

// IndexConfig configures index behavior. For single-field indexes,
// Name and ValueType are used. For compound indexes, FieldNames and
// ValueTypes provide per-field configuration.
type IndexConfig struct {
	// Name is the index name (typically the field name, or
	// "Field1_Field2" for compound indexes).
	Name string

	// Type specifies the index constraint type.
	Type IndexType

	// ValueType controls how field values are encoded in the index.
	// Defaults to IndexValueTypeString (lexicographic).
	// For compound indexes, this applies to all fields unless
	// ValueTypes is set.
	ValueType IndexValueType

	// FieldNames is an ordered list of field names for compound indexes.
	// When non-empty, this is a compound index.
	FieldNames []string

	// ValueTypes specifies per-field encoding for compound indexes.
	// Must be the same length as FieldNames when set.
	// When nil, all fields use ValueType.
	ValueTypes []IndexValueType

	// CaseSensitive indicates whether string comparisons are case-sensitive.
	// Only applies to string fields.
	CaseSensitive bool

	// Unique enforces that each indexed value maps to at most one key.
	Unique bool

	// Sparse skips indexing keys where the indexed field is absent
	// or empty.
	Sparse bool
}

// IsCompound reports whether this is a compound (multi-field) index.
func (c IndexConfig) IsCompound() bool {
	return len(c.FieldNames) > 1
}

// CompoundIndexDef defines a compound index to be created for an entity
// type. Entity types declare compound indexes by implementing the
// CompoundIndexRegisterer interface.
type CompoundIndexDef struct {
	// Fields is an ordered list of Go struct field names that form
	// the compound index key. The order determines prefix-scan
	// eligibility: a query on (A) can use index (A, B, C), but a
	// query on (B) alone cannot.
	Fields []string

	// Unique enforces that each compound value tuple maps to at most one key.
	Unique bool

	// CaseSensitive indicates whether string comparisons are case-sensitive.
	CaseSensitive bool

	// Sparse skips indexing entities where any indexed field is absent or empty.
	Sparse bool
}

// CompoundIndexRegisterer is optionally implemented by entity types that
// need compound (multi-field) indexes. The DAO checks for this interface
// at construction time via type assertion.
type CompoundIndexRegisterer interface {
	CompoundIndexes() []CompoundIndexDef
}

// IndexQueryRequest queries an index for entity IDs matching an exact value.
type IndexQueryRequest struct {
	IndexName string
	Value     []byte
}

// IndexScanRequest queries an index for entity IDs within a value range.
type IndexScanRequest struct {
	IndexName string
	Start     []byte
	End       []byte
}

// IndexListRequest lists all registered index names.
type IndexListRequest struct{}

// CompoundIndexQueryRequest queries a compound index for entity keys
// matching an exact set of field values.
type CompoundIndexQueryRequest struct {
	IndexName string
	Values    [][]byte
}

// CompoundIndexScanRequest queries a compound index using a prefix of
// field values with an optional range on the next field.
type CompoundIndexScanRequest struct {
	IndexName    string
	PrefixValues [][]byte
	RangeStart   []byte
	RangeEnd     []byte
}
