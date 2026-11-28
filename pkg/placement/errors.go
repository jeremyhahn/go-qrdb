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

package placement

import (
	"errors"
	"strconv"
)

// Sentinel errors. Callers match these with errors.Is; the richer typed errors
// below wrap the matching sentinel via Unwrap.
var (
	// ErrNilTopology is returned by NewScheduler when no TopologyProvider is
	// supplied.
	ErrNilTopology = errors.New("placement: topology provider must not be nil")

	// ErrNilKV is returned by NewStore when no KV backend is supplied.
	ErrNilKV = errors.New("placement: kv backend must not be nil")

	// ErrInvalidRF is returned by Place when the requested replication factor
	// is not positive.
	ErrInvalidRF = errors.New("placement: replication factor must be positive")

	// ErrEmptyTenant is returned when a tenant ID is empty.
	ErrEmptyTenant = errors.New("placement: tenant id must not be empty")

	// ErrEmptyDatabase is returned when a database name is empty.
	ErrEmptyDatabase = errors.New("placement: database name must not be empty")

	// ErrEmptyDatabaseID is returned when a placement entry names no
	// database id: the table is keyed by the id, never by the name.
	ErrEmptyDatabaseID = errors.New("placement: database id must not be empty")

	// ErrZeroShard is returned by Place when the requested shard ID is zero.
	ErrZeroShard = errors.New("placement: shard id must not be zero")

	// ErrInsufficientNodes is returned by Place when fewer than RF healthy,
	// distinct nodes are available to satisfy anti-affinity.
	ErrInsufficientNodes = errors.New("placement: insufficient healthy nodes for replication factor")

	// ErrPlacementNotFound is returned by Store.Get when no placement exists
	// for the requested (tenant, database).
	ErrPlacementNotFound = errors.New("placement: placement not found")

	// ErrAmbiguousPlacement is returned by Store.GetByName when more than one
	// live (not Dropping) placement entry names the same database: the name
	// no longer identifies one database, so the lookup fails closed rather
	// than pick one.
	ErrAmbiguousPlacement = errors.New("placement: more than one live placement names the database")

	// ErrWatchUnsupported is returned by Store.Watch when the backing KV does
	// not implement Watcher.
	ErrWatchUnsupported = errors.New("placement: watch not supported by kv backend")
)

// ConflictError reports that a placement entry kept changing between being
// read and being compare-and-swapped: every attempt in a bounded
// optimistic-retry loop (SetDropping, TryMarkDropping, MarkDropCommitted), or
// the single attempt of a caller that does not retry, lost the race against
// a concurrent mutation -- another drop's fence, a scale, a reconciler
// promotion. It is the inner error of a *StoreError naming the entry.
type ConflictError struct{}

// Error implements the error interface.
func (e *ConflictError) Error() string {
	return "placement: entry changed since it was read"
}

// AtomicWriteUnsupportedError reports that the backing KV does not implement
// AtomicKV, so CompareAndSwapPlacement cannot run. It is a refusal, not a
// fallback: degrading to an unconditional Put would reintroduce the
// lost-update race compare-and-swap exists to close, and no caller in this
// package degrades. It is the inner error of a *StoreError naming the entry.
type AtomicWriteUnsupportedError struct{}

// Error implements the error interface.
func (e *AtomicWriteUnsupportedError) Error() string {
	return "placement: kv backend does not support compare-and-swap"
}

// InsufficientNodesError reports that the cluster cannot satisfy a placement's
// replication factor with distinct healthy nodes. It unwraps to
// ErrInsufficientNodes.
type InsufficientNodesError struct {
	// Have is the number of healthy, distinct candidate nodes found.
	Have int

	// Need is the requested replication factor.
	Need int
}

// Error implements the error interface.
func (e *InsufficientNodesError) Error() string {
	return "placement: insufficient healthy nodes: have " + strconv.Itoa(e.Have) +
		", need " + strconv.Itoa(e.Need)
}

// Unwrap returns ErrInsufficientNodes so errors.Is matches the sentinel.
func (e *InsufficientNodesError) Unwrap() error { return ErrInsufficientNodes }

// StoreError wraps a failure reading or writing the placement table for a
// (tenant, database) pair. It identifies the entry and the operation.
type StoreError struct {
	// TenantID owns the placement entry.
	TenantID string

	// DatabaseID is the database name.
	DatabaseID string

	// Op is the failing operation (e.g. "put", "get", "delete", "list").
	Op string

	// Err is the underlying cause.
	Err error
}

// Error implements the error interface.
func (e *StoreError) Error() string {
	target := e.TenantID + "/" + e.DatabaseID
	if e.DatabaseID == "" {
		target = e.TenantID
	}
	return "placement: " + e.Op + " " + target + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is/As.
func (e *StoreError) Unwrap() error { return e.Err }

// ReplicaDataRootError refuses a placement record whose replica names a
// filesystem path. The placement table is replicated and a path names the
// disk of the node that wrote it, so every node resolves its own replica's
// directory from the tenant's volume pool instead; a record carrying a path is
// refused rather than opened. The database must be re-created.
type ReplicaDataRootError struct {
	TenantID string
	Database string
	// NodeID is the replica whose entry names the path.
	NodeID   string
	DataRoot string
}

// Error implements the error interface.
func (e *ReplicaDataRootError) Error() string {
	return "placement: replica " + e.NodeID + " of " + e.TenantID + "/" + e.Database + " names the path " + e.DataRoot +
		"; a replica's directory is resolved on each node from the tenant's volume pool, so the record is refused" +
		" -- drop and re-create the database"
}

// IdentityMismatchError reports that the placement entry stored under a
// catalog record's database id does not name that database: its recorded
// database id or name differs. A drop fences only the entry the catalog
// record names, so it is refused rather than acting on another database's
// shard -- a dropped predecessor of the same name above all.
type IdentityMismatchError struct {
	TenantID string
	Database string
	// DatabaseID is the id the catalog record holds; GotDatabaseID and
	// GotDatabase are what the placement entry under it records.
	DatabaseID    string
	GotDatabaseID string
	GotDatabase   string
}

// Error implements the error interface.
func (e *IdentityMismatchError) Error() string {
	return "placement: the entry under database id " + e.DatabaseID + " of " + e.TenantID + "/" + e.Database +
		" records database " + e.GotDatabase + " (id " + e.GotDatabaseID + "); refusing to act on another database's shard"
}

// NotAReplicaError refuses serving a dedicated-raft database on a node its
// placement does not name. The node must not resolve the database through its
// own catalog or start a shard of its own: a write it acknowledged there would
// never reach the real replicas. Replicas is the placement's replica set, so
// the caller can retry against one of them.
type NotAReplicaError struct {
	TenantID string
	Database string
	// ShardID is the database's Raft group. Zero for a local binding -- a
	// database held on one node, which has no group (see LocalBinding).
	ShardID uint64
	// NodeID is the node that refused.
	NodeID string
	// Replicas is the replica set the placement names.
	Replicas []Replica
	// LeaderReplicaID is the placement's bootstrap leader -- a hint, not a
	// fact: Raft may have elected another replica since. Replica forwarding
	// tries it first. Zero when the placement names none.
	LeaderReplicaID uint64
}

// LocalBinding reports whether the refusal names a database held on one node
// rather than a Raft group: its one replica is the owning node, and it has no
// shard.
func (e *NotAReplicaError) LocalBinding() bool {
	return e.ShardID == 0
}

// Error implements the error interface.
func (e *NotAReplicaError) Error() string {
	msg := "placement: node " + e.NodeID + " is not a replica of " + e.TenantID + "/" + e.Database +
		" (shard " + strconv.FormatUint(e.ShardID, 10) + "); its replicas are on"
	for i, r := range e.Replicas {
		if i > 0 {
			msg += ","
		}
		msg += " " + r.NodeID
	}
	return msg
}
