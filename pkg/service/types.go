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
	"github.com/jeremyhahn/go-qrdb/pkg/index"
	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Consistency levels
// ---------------------------------------------------------------------------

// ConsistencyLevel determines the read consistency guarantee.
type ConsistencyLevel uint8

const (
	// ConsistencyStrong performs a linearizable read through the Raft
	// leader, guaranteeing the most recent committed value.
	ConsistencyStrong ConsistencyLevel = iota

	// ConsistencyLinearizable is an alias for ConsistencyStrong.
	ConsistencyLinearizable

	// ConsistencyEventual reads from the local replica without a Raft
	// round-trip, allowing stale reads for lower latency.
	ConsistencyEventual
)

// ---------------------------------------------------------------------------
// MVCC key-value
// ---------------------------------------------------------------------------

// KeyValue is a versioned key-value pair with MVCC revision metadata.
// The canonical definition lives in pkg/kv; this is a type alias for
// backward compatibility within the service package.
type KeyValue = kv.KeyValue

// ---------------------------------------------------------------------------
// Watch events
// ---------------------------------------------------------------------------

// EventType identifies the type of a watch event.
// The canonical definition lives in pkg/watch.
type EventType = watch.EventType

// Watch event type constants.
var (
	EventPut    = watch.EventPut
	EventDelete = watch.EventDelete
)

// Event represents a single change to a key observed by a watcher.
// The canonical definition lives in pkg/watch.
type Event = watch.Event

// Watcher delivers a stream of events for watched keys.
// The canonical definition lives in pkg/watch.
type Watcher = watch.Watcher

// WatchResponse is the envelope delivered to watchers.
// The canonical definition lives in pkg/watch.
type WatchResponse = watch.WatchResponse

// ---------------------------------------------------------------------------
// Leases
// ---------------------------------------------------------------------------

// Lease is an alias for the canonical lease.Lease type.
type Lease = lease.Lease

// ---------------------------------------------------------------------------
// Compare-and-swap (transactions)
// ---------------------------------------------------------------------------

// CompareTarget selects which field of a key-value is compared in a
// transaction precondition.
type CompareTarget uint8

const (
	// CompareVersion compares the key's version counter.
	CompareVersion CompareTarget = iota

	// CompareCreateRevision compares the key's creation revision.
	CompareCreateRevision

	// CompareModRevision compares the key's modification revision.
	CompareModRevision

	// CompareValue compares the key's value bytes.
	CompareValue
)

// CompareResult specifies the comparison operator for a transaction
// precondition.
type CompareResult uint8

const (
	// CompareEqual requires the field to be equal to the target.
	CompareEqual CompareResult = iota

	// CompareGreater requires the field to be greater than the target.
	CompareGreater

	// CompareLess requires the field to be less than the target.
	CompareLess

	// CompareNotEqual requires the field to not be equal to the target.
	CompareNotEqual
)

// Compare is a single precondition in a transaction. It checks a field
// of the key-value against a target value using the specified comparison
// operator.
type Compare struct {
	// Key is the key to compare.
	Key []byte

	// Target selects which field to compare.
	Target CompareTarget

	// Result is the comparison operator.
	Result CompareResult

	// Value is the comparison operand when Target is CompareValue.
	Value []byte

	// Version is the comparison operand when Target is CompareVersion.
	Version int64

	// CreateRevision is the comparison operand when Target is
	// CompareCreateRevision.
	CreateRevision int64

	// ModRevision is the comparison operand when Target is
	// CompareModRevision.
	ModRevision int64
}

// ---------------------------------------------------------------------------
// Operations (transaction / batch)
// ---------------------------------------------------------------------------

// OpType identifies the type of operation in a transaction or batch.
type OpType uint8

const (
	// OpPut stores a key-value pair.
	OpPut OpType = iota

	// OpGet retrieves a key-value pair.
	OpGet

	// OpDelete removes a key-value pair.
	OpDelete

	// OpRange retrieves a range of key-value pairs.
	OpRange
)

// Op is a single operation within a transaction or batch.
type Op struct {
	// Type is the kind of operation.
	Type OpType

	// Key is the target key.
	Key []byte

	// Value is the value for put operations.
	Value []byte

	// RangeEnd defines the end of a range for range operations.
	// When nil, the operation targets a single key.
	RangeEnd []byte

	// LeaseID attaches a lease to the key for put operations.
	LeaseID int64
}

// OpResponse is the result of a single operation within a transaction
// or batch.
type OpResponse struct {
	// Kvs contains the key-values returned by get or range operations.
	Kvs []*KeyValue

	// Count is the total number of keys matching the request, which
	// may exceed len(Kvs) when a limit is applied.
	Count int64

	// Succeeded indicates whether the operation completed successfully.
	Succeeded bool
}

// TransactionResponse is the result of a compare-and-swap transaction.
type TransactionResponse struct {
	// Succeeded is true when all preconditions passed and the success
	// operations were executed; false when preconditions failed and
	// the failure operations were executed.
	Succeeded bool

	// Revision is the global MVCC revision after the transaction.
	Revision int64

	// Responses contains the results of the executed operations
	// (success ops when Succeeded is true, failure ops otherwise).
	Responses []OpResponse
}

// ---------------------------------------------------------------------------
// Range query options
// ---------------------------------------------------------------------------

// RangeOptions holds the resolved configuration for a range query.
type RangeOptions struct {
	Limit      int64
	Revision   int64
	CountOnly  bool
	KeysOnly   bool
	SortOrder  SortOrder
	SortTarget SortTarget
}

// RangeOption configures a range query.
type RangeOption func(*RangeOptions)

// WithLimit sets the maximum number of key-values to return.
func WithLimit(limit int64) RangeOption {
	return func(o *RangeOptions) {
		o.Limit = limit
	}
}

// WithRevision reads key-values at the given historical revision.
func WithRevision(rev int64) RangeOption {
	return func(o *RangeOptions) {
		o.Revision = rev
	}
}

// WithCountOnly returns only the count of matching keys without values.
func WithCountOnly() RangeOption {
	return func(o *RangeOptions) {
		o.CountOnly = true
	}
}

// WithKeysOnly returns keys without their values.
func WithKeysOnly() RangeOption {
	return func(o *RangeOptions) {
		o.KeysOnly = true
	}
}

// WithSort configures the sort order and target for range results.
func WithSort(order SortOrder, target SortTarget) RangeOption {
	return func(o *RangeOptions) {
		o.SortOrder = order
		o.SortTarget = target
	}
}

// ApplyRangeOptions resolves variadic RangeOption values into a
// RangeOptions struct.
func ApplyRangeOptions(opts []RangeOption) RangeOptions {
	var ro RangeOptions
	for _, opt := range opts {
		opt(&ro)
	}
	return ro
}

// SortOrder specifies the sort direction for range results.
type SortOrder uint8

const (
	// SortNone returns results in natural key order.
	SortNone SortOrder = iota

	// SortAscend returns results in ascending order.
	SortAscend

	// SortDescend returns results in descending order.
	SortDescend
)

// SortTarget specifies which field to sort by.
type SortTarget uint8

const (
	// SortByKey sorts by the key.
	SortByKey SortTarget = iota

	// SortByVersion sorts by the version counter.
	SortByVersion

	// SortByCreateRevision sorts by the creation revision.
	SortByCreateRevision

	// SortByModRevision sorts by the modification revision.
	SortByModRevision

	// SortByValue sorts by the value.
	SortByValue
)

// ---------------------------------------------------------------------------
// Watch options
// ---------------------------------------------------------------------------

// WatchOptions holds the resolved configuration for a watch request.
// The canonical definition lives in pkg/watch.
type WatchOptions = watch.WatchOptions

// WatchOption configures a watch request.
// The canonical definition lives in pkg/watch.
type WatchOption = watch.WatchOption

// Watch option constructors — delegate to pkg/watch.
var (
	WithStartRevision = watch.WithStartRevision
	WithPrevKV        = watch.WithPrevKV
	WithProgressNotify = watch.WithProgressNotify
	ApplyWatchOptions = watch.ApplyWatchOptions
)

// ---------------------------------------------------------------------------
// Time-series types (aliases from pkg/timeseries)
// ---------------------------------------------------------------------------

// DataPoint is an alias for the canonical timeseries.DataPoint type.
type DataPoint = timeseries.DataPoint

// AggFunc is an alias for the canonical timeseries.AggFunc type.
type AggFunc = timeseries.AggFunc

// AggResult is an alias for the canonical timeseries.AggResult type.
type AggResult = timeseries.AggResult

// TSQueryOptions is an alias for the canonical timeseries.TSQueryOptions type.
type TSQueryOptions = timeseries.TSQueryOptions

// TSQueryOption is an alias for the canonical timeseries.TSQueryOption type.
type TSQueryOption = timeseries.TSQueryOption

// Aggregation function constants.
var (
	AggAvg        = timeseries.AggAvg
	AggSum        = timeseries.AggSum
	AggMin        = timeseries.AggMin
	AggMax        = timeseries.AggMax
	AggCount      = timeseries.AggCount
	AggPercentile = timeseries.AggPercentile
)

// Functional options for time-series queries.
var (
	WithTags            = timeseries.WithTags
	WithTSLimit         = timeseries.WithTSLimit
	WithDescending      = timeseries.WithDescending
	ApplyTSQueryOptions = timeseries.ApplyTSQueryOptions
)

// ---------------------------------------------------------------------------
// Index configuration (alias from pkg/index)
// ---------------------------------------------------------------------------

// IndexConfig is an alias for the canonical index.IndexConfig type.
type IndexConfig = index.IndexConfig

// ---------------------------------------------------------------------------
// MVCC lookup query types
// ---------------------------------------------------------------------------

// MVCCGetQuery queries the latest value of a key via SyncRead/Lookup.
type MVCCGetQuery struct {
	Key []byte
}

// MVCCGetAtRevisionQuery queries a key at a specific historical revision.
type MVCCGetAtRevisionQuery struct {
	Key      []byte
	Revision int64
}

// MVCCRangeQuery queries a range of keys with optional parameters.
type MVCCRangeQuery struct {
	Start []byte
	End   []byte
	Opts  []RangeOption
}

// MVCCRevisionQuery queries the current global MVCC revision.
type MVCCRevisionQuery struct{}

// MVCCRangeResult holds the result of an MVCC range query.
type MVCCRangeResult struct {
	KeyValues []*KeyValue
	Revision  int64
}

// ---------------------------------------------------------------------------
// Lease lookup query types
// ---------------------------------------------------------------------------

// LeaseTimeToLiveQuery is an alias for the canonical lease.LeaseTimeToLiveQuery type.
type LeaseTimeToLiveQuery = lease.LeaseTimeToLiveQuery

// LeasesQuery is an alias for the canonical lease.LeasesQuery type.
type LeasesQuery = lease.LeasesQuery

// ---------------------------------------------------------------------------
// Index lookup query types (aliases from pkg/index)
// ---------------------------------------------------------------------------

// IndexQueryRequest is an alias for the canonical index.IndexQueryRequest type.
type IndexQueryRequest = index.IndexQueryRequest

// IndexScanRequest is an alias for the canonical index.IndexScanRequest type.
type IndexScanRequest = index.IndexScanRequest

// IndexListRequest is an alias for the canonical index.IndexListRequest type.
type IndexListRequest = index.IndexListRequest

// ---------------------------------------------------------------------------
// Time-series lookup query types (aliases from pkg/timeseries)
// ---------------------------------------------------------------------------

// TSQueryRequest is an alias for the canonical timeseries.TSQueryRequest type.
type TSQueryRequest = timeseries.TSQueryRequest

// TSAggregateRequest is an alias for the canonical timeseries.TSAggregateRequest type.
type TSAggregateRequest = timeseries.TSAggregateRequest
