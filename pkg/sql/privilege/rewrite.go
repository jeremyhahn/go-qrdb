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
	sqle "github.com/dolthub/go-mysql-server/sql"
	"github.com/dolthub/go-mysql-server/sql/analyzer"
	"github.com/dolthub/go-mysql-server/sql/plan"
	"github.com/dolthub/go-mysql-server/sql/transform"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// RewriteRuleID identifies the post-analyze rule this package contributes.
//
// go-mysql-server's own rule identifiers are a dense iota block
// (sql/analyzer/rule_ids.go); an integrator's rule needs an identifier
// outside it. The value is only ever compared for equality by the rule
// selectors and printed by RuleId.String, which already renders out-of-range
// identifiers as "RuleId(N)", so a deliberately distant constant is both safe
// and obviously not one of theirs.
const RewriteRuleID analyzer.RuleId = 1 << 16

// RewriteRuleName labels the rule in analyzer debug output.
const RewriteRuleName = "qrdbRewritePrivilegeStatements"

// Rewrite is the analyzer post-analyze rule that replaces go-mysql-server's
// privilege plan nodes with qrdb's, before execution sees them.
//
// # Why a rewrite, and why post-analyze
//
// GRANT, REVOKE, and SHOW GRANTS are real MySQL statements: qrdb serves them
// through the real parser and the real plan builder, so the syntax accepted
// is MySQL's own rather than a qrdb reinterpretation of it. What qrdb does
// not have is go-mysql-server's mysql_db grant tables — privileges live in
// the per-database privilege catalog instead.
//
// The two are joined here. The plan builder unconditionally resolves the
// `mysql` schema while building each of these statements
// (sql/planbuilder/priv.go), which pkg/sql/provider serves as an empty
// stand-in; the execution builder then hard-asserts that schema to
// *mysql_db.MySQLDb and requires a matching (user, host) row to already exist
// (sql/rowexec/priv.go). Post-analyze is the last point before execution and
// the first at which the whole statement is available, so the node is
// replaced here and neither of those execution-time assumptions is ever
// reached.
//
// Rewriting rather than intercepting at the parser is what gives BOTH real
// MySQL syntax and a single source of truth: there is exactly one privilege
// store, and exactly one grammar reaching it.
func Rewrite(ctx *sqle.Context, a *analyzer.Analyzer, n sqle.Node, _ *plan.Scope, _ analyzer.RuleSelector, _ *sqle.QueryFlags) (sqle.Node, transform.TreeIdentity, error) {
	registry := registryOf(a.Catalog)
	return transform.Node(ctx, n, func(_ *sqle.Context, node sqle.Node) (sqle.Node, transform.TreeIdentity, error) {
		switch node := node.(type) {
		case *plan.Grant:
			spec, err := grantSpec(node)
			if err != nil {
				return nil, transform.SameTree, err
			}
			return &mutationNode{registry: registry, spec: spec, kind: mutationGrant}, transform.NewTree, nil

		case *plan.Revoke:
			spec, err := revokeSpec(node)
			if err != nil {
				return nil, transform.SameTree, err
			}
			return &mutationNode{registry: registry, spec: spec, kind: mutationRevoke}, transform.NewTree, nil

		case *plan.ShowGrants:
			grantee, err := showGrantsGrantee(node)
			if err != nil {
				return nil, transform.SameTree, err
			}
			return &showGrantsNode{registry: registry, grantee: grantee}, transform.NewTree, nil

		default:
			return node, transform.SameTree, nil
		}
	})
}

// ---------------------------------------------------------------------------
// Plan conversion
// ---------------------------------------------------------------------------

// privilegeConversions maps go-mysql-server's plan-level privilege types onto
// the bits qrdb's per-database data plane can represent and enforce.
//
// A privilege absent from this table is refused by name rather than dropped.
// Everything absent is either server-wide (SUPER, PROCESS, RELOAD, SHUTDOWN,
// FILE, REPLICATION *, SHOW DATABASES, CREATE USER, CREATE/DROP ROLE,
// CREATE TABLESPACE, LOCK TABLES, CREATE TEMPORARY TABLES), routine- or
// object-kind-specific (EXECUTE, CREATE/ALTER ROUTINE, EVENT, TRIGGER,
// CREATE/SHOW VIEW), or dynamic. None of them names a table or a column, so
// none of them has a home in a per-database table-privilege catalog, and
// accepting one would mean storing a grant that is never consulted.
var privilegeConversions = map[plan.PrivilegeType]service.PrivilegeMask{
	plan.PrivilegeType_All:         service.PrivilegeAll,
	plan.PrivilegeType_Select:      service.PrivilegeSelect,
	plan.PrivilegeType_Insert:      service.PrivilegeInsert,
	plan.PrivilegeType_Update:      service.PrivilegeUpdate,
	plan.PrivilegeType_Delete:      service.PrivilegeDelete,
	plan.PrivilegeType_Create:      service.PrivilegeCreate,
	plan.PrivilegeType_Drop:        service.PrivilegeDrop,
	plan.PrivilegeType_Alter:       service.PrivilegeAlter,
	plan.PrivilegeType_Index:       service.PrivilegeIndex,
	plan.PrivilegeType_References:  service.PrivilegeReferences,
	plan.PrivilegeType_GrantOption: service.PrivilegeGrantOption,

	// USAGE is MySQL's "no privileges" spelling. It is representable
	// precisely because it grants nothing: GRANT USAGE ON db.* TO u arms
	// the database's latch and creates no privileges, which is exactly what
	// MySQL means by it.
	plan.PrivilegeType_Usage: 0,
}

// privilegeNames labels the privileges this plane cannot represent, so that a
// refusal can name the offending word rather than a number. Only the
// unsupported ones need naming; the supported ones never reach the refusal.
var privilegeNames = map[plan.PrivilegeType]string{
	plan.PrivilegeType_AlterRoutine:          "ALTER ROUTINE",
	plan.PrivilegeType_CreateRole:            "CREATE ROLE",
	plan.PrivilegeType_CreateRoutine:         "CREATE ROUTINE",
	plan.PrivilegeType_CreateTablespace:      "CREATE TABLESPACE",
	plan.PrivilegeType_CreateTemporaryTables: "CREATE TEMPORARY TABLES",
	plan.PrivilegeType_CreateUser:            "CREATE USER",
	plan.PrivilegeType_CreateView:            "CREATE VIEW",
	plan.PrivilegeType_DropRole:              "DROP ROLE",
	plan.PrivilegeType_Event:                 "EVENT",
	plan.PrivilegeType_Execute:               "EXECUTE",
	plan.PrivilegeType_File:                  "FILE",
	plan.PrivilegeType_LockTables:            "LOCK TABLES",
	plan.PrivilegeType_Process:               "PROCESS",
	plan.PrivilegeType_Reload:                "RELOAD",
	plan.PrivilegeType_ReplicationClient:     "REPLICATION CLIENT",
	plan.PrivilegeType_ReplicationSlave:      "REPLICATION SLAVE",
	plan.PrivilegeType_ShowDatabases:         "SHOW DATABASES",
	plan.PrivilegeType_ShowView:              "SHOW VIEW",
	plan.PrivilegeType_Shutdown:              "SHUTDOWN",
	plan.PrivilegeType_Super:                 "SUPER",
	plan.PrivilegeType_Trigger:               "TRIGGER",
	plan.PrivilegeType_Dynamic:               "a dynamic privilege",
}

// objectTypeNames labels the object kinds this plane cannot represent.
var objectTypeNames = map[plan.ObjectType]string{
	plan.ObjectType_Function:  "FUNCTION",
	plan.ObjectType_Procedure: "PROCEDURE",
}

// grantSpec converts a *plan.Grant into a validated privilegeSpec, refusing
// every form the catalog cannot hold.
func grantSpec(n *plan.Grant) (*privilegeSpec, error) {
	if n.As != nil {
		return nil, &UnsupportedGrantClauseError{Clause: "AS <user>"}
	}
	spec, err := convertSpec("GRANT", n.PrivilegeLevel, n.ObjectType, n.Privileges, n.Users)
	if err != nil {
		return nil, err
	}
	if n.WithGrantOption {
		spec.grants = append(spec.grants, service.PrivilegeGrant{
			Table:      tableOf(n.PrivilegeLevel),
			Privileges: service.PrivilegeGrantOption,
		})
	}
	return spec, nil
}

// revokeSpec converts a *plan.Revoke into a validated privilegeSpec.
func revokeSpec(n *plan.Revoke) (*privilegeSpec, error) {
	return convertSpec("REVOKE", n.PrivilegeLevel, n.ObjectType, n.Privileges, n.Users)
}

// convertSpec is the single conversion both GRANT and REVOKE go through, so
// that the two can never diverge on which forms they accept.
func convertSpec(statement string, level plan.PrivilegeLevel, objectType plan.ObjectType,
	privileges []plan.Privilege, users []plan.UserName) (*privilegeSpec, error) {
	// ON *.* is the server-wide level. The catalog and its activation latch
	// are per-database by construction, so this cannot be stored, and
	// narrowing it silently to the session's database would quietly turn a
	// server-wide grant into a local one.
	if level.Database == "*" {
		return nil, &GlobalPrivilegeLevelError{Statement: statement}
	}

	if objectType != plan.ObjectType_Any && objectType != plan.ObjectType_Table {
		return nil, &UnsupportedObjectTypeError{ObjectType: objectTypeNames[objectType]}
	}

	table := tableOf(level)

	grants, err := convertPrivileges(table, privileges)
	if err != nil {
		return nil, err
	}

	grantees, err := convertGrantees(users)
	if err != nil {
		return nil, err
	}

	return &privilegeSpec{database: level.Database, grantees: grantees, grants: grants}, nil
}

// tableOf resolves the table a privilege level names, empty for the
// database-wide level (ON db.*).
func tableOf(level plan.PrivilegeLevel) string {
	if level.TableRoutine == "*" {
		return ""
	}
	return level.TableRoutine
}

// convertPrivileges flattens a statement's privilege list into one
// service.PrivilegeGrant per tier: the table (or database) tier for
// unqualified privileges, and one entry per named column for
// column-qualified ones.
func convertPrivileges(table string, privileges []plan.Privilege) ([]service.PrivilegeGrant, error) {
	var tierMask service.PrivilegeMask
	var grants []service.PrivilegeGrant

	for _, priv := range privileges {
		mask, ok := privilegeConversions[priv.Type]
		if !ok {
			name := privilegeNames[priv.Type]
			if priv.Type == plan.PrivilegeType_Dynamic && priv.Dynamic != "" {
				name = priv.Dynamic
			}
			return nil, &UnsupportedPrivilegeError{Privilege: name}
		}

		if len(priv.Columns) == 0 {
			tierMask |= mask
			continue
		}

		// A column-qualified privilege at the database level (ON db.*) has
		// no column to attach to. MySQL rejects it too.
		if table == "" {
			return nil, &UnsupportedPrivilegeError{Privilege: "column-qualified privileges at the database level"}
		}
		for _, column := range priv.Columns {
			grants = append(grants, service.PrivilegeGrant{Table: table, Column: column, Privileges: mask})
		}
	}

	// The tier entry is emitted even when the mask is empty, because that is
	// how GRANT USAGE arms the latch without granting anything.
	return append(grants, service.PrivilegeGrant{Table: table, Privileges: tierMask}), nil
}

// convertGrantees canonicalises the accounts a statement names, refusing any
// that carries a host qrdb could never match.
func convertGrantees(users []plan.UserName) ([]string, error) {
	grantees := make([]string, 0, len(users))
	for i := range users {
		grantee, err := convertGrantee(&users[i])
		if err != nil {
			return nil, err
		}
		grantees = append(grantees, grantee)
	}
	return grantees, nil
}

// convertGrantee canonicalises one account name. Only the any-host form is
// accepted: a qrdb identity is a certificate subject with no host component,
// so 'user'@'localhost' could never match a session and would be a grant
// stored and never enforced.
func convertGrantee(user *plan.UserName) (string, error) {
	if !user.AnyHost && user.Host != "" && user.Host != anyHost {
		return "", &UnsupportedGranteeHostError{Grantee: user.Name, Host: user.Host}
	}
	grantee := canonicalGrantee(user.Name)

	// Validated here, while the statement is still being planned, against
	// the same key-safety rules the catalog itself applies, so a name that
	// could not be stored is refused before anything is written.
	if err := service.ValidateGrantee(grantee); err != nil {
		return "", err
	}
	return grantee, nil
}

// showGrantsGrantee resolves the account a SHOW GRANTS statement reports on,
// returning empty for the current-user form.
//
// The current-user form is deliberately NOT taken from the plan node. The
// plan builder fills it from the wire client's user and network address
// (sql/planbuilder/priv.go), neither of which is qrdb's notion of identity;
// the executing session's authenticated identity is, and the node resolves it
// there.
func showGrantsGrantee(n *plan.ShowGrants) (string, error) {
	if n.CurrentUser || n.For == nil {
		return "", nil
	}
	return convertGrantee(n.For)
}
