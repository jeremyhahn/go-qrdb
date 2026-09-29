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

import "strconv"

// ShardStatsError is returned when querying shard statistics fails.
type ShardStatsError struct {
	ShardID uint64
	Err     error
}

// Error returns a human-readable description of the shard stats failure.
func (e *ShardStatsError) Error() string {
	return "service: get shard stats " + strconv.FormatUint(e.ShardID, 10) + " failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *ShardStatsError) Unwrap() error {
	return e.Err
}

// RebalancePlanError is returned when generating a rebalance plan fails.
type RebalancePlanError struct {
	Err error
}

// Error returns a human-readable description of the rebalance plan failure.
func (e *RebalancePlanError) Error() string {
	return "service: create rebalance plan failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *RebalancePlanError) Unwrap() error {
	return e.Err
}

// RebalanceExecuteError is returned when executing a rebalance plan fails.
type RebalanceExecuteError struct {
	Err error
}

// Error returns a human-readable description of the rebalance execution failure.
func (e *RebalanceExecuteError) Error() string {
	return "service: execute rebalance failed: " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *RebalanceExecuteError) Unwrap() error {
	return e.Err
}

// RebalanceMoveError is returned when a single dedicated-tenant shard MOVE
// fails during ExecuteRebalance. It carries the shard, the tenant whose group
// was being moved, the name of the step that failed, and the underlying cause.
type RebalanceMoveError struct {
	// ShardID is the Raft shard that was being moved.
	ShardID uint64

	// TenantID is the tenant whose dedicated Raft group was being moved.
	TenantID string

	// Step names the move phase that failed: "leader-routing", "wait-leader",
	// "find-source", "add-placement", "add-observer", "remove-placement", or
	// "remove-node".
	Step string

	// Err is the underlying error that caused the step to fail.
	Err error
}

// Error returns a human-readable description of the MOVE failure.
func (e *RebalanceMoveError) Error() string {
	return "service: rebalance move shard " + strconv.FormatUint(e.ShardID, 10) +
		" tenant " + e.TenantID + " failed at " + e.Step + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for use with errors.Is and errors.As.
func (e *RebalanceMoveError) Unwrap() error { return e.Err }

// sourceReplicaNotFoundError is returned when the FromNode address in a
// RebalanceMovePlan cannot be matched to any Replica in the Placement entry
// for a dedicated-tenant shard. This is a programming-time configuration
// error: the plan must name a node that currently hosts the shard.
type sourceReplicaNotFoundError struct {
	ShardID  uint64
	FromNode string
}

// Error returns a human-readable description.
func (e *sourceReplicaNotFoundError) Error() string {
	return "rebalance: source replica not found for shard " +
		strconv.FormatUint(e.ShardID, 10) + " node " + e.FromNode
}

// RebalanceSystemShardMoveError is returned (and logged) for a planned move of
// the system shard, which ExecuteRebalance refuses. Every host of a cluster is
// a member of the system shard — its membership is where each host's identity
// is committed — so a destination the cluster can vouch for already hosts a
// replica of it, and adding a second one on that host is not a move. A
// destination that is not a member is a brand-new node, whose identity no
// committed membership holds: it joins through AddMember with an
// operator-supplied identity, never through a rebalance.
type RebalanceSystemShardMoveError struct {
	ShardID uint64
	ToNode  string
}

// Error returns a human-readable description.
func (e *RebalanceSystemShardMoveError) Error() string {
	return "service: rebalance refuses to move system shard " + strconv.FormatUint(e.ShardID, 10) +
		" to " + e.ToNode + ": every cluster host already hosts it; a new host joins through AddMember with its identity"
}

// RebalanceTargetHostsReplicaError is returned (and logged) for a planned move
// whose destination already hosts a replica of the shard: a host holds at
// most one replica of a shard, so the add would bind a second replica ID to
// the same host and identity.
type RebalanceTargetHostsReplicaError struct {
	ShardID uint64
	ToNode  string
}

// Error returns a human-readable description.
func (e *RebalanceTargetHostsReplicaError) Error() string {
	return "service: rebalance refuses to add shard " + strconv.FormatUint(e.ShardID, 10) +
		" to " + e.ToNode + ": it already hosts a replica of that shard"
}
