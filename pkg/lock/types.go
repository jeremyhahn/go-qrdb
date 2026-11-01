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

// Package lock provides a distributed lock service built on top of the
// QRDB lease, transaction, watch, and MVCC KV services. Lock keys are
// stored under the "__sys:lock/" prefix, and a lock's fencing token is its
// key's MVCC create revision (the etcd model), which detects stale holders.
package lock

import "time"

const lockKeyPrefix = "__sys:lock/"

// Lock represents an acquired distributed lock.
type Lock struct {
	// Key is the user-provided key without the internal prefix.
	Key string

	// FencingToken is the lock key's MVCC create revision: the revision
	// of the put that acquired the lock. Re-entries leave it unchanged, a
	// release and re-acquisition always raise it, and it can be used to
	// detect stale holders.
	FencingToken int64

	// LeaseID is the underlying lease ID that controls the TTL.
	LeaseID int64

	// HolderID is the unique identifier for the lock holder.
	HolderID string

	// Reentrant indicates whether re-acquisition by the same holder
	// is permitted.
	Reentrant bool

	// AcquireCount is the number of times a reentrant holder has
	// acquired the lock. Always 1 for non-reentrant locks.
	AcquireCount int32

	// TTL is the lease time-to-live in seconds.
	TTL int64

	// AcquiredAt is the Unix timestamp in seconds when the lock was
	// first acquired.
	AcquiredAt int64
}

// LockOptions configures lock acquisition behavior.
type LockOptions struct {
	// Key is the resource to lock. Required.
	Key string

	// HolderID is the unique holder identifier. A UUID is generated
	// automatically when empty.
	HolderID string

	// TTL is the lease time-to-live. Required and must be positive.
	TTL time.Duration

	// Reentrant allows the same holder to re-acquire the lock,
	// incrementing AcquireCount on each acquisition.
	Reentrant bool
}

// lockValue is the JSON-encoded value stored in the KV store for each
// active lock. Only internal lock state is persisted; revision metadata
// is read from the KeyValue returned by GetRevision.
type lockValue struct {
	HolderID     string `json:"holder_id"`
	Reentrant    bool   `json:"reentrant"`
	AcquireCount int32  `json:"acquire_count"`
	AcquiredAt   int64  `json:"acquired_at"`
	LeaseID      int64  `json:"lease_id"`
	TTL          int64  `json:"ttl"`
	// FencingToken is the key's create revision, stored for a binary from
	// before the token was read from the create revision, which reads it
	// here. It is written until every node reads the create revision
	// (WithTokenFieldRetired) and omitted after; this binary never reads it.
	FencingToken int64 `json:"fencing_token,omitempty"`
}

// lockKey returns the fully-qualified internal key for a user-provided key.
func lockKey(key string) string {
	return lockKeyPrefix + key
}

// userKey strips the internal lock prefix from a fully-qualified key.
func userKey(key string) string {
	if len(key) > len(lockKeyPrefix) {
		return key[len(lockKeyPrefix):]
	}
	return key
}
