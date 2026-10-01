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
	//
	// This field is named for the DEFAULT, so its ZERO VALUE folds case.
	// Every construction site must set it explicitly: since go-qrdb#34 the
	// engines derive an index's case behaviour from this field instead of
	// hardcoding CaseSensitive:true, so a composite literal that omits it
	// builds a case-INSENSITIVE index. Nothing fails loudly when that
	// happens — folding only ever ADDS matches — which is why the transports,
	// the lifecycle adapter and the collection service each state it.
	//
	// The wire and transport layers invert the polarity on purpose
	// (protocol.IndexFlagCaseInsensitive, transport.IndexConfig.CaseInsensitive):
	// there the bit marks the POSITIVE claim, so an absent flag — including in
	// every Raft command written before the flag existed — means
	// case-sensitive.
	CaseSensitive bool

	// Unique enforces that each indexed value maps to at most one key.
	Unique bool

	// KeyPrefix is the key scope the index covers, RELATIVE to the namespace
	// its Name carries. The engines maintain the index only for keys under
	// that scope, so an index declared for one collection never indexes
	// another collection's documents, another database's keys or a plain
	// key-value key (go-qrdb#410).
	//
	// Relative, because the "<tenant>/<database>/" namespace is applied to
	// the index NAME and to every KEY by the same registry wrapper; the
	// engine composes the namespace it reads back off the name with this
	// prefix (engine.ComposeIndexScope). A creator with nothing to declare
	// beyond its namespace leaves this empty and gets a namespace-wide scope;
	// a name with no namespace and no prefix is an UNSCOPED index covering
	// the whole state machine.
	//
	// Recorded for fidelity with the definition; no Index implementation
	// reads it. Scoping happens in the index-maintenance callers, as Sparse
	// does below.
	KeyPrefix string

	// Sparse skips indexing keys where the indexed field is absent
	// or empty.
	//
	// Recorded for fidelity with the definition; no Index implementation
	// reads it today. Skipping absent fields happens in the backfill and
	// index-maintenance callers.
	Sparse bool

	// Owner is the object whose lifecycle created the index (go-qrdb#426):
	// CollectionOwner for an index a collection declared, the empty string
	// for the explicit index API. It is carried on the create command and
	// recorded beside the index by the apply path, which refuses a create
	// naming an index another owner holds (see CheckCreateOwner) and removes
	// an index on an owner-checked drop only for its owner (CheckDropOwner).
	Owner string
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

// ExplicitOwner is the owner recorded on an index created through the explicit
// index API, and on every index created before owners were recorded. It is
// also the owner the explicit API's drop claims, so that drop removes only
// explicit indexes and refuses one a collection owns.
const ExplicitOwner = ""

// ownerKindCollection is the owner-kind prefix recorded for an index a
// document collection created.
const ownerKindCollection = "collection:"

// CollectionOwner returns the owner recorded for an index that one
// incarnation of the collection (database, collection) created
// (go-qrdb#426). incarnation distinguishes a collection from a later one
// created under the same name after a drop.
//
// The value names the collection unambiguously even where the index NAME does
// not: an index name is a "."-joined triple with no escaping, so field
// "v2.email" on collection "users" and field "email" on collection "users.v2"
// derive the same name. Their owners differ, so the second create is refused
// instead of adopting the first collection's index.
func CollectionOwner(database, collection, incarnation string) string {
	return ownerKindCollection + database + "/" + collection + "#" + incarnation
}

// OwnerLabel renders an owner for a message. The empty owner is the explicit
// index API, which has no name of its own.
func OwnerLabel(owner string) string {
	if owner == "" {
		return "the explicit index API"
	}
	return owner
}

// CheckCreateOwner decides a create that names an index which already exists
// under the recorded owner existing. The same owner is the idempotent
// re-create index creation has always been; a different owner is refused with
// *OwnershipError rather than adopted.
func CheckCreateOwner(name, existing, requested string) error {
	if existing == requested {
		return nil
	}
	return &OwnershipError{Name: name, Owner: existing, Requested: requested}
}

// CheckDropOwner decides a drop of an existing index recorded under owner
// existing. An unconditional drop (checkOwner false) is always permitted; an
// owner-checked drop is permitted only for the caller's own index.
func CheckDropOwner(name, existing string, checkOwner bool, requested string) error {
	if !checkOwner || existing == requested {
		return nil
	}
	return &OwnershipError{Name: name, Owner: existing, Requested: requested}
}
