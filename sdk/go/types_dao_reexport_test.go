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

package sdk_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	sdk "github.com/jeremyhahn/go-qrdb/sdk/go"
)

// This file is deliberately in the EXTERNAL test package (sdk_test) and
// imports only sdk/go. That is what makes it a consumer test rather than a
// self-check: it can only reach identifiers that actually cross the SDK
// boundary, so anything missing from the re-export fails to COMPILE here
// exactly as it would in go-quicbaas. Reaching for pkg/dao or pkg/kvstore to
// make it build would destroy the only property it has.

// reexportMember is the entity a consumer would define. The index tag is what
// routes a Where("Team = ?") query through the store's index.
type reexportMember struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Team string `json:"team" index:"true"`
}

// EntityID implements sdk.Entity.
func (m *reexportMember) EntityID() uint64 { return m.ID }

// SetEntityID implements sdk.Entity.
func (m *reexportMember) SetEntityID(id uint64) { m.ID = id }

// TestSDK_AtomicKVStoreAssertionIsExpressible covers issue #28's first item:
// a consumer must be able to make the fail-closed capability assertion at
// construction time without importing pkg/kvstore, which 00 L3 forbids.
// Before the re-export the assertion was simply unwritable from an SDK
// consumer, so quicbaas fell back to a private one-method structural
// interface — a workaround that cannot see the typed refusal.
func TestSDK_AtomicKVStoreAssertionIsExpressible(t *testing.T) {
	var store sdk.KVStore = sdk.NewMemoryKVStore()

	atomicStore, ok := store.(sdk.AtomicKVStore)
	require.True(t, ok,
		"the SDK memory store must satisfy sdk.AtomicKVStore; a consumer's "+
			"fail-closed guard is written against exactly this assertion")

	stored, err := atomicStore.PutIfAbsent(context.Background(), "member/claim", []byte(`{"team":"alpha"}`))
	require.NoError(t, err)
	require.True(t, stored)
}

// TestSDK_MemoryKVStoreClaimHasOneWinner is issue #29's stated verification:
// two concurrent callers, exactly one wins, without booting a node.
func TestSDK_MemoryKVStoreClaimHasOneWinner(t *testing.T) {
	store := sdk.NewMemoryKVStore()
	ctx := context.Background()

	var winners atomic.Int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			<-start
			if stored, err := store.PutIfAbsent(ctx, "member/claim", []byte(`{"team":"alpha"}`)); err == nil && stored {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, int64(1), winners.Load(),
		"exactly one concurrent claimant may win")
}

// TestSDK_DAOOverMemoryStoreAnswersIndexQueries is the end-to-end consumer
// story #29 exists for: build a DAO through the SDK over the exported memory
// store and get REAL index answers, with no node and no hand-written fake.
//
// The assertion is non-empty, for the same reason it is everywhere else in
// this change: an empty result would have passed against every store in the
// portfolio before it.
func TestSDK_DAOOverMemoryStoreAnswersIndexQueries(t *testing.T) {
	ctx := context.Background()
	store := sdk.NewMemoryKVStore()

	d, err := sdk.NewDAO(store, "member", func() *reexportMember { return &reexportMember{} })
	require.NoError(t, err)

	for _, m := range []*reexportMember{
		{Name: "ana", Team: "alpha"},
		{Name: "ben", Team: "alpha"},
		{Name: "cal", Team: "beta"},
	} {
		require.NoError(t, d.Save(ctx, m))
	}

	direct, err := d.QueryByIndex(ctx, "Team", "alpha")
	require.NoError(t, err)
	require.Len(t, direct, 2, "QueryByIndex must answer from the maintained index")

	planned, err := d.Find(ctx).Where("Team = ?", "alpha").Execute()
	require.NoError(t, err)
	require.Len(t, planned, 2, "the planned query must route through the same index")
}

// TestSDK_DAOOptionsAndCodecConstantsCrossTheBoundary covers issue #28's
// second and third items. Every identifier named here failed to compile from
// an SDK consumer before this change; the assertions are secondary to the
// fact that the file builds at all.
func TestSDK_DAOOptionsAndCodecConstantsCrossTheBoundary(t *testing.T) {
	ctx := context.Background()
	store := sdk.NewMemoryKVStore()

	d, err := sdk.NewDAO(store, "member",
		func() *reexportMember { return &reexportMember{} },
		sdk.DAOWithShard(1),
		sdk.DAOWithCodec(sdk.DAOCodecJSON),
		sdk.DAOWithTimeout(30*time.Second),
		sdk.DAOWithPartition(""),
	)
	require.NoError(t, err, "every re-exported DAO option must be accepted by sdk.NewDAO")

	require.NoError(t, d.Save(ctx, &reexportMember{Name: "ana", Team: "alpha"}))
	require.NoError(t, d.Save(ctx, &reexportMember{Name: "ben", Team: "beta"}))

	// The sort constants are usable as an OrderBy direction, which is the
	// only reason a consumer needs them exported.
	ascending, err := d.Find(ctx).OrderBy("Name", sdk.DAOSortAscending).Execute()
	require.NoError(t, err)
	require.Len(t, ascending, 2)
	require.Equal(t, "ana", ascending[0].Name)

	descending, err := d.Find(ctx).OrderBy("Name", sdk.DAOSortDescending).Execute()
	require.NoError(t, err)
	require.Len(t, descending, 2)
	require.Equal(t, "ben", descending[0].Name)

	// DAOWithDatabase changes the index namespace, so a DAO built with it
	// resolves a different index name.
	scoped, err := sdk.NewDAO(store, "member",
		func() *reexportMember { return &reexportMember{} },
		sdk.DAOWithDatabase("tenantdb"),
	)
	require.NoError(t, err)
	require.NotNil(t, scoped)
}

// TestSDK_IndexUnsupportedErrorIsTestableByConsumers pins the sentinel a
// consumer branches on. It is the contract quicbaas asserts against when it
// drops its interim tagless-entity posture, so it must be reachable and
// comparable with errors.Is from outside the module.
func TestSDK_IndexUnsupportedErrorIsTestableByConsumers(t *testing.T) {
	ctx := context.Background()
	store := sdk.NewMemoryKVStore()

	// An index nobody registered: the refusal, never an empty result.
	keys, err := store.QueryIndex(ctx, "member.Team", []byte("alpha"))
	require.ErrorIs(t, err, sdk.ErrKVIndexUnsupported)
	require.Empty(t, keys)

	// The atomic sentinel is reachable too; a consumer's fail-closed guard
	// returns it when a store lacks the capability.
	require.NotNil(t, sdk.ErrKVAtomicWriteUnsupported)
}

// TestSDK_EntityTraitTypesCrossTheBoundary covers the last of #28 item 3: the
// UnitOfWork / TransactionalDAO / Versioned / Timestamped types, of which
// only the ERRORS crossed before — so a consumer could test for a failure
// whose type it could not name.
func TestSDK_EntityTraitTypesCrossTheBoundary(t *testing.T) {
	var (
		_ *sdk.DAOUnitOfWork
		_ sdk.DAOTransactional[*reexportMember]
		_ sdk.DAOVersioned
		_ sdk.DAOTimestamped
		_ sdk.DAOCodecType     = sdk.DAOCodecMsgPack
		_ sdk.DAOCodecType     = sdk.DAOCodecCBOR
		_ sdk.DAOSortDirection = sdk.DAOSortAscending
	)
}

// nonAtomicKVStore is a consumer's KVStore with no atomic conditional write:
// embedding the interface leaves the method set exactly sdk.KVStore, so the
// AtomicKVStore capability is absent the way it is on a real plain backend.
type nonAtomicKVStore struct {
	sdk.KVStore
}

// TestSDK_SaveIfAbsentRefusalIsTestableByConsumers covers #568: a consumer
// tells "this backend has no atomic write" apart from a real SaveIfAbsent
// failure through the SDK alone, by the marker or by the typed refusal, and
// the refusal writes nothing.
func TestSDK_SaveIfAbsentRefusalIsTestableByConsumers(t *testing.T) {
	var (
		_ error = (*sdk.DAOAtomicWriteUnsupportedError)(nil)
	)
	ctx := context.Background()
	inner := sdk.NewMemoryKVStore()
	d, err := sdk.NewDAO(nonAtomicKVStore{inner}, "member", func() *reexportMember { return &reexportMember{} })
	require.NoError(t, err)

	stored, err := d.SaveIfAbsent(ctx, &reexportMember{ID: 9, Name: "ada", Team: "alpha"})
	require.False(t, stored)
	require.ErrorIs(t, err, sdk.ErrKVAtomicWriteUnsupported, "the refusal matches the canonical marker")

	var refusal *sdk.DAOAtomicWriteUnsupportedError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "member", refusal.EntityType)

	_, getErr := d.Get(ctx, 9)
	var notFound sdk.DAONotFoundError
	require.ErrorAs(t, getErr, &notFound, "the refused write must not reach the store")

	// The same DAO over an atomic store creates the entity: the refusal is
	// the capability, not the call.
	atomicDAO, err := sdk.NewDAO(sdk.NewMemoryKVStore(), "member", func() *reexportMember { return &reexportMember{} })
	require.NoError(t, err)
	stored, err = atomicDAO.SaveIfAbsent(ctx, &reexportMember{ID: 9, Name: "ada", Team: "alpha"})
	require.NoError(t, err)
	require.True(t, stored)
}

// TestSDK_UnitOfWorkRefusalIsTestableByConsumers covers go-qrdb#589's
// coordinator ruling: DAOUnitOfWork.Commit no longer degrades to a
// sequential, non-atomic apply-and-compensate when the store cannot do
// atomic multi-key batches -- it refuses, and a consumer must be able to
// test for and name that refusal through the SDK alone, without reaching
// for pkg/kvstore/kvstoretest. sdk.NewMemoryKVStore() implements
// sdk.AtomicKVStore (single-key) but not the multi-key batch capability
// Commit requires, so it is this refusal's own fixture.
func TestSDK_UnitOfWorkRefusalIsTestableByConsumers(t *testing.T) {
	var _ error = (*sdk.DAOAtomicBatchUnsupportedError)(nil)

	ctx := context.Background()
	store := sdk.NewMemoryKVStore()
	d, err := sdk.NewDAO(store, "member", func() *reexportMember { return &reexportMember{} })
	require.NoError(t, err)

	uow := sdk.NewDAOUnitOfWork(store)
	require.NoError(t, sdk.RegisterDAOSave(uow, d, &reexportMember{ID: 1, Name: "ada", Team: "alpha"}))

	err = uow.Commit(ctx)
	var refusal *sdk.DAOAtomicBatchUnsupportedError
	require.ErrorAs(t, err, &refusal)
	require.ErrorIs(t, err, sdk.ErrKVAtomicWriteUnsupported, "the refusal matches the canonical marker")

	_, getErr := d.Get(ctx, 1)
	var notFound sdk.DAONotFoundError
	require.ErrorAs(t, getErr, &notFound, "a refused Commit must not reach the store")
}
