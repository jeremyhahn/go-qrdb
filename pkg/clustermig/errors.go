// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package clustermig

import (
	"errors"
	"strconv"
)

var (
	// ErrNilMigration is returned when a nil ClusterMigration is persisted.
	ErrNilMigration = errors.New("clustermig: nil migration record")
	// ErrEmptyTarget is returned when a migration record has no target strategy.
	ErrEmptyTarget = errors.New("clustermig: migration target strategy is empty")
	// ErrNilStatus is returned when a nil or node-less status is persisted.
	ErrNilStatus = errors.New("clustermig: nil or node-less status record")
	// ErrNotLeader is returned when a coordinator intent write is attempted on a
	// node that is not the cluster leader; the caller should forward to the leader.
	ErrNotLeader = errors.New("clustermig: not the cluster leader")
	// ErrNoOwners is returned when a migration is requested for a scope whose
	// owner set resolves empty (no placed replicas / no cluster members).
	ErrNoOwners = errors.New("clustermig: migration scope has no owners")
	// ErrCredentialAwaited is returned by a CredentialSource when this node's
	// credential is not there yet: no request credential is held for the
	// migration and no credential file exists. The owner waits in phase
	// awaiting-credential.
	ErrCredentialAwaited = errors.New("clustermig: this node's migration credential is not available yet")
	// ErrEmptyCredential is returned when an empty credential is offered.
	ErrEmptyCredential = errors.New("clustermig: empty migration credential")
)

// MigrationCredentialFileError is returned when this node's migration
// credential file exists but is refused: not a regular file, readable by
// group or others, not owned by the daemon's user, empty, or too large.
type MigrationCredentialFileError struct {
	Path   string
	Reason string
}

func (e *MigrationCredentialFileError) Error() string {
	return "clustermig: migration credential file " + e.Path + " refused: " + e.Reason
}

// StoreError wraps a coordination-store operation failure, naming the step.
type StoreError struct {
	Op  string
	Err error
}

func (e *StoreError) Error() string { return "clustermig: " + e.Op + ": " + e.Err.Error() }
func (e *StoreError) Unwrap() error { return e.Err }

// NotShardLeaderError is a ReplicationProgress query on a node that does not
// lead the shard: only the leader tracks how far each replica's log has got,
// so the rolling step waits, and Advance reports why.
type NotShardLeaderError struct {
	ShardID uint64
}

func (e *NotShardLeaderError) Error() string {
	return "clustermig: replication progress of shard " + strconv.FormatUint(e.ShardID, 10) + " is known only to its leader"
}

// NotShardMemberError is a ReplicationProgress query for a node that is not a
// replica of the shard, so it has no log to catch up.
type NotShardMemberError struct {
	ShardID uint64
	NodeID  string
}

func (e *NotShardMemberError) Error() string {
	return "clustermig: node " + e.NodeID + " is not a replica of shard " + strconv.FormatUint(e.ShardID, 10)
}
