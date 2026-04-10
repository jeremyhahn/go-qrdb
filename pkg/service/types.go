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
	"github.com/jeremyhahn/go-qrdb/pkg/lock"
	"github.com/jeremyhahn/go-qrdb/pkg/statemachine"
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
//
// Canonical definitions live in pkg/lock to avoid the lock ↔ service import
// cycle. These are type aliases for backward compatibility.
// ---------------------------------------------------------------------------

// CompareTarget selects which field of a key-value is compared in a
// transaction precondition.
// The canonical definition lives in pkg/lock.
type CompareTarget = lock.CompareTarget

// Compare target constants — alias the canonical pkg/lock values.
var (
	CompareVersion        = lock.CompareVersion
	CompareCreateRevision = lock.CompareCreateRevision
	CompareModRevision    = lock.CompareModRevision
	CompareValue          = lock.CompareValue
)

// CompareResult specifies the comparison operator for a transaction
// precondition.
// The canonical definition lives in pkg/lock.
type CompareResult = lock.CompareResult

// Compare result constants — alias the canonical pkg/lock values.
var (
	CompareEqual    = lock.CompareEqual
	CompareGreater  = lock.CompareGreater
	CompareLess     = lock.CompareLess
	CompareNotEqual = lock.CompareNotEqual
)

// Compare is a single precondition in a transaction.
// The canonical definition lives in pkg/lock.
type Compare = lock.Compare

// ---------------------------------------------------------------------------
// Operations (transaction / batch)
// ---------------------------------------------------------------------------

// OpType identifies the type of operation in a transaction or batch.
// The canonical definition lives in pkg/lock.
type OpType = lock.OpType

// Op type constants — alias the canonical pkg/lock values.
var (
	OpPut    = lock.OpPut
	OpGet    = lock.OpGet
	OpDelete = lock.OpDelete
	OpRange  = lock.OpRange
)

// Op is a single operation within a transaction or batch.
// The canonical definition lives in pkg/lock.
type Op = lock.Op

// OpResponse is the result of a single operation within a transaction
// or batch.
// The canonical definition lives in pkg/lock.
type OpResponse = lock.OpResponse

// TransactionResponse is the result of a compare-and-swap transaction.
// The canonical definition lives in pkg/lock.
type TransactionResponse = lock.TransactionResponse

// ---------------------------------------------------------------------------
// Range query options
// ---------------------------------------------------------------------------

// RangeOptions holds the resolved configuration for a range query.
// The canonical definition lives in pkg/lock.
type RangeOptions = lock.RangeOptions

// RangeOption configures a range query.
// The canonical definition lives in pkg/lock.
type RangeOption = lock.RangeOption

// Range option constructors — delegate to pkg/lock.
var (
	WithLimit         = lock.WithLimit
	WithRevision      = lock.WithRevision
	WithCountOnly     = lock.WithCountOnly
	WithKeysOnly      = lock.WithKeysOnly
	ApplyRangeOptions = lock.ApplyRangeOptions
)

// SortOrder specifies the sort direction for range results.
// The canonical definition lives in pkg/lock.
type SortOrder = lock.SortOrder

// Sort order constants — alias the canonical pkg/lock values.
var (
	SortNone    = lock.SortNone
	SortAscend  = lock.SortAscend
	SortDescend = lock.SortDescend
)

// SortTarget specifies which field to sort by.
// The canonical definition lives in pkg/lock.
type SortTarget = lock.SortTarget

// Sort target constants — alias the canonical pkg/lock values.
var (
	SortByKey            = lock.SortByKey
	SortByVersion        = lock.SortByVersion
	SortByCreateRevision = lock.SortByCreateRevision
	SortByModRevision    = lock.SortByModRevision
	SortByValue          = lock.SortByValue
)

// WithSort configures the sort order and target for range results.
var WithSort = lock.WithSort

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
	WithStartRevision  = watch.WithStartRevision
	WithPrevKV         = watch.WithPrevKV
	WithProgressNotify = watch.WithProgressNotify
	ApplyWatchOptions  = watch.ApplyWatchOptions
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
// MVCC lookup query types (aliases to statemachine package to avoid
// type mismatch when passed through SyncRead → FSM Lookup)
// ---------------------------------------------------------------------------

// MVCCGetQuery queries the latest value of a key via SyncRead/Lookup.
type MVCCGetQuery = statemachine.MVCCGetQuery

// MVCCGetAtRevisionQuery queries a key at a specific historical revision.
type MVCCGetAtRevisionQuery = statemachine.MVCCGetAtRevisionQuery

// MVCCRangeQuery queries a range of keys with optional parameters.
// Uses flat fields so the FSM can type-switch without circular imports.
type MVCCRangeQuery = statemachine.MVCCRangeQuery

// MVCCRevisionQuery queries the current global MVCC revision.
type MVCCRevisionQuery = statemachine.MVCCRevisionQuery

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

// TSRichQueryRequest is an alias for the canonical timeseries.TSRichQueryRequest type.
type TSRichQueryRequest = timeseries.TSRichQueryRequest

// TSGetRetentionRequest is an alias for the canonical timeseries.TSGetRetentionRequest type.
type TSGetRetentionRequest = timeseries.TSGetRetentionRequest

// TSListMetricsRequest is an alias for the canonical timeseries.TSListMetricsRequest type.
type TSListMetricsRequest = timeseries.TSListMetricsRequest

// QueryRequest is an alias for the canonical timeseries.QueryRequest type.
type QueryRequest = timeseries.QueryRequest

// QueryResult is an alias for the canonical timeseries.QueryResult type.
type QueryResult = timeseries.QueryResult

// RetentionPolicy is an alias for the canonical timeseries.RetentionPolicy type.
type RetentionPolicy = timeseries.RetentionPolicy

// DeleteRequest is an alias for the canonical timeseries.DeleteRequest type.
type DeleteRequest = timeseries.DeleteRequest

// ---------------------------------------------------------------------------
// Shard management types
// ---------------------------------------------------------------------------

// StartShardRequest describes the parameters for starting a new Raft shard.
type StartShardRequest struct {
	// ShardID is the unique shard identifier to start.
	ShardID uint64

	// ReplicaID is this node's replica identifier within the shard.
	ReplicaID uint64

	// Members maps replicaID to Raft network address for initial
	// cluster members. Required for new clusters; empty for joins.
	Members map[uint64]string

	// Join indicates whether this node is joining an existing shard
	// rather than forming a new one.
	Join bool
}

// ShardStateResponse describes the Raft state of a single shard.
type ShardStateResponse struct {
	// ShardID is the shard's unique identifier.
	ShardID uint64

	// ConfigChangeIndex is the current Raft configuration change index.
	ConfigChangeIndex uint64

	// Term is the current Raft term.
	Term uint64

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64

	// HasLeader is true when a leader is known for this shard.
	HasLeader bool
}

// TopologyResponse describes the full cluster topology as seen from
// a single node.
type TopologyResponse struct {
	// NodeID is the unique host identifier.
	NodeID string

	// RaftAddress is the host's Raft network address.
	RaftAddress string

	// ShardCount is the number of shards loaded on this host.
	ShardCount int

	// Shards contains per-shard topology information.
	Shards []TopologyShardInfo
}

// TopologyShardInfo describes a single shard's topology including its
// membership and leadership state.
type TopologyShardInfo struct {
	// ShardID is the shard's unique identifier.
	ShardID uint64

	// ReplicaID is this node's replica identifier within the shard.
	ReplicaID uint64

	// IsLeader is true when this replica is the current leader.
	IsLeader bool

	// LeaderID is the current leader's replica ID. 0 if unknown.
	LeaderID uint64

	// Nodes maps replicaID to Raft network address for all known
	// cluster members.
	Nodes map[uint64]string
}
