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

	ast "github.com/dolthub/vitess/go/vt/sqlparser"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
)

// buildSet composes a record through the same GRANT path the service uses,
// so a rendering test cannot drift from how records are actually built.
func buildSet(t *testing.T, grants ...service.PrivilegeGrant) *service.PrivilegeSet {
	t.Helper()
	svc := sqltest.NewHarness().PrivSvc
	require.NoError(t, svc.Grant(t.Context(), &service.GrantPrivilegesRequest{
		Database: "shop", Grantee: "alice@%", Grants: grants,
	}))
	set, err := svc.Privileges(t.Context(), "shop", "alice@%")
	require.NoError(t, err)
	return set
}

func TestRenderGrants_UsageLineIsAlwaysFirst(t *testing.T) {
	lines := renderGrants("shop", "alice@%", nil)
	require.Len(t, lines, 1)
	assert.Equal(t, "GRANT USAGE ON *.* TO `alice`@`%`", lines[0],
		"MySQL always reports the account's existence with a USAGE line")
}

func TestRenderGrants_DatabaseTier(t *testing.T) {
	set := buildSet(t, service.PrivilegeGrant{Privileges: service.PrivilegeSelect | service.PrivilegeInsert})

	lines := renderGrants("shop", "alice@%", set)
	assert.Equal(t, []string{
		"GRANT USAGE ON *.* TO `alice`@`%`",
		"GRANT SELECT, INSERT ON `shop`.* TO `alice`@`%`",
	}, lines)
}

func TestRenderGrants_DatabaseTierWithGrantOption(t *testing.T) {
	set := buildSet(t, service.PrivilegeGrant{
		Privileges: service.PrivilegeSelect | service.PrivilegeGrantOption,
	})

	lines := renderGrants("shop", "alice@%", set)
	assert.Equal(t, "GRANT SELECT ON `shop`.* TO `alice`@`%` WITH GRANT OPTION", lines[1])
}

func TestRenderGrants_AllPrivilegesCollapses(t *testing.T) {
	set := buildSet(t, service.PrivilegeGrant{Privileges: service.PrivilegeAll})

	lines := renderGrants("shop", "alice@%", set)
	assert.Equal(t, "GRANT ALL PRIVILEGES ON `shop`.* TO `alice`@`%`", lines[1])
}

func TestRenderGrants_TableTier(t *testing.T) {
	set := buildSet(t,
		service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect},
		service.PrivilegeGrant{Table: "customers", Privileges: service.PrivilegeInsert},
	)

	lines := renderGrants("shop", "alice@%", set)
	assert.Equal(t, []string{
		"GRANT USAGE ON *.* TO `alice`@`%`",
		"GRANT INSERT ON `shop`.`customers` TO `alice`@`%`",
		"GRANT SELECT ON `shop`.`orders` TO `alice`@`%`",
	}, lines, "tables must be reported in a stable, sorted order")
}

func TestRenderGrants_ColumnTierGroupsColumns(t *testing.T) {
	set := buildSet(t,
		service.PrivilegeGrant{Table: "orders", Column: "id", Privileges: service.PrivilegeSelect},
		service.PrivilegeGrant{Table: "orders", Column: "total", Privileges: service.PrivilegeSelect},
		service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeInsert},
	)

	lines := renderGrants("shop", "alice@%", set)
	assert.Equal(t,
		"GRANT SELECT (`id`, `total`), INSERT ON `shop`.`orders` TO `alice`@`%`",
		lines[1])
}

// TestRenderGrants_ColumnCoveredByTableTierIsNotRepeated pins that a column
// grant subsumed by the table tier is not printed twice, matching MySQL.
func TestRenderGrants_ColumnCoveredByTableTierIsNotRepeated(t *testing.T) {
	set := buildSet(t,
		service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeSelect},
		service.PrivilegeGrant{Table: "orders", Column: "id", Privileges: service.PrivilegeSelect},
	)

	lines := renderGrants("shop", "alice@%", set)
	assert.Equal(t, "GRANT SELECT ON `shop`.`orders` TO `alice`@`%`", lines[1])
}

// TestRenderGrants_LinesAreValidGrantStatements is the round-trip guarantee:
// everything SHOW GRANTS emits must parse back as a GRANT.
func TestRenderGrants_LinesAreValidGrantStatements(t *testing.T) {
	set := buildSet(t,
		service.PrivilegeGrant{Privileges: service.PrivilegeSelect},
		service.PrivilegeGrant{Table: "orders", Privileges: service.PrivilegeInsert | service.PrivilegeGrantOption},
		service.PrivilegeGrant{Table: "customers", Column: "zip", Privileges: service.PrivilegeSelect},
	)

	for _, line := range renderGrants("shop", "alice@%", set) {
		stmt, err := ast.Parse(line)
		require.NoErrorf(t, err, "SHOW GRANTS emitted an unparseable line: %s", line)
		assert.IsTypef(t, &ast.GrantPrivilege{}, stmt, "line did not parse as a GRANT: %s", line)
	}
}

func TestQuoteIdent_EscapesBackticks(t *testing.T) {
	assert.Equal(t, "`orders`", quoteIdent("orders"))
	assert.Equal(t, "`we``ird`", quoteIdent("we`ird"))
	assert.Equal(t, "`a`, `b`", quoteIdents([]string{"a", "b"}))
}

func TestGranteeDisplay(t *testing.T) {
	assert.Equal(t, "`alice`@`%`", granteeDisplay("alice@%"))
	assert.Equal(t, "`alice`@`%`", granteeDisplay("alice"))
}

func TestGrantOptionSuffix(t *testing.T) {
	assert.Empty(t, grantOptionSuffix(service.PrivilegeSelect))
	assert.Equal(t, " WITH GRANT OPTION", grantOptionSuffix(service.PrivilegeGrantOption))
}

func TestSortedMasks(t *testing.T) {
	got := sortedMasks(map[service.PrivilegeMask][]string{
		service.PrivilegeUpdate: {"a"},
		service.PrivilegeSelect: {"b"},
		service.PrivilegeInsert: {"c"},
	})
	assert.Equal(t, []service.PrivilegeMask{
		service.PrivilegeSelect, service.PrivilegeInsert, service.PrivilegeUpdate,
	}, got)
}

func TestMutationNames(t *testing.T) {
	assert.Equal(t, "GRANT", mutationNames[mutationGrant])
	assert.Equal(t, "REVOKE", mutationNames[mutationRevoke])
	assert.Len(t, mutationAppliers, len(mutationNames),
		"every mutation kind must have both a name and an applier")
}

func TestMutationNode_String(t *testing.T) {
	named := &mutationNode{kind: mutationGrant, spec: &privilegeSpec{database: "shop", grantees: []string{"alice@%"}}}
	assert.Equal(t, "GRANT(On: shop, To: alice@%)", named.String())

	deferred := &mutationNode{kind: mutationRevoke, spec: &privilegeSpec{grantees: []string{"alice@%"}}}
	assert.Contains(t, deferred.String(), "<current database>")
}

func TestShowGrantsNode_String(t *testing.T) {
	assert.Equal(t, "ShowGrants(CurrentUser)", (&showGrantsNode{}).String())
	assert.Equal(t, "ShowGrants(bob@%)", (&showGrantsNode{grantee: "bob@%"}).String())
}
