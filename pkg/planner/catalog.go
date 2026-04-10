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

package planner

// IndexInfo describes a single available index that the planner may consider
// when selecting an execution strategy. For single-field indexes, FieldName
// and FieldType are used. For compound indexes, FieldNames and FieldTypes
// are used instead.
type IndexInfo struct {
	// Name is the fully-qualified index identifier, e.g. "mydb.users.Email"
	// or "mydb.messages.ConversationID_Timestamp" for compound indexes.
	Name string

	// FieldName is the struct field name covered by this index, e.g. "Email".
	// Used for single-field indexes. Ignored when FieldNames is non-empty.
	FieldName string

	// FieldNames is an ordered list of struct field names for compound
	// indexes. When non-empty, this is a compound index and FieldName
	// is ignored.
	FieldNames []string

	// Unique reports whether the index enforces a uniqueness constraint.
	Unique bool

	// FieldType is the Go type name of the indexed field, e.g. "string",
	// "uint64", "int64", "float64".
	// Used for single-field indexes.
	FieldType string

	// FieldTypes is an ordered list of Go type names for compound indexes.
	// Must be the same length as FieldNames when set.
	FieldTypes []string
}

// IsCompound reports whether this describes a compound (multi-field) index.
func (ii IndexInfo) IsCompound() bool {
	return len(ii.FieldNames) > 1
}

// IndexCatalog provides the set of indexes available for the planner to
// consider when building a query plan.
type IndexCatalog interface {
	// AvailableIndexes returns all indexes that the planner may use.
	// The returned slice must not be modified by the caller.
	AvailableIndexes() []IndexInfo
}

// StaticCatalog is an IndexCatalog backed by a fixed slice of IndexInfo
// values supplied at construction time. It is safe for concurrent read
// access after construction.
type StaticCatalog struct {
	indexes []IndexInfo
}

// NewStaticCatalog creates a StaticCatalog from the provided index descriptors.
// The caller's slice is copied so subsequent modifications have no effect.
func NewStaticCatalog(indexes []IndexInfo) *StaticCatalog {
	cp := make([]IndexInfo, len(indexes))
	copy(cp, indexes)
	return &StaticCatalog{indexes: cp}
}

// AvailableIndexes implements IndexCatalog and returns all indexes in the
// catalog. The returned slice is a copy; callers may not modify it.
func (c *StaticCatalog) AvailableIndexes() []IndexInfo {
	cp := make([]IndexInfo, len(c.indexes))
	copy(cp, c.indexes)
	return cp
}
