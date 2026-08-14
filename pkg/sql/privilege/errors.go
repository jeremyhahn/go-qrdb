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
	"strconv"

	"github.com/dolthub/vitess/go/mysql"
)

// MySQL wire error numbers for a refused statement.
//
// Vitess only defines ERAccessDeniedError (1045), which is the connection-time
// code. A statement refused for lack of a privilege on an object uses the
// object-level codes, which is what a MySQL client -- and therefore
// go-sql-driver/mysql's *mysql.MySQLError, and qrdb's own
// driver.SQLStateError -- expects to classify the failure by.
const (
	// ErrCodeDatabaseAccessDenied is ER_DBACCESS_DENIED_ERROR.
	ErrCodeDatabaseAccessDenied = 1044

	// ErrCodeTableAccessDenied is ER_TABLEACCESS_DENIED_ERROR.
	ErrCodeTableAccessDenied = 1142

	// ErrCodeColumnAccessDenied is ER_COLUMNACCESS_DENIED_ERROR.
	ErrCodeColumnAccessDenied = 1143

	// sqlStateAccessViolation is SQLSTATE 42000, which MySQL reports with
	// all three codes above.
	sqlStateAccessViolation = "42000"
)

// errDatabaseAccessDenied builds the MySQL error a statement refused at the
// database level returns.
//
// It returns *mysql.SQLError rather than a qrdb error type because that is
// the only shape sql.CastSQLError forwards verbatim: any other error, however
// well typed, reaches the client as the generic 1105 and loses the code a
// caller classifies on. The message follows MySQL's own wording so that a
// client comparing text sees what it would see from MySQL.
func errDatabaseAccessDenied(grantee, database string) *mysql.SQLError {
	return mysql.NewSQLError(ErrCodeDatabaseAccessDenied, sqlStateAccessViolation,
		"Access denied for user %s to database %s",
		strconv.Quote(grantee), strconv.Quote(database))
}

// errTableAccessDenied builds the MySQL error a statement refused at the
// table level returns. operation names the SQL command, matching MySQL's
// "SELECT command denied to user ..." phrasing.
func errTableAccessDenied(operation, grantee, table string) *mysql.SQLError {
	return mysql.NewSQLError(ErrCodeTableAccessDenied, sqlStateAccessViolation,
		"%s command denied to user %s for table %s",
		operation, strconv.Quote(grantee), strconv.Quote(table))
}

// errColumnAccessDenied builds the MySQL error a statement refused at the
// column level returns.
func errColumnAccessDenied(operation, grantee, table, column string) *mysql.SQLError {
	return mysql.NewSQLError(ErrCodeColumnAccessDenied, sqlStateAccessViolation,
		"%s command denied to user %s for column %s in table %s",
		operation, strconv.Quote(grantee), strconv.Quote(column), strconv.Quote(table))
}

// ---------------------------------------------------------------------------
// Statement-shape refusals
//
// These are qrdb typed errors, not MySQL wire errors: they report that a
// GRANT or REVOKE names something this privilege plane cannot represent, and
// therefore would never enforce. Refusing is the whole point -- accepting a
// grant that is silently never checked is strictly worse than rejecting it --
// so each one names exactly what it refused.
// ---------------------------------------------------------------------------

// GlobalPrivilegeLevelError is returned for a GRANT or REVOKE at the ON *.*
// level. The privilege catalog and the activation latch that arms it are
// per-database by construction, so a server-wide grant has no home here;
// server-wide authority is granted through the service RBAC control plane.
type GlobalPrivilegeLevelError struct {
	Statement string
}

// Error returns a human-readable description.
func (e *GlobalPrivilegeLevelError) Error() string {
	return "privilege: " + e.Statement + " ON *.* is not supported: qrdb's SQL privileges are per-database; " +
		"server-wide authority is granted through service roles, not SQL"
}

// UnsupportedPrivilegeError is returned when a GRANT or REVOKE names a
// privilege the per-database data plane cannot represent -- a server-wide
// privilege such as SUPER or PROCESS, a routine privilege, or a dynamic
// privilege.
type UnsupportedPrivilegeError struct {
	Privilege string
}

// Error returns a human-readable description.
func (e *UnsupportedPrivilegeError) Error() string {
	return "privilege: unsupported privilege " + strconv.Quote(e.Privilege) +
		": qrdb's SQL privileges cover table and column access only"
}

// UnsupportedObjectTypeError is returned when a GRANT or REVOKE names an
// object kind other than a table -- ON FUNCTION or ON PROCEDURE. qrdb has no
// stored routines, so there is nothing for such a grant to protect.
type UnsupportedObjectTypeError struct {
	ObjectType string
}

// Error returns a human-readable description.
func (e *UnsupportedObjectTypeError) Error() string {
	return "privilege: unsupported object type " + e.ObjectType +
		": qrdb's SQL privileges cover tables only"
}

// UnsupportedGranteeHostError is returned when a grantee names a host other
// than the any-host wildcard. qrdb identities are authenticated by
// certificate subject and carry no host component, so a host-qualified grant
// could never match a session and would be silently inert.
type UnsupportedGranteeHostError struct {
	Grantee string
	Host    string
}

// Error returns a human-readable description.
func (e *UnsupportedGranteeHostError) Error() string {
	return "privilege: unsupported grantee host " + strconv.Quote(e.Host) + " for " + strconv.Quote(e.Grantee) +
		": qrdb identities carry no host, so only the any-host form ('user'@'%') can be enforced"
}

// UnsupportedGrantClauseError is returned when a GRANT carries a clause this
// plane cannot honour -- today only AS <user> (user assumption).
type UnsupportedGrantClauseError struct {
	Clause string
}

// Error returns a human-readable description.
func (e *UnsupportedGrantClauseError) Error() string {
	return "privilege: unsupported GRANT clause " + e.Clause
}

// NoCurrentDatabaseError is returned when a privilege statement names no
// database and the session has none selected.
type NoCurrentDatabaseError struct {
	Statement string
}

// Error returns a human-readable description.
func (e *NoCurrentDatabaseError) Error() string {
	return "privilege: " + e.Statement + " requires a database: none was named and none is selected"
}

// CatalogUnavailableError is returned when a privilege statement reaches an
// engine that has no service.Registry behind it, and therefore no privilege
// catalog to write to. It is the same condition that makes CREATE TABLE
// unavailable on the KV-only embedded path.
type CatalogUnavailableError struct {
	Statement string
}

// Error returns a human-readable description.
func (e *CatalogUnavailableError) Error() string {
	return "privilege: " + e.Statement + " requires a relational catalog: this engine has no service registry"
}

// UnidentifiedSessionError is returned when a privilege statement is issued
// by a session that carries no authenticated identity, in a context where the
// grantee must be derived from one (SHOW GRANTS for the current user).
type UnidentifiedSessionError struct {
	Statement string
}

// Error returns a human-readable description.
func (e *UnidentifiedSessionError) Error() string {
	return "privilege: " + e.Statement + " requires an authenticated session identity"
}
