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

package transport

// ---------------------------------------------------------------------------
// Write mode options
// ---------------------------------------------------------------------------

// WriteMode controls the durability guarantee for write operations.
type WriteMode uint8

const (
	// WriteModeSync waits for the write to be replicated to a quorum and
	// committed. Strongest durability (~10-30ms per write).
	WriteModeSync WriteMode = iota

	// WriteModeWALDurable waits only for local WAL persistence. Faster
	// (~1-2ms) but risks data loss if leader crashes before replication.
	WriteModeWALDurable

	// WriteModeBatched collects writes and proposes as a single Raft entry.
	// Same durability as Sync but amortized cost (~0.1-0.3ms per op).
	WriteModeBatched
)

// String returns the human-readable name of the write mode.
func (m WriteMode) String() string {
	switch m {
	case WriteModeSync:
		return "sync"
	case WriteModeWALDurable:
		return "wal_durable"
	case WriteModeBatched:
		return "batched"
	default:
		return "unknown"
	}
}

// IsValid reports whether the write mode is a recognized value.
func (m WriteMode) IsValid() bool {
	return m <= WriteModeBatched
}

// ParseWriteMode converts a string to a WriteMode.
// Accepted values: "sync", "wal_durable", "batched".
func ParseWriteMode(s string) (WriteMode, error) {
	switch s {
	case "sync", "":
		return WriteModeSync, nil
	case "wal_durable":
		return WriteModeWALDurable, nil
	case "batched":
		return WriteModeBatched, nil
	default:
		return 0, &InvalidWriteModeError{Mode: s}
	}
}

// writeOptions holds resolved write configuration.
type writeOptions struct {
	Mode    WriteMode
	ModeSet bool
}

// WriteOption configures a write operation.
type WriteOption func(*writeOptions)

// WithWriteMode sets the write durability mode.
func WithWriteMode(mode WriteMode) WriteOption {
	return func(o *writeOptions) {
		o.Mode = mode
		o.ModeSet = true
	}
}

// ResolveWriteOptions applies the given options and returns the resolved
// configuration. Callers should check ModeSet to determine whether a
// non-default mode was explicitly requested.
func ResolveWriteOptions(opts ...WriteOption) writeOptions {
	var o writeOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// ---------------------------------------------------------------------------
// Read consistency options
// ---------------------------------------------------------------------------

// ConsistencyLevel determines the read consistency guarantee.
type ConsistencyLevel uint8

const (
	// ConsistencyStrong requires full Raft consensus on every read.
	// Guarantees linearizable reads but has highest latency (10-50ms).
	ConsistencyStrong ConsistencyLevel = iota

	// ConsistencyLinearizable uses Raft ReadIndex optimization. Guarantees
	// linearizable reads with much lower latency (1-5ms). This is the
	// recommended default for most applications.
	ConsistencyLinearizable

	// ConsistencyEventual allows reading from local node without Raft
	// coordination. Fastest reads (<1ms) but may return stale data if
	// node is partitioned.
	ConsistencyEventual

	// ConsistencyFollower reads from the local replica's state machine
	// after it has applied up to its committed index, without a leader
	// ReadIndex round-trip. Unlike ConsistencyEventual it reflects every
	// entry committed at read time (read-your-committed-writes), and unlike
	// ConsistencyLinearizable it is served locally on any replica — so
	// followers can serve reads even when the leader is momentarily
	// unreachable for a ReadIndex confirmation. Recommended for replicated
	// stores that read on every node (e.g. health checks, key existence).
	ConsistencyFollower
)

// String returns the human-readable name of the consistency level.
func (l ConsistencyLevel) String() string {
	switch l {
	case ConsistencyStrong:
		return "strong"
	case ConsistencyLinearizable:
		return "linearizable"
	case ConsistencyEventual:
		return "eventual"
	case ConsistencyFollower:
		return "follower"
	default:
		return "unknown"
	}
}

// IsValid reports whether the consistency level is a recognized value.
func (l ConsistencyLevel) IsValid() bool {
	return l <= ConsistencyFollower
}

// ParseConsistencyLevel converts a string to a ConsistencyLevel.
// Accepted values: "strong", "linearizable", "eventual", "stale".
func ParseConsistencyLevel(s string) (ConsistencyLevel, error) {
	switch s {
	case "strong":
		return ConsistencyStrong, nil
	case "linearizable", "":
		return ConsistencyLinearizable, nil
	case "eventual", "stale":
		return ConsistencyEventual, nil
	case "follower":
		return ConsistencyFollower, nil
	default:
		return 0, &InvalidConsistencyLevelError{Level: s}
	}
}

// readOptions holds resolved read configuration.
type readOptions struct {
	Consistency    ConsistencyLevel
	ConsistencySet bool
	TimeoutMs      int64
	AllowStale     bool
}

// ReadOption configures a read operation.
type ReadOption func(*readOptions)

// WithConsistency sets the read consistency level.
func WithConsistency(level ConsistencyLevel) ReadOption {
	return func(o *readOptions) {
		o.Consistency = level
		o.ConsistencySet = true
	}
}

// WithReadTimeout sets the maximum time in milliseconds to wait for
// the read to complete.
func WithReadTimeout(ms int64) ReadOption {
	return func(o *readOptions) {
		o.TimeoutMs = ms
	}
}

// WithAllowStale explicitly allows stale reads when ConsistencyEventual
// is in effect.
func WithAllowStale() ReadOption {
	return func(o *readOptions) {
		o.AllowStale = true
	}
}

// ResolveReadOptions applies the given options and returns the resolved
// configuration. Callers should check ConsistencySet to determine whether
// a non-default level was explicitly requested.
func ResolveReadOptions(opts ...ReadOption) readOptions {
	var o readOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}
