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

// newTestPrivilegeService builds a PrivilegeService over the same
// self-contained in-memory catalog store the table-catalog tests use, so a
// write proposed through Execute is immediately visible to Get/GetRevision.
func newTestPrivilegeService() (*PrivilegeService, *fakeCatalogStore) {
	store := newFakeCatalogStore()
	return NewPrivilegeService(store, store, store), store
}

// grantSelect is the shorthand every test that only needs "some grant" uses.
func grantSelect(t *testing.T, svc *PrivilegeService, database, grantee, table string) {
	t.Helper()
	require.NoError(t, svc.Grant(context.Background(), &GrantPrivilegesRequest{
		Database: database,
		Grantee:  grantee,
		Grants:   []PrivilegeGrant{{Table: table, Privileges: PrivilegeSelect}},
	}))
}

// ---------------------------------------------------------------------------
// Grant
// ---------------------------------------------------------------------------

func TestPrivilegeService_GrantTableTier(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.Equal(t, PrivilegeSelect, set.Table("orders"))
	assert.Equal(t, PrivilegeMask(0), set.Table("customers"),
		"a table-tier grant must not leak to another table")
	assert.Equal(t, PrivilegeMask(0), set.OnDatabase)
}

func TestPrivilegeService_GrantDatabaseTierCoversEveryTable(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	require.NoError(t, svc.Grant(ctx, &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants:   []PrivilegeGrant{{Privileges: PrivilegeSelect | PrivilegeInsert}},
	}))

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.True(t, set.Table("orders").Has(PrivilegeSelect|PrivilegeInsert))
	assert.True(t, set.Table("a-table-that-does-not-exist-yet").Has(PrivilegeSelect),
		"the database tier must cover tables created later")
}

func TestPrivilegeService_GrantColumnTier(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	require.NoError(t, svc.Grant(ctx, &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants: []PrivilegeGrant{
			{Table: "orders", Column: "id", Privileges: PrivilegeSelect},
			{Table: "orders", Column: "total", Privileges: PrivilegeSelect},
		},
	}))

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.Equal(t, PrivilegeSelect, set.Column("orders", "id"))
	assert.Equal(t, PrivilegeMask(0), set.Column("orders", "secret"),
		"a column grant must not reach an ungranted column")
	assert.Equal(t, PrivilegeMask(0), set.Table("orders"),
		"a column grant must not become a table grant")
	assert.Equal(t, []string{"id", "total"}, set.Columns("orders"))
}

func TestPrivilegeService_GrantAccumulates(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")
	require.NoError(t, svc.Grant(ctx, &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants:   []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeInsert}},
	}))

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.True(t, set.Table("orders").Has(PrivilegeSelect|PrivilegeInsert))
}

// ---------------------------------------------------------------------------
// Revoke
// ---------------------------------------------------------------------------

func TestPrivilegeService_RevokeRemovesOnlyWhatIsNamed(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	require.NoError(t, svc.Grant(ctx, &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants:   []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeSelect | PrivilegeInsert}},
	}))
	require.NoError(t, svc.Revoke(ctx, &RevokePrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Revokes:  []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeInsert}},
	}))

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.Equal(t, PrivilegeSelect, set.Table("orders"))
}

func TestPrivilegeService_RevokeTableTierClearsColumnsBeneathIt(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	require.NoError(t, svc.Grant(ctx, &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants: []PrivilegeGrant{
			{Table: "orders", Privileges: PrivilegeSelect},
			{Table: "orders", Column: "id", Privileges: PrivilegeSelect},
		},
	}))
	require.NoError(t, svc.Revoke(ctx, &RevokePrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Revokes:  []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeSelect}},
	}))

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.Equal(t, PrivilegeMask(0), set.Column("orders", "id"),
		"revoking at the table tier must not leave a column-level grant behind")
	assert.True(t, set.IsEmpty())
}

func TestPrivilegeService_RevokeEverythingDeletesTheRecord(t *testing.T) {
	svc, store := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")
	require.NoError(t, svc.Revoke(ctx, &RevokePrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Revokes:  []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeSelect}},
	}))

	_, err := store.Get(ctx, privilegeGranteeKey("shop", "alice@%"))
	assert.True(t, isKeyNotFound(err), "an emptied record must be deleted, got %v", err)

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err, "an absent record must read back as an empty one")
	assert.True(t, set.IsEmpty())
}

// ---------------------------------------------------------------------------
// Activation latch
// ---------------------------------------------------------------------------

func TestPrivilegeService_ResolveUnlatchedDatabaseIsUnrestricted(t *testing.T) {
	svc, _ := newTestPrivilegeService()

	res, err := svc.Resolve(context.Background(), &ResolvePrivilegesRequest{
		Database: "shop", Grantee: "alice@%",
	})
	require.NoError(t, err)
	assert.False(t, res.Latched)
	assert.Zero(t, res.Generation)
	assert.Nil(t, res.Set)
}

func TestPrivilegeService_FirstGrantArmsTheLatch(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")

	res, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "bob@%"})
	require.NoError(t, err)
	assert.True(t, res.Latched, "the first mutation must arm the database")
	assert.Equal(t, uint64(1), res.Generation)
	assert.Zero(t, res.Epoch, "a grantee with no record has no epoch")
	require.NotNil(t, res.Set)
	assert.True(t, res.Set.IsEmpty(), "a grantee with no record resolves to an empty set")
}

func TestPrivilegeService_RevokeNeverClearsTheLatch(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")
	require.NoError(t, svc.Revoke(ctx, &RevokePrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Revokes:  []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeAll | PrivilegeGrantOption}},
	}))

	res, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "alice@%"})
	require.NoError(t, err)
	assert.True(t, res.Latched,
		"revoking every privilege must leave the database armed and empty, not unrestricted again")
	assert.Equal(t, uint64(2), res.Generation, "the latch value is monotonic across mutations")
}

func TestPrivilegeService_LatchGenerationIsMonotonic(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	var previous uint64
	for i, grantee := range []string{"alice@%", "bob@%", "alice@%", "carol@%"} {
		grantSelect(t, svc, "shop", grantee, "orders")

		res, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "nobody@%"})
		require.NoError(t, err)
		assert.Greater(t, res.Generation, previous, "generation must strictly increase (mutation %d)", i)
		previous = res.Generation
	}
}

// ---------------------------------------------------------------------------
// Resolve: staged reads and epoch invalidation
// ---------------------------------------------------------------------------

func TestPrivilegeService_ResolveReportsUnchangedAtSameGeneration(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")
	first, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "alice@%"})
	require.NoError(t, err)
	require.NotNil(t, first.Set)

	second, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{
		Database:        "shop",
		Grantee:         "alice@%",
		KnownGeneration: first.Generation,
		KnownEpoch:      first.Epoch,
	})
	require.NoError(t, err)
	assert.True(t, second.Unchanged)
	assert.Nil(t, second.Set, "an unchanged resolution must not re-read the record")
	assert.Equal(t, first.Generation, second.Generation)
}

func TestPrivilegeService_ResolveUnchangedWhenAnotherGranteeMoved(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")
	first, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "alice@%"})
	require.NoError(t, err)

	// A mutation to a DIFFERENT grantee advances the database generation but
	// not alice's own epoch, so her cached set is still current.
	grantSelect(t, svc, "shop", "bob@%", "orders")

	second, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{
		Database:        "shop",
		Grantee:         "alice@%",
		KnownGeneration: first.Generation,
		KnownEpoch:      first.Epoch,
	})
	require.NoError(t, err)
	assert.Greater(t, second.Generation, first.Generation)
	assert.Equal(t, first.Epoch, second.Epoch)
	assert.True(t, second.Unchanged)
	assert.Nil(t, second.Set)
}

func TestPrivilegeService_ResolveRereadsWhenGranteeEpochMoved(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "alice@%", "orders")
	first, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "alice@%"})
	require.NoError(t, err)

	require.NoError(t, svc.Revoke(ctx, &RevokePrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Revokes:  []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeSelect}},
	}))

	second, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{
		Database:        "shop",
		Grantee:         "alice@%",
		KnownGeneration: first.Generation,
		KnownEpoch:      first.Epoch,
	})
	require.NoError(t, err)
	assert.False(t, second.Unchanged, "the grantee's own epoch moved, so the record must be re-read")
	assert.Greater(t, second.Epoch, first.Epoch)
	require.NotNil(t, second.Set)
	assert.True(t, second.Set.IsEmpty())
}

// ---------------------------------------------------------------------------
// Compare-and-swap behaviour
// ---------------------------------------------------------------------------

func TestPrivilegeService_GrantRetriesLostCompareAndSwap(t *testing.T) {
	svc, store := newTestPrivilegeService()

	store.forceConflicts = 3
	grantSelect(t, svc, "shop", "alice@%", "orders")

	assert.Equal(t, 4, store.executeCalls,
		"three forced conflicts must cost three retries and one success")

	set, err := svc.Privileges(context.Background(), "shop", "alice@%")
	require.NoError(t, err)
	assert.Equal(t, PrivilegeSelect, set.Table("orders"))
}

func TestPrivilegeService_GrantGivesUpAfterRetryBudget(t *testing.T) {
	svc, store := newTestPrivilegeService()

	store.forceConflicts = catalogCASMaxAttempts + 1
	err := svc.Grant(context.Background(), &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants:   []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeSelect}},
	})

	var conflict *PrivilegeConflictError
	require.ErrorAs(t, err, &conflict)
	assert.Equal(t, "shop", conflict.Database)
	assert.Equal(t, "alice@%", conflict.Grantee)
	assert.Equal(t, catalogCASMaxAttempts, store.executeCalls)
}

func TestPrivilegeService_GrantComposesWithTheRaceWinner(t *testing.T) {
	svc, store := newTestPrivilegeService()
	ctx := context.Background()

	// Land a competing grant, then force one conflict so the retry re-reads
	// the record the competitor wrote rather than the one it started from.
	grantSelect(t, svc, "shop", "alice@%", "orders")
	store.forceConflicts = 1

	require.NoError(t, svc.Grant(ctx, &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants:   []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeInsert}},
	}))

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.True(t, set.Table("orders").Has(PrivilegeSelect|PrivilegeInsert),
		"a retried mutation must compose with the winner, not overwrite it")
}

func TestPrivilegeService_GrantPropagatesTransactionError(t *testing.T) {
	svc, store := newTestPrivilegeService()

	sentinel := errors.New("shard unreachable")
	store.injectExecuteErr = sentinel

	err := svc.Grant(context.Background(), &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants:   []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeSelect}},
	})
	assert.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// Enumeration
// ---------------------------------------------------------------------------

func TestPrivilegeService_GranteesExcludesCatalogOwnKeys(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	grantSelect(t, svc, "shop", "bob@%", "orders")
	grantSelect(t, svc, "shop", "alice@%", "orders")
	grantSelect(t, svc, "other", "carol@%", "orders")

	grantees, err := svc.Grantees(ctx, "shop")
	require.NoError(t, err)
	assert.Equal(t, []string{"alice@%", "bob@%"}, grantees,
		"the latch and per-grantee epoch keys must not be reported as grantees")
}

func TestPrivilegeService_GranteesEmptyDatabase(t *testing.T) {
	svc, _ := newTestPrivilegeService()

	_, err := svc.Grantees(context.Background(), "")
	var empty *PrivilegeEmptyDatabaseError
	assert.ErrorAs(t, err, &empty)
}

func TestPrivilegeService_GranteesPropagatesListError(t *testing.T) {
	svc, store := newTestPrivilegeService()

	sentinel := errors.New("list failed")
	store.injectListErr = sentinel

	_, err := svc.Grantees(context.Background(), "shop")
	var metadata *PrivilegeMetadataError
	require.ErrorAs(t, err, &metadata)
	assert.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func TestPrivilegeService_ValidationRejectsMalformedRequests(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	tests := []struct {
		name string
		req  *GrantPrivilegesRequest
		want any
	}{
		{
			name: "no database",
			req:  &GrantPrivilegesRequest{Grantee: "alice@%", Grants: []PrivilegeGrant{{}}},
			want: new(*PrivilegeEmptyDatabaseError),
		},
		{
			name: "no grantee",
			req:  &GrantPrivilegesRequest{Database: "shop", Grants: []PrivilegeGrant{{}}},
			want: new(*PrivilegeInvalidGranteeError),
		},
		{
			name: "grantee with a path separator",
			req:  &GrantPrivilegesRequest{Database: "shop", Grantee: "a/b", Grants: []PrivilegeGrant{{}}},
			want: new(*PrivilegeInvalidGranteeError),
		},
		{
			name: "grantee in the catalog's own namespace",
			req:  &GrantPrivilegesRequest{Database: "shop", Grantee: "__latch", Grants: []PrivilegeGrant{{}}},
			want: new(*PrivilegeInvalidGranteeError),
		},
		{
			name: "no grants",
			req:  &GrantPrivilegesRequest{Database: "shop", Grantee: "alice@%"},
			want: new(*PrivilegeNoGrantsError),
		},
		{
			name: "column without a table",
			req: &GrantPrivilegesRequest{Database: "shop", Grantee: "alice@%",
				Grants: []PrivilegeGrant{{Column: "id", Privileges: PrivilegeSelect}}},
			want: new(*PrivilegeColumnWithoutTableError),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := svc.Grant(ctx, tc.req)
			require.Error(t, err)
			assert.True(t, errors.As(err, tc.want), "unexpected error type: %T (%v)", err, err)

			// The same validation must guard REVOKE, so an invalid request
			// can never reach the store through the other door either.
			revokeErr := svc.Revoke(ctx, &RevokePrivilegesRequest{
				Database: tc.req.Database, Grantee: tc.req.Grantee, Revokes: tc.req.Grants,
			})
			assert.Error(t, revokeErr)
		})
	}
}

func TestPrivilegeService_NilRequests(t *testing.T) {
	svc, _ := newTestPrivilegeService()
	ctx := context.Background()

	var noGrants *PrivilegeNoGrantsError
	assert.ErrorAs(t, svc.Grant(ctx, nil), &noGrants)
	assert.ErrorAs(t, svc.Revoke(ctx, nil), &noGrants)

	var emptyDB *PrivilegeEmptyDatabaseError
	_, err := svc.Resolve(ctx, nil)
	assert.ErrorAs(t, err, &emptyDB)
}

func TestPrivilegeService_ResolveValidatesReference(t *testing.T) {
	svc, _ := newTestPrivilegeService()

	_, err := svc.Resolve(context.Background(), &ResolvePrivilegesRequest{Grantee: "alice@%"})
	var empty *PrivilegeEmptyDatabaseError
	assert.ErrorAs(t, err, &empty)

	_, err = svc.Privileges(context.Background(), "shop", "")
	var grantee *PrivilegeInvalidGranteeError
	assert.ErrorAs(t, err, &grantee)
}

// ---------------------------------------------------------------------------
// Storage layout and decoding
// ---------------------------------------------------------------------------

func TestPrivilegeService_KeysLiveUnderTheGuardedSystemNamespace(t *testing.T) {
	for _, key := range []string{
		privilegeLatchKey("shop"),
		privilegeGranteeKey("shop", "alice@%"),
		privilegeEpochKey("shop", "alice@%"),
		privilegeScanPrefix("shop"),
	} {
		assert.True(t, IsInternalKey(key),
			"%q must be excluded from user key enumeration", key)
	}

	assert.Equal(t, "__sys:privs/shop/__latch", privilegeLatchKey("shop"))
	assert.Equal(t, "__sys:privs/shop/alice@%", privilegeGranteeKey("shop", "alice@%"))
	assert.Equal(t, "__sys:privs/shop/alice@%/__epoch", privilegeEpochKey("shop", "alice@%"))
}

func TestPrivilegeService_RecordIdentityComesFromItsKey(t *testing.T) {
	svc, store := newTestPrivilegeService()
	ctx := context.Background()

	// A record whose stored body disagrees with its location must be read
	// back with the location winning; nothing else could be safe, since the
	// location is what the enforcement path looked the record up by.
	stored, err := store.PutIfAbsent(ctx, privilegeGranteeKey("shop", "alice@%"),
		[]byte(`{"grantee":"mallory@%","database":"other","on_database":["SELECT"]}`))
	require.NoError(t, err)
	require.True(t, stored)

	set, err := svc.Privileges(ctx, "shop", "alice@%")
	require.NoError(t, err)
	assert.Equal(t, "alice@%", set.Grantee)
	assert.Equal(t, "shop", set.Database)
	assert.Equal(t, PrivilegeSelect, set.OnDatabase)
}

func TestPrivilegeService_UndecodableRecordIsAnError(t *testing.T) {
	svc, store := newTestPrivilegeService()
	ctx := context.Background()

	stored, err := store.PutIfAbsent(ctx, privilegeGranteeKey("shop", "alice@%"), []byte("{not json"))
	require.NoError(t, err)
	require.True(t, stored)

	_, err = svc.Privileges(ctx, "shop", "alice@%")
	var metadata *PrivilegeMetadataError
	assert.ErrorAs(t, err, &metadata)
}

func TestPrivilegeService_RecordNamingAnUnknownPrivilegeIsAnError(t *testing.T) {
	svc, store := newTestPrivilegeService()
	ctx := context.Background()

	stored, err := store.PutIfAbsent(ctx, privilegeGranteeKey("shop", "alice@%"),
		[]byte(`{"on_database":["TELEPORT"]}`))
	require.NoError(t, err)
	require.True(t, stored)

	_, err = svc.Privileges(ctx, "shop", "alice@%")
	var unknown *PrivilegeUnknownNameError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, "TELEPORT", unknown.Name)
}

func TestPrivilegeService_ResolvePropagatesReadErrors(t *testing.T) {
	svc, store := newTestPrivilegeService()
	ctx := context.Background()

	sentinel := errors.New("read failed")
	store.injectGetErr = sentinel

	_, err := svc.Resolve(ctx, &ResolvePrivilegesRequest{Database: "shop", Grantee: "alice@%"})
	var metadata *PrivilegeMetadataError
	require.ErrorAs(t, err, &metadata)
	assert.ErrorIs(t, err, sentinel)
}

func TestPrivilegeService_MutatePropagatesRevisionReadErrors(t *testing.T) {
	svc, store := newTestPrivilegeService()

	sentinel := errors.New("revision read failed")
	store.injectGetRevisionErr = sentinel

	err := svc.Grant(context.Background(), &GrantPrivilegesRequest{
		Database: "shop",
		Grantee:  "alice@%",
		Grants:   []PrivilegeGrant{{Table: "orders", Privileges: PrivilegeSelect}},
	})
	var metadata *PrivilegeMetadataError
	require.ErrorAs(t, err, &metadata)
	assert.ErrorIs(t, err, sentinel)
}
