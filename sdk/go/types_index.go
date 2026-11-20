package sdk

import "github.com/jeremyhahn/go-qrdb/pkg/index"

// CompoundIndexDef declares one multi-field secondary index of an entity:
// an ordered list of Go struct field names (the order decides which prefix
// queries the index can serve) and its Unique, CaseSensitive and Sparse
// options. An entity returns its definitions from CompoundIndexes().
type CompoundIndexDef = index.CompoundIndexDef

// CompoundIndexRegisterer is implemented by an entity that declares
// compound indexes. NewDAO registers every definition it returns alongside
// the single-field indexes named by the entity's `index` struct tags.
type CompoundIndexRegisterer = index.CompoundIndexRegisterer

// IndexQueryRequest is an exact-match lookup on a single-field index: the
// entity keys whose indexed field equals Value.
type IndexQueryRequest = index.IndexQueryRequest

// IndexScanRequest is a range scan on a single-field index over the
// half-open interval [Start, End); a nil bound leaves that side open.
type IndexScanRequest = index.IndexScanRequest

// CompoundIndexQueryRequest is an exact-match lookup on a compound index:
// every field value in Values must match, in the index's field order.
type CompoundIndexQueryRequest = index.CompoundIndexQueryRequest

// CompoundIndexScanRequest is a prefix and range scan on a compound index:
// PrefixValues match the leading fields exactly and [RangeStart, RangeEnd)
// bounds the next field; a nil bound leaves that side open.
type CompoundIndexScanRequest = index.CompoundIndexScanRequest
