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
	"context"
	"errors"
	"strings"
	"testing"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/tenantid"
)

// emptyAnalyzer is an analyzer whose catalog carries no service registry, so
// the rewrite rule's node substitution can be exercised without standing up a
// provider. The nodes it produces resolve their service at execution time, so
// the registry is irrelevant to the substitution itself.
func emptyAnalyzer() *analyzer.Analyzer {
	return analyzer.NewBuilder(nil).Build()
}

// nodeContext builds a session context for node execution.
func nodeContext(tenantID, subject, database string) *sqle.Context {
	ctx := sqle.NewContext(context.Background(), sqle.WithSession(newFakeSession(tenantID, subject)))
	ctx.SetCurrentDatabase(database)
	return ctx
}

// rows drains a node's iterator.
func rows(t *testing.T, ctx *sqle.Context, node sqle.ExecSourceRel) []sqle.Row {
	t.Helper()
	iter, err := node.RowIter(ctx, nil)
	require.NoError(t, err)
	out, err := sqle.RowIterToRows(ctx, iter)
	require.NoError(t, err)
	return out
}

// ---------------------------------------------------------------------------
// sql.Node contract
// ---------------------------------------------------------------------------

func TestNodes_NodeContract(t *testing.T) {
	nodes := map[string]sqle.Node{
		"grant":       &mutationNode{kind: mutationGrant, spec: &privilegeSpec{}},
		"revoke":      &mutationNode{kind: mutationRevoke, spec: &privilegeSpec{}},
		"show grants": &showGrantsNode{},
	}

	for name, node := range nodes {
		t.Run(name, func(t *testing.T) {
			assert.True(t, node.Resolved(), "the node is built from a resolved plan node")
			assert.Nil(t, node.Children())

			same, err := node.WithChildren(nil)
			require.NoError(t, err)
			assert.Same(t, node, same)

			_, err = node.WithChildren(nil, node)
			assert.Error(t, err, "a childless node must reject children")

			collation, coercibility := node.(sqle.CollationCoercible).CollationCoercibility(nil)
			assert.Equal(t, sqle.Collation_binary, collation)
			assert.Equal(t, ignorableCoercibility, coercibility)

			assert.NotEmpty(t, node.String())
		})
	}
}

func TestNodes_ReadOnliness(t *testing.T) {
	assert.False(t, (&mutationNode{kind: mutationGrant, spec: &privilegeSpec{}}).IsReadOnly(),
		"GRANT writes to the catalog")
	assert.False(t, (&mutationNode{kind: mutationRevoke, spec: &privilegeSpec{}}).IsReadOnly())
	assert.True(t, (&showGrantsNode{}).IsReadOnly())
}

func TestNodes_Schemas(t *testing.T) {
	assert.Len(t, (&mutationNode{spec: &privilegeSpec{}}).Schema(nil), 1,
		"GRANT and REVOKE return an OK result")

	explicit := (&showGrantsNode{grantee: "bob@%"}).Schema(nil)
	require.Len(t, explicit, 1)
	assert.Equal(t, "Grants for `bob`@`%`", explicit[0].Name)

	ctx := nodeContext(testTenant, testSubject, testDatabase)
	current := (&showGrantsNode{}).Schema(ctx)
	require.Len(t, current, 1)
	assert.Equal(t, "Grants for `alice`@`%`", current[0].Name,
		"the current-user form names the session's identity")
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

func TestMutationNode_AppliesToEveryGrantee(t *testing.T) {
	h := sqltest.NewHarness()
	ctx := nodeContext(tenantid.System, "operator", testDatabase)

	node := &mutationNode{
		registry: h.Registry,
		kind:     mutationGrant,
		spec: &privilegeSpec{
			grantees: []string{"bob@%", "carol@%"},
			grants:   []service.PrivilegeGrant{{Table: "orders", Privileges: service.PrivilegeSelect}},
		},
	}
	require.Len(t, rows(t, ctx, node), 1, "GRANT returns a single OK result row")

	for _, grantee := range []string{"bob@%", "carol@%"} {
		set, err := h.PrivSvc.Privileges(context.Background(), testDatabase, grantee)
		require.NoError(t, err)
		assert.Equal(t, service.PrivilegeSelect, set.Table("orders"))
	}
}

func TestMutationNode_RevokeUndoesGrant(t *testing.T) {
	h := sqltest.NewHarness()
	ctx := nodeContext(tenantid.System, "operator", testDatabase)
	spec := &privilegeSpec{
		grantees: []string{"bob@%"},
		grants:   []service.PrivilegeGrant{{Table: "orders", Privileges: service.PrivilegeSelect}},
	}

	rows(t, ctx, &mutationNode{registry: h.Registry, kind: mutationGrant, spec: spec})
	rows(t, ctx, &mutationNode{registry: h.Registry, kind: mutationRevoke, spec: spec})

	set, err := h.PrivSvc.Privileges(context.Background(), testDatabase, "bob@%")
	require.NoError(t, err)
	assert.True(t, set.IsEmpty())
}

func TestMutationNode_PropagatesServiceError(t *testing.T) {
	h := sqltest.NewHarness()
	ctx := nodeContext(tenantid.System, "operator", testDatabase)

	// A grantee the catalog cannot store is rejected by the service, and the
	// node must surface that rather than swallow it.
	node := &mutationNode{
		registry: h.Registry,
		kind:     mutationGrant,
		spec: &privilegeSpec{
			grantees: []string{"a/b"},
			grants:   []service.PrivilegeGrant{{Table: "orders", Privileges: service.PrivilegeSelect}},
		},
	}
	_, err := node.RowIter(ctx, nil)
	var invalid *service.PrivilegeInvalidGranteeError
	assert.ErrorAs(t, err, &invalid)
}

func TestShowGrantsNode_ReportsTheSessionIdentity(t *testing.T) {
	h := sqltest.NewHarness()
	require.NoError(t, h.PrivSvc.Grant(context.Background(), &service.GrantPrivilegesRequest{
		Database: testDatabase,
		Grantee:  testGrantee,
		Grants:   []service.PrivilegeGrant{{Table: "orders", Privileges: service.PrivilegeSelect}},
	}))

	ctx := nodeContext(testTenant, testSubject, testDatabase)
	out := rows(t, ctx, &showGrantsNode{registry: h.Registry})

	lines := make([]string, len(out))
	for i, row := range out {
		lines[i] = row[0].(string)
	}
	assert.Equal(t, []string{
		"GRANT USAGE ON *.* TO `alice`@`%`",
		"GRANT SELECT ON `shop`.`orders` TO `alice`@`%`",
	}, lines)
}

func TestShowGrantsNode_RequiresAnIdentityForTheCurrentUserForm(t *testing.T) {
	h := sqltest.NewHarness()
	ctx := sqle.NewContext(context.Background(),
		sqle.WithSession(&plainSession{BaseSession: sqle.NewBaseSession()}))
	ctx.SetCurrentDatabase(testDatabase)

	_, err := (&showGrantsNode{registry: h.Registry}).RowIter(ctx, nil)
	var unidentified *UnidentifiedSessionError
	assert.ErrorAs(t, err, &unidentified)
}

// ---------------------------------------------------------------------------
// Service resolution
// ---------------------------------------------------------------------------

func TestResolvePrivilegeService(t *testing.T) {
	h := sqltest.NewHarness()

	t.Run("no registry", func(t *testing.T) {
		_, _, err := resolvePrivilegeService(nodeContext(testTenant, testSubject, testDatabase), nil, "", "GRANT")
		var unavailable *CatalogUnavailableError
		require.ErrorAs(t, err, &unavailable)
		assert.Equal(t, "GRANT", unavailable.Statement)
	})

	t.Run("no database anywhere", func(t *testing.T) {
		_, _, err := resolvePrivilegeService(nodeContext(testTenant, testSubject, ""), h.Registry, "", "REVOKE")
		var noDB *NoCurrentDatabaseError
		require.ErrorAs(t, err, &noDB)
		assert.Equal(t, "REVOKE", noDB.Statement)
	})

	t.Run("the session database is used when the statement names none", func(t *testing.T) {
		_, database, err := resolvePrivilegeService(
			nodeContext(testTenant, testSubject, testDatabase), h.Registry, "", "GRANT")
		require.NoError(t, err)
		assert.Equal(t, testDatabase, database)
	})

	t.Run("an explicit database wins", func(t *testing.T) {
		_, database, err := resolvePrivilegeService(
			nodeContext(testTenant, testSubject, testDatabase), h.Registry, "elsewhere", "GRANT")
		require.NoError(t, err)
		assert.Equal(t, "elsewhere", database)
	})

	t.Run("registry failure propagates", func(t *testing.T) {
		sentinel := errors.New("no such tenant")
		_, _, err := resolvePrivilegeService(
			nodeContext(testTenant, testSubject, testDatabase), &failingRegistry{err: sentinel}, "", "GRANT")
		assert.ErrorIs(t, err, sentinel)
	})
}

// ---------------------------------------------------------------------------
// Rewrite rule
// ---------------------------------------------------------------------------

// TestRewrite_LeavesUnrelatedNodesAlone pins that the rule is a targeted
// replacement, not a general traversal that could disturb a plan.
func TestRewrite_LeavesUnrelatedNodesAlone(t *testing.T) {
	node := plan.NewShowTables(nil, false, nil)
	out, same, err := Rewrite(sqle.NewEmptyContext(), emptyAnalyzer(), node, nil, nil, nil)
	require.NoError(t, err)
	assert.Same(t, node, out)
	assert.True(t, bool(same))
}

// TestRewrite_ReplacesEachPrivilegeNode pins the substitution itself.
func TestRewrite_ReplacesEachPrivilegeNode(t *testing.T) {
	user := plan.UserName{Name: "bob", AnyHost: true}

	grant, _, err := Rewrite(sqle.NewEmptyContext(), emptyAnalyzer(), &plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: testDatabase, TableRoutine: "orders"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{user},
	}, nil, nil, nil)
	require.NoError(t, err)
	assert.IsType(t, &mutationNode{}, grant)

	revoke, _, err := Rewrite(sqle.NewEmptyContext(), emptyAnalyzer(), &plan.Revoke{
		PrivilegeLevel: plan.PrivilegeLevel{Database: testDatabase, TableRoutine: "orders"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{user},
	}, nil, nil, nil)
	require.NoError(t, err)
	assert.IsType(t, &mutationNode{}, revoke)

	show, _, err := Rewrite(sqle.NewEmptyContext(), emptyAnalyzer(), &plan.ShowGrants{
		CurrentUser: true,
	}, nil, nil, nil)
	require.NoError(t, err)
	assert.IsType(t, &showGrantsNode{}, show)
}

// TestRewrite_SurfacesConversionFailures pins that a refused statement fails
// during analysis, before anything is written.
func TestRewrite_SurfacesConversionFailures(t *testing.T) {
	_, _, err := Rewrite(sqle.NewEmptyContext(), emptyAnalyzer(), &plan.Grant{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "*", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{{Name: "bob", AnyHost: true}},
	}, nil, nil, nil)
	var global *GlobalPrivilegeLevelError
	assert.ErrorAs(t, err, &global)

	_, _, err = Rewrite(sqle.NewEmptyContext(), emptyAnalyzer(), &plan.Revoke{
		PrivilegeLevel: plan.PrivilegeLevel{Database: "*", TableRoutine: "*"},
		Privileges:     []plan.Privilege{{Type: plan.PrivilegeType_Select}},
		Users:          []plan.UserName{{Name: "bob", AnyHost: true}},
	}, nil, nil, nil)
	assert.ErrorAs(t, err, &global)

	host := plan.UserName{Name: "bob", Host: "localhost"}
	_, _, err = Rewrite(sqle.NewEmptyContext(), emptyAnalyzer(), &plan.ShowGrants{For: &host}, nil, nil, nil)
	var hostErr *UnsupportedGranteeHostError
	assert.ErrorAs(t, err, &hostErr)
}

func TestRewriteRuleIdentity(t *testing.T) {
	assert.NotEmpty(t, RewriteRuleName)
	assert.Equal(t, "RuleId(65536)", RewriteRuleID.String(),
		"an integrator rule id must fall outside go-mysql-server's own dense block")
}

// ---------------------------------------------------------------------------
// Statement-shape refusal messages
// ---------------------------------------------------------------------------

func TestStatementRefusalErrors_NameWhatTheyRefused(t *testing.T) {
	tests := []struct {
		err      error
		contains []string
	}{
		{&GlobalPrivilegeLevelError{Statement: "GRANT"}, []string{"GRANT", "*.*", "per-database"}},
		{&UnsupportedPrivilegeError{Privilege: "SUPER"}, []string{`"SUPER"`}},
		{&UnsupportedObjectTypeError{ObjectType: "PROCEDURE"}, []string{"PROCEDURE"}},
		{&UnsupportedGranteeHostError{Grantee: "bob", Host: "localhost"}, []string{`"bob"`, `"localhost"`}},
		{&UnsupportedGrantClauseError{Clause: "AS <user>"}, []string{"AS <user>"}},
		{&NoCurrentDatabaseError{Statement: "SHOW GRANTS"}, []string{"SHOW GRANTS", "database"}},
		{&CatalogUnavailableError{Statement: "GRANT"}, []string{"GRANT", "registry"}},
		{&UnidentifiedSessionError{Statement: "SHOW GRANTS"}, []string{"SHOW GRANTS", "identity"}},
	}

	for _, tc := range tests {
		msg := tc.err.Error()
		assert.Truef(t, strings.HasPrefix(msg, "privilege: "),
			"every error must identify its origin package, got %q", msg)
		for _, want := range tc.contains {
			assert.Contains(t, msg, want)
		}
	}
}

func TestAccessDeniedErrorsCarryTheMySQLCodes(t *testing.T) {
	assert.Equal(t, ErrCodeDatabaseAccessDenied, errDatabaseAccessDenied("alice@%", "shop").Num)
	assert.Equal(t, ErrCodeTableAccessDenied, errTableAccessDenied("SELECT", "alice@%", "orders").Num)
	assert.Equal(t, ErrCodeColumnAccessDenied, errColumnAccessDenied("SELECT", "alice@%", "orders", "total").Num)

	for _, err := range []error{
		errDatabaseAccessDenied("alice@%", "shop"),
		errTableAccessDenied("SELECT", "alice@%", "orders"),
		errColumnAccessDenied("SELECT", "alice@%", "orders", "total"),
	} {
		assert.Equal(t, sqlStateAccessViolation, sqle.CastSQLError(err).State)
		assert.Contains(t, err.Error(), "alice@%")
	}
}

func TestSnapshot_NilAndUnlatchedPermitEverything(t *testing.T) {
	for _, snap := range []*snapshot{nil, {latched: false}} {
		assert.True(t, snap.permits("orders", service.PrivilegeAll))
		assert.True(t, snap.permitsColumn("orders", "total", service.PrivilegeAll))
		assert.True(t, snap.permitsDatabase(service.PrivilegeAll))
	}
}
