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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/lease"
	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/watch"
)

// injectedError is the failure the fault wrappers below inject.
type injectedError struct{ op string }

func (e *injectedError) Error() string { return "injected failure: " + e.op }

// faultyTxn fails Execute after the first `allow` calls succeed.
type faultyTxn struct {
	service.TransactionServicer
	allow atomic.Int64
}

func (f *faultyTxn) Execute(ctx context.Context, c []service.Compare, s, fo []service.Op) (*service.TransactionResponse, error) {
	if f.allow.Add(-1) < 0 {
		return nil, &injectedError{op: "execute"}
	}
	return f.TransactionServicer.Execute(ctx, c, s, fo)
}

// faultyMVCC fails Range after the first `allow` calls succeed.
type faultyMVCC struct {
	service.MVCCKVServicer
	allow atomic.Int64
}

func (f *faultyMVCC) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	if f.allow.Add(-1) < 0 {
		return nil, 0, 0, &injectedError{op: "range"}
	}
	return f.MVCCKVServicer.Range(ctx, start, end, opts...)
}

// detectionBarrier holds every deadlock-detection read -- a Range over the
// whole lock table -- until n of them have arrived, so the detectors of n
// requests all read a table in which every one of them has enqueued. Each
// read signals arrived before it blocks.
type detectionBarrier struct {
	service.MVCCKVServicer
	arrived chan struct{}
	release chan struct{}
	pending atomic.Int64
}

func newDetectionBarrier(inner service.MVCCKVServicer, n int64) *detectionBarrier {
	b := &detectionBarrier{
		MVCCKVServicer: inner,
		arrived:        make(chan struct{}, n),
		release:        make(chan struct{}),
	}
	b.pending.Store(n)
	return b
}

func (b *detectionBarrier) Range(ctx context.Context, start, end []byte, opts ...service.RangeOption) ([]*service.KeyValue, int64, int64, error) {
	if string(start) == Prefix {
		select {
		case b.arrived <- struct{}{}:
		default:
		}
		if b.pending.Add(-1) == 0 {
			close(b.release)
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, 0, 0, ctx.Err()
		}
	}
	return b.MVCCKVServicer.Range(ctx, start, end, opts...)
}

// failingGrant refuses every lease grant.
type failingGrant struct{ service.LeaseServicer }

func (failingGrant) Grant(context.Context, int64) (*lease.Lease, error) {
	return nil, &injectedError{op: "grant"}
}

// failingRevoke refuses every revoke.
type failingRevoke struct{ service.LeaseServicer }

func (failingRevoke) Revoke(context.Context, int64) error { return &injectedError{op: "revoke"} }

// failingWatch refuses every watch.
type failingWatch struct{ service.WatchServicer }

func (failingWatch) WatchPrefix(context.Context, string, ...watch.WatchOption) (watch.Watcher, error) {
	return nil, &injectedError{op: "watch"}
}

func TestAcquire_LeaseGrantFailure_IsReturned(t *testing.T) {
	s := newStack(t)
	d := s.deps
	d.Lease = failingGrant{s.deps.Lease}
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	var inj *injectedError
	require.ErrorAs(t, NewSession().NewOwner().Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0), &inj)
}

func TestAcquire_EnqueueFailure_IsReturned(t *testing.T) {
	s := newStack(t)
	d := s.deps
	ft := &faultyTxn{TransactionServicer: s.deps.Txn}
	d.Txn = ft
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })
	var inj *injectedError
	require.ErrorAs(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0), &inj)
	assert.Empty(t, queueOwners(t, s.table(t), keyA))
}

func TestAcquire_QueueReadFailure_WithdrawsTheRequest(t *testing.T) {
	s := newStack(t)
	d := s.deps
	fm := &faultyMVCC{MVCCKVServicer: s.deps.MVCC}
	d.MVCC = fm
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })
	var inj *injectedError
	require.ErrorAs(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0), &inj)
	assert.Empty(t, queueOwners(t, s.table(t), keyA), "a failed request must not linger in the queue")
}

func TestAcquire_DeadlockReadFailure_WithdrawsTheWaitingRecord(t *testing.T) {
	s := newStack(t)
	holder := NewSession().NewOwner()
	t.Cleanup(func() { _ = holder.Release(context.Background()) })
	require.NoError(t, holder.Acquire(context.Background(), s.table(t), []string{keyA}, ModeExclusive, 0))

	d := s.deps
	fm := &faultyMVCC{MVCCKVServicer: s.deps.MVCC}
	fm.allow.Store(1) // the queue read succeeds, the detector's read fails
	d.MVCC = fm
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	b := NewSession().NewOwner()
	t.Cleanup(func() { _ = b.Release(context.Background()) })
	var inj *injectedError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, time.Second), &inj)
	assert.Equal(t, []string{holder.ID() + "/x"}, queueOwners(t, s.table(t), keyA))
}

func TestAcquire_WatchFailure_IsReturnedAndWithdraws(t *testing.T) {
	s := newStack(t)
	holder := NewSession().NewOwner()
	t.Cleanup(func() { _ = holder.Release(context.Background()) })
	require.NoError(t, holder.Acquire(context.Background(), s.table(t), []string{keyA}, ModeExclusive, 0))

	d := s.deps
	d.Watch = failingWatch{s.deps.Watch}
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	b := NewSession().NewOwner()
	t.Cleanup(func() { _ = b.Release(context.Background()) })
	var inj *injectedError
	require.ErrorAs(t, b.Acquire(context.Background(), tbl, []string{keyA}, ModeExclusive, time.Second), &inj)
	assert.Equal(t, []string{holder.ID() + "/x"}, queueOwners(t, s.table(t), keyA))
}

func TestRelease_RevokeFailure_IsReturned(t *testing.T) {
	s := newStack(t)
	d := s.deps
	d.Lease = failingRevoke{s.deps.Lease}
	// Release revokes only when deleting the owner's records failed.
	d.Txn = failingDeletes{s.deps.Txn}
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	a := NewSession().NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	var inj *injectedError
	require.ErrorAs(t, a.Release(context.Background()), &inj)
	// The records stay bound to the lease, which expiry still removes.
	require.NoError(t, s.deps.Lease.Revoke(context.Background(), a.lease.id))
}

func TestRelease_AlreadyRevokedLease_IsNotAnError(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a := NewSession().NewOwner()
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	require.NoError(t, s.deps.Lease.Revoke(context.Background(), a.lease.id))
	require.NoError(t, a.Release(context.Background()))
}

func TestKeepalive_RevokedLease_MarksTheOwnerLost(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t, WithLeaseTTL(1))
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	require.NoError(t, s.deps.Lease.Revoke(context.Background(), a.lease.id))

	deadline := time.Now().Add(3 * time.Second)
	for !a.lease.lost.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	require.True(t, a.lease.lost.Load(), "a lease that can no longer be renewed must be noticed")
	var lost *LockLostError
	require.ErrorAs(t, a.Acquire(context.Background(), tbl, []string{keyB}, ModeShared, 0), &lost)
}

func TestAcquire_CorruptRecordInQueue_IsReported(t *testing.T) {
	s := newStack(t)
	_, err := s.deps.Txn.Execute(context.Background(), nil, []service.Op{{
		Type: service.OpPut, Key: []byte(queuePrefix(keyA) + "junk"), Value: []byte{1},
	}}, nil)
	require.NoError(t, err)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })
	var corrupt *CorruptRecordError
	require.ErrorAs(t, a.Acquire(context.Background(), s.table(t), []string{keyA}, ModeShared, 0), &corrupt)
}

// A lease that ends between the owner's last renewal and its next enqueue
// (expired, or revoked by the leader's sweep) is a lost lock: every record
// the transaction held went with the lease, so the request must not surface
// as an infrastructure error the caller could retry inside the same
// transaction.
func TestAcquire_EnqueueOnAnEndedLease_IsALostLock(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })
	require.NoError(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0))
	require.NoError(t, s.deps.Lease.Revoke(context.Background(), a.lease.id))
	require.False(t, a.lease.lost.Load(), "the keepalive has not noticed yet")

	err := a.Acquire(context.Background(), tbl, []string{keyB}, ModeExclusive, 0)
	var lost *LockLostError
	require.ErrorAs(t, err, &lost)
	assert.Equal(t, keyB, lost.Key)
	assert.False(t, a.Active(), "a lost owner is released")
	var released *OwnerReleasedError
	require.ErrorAs(t, a.Acquire(context.Background(), tbl, []string{keyC}, ModeShared, 0), &released)
	assert.Empty(t, queueOwners(t, tbl, keyB), "no record was left behind")
}

// TestTableEnqueue_OnAnEndedLease_IsRefusedAndWritesNothing pins the store
// behaviour Owner.enqueue's lost-lock and fresh-owner paths react to: the
// state machine refuses a write naming a lease it no longer holds, with
// *service.LeaseNotFoundError, and writes nothing -- so no record is ever
// left attached to nothing. A write on a live lease is attached to it and
// goes with its revoke.
func TestTableEnqueue_OnAnEndedLease_IsRefusedAndWritesNothing(t *testing.T) {
	s := newStack(t)
	tbl := s.table(t)
	ctx := context.Background()

	live, err := s.deps.Lease.Grant(ctx, 60)
	require.NoError(t, err)
	written, err := tbl.enqueue(ctx, []string{keyA}, "owner-live", ModeExclusive, live.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{keyA}, written)
	require.NoError(t, s.deps.Lease.Revoke(ctx, live.ID))
	assert.Empty(t, queueOwners(t, tbl, keyA), "a record on a live lease goes with its revoke")

	written, err = tbl.enqueue(ctx, []string{keyB}, "owner-ended", ModeExclusive, live.ID)
	var gone *service.LeaseNotFoundError
	require.ErrorAs(t, err, &gone, "a write naming an ended lease is refused")
	assert.Empty(t, written)
	assert.Empty(t, queueOwners(t, tbl, keyB), "the refused write left no record")
}

// failingTimeToLive refuses every lease lookup.
type failingTimeToLive struct{ service.LeaseServicer }

func (failingTimeToLive) TimeToLive(context.Context, int64) (*lease.Lease, error) {
	return nil, &injectedError{op: "ttl"}
}

func TestAcquire_LeaseCheckFailure_IsReturnedAndWithdraws(t *testing.T) {
	s := newStack(t)
	d := s.deps
	d.Lease = failingTimeToLive{LeaseServicer: s.deps.Lease}
	tbl, err := New(d, "acme/shop")
	require.NoError(t, err)
	a := NewSession().NewOwner()
	t.Cleanup(func() { _ = a.Release(context.Background()) })
	var inj *injectedError
	require.ErrorAs(t, a.Acquire(context.Background(), tbl, []string{keyA}, ModeShared, 0), &inj)
	assert.Empty(t, queueOwners(t, s.table(t), keyA), "an unconfirmed request is withdrawn")
	assert.True(t, a.Active(), "an infrastructure failure is not a lost lock")
}
