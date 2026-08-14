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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrivilegeMask_HasAndHasAny(t *testing.T) {
	m := PrivilegeSelect | PrivilegeInsert

	assert.True(t, m.Has(PrivilegeSelect))
	assert.True(t, m.Has(PrivilegeSelect|PrivilegeInsert))
	assert.False(t, m.Has(PrivilegeSelect|PrivilegeDelete),
		"Has must require every requested privilege, not any of them")
	assert.True(t, m.HasAny(PrivilegeSelect|PrivilegeDelete))
	assert.False(t, m.HasAny(PrivilegeDelete))
	assert.True(t, m.Has(0), "an empty requirement is always satisfied")
}

func TestPrivilegeMask_NamesFollowMySQLOrder(t *testing.T) {
	m := PrivilegeDelete | PrivilegeSelect | PrivilegeGrantOption

	assert.Equal(t, []string{"SELECT", "DELETE", "GRANT OPTION"}, m.Names())
	assert.Empty(t, PrivilegeMask(0).Names())
}

func TestPrivilegeMask_String(t *testing.T) {
	tests := []struct {
		name string
		mask PrivilegeMask
		want string
	}{
		{"none is USAGE", 0, "USAGE"},
		{"grant option alone is still USAGE", PrivilegeGrantOption, "USAGE"},
		{"the full set collapses", PrivilegeAll, PrivilegeAllName},
		{"the full set plus grant option still collapses", PrivilegeAll | PrivilegeGrantOption, PrivilegeAllName},
		{"a partial set lists its names", PrivilegeSelect | PrivilegeUpdate, "SELECT, UPDATE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.mask.String())
		})
	}
}

func TestPrivilegeAll_ExcludesGrantOption(t *testing.T) {
	assert.False(t, PrivilegeAll.HasAny(PrivilegeGrantOption),
		"GRANT ALL PRIVILEGES must not confer GRANT OPTION, matching MySQL")
}

func TestPrivilegeByName(t *testing.T) {
	tests := []struct {
		in     string
		want   PrivilegeMask
		wantOk bool
	}{
		{"SELECT", PrivilegeSelect, true},
		{"select", PrivilegeSelect, true},
		{"  Grant Option  ", PrivilegeGrantOption, true},
		{PrivilegeAllName, PrivilegeAll, true},
		{"SUPER", 0, false},
		{"", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := PrivilegeByName(tc.in)
			assert.Equal(t, tc.wantOk, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestPrivilegeMask_JSONRoundTrip(t *testing.T) {
	for _, mask := range []PrivilegeMask{
		0,
		PrivilegeSelect,
		PrivilegeAll,
		PrivilegeAll | PrivilegeGrantOption,
	} {
		data, err := json.Marshal(mask)
		require.NoError(t, err)

		var back PrivilegeMask
		require.NoError(t, json.Unmarshal(data, &back))
		assert.Equal(t, mask, back)
	}
}

func TestPrivilegeMask_JSONIsNamesNotAnInteger(t *testing.T) {
	data, err := json.Marshal(PrivilegeSelect | PrivilegeInsert)
	require.NoError(t, err)
	assert.JSONEq(t, `["SELECT","INSERT"]`, string(data),
		"a stored record must be readable, and must not freeze the bit assignments")
}

func TestPrivilegeMask_UnmarshalRejectsUnknownName(t *testing.T) {
	var mask PrivilegeMask
	err := json.Unmarshal([]byte(`["SELECT","TELEPORT"]`), &mask)

	var unknown *PrivilegeUnknownNameError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, "TELEPORT", unknown.Name)
}

func TestPrivilegeMask_UnmarshalRejectsNonArray(t *testing.T) {
	var mask PrivilegeMask
	assert.Error(t, json.Unmarshal([]byte(`3`), &mask))
}

// ---------------------------------------------------------------------------
// PrivilegeSet tiers
// ---------------------------------------------------------------------------

func TestPrivilegeSet_TierResolution(t *testing.T) {
	set := newPrivilegeSet("shop", "alice@%")
	set.grant(PrivilegeGrant{Privileges: PrivilegeSelect})
	set.grant(PrivilegeGrant{Table: "orders", Privileges: PrivilegeInsert})
	set.grant(PrivilegeGrant{Table: "orders", Column: "total", Privileges: PrivilegeUpdate})

	assert.Equal(t, PrivilegeSelect|PrivilegeInsert, set.Table("orders"),
		"the table tier must include the database tier above it")
	assert.Equal(t, PrivilegeSelect, set.Table("customers"))
	assert.Equal(t, PrivilegeSelect|PrivilegeInsert|PrivilegeUpdate, set.Column("orders", "total"),
		"the column tier must include both tiers above it")
	assert.Equal(t, PrivilegeSelect|PrivilegeInsert, set.Column("orders", "id"))
}

func TestPrivilegeSet_AnyAndAnyOnTable(t *testing.T) {
	set := newPrivilegeSet("shop", "alice@%")
	set.grant(PrivilegeGrant{Table: "orders", Column: "total", Privileges: PrivilegeUpdate})

	assert.Equal(t, PrivilegeUpdate, set.Any(),
		"a column-only grant still makes the database reachable")
	assert.Equal(t, PrivilegeUpdate, set.AnyOnTable("orders"))
	assert.Equal(t, PrivilegeMask(0), set.AnyOnTable("customers"))
}

func TestPrivilegeSet_NilReceiverIsAnEmptySet(t *testing.T) {
	var set *PrivilegeSet

	assert.Equal(t, PrivilegeMask(0), set.Table("orders"))
	assert.Equal(t, PrivilegeMask(0), set.Column("orders", "id"))
	assert.Equal(t, PrivilegeMask(0), set.AnyOnTable("orders"))
	assert.Equal(t, PrivilegeMask(0), set.Any())
	assert.Nil(t, set.Tables())
	assert.Nil(t, set.Columns("orders"))
}

func TestPrivilegeSet_TablesAndColumnsAreSorted(t *testing.T) {
	set := newPrivilegeSet("shop", "alice@%")
	set.grant(PrivilegeGrant{Table: "orders", Privileges: PrivilegeSelect})
	set.grant(PrivilegeGrant{Table: "customers", Column: "zip", Privileges: PrivilegeSelect})
	set.grant(PrivilegeGrant{Table: "customers", Column: "age", Privileges: PrivilegeSelect})

	assert.Equal(t, []string{"customers", "orders"}, set.Tables())
	assert.Equal(t, []string{"age", "zip"}, set.Columns("customers"))
}

func TestPrivilegeSet_RevokePrunesEmptyTiers(t *testing.T) {
	set := newPrivilegeSet("shop", "alice@%")
	set.grant(PrivilegeGrant{Table: "orders", Privileges: PrivilegeSelect})
	set.grant(PrivilegeGrant{Table: "orders", Column: "total", Privileges: PrivilegeUpdate})

	set.revoke(PrivilegeGrant{Table: "orders", Column: "total", Privileges: PrivilegeUpdate})
	assert.Empty(t, set.OnColumns, "an emptied column tier must be pruned")

	set.revoke(PrivilegeGrant{Table: "orders", Privileges: PrivilegeSelect})
	assert.Empty(t, set.OnTables, "an emptied table tier must be pruned")
	assert.True(t, set.IsEmpty())
}

func TestPrivilegeSet_RevokeUnheldPrivilegeIsANoOp(t *testing.T) {
	set := newPrivilegeSet("shop", "alice@%")
	set.grant(PrivilegeGrant{Table: "orders", Privileges: PrivilegeSelect})

	set.revoke(PrivilegeGrant{Table: "customers", Privileges: PrivilegeSelect})
	set.revoke(PrivilegeGrant{Table: "orders", Column: "nope", Privileges: PrivilegeSelect})

	assert.Equal(t, PrivilegeSelect, set.Table("orders"))
}

func TestPrivilegeSet_IsEmpty(t *testing.T) {
	assert.True(t, newPrivilegeSet("shop", "alice@%").IsEmpty())

	dbTier := newPrivilegeSet("shop", "alice@%")
	dbTier.grant(PrivilegeGrant{Privileges: PrivilegeSelect})
	assert.False(t, dbTier.IsEmpty())

	tableTier := newPrivilegeSet("shop", "alice@%")
	tableTier.grant(PrivilegeGrant{Table: "orders", Privileges: PrivilegeSelect})
	assert.False(t, tableTier.IsEmpty())

	columnTier := newPrivilegeSet("shop", "alice@%")
	columnTier.grant(PrivilegeGrant{Table: "orders", Column: "id", Privileges: PrivilegeSelect})
	assert.False(t, columnTier.IsEmpty())
}

func TestPrivilegeSet_JSONRoundTrip(t *testing.T) {
	set := newPrivilegeSet("shop", "alice@%")
	set.grant(PrivilegeGrant{Privileges: PrivilegeSelect})
	set.grant(PrivilegeGrant{Table: "orders", Privileges: PrivilegeInsert | PrivilegeGrantOption})
	set.grant(PrivilegeGrant{Table: "orders", Column: "total", Privileges: PrivilegeUpdate})

	data, err := json.Marshal(set)
	require.NoError(t, err)

	var back PrivilegeSet
	require.NoError(t, json.Unmarshal(data, &back))
	assert.Equal(t, set.OnDatabase, back.OnDatabase)
	assert.Equal(t, set.OnTables, back.OnTables)
	assert.Equal(t, set.OnColumns, back.OnColumns)
}

func TestValidateGrantee(t *testing.T) {
	assert.NoError(t, ValidateGrantee("alice@%"))
	assert.NoError(t, ValidateGrantee("svc.reporting@%"))

	for _, bad := range []string{"", "a/b", "__latch", "__epoch"} {
		var invalid *PrivilegeInvalidGranteeError
		assert.ErrorAs(t, ValidateGrantee(bad), &invalid, "expected %q to be rejected", bad)
	}
}
