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
		_ sdk.AtomicKVStore = sdk.NewMemoryKVStore()
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

// reexportInvitee is a struct entity with a natural-key field, used by the
// go-qrdb#589 ID-collision tests below. It needs its own type because
// reexportMember's natural key (Team) is not unique per entity.
type reexportInvitee struct {
	ID    uint64 `json:"id"`
	Email string `json:"email"`
}

func (i *reexportInvitee) EntityID() uint64      { return i.ID }
func (i *reexportInvitee) SetEntityID(id uint64) { i.ID = id }

// collidingGenerator is a consumer-defined DeterministicIDGenerator that
// maps every value to the same ID, standing in for a real hash collision so
// the test is deterministic. Satisfying sdk.DeterministicIDGenerator from
// OUTSIDE the module is itself part of what this test proves: an embedder's
// own custom generator gets the same create-if-absent and GetByNaturalKey
// protection as the built-in ones.
type collidingGenerator struct{ id uint64 }

func (g *collidingGenerator) NextID(sdk.Entity) uint64 { return g.id }
func (g *collidingGenerator) FieldName() string        { return "Email" }
func (g *collidingGenerator) IDForValue(string) uint64 { return g.id }

var _ sdk.DeterministicIDGenerator = (*collidingGenerator)(nil)

// TestSDK_IDCollisionIsTestableByConsumers covers go-qrdb#589's write-path
// guard: Save routes a DeterministicIDGenerator's first-time ID assignment
// through an atomic create-if-absent write, and a consumer can name the
// collision refusal through the SDK alone.
func TestSDK_IDCollisionIsTestableByConsumers(t *testing.T) {
	var _ error = (*sdk.DAOIDCollisionError)(nil)

	ctx := context.Background()
	gen := &collidingGenerator{id: 55}
	d, err := sdk.NewDAO(sdk.NewMemoryKVStore(), "invitee",
		func() *reexportInvitee { return &reexportInvitee{} },
		sdk.WithIDGenerator(gen))
	require.NoError(t, err)

	require.NoError(t, d.Save(ctx, &reexportInvitee{Email: "a@example.com"}))

	err = d.Save(ctx, &reexportInvitee{Email: "b@example.com"})
	var collision *sdk.DAOIDCollisionError
	require.ErrorAs(t, err, &collision)
	require.Equal(t, uint64(55), collision.ID)
}

// TestSDK_GetByNaturalKeyIsTestableByConsumers covers go-qrdb#589's
// read-path guard: GetByNaturalKey derives the lookup ID and verifies it
// against the natural-key value in one call a consumer cannot split apart
// and only do half of.
func TestSDK_GetByNaturalKeyIsTestableByConsumers(t *testing.T) {
	var _ error = (*sdk.DAOUnsupportedIDGeneratorError)(nil)
	var _ error = (*sdk.NaturalKeyMismatchError)(nil)

	ctx := context.Background()
	gen := &collidingGenerator{id: 77}
	d, err := sdk.NewDAO(sdk.NewMemoryKVStore(), "invitee",
		func() *reexportInvitee { return &reexportInvitee{} },
		sdk.WithIDGenerator(gen))
	require.NoError(t, err)

	require.NoError(t, d.Save(ctx, &reexportInvitee{Email: "a@example.com"}))

	got, err := d.GetByNaturalKey(ctx, "a@example.com")
	require.NoError(t, err)
	require.Equal(t, "a@example.com", got.Email)

	// "b@example.com" forces the same ID but was never actually stored --
	// the write-side guard refused it -- so the read must report the
	// mismatch rather than hand back "a@example.com" unexamined.
	_, err = d.GetByNaturalKey(ctx, "b@example.com")
	var mismatch sdk.NaturalKeyMismatchError
	require.ErrorAs(t, err, &mismatch)

	// A DAO with no DeterministicIDGenerator configured refuses instead of
	// guessing at an ID.
	plain, err := sdk.NewDAO(sdk.NewMemoryKVStore(), "member", func() *reexportMember { return &reexportMember{} })
	require.NoError(t, err)
	_, err = plain.GetByNaturalKey(ctx, "alpha")
	var unsupported *sdk.DAOUnsupportedIDGeneratorError
	require.ErrorAs(t, err, &unsupported)
}

// reexportKey is a consumer's key entity: its key material is a
// DAOSecretBytes, held in protected memory.
type reexportKey struct {
	ID  uint64             `json:"id" msgpack:"id"`
	Key sdk.DAOSecretBytes `json:"key" msgpack:"key"`
}

// EntityID implements sdk.Entity.
func (k *reexportKey) EntityID() uint64 { return k.ID }

// SetEntityID implements sdk.Entity.
func (k *reexportKey) SetEntityID(id uint64) { k.ID = id }

// TestSDK_SecretBytesCrossesTheBoundary: a consumer can build a secret
// field, wipe it, and recognise the JSON codec's refusal of an entity
// holding one without importing pkg/dao.
func TestSDK_SecretBytesCrossesTheBoundary(t *testing.T) {
	src := []byte("consumer-key-material")
	s, err := sdk.NewDAOSecretBytes(src)
	require.NoError(t, err)
	require.Equal(t, make([]byte, len(src)), src, "the heap source must be wiped")
	require.Equal(t, len("consumer-key-material"), s.Len())
	require.Same(t, s.Buffer(), sdk.DAOSecretBytesFromBuffer(s.Buffer()).Buffer())
	s.Destroy()
	require.Zero(t, s.Len())

	_, err = sdk.NewDAO(sdk.NewMemoryKVStore(), "key",
		func() *reexportKey { return &reexportKey{} },
		sdk.DAOWithCodec(sdk.DAOCodecJSON),
	)
	var created sdk.DAOCodecCreationError
	var refusal sdk.DAOSecretCodecUnsupportedError
	require.ErrorAs(t, err, &created)
	require.ErrorAs(t, err, &refusal)

	var _ error = sdk.DAOSecretEncodingSizeError{}
	var _ error = sdk.DAOSecretFieldIndexedError{}
}
