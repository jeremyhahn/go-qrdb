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

import "context"

// LeaseService manages distributed time-to-live leases. Keys attached
// to a lease are automatically deleted when the lease expires. Leases
// are replicated through Raft consensus so they survive leader failover.
type LeaseServicer interface {
	// Grant creates a new lease with the specified TTL in seconds.
	// The server may adjust the TTL to fit within configured bounds.
	Grant(ctx context.Context, ttl int64) (*Lease, error)

	// Revoke immediately expires a lease and deletes all attached keys.
	Revoke(ctx context.Context, leaseID int64) error

	// KeepAlive resets the TTL on an existing lease, preventing it
	// from expiring. Returns the refreshed lease with updated TTL.
	KeepAlive(ctx context.Context, leaseID int64) (*Lease, error)

	// TimeToLive returns the current state of a lease including its
	// remaining TTL and attached keys.
	TimeToLive(ctx context.Context, leaseID int64) (*Lease, error)

	// Leases returns all active leases.
	Leases(ctx context.Context) ([]*Lease, error)
}
