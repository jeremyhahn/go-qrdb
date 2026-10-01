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

package rowlock

import (
	"context"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

const (
	// DefaultLeaseTTL is the TTL, in seconds, of the lease every locking
	// transaction's records are bound to: how long a crashed holder's locks
	// outlive it. The owner renews it every third of that.
	DefaultLeaseTTL int64 = 10

	// MaxDetectionRecords bounds the lock records one deadlock check reads.
	// A lock table larger than this makes the requester the victim rather
	// than wait on a wait-for graph that was not fully read -- InnoDB's rule
	// for a graph too large to search.
	MaxDetectionRecords = 10_000

	// maxRecordsPerProposal bounds how many lock records one enqueue
	// transaction writes, keeping an enqueue of a large scan well under the
	// per-proposal size ceiling.
	maxRecordsPerProposal = 1_000
)

// Deps are the (tenant, database)-scoped servicers a Table locks through.
// They must all address the same store: the registry-namespaced servicers
// for one database.
type Deps struct {
	// Txn writes and withdraws lock records.
	Txn service.TransactionServicer

	// MVCC reads queues (linearizably) and the whole table for deadlock
	// detection.
	MVCC service.MVCCKVServicer

	// Lease binds every record to its owner's liveness.
	Lease service.LeaseServicer

	// Watch wakes a waiting request when a record ahead of it is deleted.
	Watch service.WatchServicer
}

// Table is the lock table of one (tenant, database) store. It is a cheap,
// stateless handle: building one per statement is fine, and two Tables over
// the same store with the same scope are interchangeable.
type Table struct {
	deps     Deps
	scope    string
	leaseTTL int64
	now      func() time.Time
}

// Option configures a Table.
type Option func(*Table)

// WithLeaseTTL overrides DefaultLeaseTTL, in seconds. Values below one are
// ignored.
func WithLeaseTTL(seconds int64) Option {
	return func(t *Table) {
		if seconds >= 1 {
			t.leaseTTL = seconds
		}
	}
}

// New returns the lock table reached through deps. scope names the store --
// "<tenant>/<database>" -- and is what an Owner binds to: an owner refuses to
// lock in a second scope (see ScopeMismatchError).
func New(deps Deps, scope string, opts ...Option) (*Table, error) {
	switch {
	case deps.Txn == nil:
		return nil, &MissingDependencyError{Name: "transaction service"}
	case deps.MVCC == nil:
		return nil, &MissingDependencyError{Name: "mvcc service"}
	case deps.Lease == nil:
		return nil, &MissingDependencyError{Name: "lease service"}
	case deps.Watch == nil:
		return nil, &MissingDependencyError{Name: "watch service"}
	}
	t := &Table{deps: deps, scope: scope, leaseTTL: DefaultLeaseTTL, now: time.Now}
	for _, opt := range opts {
		opt(t)
	}
	return t, nil
}

// Scope returns the scope the table was built for.
func (t *Table) Scope() string { return t.scope }

// queue reads key's records, linearizably, in FIFO order.
func (t *Table) queue(ctx context.Context, key string) ([]record, error) {
	prefix := queuePrefix(key)
	kvs, _, _, err := t.deps.MVCC.Range(ctx, []byte(prefix), prefixEnd(prefix))
	if err != nil {
		return nil, err
	}
	q := make([]record, 0, len(kvs))
	for _, kvp := range kvs {
		r, perr := parseRecord(kvp)
		if perr != nil {
			return nil, perr
		}
		q = append(q, r)
	}
	sortQueue(q)
	return q, nil
}

// state evaluates owner's request for mode on key against the live queue.
func (t *Table) state(ctx context.Context, key, owner string, mode Mode) (grantState, error) {
	q, err := t.queue(ctx, key)
	if err != nil {
		return stateMissing, err
	}
	return evaluate(q, owner, mode), nil
}

// deadlocked reports whether owner is on a cycle of the live wait-for graph,
// and whether the graph was too large to read (in which case owner is the
// victim by rule).
func (t *Table) deadlocked(ctx context.Context, owner string) (cycle, unsearched bool, err error) {
	kvs, _, _, err := t.deps.MVCC.Range(ctx, []byte(Prefix), prefixEnd(Prefix),
		service.WithLimit(MaxDetectionRecords+1))
	if err != nil {
		return false, false, err
	}
	if len(kvs) > MaxDetectionRecords {
		return false, true, nil
	}
	records := make([]record, 0, len(kvs))
	for _, kvp := range kvs {
		r, perr := parseRecord(kvp)
		if perr != nil {
			return false, false, perr
		}
		records = append(records, r)
	}
	return onCycle(waitsFor(records), owner), false, nil
}

// watchQueue watches key's queue for deletions. The watch is opened before
// the caller re-reads the queue, so a deletion between that read and the
// wait is delivered rather than missed.
func (t *Table) watchQueue(ctx context.Context, key string) (watch.Watcher, error) {
	return t.deps.Watch.WatchPrefix(ctx, queuePrefix(key), watch.WithFilters(watch.WatchFilterNoPut))
}

// enqueue writes owner's records for mode on keys, bound to leaseID, in as
// few transactions as maxRecordsPerProposal allows. It returns the keys
// whose records were written, so a failure part way can withdraw exactly
// those.
func (t *Table) enqueue(ctx context.Context, keys []string, owner string, mode Mode, leaseID int64) (written []string, err error) {
	value := encodeValue(mode, t.now().UnixNano())
	for start := 0; start < len(keys); start += maxRecordsPerProposal {
		end := min(start+maxRecordsPerProposal, len(keys))
		ops := make([]service.Op, 0, end-start)
		for _, k := range keys[start:end] {
			ops = append(ops, service.Op{Type: service.OpPut, Key: []byte(recordKey(k, owner, mode)), Value: value, LeaseID: leaseID})
		}
		if _, err := t.deps.Txn.Execute(ctx, nil, ops, nil); err != nil {
			return written, err
		}
		written = append(written, keys[start:end]...)
	}
	return written, nil
}

// leaseAlive confirms, with a linearizable read issued after an enqueue
// has been applied, that leaseID still exists. Raft orders the enqueue and
// any revoke of the lease (an explicit revoke or the leader's expiry
// sweep): if the lease is still present now, the revoke has not been
// applied yet, so the records were written while it existed, were attached
// to it, and will go with it. If it is gone, the records may have been
// written after it ended and attached to nothing; the caller removes them
// (see Owner.lostAtEnqueue). A missing lease is *service.LeaseNotFoundError.
func (t *Table) leaseAlive(ctx context.Context, leaseID int64) error {
	_, err := t.deps.Lease.TimeToLive(ctx, leaseID)
	return err
}

// withdraw deletes owner's records for mode on keys.
func (t *Table) withdraw(ctx context.Context, keys []string, owner string, mode Mode) error {
	for start := 0; start < len(keys); start += maxRecordsPerProposal {
		end := min(start+maxRecordsPerProposal, len(keys))
		ops := make([]service.Op, 0, end-start)
		for _, k := range keys[start:end] {
			ops = append(ops, service.Op{Type: service.OpDelete, Key: []byte(recordKey(k, owner, mode))})
		}
		if _, err := t.deps.Txn.Execute(ctx, nil, ops, nil); err != nil {
			return err
		}
	}
	return nil
}
