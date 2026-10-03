//go:build qrdb_sql

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

package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranslateUpdateFrom covers the PostgreSQL UPDATE ... FROM to MySQL
// multi-table UPDATE rewrite in isolation (inputs are post-quoting, the
// form the step sees inside Translate).
func TestTranslateUpdateFrom(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  string
	}{
		{
			name:  "assignments qualified, WHERE preserved",
			query: "UPDATE `users` SET `name`=?,`updated_at`=? FROM `accounts` WHERE (accounts.user_id = users.id)",
			want:  "UPDATE `users`, `accounts` SET `users`.`name`=?, `users`.`updated_at`=? WHERE (accounts.user_id = users.id)",
		},
		{
			name:  "already-qualified assignment untouched",
			query: "UPDATE `users` SET `name`=accounts.number, accounts.note=? FROM `accounts` WHERE accounts.user_id = users.id",
			want:  "UPDATE `users`, `accounts` SET `users`.`name`=accounts.number, accounts.note=? WHERE accounts.user_id = users.id",
		},
		{
			name:  "no WHERE clause",
			query: "UPDATE `users` SET `age`=? FROM `accounts`",
			want:  "UPDATE `users`, `accounts` SET `users`.`age`=?",
		},
		{
			name:  "subquery FROM does not split the statement",
			query: "UPDATE `users` SET `age`=(SELECT MAX(`age`) FROM `others`) WHERE `id` = ?",
			want:  "UPDATE `users` SET `age`=(SELECT MAX(`age`) FROM `others`) WHERE `id` = ?",
		},
		{
			name:  "FROM inside a string literal is ignored",
			query: "UPDATE `users` SET `note`='copied FROM ledger' WHERE `id` = ?",
			want:  "UPDATE `users` SET `note`='copied FROM ledger' WHERE `id` = ?",
		},
		{
			name:  "plain UPDATE passes through",
			query: "UPDATE `users` SET `age`=? WHERE `id` = ?",
			want:  "UPDATE `users` SET `age`=? WHERE `id` = ?",
		},
		{
			name:  "non-UPDATE passes through",
			query: "SELECT * FROM `users`",
			want:  "SELECT * FROM `users`",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, translateUpdateFrom(tt.query))
		})
	}
}

// TestTranslateUpdateFromThroughTranslator proves the full translation of
// the exact statements gorm emits for TestUpdateFrom on the PostgreSQL
// wire.
func TestTranslateUpdateFromThroughTranslator(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`UPDATE "users" SET "name"=$1,"updated_at"=$2 FROM "accounts" WHERE (accounts.user_id = users.id AND accounts.number = $3 AND accounts.deleted_at IS NULL) AND "users"."deleted_at" IS NULL`)
	require.NoError(t, err)
	assert.Equal(t,
		"UPDATE `users`, `accounts` SET `users`.`name`=?, `users`.`updated_at`=? WHERE (accounts.user_id = users.id AND accounts.number = ? AND accounts.deleted_at IS NULL) AND `users`.`deleted_at` IS NULL",
		got)

	got, err = tr.Translate(`UPDATE "users" SET "name"=accounts.number,"updated_at"=$1 FROM "accounts" WHERE (accounts.user_id = users.id AND accounts.number IN ($2,$3))`)
	require.NoError(t, err)
	assert.Equal(t,
		"UPDATE `users`, `accounts` SET `users`.`name`=accounts.number, `users`.`updated_at`=? WHERE (accounts.user_id = users.id AND accounts.number IN (?,?))",
		got)
}

// TestTranslateArrayColumnTypes covers PostgreSQL array column declarations,
// which translate to text storage: an array VALUE on the wire is its text
// literal ('{a,b}'), which lib/pq's and pgx's array types write and parse,
// so a text column round-trips it exactly.
func TestTranslateArrayColumnTypes(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`CREATE TABLE "harumphs" ("id" bigserial, "things" text[], PRIMARY KEY ("id"))`)
	require.NoError(t, err)
	assert.Contains(t, got, "`things` LONGTEXT")

	got, err = tr.Translate(`CREATE TABLE "t" ("xs" integer[][])`)
	require.NoError(t, err)
	assert.Contains(t, got, "`xs` LONGTEXT")

	// A keyed array column takes the bounded text type every other keyed
	// text column takes.
	got, err = tr.Translate(`CREATE TABLE "t2" ("xs" text[], PRIMARY KEY ("xs"))`)
	require.NoError(t, err)
	assert.Contains(t, got, "`xs` VARCHAR(768)")
}

// TestTranslateNumericColumnTypes pins numeric/decimal translation: a
// modifier-less declaration takes MySQL's widest scale rather than the
// (10,0) default that rounds every fraction, and an explicit (p,s) is kept.
func TestTranslateNumericColumnTypes(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`CREATE TABLE "t" ("height" numeric, "price" decimal)`)
	require.NoError(t, err)
	assert.Contains(t, got, "`height` DECIMAL(65,30)")
	assert.Contains(t, got, "`price` DECIMAL(65,30)")

	got, err = tr.Translate(`CREATE TABLE "t2" ("recid1" numeric(9,0) NOT NULL)`)
	require.NoError(t, err)
	assert.Contains(t, got, "`recid1` DECIMAL(9,0)")
}

// TestTranslateDDLTypes_ArraysAndNumerics pins the CREATE TABLE spellings
// that used to be rejected (arrays) or passed through to a lossy MySQL
// default (modifier-less numeric/decimal).
func TestTranslateDDLTypes_ArraysAndNumerics(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "array of mapped type",
			input:    "CREATE TABLE `t` (`things` text[])",
			expected: "CREATE TABLE `t` (`things` LONGTEXT)",
		},
		{
			name:     "array of passthrough type",
			input:    "CREATE TABLE `t` (`ids` bigint[])",
			expected: "CREATE TABLE `t` (`ids` LONGTEXT)",
		},
		{
			name:     "array after modifier",
			input:    "CREATE TABLE `t` (`names` varchar(20)[])",
			expected: "CREATE TABLE `t` (`names` LONGTEXT)",
		},
		{
			name:     "array in alter table",
			input:    "ALTER TABLE `t` ADD `things` text[]",
			expected: "ALTER TABLE `t` ADD `things` LONGTEXT",
		},
		{
			name:     "bare decimal takes the widest scale",
			input:    "CREATE TABLE `t` (`d` decimal,`e` numeric(9,0))",
			expected: "CREATE TABLE `t` (`d` DECIMAL(65,30),`e` DECIMAL(9,0))",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := translateDDLTypes(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

// TestTranslateEmptyCreateTable pins the zero-column CREATE TABLE rewrite:
// PostgreSQL permits an empty column list, MySQL's grammar does not, so a
// placeholder column is declared. Non-empty lists pass through untouched.
func TestTranslateEmptyCreateTable(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`CREATE TABLE "objects" ()`)
	require.NoError(t, err)
	assert.Equal(t, "CREATE TABLE `objects` (`qrdb_zero_column` TINYINT NULL)", got)

	got, err = tr.Translate(`CREATE TABLE IF NOT EXISTS "objects" ( )`)
	require.NoError(t, err)
	assert.Equal(t, "CREATE TABLE IF NOT EXISTS `objects` (`qrdb_zero_column` TINYINT NULL)", got)

	got, err = tr.Translate(`CREATE TABLE "t" ("id" bigint)`)
	require.NoError(t, err)
	assert.Equal(t, "CREATE TABLE `t` (`id` bigint)", got)
}

// TestAlignCurrentTimestampPrecision pins the DEFAULT current_timestamp
// precision alignment for the DATETIME(6) columns timestamp translation
// produces.
func TestAlignCurrentTimestampPrecision(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`CREATE TABLE "t" ("updated_at" TIMESTAMP WITHOUT TIME ZONE DEFAULT current_timestamp)`)
	require.NoError(t, err)
	assert.Contains(t, got, "DATETIME(6) DEFAULT CURRENT_TIMESTAMP(6)")

	// An explicit precision is left as written.
	got, err = tr.Translate(`CREATE TABLE "t2" ("updated_at" timestamp(3) DEFAULT current_timestamp(3))`)
	require.NoError(t, err)
	assert.Contains(t, got, "DATETIME(3) DEFAULT current_timestamp(3)")
}

// TestTranslateOnConflictOnConstraint pins the named-constraint upsert
// target: MySQL's forms key off whichever unique index actually conflicts,
// the same widening the (cols) translation accepts, so the named form
// translates rather than being refused.
func TestTranslateOnConflictOnConstraint(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`INSERT INTO "things" ("data") VALUES ($1) ON CONFLICT ON CONSTRAINT some_id_other_id_unique DO UPDATE SET "data"="excluded"."data"`)
	require.NoError(t, err)
	assert.Contains(t, got, "ON DUPLICATE KEY UPDATE")
	assert.NotContains(t, got, "CONFLICT")

	got, err = tr.Translate(`INSERT INTO "things" ("data") VALUES ($1) ON CONFLICT ON CONSTRAINT "some_id_other_id_unique" DO NOTHING`)
	require.NoError(t, err)
	assert.Contains(t, got, "INSERT IGNORE")
	assert.NotContains(t, got, "CONFLICT")
}

// TestStripPublicSchemaQualifier pins the public. qualifier removal in
// both quoting styles, leaving literals and non-public qualifiers alone.
func TestStripPublicSchemaQualifier(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`CREATE TABLE public.log_usage (log_id bigint NOT NULL)`)
	require.NoError(t, err)
	assert.Equal(t, "CREATE TABLE log_usage (log_id bigint NOT NULL)", got)

	got, err = tr.Translate(`SELECT * FROM "public"."users" WHERE note = 'public.users'`)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM `users` WHERE note = 'public.users'", got)

	got, err = tr.Translate(`SELECT * FROM "other"."users"`)
	require.NoError(t, err)
	assert.Equal(t, "SELECT * FROM `other`.`users`", got)
}

// TestSplitTopLevelStatements pins the multi-statement split: top-level
// semicolons separate, everything nested or quoted does not.
func TestSplitTopLevelStatements(t *testing.T) {
	statements := SplitTopLevelStatements("CREATE TABLE t (a int); ALTER TABLE t ALTER a SET DEFAULT 1;")
	require.Len(t, statements, 2)
	assert.Equal(t, "CREATE TABLE t (a int)", statements[0])

	statements = SplitTopLevelStatements("SELECT 'a;b'; SELECT (1); SELECT 2")
	require.Len(t, statements, 3)

	statements = SplitTopLevelStatements("SELECT 1")
	require.Len(t, statements, 1)
}

// TestWrapNextvalDefault pins the SET DEFAULT nextval parenthesization.
func TestWrapNextvalDefault(t *testing.T) {
	tr := NewTranslator()

	got, err := tr.Translate(`ALTER TABLE "events" ALTER COLUMN "uid" SET DEFAULT nextval('events_uid_seq')`)
	require.NoError(t, err)
	assert.Contains(t, got, "DEFAULT (nextval('events_uid_seq'))")

	// Already-parenthesized defaults are left alone.
	assert.Equal(t, "ALTER TABLE t ALTER c SET DEFAULT (NEXTVAL('s'))",
		wrapNextvalDefault("ALTER TABLE t ALTER c SET DEFAULT (NEXTVAL('s'))"))
}
