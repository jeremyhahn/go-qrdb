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

package featuregate

import "strconv"

// UnsupportedFeatureError refuses to start a binary under a cluster that has
// activated a feature the binary does not implement. Starting would let it
// receive commands it cannot apply and diverge from its peers.
type UnsupportedFeatureError struct {
	Feature Feature
}

// Error implements error.
func (e *UnsupportedFeatureError) Error() string {
	return "featuregate: the cluster has activated feature " + string(e.Feature) +
		", which this binary does not implement; run a release that does"
}

// ConfigError reports a Gate built or started without a required field.
type ConfigError struct {
	Field string
}

// Error implements error.
func (e *ConfigError) Error() string {
	return "featuregate: invalid configuration: " + e.Field
}

// StoreError reports that the gate could not read or write the system store.
type StoreError struct {
	Op  string
	Err error
}

// Error implements error.
func (e *StoreError) Error() string {
	return "featuregate: " + e.Op + ": " + e.Err.Error()
}

// Unwrap returns the cause.
func (e *StoreError) Unwrap() error { return e.Err }

// NotAMemberError reports that the local node is not a member of the system
// shard, so it has no committed address to advertise.
type NotAMemberError struct {
	ReplicaID uint64
	ShardID   uint64
}

// Error implements error.
func (e *NotAMemberError) Error() string {
	return "featuregate: replica " + strconv.FormatUint(e.ReplicaID, 10) +
		" is not a member of shard " + strconv.FormatUint(e.ShardID, 10)
}

// UnsupportedReplicaError refuses to add a replica of a group on a node that
// does not advertise a feature the group uses: go-quicraft would replay the
// group's log to it, and it would diverge on the first entry that uses the
// feature. A node that is not yet a member has no advertisement until its
// enrollment publishes one, so after activation it is refused until then.
type UnsupportedReplicaError struct {
	ShardID uint64
	Address string
	Feature Feature
}

// Error implements error.
func (e *UnsupportedReplicaError) Error() string {
	return "featuregate: cannot add a replica of shard " + strconv.FormatUint(e.ShardID, 10) +
		" on " + e.Address + ": no live feature advertisement from that node names " + string(e.Feature) +
		", which the group uses; a joining node's advertisement is published by its enrollment, " +
		"a member's by its own gate -- enroll or upgrade the node first"
}

// NotActiveError refuses an operation that depends on a cluster feature the
// cluster has not yet activated: until every node -- for a group's command,
// every replica of the group -- runs a binary that implements the feature,
// using it could let one node act on what another cannot apply. Nothing was
// done. It is the one refusal for every feature-gated operation, and it is
// transient: the operation succeeds once activation completes, so it is
// classified Unavailable on every transport.
type NotActiveError struct {
	// Feature is the feature the operation needs.
	Feature Feature
	// ShardID is the group whose replicas must all support the feature, or
	// zero for a feature the operation needs cluster-wide.
	ShardID uint64
}

// Error implements error.
func (e *NotActiveError) Error() string {
	msg := "featuregate: cluster feature " + strconv.Quote(string(e.Feature)) + " is not active yet"
	if e.ShardID != 0 {
		msg += " on shard " + strconv.FormatUint(e.ShardID, 10)
	}
	return msg + ": every node must run a release that supports it"
}
