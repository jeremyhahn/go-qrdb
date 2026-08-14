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
	"sort"
	"strings"

	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/types"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Executable plan nodes
//
// These replace go-mysql-server's *plan.Grant, *plan.Revoke, and
// *plan.ShowGrants during post-analysis (see rewrite.go). They implement
// sql.ExecSourceRel, the escape hatch the execution builder offers for node
// types defined outside go-mysql-server (sql/rowexec/node_builder.gen.go),
// so nothing about the execution path had to be forked to reach them.
//
// Every node resolves its service.PrivilegeServicer at EXECUTION time from
// the session in the context, never at analysis time. A plan may be cached
// and replayed on another session in the same engine, so binding a
// tenant-scoped service into the plan would let one tenant's cached GRANT
// execute against another tenant's catalog.
// ---------------------------------------------------------------------------

// ignorableCoercibility is MySQL's lowest-priority coercibility level, which
// go-mysql-server's own privilege plan nodes report because their output
// participates in no collation comparison.
const ignorableCoercibility byte = 7

// mutationKind distinguishes the two catalog mutations.
type mutationKind int

const (
	// mutationGrant adds privileges.
	mutationGrant mutationKind = iota

	// mutationRevoke removes them.
	mutationRevoke
)

// mutationApplier applies one grantee's worth of a mutation.
type mutationApplier func(ctx context.Context, svc service.PrivilegeServicer, database, grantee string, grants []service.PrivilegeGrant) error

// mutationAppliers dispatches a mutationKind onto the service call that
// performs it. Both calls take the same shape, which is why one node type
// serves GRANT and REVOKE.
var mutationAppliers = map[mutationKind]mutationApplier{
	mutationGrant: func(ctx context.Context, svc service.PrivilegeServicer, database, grantee string, grants []service.PrivilegeGrant) error {
		return svc.Grant(ctx, &service.GrantPrivilegesRequest{Database: database, Grantee: grantee, Grants: grants})
	},
	mutationRevoke: func(ctx context.Context, svc service.PrivilegeServicer, database, grantee string, grants []service.PrivilegeGrant) error {
		return svc.Revoke(ctx, &service.RevokePrivilegesRequest{Database: database, Grantee: grantee, Revokes: grants})
	},
}

// mutationNames labels a mutationKind in plan output and error messages.
var mutationNames = map[mutationKind]string{
	mutationGrant:  "GRANT",
	mutationRevoke: "REVOKE",
}

// privilegeSpec is one fully validated GRANT or REVOKE, independent of the
// session that will run it.
type privilegeSpec struct {
	// database is the database the statement names, empty when it named
	// none and the executing session's current database applies.
	database string

	// grantees are the canonical grantee names the statement addresses.
	grantees []string

	// grants are the privilege assignments, already flattened to one entry
	// per (table, column) tier.
	grants []service.PrivilegeGrant
}

// mutationNode applies a GRANT or REVOKE to qrdb's privilege catalog.
type mutationNode struct {
	registry service.Registry
	spec     *privilegeSpec
	kind     mutationKind
}

// Compile-time interface assertions.
var (
	_ sqle.Node               = (*mutationNode)(nil)
	_ sqle.ExecSourceRel      = (*mutationNode)(nil)
	_ sqle.CollationCoercible = (*mutationNode)(nil)
	_ sqle.Node               = (*showGrantsNode)(nil)
	_ sqle.ExecSourceRel      = (*showGrantsNode)(nil)
	_ sqle.CollationCoercible = (*showGrantsNode)(nil)
)

// Resolved implements sql.Node. The node is built from an already-resolved
// plan node and holds no unresolved references.
func (n *mutationNode) Resolved() bool { return true }

// IsReadOnly implements sql.Node.
func (n *mutationNode) IsReadOnly() bool { return false }

// Children implements sql.Node.
func (n *mutationNode) Children() []sqle.Node { return nil }

// WithChildren implements sql.Node.
func (n *mutationNode) WithChildren(_ *sqle.Context, children ...sqle.Node) (sqle.Node, error) {
	if len(children) != 0 {
		return nil, sqle.ErrInvalidChildrenNumber.New(n, len(children), 0)
	}
	return n, nil
}

// Schema implements sql.Node: the statement returns an OK result.
func (n *mutationNode) Schema(_ *sqle.Context) sqle.Schema { return types.OkResultSchema }

// CollationCoercibility implements sql.CollationCoercible, matching what
// go-mysql-server's own *plan.Grant reports.
func (n *mutationNode) CollationCoercibility(_ *sqle.Context) (sqle.CollationID, byte) {
	return sqle.Collation_binary, ignorableCoercibility
}

// String implements fmt.Stringer.
func (n *mutationNode) String() string {
	return mutationNames[n.kind] + "(On: " + n.spec.databaseLabel() + ", To: " + strings.Join(n.spec.grantees, ", ") + ")"
}

// databaseLabel renders the spec's database for plan output.
func (s *privilegeSpec) databaseLabel() string {
	if s.database == "" {
		return "<current database>"
	}
	return s.database
}

// RowIter implements sql.ExecSourceRel: it applies the mutation to every
// grantee named, then returns the OK result MySQL returns for GRANT and
// REVOKE.
//
// Grantees are applied one at a time rather than atomically as a group,
// which is MySQL's behaviour too: a multi-grantee GRANT that fails partway
// leaves the grantees already processed changed. Each individual grantee's
// record, latch, and epoch still move as one compare-and-swap.
func (n *mutationNode) RowIter(ctx *sqle.Context, _ sqle.Row) (sqle.RowIter, error) {
	svc, database, err := resolvePrivilegeService(ctx, n.registry, n.spec.database, mutationNames[n.kind])
	if err != nil {
		return nil, err
	}
	apply := mutationAppliers[n.kind]
	for _, grantee := range n.spec.grantees {
		if err := apply(ctx, svc, database, grantee, n.spec.grants); err != nil {
			return nil, err
		}
	}
	return sqle.RowsToRowIter(sqle.NewRow(types.NewOkResult(0))), nil
}

// ---------------------------------------------------------------------------
// SHOW GRANTS
// ---------------------------------------------------------------------------

// showGrantsNode renders one grantee's privilege record as MySQL's SHOW
// GRANTS output.
type showGrantsNode struct {
	registry service.Registry

	// grantee is the canonical grantee to report, empty when the statement
	// named none and the executing session's own identity applies.
	grantee string
}

// Resolved implements sql.Node.
func (n *showGrantsNode) Resolved() bool { return true }

// IsReadOnly implements sql.Node.
func (n *showGrantsNode) IsReadOnly() bool { return true }

// Children implements sql.Node.
func (n *showGrantsNode) Children() []sqle.Node { return nil }

// WithChildren implements sql.Node.
func (n *showGrantsNode) WithChildren(_ *sqle.Context, children ...sqle.Node) (sqle.Node, error) {
	if len(children) != 0 {
		return nil, sqle.ErrInvalidChildrenNumber.New(n, len(children), 0)
	}
	return n, nil
}

// Schema implements sql.Node. MySQL names the single column after the
// grantee, and go-mysql-server's own ShowGrants node does the same, so a
// client reading the column name sees what it would see from MySQL.
//
// The name is resolved from the session when the statement named no grantee,
// which is why this needs the context.
func (n *showGrantsNode) Schema(ctx *sqle.Context) sqle.Schema {
	grantee := n.grantee
	if grantee == "" && ctx != nil {
		grantee = principalOf(ctx.Session).grantee
	}
	return sqle.Schema{{
		Name: "Grants for " + granteeDisplay(grantee),
		Type: types.LongText,
	}}
}

// CollationCoercibility implements sql.CollationCoercible, matching what
// go-mysql-server's own *plan.ShowGrants reports.
func (n *showGrantsNode) CollationCoercibility(_ *sqle.Context) (sqle.CollationID, byte) {
	return sqle.Collation_binary, ignorableCoercibility
}

// String implements fmt.Stringer.
func (n *showGrantsNode) String() string {
	if n.grantee == "" {
		return "ShowGrants(CurrentUser)"
	}
	return "ShowGrants(" + n.grantee + ")"
}

// RowIter implements sql.ExecSourceRel.
func (n *showGrantsNode) RowIter(ctx *sqle.Context, _ sqle.Row) (sqle.RowIter, error) {
	grantee := n.grantee
	if grantee == "" {
		grantee = principalOf(ctx.Session).grantee
		if grantee == "" {
			return nil, &UnidentifiedSessionError{Statement: "SHOW GRANTS"}
		}
	}

	svc, database, err := resolvePrivilegeService(ctx, n.registry, "", "SHOW GRANTS")
	if err != nil {
		return nil, err
	}

	set, err := svc.Privileges(ctx, database, grantee)
	if err != nil {
		return nil, err
	}

	lines := renderGrants(database, grantee, set)
	rows := make([]sqle.Row, len(lines))
	for i, line := range lines {
		rows[i] = sqle.NewRow(line)
	}
	return sqle.RowsToRowIter(rows...), nil
}

// ---------------------------------------------------------------------------
// Shared execution helpers
// ---------------------------------------------------------------------------

// resolvePrivilegeService resolves the privilege catalog for the executing
// session, defaulting an unnamed database to the session's current one.
func resolvePrivilegeService(ctx *sqle.Context, registry service.Registry, database, statement string) (service.PrivilegeServicer, string, error) {
	if registry == nil {
		return nil, "", &CatalogUnavailableError{Statement: statement}
	}
	if database == "" {
		database = ctx.GetCurrentDatabase()
	}
	if database == "" {
		return nil, "", &NoCurrentDatabaseError{Statement: statement}
	}
	svc, err := registry.PrivilegeService(principalOf(ctx.Session).tenant, database)
	if err != nil {
		return nil, "", err
	}
	return svc, database, nil
}

// granteeDisplay renders a canonical grantee in MySQL's quoted account
// notation, which is the form SHOW GRANTS emits and GRANT accepts back.
func granteeDisplay(grantee string) string {
	return "`" + granteeName(grantee) + "`@`" + anyHost + "`"
}

// renderGrants produces MySQL's SHOW GRANTS output for one record.
//
// The lines are valid GRANT statements, in MySQL's own order: the mandatory
// USAGE line first, then the database tier, then one line per table merging
// that table's table-tier and column-tier privileges the way MySQL does. A
// line produced here can be fed back to GRANT or, with FROM substituted for
// TO, to REVOKE.
func renderGrants(database, grantee string, set *service.PrivilegeSet) []string {
	account := granteeDisplay(grantee)

	// MySQL always reports USAGE ON *.*, which means "the account exists and
	// holds nothing globally". qrdb has no global tier at all, so the line is
	// always exactly this.
	lines := []string{"GRANT USAGE ON *.* TO " + account}

	if set == nil {
		return lines
	}

	if set.OnDatabase != 0 {
		lines = append(lines, "GRANT "+set.OnDatabase.String()+" ON "+quoteIdent(database)+".* TO "+
			account+grantOptionSuffix(set.OnDatabase))
	}

	for _, table := range set.Tables() {
		privs := renderTablePrivileges(set, table)
		if privs == "" {
			continue
		}
		lines = append(lines, "GRANT "+privs+" ON "+quoteIdent(database)+"."+quoteIdent(table)+" TO "+
			account+grantOptionSuffix(set.OnTables[table]))
	}

	return lines
}

// renderTablePrivileges renders one table's table-tier and column-tier
// privileges as a single MySQL privilege list -- "SELECT (`id`, `name`),
// INSERT" -- with column-qualified entries first, as MySQL emits them.
//
// A table on which only GRANT OPTION is held renders as USAGE, which is how
// MySQL reports an account that may delegate on a table it holds nothing else
// on; the WITH GRANT OPTION clause is appended by the caller. It returns the
// empty string only when the table carries nothing at all, which the caller
// takes as "emit no line".
func renderTablePrivileges(set *service.PrivilegeSet, table string) string {
	var parts []string

	// Column tier: group the columns by the privilege they carry so that
	// SELECT on two columns renders as one entry, matching MySQL.
	byPrivilege := make(map[service.PrivilegeMask][]string)
	for _, column := range set.Columns(table) {
		mask := set.OnColumns[table][column] &^ set.OnTables[table]
		if mask == 0 {
			continue
		}
		byPrivilege[mask] = append(byPrivilege[mask], column)
	}
	for _, mask := range sortedMasks(byPrivilege) {
		for _, name := range mask.Names() {
			parts = append(parts, name+" ("+quoteIdents(byPrivilege[mask])+")")
		}
	}

	tableMask := set.OnTables[table] &^ service.PrivilegeGrantOption
	if tableMask != 0 {
		parts = append(parts, tableMask.String())
	}

	if len(parts) == 0 && set.OnTables[table].HasAny(service.PrivilegeGrantOption) {
		return "USAGE"
	}
	return strings.Join(parts, ", ")
}

// sortedMasks returns the keys of byPrivilege in ascending bit order, so
// SHOW GRANTS output is stable across runs rather than following Go's
// randomised map iteration.
func sortedMasks(byPrivilege map[service.PrivilegeMask][]string) []service.PrivilegeMask {
	masks := make([]service.PrivilegeMask, 0, len(byPrivilege))
	for mask := range byPrivilege {
		masks = append(masks, mask)
	}
	sort.Slice(masks, func(i, j int) bool { return masks[i] < masks[j] })
	return masks
}

// grantOptionSuffix returns MySQL's WITH GRANT OPTION clause when the mask
// carries it.
func grantOptionSuffix(mask service.PrivilegeMask) string {
	if mask.HasAny(service.PrivilegeGrantOption) {
		return " WITH GRANT OPTION"
	}
	return ""
}

// quoteIdent renders an identifier in MySQL backticks, doubling any backtick
// it contains exactly as MySQL does.
func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// quoteIdents renders a comma-separated backtick-quoted identifier list.
func quoteIdents(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quoteIdent(name)
	}
	return strings.Join(quoted, ", ")
}
