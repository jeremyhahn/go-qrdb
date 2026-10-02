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

package shard

import "strconv"

// ShardAlreadyRegisteredError is returned when attempting to register a
// shard that is already present in the registry.
type ShardAlreadyRegisteredError struct {
	ShardID uint64
}

func (e *ShardAlreadyRegisteredError) Error() string {
	return "shard: already registered: " + strconv.FormatUint(e.ShardID, 10)
}

// ShardNotRegisteredError is returned when an operation references a
// shard that is not present in the registry.
type ShardNotRegisteredError struct {
	ShardID uint64
}

func (e *ShardNotRegisteredError) Error() string {
	return "shard: not registered: " + strconv.FormatUint(e.ShardID, 10)
}

// InvalidShardIDError is returned when a shard ID of zero is provided.
type InvalidShardIDError struct {
	ShardID uint64
}

func (e *InvalidShardIDError) Error() string {
	return "shard: invalid shard ID: " + strconv.FormatUint(e.ShardID, 10)
}

// ClaimerConfigError is returned when a Claimer is constructed without a
// required dependency.
type ClaimerConfigError struct {
	Field string
}

func (e *ClaimerConfigError) Error() string {
	return "shard: claimer requires " + e.Field
}

// ClaimStoreError wraps a failure of the replicated store while claiming a
// shard ID. The outcome of the failed operation is unknown, so no ID was
// issued to the caller; an ID the store did record is left as a gap.
type ClaimStoreError struct {
	Op  string
	Err error
}

func (e *ClaimStoreError) Error() string {
	return "shard: claim " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the store's error.
func (e *ClaimStoreError) Unwrap() error { return e.Err }

// HighWaterCorruptError is returned when the replicated high-water mark does
// not hold a decimal shard ID. A claim refuses rather than guess, because a
// guess can re-issue an ID.
type HighWaterCorruptError struct {
	Value string
}

func (e *HighWaterCorruptError) Error() string {
	return "shard: high-water mark is not a shard ID: " + strconv.Quote(e.Value)
}

// ClaimContentionError is returned when a claim lost the race for the
// high-water mark on every attempt. Each lost race is another node's claim
// committing, so the condition is transient.
type ClaimContentionError struct {
	Attempts int
}

func (e *ClaimContentionError) Error() string {
	return "shard: claim lost the high-water race " + strconv.Itoa(e.Attempts) + " times"
}

// FeatureNotActiveError is returned by a claim made before the cluster has
// activated the feature claims depend on: until every node runs a binary
// that claims shard IDs, no node issues one. Nothing was claimed; the claim
// succeeds once the feature is active.
type FeatureNotActiveError struct {
	Feature string
}

func (e *FeatureNotActiveError) Error() string {
	return "shard: cluster feature " + strconv.Quote(e.Feature) +
		" is not active yet: raft databases cannot be created until every node runs a binary that supports it"
}

// ShardIDExhaustedError is returned when no shard ID remains to issue.
type ShardIDExhaustedError struct{}

func (e *ShardIDExhaustedError) Error() string {
	return "shard: shard ID space exhausted"
}
