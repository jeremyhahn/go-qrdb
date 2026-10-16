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

// Package consistency defines consistency levels for read operations.
// These levels allow clients to trade consistency for performance based
// on their requirements.
package consistency

import "strconv"

// Level represents the consistency guarantee for a read operation.
type Level int

const (
	// Strong requires full Raft consensus on every read (SyncRead).
	// Guarantees linearizable reads but has highest latency (10-50ms).
	// Use for: financial transactions, critical data where stale reads
	// are unacceptable.
	Strong Level = iota

	// Linearizable uses Raft ReadIndex optimization. Guarantees
	// linearizable reads with much lower latency (1-5ms). Leader
	// confirms it is still leader, then reads from local state machine.
	// This is the recommended default for most applications.
	Linearizable

	// LeaseRead allows the Raft leader to serve reads directly when its
	// leadership lease is still valid, skipping the ReadIndex round-trip.
	// The lease is measured on the leader's monotonic clock, so reads stay
	// linearizable, with no stale-read window, while replica clock rates
	// differ by less than 1%; near or after lease expiry the read falls back
	// to the heartbeat round. Use for: high-throughput read workloads.
	LeaseRead

	// Eventual allows reading from local node without Raft coordination.
	// Fastest reads (<1ms) but may return stale data if node is
	// partitioned. Use for: analytics, dashboards, caching where
	// staleness is acceptable.
	Eventual

	// FollowerRead reads from the local state machine after the node
	// confirms it has applied up to the committed index via ReadIndex.
	// Unlike Eventual (StaleRead), this guarantees the read reflects
	// all committed entries without requiring the client to contact the
	// leader. Provides stronger guarantees than Eventual with lower
	// latency than SyncRead on followers since the leader does not
	// need to execute the query itself.
	FollowerRead
)

// String returns the string representation of the consistency level.
func (l Level) String() string {
	switch l {
	case Strong:
		return "strong"
	case Linearizable:
		return "linearizable"
	case LeaseRead:
		return "lease_read"
	case Eventual:
		return "eventual"
	case FollowerRead:
		return "follower_read"
	default:
		return "unknown(" + strconv.Itoa(int(l)) + ")"
	}
}

// ParseLevel converts a string to a consistency level. An empty string
// returns the default level (Linearizable). Recognized values are:
// "strong", "linearizable", "lease_read" (aliases: "leaseread"),
// "eventual" (aliases: "stale"), "follower_read" (aliases: "followerread").
// All values are accepted in upper-case.
func ParseLevel(s string) (Level, error) {
	switch s {
	case "strong", "STRONG":
		return Strong, nil
	case "linearizable", "LINEARIZABLE", "":
		return Linearizable, nil
	case "lease_read", "LEASE_READ", "leaseread", "LEASEREAD":
		return LeaseRead, nil
	case "eventual", "EVENTUAL", "stale", "STALE":
		return Eventual, nil
	case "follower_read", "FOLLOWER_READ", "followerread", "FOLLOWERREAD":
		return FollowerRead, nil
	default:
		return Linearizable, &UnknownLevelError{Level: s}
	}
}

// IsValid returns true if the consistency level is a recognized value.
func (l Level) IsValid() bool {
	return l >= Strong && l <= FollowerRead
}

// Default returns the default consistency level (Linearizable).
func Default() Level {
	return Linearizable
}
