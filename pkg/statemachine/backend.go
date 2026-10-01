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

package statemachine

import (
	"io"
	"sort"
	"sync"
)

// Backend abstracts the underlying key-value storage for state machines.
// Implementations handle get/put/delete/list/scan operations with their
// own concurrency model (single-threaded, sharded, disk-backed, etc.).
//
// There are TWO write contracts here, and the difference is the whole reason
// the interface carries four write methods instead of two.
//
// Put and Delete are the UNGUARDED pair. Their promise is "your write is
// queued": an implementation may buffer them and persist in the background,
// and a storage failure may be logged rather than returned. FastBackend exists
// to provide exactly that, and nothing in this change alters it.
//
// PutDurable and DeleteDurable are the GUARDED pair, and every implementation
// must satisfy three properties:
//
//   - DURABLE ON RETURN. A nil return means the write has taken effect, so a
//     caller told "you won the claim" or "you released it" is not contradicted
//     by a restart.
//   - FAILURE RETURNED. A storage fault comes back as an error rather than
//     reaching a log line nobody reads, so the guard can refuse instead of
//     reporting a success that did not happen.
//   - ORDERED. The write lands after every earlier write to the same key, so
//     no pending older write can overwrite it.
//
// They are METHODS ON Backend rather than an optional interface a backend may
// or may not implement (owner ruling 2026-09-03, go-qrdb#216, option 2). An
// optional interface is the idiomatic Go shape and it is wrong here: a backend
// that does not implement it falls back to the asynchronous path SILENTLY, and
// silently degrading is the exact thing the guarded plane exists to refuse. An
// opt-in guarantee is not a guarantee. On this interface a new backend — in
// this repository or outside it — cannot compile without answering, and the
// answer is visible in its source rather than inferred from a type assertion.
//
// A backend with no durability to promise satisfies the guarded pair by
// delegating: for a pure in-memory map the write has landed when Put returns,
// so the two contracts coincide. The pair is not redundant there — it is
// trivially satisfied there, which is a different statement, and the one
// FastBackend does not get to make.
type Backend interface {
	// Get retrieves the value for the given key. Returns nil, nil when
	// the key does not exist.
	Get(key []byte) ([]byte, error)

	// Put stores a key-value pair. Implementations may buffer writes
	// and persist asynchronously.
	Put(key, value []byte) error

	// Delete removes a key. Implementations may buffer deletes and
	// persist asynchronously.
	Delete(key []byte) error

	// PutDurable stores a key-value pair and returns only once the write
	// has taken effect, reporting a failure instead of logging it. See the
	// contract note above the interface.
	PutDurable(key, value []byte) error

	// DeleteDurable removes a key and returns only once the removal has
	// taken effect, reporting a failure instead of logging it. See the
	// contract note above the interface.
	DeleteDurable(key []byte) error

	// ApplyAtomic applies every mutation in muts, or none of them. It is the
	// write a multi-key transaction or batch commits through.
	//
	//   - ALL OR NOTHING TO READERS. A concurrent Lookup, List, Scan or
	//     snapshot never observes a subset of the set.
	//   - DURABLE AS THE STATE MACHINE RECOVERS. Where recovery reads this
	//     backend's storage (DiskBackend), the set is one storage batch that is
	//     durable on return, all or nothing after a crash, with a failure
	//     returned. Where recovery rebuilds the keyspace from the Raft log and
	//     snapshots instead (the fast state machine resets its storage at every
	//     start), the log entry is the durable copy and storage is written
	//     through asynchronously, in apply order, as Put writes it.
	//
	// Mutations are applied in order, so a later mutation of a key wins over
	// an earlier one in the same set. An empty set is a no-op. A failure
	// leaves the backend as it was before the call.
	ApplyAtomic(muts []Mutation) error

	// List returns all keys matching the given prefix. An empty prefix
	// returns all keys.
	List(prefix []byte) ([][]byte, error)

	// Scan returns all key-value pairs matching the given prefix as a
	// map. When limit > 0, the scan stops after collecting that many
	// entries.
	Scan(prefix []byte, limit int) (map[string][]byte, error)

	// SaveSnapshot serializes all backend state to the writer. The
	// stopper channel signals cancellation.
	SaveSnapshot(w io.Writer, stopper <-chan struct{}) error

	// RecoverSnapshot replaces all backend state with data read from
	// the reader. The stopper channel signals cancellation.
	RecoverSnapshot(r io.Reader, stopper <-chan struct{}) error

	// GetHash computes a deterministic hash over all key-value pairs
	// for cross-replica consistency verification.
	GetHash() (uint64, error)

	// Close releases all resources held by the backend. It must wait
	// for any in-flight asynchronous operations to complete.
	Close() error
}

// Mutation is one write in a set Backend.ApplyAtomic applies all or nothing:
// a put of Value under Key, or, when Delete is set, the removal of Key.
type Mutation struct {
	Key    []byte
	Value  []byte
	Delete bool
}

// lastWritePerKey returns muts with every mutation that a later one of the
// same key supersedes removed, keeping the survivors in their original order.
//
// Backend.ApplyAtomic promises that the later mutation of a key wins. The map
// backends get that for free by applying in order, but the sharded caches
// record an in-flight claim per key, and two claims on one key in one set
// would make a rollback restore the first write's value instead of what the
// cache held before the set — so they apply the collapsed set instead.
func lastWritePerKey(muts []Mutation) []Mutation {
	last := make(map[string]int, len(muts))
	for i, m := range muts {
		last[string(m.Key)] = i
	}
	if len(last) == len(muts) {
		return muts
	}
	out := make([]Mutation, 0, len(last))
	for i, m := range muts {
		if last[string(m.Key)] == i {
			out = append(out, m)
		}
	}
	return out
}

// ownMutations returns a deep copy of muts. Apply payloads are recycled
// buffers, so a backend that retains a mutation's key or value must copy it
// first; copying before any lock is taken keeps the copy out of the critical
// section.
func ownMutations(muts []Mutation) []Mutation {
	out := make([]Mutation, len(muts))
	for i, m := range muts {
		out[i] = Mutation{Key: append([]byte(nil), m.Key...), Delete: m.Delete}
		if !m.Delete {
			out[i].Value = append(make([]byte, 0, len(m.Value)), m.Value...)
		}
	}
	return out
}

// shardSet describes the mutex-guarded map shards of a sharded in-memory
// backend to applyAcrossShards: which shard a key belongs to, that shard's
// lock, and — read only while the lock is held, because RecoverSnapshot and
// Close replace it — that shard's map.
type shardSet struct {
	indexOf func(key string) int
	lock    func(i int) *sync.RWMutex
	data    func(i int) map[string][]byte
}

// applyAcrossShards is Backend.ApplyAtomic for the sharded in-memory backends.
//
// Every shard the set touches is write-locked BEFORE any of it is applied and
// released only after all of it is, so a concurrent Lookup — which takes one
// shard's read lock at a time — sees each key either before the set or after
// it, and a snapshot, which read-locks every shard, sees none of it or all of
// it. The locks are taken in ascending shard order, the order SaveSnapshot
// takes them in, so two lockers can never wait on each other.
func applyAcrossShards(muts []Mutation, s shardSet) {
	owned := ownMutations(muts)

	idx := make([]int, 0, len(owned))
	seen := make(map[int]struct{}, len(owned))
	for _, m := range owned {
		i := s.indexOf(string(m.Key))
		if _, dup := seen[i]; !dup {
			seen[i] = struct{}{}
			idx = append(idx, i)
		}
	}
	sort.Ints(idx)

	for _, i := range idx {
		s.lock(i).Lock()
	}
	for _, m := range owned {
		data := s.data(s.indexOf(string(m.Key)))
		if m.Delete {
			delete(data, string(m.Key))
			continue
		}
		data[string(m.Key)] = m.Value
	}
	for j := len(idx) - 1; j >= 0; j-- {
		s.lock(idx[j]).Unlock()
	}
}
