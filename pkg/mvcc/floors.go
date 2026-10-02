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

package mvcc

import (
	"bytes"
	"slices"

	"github.com/jeremyhahn/go-qrdb/pkg/tenant"
)

// compactionFloors is an immutable snapshot of the store's compaction floors:
// for each keyspace prefix that has been compacted, the revision it was
// compacted to, and the floor of the node keyspace.
//
// The store keeps a floor per prefix rather than one for the whole store
// because one store serves many databases -- every prefix-isolated tenant's
// databases share the system shard -- and compacting one database must not
// discard another's history. A read is refused when its revision is at or
// below a floor that covers it: for a key, the floor of every prefix of the
// key; for a range, every floor whose keyspace the range overlaps.
//
// The node keyspace is every key outside all "<tenant>/<db>/" namespaces
// (tenant.NamespaceOf): the node's own internal records and the system
// tenant's raw keyspace. Its floor covers exactly those keys, so it never
// covers a key of any tenant's database.
//
// A snapshot is never modified after it is published; with returns a new one.
// Readers load the current snapshot through an atomic pointer, so a read never
// takes a lock and never observes a half-applied compaction.
type compactionFloors struct {
	// byPrefix maps a compacted prefix to its floor.
	byPrefix map[string]int64

	// lengths holds the distinct lengths of the prefixes in byPrefix,
	// ascending, so a key's covering floors are found with one map lookup per
	// length rather than a scan of every floor.
	lengths []int

	// node is the node keyspace's floor; zero when it was never compacted.
	node int64
}

// emptyFloors is the snapshot of a store nothing has been compacted in.
var emptyFloors = &compactionFloors{byPrefix: map[string]int64{}}

// with returns a copy of f with prefix's floor set to revision.
func (f *compactionFloors) with(prefix []byte, revision int64) *compactionFloors {
	next := &compactionFloors{byPrefix: make(map[string]int64, len(f.byPrefix)+1), node: f.node}
	for p, rev := range f.byPrefix {
		next.byPrefix[p] = rev
	}
	next.byPrefix[string(prefix)] = revision
	for p := range next.byPrefix {
		if !slices.Contains(next.lengths, len(p)) {
			next.lengths = append(next.lengths, len(p))
		}
	}
	slices.Sort(next.lengths)
	return next
}

// withNode returns a copy of f with the node keyspace's floor set to
// revision.
func (f *compactionFloors) withNode(revision int64) *compactionFloors {
	next := *f
	next.node = revision
	return &next
}

// forKey returns the highest floor covering key: the largest floor among the
// prefixes of key, including the empty prefix, and the node keyspace's floor
// when key lies outside every database namespace. Zero means nothing covering
// key has been compacted.
func (f *compactionFloors) forKey(key []byte) int64 {
	var floor int64
	for _, n := range f.lengths {
		if n > len(key) {
			break
		}
		if rev, ok := f.byPrefix[string(key[:n])]; ok && rev > floor {
			floor = rev
		}
	}
	if f.node > floor {
		if _, namespaced := tenant.NamespaceOf(key); !namespaced {
			floor = f.node
		}
	}
	return floor
}

// forRange returns the highest floor whose keyspace overlaps [start, end). A
// nil end is unbounded above. The node keyspace's floor counts unless the
// whole range lies inside one database namespace -- the only ranges that
// provably hold no node-keyspace key.
func (f *compactionFloors) forRange(start, end []byte) int64 {
	var floor int64
	for p, rev := range f.byPrefix {
		if rev > floor && prefixOverlaps([]byte(p), start, end) {
			floor = rev
		}
	}
	if f.node > floor && !withinOneNamespace(start, end) {
		floor = f.node
	}
	return floor
}

// withinOneNamespace reports whether every key in [start, end) lies under the
// database namespace of start.
func withinOneNamespace(start, end []byte) bool {
	ns, ok := tenant.NamespaceOf(start)
	if !ok || end == nil {
		return false
	}
	nsEnd := prefixEnd(ns)
	return nsEnd != nil && bytes.Compare(end, nsEnd) <= 0
}

// prefixOverlaps reports whether the keyspace of prefix -- every key that
// begins with it -- intersects [start, end). A nil end is unbounded above.
func prefixOverlaps(prefix, start, end []byte) bool {
	if pEnd := prefixEnd(prefix); pEnd != nil && bytes.Compare(start, pEnd) >= 0 {
		return false
	}
	if end != nil && bytes.Compare(prefix, end) >= 0 {
		return false
	}
	return true
}
