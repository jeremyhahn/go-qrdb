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

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSequenceService returns a SequenceService and the fakeCatalogStore
// backing it (see table_service_test.go for fakeCatalogStore).
func newTestSequenceService() (*SequenceService, *fakeCatalogStore) {
	store := newFakeCatalogStore()
	return NewSequenceService(store, store), store
}

func TestSequenceService_Allocate_StartsAtOne(t *testing.T) {
	svc, _ := newTestSequenceService()
	first, err := svc.Allocate(context.Background(), "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first)
}

func TestSequenceService_Allocate_Sequential(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	first, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	second, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	third, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)

	assert.Equal(t, []uint64{1, 2, 3}, []uint64{first, second, third})
}

func TestSequenceService_Allocate_ContiguousBlockForNGreaterThanOne(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	first, err := svc.Allocate(ctx, "db", "seq", 5)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first)

	peek, err := svc.Peek(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(5), peek, "a block of 5 must advance the counter by exactly 5")

	next, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(6), next, "the next allocation must not overlap the previous block")
}

func TestSequenceService_Allocate_DistinctSequencesIndependent(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	a, err := svc.Allocate(ctx, "db", "seq_a", 1)
	require.NoError(t, err)
	b, err := svc.Allocate(ctx, "db", "seq_b", 1)
	require.NoError(t, err)

	assert.Equal(t, uint64(1), a)
	assert.Equal(t, uint64(1), b)
}

func TestSequenceService_Allocate_InvalidCount(t *testing.T) {
	svc, _ := newTestSequenceService()
	_, err := svc.Allocate(context.Background(), "db", "seq", 0)
	var e *SequenceInvalidCountError
	require.ErrorAs(t, err, &e)
}

func TestSequenceService_Allocate_Validation(t *testing.T) {
	svc, _ := newTestSequenceService()

	_, err := svc.Allocate(context.Background(), "", "seq", 1)
	var emptyDB *SequenceEmptyDatabaseError
	assert.ErrorAs(t, err, &emptyDB)

	_, err = svc.Allocate(context.Background(), "db", "", 1)
	var emptyName *SequenceEmptyNameError
	assert.ErrorAs(t, err, &emptyName)
}

// TestSequenceService_Allocate_AbsentKeyCreateRace exercises "modRev == 0
// means absent" on a brand-new sequence: a forced conflict on the very
// first attempt simulates a concurrent allocator winning the race to
// create the counter key. The retry must re-read (observe the winner's
// committed value) and compute a fresh, non-overlapping block rather than
// retrying its stale candidate -- so no two Allocate calls ever return
// overlapping ranges even when the underlying key started out absent.
func TestSequenceService_Allocate_AbsentKeyCreateRace(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()

	store.forceConflicts = 1
	first, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first)

	second, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(2), second)
	assert.NotEqual(t, first, second, "a losing race on an absent key must never produce a duplicate allocation")
}

func TestSequenceService_Allocate_RetryThenSucceed(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()

	store.forceConflicts = 3
	first, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first)
	assert.Equal(t, 4, store.executeCallsSnapshot())
}

func TestSequenceService_Allocate_RetryExhausted(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()

	store.forceConflicts = 1000
	_, err := svc.Allocate(ctx, "db", "seq", 1)
	require.Error(t, err)

	var conflict *SequenceConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "db", conflict.Database)
	assert.Equal(t, "seq", conflict.Name)
	assert.Equal(t, catalogCASMaxAttempts, store.executeCallsSnapshot())
}

func TestSequenceService_Allocate_ReadError(t *testing.T) {
	svc, store := newTestSequenceService()
	store.injectGetRevisionErr = &ReadError{Err: errors.New("boom")}

	_, err := svc.Allocate(context.Background(), "db", "seq", 1)
	require.Error(t, err)
	assert.Equal(t, 0, store.executeCallsSnapshot(), "a non-conflict read error must not be retried")
}

func TestSequenceService_Allocate_ExecuteError(t *testing.T) {
	svc, store := newTestSequenceService()
	store.injectExecuteErr = errors.New("propose failed")

	_, err := svc.Allocate(context.Background(), "db", "seq", 1)
	require.Error(t, err)
	assert.Equal(t, 1, store.executeCallsSnapshot(), "a non-conflict execute error must not be retried")
}

func TestSequenceService_Peek_FreshSequenceIsZero(t *testing.T) {
	svc, _ := newTestSequenceService()
	v, err := svc.Peek(context.Background(), "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), v)
}

func TestSequenceService_Peek_ReflectsAllocations(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	_, err := svc.Allocate(ctx, "db", "seq", 3)
	require.NoError(t, err)

	v, err := svc.Peek(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(3), v)
}

func TestSequenceService_Peek_ReadError(t *testing.T) {
	svc, store := newTestSequenceService()
	store.injectGetRevisionErr = &ReadError{Err: errors.New("boom")}

	_, err := svc.Peek(context.Background(), "db", "seq")
	require.Error(t, err)
}

func TestSequenceService_Peek_Validation(t *testing.T) {
	svc, _ := newTestSequenceService()
	_, err := svc.Peek(context.Background(), "", "seq")
	var e *SequenceEmptyDatabaseError
	assert.ErrorAs(t, err, &e)
}

func TestSequenceService_Advance_RaisesFromZero(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	require.NoError(t, svc.Advance(ctx, "db", "seq", 100))
	v, err := svc.Peek(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(100), v)
}

func TestSequenceService_Advance_Monotonic(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	require.NoError(t, svc.Advance(ctx, "db", "seq", 10))
	require.NoError(t, svc.Advance(ctx, "db", "seq", 50))
	v, err := svc.Peek(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(50), v)
}

func TestSequenceService_Advance_NoOpWhenAlreadyAhead(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()

	require.NoError(t, svc.Advance(ctx, "db", "seq", 50))
	before := store.executeCallsSnapshot()

	require.NoError(t, svc.Advance(ctx, "db", "seq", 10))
	assert.Equal(t, before, store.executeCallsSnapshot(), "advancing to a value already reached must not propose anything")

	v, err := svc.Peek(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(50), v, "advancing backward must never lower the counter")
}

func TestSequenceService_Advance_ThenAllocateContinuesFromAdvancedValue(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	require.NoError(t, svc.Advance(ctx, "db", "seq", 100))
	next, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(101), next)
}

func TestSequenceService_Advance_Validation(t *testing.T) {
	svc, _ := newTestSequenceService()
	err := svc.Advance(context.Background(), "db", "", 1)
	var e *SequenceEmptyNameError
	assert.ErrorAs(t, err, &e)
}

func TestSequenceService_Advance_RetryExhausted(t *testing.T) {
	svc, store := newTestSequenceService()
	store.forceConflicts = 1000
	err := svc.Advance(context.Background(), "db", "seq", 5)
	var conflict *SequenceConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestSequenceService_Reset_ReturnsToZero(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	_, err := svc.Allocate(ctx, "db", "seq", 5)
	require.NoError(t, err)

	require.NoError(t, svc.Reset(ctx, "db", "seq"))
	v, err := svc.Peek(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), v)
}

func TestSequenceService_Reset_NoOpWhenAlreadyZero(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()

	before := store.executeCallsSnapshot()
	require.NoError(t, svc.Reset(ctx, "db", "seq"))
	assert.Equal(t, before, store.executeCallsSnapshot(), "resetting an already-zero (never allocated) sequence must not propose anything")
}

func TestSequenceService_Reset_ThenAllocateStartsOverAtOne(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	_, err := svc.Allocate(ctx, "db", "seq", 10)
	require.NoError(t, err)
	require.NoError(t, svc.Reset(ctx, "db", "seq"))

	first, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first)
}

func TestSequenceService_Reset_Validation(t *testing.T) {
	svc, _ := newTestSequenceService()
	err := svc.Reset(context.Background(), "", "seq")
	var e *SequenceEmptyDatabaseError
	assert.ErrorAs(t, err, &e)
}

func TestSequenceService_Reset_RetryExhausted(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()
	_, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)

	store.forceConflicts = 1000
	err = svc.Reset(ctx, "db", "seq")
	var conflict *SequenceConflictError
	require.ErrorAs(t, err, &conflict)
}

func TestEncodeDecodeSequenceCounter(t *testing.T) {
	assert.Equal(t, uint64(0), decodeSequenceCounter(nil))
	assert.Equal(t, uint64(0), decodeSequenceCounter([]byte{1, 2, 3}))
	assert.Equal(t, uint64(42), decodeSequenceCounter(encodeSequenceCounter(42)))
}
