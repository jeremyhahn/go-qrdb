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

// Package lease provides distributed time-to-live lease management for
// the MVCC key-value store. Leases allow keys to be automatically
// deleted when their lease expires, enabling session management,
// service discovery, and distributed locking patterns.
//
// Leases are replicated through Raft consensus. The lessor maintains an
// in-memory lease table, and the state machine stages every change to it into
// the same apply batch as the data write that caused it (see table.go), so
// the table is durable, travels in snapshots, and is restored with Restore
// when a state machine opens or recovers. Only the Raft leader runs the
// expiration timer to avoid duplicate revocations.
package lease

import (
	"bytes"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/clock"
	"github.com/jeremyhahn/go-qrdb/pkg/storage"
)

// lease is the internal lease state tracked by the Lessor.
//
// mu guards BOTH keys and expiry. expiry is mutated by KeepAlive, which runs
// on the Raft apply goroutine, and read by the lease-expiry sweeper on its own
// goroutine -- so an unsynchronised read of it is a genuine data race, not a
// benign one. It went unobserved because the only composition that ran the
// sweeper (cmd/qrdbd) has no -race test driving a keep-alive concurrently with
// a sweep; the race detector reported it the first time sdk/go/node started
// the sweeper under the embedded raft lease tests (go-qrdb#174).
//
// checkpointTTL, renewIndex and the leader-local pending mark are guarded by
// mu too.
type lease struct {
	id     int64
	ttl    int64 // granted TTL in seconds
	expiry time.Time
	keys   map[string]struct{} // attached keys

	// checkpointTTL is the remaining TTL, in seconds, the lease's checkpoint
	// record holds; 0 when it has none. It is replicated state: only apply
	// changes it (ApplyCheckpoints, KeepAlive), and Restore reads it back.
	checkpointTTL int64

	// renewIndex is the Raft index of the lease's last replicated renewal
	// recorded since the group's renewal marker; 0 when there is none. It is
	// replicated state like checkpointTTL, and it is what lets a checkpoint
	// apply reject a checkpoint sampled before that renewal.
	renewIndex uint64

	// pending is local to the replica whose checkpoint loop set it (see
	// expiry.go), which is the leader; no other replica reads it. It is the
	// token of the newest checkpoint the loop proposed for the lease that this
	// replica has not applied yet; 0 when there is none.
	pending uint64

	// revokec is closed when the lease leaves the table: its revoke applies,
	// or a Restore drops it. A renewal of an expired lease waits on it (see
	// ExpiredError). It is never replaced while the lease lives; Restore
	// carries it over to the lease's new record.
	revokec chan struct{}

	mu sync.RWMutex
}

// closeRevoked closes l.revokec. A nil channel, as on a lease built without
// newLease, has no waiter to wake.
func (l *lease) closeRevoked() {
	if l.revokec != nil {
		close(l.revokec)
	}
}

// effectiveTTLLocked returns the TTL, in seconds, a replica counts the lease's
// deadline from when it has nothing fresher: the checkpointed remaining TTL
// when there is one, the granted TTL otherwise. The CALLER MUST HOLD l.mu.
func (l *lease) effectiveTTLLocked() int64 {
	if l.checkpointTTL > 0 {
		return l.checkpointTTL
	}
	return l.ttl
}

// remainingTTLLocked returns the remaining TTL in seconds as of now.
//
// The CALLER MUST HOLD l.mu (read or write). The locking variants below do not
// wrap this one for a reason: toLease already holds l.mu when it needs the
// value, and sync.RWMutex is not reentrant -- a nested RLock deadlocks the
// moment a writer is queued between the two acquisitions. Splitting the
// locked/unlocked pair is what keeps that from being reintroduced by someone
// "adding the missing lock".
func (l *lease) remainingTTLLocked(now time.Time) int64 {
	remaining := l.expiry.Sub(now).Seconds()
	if remaining < 0 {
		return 0
	}
	return int64(remaining)
}

// isExpiredLocked reports whether the lease has passed its expiry time as of
// now. The CALLER MUST HOLD l.mu -- see remainingTTLLocked.
func (l *lease) isExpiredLocked(now time.Time) bool {
	return now.After(l.expiry)
}

// isExpired reports whether the lease has passed its expiry time as of now,
// taking l.mu itself. Callers that do not already hold l.mu use this one.
//
// Lock ordering across this package is lr.mu then l.mu, never the reverse, so
// calling this while holding lr.mu (as Leases and ExpiredLeaseIDs do) is safe.
func (l *lease) isExpired(now time.Time) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.isExpiredLocked(now)
}

// NoLease is the lease ID that means "no lease". Binding a key to it releases
// the key from whatever lease held it, which is what a write without a lease
// does to a key that had one.
const NoLease int64 = 0

// Lessor manages the lifecycle of leases in memory. Lease mutations
// (grant, revoke, keep-alive, key binding) are driven by the state machine
// after Raft consensus, ensuring all replicas maintain consistent state.
//
// Every mutation is also staged into the lease table (table.go) through the
// Writer the state machine passes in -- its apply batch -- so the table
// commits atomically with the data write that caused it, and Restore can
// rebuild this state after a restart or a snapshot recovery. The table is
// written FIRST: a mutation whose write fails leaves the in-memory state
// untouched.
//
// A Lessor serves exactly one state machine. Restore replaces the whole
// in-memory table with the one state machine's persisted copy, so sharing a
// Lessor across shards would let one shard's restore erase another's leases.
//
// The binding invariant: a key is bound to at most one lease, bindings[k]
// names it, and that lease's keys set holds k. Revoke therefore deletes only
// keys whose most recent write named the revoked lease -- a key re-put under
// another lease, or with none, has already left it.
//
// Expiry follows the etcd lessor. It is wall-clock state each replica keeps
// for itself, and it is renewed two ways. KeepAlive is the replicated one: the
// state machine applies a keep-alive command on every replica. RenewLocal is
// the fast one: the leader renews in its own memory without touching the log.
// What the other replicas learn instead is a checkpoint (ApplyCheckpoints): the
// leader's checkpoint loop (see RunExpiry) periodically records, through the
// log, how much TTL each long-lived lease has left. A replica that becomes
// leader (Promote) or restarts (Restore) counts each lease's deadline from its
// checkpoint, or from a full TTL when it has none -- never from less than the
// lease was last renewed for, because a renewal that would make a checkpoint
// understate the lease goes through the log, whose apply clears it, and a
// checkpoint sampled before such a renewal is rejected when it applies.
type Lessor struct {
	mu       sync.RWMutex
	leases   map[int64]*lease
	bindings map[string]int64 // key -> the lease it is bound to
	idSeq    int64            // highest ID granted; guarded by mu
	closed   atomic.Bool
	clock    clock.Clock
	leader   atomic.Pointer[func() bool] // live leadership test installed by RunExpiry

	// The on-demand checkpoint (RequestCheckpoint). checkpointing is set while
	// RunExpiry runs with a Checkpointer; checkpointWaiters, guarded by
	// waitersMu, are the requests the loop has not answered yet; checkpointPoke
	// wakes the loop.
	checkpointing     atomic.Bool
	waitersMu         sync.Mutex
	checkpointWaiters []chan error
	checkpointPoke    chan struct{}

	// checkpointToken numbers the checkpoint rounds; only the RunExpiry
	// goroutine touches it.
	checkpointToken uint64

	// applying is the Raft index of the entry the state machine is applying
	// (see Applying), and applied the highest index whose lease changes are
	// complete. The apply goroutine stores both; the checkpoint loop reads
	// applied to stamp a checkpoint with the index it was sampled at.
	applying atomic.Uint64
	applied  atomic.Uint64

	// recordFrom is the Raft index the group records replicated renewals
	// from: the index of the first checkpoint it applied, 0 until then. It is
	// replicated state, changed only by apply and Restore.
	recordFrom atomic.Uint64

	// demotec is open while this replica is the lease primary as the expiry
	// loop sees it: the loop opens it on a tick that will revoke expired
	// leases and closes it on one that will not, when it stops, and on Close.
	// nil means not primary. A renewal of an expired lease waits on it (see
	// ExpiredError), as etcd's lessor.Renew waits on its demotec.
	demotec atomic.Pointer[chan struct{}]
}

// closedChan stands in for demotec when there is none: a renewal waiting on
// it learns at once that this replica is not the primary.
var closedChan = func() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}()

// beginPrimary opens demotec unless it is open already. Only the expiry loop
// calls it.
func (lr *Lessor) beginPrimary() {
	if lr.demotec.Load() == nil {
		c := make(chan struct{})
		lr.demotec.CompareAndSwap(nil, &c)
	}
}

// endPrimary closes demotec, waking every renewal waiting on it, and leaves
// it nil.
func (lr *Lessor) endPrimary() {
	if c := lr.demotec.Swap(nil); c != nil {
		close(*c)
	}
}

// demoted returns the channel a renewal waits on to learn this replica
// stopped being the lease primary.
func (lr *Lessor) demoted() <-chan struct{} {
	if c := lr.demotec.Load(); c != nil {
		return *c
	}
	return closedChan
}

// Option configures a Lessor.
type Option func(*Lessor)

// WithClock sets the time source every expiry, remaining-TTL, renewal,
// promotion and sweep decision is made against. The default is clock.Real; a
// test passes a clocktest.Fake so a lease's lifetime is driven by Advance
// rather than waited out. A nil Clock leaves the default in place.
func WithClock(c clock.Clock) Option {
	return func(lr *Lessor) {
		if c != nil {
			lr.clock = c
		}
	}
}

// NewLessor creates a new Lessor.
func NewLessor(opts ...Option) *Lessor {
	lr := &Lessor{
		leases:         make(map[int64]*lease),
		bindings:       make(map[string]int64),
		clock:          clock.Real(),
		checkpointPoke: make(chan struct{}, 1),
	}
	for _, opt := range opts {
		opt(lr)
	}
	return lr
}

// now returns the lessor's current time.
func (lr *Lessor) now() time.Time { return lr.clock.Now() }

// newLease builds the in-memory state of lease id with its effective TTL --
// the checkpointed remaining TTL, or the full TTL when checkpointTTL is 0 --
// ahead of it, counted from now.
func newLease(id, ttl, checkpointTTL int64, now time.Time) *lease {
	l := &lease{
		id:            id,
		ttl:           ttl,
		checkpointTTL: checkpointTTL,
		keys:          make(map[string]struct{}),
		revokec:       make(chan struct{}),
	}
	l.expiry = now.Add(time.Duration(l.effectiveTTLLocked()) * time.Second)
	return l
}

// Grant registers a new lease with the given TTL in seconds under the next
// ID in sequence, and records the lease and the advanced sequence through w.
// The sequence is part of the table, so a replica that restarts or recovers
// from a snapshot allocates the same next ID as its peers.
func (lr *Lessor) Grant(w Writer, ttl int64) (*Lease, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}
	if ttl <= 0 {
		return nil, &InvalidTTLError{TTL: ttl}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()

	id := lr.idSeq + 1
	if _, ok := lr.leases[id]; ok {
		return nil, &ExistsError{ID: id}
	}

	if err := w.Set(leaseRecordKey(id), encodeInt64(ttl)); err != nil {
		return nil, &TableWriteError{Err: err}
	}
	if err := w.Set([]byte(sequenceKey), encodeInt64(id)); err != nil {
		return nil, &TableWriteError{Err: err}
	}

	lr.idSeq = id
	l := newLease(id, ttl, 0, lr.now())
	lr.leases[id] = l

	return lr.toLease(l), nil
}

// Revoke removes a lease, releases every key bound to it, and returns those
// keys in ascending order. The caller is responsible for deleting them; the
// order is fixed so every replica emits the resulting delete events in the
// same sequence.
func (lr *Lessor) Revoke(w Writer, id int64) ([][]byte, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()

	l, ok := lr.leases[id]
	if !ok {
		return nil, &NotFoundError{ID: id}
	}

	l.mu.RLock()
	keys := make([][]byte, 0, len(l.keys))
	for k := range l.keys {
		keys = append(keys, []byte(k))
	}
	checkpointed := l.checkpointTTL > 0
	renewed := l.renewIndex != 0
	l.mu.RUnlock()
	slices.SortFunc(keys, bytes.Compare)

	for _, k := range keys {
		if err := w.Delete(bindingKey(k)); err != nil {
			return nil, &TableWriteError{Err: err}
		}
	}
	if checkpointed {
		if err := w.Delete(checkpointRecordKey(id)); err != nil {
			return nil, &TableWriteError{Err: err}
		}
	}
	if renewed {
		if err := w.Delete(renewalRecordKey(id)); err != nil {
			return nil, &TableWriteError{Err: err}
		}
	}
	if err := w.Delete(leaseRecordKey(id)); err != nil {
		return nil, &TableWriteError{Err: err}
	}

	for _, k := range keys {
		delete(lr.bindings, string(k))
	}
	delete(lr.leases, id)
	l.closeRevoked()

	return keys, nil
}

// KeepAlive resets the expiry of a lease to now + TTL. Returns the
// updated lease info.
//
// The new expiry is wall-clock state local to each replica and is not
// written. What is written, through w, is the removal of the lease's
// checkpoint record when it has one: the renewal gives the lease a full TTL
// again, so a checkpoint recording less would make a new leader or a restarted
// replica count its deadline short. This is the clear etcd proposes when a
// lease with a checkpoint is renewed.
//
// Once the group has applied a checkpoint (see ApplyCheckpoints), the renewal
// also records the Raft index it applies at (see Applying) as the lease's
// renewal index, so a checkpoint sampled before it -- one that may record the
// remaining TTL from before the renewal -- is rejected wherever and whenever it
// applies, a replay after a restart included. Before the first checkpoint
// nothing more is written than before checkpoints existed.
func (lr *Lessor) KeepAlive(w Writer, id int64) (*Lease, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}

	lr.mu.RLock()
	l, ok := lr.leases[id]
	lr.mu.RUnlock()

	if !ok {
		return nil, &NotFoundError{ID: id}
	}

	l.mu.Lock()
	if l.checkpointTTL > 0 {
		if err := w.Delete(checkpointRecordKey(id)); err != nil {
			l.mu.Unlock()
			return nil, &TableWriteError{Err: err}
		}
	}
	index := lr.applying.Load()
	if lr.recordFrom.Load() != 0 {
		if err := w.Set(renewalRecordKey(id), encodeIndex(index)); err != nil {
			l.mu.Unlock()
			return nil, &TableWriteError{Err: err}
		}
		l.renewIndex = index
	}
	l.checkpointTTL = 0
	l.extendLocked(lr.now())
	l.mu.Unlock()
	lr.applied.Store(index)

	return lr.toLease(l), nil
}

// extendLocked moves the expiry to now + TTL unless it is already later. The
// CALLER MUST HOLD l.mu for writing.
func (l *lease) extendLocked(now time.Time) {
	if next := now.Add(time.Duration(l.ttl) * time.Second); next.After(l.expiry) {
		l.expiry = next
	}
}

// RenewLocal renews lease id in this replica's memory only, without a Raft
// proposal. It is the keep-alive fast path and is valid only on the replica
// that currently leads: a replica whose live leadership test (installed by
// RunExpiry) is absent or false answers *NotPrimaryError and changes nothing,
// and the caller renews through the replicated log with KeepAlive instead.
//
// A lease past its deadline answers *ExpiredError and changes nothing: the
// expiry loop revokes it, and a renewal that answered it lives on would be
// overtaken by that revoke. The caller waits on the error's Await for the
// outcome, outside the read RenewLocal runs in, since the revoke's apply waits
// behind that read.
//
// A lease with a checkpoint recorded, or one proposed and not yet applied
// here, answers *CheckpointedError and changes nothing, and the caller renews
// through the log in the same way. A renewal in memory would leave that
// checkpoint understating the lease on every other replica, where KeepAlive's
// apply clears it; this is the on-demand checkpoint etcd proposes on a
// renewal, and it costs one replicated renewal per lease per checkpoint.
//
// The caller is responsible for confirming leadership with the quorum before
// calling, as a linearizable read does (the service layer renews inside
// SyncRead's Lookup). The test here narrows the window in which a deposed
// leader that has not noticed yet can renew; it cannot close it alone.
func (lr *Lessor) RenewLocal(id int64) (*Lease, error) {
	if lr.closed.Load() {
		return nil, &LessorClosedError{}
	}
	if !lr.isPrimary() {
		return nil, &NotPrimaryError{}
	}

	lr.mu.RLock()
	l, ok := lr.leases[id]
	lr.mu.RUnlock()

	if !ok {
		return nil, &NotFoundError{ID: id}
	}

	now := lr.now()
	l.mu.Lock()
	if l.isExpiredLocked(now) {
		l.mu.Unlock()
		return nil, &ExpiredError{ID: id, revoked: l.revokec, demoted: lr.demoted()}
	}
	if l.checkpointTTL > 0 || l.pending != 0 {
		l.mu.Unlock()
		return nil, &CheckpointedError{ID: id}
	}
	l.extendLocked(now)
	l.mu.Unlock()

	return lr.toLease(l), nil
}

// isPrimary reports whether the live leadership test RunExpiry installed says
// this replica leads. It is false when no test is installed.
func (lr *Lessor) isPrimary() bool {
	isLeader := lr.leader.Load()
	return isLeader != nil && (*isLeader)()
}

// isFollower reports whether the live leadership test RunExpiry installed says
// this replica does not lead. It is false when no test is installed: a lessor
// that runs no expiry loop counts every lease from the log alone, so its
// deadlines are the only ones there are.
func (lr *Lessor) isFollower() bool {
	isLeader := lr.leader.Load()
	return isLeader != nil && !(*isLeader)()
}

// Promote is called when this replica becomes the leader. Expiry is
// wall-clock state each replica keeps for itself, and renewals the previous
// leader made in memory reached no other replica, so Promote gives every lease
// a deadline of at least now + grace + its effective TTL: the remaining TTL of
// its last checkpoint, or the full TTL when it has none. A lease whose owner
// kept renewing through the election is never revoked because of the failover
// -- every renewal since its checkpoint went through the log and cleared it --
// and a lease whose owner is gone is revoked at most grace plus the time
// between its last renewal and its last checkpoint later than it otherwise
// would have been. It never shortens a deadline.
//
// The leader-local pending mark of every lease is dropped: it describes
// checkpoints a previous term of this replica proposed, which either applied
// before this replica promoted -- RunExpiry promotes only once
// Checkpointer.AwaitApplied has returned -- or never will.
func (lr *Lessor) Promote(grace time.Duration) {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	now := lr.now()
	for _, l := range lr.leases {
		l.mu.Lock()
		if floor := now.Add(time.Duration(l.effectiveTTLLocked())*time.Second + grace); floor.After(l.expiry) {
			l.expiry = floor
		}
		l.pending = 0
		l.mu.Unlock()
	}
}

// Checkpoint is one lease's entry in a checkpoint: the remaining TTL, in
// seconds, the leader recorded for it. Zero clears the lease's checkpoint.
type Checkpoint struct {
	ID           int64
	RemainingTTL int64
}

// Applying tells the lessor the Raft index of the entry its state machine is
// about to apply. Every apply loop over a Lessor calls it before each entry: a
// replicated renewal records the index as the lease's renewal index, and a
// checkpoint judges itself against it. Every entry below it is applied, so it
// also moves the index the checkpoint loop stamps a checkpoint with (see
// sampledIndex) to the one before it.
func (lr *Lessor) Applying(index uint64) {
	lr.applying.Store(index)
	if index > 0 {
		lr.applied.Store(index - 1)
	}
}

// sampledIndex returns the Raft index a checkpoint sampled now is stamped
// with: the highest index whose lease changes are complete. A renewal or a
// checkpoint advances it to its own index once its change is made, so the
// CALLER reads it BEFORE sampling any lease: a renewal the sample reflects is
// then never at an index above the stamp.
func (lr *Lessor) sampledIndex() uint64 {
	return lr.applied.Load()
}

// ApplyCheckpoints records a replicated checkpoint on this replica, staging
// every change to the lease table through w before changing anything in
// memory. index is the Raft index the leader sampled the checkpoint at. Each
// entry's remaining TTL is clamped to the lease's granted TTL; a value of zero
// or less deletes the lease's checkpoint record. An entry naming a lease this
// replica does not hold -- revoked since the leader checkpointed it -- is
// skipped. No deadline changes: a checkpoint only says what a new leader or a
// restarted replica counts from.
//
// An entry a replicated renewal superseded is rejected: one whose index is
// below the lease's renewal index (see KeepAlive), because the TTL it records
// may be from before that renewal, or below the group's renewal marker, before
// which renewals were not recorded. The first checkpoint the group applies
// sets the marker to the index it applies at (see Applying), so every entry of
// that first checkpoint is rejected. The rule reads only replicated state and
// the command, so every replica, and every replay of the log, decides the
// same.
//
// token is the opaque round number the proposing leader carried. On that
// leader it settles the lease's pending mark, applied or rejected. Every other
// replica's marks are unset and nothing here reads them.
func (lr *Lessor) ApplyCheckpoints(w Writer, token, index uint64, cps []Checkpoint) error {
	if lr.closed.Load() {
		return &LessorClosedError{}
	}

	lr.mu.RLock()
	defer lr.mu.RUnlock()

	from := lr.recordFrom.Load()
	if from == 0 {
		from = lr.applying.Load()
		if from == 0 {
			return &ApplyIndexUnsetError{}
		}
		if err := w.Set([]byte(recordFromKey), encodeIndex(from)); err != nil {
			return &TableWriteError{Err: err}
		}
	}

	type update struct {
		l         *lease
		remaining int64
		rejected  bool
	}
	updates := make([]update, 0, len(cps))
	for _, cp := range cps {
		l, ok := lr.leases[cp.ID]
		if !ok {
			continue
		}
		l.mu.RLock()
		rejected := index < max(from, l.renewIndex)
		l.mu.RUnlock()
		remaining := min(max(cp.RemainingTTL, 0), l.ttl)
		if !rejected {
			var err error
			if remaining > 0 {
				err = w.Set(checkpointRecordKey(cp.ID), encodeInt64(remaining))
			} else {
				err = w.Delete(checkpointRecordKey(cp.ID))
			}
			if err != nil {
				return &TableWriteError{Err: err}
			}
		}
		updates = append(updates, update{l: l, remaining: remaining, rejected: rejected})
	}

	lr.recordFrom.Store(from)
	for _, u := range updates {
		l := u.l
		l.mu.Lock()
		if !u.rejected {
			l.checkpointTTL = u.remaining
		}
		if token != 0 && l.pending == token {
			l.pending = 0
		}
		l.mu.Unlock()
	}
	lr.applied.Store(lr.applying.Load())
	return nil
}

// Bind makes leaseID the one lease key belongs to, recording the change
// through w. It is called for EVERY write of key, with the lease that write
// named:
//
//   - a write naming a lease moves the key to it, out of any lease it was in;
//   - a write naming NoLease releases the key from any lease it was in.
//
// Either way, revoking or expiring a lease the key has left no longer deletes
// it. This is etcd's semantics: a key belongs to the lease of its latest
// value, not to every lease any of its values ever named.
//
// A leaseID the Lessor does not hold is a *NotFoundError and changes nothing.
// The state machine checks for that BEFORE writing the key (see Has), so the
// error here is a backstop rather than the refusal path.
func (lr *Lessor) Bind(w Writer, key []byte, leaseID int64) error {
	lr.mu.Lock()
	defer lr.mu.Unlock()

	k := string(key)
	prev, bound := lr.bindings[k]

	if leaseID == NoLease {
		if !bound {
			return nil
		}
		if err := w.Delete(bindingKey(key)); err != nil {
			return &TableWriteError{Err: err}
		}
		lr.releaseLocked(prev, k)
		delete(lr.bindings, k)
		return nil
	}

	l, ok := lr.leases[leaseID]
	if !ok {
		return &NotFoundError{ID: leaseID}
	}
	if bound && prev == leaseID {
		return nil
	}
	if err := w.Set(bindingKey(key), encodeInt64(leaseID)); err != nil {
		return &TableWriteError{Err: err}
	}
	if bound {
		lr.releaseLocked(prev, k)
	}
	l.mu.Lock()
	l.keys[k] = struct{}{}
	l.mu.Unlock()
	lr.bindings[k] = leaseID
	return nil
}

// Unbind releases key from whatever lease it is bound to. It is what a delete
// of key does to its lease: Bind with NoLease.
func (lr *Lessor) Unbind(w Writer, key []byte) error {
	return lr.Bind(w, key, NoLease)
}

// releaseLocked removes k from lease id's key set. The CALLER MUST HOLD lr.mu
// for writing.
func (lr *Lessor) releaseLocked(id int64, k string) {
	l, ok := lr.leases[id]
	if !ok {
		return
	}
	l.mu.Lock()
	delete(l.keys, k)
	l.mu.Unlock()
}

// Has reports whether the Lessor holds lease id. NoLease is always held: a
// write naming it attaches nothing and needs no lease to exist.
func (lr *Lessor) Has(id int64) bool {
	if id == NoLease {
		return true
	}
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	_, ok := lr.leases[id]
	return ok
}

// LeaseOf returns the lease key is bound to, or NoLease.
func (lr *Lessor) LeaseOf(key []byte) int64 {
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	return lr.bindings[string(key)]
}

// Restore replaces the Lessor's entire in-memory state with the lease table
// persisted in r. A state machine calls it when it opens and after it
// recovers from a snapshot -- the two moments its storage changes underneath
// the Lessor without passing through apply.
//
// Every restored lease starts its effective TTL from now: the remaining TTL
// of its checkpoint, or the full TTL when it has none. The deadline itself is
// wall-clock state that was never replicated, so after a gap of unknown length
// the only safe value is one no renewal can have exceeded: expiring a lease
// early deletes keys whose owner may still be renewing it, while expiring it
// late costs that much staleness. Every renewal since a lease's checkpoint
// went through the log and cleared it (see RenewLocal), and no checkpoint
// sampled before such a renewal was applied (see ApplyCheckpoints), so the
// checkpoint is such a value. etcd makes the same choice when a new leader takes over its
// lessor.
//
// A table that fails to read or validate leaves the Lessor unchanged.
func (lr *Lessor) Restore(r storage.Reader) error {
	t, err := readTable(r)
	if err != nil {
		return err
	}

	now := lr.now()
	leases := make(map[int64]*lease, len(t.ttls))
	for id, ttl := range t.ttls {
		leases[id] = newLease(id, ttl, t.checkpoints[id], now)
		leases[id].renewIndex = t.renewals[id]
	}
	for k, id := range t.bindings {
		leases[id].keys[k] = struct{}{}
	}

	lr.mu.Lock()
	defer lr.mu.Unlock()
	// A lease the snapshot keeps keeps its revokec, so a renewal waiting on
	// it still learns of its revoke; one the snapshot drops is revoked.
	for id, old := range lr.leases {
		if l, ok := leases[id]; ok {
			if old.revokec != nil {
				l.revokec = old.revokec
			}
			continue
		}
		old.closeRevoked()
	}
	lr.leases = leases
	lr.bindings = t.bindings
	lr.idSeq = t.seq
	lr.recordFrom.Store(t.recordFrom)
	return nil
}

// Lookup returns the lease info for the given ID. Returns nil if the
// lease does not exist.
func (lr *Lessor) Lookup(id int64) *Lease {
	lr.mu.RLock()
	l, ok := lr.leases[id]
	lr.mu.RUnlock()

	if !ok {
		return nil
	}

	return lr.toLease(l)
}

// LookupOrNotFound is Lookup with the miss turned into a *NotFoundError
// instead of a nil result. It exists because every LeaseTimeToLiveQuery
// dispatcher (pkg/engine's MVCCEngine and pkg/store's PebbleStateMachine --
// each backs a different raft.sm_type, and both hold a live Lessor in a
// clustered deployment) needs to answer a missing lease identically, and
// this is the single place that decision is made, so the two callers
// cannot drift apart on what "not found" means. Callers translate the
// returned *NotFoundError into their own layer's typed not-found error
// (e.g. service.LeaseNotFoundError) at the boundary, the same way
// Revoke/KeepAlive's callers already do.
func (lr *Lessor) LookupOrNotFound(id int64) (*Lease, error) {
	l := lr.Lookup(id)
	if l == nil {
		return nil, &NotFoundError{ID: id}
	}
	return l, nil
}

// TimeToLive answers the remaining-TTL query for lease id: *NotFoundError for
// a lease this replica does not hold, and the lease otherwise -- unless this
// replica's leadership test says it does not lead, when the answer is
// *StaleTTLError carrying its view of the lease. Renewals the leader made in
// memory reached no other replica, so a follower's deadline is behind the
// leader's and only the leader may report the remaining TTL, as only etcd's
// leader does. Whether the lease exists is every replica's to answer: grants
// and revocations are replicated.
func (lr *Lessor) TimeToLive(id int64) (*Lease, error) {
	l, err := lr.LookupOrNotFound(id)
	if err != nil {
		return nil, err
	}
	if lr.isFollower() {
		return nil, &StaleTTLError{Lease: l}
	}
	return l, nil
}

// Leases returns info for all active leases.
//
// Whether a lease has expired is the leader's call, and only the leader's
// deadlines are current: renewals made in the leader's memory reach no other
// replica. A replica whose leadership test (installed by RunExpiry) says it
// does not lead therefore lists every lease it holds -- each stays until the
// leader's revoke applies -- rather than hide one its stale deadline says has
// passed. Anywhere else, leases past their deadline are left out.
func (lr *Lessor) Leases() []*Lease {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	follower := lr.isFollower()
	now := lr.now()
	result := make([]*Lease, 0, len(lr.leases))
	for _, l := range lr.leases {
		if !follower && l.isExpired(now) {
			continue
		}
		result = append(result, lr.toLease(l))
	}
	return result
}

// ExpiredLeaseIDs returns the IDs of all expired leases.
func (lr *Lessor) ExpiredLeaseIDs() []int64 {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	now := lr.now()
	var expired []int64
	for id, l := range lr.leases {
		if l.isExpired(now) {
			expired = append(expired, id)
		}
	}
	return expired
}

// renewAfterHold gives every lease a full TTL from now, never shortening one.
// Unlike Promote it ignores checkpoints: after a span in which this node could
// neither take keep-alives nor revoke (WithExpiryHold), no holder is charged
// for that span.
func (lr *Lessor) renewAfterHold() {
	lr.mu.RLock()
	defer lr.mu.RUnlock()

	now := lr.clock.Now()
	for _, l := range lr.leases {
		l.mu.Lock()
		if renewed := now.Add(time.Duration(l.ttl) * time.Second); renewed.After(l.expiry) {
			l.expiry = renewed
		}
		l.mu.Unlock()
	}
}

// Count returns the number of active leases.
func (lr *Lessor) Count() int {
	lr.mu.RLock()
	defer lr.mu.RUnlock()
	return len(lr.leases)
}

// Close marks the lessor as closed. Subsequent operations will return
// LessorClosedError.
func (lr *Lessor) Close() {
	lr.closed.Store(true)
	lr.endPrimary()
}

// toLease converts an internal lease to the exported Lease type.
func (lr *Lessor) toLease(l *lease) *Lease {
	now := lr.now()
	l.mu.RLock()
	defer l.mu.RUnlock()

	keys := make([][]byte, 0, len(l.keys))
	for k := range l.keys {
		keys = append(keys, []byte(k))
	}

	return &Lease{
		ID:           l.id,
		TTL:          l.ttl,
		GrantedTTL:   l.ttl,
		RemainingTTL: l.remainingTTLLocked(now),
		Keys:         keys,
	}
}
