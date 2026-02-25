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

// LeasesQuery queries all active leases.
type LeasesQuery struct{}
