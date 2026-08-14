// Copyright 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build qrdb_mysql

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"reflect"
	"strconv"
	"testing"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeremyhahn/go-qrdb/pkg/service"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/internal/sqltest"
	"github.com/jeremyhahn/go-qrdb/pkg/sql/query"
	qrdbdriver "github.com/jeremyhahn/go-qrdb/sdk/go/driver"
)

// ---------------------------------------------------------------------------
// Cross-transport parity for symbolically-represented columns.
//
// go-mysql-server stores an ENUM cell as the uint16 ordinal of its label and
// a SET cell as the uint64 bitmask of its members. On the MySQL wire the
// engine itself resolves those symbols back to labels; every other qrdb
// result path has to do it, and each one used to render the row by switching
// on the Go type of the cell instead of consulting the column type -- so the
// wire returned "active" while the embedded driver and the service-layer SQL
// API returned 2, from the identical row.
//
// This file is the guard against that divergence returning. It is the only
// place in the tree that can hold all three paths against one store at once:
// pkg/sql/mysql already owns the wire server and can reach both the
// pkg/sql/internal/sqltest harness and sdk/go/driver.
// ---------------------------------------------------------------------------

// paritySuite holds one relational catalog exposed simultaneously through
// the MySQL wire protocol, the embedded database/sql driver, and the
// service-layer SQL API, all reading and writing the same MemStore.
type paritySuite struct {
	wire     *sql.DB
	embedded *sql.DB
	service  service.SQLServicer
}

// newParitySuite starts a MySQL wire server over a fresh harness and opens
// an embedded connection and a service-layer SQL service over the very same
// harness, so any difference the tests observe is a difference in rendering
// and never a difference in stored data.
func newParitySuite(t *testing.T) *paritySuite {
	t.Helper()

	h := sqltest.NewHarness()

	srv := NewServer(&mockKVService{}, &mockAdminService{}, "127.0.0.1:0",
		WithDatabaseName("qrdb"),
		WithServiceRegistry(h.Registry),
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	require.NoError(t, srv.Start())
	t.Cleanup(srv.Stop)

	cfg := gomysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = srv.Addr()
	cfg.User = "qrdb"
	cfg.DBName = "qrdb"
	cfg.AllowNativePasswords = true
	cfg.Timeout = 30 * time.Second

	wire, err := sql.Open("mysql", cfg.FormatDSN())
	require.NoError(t, err)
	t.Cleanup(func() { _ = wire.Close() })

	embedded := sql.OpenDB(qrdbdriver.NewEmbeddedConnector(
		&qrdbdriver.DSNConfig{
			Scheme:   qrdbdriver.SchemeEmbedded,
			Database: "qrdb",
			Backend:  "memory",
			Engine:   "kv",
		},
		qrdbdriver.WithRegistry(h.Registry),
	))
	t.Cleanup(func() { _ = embedded.Close() })

	return &paritySuite{
		wire:     wire,
		embedded: embedded,
		service:  query.NewService(h.KVSvc, nil, query.WithRegistry(h.Registry)),
	}
}

// text reads a single-column, single-row result as a string, using the same
// database/sql scan on both transports. Scanning into *sql.NullString is what
// makes the comparison meaningful: the MySQL text protocol delivers every
// cell as bytes and the embedded driver delivers native Go values, so the
// value -- not the client's transport representation of it -- is what gets
// compared.
func text(t *testing.T, db *sql.DB, q string, args ...any) sql.NullString {
	t.Helper()
	var got sql.NullString
	require.NoError(t, db.QueryRow(q, args...).Scan(&got))
	return got
}

// serviceText reads the same single cell through the service-layer SQL API,
// which pre-formats every cell as text for its REST/gRPC/QUIC/MCP callers.
func serviceText(t *testing.T, svc service.SQLServicer, q, column string) string {
	t.Helper()
	res, err := svc.Execute(context.Background(), "qrdb", q)
	require.NoError(t, err)
	require.Len(t, res.Rows, 1)
	v, ok := res.Rows[0][column]
	require.True(t, ok, "column %q missing from result", column)
	s, ok := v.(string)
	require.True(t, ok, "column %q is %T, want string", column, v)
	return s
}

const paritySymbolicDDL = `CREATE TABLE accounts (
	id INT PRIMARY KEY,
	status ENUM('pending','active','banned'),
	perms SET('read','write','admin')
)`

// TestTransportParity_EnumAndSet is the divergence guard proper: for one row
// written once, the MySQL wire, the embedded driver, and the service-layer
// SQL API must all report the same value for every symbolic column.
func TestTransportParity_EnumAndSet(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.wire.Exec(paritySymbolicDDL)
	require.NoError(t, err)
	_, err = s.wire.Exec("INSERT INTO accounts VALUES (1, 'active', 'read,admin')")
	require.NoError(t, err)

	cases := []struct {
		column string
		want   string
	}{
		{"status", "active"},
		{"perms", "read,admin"},
	}

	for _, tc := range cases {
		t.Run(tc.column, func(t *testing.T) {
			q := "SELECT " + tc.column + " FROM accounts WHERE id = 1"

			wireVal := text(t, s.wire, q)
			embeddedVal := text(t, s.embedded, q)
			serviceVal := serviceText(t, s.service, q, tc.column)

			// Anchor the parity on the correct answer, so three
			// transports agreeing on the ordinal cannot pass.
			assert.Equal(t, tc.want, wireVal.String, "mysql wire")
			assert.Equal(t, tc.want, embeddedVal.String, "embedded driver")
			assert.Equal(t, tc.want, serviceVal, "service-layer SQL API")

			assert.Equal(t, wireVal, embeddedVal, "mysql wire and embedded driver diverged")
			assert.Equal(t, wireVal.String, serviceVal, "mysql wire and service-layer SQL API diverged")
		})
	}
}

// TestTransportParity_EnumAndSet_WrittenEmbeddedReadEverywhere writes through
// the embedded driver instead of the wire, proving the parity is a property
// of the stored row rather than of the transport that happened to write it.
func TestTransportParity_EnumAndSet_WrittenEmbeddedReadEverywhere(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.embedded.Exec(paritySymbolicDDL)
	require.NoError(t, err)
	_, err = s.embedded.Exec("INSERT INTO accounts VALUES (?, ?, ?)", 1, "banned", "write")
	require.NoError(t, err)

	const q = "SELECT status, perms FROM accounts WHERE id = 1"

	var wireStatus, wirePerms sql.NullString
	require.NoError(t, s.wire.QueryRow(q).Scan(&wireStatus, &wirePerms))

	var embStatus, embPerms sql.NullString
	require.NoError(t, s.embedded.QueryRow(q).Scan(&embStatus, &embPerms))

	assert.Equal(t, "banned", wireStatus.String)
	assert.Equal(t, "write", wirePerms.String)
	assert.Equal(t, wireStatus, embStatus)
	assert.Equal(t, wirePerms, embPerms)

	assert.Equal(t, "banned", serviceText(t, s.service, "SELECT status FROM accounts WHERE id = 1", "status"))
	assert.Equal(t, "write", serviceText(t, s.service, "SELECT perms FROM accounts WHERE id = 1", "perms"))
}

// TestTransportParity_EnumAndSet_Null asserts the transports agree on NULL
// too. NULL is where a resolution bug is most likely to invent a value:
// ordinal 0 belongs to no ENUM member, and bitmask 0 renders as the empty
// string, which is a legitimate SET value and not the same thing as NULL.
func TestTransportParity_EnumAndSet_Null(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.wire.Exec(paritySymbolicDDL)
	require.NoError(t, err)
	_, err = s.wire.Exec("INSERT INTO accounts VALUES (1, NULL, NULL), (2, 'pending', '')")
	require.NoError(t, err)

	t.Run("null", func(t *testing.T) {
		for _, column := range []string{"status", "perms"} {
			q := "SELECT " + column + " FROM accounts WHERE id = 1"
			wireVal := text(t, s.wire, q)
			embeddedVal := text(t, s.embedded, q)

			assert.False(t, wireVal.Valid, "mysql wire %s", column)
			assert.False(t, embeddedVal.Valid, "embedded driver %s", column)
			assert.Equal(t, wireVal, embeddedVal)
		}
	})

	t.Run("empty set is not null", func(t *testing.T) {
		const q = "SELECT perms FROM accounts WHERE id = 2"
		wireVal := text(t, s.wire, q)
		embeddedVal := text(t, s.embedded, q)

		assert.True(t, wireVal.Valid, "mysql wire")
		assert.Equal(t, "", wireVal.String)
		assert.Equal(t, wireVal, embeddedVal)
	})
}

// TestTransportParity_EnumPredicate asserts both transports still filter on
// the stored ordinal identically. Resolution happens on the way out, so a
// predicate must keep matching the label it always did -- on both sides.
func TestTransportParity_EnumPredicate(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.wire.Exec(paritySymbolicDDL)
	require.NoError(t, err)
	_, err = s.wire.Exec("INSERT INTO accounts VALUES (1,'active','read'), (2,'banned','write'), (3,'active','admin')")
	require.NoError(t, err)

	for _, q := range []string{
		"SELECT COUNT(*) FROM accounts WHERE status = 'active'",
		"SELECT COUNT(*) FROM accounts WHERE perms = 'write'",
		"SELECT COUNT(*) FROM accounts WHERE status <> 'active'",
	} {
		t.Run(q, func(t *testing.T) {
			assert.Equal(t, text(t, s.wire, q), text(t, s.embedded, q))
		})
	}

	assert.Equal(t, "2", text(t, s.wire, "SELECT COUNT(*) FROM accounts WHERE status = 'active'").String)
}

// TestTransportParity_EnumOrderBy asserts ordering stays ordinal-based --
// declaration order, not the labels' lexical order -- on both transports.
// 'active' sorts before 'banned' lexically but after it by ordinal only if
// the declaration says so, which is what makes this worth pinning.
func TestTransportParity_EnumOrderBy(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.wire.Exec(paritySymbolicDDL)
	require.NoError(t, err)
	_, err = s.wire.Exec("INSERT INTO accounts VALUES (1,'banned','read'), (2,'pending','read'), (3,'active','read')")
	require.NoError(t, err)

	const q = "SELECT status FROM accounts ORDER BY status"

	collect := func(db *sql.DB) []string {
		rows, err := db.Query(q)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()

		var out []string
		for rows.Next() {
			var v string
			require.NoError(t, rows.Scan(&v))
			out = append(out, v)
		}
		require.NoError(t, rows.Err())
		return out
	}

	wireOrder := collect(s.wire)
	assert.Equal(t, []string{"pending", "active", "banned"}, wireOrder, "ENUM orders by ordinal, not label")
	assert.Equal(t, wireOrder, collect(s.embedded))
}

// ---------------------------------------------------------------------------
// Cross-transport parity for exactly-represented numeric columns.
//
// DECIMAL and BIGINT UNSIGNED are the two column types whose whole reason for
// existing is that they carry a value no float64 -- and, for the upper half of
// BIGINT UNSIGNED, no int64 either -- can hold. A transport that narrows
// either one does not return an approximation of the stored row, it returns a
// different row, and does so without an error. The tests below pin the wire,
// the embedded driver, and the service-layer SQL API to the exact stored text.
// ---------------------------------------------------------------------------

const parityExactDDL = `CREATE TABLE ledger (
	id INT PRIMARY KEY,
	amt DECIMAL(20,4),
	big BIGINT UNSIGNED
)`

// maxUint64Text is math.MaxUint64 written out: the canonical BIGINT UNSIGNED
// value that cannot survive a trip through int64.
const maxUint64Text = "18446744073709551615"

// TestTransportParity_Decimal asserts a DECIMAL column can be written at all
// and reads back with its exact stored text on every transport. The write is
// the load-bearing half: the row codec asserts a concrete Go decimal type, so
// a mismatch with the type the engine hands it fails every INSERT outright.
func TestTransportParity_Decimal(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.wire.Exec(parityExactDDL)
	require.NoError(t, err)
	_, err = s.wire.Exec("INSERT INTO ledger VALUES (1, 12345678901234.5678, 1)")
	require.NoError(t, err)

	const q = "SELECT amt FROM ledger WHERE id = 1"

	wireVal := text(t, s.wire, q)
	embeddedVal := text(t, s.embedded, q)
	serviceVal := serviceText(t, s.service, q, "amt")

	assert.Equal(t, "12345678901234.5678", wireVal.String, "mysql wire")
	assert.Equal(t, "12345678901234.5678", embeddedVal.String, "embedded driver")
	assert.Equal(t, "12345678901234.5678", serviceVal, "service-layer SQL API")
	assert.Equal(t, wireVal, embeddedVal, "mysql wire and embedded driver diverged")
}

// TestTransportParity_Decimal_WrittenEmbedded proves the DECIMAL write path is
// exercised from the embedded driver as well, and that a value with more
// integer digits than a float64 can represent survives the round trip
// digit-for-digit.
func TestTransportParity_Decimal_WrittenEmbedded(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.embedded.Exec(parityExactDDL)
	require.NoError(t, err)
	_, err = s.embedded.Exec("INSERT INTO ledger VALUES (?, ?, ?)", 1, "9999999999999999.9999", 1)
	require.NoError(t, err)

	const q = "SELECT amt FROM ledger WHERE id = 1"

	wireVal := text(t, s.wire, q)
	embeddedVal := text(t, s.embedded, q)

	assert.Equal(t, "9999999999999999.9999", wireVal.String, "mysql wire")
	assert.Equal(t, wireVal, embeddedVal, "mysql wire and embedded driver diverged")
}

// TestTransportParity_Decimal_NegativeAndZero covers the sign and scale
// carriers a decimal codec is most likely to drop: a negative value, and a
// zero that must keep the column's declared scale rather than collapsing to
// "0".
func TestTransportParity_Decimal_NegativeAndZero(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.wire.Exec(parityExactDDL)
	require.NoError(t, err)
	_, err = s.wire.Exec("INSERT INTO ledger VALUES (1, -0.0001, 1), (2, 0.0000, 2)")
	require.NoError(t, err)

	for _, tc := range []struct {
		id   int
		want string
	}{
		{1, "-0.0001"},
		{2, "0.0000"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			q := "SELECT amt FROM ledger WHERE id = ?"
			wireVal := text(t, s.wire, q, tc.id)
			embeddedVal := text(t, s.embedded, q, tc.id)

			assert.Equal(t, tc.want, wireVal.String, "mysql wire")
			assert.Equal(t, tc.want, embeddedVal.String, "embedded driver")
			assert.Equal(t, wireVal, embeddedVal, "mysql wire and embedded driver diverged")
		})
	}
}

// TestTransportParity_BigintUnsigned is the silent-corruption guard: a
// BIGINT UNSIGNED above math.MaxInt64 must scan into a uint64 with its exact
// value on both transports. database/sql/driver.Value cannot carry such a
// number as an integer, so the driver has to hand database/sql the decimal
// text -- exactly as go-sql-driver/mysql does -- rather than reinterpreting
// the bits as a negative int64.
func TestTransportParity_BigintUnsigned(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.wire.Exec(parityExactDDL)
	require.NoError(t, err)
	_, err = s.wire.Exec("INSERT INTO ledger VALUES (1, 0.0, " + maxUint64Text + ")")
	require.NoError(t, err)

	const q = "SELECT big FROM ledger WHERE id = 1"

	t.Run("scan uint64", func(t *testing.T) {
		var wireVal, embeddedVal uint64
		require.NoError(t, s.wire.QueryRow(q).Scan(&wireVal), "mysql wire")
		require.NoError(t, s.embedded.QueryRow(q).Scan(&embeddedVal), "embedded driver")

		assert.Equal(t, uint64(math.MaxUint64), wireVal, "mysql wire")
		assert.Equal(t, uint64(math.MaxUint64), embeddedVal, "embedded driver")
		assert.Equal(t, wireVal, embeddedVal, "mysql wire and embedded driver diverged")
	})

	t.Run("scan text", func(t *testing.T) {
		wireVal := text(t, s.wire, q)
		embeddedVal := text(t, s.embedded, q)
		serviceVal := serviceText(t, s.service, q, "big")

		assert.Equal(t, maxUint64Text, wireVal.String, "mysql wire")
		assert.Equal(t, maxUint64Text, embeddedVal.String, "embedded driver")
		assert.Equal(t, maxUint64Text, serviceVal, "service-layer SQL API")
	})
}

// TestTransportParity_BigintUnsigned_Boundaries walks the int64 boundary,
// which is where a narrowing conversion changes behaviour: everything at or
// below math.MaxInt64 must still arrive as an ordinary integer, and the first
// value above it must not wrap to a negative one.
func TestTransportParity_BigintUnsigned_Boundaries(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.embedded.Exec(parityExactDDL)
	require.NoError(t, err)

	cases := []struct {
		name string
		text string
	}{
		{"zero", "0"},
		{"maxint64", "9223372036854775807"},
		{"maxint64+1", "9223372036854775808"},
		{"maxuint64", maxUint64Text},
	}

	for i, tc := range cases {
		_, err := s.embedded.Exec("INSERT INTO ledger VALUES (?, 0.0, "+tc.text+")", i+1)
		require.NoError(t, err, tc.name)
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := "SELECT big FROM ledger WHERE id = ?"

			var wireVal, embeddedVal uint64
			require.NoError(t, s.wire.QueryRow(q, i+1).Scan(&wireVal), "mysql wire")
			require.NoError(t, s.embedded.QueryRow(q, i+1).Scan(&embeddedVal), "embedded driver")

			want, err := strconv.ParseUint(tc.text, 10, 64)
			require.NoError(t, err)

			assert.Equal(t, want, wireVal, "mysql wire")
			assert.Equal(t, want, embeddedVal, "embedded driver")
			assert.Equal(t, wireVal, embeddedVal, "mysql wire and embedded driver diverged")
		})
	}
}

// TestTransportParity_ScanTypes asserts the embedded driver's advertised
// ColumnTypeScanType is a type its own rows can actually be scanned into --
// the property that makes ScanType usable by a generic mapper such as GORM,
// and the one an ad-hoc value conversion silently breaks.
func TestTransportParity_ScanTypes(t *testing.T) {
	s := newParitySuite(t)

	_, err := s.embedded.Exec(parityExactDDL)
	require.NoError(t, err)
	_, err = s.embedded.Exec("INSERT INTO ledger VALUES (1, 1.2345, " + maxUint64Text + ")")
	require.NoError(t, err)

	rows, err := s.embedded.Query("SELECT id, amt, big FROM ledger WHERE id = 1")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	types, err := rows.ColumnTypes()
	require.NoError(t, err)
	require.Len(t, types, 3)

	assert.Equal(t, reflect.TypeOf(int32(0)), types[0].ScanType(), "id")
	assert.Equal(t, reflect.TypeOf(""), types[1].ScanType(), "amt")
	assert.Equal(t, reflect.TypeOf(uint64(0)), types[2].ScanType(), "big")

	require.True(t, rows.Next())
	dest := make([]any, len(types))
	for i := range dest {
		dest[i] = reflect.New(types[i].ScanType()).Interface()
	}
	require.NoError(t, rows.Scan(dest...), "advertised scan types must be scannable")

	assert.Equal(t, int32(1), *dest[0].(*int32))
	assert.Equal(t, "1.2345", *dest[1].(*string))
	assert.Equal(t, uint64(math.MaxUint64), *dest[2].(*uint64))
	require.NoError(t, rows.Err())
}

// ---------------------------------------------------------------------------
// Cross-transport parity for FOREIGN KEY enforcement.
//
// Referential integrity is enforced by go-mysql-server from the constraint
// metadata pkg/sql/provider stores (see relational_foreign_key.go), which
// means it is enforced once, below every transport. That is precisely the
// property worth pinning: a constraint that held on the wire but not through
// the embedded driver -- or a violation that surfaced as MySQL 1452 on one
// and as an opaque error on the other -- would be a data-integrity difference
// between two ways of reaching the same rows, not a rendering difference.
//
// The error NUMBERS are asserted, not just the failure, because they are the
// contract sdk/go/gorm's error translator matches on to produce
// gorm.ErrForeignKeyViolated.
// ---------------------------------------------------------------------------

const parityForeignKeyDDL = `CREATE TABLE parity_users (
	id BIGINT PRIMARY KEY,
	company_id BIGINT,
	CONSTRAINT fk_parity_users_company FOREIGN KEY (company_id) REFERENCES parity_companies(id)
)`

// createParityFKSchema creates the parent/child pair through whichever
// transport the caller passes, so the schema under test is not privileged
// toward the transport that declared it.
func createParityFKSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec("CREATE TABLE parity_companies (id BIGINT PRIMARY KEY, name VARCHAR(64))")
	require.NoError(t, err)
	_, err = db.Exec(parityForeignKeyDDL)
	require.NoError(t, err)
	_, err = db.Exec("INSERT INTO parity_companies VALUES (1, 'acme')")
	require.NoError(t, err)
}

// mysqlErrorNumber extracts the MySQL error number from either transport's
// error representation: the wire client returns *mysql.MySQLError, the
// embedded driver returns *qrdbdriver.SQLStateError. Both carry the same
// number for the same failure, which is what these tests assert.
func mysqlErrorNumber(t *testing.T, err error) uint16 {
	t.Helper()
	require.Error(t, err)

	var wireErr *gomysql.MySQLError
	if errors.As(err, &wireErr) {
		return wireErr.Number
	}
	var embeddedErr *qrdbdriver.SQLStateError
	if errors.As(err, &embeddedErr) {
		return embeddedErr.Number
	}
	t.Fatalf("error = %v (%T) carries no MySQL error number", err, err)
	return 0
}

// TestTransportParity_ForeignKeyChildViolation asserts a child row with no
// parent is refused as MySQL 1452 on both the wire and the embedded driver,
// whichever one declared the schema.
func TestTransportParity_ForeignKeyChildViolation(t *testing.T) {
	s := newParitySuite(t)
	createParityFKSchema(t, s.wire)

	const orphan = "INSERT INTO parity_users VALUES (%d, 99)"

	_, wireErr := s.wire.Exec(fmt.Sprintf(orphan, 1))
	_, embeddedErr := s.embedded.Exec(fmt.Sprintf(orphan, 2))

	assert.Equal(t, uint16(1452), mysqlErrorNumber(t, wireErr), "mysql wire")
	assert.Equal(t, uint16(1452), mysqlErrorNumber(t, embeddedErr), "embedded driver")

	// The service-layer SQL API refuses it too; it has no MySQL error number
	// of its own, so the assertion is that it failed rather than wrote.
	_, svcErr := s.service.Execute(context.Background(), "qrdb", fmt.Sprintf(orphan, 3))
	assert.Error(t, svcErr, "service-layer SQL API")

	// Nothing was written by any of the three.
	assert.Equal(t, "0", text(t, s.wire, "SELECT COUNT(*) FROM parity_users").String)

	// And a row WITH a parent is accepted on both.
	_, err := s.wire.Exec("INSERT INTO parity_users VALUES (1, 1)")
	require.NoError(t, err, "mysql wire")
	_, err = s.embedded.Exec("INSERT INTO parity_users VALUES (2, 1)")
	require.NoError(t, err, "embedded driver")
}

// TestTransportParity_ForeignKeyParentViolation asserts deleting a referenced
// parent is refused as MySQL 1451 on both transports.
func TestTransportParity_ForeignKeyParentViolation(t *testing.T) {
	s := newParitySuite(t)
	createParityFKSchema(t, s.embedded)

	_, err := s.embedded.Exec("INSERT INTO parity_users VALUES (1, 1)")
	require.NoError(t, err)

	const orphaning = "DELETE FROM parity_companies WHERE id = 1"

	_, wireErr := s.wire.Exec(orphaning)
	_, embeddedErr := s.embedded.Exec(orphaning)

	assert.Equal(t, uint16(1451), mysqlErrorNumber(t, wireErr), "mysql wire")
	assert.Equal(t, uint16(1451), mysqlErrorNumber(t, embeddedErr), "embedded driver")

	assert.Equal(t, "1", text(t, s.wire, "SELECT COUNT(*) FROM parity_companies").String)
	assert.Equal(t, "1", text(t, s.embedded, "SELECT COUNT(*) FROM parity_companies").String)
}

// TestTransportParity_ForeignKeyMetadataIsShared asserts both transports
// report the same constraint from INFORMATION_SCHEMA, which is the metadata a
// migration tool reads back to decide whether a constraint already exists.
// The constraint is declared over one transport and read over the other.
func TestTransportParity_ForeignKeyMetadataIsShared(t *testing.T) {
	s := newParitySuite(t)
	createParityFKSchema(t, s.wire)

	const q = `SELECT COUNT(*) FROM information_schema.referential_constraints
		WHERE constraint_name = 'fk_parity_users_company'`

	assert.Equal(t, "1", text(t, s.wire, q).String, "mysql wire")
	assert.Equal(t, "1", text(t, s.embedded, q).String, "embedded driver")

	// Dropping it over the embedded driver must be visible on the wire.
	_, err := s.embedded.Exec("ALTER TABLE parity_users DROP FOREIGN KEY fk_parity_users_company")
	require.NoError(t, err)

	assert.Equal(t, "0", text(t, s.wire, q).String, "mysql wire after DROP over the embedded driver")

	// And with the constraint gone, the previously-refused row is accepted on
	// both transports.
	_, err = s.wire.Exec("INSERT INTO parity_users VALUES (1, 99)")
	assert.NoError(t, err, "mysql wire")
	_, err = s.embedded.Exec("INSERT INTO parity_users VALUES (2, 99)")
	assert.NoError(t, err, "embedded driver")
}
