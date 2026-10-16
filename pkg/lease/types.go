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

package lease

// Lease represents a distributed time-to-live lease that can be attached
// to keys. When a lease expires, all attached keys are deleted.
type Lease struct {
	// ID is the unique lease identifier.
	ID int64

	// TTL is the advisory time-to-live in seconds.
	TTL int64

	// GrantedTTL is the initial TTL granted when the lease was created.
	GrantedTTL int64

	// RemainingTTL is the current remaining TTL in seconds.
	RemainingTTL int64

	// Keys lists the keys currently attached to this lease.
	Keys [][]byte
}

// LeaseTimeToLiveQuery queries the state of a lease by its ID.
type LeaseTimeToLiveQuery struct {
	ID int64
}

// LeaseKeepAliveQuery renews a lease in the memory of the replica that answers
// it, which must be the Raft leader. It travels as a linearizable read, not a
// proposal: the read confirms leadership with a heartbeat quorum and writes
// nothing to the log. A replica that is not the lease primary answers with
// *NotPrimaryError, and a lease whose checkpoint the renewal would change
// with *CheckpointedError (see Lessor.RenewLocal).
type LeaseKeepAliveQuery struct {
	ID int64
}

// LeaseCheckpointNowQuery asks the leader's lessor to checkpoint every live
// lease at once (see Lessor.RequestCheckpoint). It travels as a local read of
// the replica that answers it and is answered with a <-chan error that
// receives the outcome once the checkpoint has been applied, or with
// *NotPrimaryError on a replica that is not the lease primary.
type LeaseCheckpointNowQuery struct{}

// LeasesQuery queries all active leases.
type LeasesQuery struct{}
