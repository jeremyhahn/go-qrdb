// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package firstboot

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/ceremony"
	"github.com/jeremyhahn/go-qrdb/pkg/ceremony/ceremonytest"
	"github.com/jeremyhahn/go-qrdb/pkg/pin/pintest"
)

// errElecting stands in for the store's shard electing a leader: the
// "service: not leader for shard 1, leader unknown" go-qrdb#662 exited on.
var errElecting = errors.New("test: not leader, leader unknown")

// errStoreFault is a store error no election clears.
var errStoreFault = errors.New("test: store fault")

func electing(err error) bool { return errors.Is(err, errElecting) }

// electingStore is a PendingAdminCertStore whose writes fail while its shard
// has no leader. Reads answer from the replica's own state, as the composers'
// boot stores do, so they need no leader. failWrites is how many PutIfAbsent
// attempts fail with writeErr before one goes through; commitBeforeFail makes
// a failing attempt commit first, the timed-out write that landed anyway.
type electingStore struct {
	*memPendingAdminCertStore
	failWrites       int64
	writeErr         error
	commitBeforeFail bool
	attempts         atomic.Int64
}

func (s *electingStore) PutIfAbsent(ctx context.Context, key string, value []byte) (bool, error) {
	if s.attempts.Add(1) > s.failWrites {
		return s.memPendingAdminCertStore.PutIfAbsent(ctx, key, value)
	}
	if s.commitBeforeFail {
		if _, err := s.memPendingAdminCertStore.PutIfAbsent(ctx, key, value); err != nil {
			return false, err
		}
	}
	return false, s.writeErr
}

func newElectingStore(failWrites int64, writeErr error) *electingStore {
	return &electingStore{memPendingAdminCertStore: newMemPendingAdminCertStore(), failWrites: failWrites, writeErr: writeErr}
}

// issuedCeremonyCore returns a real ceremony core that has issued a bootstrap
// admin certificate, as the node that ran the ceremony holds one.
func issuedCeremonyCore(t *testing.T) (*ceremony.CeremonyService, []byte) {
	t.Helper()
	core := testCeremonyCore(t)
	ca, _ := ceremonytest.NewCA(t, "test-ca")
	core.SetPINBackend(pintest.NewBackend(t))
	_, err := core.Initialize(context.Background(), ca, &ceremony.InitializeOptions{
		SOPin: "test-so-pin", UserPin: "test-user-pin", AdminGenerate: true,
	})
	require.NoError(t, err)
	certPEM, _ := core.LastIssuedAdminCertPEM()
	require.NotEmpty(t, certPEM)
	return core, certPEM
}

// shortenSettleBudget sets pendingAdminCertSettleBudget for one test.
func shortenSettleBudget(t *testing.T, budget time.Duration) {
	t.Helper()
	prev := pendingAdminCertSettleBudget
	pendingAdminCertSettleBudget = budget
	t.Cleanup(func() { pendingAdminCertSettleBudget = prev })
}

// TestWriteIssuedAdminCertIfAny_PostRecycleBootProposesNothing reproduces
// go-qrdb#662: the node that ran the ceremony recycles in process, keeps its
// core and the certificate it issued, and boots again while the recycle's
// election has left the shard with no leader. The hand-off was consumed at
// the first boot, so the step must propose nothing, and must not fail on the
// write it used to retry against a leaderless shard.
func TestWriteIssuedAdminCertIfAny_PostRecycleBootProposesNothing(t *testing.T) {
	ctx := context.Background()
	core, _ := issuedCeremonyCore(t)
	store := newElectingStore(0, errElecting)

	// First boot: a leader answers, the record is written, then consumed.
	require.NoError(t, WriteIssuedAdminCertIfAny(ctx, core, store, electing))
	require.NoError(t, ClearPendingAdminCert(ctx, store))

	// The recycle's boot: same core, every write now meets an election.
	store.failWrites = 1 << 30
	store.attempts.Store(0)
	require.NoError(t, WriteIssuedAdminCertIfAny(ctx, core, store, nil))
	require.Zero(t, store.attempts.Load(), "a consumed hand-off must not be proposed again")

	got, err := store.Get(ctx, PendingAdminCertKey)
	require.NoError(t, err)
	require.Equal(t, pendingAdminCertConsumedMarker, got, "the consumed record must be left untouched")
}

// TestWriteIssuedAdminCertIfAny_PendingRecordProposesNothing covers the other
// recorded state: another node's credential is already pending, so this
// node's PutIfAbsent could only lose and is not proposed.
func TestWriteIssuedAdminCertIfAny_PendingRecordProposesNothing(t *testing.T) {
	ctx := context.Background()
	core, _ := issuedCeremonyCore(t)
	store := newElectingStore(1<<30, errElecting)
	require.NoError(t, store.memPendingAdminCertStore.Put(ctx, PendingAdminCertKey, []byte("another-node-won")))

	require.NoError(t, WriteIssuedAdminCertIfAny(ctx, core, store, nil))
	require.Zero(t, store.attempts.Load())
	got, err := store.Get(ctx, PendingAdminCertKey)
	require.NoError(t, err)
	require.Equal(t, []byte("another-node-won"), got)
}

// TestWriteIssuedAdminCertIfAny_WaitsOutAnElection covers a write that must
// run: it meets an election twice, then a leader, and lands.
func TestWriteIssuedAdminCertIfAny_WaitsOutAnElection(t *testing.T) {
	ctx := context.Background()
	core, certPEM := issuedCeremonyCore(t)
	store := newElectingStore(2, errElecting)

	require.NoError(t, WriteIssuedAdminCertIfAny(ctx, core, store, electing))
	require.Equal(t, int64(3), store.attempts.Load())
	got, err := store.Get(ctx, PendingAdminCertKey)
	require.NoError(t, err)
	require.Equal(t, certPEM, got)
}

// TestWriteIssuedAdminCertIfAny_TimedOutWriteThatCommittedIsNotAnError covers
// the attempt that timed out after committing: its retry loses the
// create-if-absent race to itself, which is success.
func TestWriteIssuedAdminCertIfAny_TimedOutWriteThatCommittedIsNotAnError(t *testing.T) {
	ctx := context.Background()
	core, certPEM := issuedCeremonyCore(t)
	store := newElectingStore(1, errElecting)
	store.commitBeforeFail = true

	require.NoError(t, WriteIssuedAdminCertIfAny(ctx, core, store, electing))
	require.Equal(t, int64(2), store.attempts.Load())
	got, err := store.Get(ctx, PendingAdminCertKey)
	require.NoError(t, err)
	require.Equal(t, certPEM, got)
}

// TestWriteIssuedAdminCertIfAny_ElectionPastTheBudgetStands covers an
// election that outlasts the budget: the write's typed error stands.
func TestWriteIssuedAdminCertIfAny_ElectionPastTheBudgetStands(t *testing.T) {
	shortenSettleBudget(t, 250*time.Millisecond)
	core, _ := issuedCeremonyCore(t)
	store := newElectingStore(1<<30, errElecting)

	err := WriteIssuedAdminCertIfAny(context.Background(), core, store, electing)
	var storeErr *PendingAdminCertStoreError
	require.ErrorAs(t, err, &storeErr)
	require.ErrorIs(t, err, errElecting)
	require.Greater(t, store.attempts.Load(), int64(1), "the election must have been waited out before the error stood")
}

// TestWriteIssuedAdminCertIfAny_NonTransientErrorIsFinal covers a store fault
// no election clears: it stands at once, without a retry.
func TestWriteIssuedAdminCertIfAny_NonTransientErrorIsFinal(t *testing.T) {
	core, _ := issuedCeremonyCore(t)
	store := newElectingStore(1<<30, errStoreFault)

	err := WriteIssuedAdminCertIfAny(context.Background(), core, store, electing)
	require.ErrorIs(t, err, errStoreFault)
	require.Equal(t, int64(1), store.attempts.Load())
}

// TestWriteIssuedAdminCertIfAny_NilClassifierRetriesNothing covers a caller
// that passes no classifier: the first error stands.
func TestWriteIssuedAdminCertIfAny_NilClassifierRetriesNothing(t *testing.T) {
	core, _ := issuedCeremonyCore(t)
	store := newElectingStore(1<<30, errElecting)

	err := WriteIssuedAdminCertIfAny(context.Background(), core, store, nil)
	require.ErrorIs(t, err, errElecting)
	require.Equal(t, int64(1), store.attempts.Load())
}

// TestWriteIssuedAdminCertIfAny_ContextEndsTheWait covers a boot whose
// context ends mid-election: the wait stops there, not at the budget.
func TestWriteIssuedAdminCertIfAny_ContextEndsTheWait(t *testing.T) {
	core, _ := issuedCeremonyCore(t)
	store := newElectingStore(1<<30, errElecting)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := WriteIssuedAdminCertIfAny(ctx, core, store, electing)
	require.ErrorIs(t, err, errElecting)
	require.Less(t, time.Since(start), pendingAdminCertSettleBudget)
}

// TestWriteIssuedAdminCertIfAny_NilStoreIsNoOp covers a composer with no
// store to record into: nothing is written and nothing fails.
func TestWriteIssuedAdminCertIfAny_NilStoreIsNoOp(t *testing.T) {
	core, _ := issuedCeremonyCore(t)
	require.NoError(t, WriteIssuedAdminCertIfAny(context.Background(), core, nil, electing))
}
