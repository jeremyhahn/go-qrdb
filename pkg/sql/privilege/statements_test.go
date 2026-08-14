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

package privilege_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/sql/privilege"
)

// grantLines runs a SHOW GRANTS statement and returns its rendered lines.
func grantLines(t *testing.T, h *benchHarness, statement string) []string {
	t.Helper()
	rows, err := h.query(t, statement)
	require.NoError(t, err)

	lines := make([]string, len(rows))
	for i, row := range rows {
		require.Len(t, row, 1)
		line, ok := row[0].(string)
		require.Truef(t, ok, "SHOW GRANTS returned %T, want string", row[0])
		lines[i] = line
	}
	return lines
}

// exec runs a statement and drains it, requiring success.
func exec(t *testing.T, h *benchHarness, statement string) {
	t.Helper()
	_, err := h.query(t, statement)
	require.NoErrorf(t, err, "statement failed: %s", statement)
}

// ---------------------------------------------------------------------------
// The pre-existing defect this work also fixes
// ---------------------------------------------------------------------------

// TestPrivilegeStatementsNoLongerFailOnTheMySQLSchema pins the fix for a
// user-visible defect that predates privileges entirely: every GRANT, REVOKE,
// and SHOW GRANTS failed with "database not found: mysql", naming a database
// the user never mentioned, because go-mysql-server's plan builder resolves
// that schema unconditionally and qrdb's provider did not serve it.
func TestPrivilegeStatementsNoLongerFailOnTheMySQLSchema(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)

	for _, statement := range []string{
		"GRANT SELECT ON privdb.users TO 'bob'@'%'",
		"REVOKE SELECT ON privdb.users FROM 'bob'@'%'",
		"SHOW GRANTS FOR 'bob'@'%'",
		"SHOW GRANTS",
	} {
		_, err := h.query(t, statement)
		require.NoErrorf(t, err, "statement failed: %s", statement)
	}
}

// ---------------------------------------------------------------------------
// Round trip
// ---------------------------------------------------------------------------

// TestGrantShowRevokeRoundTrip drives the whole administration surface as real
// SQL and asserts that what SHOW GRANTS reports can be fed straight back to
// REVOKE.
func TestGrantShowRevokeRoundTrip(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)

	exec(t, h, "GRANT SELECT, INSERT ON privdb.* TO 'bob'@'%'")
	exec(t, h, "GRANT UPDATE ON privdb.users TO 'bob'@'%' WITH GRANT OPTION")
	exec(t, h, "GRANT SELECT (id, name) ON privdb.users TO 'bob'@'%'")

	lines := grantLines(t, h, "SHOW GRANTS FOR 'bob'@'%'")
	assert.Equal(t, []string{
		"GRANT USAGE ON *.* TO `bob`@`%`",
		"GRANT SELECT, INSERT ON `privdb`.* TO `bob`@`%`",
		"GRANT SELECT (`id`, `name`), UPDATE ON `privdb`.`users` TO `bob`@`%` WITH GRANT OPTION",
	}, lines, "column-qualified privileges are reported first, merged into their table's line")

	// Feed every reported grant back as a REVOKE.
	for _, line := range lines {
		if strings.Contains(line, "ON *.*") {
			// The USAGE line reports the account's existence rather than a
			// privilege; MySQL cannot revoke it either.
			continue
		}
		object := strings.TrimSuffix(line, " WITH GRANT OPTION")
		revoke := strings.Replace(strings.Replace(object, "GRANT ", "REVOKE ", 1), " TO ", " FROM ", 1)
		exec(t, h, revoke)

		// WITH GRANT OPTION is a clause on GRANT and a named privilege on
		// REVOKE, which is the one asymmetry in MySQL's own grammar.
		if strings.HasSuffix(line, " WITH GRANT OPTION") {
			on := object[strings.Index(object, " ON "):]
			exec(t, h, "REVOKE GRANT OPTION"+strings.Replace(on, " TO ", " FROM ", 1))
		}
	}

	assert.Equal(t, []string{"GRANT USAGE ON *.* TO `bob`@`%`"},
		grantLines(t, h, "SHOW GRANTS FOR 'bob'@'%'"),
		"revoking everything SHOW GRANTS reported must leave nothing behind")
}

// TestShowGrantsForCurrentUserUsesTheSessionIdentity pins that the no-FOR form
// reports the session's authenticated identity, not the wire client's user and
// network address, which is what go-mysql-server's own plan node carries.
func TestShowGrantsForCurrentUserUsesTheSessionIdentity(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	exec(t, h, "GRANT SELECT ON privdb.users TO 'operator'@'%'")

	governed := h.as(benchTenantIdentity)
	lines := grantLines(t, governed, "SHOW GRANTS")
	assert.Equal(t, []string{"GRANT USAGE ON *.* TO `alice`@`%`"}, lines)

	operator := h.as(benchSystemIdentity)
	lines = grantLines(t, operator, "SHOW GRANTS")
	assert.Contains(t, lines, "GRANT SELECT ON `privdb`.`users` TO `operator`@`%`")
}

// TestGrantOnUnqualifiedTableUsesTheSessionDatabase pins that a statement
// naming no database resolves it at execution time from the session.
func TestGrantOnUnqualifiedTableUsesTheSessionDatabase(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	exec(t, h, "GRANT SELECT ON users TO 'bob'@'%'")

	assert.Contains(t, grantLines(t, h, "SHOW GRANTS FOR 'bob'@'%'"),
		"GRANT SELECT ON `privdb`.`users` TO `bob`@`%`")
}

// TestGrantToMultipleGrantees pins that one statement addressing several
// accounts writes a record for each.
func TestGrantToMultipleGrantees(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	exec(t, h, "GRANT SELECT ON privdb.users TO 'bob'@'%', 'carol'@'%'")

	for _, grantee := range []string{"bob", "carol"} {
		assert.Contains(t, grantLines(t, h, "SHOW GRANTS FOR '"+grantee+"'@'%'"),
			"GRANT SELECT ON `privdb`.`users` TO `"+grantee+"`@`%`")
	}
}

// TestGrantArmsTheLatchForEverySession is the posture change made visible
// through SQL alone: before the first GRANT the database is unrestricted, and
// after it every non-exempt session is default-deny.
func TestGrantArmsTheLatchForEverySession(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	governed := h.as(benchTenantIdentity)

	_, err := governed.query(t, "SELECT id FROM users")
	require.NoError(t, err, "an unlatched database is unrestricted")

	exec(t, h, "GRANT SELECT ON privdb.users TO 'somebody-else'@'%'")

	_, err = governed.query(t, "SELECT id FROM users")
	requireAccessDenied(t, err)
}

// TestRevokeDoesNotDisarmTheLatch is the monotonicity guarantee, asserted
// through SQL: revoking the only grant a database ever had leaves it armed.
func TestRevokeDoesNotDisarmTheLatch(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	governed := h.as(benchTenantIdentity)

	exec(t, h, "GRANT SELECT ON privdb.users TO 'somebody-else'@'%'")
	exec(t, h, "REVOKE SELECT ON privdb.users FROM 'somebody-else'@'%'")

	_, err := governed.query(t, "SELECT id FROM users")
	requireAccessDenied(t, err)
}

// TestGrantRequiresGrantOptionOnALatchedDatabase pins that administering
// privileges is itself a privilege once the catalog is armed.
func TestGrantRequiresGrantOptionOnALatchedDatabase(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	governed := h.as(benchTenantIdentity)

	exec(t, h, "GRANT ALL PRIVILEGES ON privdb.* TO 'alice'@'%'")

	_, err := governed.query(t, "GRANT SELECT ON privdb.users TO 'mallory'@'%'")
	requireAccessDenied(t, err)

	exec(t, h, "GRANT SELECT ON privdb.* TO 'alice'@'%' WITH GRANT OPTION")
	exec(t, governed, "GRANT SELECT ON privdb.users TO 'mallory'@'%'")
}

// ---------------------------------------------------------------------------
// Refusals, as real SQL
// ---------------------------------------------------------------------------

// TestUnsupportedGrantFormsAreRefusedByName pins that every GRANT form this
// plane cannot enforce is rejected, and that the rejection names it. Silently
// accepting a grant that is never consulted would be strictly worse.
func TestUnsupportedGrantFormsAreRefusedByName(t *testing.T) {
	tests := []struct {
		name      string
		statement string
		wantErr   any
		wantText  string
	}{
		{
			name:      "server-wide level",
			statement: "GRANT SELECT ON *.* TO 'bob'@'%'",
			wantErr:   new(*privilege.GlobalPrivilegeLevelError),
			wantText:  "*.*",
		},
		{
			name:      "server-wide privilege",
			statement: "GRANT SUPER ON privdb.* TO 'bob'@'%'",
			wantErr:   new(*privilege.UnsupportedPrivilegeError),
			wantText:  "SUPER",
		},
		{
			name:      "routine object type",
			statement: "GRANT SELECT ON PROCEDURE privdb.p TO 'bob'@'%'",
			wantErr:   new(*privilege.UnsupportedObjectTypeError),
			wantText:  "PROCEDURE",
		},
		{
			name:      "host-qualified grantee",
			statement: "GRANT SELECT ON privdb.users TO 'bob'@'localhost'",
			wantErr:   new(*privilege.UnsupportedGranteeHostError),
			wantText:  "localhost",
		},
		{
			name:      "revoke at the server-wide level",
			statement: "REVOKE ALL PRIVILEGES, GRANT OPTION FROM 'bob'@'%'",
			wantErr:   new(*privilege.GlobalPrivilegeLevelError),
			wantText:  "*.*",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newBenchHarness(t, benchSystemIdentity)

			_, err := h.query(t, tc.statement)
			require.Error(t, err)
			assert.Truef(t, errors.As(err, tc.wantErr), "unexpected error type %T: %v", err, err)
			assert.Contains(t, err.Error(), tc.wantText,
				"the refusal must name what it refused")

			// Nothing may have been written: a refused statement must not
			// arm the database's activation latch.
			assert.Equal(t, []string{"GRANT USAGE ON *.* TO `bob`@`%`"},
				grantLines(t, h, "SHOW GRANTS FOR 'bob'@'%'"))
		})
	}
}

// TestGrantUsageArmsWithoutGranting pins MySQL's "no privileges" spelling:
// it creates the account's record and arms the database, and grants nothing.
func TestGrantUsageArmsWithoutGranting(t *testing.T) {
	h := newBenchHarness(t, benchSystemIdentity)
	governed := h.as(benchTenantIdentity)

	exec(t, h, "GRANT USAGE ON privdb.* TO 'alice'@'%'")

	assert.Equal(t, []string{"GRANT USAGE ON *.* TO `alice`@`%`"},
		grantLines(t, h, "SHOW GRANTS FOR 'alice'@'%'"))

	_, err := governed.query(t, "SELECT id FROM users")
	requireAccessDenied(t, err)
}
