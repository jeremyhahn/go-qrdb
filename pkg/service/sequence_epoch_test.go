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

// ---------------------------------------------------------------------------
// Epoch fencing (go-qrdb#547)
//
// SequenceServicer.Reset advances an epoch in the same compare-and-swap that
// zeroes the counter; Allocate reports the epoch a block was reserved under
// and Epoch reads the current one, so a caller caching a block can tell it
// has gone stale. See the SequenceServicer doc comment.
// ---------------------------------------------------------------------------

func TestSequenceService_Allocate_FreshSequenceIsEpochZero(t *testing.T) {
	svc, _ := newTestSequenceService()
	first, epoch, err := svc.Allocate(context.Background(), "db", "seq", 4)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first)
	assert.Equal(t, uint64(0), epoch)
}

func TestSequenceService_Reset_AdvancesEpoch(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	_, before, err := svc.Allocate(ctx, "db", "seq", 4)
	require.NoError(t, err)
	require.NoError(t, svc.Reset(ctx, "db", "seq"))

	epoch, err := svc.Epoch(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, before+1, epoch)

	first, after, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first, "the counter restarts")
	assert.Equal(t, epoch, after, "a post-reset block is reserved under the advanced epoch")
}

func TestSequenceService_Reset_EveryResetAdvancesEpoch(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	for want := uint64(1); want <= 3; want++ {
		_, _, err := svc.Allocate(ctx, "db", "seq", 2)
		require.NoError(t, err)
		require.NoError(t, svc.Reset(ctx, "db", "seq"))
		epoch, err := svc.Epoch(ctx, "db", "seq")
		require.NoError(t, err)
		assert.Equal(t, want, epoch)
	}
}

func TestSequenceService_Reset_ZeroCounterKeepsEpoch(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()

	_, _, err := svc.Allocate(ctx, "db", "seq", 2)
	require.NoError(t, err)
	require.NoError(t, svc.Reset(ctx, "db", "seq"))
	calls := store.executeCallsSnapshot()

	require.NoError(t, svc.Reset(ctx, "db", "seq"))
	assert.Equal(t, calls, store.executeCallsSnapshot(), "resetting a zero counter proposes nothing")
	epoch, err := svc.Epoch(ctx, "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), epoch, "nothing was reserved since the last reset, so there is nothing to fence")
}

func TestSequenceService_Advance_KeepsEpoch(t *testing.T) {
	svc, _ := newTestSequenceService()
	ctx := context.Background()

	_, _, err := svc.Allocate(ctx, "db", "seq", 2)
	require.NoError(t, err)
	require.NoError(t, svc.Reset(ctx, "db", "seq"))
	require.NoError(t, svc.Advance(ctx, "db", "seq", 100))

	first, epoch, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(101), first)
	assert.Equal(t, uint64(1), epoch, "Advance raises the counter without fencing anything")
}

func TestSequenceService_Epoch_FreshSequenceIsZero(t *testing.T) {
	svc, _ := newTestSequenceService()
	epoch, err := svc.Epoch(context.Background(), "db", "seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), epoch)
}

func TestSequenceService_Epoch_ReadError(t *testing.T) {
	svc, store := newTestSequenceService()
	store.injectGetRevisionErr = &ReadError{Err: errors.New("boom")}

	_, err := svc.Epoch(context.Background(), "db", "seq")
	var readErr *ReadError
	assert.ErrorAs(t, err, &readErr)
}

func TestSequenceService_Epoch_Validation(t *testing.T) {
	svc, _ := newTestSequenceService()

	_, err := svc.Epoch(context.Background(), "", "seq")
	var dbErr *SequenceEmptyDatabaseError
	assert.ErrorAs(t, err, &dbErr)

	_, err = svc.Epoch(context.Background(), "db", "")
	var nameErr *SequenceEmptyNameError
	assert.ErrorAs(t, err, &nameErr)
}

// TestSequenceService_CounterOnlyValueKeepsCounting pins the read of the
// 8-byte counter-only value earlier releases wrote: it is that counter at
// epoch 0, so allocation continues past it instead of restarting at 1 and
// handing out values -- table IDs among them -- a second time.
func TestSequenceService_CounterOnlyValueKeepsCounting(t *testing.T) {
	svc, store := newTestSequenceService()
	ctx := context.Background()
	store.put(tableSequenceKey("db", "seq"), encodeSequenceCounter(41))

	first, epoch, err := svc.Allocate(ctx, "db", "seq", 1)
	require.NoError(t, err)
	assert.Equal(t, uint64(42), first)
	assert.Equal(t, uint64(0), epoch)

	data, err := store.Get(ctx, tableSequenceKey("db", "seq"))
	require.NoError(t, err)
	assert.Len(t, data, sequenceStateSize, "the next write stores the full state")
}

func TestSequenceState_EncodeDecodeRoundTrip(t *testing.T) {
	for _, s := range []sequenceState{{}, {counter: 7}, {epoch: 3}, {counter: 1 << 60, epoch: 1<<63 + 5}} {
		assert.Equal(t, s, decodeSequenceState(s.encode()))
	}
}

func TestSequenceState_EncodeLeadsWithTheCounter(t *testing.T) {
	enc := sequenceState{counter: 9, epoch: 4}.encode()
	assert.Equal(t, uint64(9), decodeSequenceCounter(enc),
		"a reader of the counter-only format sees the counter, not the epoch")
}

func TestDecodeSequenceState_ShortValues(t *testing.T) {
	assert.Equal(t, sequenceState{}, decodeSequenceState(nil))
	assert.Equal(t, sequenceState{}, decodeSequenceState([]byte{1, 2, 3}))
	assert.Equal(t, sequenceState{counter: 12}, decodeSequenceState(append(encodeSequenceCounter(12), 0xff)))
}

func TestSequenceState_Reset(t *testing.T) {
	next, changed := sequenceState{counter: 5, epoch: 2}.reset()
	assert.True(t, changed)
	assert.Equal(t, sequenceState{counter: 0, epoch: 3}, next)

	next, changed = sequenceState{counter: 0, epoch: 2}.reset()
	assert.False(t, changed)
	assert.Equal(t, sequenceState{counter: 0, epoch: 2}, next)
}
