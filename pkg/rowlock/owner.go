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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// releaseTimeout bounds the record deletes and lease revokes Release issues.
// Release runs on paths whose own context may already be cancelled (a
// dropped connection, a statement that hit its deadline), so it detaches
// from the caller's cancellation and applies its own bound instead.
const releaseTimeout = 10 * time.Second

// Owner is one transaction's lock holder: a fresh identity, the session
// lease every one of its records is bound to, and the locks it holds.
//
// An owner is used by the one goroutine driving its transaction's current
// statement, but Release may also arrive from another (a connection closing
// under an in-flight statement), so its state is guarded by a mutex. Waiting
// happens with the mutex released.
type Owner struct {
	id string

	// session holds the lease the owner's records are bound to. private is
	// true for an owner made by NewOwner, whose session is its own: its
	// release revokes the lease rather than deleting its records one by one.
	session *Session
	private bool

	mu       sync.Mutex
	table    *Table
	lease    *sessionLease
	held     map[string]Mode
	released bool

	// records are the keys of every record the owner has written and not
	// withdrawn: what Release deletes.
	records map[string]struct{}
}

// NewOwner returns an owner with a fresh, random identity, no locks, and a
// lease of its own, granted at its first lock and revoked by Release. An
// owner of a long-lived client session is made with Session.NewOwner
// instead, which shares the session's lease.
func NewOwner() *Owner {
	o := newOwner()
	o.session = NewSession()
	o.private = true
	return o
}

// newOwner returns an owner with a fresh, random identity and no session.
func newOwner() *Owner {
	var raw [ownerIDLen / 2]byte
	// crypto/rand.Read never returns an error on the platforms Go supports
	// (it panics internally if the system source fails), per its contract.
	_, _ = rand.Read(raw[:])
	return &Owner{id: hex.EncodeToString(raw[:]), held: make(map[string]Mode), records: make(map[string]struct{})}
}

// ID returns the owner's identity as it appears in its lock records.
func (o *Owner) ID() string { return o.id }

// Holds reports whether the owner holds key in mode, or in a stronger one.
func (o *Owner) Holds(key string, mode Mode) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.held[key] >= mode
}

// Active reports whether the owner holds, or has ever requested, any lock
// that has not been released.
func (o *Owner) Active() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.table != nil && !o.released
}

// Acquire locks every key in keys in mode on t, waiting up to wait for each
// one that another owner holds incompatibly.
//
// A wait of zero is NOWAIT: a key that is not immediately available fails
// the request with *LockNotAvailableError. A request that times out fails
// with *LockWaitTimeoutError. Either way only this request fails: its
// waiting records are withdrawn, and locks granted before -- including keys
// of this same request that were granted -- stay held.
//
// A request that would wait and closes a cycle in the wait-for graph fails
// with *DeadlockError, and a request whose own record disappeared with
// *LockLostError; in both cases the owner is released before Acquire
// returns, because the transaction must be rolled back.
func (o *Owner) Acquire(ctx context.Context, t *Table, keys []string, mode Mode, wait time.Duration) error {
	if !mode.valid() {
		return &InvalidModeError{Mode: mode}
	}

	o.mu.Lock()
	if o.released {
		o.mu.Unlock()
		return &OwnerReleasedError{}
	}
	if o.table != nil && o.table.scope != t.scope {
		bound := o.table.scope
		o.mu.Unlock()
		return &ScopeMismatchError{Bound: bound, Requested: t.scope}
	}
	if o.lease != nil && o.lease.lost.Load() {
		o.mu.Unlock()
		o.releaseAfterLoss(ctx)
		return &LockLostError{}
	}
	pending := o.needed(keys, mode)
	if len(pending) == 0 {
		o.mu.Unlock()
		return nil
	}
	o.table = t
	if err := o.ensureLease(ctx); err != nil {
		o.mu.Unlock()
		return err
	}
	leaseID := o.lease.id
	o.mu.Unlock()

	written, err := t.enqueue(ctx, pending, o.id, mode, leaseID)
	o.wrote(written, mode)
	if err == nil {
		err = t.leaseAlive(ctx, leaseID)
	}
	if err != nil {
		var gone *service.LeaseNotFoundError
		if errors.As(err, &gone) {
			return o.lostAtEnqueue(ctx, t, written, pending[0], mode)
		}
		o.withdrawBestEffort(ctx, t, written, mode)
		return err
	}

	waiting, err := o.grantedNow(ctx, t, pending, mode)
	if err != nil {
		o.withdrawBestEffort(ctx, t, pending, mode)
		return err
	}
	if len(waiting) == 0 {
		requests(mode, outcomeGranted).Inc()
		return nil
	}

	if wait <= 0 {
		o.withdrawBestEffort(ctx, t, waiting, mode)
		requests(mode, outcomeNowait).Inc()
		return &LockNotAvailableError{Key: waiting[0], Mode: mode}
	}

	cycle, unsearched, err := t.deadlocked(ctx, o.id)
	if err != nil {
		o.withdrawBestEffort(ctx, t, waiting, mode)
		return err
	}
	if cycle || unsearched {
		_ = o.Release(ctx)
		outcome := outcomeDeadlock
		if unsearched {
			outcome = outcomeUnsearched
		}
		requests(mode, outcome).Inc()
		return &DeadlockError{Key: waiting[0], Mode: mode, Unsearched: unsearched}
	}

	return o.wait(ctx, t, waiting, mode, wait)
}

// needed returns the distinct keys of keys this owner does not already hold
// in mode or stronger. Callers hold o.mu.
func (o *Owner) needed(keys []string, mode Mode) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		if o.held[k] >= mode {
			continue
		}
		out = append(out, k)
	}
	return out
}

// grantedNow evaluates every key of a freshly enqueued request, records the
// granted ones as held, and returns the rest in request order.
func (o *Owner) grantedNow(ctx context.Context, t *Table, keys []string, mode Mode) ([]string, error) {
	var waiting []string
	for _, k := range keys {
		st, err := t.state(ctx, k, o.id, mode)
		if err != nil {
			return nil, err
		}
		switch st {
		case stateGranted:
			o.markHeld(k, mode)
		case stateWaiting:
			waiting = append(waiting, k)
		default:
			// Our own record is gone between writing it and reading it
			// back: the lease expired, or the namespace was reclaimed.
			_ = o.Release(ctx)
			requests(mode, outcomeLost).Inc()
			return nil, &LockLostError{Key: k}
		}
	}
	return waiting, nil
}

// wait blocks until every key in waiting is granted, the deadline passes,
// or ctx ends.
func (o *Owner) wait(ctx context.Context, t *Table, waiting []string, mode Mode, wait time.Duration) error {
	started := time.Now()
	waiters().Inc()
	defer waiters().Dec()

	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	for i, k := range waiting {
		err := o.waitOne(ctx, t, k, mode, deadline.C)
		if err == nil {
			o.markHeld(k, mode)
			continue
		}
		var lost *LockLostError
		if errors.As(err, &lost) {
			_ = o.Release(ctx)
			requests(mode, outcomeLost).Inc()
			return err
		}
		o.withdrawBestEffort(ctx, t, waiting[i:], mode)
		var timeout *LockWaitTimeoutError
		if errors.As(err, &timeout) {
			timeout.Mode, timeout.Wait = mode, wait
			requests(mode, outcomeTimeout).Inc()
			waitSeconds(mode).Observe(time.Since(started).Seconds())
			return timeout
		}
		requests(mode, outcomeCanceled).Inc()
		return err
	}
	requests(mode, outcomeWaited).Inc()
	waitSeconds(mode).Observe(time.Since(started).Seconds())
	return nil
}

// waitOne blocks until the owner's request for mode on key is granted. The
// watch is opened before the queue is re-read, so a deletion that lands
// between the two is delivered rather than missed; a watch the server ends
// (overflow, compaction) is reopened and the queue re-read.
func (o *Owner) waitOne(ctx context.Context, t *Table, key string, mode Mode, deadline <-chan time.Time) error {
	for {
		wctx, cancel := context.WithCancel(ctx)
		w, err := t.watchQueue(wctx, key)
		if err != nil {
			cancel()
			return err
		}
		granted, err := o.watchUntilGranted(ctx, t, w.Events(), key, mode, deadline)
		_ = w.Close()
		cancel()
		if err != nil || granted {
			return err
		}
	}
}

// watchUntilGranted re-reads key's queue now and after every deletion on it.
// It returns true once the request is granted, false (with no error) when
// the watch ended and must be reopened, or an error: a lost record, the
// deadline, or ctx ending.
func (o *Owner) watchUntilGranted(ctx context.Context, t *Table, events <-chan watch.WatchResponse, key string, mode Mode, deadline <-chan time.Time) (bool, error) {
	for {
		st, err := t.state(ctx, key, o.id, mode)
		if err != nil {
			return false, err
		}
		switch st {
		case stateGranted:
			return true, nil
		case stateMissing:
			return false, &LockLostError{Key: key}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline:
			return false, &LockWaitTimeoutError{Key: key}
		case resp, ok := <-events:
			if !ok || resp.Canceled || resp.CompactRevision != 0 {
				return false, nil
			}
		}
	}
}

// markHeld records key as held in mode, keeping a stronger mode it already
// held.
func (o *Owner) markHeld(key string, mode Mode) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.held[key] < mode {
		o.held[key] = mode
	}
}

// ensureLease binds the owner to its session's lease in the owner's store,
// once. Callers hold o.mu.
func (o *Owner) ensureLease(ctx context.Context) error {
	if o.lease != nil {
		return nil
	}
	l, err := o.session.acquire(ctx, o.table)
	if err != nil {
		return err
	}
	o.lease = l
	return nil
}

// wrote records that the owner's records for mode on keys were written.
func (o *Owner) wrote(keys []string, mode Mode) {
	if len(keys) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, k := range keys {
		o.records[recordKey(k, o.id, mode)] = struct{}{}
	}
}

// withdrawn records that the owner's records for mode on keys were deleted.
func (o *Owner) withdrawn(keys []string, mode Mode) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, k := range keys {
		delete(o.records, recordKey(k, o.id, mode))
	}
}

// Release releases every lock the owner holds. It is idempotent and safe to
// call from any goroutine.
//
// An owner sharing a session's lease deletes its own records -- in one
// proposal per thousand -- and leaves the lease to the session's next
// transaction. If the delete fails, the session's lease is revoked instead,
// which removes the records just the same; a record is never left bound to
// a lease the session goes on renewing. An owner made by NewOwner revokes
// its private lease, which deletes all of its records in one replicated
// operation.
//
// The deletes and the revoke run detached from ctx's cancellation, bounded
// by their own timeout, because the paths that release -- a dropped
// connection, a statement past its deadline -- are exactly the ones whose
// context is already done.
func (o *Owner) Release(ctx context.Context) error {
	o.mu.Lock()
	if o.released {
		o.mu.Unlock()
		return nil
	}
	o.released = true
	t, l := o.table, o.lease
	records := make([]string, 0, len(o.records))
	for k := range o.records {
		records = append(records, k)
	}
	o.held = make(map[string]Mode)
	o.records = make(map[string]struct{})
	o.mu.Unlock()

	if l == nil {
		return nil
	}
	if o.private {
		return o.session.Close(ctx)
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	err := t.deleteRecords(rctx, records)
	return o.session.release(rctx, l, err == nil)
}

// lostAtEnqueue handles a lease found ended at enqueue: expired, or revoked
// by the leader's sweep, after the session's last renewal and before its
// keepalive noticed. Every record the owner held went with the lease, so the
// transaction has lost its locks; the request fails with *LockLostError and
// the owner is released, exactly as a waiter whose record disappeared.
//
// The records this request just wrote are deleted explicitly. A write that
// carries a lease ID no longer in the lessor is stored but attached to
// nothing (the state machine does not refuse it), so no lease revoke or
// expiry would ever remove it: left behind, it would block its row forever.
// The lease is marked lost, so the session grants a live one for its next
// transaction.
func (o *Owner) lostAtEnqueue(ctx context.Context, t *Table, written []string, key string, mode Mode) error {
	if l := o.lease; l != nil {
		l.lost.Store(true)
	}
	if len(written) > 0 {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		if err := t.withdraw(rctx, written, o.id, mode); err == nil {
			o.withdrawn(written, mode)
		}
		cancel()
	}
	_ = o.Release(ctx)
	requests(mode, outcomeLost).Inc()
	return &LockLostError{Key: key}
}

// releaseAfterLoss releases an owner whose lease was found lost.
func (o *Owner) releaseAfterLoss(ctx context.Context) {
	_ = o.Release(ctx)
}

// withdrawBestEffort deletes the owner's records for mode on the keys of
// keys it does not hold in mode (a granted lock stays held until Release),
// detached from ctx's cancellation for the same reason as Release. A failure
// is not returned: the caller is already failing the request with the error
// that made it withdraw, and a record that could not be deleted stays on
// the owner's list, so Release deletes it -- or, failing that, revokes the
// lease it is bound to.
//
// What a failed withdraw costs: the owner's transaction stays open after a
// lock-wait timeout or NOWAIT (only the statement fails), so a waiting
// record that could not be deleted stays in its row's queue until that
// transaction ends. Until then it blocks every request queued behind it as
// a waiter would, and the deadlock detector sees the owner as waiting on
// that row, which can make a later request the victim of a cycle through a
// wait that no longer exists. Both end at the owner's COMMIT, ROLLBACK,
// session end or lease expiry -- the same bound as every other lock it
// holds. A failed withdraw requires the store to refuse a delete the same
// owner's enqueue just succeeded at, so it is the rare path, not the
// common one.
func (o *Owner) withdrawBestEffort(ctx context.Context, t *Table, keys []string, mode Mode) {
	o.mu.Lock()
	unheld := make([]string, 0, len(keys))
	for _, k := range keys {
		if o.held[k] < mode {
			unheld = append(unheld, k)
		}
	}
	o.mu.Unlock()
	if len(unheld) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := t.withdraw(rctx, unheld, o.id, mode); err == nil {
		o.withdrawn(unheld, mode)
	}
}
