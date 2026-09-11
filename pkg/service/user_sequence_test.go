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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TableService's UserSequencer half -- the service side of PostgreSQL's
// CREATE SEQUENCE / nextval() / DROP SEQUENCE -- had no unit coverage. Its
// drop verb is the sibling of the collection leak go-qrdb#411 reports:
// DROP SEQUENCE is the only caller of ResetUserSequence, so routing it to
// SequenceServicer.Delete reclaims the counter key with no change to
// anything a SQL client can observe.

func TestTableService_NextUserSequenceValue_StartsAtOneAndAdvances(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	first, err := svc.NextUserSequenceValue(ctx, "shop", "order_id_seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), first, "sequences materialize on first use and start at 1")

	second, err := svc.NextUserSequenceValue(ctx, "shop", "order_id_seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), second)
}

// TestTableService_NextUserSequenceValue_NamespacedAwayFromInternalCounters
// keeps a client-named sequence off an internal allocator's key. A client
// naming its sequence "__tableid" must not be able to read, advance or drop
// the TableID counter.
func TestTableService_NextUserSequenceValue_NamespacedAwayFromInternalCounters(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()

	_, err := svc.NextUserSequenceValue(ctx, "shop", tableIDSequenceName)
	require.NoError(t, err)

	_, err = store.Get(ctx, tableSequenceKey("shop", UserSequenceName(tableIDSequenceName)))
	require.NoError(t, err, "the client sequence must live under the client namespace")

	_, err = store.Get(ctx, tableSequenceKey("shop", tableIDSequenceName))
	var notFound *KeyNotFoundError
	require.ErrorAs(t, err, &notFound, "naming a client sequence after an internal counter must not touch that counter's key")
}

// TestTableService_ResetUserSequence_ReclaimsTheCounterKey is the point of
// the change: DROP SEQUENCE used to write a zero counter, leaving a key per
// dropped sequence behind forever.
func TestTableService_ResetUserSequence_ReclaimsTheCounterKey(t *testing.T) {
	svc, store := newTestTableService()
	ctx := context.Background()
	key := tableSequenceKey("shop", UserSequenceName("order_id_seq"))

	_, err := svc.NextUserSequenceValue(ctx, "shop", "order_id_seq")
	require.NoError(t, err)
	_, err = store.Get(ctx, key)
	require.NoError(t, err)

	require.NoError(t, svc.ResetUserSequence(ctx, "shop", "order_id_seq"))

	_, err = store.Get(ctx, key)
	var notFound *KeyNotFoundError
	require.ErrorAs(t, err, &notFound, "DROP SEQUENCE must reclaim the counter key, not zero it")
}

// TestTableService_ResetUserSequence_ObservableEffectIsUnchanged is the
// control: under materialize-on-first-use, delete and reset leave the same
// state as far as a SQL client is concerned, which is why reclaiming the
// key is not a behaviour change.
func TestTableService_ResetUserSequence_ObservableEffectIsUnchanged(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		_, err := svc.NextUserSequenceValue(ctx, "shop", "order_id_seq")
		require.NoError(t, err)
	}

	require.NoError(t, svc.ResetUserSequence(ctx, "shop", "order_id_seq"))

	again, err := svc.NextUserSequenceValue(ctx, "shop", "order_id_seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), again, "a re-created sequence starts at 1, exactly as drop-and-recreate does in PostgreSQL")
}

func TestTableService_ResetUserSequence_UnknownSequenceIsSuccess(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	require.NoError(t, svc.ResetUserSequence(ctx, "shop", "never_created"),
		"DROP SEQUENCE on a name nothing ever allocated from is a no-op, not an error")
	require.NoError(t, svc.ResetUserSequence(ctx, "shop", "never_created"))
}

func TestTableService_ResetUserSequence_SiblingSequencesSurvive(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := svc.NextUserSequenceValue(ctx, "shop", "keep_seq")
		require.NoError(t, err)
	}
	_, err := svc.NextUserSequenceValue(ctx, "shop", "drop_seq")
	require.NoError(t, err)

	require.NoError(t, svc.ResetUserSequence(ctx, "shop", "drop_seq"))

	next, err := svc.NextUserSequenceValue(ctx, "shop", "keep_seq")
	require.NoError(t, err)
	assert.Equal(t, uint64(4), next, "dropping one client sequence must not disturb another")
}

func TestTableService_ResetUserSequence_Validation(t *testing.T) {
	svc, _ := newTestTableService()
	ctx := context.Background()

	var emptyDB *SequenceEmptyDatabaseError
	assert.ErrorAs(t, svc.ResetUserSequence(ctx, "", "order_id_seq"), &emptyDB)
}

func TestUserSequenceName_IsPrefixed(t *testing.T) {
	assert.Equal(t, "__usq:order_id_seq", UserSequenceName("order_id_seq"))
	assert.Equal(t, "__usq:", UserSequenceName(""))
}
