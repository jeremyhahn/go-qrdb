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
	ast "github.com/dolthub/vitess/go/vt/sqlparser"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
)

// ---------------------------------------------------------------------------
// Statement dispatch
//
// go-mysql-server hands every statement's AuthInformation to HandleAuth. The
// pair (AuthType, TargetType) describes what the statement wants and what it
// wants it on; these two tables turn that pair into a check.
//
// Both tables are closed: an AuthType or TargetType with no entry is DENIED
// on a latched database. That is the whole point of routing enforcement
// through the engine's own question rather than through a list of statements
// maintained here -- a statement kind nobody enumerated cannot slip through
// unchecked, it fails closed until somebody decides what it should require.
// ---------------------------------------------------------------------------

// authRule enforces one AuthType.
type authRule func(ctx *sqle.Context, state *queryState, auth ast.AuthInformation) error

// authRules maps every authorization type this plane models to its rule.
//
// The types absent from this table are absent deliberately: SUPER, PROCESS,
// RELOAD, FILE, SHUTDOWN, REPLICATION, CREATE/ALTER USER, CREATE/DROP ROLE,
// GRANT/REVOKE ROLE and PROXY, stored-routine and trigger and event and view
// privileges, LOCK TABLES, and CREATE TEMPORARY TABLES. None of them can be
// GRANTed here (see convertPrivileges), so no record could ever satisfy them,
// and permitting them would mean permitting them unconditionally.
var authRules = map[string]authRule{
	// The statement's authorization is handled by a parent or child node.
	ast.AuthType_IGNORE: permitStatement,

	// SHOW is a placeholder in the parser: it covers SHOW TABLES, SHOW
	// DATABASES, SHOW CREATE TABLE and friends, which carry no target and
	// expose only metadata about objects the session can already name. The
	// table and database reachability checks (CheckTable, CheckDatabase)
	// are where those are governed.
	ast.AuthType_SHOW: permitStatement,

	ast.AuthType_SELECT:      requirePrivileges("SELECT", service.PrivilegeSelect),
	ast.AuthType_INSERT:      requirePrivileges("INSERT", service.PrivilegeInsert),
	ast.AuthType_UPDATE:      requirePrivileges("UPDATE", service.PrivilegeUpdate),
	ast.AuthType_DELETE:      requirePrivileges("DELETE", service.PrivilegeDelete),
	ast.AuthType_REPLACE:     requirePrivileges("REPLACE", service.PrivilegeInsert|service.PrivilegeDelete),
	ast.AuthType_CREATE:      requirePrivileges("CREATE", service.PrivilegeCreate),
	ast.AuthType_DROP:        requirePrivileges("DROP", service.PrivilegeDrop),
	ast.AuthType_ALTER:       requirePrivileges("ALTER", service.PrivilegeAlter),
	ast.AuthType_INDEX:       requirePrivileges("INDEX", service.PrivilegeIndex),
	ast.AuthType_FOREIGN_KEY: requirePrivileges("REFERENCES", service.PrivilegeReferences),

	// RENAME names its tables in groups of four rather than through a
	// TargetType, so it carries its own rule.
	ast.AuthType_RENAME: renameStatement,

	// VISIBLE asks whether the session may see the named databases at all.
	ast.AuthType_VISIBLE: visibleStatement,

	// Administering privileges requires holding GRANT OPTION at the
	// DATABASE tier of the database being administered.
	//
	// The database tier specifically, and not the table tier, because the
	// authorization information the parser attaches to GRANT and REVOKE
	// names no target at all (TargetType IGNORE, no names) -- the objects
	// are in the statement's own plan node, which HandleAuth never sees.
	// With nothing to check against, the only tier that can be checked is
	// the whole database. That is stricter than MySQL, which would let the
	// holder of a table-level GRANT OPTION re-grant that one table; erring
	// toward the stricter reading is the right direction for a check that
	// cannot see its target.
	//
	// A database with no catalog is unrestricted, so the FIRST grant in a
	// database can always be issued: that is how a catalog is bootstrapped
	// from inside its own tenant. The system tenant is additionally exempt
	// from every check (see principal.exempt).
	ast.AuthType_GRANT_PRIVILEGE:  requirePrivileges("GRANT", service.PrivilegeGrantOption),
	ast.AuthType_REVOKE_PRIVILEGE: requirePrivileges("REVOKE", service.PrivilegeGrantOption),
	ast.AuthType_REVOKE_ALL:       requirePrivileges("REVOKE", service.PrivilegeGrantOption),
}

// permitStatement allows the statement outright.
func permitStatement(_ *sqle.Context, _ *queryState, _ ast.AuthInformation) error { return nil }

// requirePrivileges builds the rule for an authorization type that demands
// want at whatever target the AuthInformation names. operation labels the
// statement in the refusal message, matching MySQL's
// "<OPERATION> command denied to user ..." wording.
func requirePrivileges(operation string, want service.PrivilegeMask) authRule {
	return func(ctx *sqle.Context, state *queryState, auth ast.AuthInformation) error {
		target, ok := targetRules[auth.TargetType]
		if !ok {
			return errDatabaseAccessDenied(state.p.grantee, state.database)
		}
		return target(ctx, state, auth, operation, want)
	}
}

// ---------------------------------------------------------------------------
// Target dispatch
// ---------------------------------------------------------------------------

// targetRule enforces want against the objects one TargetType names.
type targetRule func(ctx *sqle.Context, state *queryState, auth ast.AuthInformation, operation string, want service.PrivilegeMask) error

// targetRules maps every target shape this plane models to its check.
var targetRules = map[string]targetRule{
	// The authorization type named no target, so the requirement applies to
	// the session's current database as a whole.
	ast.AuthTargetType_Ignore: targetSessionDatabase,

	// A server-level target. The database lifecycle (CREATE DATABASE, DROP
	// DATABASE) arrives here, and it belongs to the service RBAC control
	// plane, not to this one -- see the package doc comment on the seam
	// between the two. Permitting it here defers to RBAC rather than
	// double-gating it with a grant that cannot be written.
	ast.AuthTargetType_Global: permitTarget,

	// A target the parser has not classified yet. go-mysql-server treats it
	// as unclassified rather than as unchecked, and so does this: the
	// statements that reach it (SHOW ...) expose no row data.
	ast.AuthTargetType_TODO: permitTarget,

	ast.AuthTargetType_DatabaseIdentifiers:      targetDatabases,
	ast.AuthTargetType_SingleTableIdentifier:    targetSingleTable,
	ast.AuthTargetType_MultipleTableIdentifiers: targetMultipleTables,
	ast.AuthTargetType_TableColumn:              targetTableColumn,
}

// permitTarget allows the statement outright.
func permitTarget(_ *sqle.Context, _ *queryState, _ ast.AuthInformation, _ string, _ service.PrivilegeMask) error {
	return nil
}

// targetSessionDatabase checks want at the session database's database tier.
func targetSessionDatabase(ctx *sqle.Context, state *queryState, _ ast.AuthInformation, _ string, want service.PrivilegeMask) error {
	return checkDatabaseTier(ctx, state, "", want)
}

// targetDatabases checks want at the database tier of each named database.
//
// An empty name list means the session's current database: CREATE TABLE
// arrives exactly this way (AuthType CREATE, target DB_IDENTS, no names),
// because the database it creates the table in is the session's. Treating an
// empty list as "nothing to check" would leave CREATE TABLE unenforced.
func targetDatabases(ctx *sqle.Context, state *queryState, auth ast.AuthInformation, _ string, want service.PrivilegeMask) error {
	if len(auth.TargetNames) == 0 {
		return checkDatabaseTier(ctx, state, "", want)
	}
	for _, dbName := range auth.TargetNames {
		if err := checkDatabaseTier(ctx, state, dbName, want); err != nil {
			return err
		}
	}
	return nil
}

// targetSingleTable checks want on the one (database, table) pair named.
func targetSingleTable(ctx *sqle.Context, state *queryState, auth ast.AuthInformation, operation string, want service.PrivilegeMask) error {
	if len(auth.TargetNames) < 2 {
		return errDatabaseAccessDenied(state.p.grantee, state.database)
	}
	return checkTableTier(ctx, state, auth.TargetNames[0], auth.TargetNames[1], operation, want)
}

// targetMultipleTables checks want on each (database, table) pair named.
func targetMultipleTables(ctx *sqle.Context, state *queryState, auth ast.AuthInformation, operation string, want service.PrivilegeMask) error {
	if len(auth.TargetNames)%2 != 0 {
		return errDatabaseAccessDenied(state.p.grantee, state.database)
	}
	for i := 0; i < len(auth.TargetNames); i += 2 {
		if err := checkTableTier(ctx, state, auth.TargetNames[i], auth.TargetNames[i+1], operation, want); err != nil {
			return err
		}
	}
	return nil
}

// targetTableColumn checks want on the one (database, table, column) triple
// named.
func targetTableColumn(ctx *sqle.Context, state *queryState, auth ast.AuthInformation, operation string, want service.PrivilegeMask) error {
	if len(auth.TargetNames) < 3 {
		return errDatabaseAccessDenied(state.p.grantee, state.database)
	}
	dbName, tableName, columnName := auth.TargetNames[0], auth.TargetNames[1], auth.TargetNames[2]

	snap, err := state.snapshotFor(ctx, dbName)
	if err != nil {
		return err
	}
	if snap.permitsColumn(tableName, columnName, want) {
		return nil
	}
	return errColumnAccessDenied(operation, state.p.grantee, tableName, columnName)
}

// renameStatement enforces RENAME TABLE, whose targets arrive in groups of
// four: source database, source table, target database, target table. MySQL
// requires ALTER and DROP on the source and CREATE and INSERT on the target.
func renameStatement(ctx *sqle.Context, state *queryState, auth ast.AuthInformation) error {
	if len(auth.TargetNames)%4 != 0 {
		return errDatabaseAccessDenied(state.p.grantee, state.database)
	}
	for i := 0; i < len(auth.TargetNames); i += 4 {
		if err := checkTableTier(ctx, state, auth.TargetNames[i], auth.TargetNames[i+1],
			"ALTER", service.PrivilegeAlter|service.PrivilegeDrop); err != nil {
			return err
		}
		if err := checkTableTier(ctx, state, auth.TargetNames[i+2], auth.TargetNames[i+3],
			"CREATE", service.PrivilegeCreate|service.PrivilegeInsert); err != nil {
			return err
		}
	}
	return nil
}

// visibleStatement enforces the database-visibility question, which asks
// only whether the session holds anything at all in the named databases.
func visibleStatement(ctx *sqle.Context, state *queryState, auth ast.AuthInformation) error {
	if auth.TargetType != ast.AuthTargetType_DatabaseIdentifiers {
		return nil
	}
	for _, dbName := range auth.TargetNames {
		if err := checkDatabaseReachable(ctx, state, dbName); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Checks
// ---------------------------------------------------------------------------

// checkDatabaseTier refuses unless the grantee holds want at the database
// tier of dbName, which is the tier that covers every table in the database
// including tables that do not exist yet.
//
// The refusal is reported as ER_DBACCESS_DENIED_ERROR rather than as the
// table-level code because a statement that reaches this check named no
// table: CREATE TABLE, for one, arrives with its database implied and its
// table name nowhere in the authorization information.
func checkDatabaseTier(ctx *sqle.Context, state *queryState, dbName string, want service.PrivilegeMask) error {
	if isReservedSchema(dbName) {
		return nil
	}
	snap, err := state.snapshotFor(ctx, dbName)
	if err != nil {
		return err
	}
	if snap.permitsDatabase(want) {
		return nil
	}
	return errDatabaseAccessDenied(state.p.grantee, effectiveDatabase(state, dbName))
}

// checkTableTier refuses unless the grantee holds want on the named table,
// counting the database tier as covering every table in it.
func checkTableTier(ctx *sqle.Context, state *queryState, dbName, tableName, operation string, want service.PrivilegeMask) error {
	if isReservedSchema(dbName) {
		return nil
	}
	snap, err := state.snapshotFor(ctx, dbName)
	if err != nil {
		return err
	}
	if snap.permits(tableName, want) {
		return nil
	}
	return errTableAccessDenied(operation, state.p.grantee, tableName)
}
