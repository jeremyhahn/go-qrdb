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

package lock

import (
	"context"

	"github.com/jeremyhahn/go-qrdb/pkg/kv"
	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// ---------------------------------------------------------------------------
// Narrow service interfaces used by the lock package.
//
// These are minimal subsets of the fuller interfaces defined in pkg/service.
// pkg/service types satisfy these interfaces structurally, so no explicit
// declaration is required on the service side.
// ---------------------------------------------------------------------------

// TransactionServicer is the minimal transaction interface required by the
// lock package. It covers only the Execute method used for CAS operations.
type TransactionServicer interface {
	Execute(ctx context.Context, compares []Compare, successOps, failureOps []Op) (*TransactionResponse, error)
}

// LeaseServicer is the minimal lease interface required by the lock package.
type LeaseServicer interface {
	Grant(ctx context.Context, ttl int64) (*lease.Lease, error)
	Revoke(ctx context.Context, leaseID int64) error
	KeepAlive(ctx context.Context, leaseID int64) (*lease.Lease, error)
}

// WatchServicer is the minimal watch interface required by the lock package.
type WatchServicer interface {
	Watch(ctx context.Context, key string, opts ...watch.WatchOption) (watch.Watcher, error)
}

// MVCCKVServicer is the minimal MVCC KV interface required by the lock
// package. Only GetRevision and Range are used by lock operations.
type MVCCKVServicer interface {
	GetRevision(ctx context.Context, key string) (*kv.KeyValue, error)
	Range(ctx context.Context, start, end []byte, opts ...RangeOption) ([]*kv.KeyValue, int64, error)
}

// ---------------------------------------------------------------------------
// Compare-and-swap types (transactions / batch).
//
// These are the canonical definitions. pkg/service aliases them back to
// these types for backward compatibility.
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
	Kvs []*kv.KeyValue

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
