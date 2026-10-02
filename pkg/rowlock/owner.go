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

	// session holds the lease the owner's records are bound to.
	session *Session

	mu       sync.Mutex
	table    *Table
	lease    *sessionLease
	held     map[string]Mode
	released bool

	// records are the keys of every record the owner has written, bound to
	// its lease, and not withdrawn: what Release deletes while the lease is
	// live. A lease that has ended took them with it.
	records map[string]struct{}

	// unattached are the keys of records written after the owner's lease had
	// already ended, which the store keeps attached to nothing, and which an
	// explicit withdraw failed to delete: no revoke or expiry will ever
	// remove them, so Release deletes them whatever became of the lease.
	unattached map[string]struct{}
}

// newOwner returns an owner of session with a fresh, random identity.
func newOwner(session *Session) *Owner {
	var raw [ownerIDLen / 2]byte
	// crypto/rand.Read never returns an error on the platforms Go supports
	// (it panics internally if the system source fails), per its contract.
	_, _ = rand.Read(raw[:])
	return &Owner{
		id:         hex.EncodeToString(raw[:]),
		session:    session,
		held:       make(map[string]Mode),
		records:    make(map[string]struct{}),
		unattached: make(map[string]struct{}),
	}
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
	// An owner that holds nothing and has written nothing has nothing to
	// lose: if the session's lease turns out to have ended, it can start
	// over on a live one instead of failing its transaction.
	fresh := len(o.held) == 0 && len(o.records) == 0 && len(o.unattached) == 0
	o.mu.Unlock()

	if err := o.enqueue(ctx, t, pending, mode, fresh); err != nil {
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

// enqueue writes the owner's records for mode on pending, bound to the
// session's lease, and confirms the lease outlived the write (invariant 9).
//
// A lease found ended is a lost lock -- unless the owner is fresh (it held
// nothing and wrote nothing before this request), in which case the
// records it just wrote are withdrawn, the lease is retired, and the
// request is made once more on a live lease. An owner released while its
// records were being written deletes them and fails with
// *OwnerReleasedError: nothing will ever release them otherwise.
func (o *Owner) enqueue(ctx context.Context, t *Table, pending []string, mode Mode, fresh bool) error {
	for attempt := 0; ; attempt++ {
		l, err := o.ensureLease(ctx)
		if err != nil {
			return err
		}

		written, err := t.enqueue(ctx, pending, o.id, mode, l.id)
		if !o.wrote(written, mode) {
			o.deleteBestEffort(ctx, t, written, mode)
			return &OwnerReleasedError{}
		}
		if err == nil {
			err = t.leaseAlive(ctx, l.id)
		}
		if err == nil {
			return nil
		}
		var gone *service.LeaseNotFoundError
		if !errors.As(err, &gone) {
			o.withdrawBestEffort(ctx, t, written, mode)
			return err
		}
		if !fresh || attempt > 0 {
			return o.lostAtEnqueue(ctx, t, written, pending[0], mode)
		}
		if err := o.renewLease(ctx, t, l, written, mode); err != nil {
			return o.lostAtEnqueue(ctx, t, written, pending[0], mode)
		}
	}
}

// renewLease drops l, found ended under a fresh owner, after withdrawing
// the records just written to it (attached to nothing, so only an explicit
// delete removes them). The next ensureLease is granted a live lease.
func (o *Owner) renewLease(ctx context.Context, t *Table, l *sessionLease, written []string, mode Mode) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	if err := t.withdraw(rctx, written, o.id, mode); err != nil {
		return err
	}
	o.withdrawn(written, mode)
	l.ended.Store(true)
	l.lost.Store(true)
	_ = o.session.release(rctx, l, true)
	o.mu.Lock()
	o.lease = nil
	o.mu.Unlock()
	return nil
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

// ensureLease returns the owner's lease, binding it to its session's lease
// in the owner's store the first time. The session may have to grant one,
// which is done without holding the owner's lock, so a Release arriving
// meanwhile is not held up by it.
func (o *Owner) ensureLease(ctx context.Context) (*sessionLease, error) {
	o.mu.Lock()
	if o.lease != nil {
		l := o.lease
		o.mu.Unlock()
		return l, nil
	}
	t := o.table
	o.mu.Unlock()

	l, err := o.session.acquire(ctx, t)
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	if o.released {
		o.mu.Unlock()
		_ = o.session.release(ctx, l, true)
		return nil, &OwnerReleasedError{}
	}
	o.lease = l
	o.mu.Unlock()
	return l, nil
}

// wrote records that the owner's records for mode on keys were written,
// and reports false -- recording nothing -- when the owner was released
// meanwhile, in which case the caller must delete them itself.
func (o *Owner) wrote(keys []string, mode Mode) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.released {
		return false
	}
	for _, k := range keys {
		o.records[recordKey(k, o.id, mode)] = struct{}{}
	}
	return true
}

// withdrawn records that the owner's records for mode on keys were deleted.
func (o *Owner) withdrawn(keys []string, mode Mode) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, k := range keys {
		r := recordKey(k, o.id, mode)
		delete(o.records, r)
		delete(o.unattached, r)
	}
}

// Release releases every lock the owner holds by deleting its records, in
// one proposal per thousand. It is idempotent and safe to call from any
// goroutine.
//
// A lease known to have ended -- expired, swept, or successfully revoked by
// the session (its Close, or a failed release) -- took the owner's records
// with it, so none are deleted; only a record written after the lease
// ended, which is bound to nothing, still is. A lease the session retired
// but could not revoke still holds them, and they are deleted here. If the delete fails, the session revokes the lease
// instead, which removes the records just the same: a record is never left
// bound to a lease the session goes on renewing.
//
// The deletes run detached from ctx's cancellation, bounded by their own
// timeout, because the paths that release -- a dropped connection, a
// statement past its deadline -- are exactly the ones whose context is
// already done.
func (o *Owner) Release(ctx context.Context) error {
	t, l, attached, unattached, ok := o.markReleased()
	if !ok || l == nil {
		return nil
	}
	toDelete := unattached
	if !l.ended.Load() {
		toDelete = append(toDelete, attached...)
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	err := t.deleteRecords(rctx, toDelete)
	return o.session.release(rctx, l, err == nil)
}

// Scope returns the scope of the lock table the owner is bound to -- the
// store its records live in -- or "" before its first lock.
func (o *Owner) Scope() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.table == nil {
		return ""
	}
	return o.table.scope
}

// ReleaseWith releases the owner's locks inside the caller's own final
// proposal instead of in one of their own: the commit of the transaction
// the locks protected.
//
// final is handed the ops that delete every record the owner holds, which
// it must apply in BOTH branches of its compare-and-swap -- the locks end
// with the transaction whether its commit wins or loses -- and reports
// whether it proposed them. They are nil when the owner cannot fold its
// release (no lock, more records than one proposal carries, or a lease
// that has ended, whose records are already gone or bound to nothing), in
// which case final runs on its own and the owner is then released as
// Release does. An error from final means the proposal's outcome is
// unknown -- an infrastructure failure the caller may retry -- and the locks
// stay held.
func (o *Owner) ReleaseWith(ctx context.Context, final func(release []service.Op) (folded bool, err error)) error {
	ops := o.releaseOps()
	folded, err := final(ops)
	if err != nil {
		return err
	}
	if !folded || ops == nil {
		return o.Release(ctx)
	}
	_, l, _, _, ok := o.markReleased()
	if !ok || l == nil {
		return nil
	}
	return o.session.release(ctx, l, true)
}

// releaseOps returns the ops deleting every record the owner holds, or nil
// when its release cannot be folded into another proposal (see
// ReleaseWith).
func (o *Owner) releaseOps() []service.Op {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.released || o.lease == nil || o.lease.lost.Load() || len(o.unattached) > 0 ||
		len(o.records) == 0 || len(o.records) > maxRecordsPerProposal {
		return nil
	}
	ops := make([]service.Op, 0, len(o.records))
	for r := range o.records {
		ops = append(ops, service.Op{Type: service.OpDelete, Key: []byte(r)})
	}
	return ops
}

// markReleased marks the owner released, forgets its locks, and returns
// what was needed to release them: its table and lease and the records it
// wrote, bound to the lease and not. ok is false when it was already
// released.
func (o *Owner) markReleased() (t *Table, l *sessionLease, attached, unattached []string, ok bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.released {
		return nil, nil, nil, nil, false
	}
	o.released = true
	for k := range o.records {
		attached = append(attached, k)
	}
	for k := range o.unattached {
		unattached = append(unattached, k)
	}
	t, l = o.table, o.lease
	o.held = make(map[string]Mode)
	o.records = make(map[string]struct{})
	o.unattached = make(map[string]struct{})
	return t, l, attached, unattached, true
}

// lostAtEnqueue handles a lease found ended at enqueue: expired, or revoked
// by the leader's sweep or by the session, after the session's last renewal
// and before its keepalive noticed. Every record the owner held went with
// the lease, so the transaction has lost its locks; the request fails with
// *LockLostError and the owner is released, exactly as a waiter whose
// record disappeared.
//
// The state machine refuses an enqueue naming a lease it no longer holds,
// with *service.LeaseNotFoundError, and writes nothing. A request split over
// several proposals can still have written its earlier batches before the
// lease ended; those records are bound to the lease and go with its revoke,
// but they are deleted explicitly here too, so the request leaves nothing
// behind whatever the lease's state. One the withdraw fails to delete stays
// on the owner's list for Release.
// The lease is marked lost, so the session grants a live one for its next
// transaction.
func (o *Owner) lostAtEnqueue(ctx context.Context, t *Table, written []string, key string, mode Mode) error {
	o.mu.Lock()
	l := o.lease
	for _, k := range written {
		r := recordKey(k, o.id, mode)
		delete(o.records, r)
		o.unattached[r] = struct{}{}
	}
	o.mu.Unlock()
	if l != nil {
		// The store reported the lease gone: the records bound to it went
		// with it.
		l.ended.Store(true)
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

// deleteBestEffort deletes the owner's records for mode on keys, written
// for an owner that was released while they were being written, detached
// from ctx's cancellation for the same reason as Release.
func (o *Owner) deleteBestEffort(ctx context.Context, t *Table, keys []string, mode Mode) {
	if len(keys) == 0 {
		return
	}
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()
	_ = t.withdraw(rctx, keys, o.id, mode)
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
