//go:build qrdb_sql

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

package privilege

import (
	"testing"

	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// anyHostUser is the account form every supported grant names.
func anyHostUser(name string) plan.UserName {
	return plan.UserName{Name: name, Host: anyHost, AnyHost: true}
}

// ---------------------------------------------------------------------------
// Supported GRANT/REVOKE forms
// ---------------------------------------------------------------------------

func TestGrantSpec_TableTier(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "orders"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	require.NoError(t, err)

	assert.Equal(t, "shop", spec.database)
	assert.Equal(t, []string{"alice@%"}, spec.grantees)
	assert.Equal(t, []service.PrivilegeGrant{
		{Table: "orders", Privileges: service.PrivilegeSelect},
	}, spec.grants)
}

func TestGrantSpec_DatabaseTier(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges: []plan.Privilege{
			{Type: plan.PrivilegeType_Select},
			{Type: plan.PrivilegeType_Insert},
		},
		Users: []plan.UserName{anyHostUser("alice")},
	})
	require.NoError(t, err)

	assert.Equal(t, []service.PrivilegeGrant{
		{Privileges: service.PrivilegeSelect | service.PrivilegeInsert},
	}, spec.grants, "ON db.* must produce a single database-tier entry")
}

func TestGrantSpec_ColumnTierFlattens(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "orders"},
		Privileges: []plan.Privilege{
			{Type: plan.PrivilegeType_Select, Columns: []string{"id", "total"}},
			{Type: plan.PrivilegeType_Insert},
		},
		Users: []plan.UserName{anyHostUser("alice")},
	})
	require.NoError(t, err)

	assert.Equal(t, []service.PrivilegeGrant{
		{Table: "orders", Column: "id", Privileges: service.PrivilegeSelect},
		{Table: "orders", Column: "total", Privileges: service.PrivilegeSelect},
		{Table: "orders", Privileges: service.PrivilegeInsert},
	}, spec.grants)
}

func TestGrantSpec_UnnamedDatabaseIsDeferredToTheSession(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{TableRoutine: "orders"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	require.NoError(t, err)
	assert.Empty(t, spec.database,
		"GRANT ... ON tbl must resolve its database at execution time, not at analysis time")
}

func TestGrantSpec_WithGrantOption(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel:  plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:      []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:           []plan.UserName{anyHostUser("alice")},
		WithGrantOption: true,
	})
	require.NoError(t, err)

	assert.Contains(t, spec.grants, service.PrivilegeGrant{Privileges: service.PrivilegeGrantOption})
}

func TestGrantSpec_AllPrivileges(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_All}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	require.NoError(t, err)

	assert.Equal(t, service.PrivilegeAll, spec.grants[0].Privileges)
	assert.False(t, spec.grants[0].Privileges.HasAny(service.PrivilegeGrantOption),
		"ALL PRIVILEGES must not silently confer GRANT OPTION")
}

func TestGrantSpec_UsageArmsWithoutGranting(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Usage}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	require.NoError(t, err)

	require.Len(t, spec.grants, 1)
	assert.Equal(t, service.PrivilegeMask(0), spec.grants[0].Privileges)
}

func TestGrantSpec_MultipleGrantees(t *testing.T) {
	spec, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{anyHostUser("alice"), anyHostUser("bob")},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"alice@%", "bob@%"}, spec.grantees)
}

func TestRevokeSpec_SharesTheGrantConversion(t *testing.T) {
	spec, err := revokeSpec(&plan.Revoke{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "orders"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	require.NoError(t, err)
	assert.Equal(t, []service.PrivilegeGrant{
		{Table: "orders", Privileges: service.PrivilegeSelect},
	}, spec.grants)
}

// ---------------------------------------------------------------------------
// Refusals -- every form that would otherwise be stored and never enforced
// ---------------------------------------------------------------------------

func TestGrantSpec_RefusesGlobalLevel(t *testing.T) {
	for _, level := range []plan.PrivilegeLevel{
		{Database: "*", TableRoutine: "*"},
		{Database: "*", TableRoutine: "orders"},
	} {
		_, err := grantSpec(&plan.Grant{
			PrivilegeLevel: level,
			Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
			Users:          []plan.UserName{anyHostUser("alice")},
		})
		var global *GlobalPrivilegeLevelError
		require.ErrorAs(t, err, &global)
		assert.Equal(t, "GRANT", global.Statement)
	}
}

func TestRevokeSpec_RefusesGlobalLevel(t *testing.T) {
	// REVOKE ALL PRIVILEGES, GRANT OPTION FROM u parses to the *.* level with
	// no ON clause, so it is refused for the same reason GRANT ON *.* is.
	_, err := revokeSpec(&plan.Revoke{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "*", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_All}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	var global *GlobalPrivilegeLevelError
	require.ErrorAs(t, err, &global)
	assert.Equal(t, "REVOKE", global.Statement)
}

func TestGrantSpec_RefusesUnsupportedPrivileges(t *testing.T) {
	tests := []struct {
		privilege plan.PrivilegeType
		want      string
	}{
		{plan.PrivilegeType_Super, "SUPER"},
		{plan.PrivilegeType_Process, "PROCESS"},
		{plan.PrivilegeType_File, "FILE"},
		{plan.PrivilegeType_CreateUser, "CREATE USER"},
		{plan.PrivilegeType_Trigger, "TRIGGER"},
		{plan.PrivilegeType_Execute, "EXECUTE"},
		{plan.PrivilegeType_LockTables, "LOCK TABLES"},
		{plan.PrivilegeType_ShowDatabases, "SHOW DATABASES"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			_, err := grantSpec(&plan.Grant{
				PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
				Privileges:     []plan.Privilege{{Type: tc.privilege}},
				Users:          []plan.UserName{anyHostUser("alice")},
			})
			var unsupported *UnsupportedPrivilegeError
			require.ErrorAs(t, err, &unsupported)
			assert.Equal(t, tc.want, unsupported.Privilege,
				"the refusal must name the privilege it refused")
		})
	}
}

func TestGrantSpec_RefusesDynamicPrivilegeByItsOwnName(t *testing.T) {
	_, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Dynamic, Dynamic: "binlog_admin"}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	var unsupported *UnsupportedPrivilegeError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "binlog_admin", unsupported.Privilege)
}

func TestGrantSpec_RefusesRoutineObjectTypes(t *testing.T) {
	for objectType, want := range objectTypeNames {
		_, err := grantSpec(&plan.Grant{
			PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "p"},
			ObjectType:     objectType,
			Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Execute}},
			Users:          []plan.UserName{anyHostUser("alice")},
		})
		var unsupported *UnsupportedObjectTypeError
		require.ErrorAs(t, err, &unsupported)
		assert.Equal(t, want, unsupported.ObjectType)
	}
}

func TestGrantSpec_RefusesColumnPrivilegesAtTheDatabaseLevel(t *testing.T) {
	_, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select, Columns: []string{"id"}}},
		Users:          []plan.UserName{anyHostUser("alice")},
	})
	var unsupported *UnsupportedPrivilegeError
	assert.ErrorAs(t, err, &unsupported)
}

func TestGrantSpec_RefusesHostQualifiedGrantee(t *testing.T) {
	_, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{{Name: "alice", Host: "localhost"}},
	})
	var host *UnsupportedGranteeHostError
	require.ErrorAs(t, err, &host)
	assert.Equal(t, "localhost", host.Host)
	assert.Equal(t, "alice", host.Grantee)
}

func TestGrantSpec_AcceptsBareAndWildcardHosts(t *testing.T) {
	for _, user := range []plan.UserName{
		{Name: "alice"},
		{Name: "alice", Host: anyHost},
		{Name: "alice", AnyHost: true},
	} {
		spec, err := grantSpec(&plan.Grant{
			PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
			Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
			Users:          []plan.UserName{user},
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"alice@%"}, spec.grantees)
	}
}

func TestGrantSpec_RefusesGranteeTheCatalogCannotStore(t *testing.T) {
	_, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{anyHostUser("a/b")},
	})
	var invalid *service.PrivilegeInvalidGranteeError
	assert.ErrorAs(t, err, &invalid,
		"the SQL layer must refuse against the catalog's own key rules, not a second copy of them")
}

func TestGrantSpec_RefusesUserAssumption(t *testing.T) {
	_, err := grantSpec(&plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{anyHostUser("alice")},
		As:             &plan.GrantUserAssumption{},
	})
	var clause *UnsupportedGrantClauseError
	require.ErrorAs(t, err, &clause)
	assert.Equal(t, "AS <user>", clause.Clause)
}

// ---------------------------------------------------------------------------
// SHOW GRANTS grantee resolution
// ---------------------------------------------------------------------------

func TestShowGrantsGrantee(t *testing.T) {
	current, err := showGrantsGrantee(&plan.ShowGrants{CurrentUser: true})
	require.NoError(t, err)
	assert.Empty(t, current, "the current-user form must be resolved from the session, not the plan")

	nilFor, err := showGrantsGrantee(&plan.ShowGrants{})
	require.NoError(t, err)
	assert.Empty(t, nilFor)

	named := anyHostUser("bob")
	explicit, err := showGrantsGrantee(&plan.ShowGrants{For: &named})
	require.NoError(t, err)
	assert.Equal(t, "bob@%", explicit)

	hostQualified := plan.UserName{Name: "bob", Host: "localhost"}
	_, err = showGrantsGrantee(&plan.ShowGrants{For: &hostQualified})
	var host *UnsupportedGranteeHostError
	assert.ErrorAs(t, err, &host)
}

// ---------------------------------------------------------------------------
// Conversion table integrity
// ---------------------------------------------------------------------------

// TestPrivilegeTables_AreDisjoint pins that no privilege is both convertible
// and listed as unsupported: the two tables together must partition the plan
// privilege space, so a refusal can always name what it refused.
func TestPrivilegeTables_AreDisjoint(t *testing.T) {
	for privilege := range privilegeConversions {
		_, named := privilegeNames[privilege]
		assert.False(t, named,
			"privilege %d is both convertible and listed as unsupported", privilege)
	}
	for privilege, name := range privilegeNames {
		assert.NotEmpty(t, name, "privilege %d is listed as unsupported with no name", privilege)
	}
}

func TestTableOf(t *testing.T) {
	assert.Empty(t, tableOf(plan.PrivilegeLevel{Database: "shop", TableRoutine: "*"}))
	assert.Equal(t, "orders", tableOf(plan.PrivilegeLevel{Database: "shop", TableRoutine: "orders"}))
}
