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

// Package statemachine provides a high-performance sharded in-memory cache
// backed by persistent storage for use as a Raft state machine.
package statemachine

// WatchManager receives notifications about key changes.
type WatchManager interface {
	// Notify sends a key-change event to registered watchers.
	Notify(key string, value []byte, revision uint64, deleted bool)
}

// PrefixQuery is a lookup request for all keys sharing a common prefix.
type PrefixQuery struct {
	// Prefix is the key prefix to scan (e.g., "product:" to find all product keys)
	Prefix []byte
	// Limit is the maximum number of results to return (0 = unlimited)
	Limit int
}

// LeaseManager handles lease lifecycle operations within the FSM.
// Implementations must be safe for concurrent use. The FSM delegates
// lease grant, revoke, and keep-alive mutations through this interface
// after Raft consensus, and read-only queries through TimeToLive and
// Leases during Lookup.
type LeaseManager interface {
	// Grant creates a new lease with the given TTL in seconds.
	// Returns the assigned lease ID and the TTL that was actually granted.
	Grant(ttl int64) (leaseID int64, grantedTTL int64, err error)

	// Revoke removes a lease by ID. The caller is responsible for
	// cleaning up any keys that were attached to the lease.
	Revoke(leaseID int64) error

	// KeepAlive refreshes the TTL of an existing lease. Returns the
	// remaining TTL in seconds after the refresh.
	KeepAlive(leaseID int64) (ttl int64, err error)

	// TimeToLive returns the current state of a lease by ID. The
	// returned interface{} is expected to be a *lease.Lease or
	// equivalent struct that callers can type-assert.
	TimeToLive(leaseID int64) (interface{}, error)

	// Leases returns all active leases. The returned interface{} is
	// expected to be a []*lease.Lease or equivalent slice.
	Leases() (interface{}, error)
}

// IndexManager handles secondary index operations within the FSM.
// Implementations must be safe for concurrent use. The FSM delegates
// index create and drop mutations through this interface after Raft
// consensus, and read-only queries (Query, Scan, List) during Lookup.
type IndexManager interface {
	// CreateIndex registers a new secondary index with the given name and
	// field configuration. If unique is true, the index enforces a
	// one-to-one mapping between indexed values and keys. If sparse is
	// true, keys where the indexed field is absent are skipped. If numeric
	// is true, field values are encoded as fixed-width 8-byte big-endian
	// integers so that byte ordering matches numeric ordering.
	CreateIndex(name, fieldName string, unique, sparse, numeric bool) error

	// DropIndex removes a secondary index by name.
	DropIndex(name string) error

	// Query returns the entity keys whose indexed value matches exactly.
	Query(indexName string, value []byte) ([][]byte, error)

	// Scan returns entity keys whose indexed values fall within the
	// [start, end) range, keyed by the string representation of each
	// matching value.
	Scan(indexName string, start, end []byte) (map[string][][]byte, error)

	// List returns metadata for all registered indexes.
	List() ([]interface{}, error)
}

// TimeSeriesStore handles time-series write and read operations within the
// FSM. Write operations are invoked from the Update path after Raft
// consensus. Read operations are invoked from the Lookup path.
//
// This interface uses primitive types to avoid circular dependencies
// between statemachine and the timeseries package.
type TimeSeriesStore interface {
	// WriteDataPoint stores a single time-series data point.
	WriteDataPoint(metric string, timestamp int64, value float64, tags map[string]string) error

	// WriteDataPoints stores multiple time-series data points atomically.
	WriteDataPoints(points []TSDataPoint) error

	// DeleteSeries deletes time-series data matching the given criteria.
	// startNs and endNs are nanosecond timestamps bounding the range.
	DeleteSeries(metric string, startNs, endNs int64, tags map[string]string) error

	// SetRetentionPolicy configures the retention policy for a metric.
	// durationNs is the retention duration in nanoseconds.
	// resolutionNs is the downsampling resolution in nanoseconds.
	// aggregation is the AggFunc byte value for downsampled data.
	SetRetentionPolicy(metric string, durationNs, resolutionNs int64, aggregation byte) error

	// QueryRange retrieves data points for a metric within [startNs, endNs).
	QueryRange(metric string, startNs, endNs int64) (interface{}, error)

	// QueryAggregate computes an aggregation over time-series data points.
	// fn is the AggFunc byte value. intervalNs is the grouping interval
	// in nanoseconds.
	QueryAggregate(metric string, startNs, endNs int64, fn byte, intervalNs int64) (interface{}, error)

	// QueryRich performs a rich query with filtering, aggregation,
	// downsampling, and grouping. The request parameter is expected
	// to be a *timeseries.QueryRequest.
	QueryRich(request interface{}) (interface{}, error)

	// GetRetentionPolicy retrieves the retention policy for a metric.
	GetRetentionPolicy(metric string) (interface{}, error)

	// ListMetrics returns metric names matching the prefix up to limit.
	ListMetrics(prefix string, limit int) ([]string, error)
}

// TSDataPoint is a protocol-level data point for batch writes.
// Fields use primitive types to avoid importing the timeseries package.
type TSDataPoint struct {
	// Metric is the metric name (e.g., "cpu.usage").
	Metric string

	// Timestamp is the measurement time in nanoseconds since epoch.
	Timestamp int64

	// Value is the measured value.
	Value float64

	// Tags are optional key-value labels for filtering and grouping.
	Tags map[string]string
}

// MVCCStore handles MVCC operations within the FSM. Implementations
// must be safe for concurrent use. The FSM delegates MVCC mutations
// and transactions through this interface after Raft consensus. Range
// queries may also be invoked from the Lookup path for read-only access.
//
// This interface uses primitive types and generic return values to avoid
// circular dependencies between statemachine and the mvcc package.
type MVCCStore interface {
	// Put stores a key-value pair at the given revision with an
	// optional lease attachment. Returns the resulting key-value
	// metadata as an interface{} (expected to be *service.KeyValue).
	Put(key, value []byte, leaseID int64, revision uint64) (interface{}, error)

	// Delete removes a key at the given revision. Returns the previous
	// key-value state as an interface{} (expected to be *service.KeyValue),
	// or nil if the key did not exist.
	Delete(key []byte, revision uint64) (interface{}, error)

	// Range queries a range of keys. Parameters control filtering:
	//   - start, end: key range [start, end). If end is nil, returns
	//     only the key matching start.
	//   - revision: point-in-time revision (0 = current).
	//   - limit: maximum results (0 = unlimited).
	//   - countOnly: return only the count, not values.
	//   - keysOnly: return keys without their values.
	// Returns the results as an interface{} and the current store
	// revision as an int64.
	Range(start, end []byte, revision, limit int64, countOnly, keysOnly bool) (interface{}, int64, error)

	// Compact removes all data entries at or below the specified
	// revision.
	Compact(revision int64) error

	// Transaction evaluates compare preconditions and executes the
	// success or failure operations atomically. Returns whether the
	// preconditions succeeded, the operation responses as an
	// interface{}, and the resulting revision.
	Transaction(compares []MVCCCompare, successOps, failureOps []MVCCOp) (succeeded bool, revision int64, responses interface{}, err error)
}

// MVCCCompare represents a conditional check in a transaction.
type MVCCCompare struct {
	// Key is the key to check.
	Key []byte

	// Target selects which field of the key-value is compared
	// (maps to service.CompareTarget).
	Target byte

	// Result is the comparison operator (maps to service.CompareResult).
	Result byte

	// Value is the comparison operand for value-based comparisons.
	Value []byte

	// Version is the comparison operand for version comparisons.
	Version int64

	// CreateRevision is the comparison operand for create-revision
	// comparisons.
	CreateRevision int64

	// ModRevision is the comparison operand for mod-revision
	// comparisons.
	ModRevision int64
}

// MVCCOp represents an operation within a transaction.
type MVCCOp struct {
	// Type is the operation type (e.g., put, get, delete, range).
	Type byte

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

// ---------------------------------------------------------------------------
// MVCC Lookup query types (defined here to avoid circular imports with
// the service package)
// ---------------------------------------------------------------------------

// MVCCGetQuery is a point lookup for a single key at the current revision.
type MVCCGetQuery struct {
	Key []byte
}

// MVCCGetAtRevisionQuery is a point lookup for a single key at a specific
// historical revision.
type MVCCGetAtRevisionQuery struct {
	Key      []byte
	Revision int64
}

// MVCCRangeQuery queries a range of keys with optional parameters.
type MVCCRangeQuery struct {
	Start     []byte
	End       []byte
	Revision  int64
	Limit     int64
	CountOnly bool
	KeysOnly  bool
}

// MVCCRevisionQuery retrieves the current global MVCC revision.
type MVCCRevisionQuery struct{}
