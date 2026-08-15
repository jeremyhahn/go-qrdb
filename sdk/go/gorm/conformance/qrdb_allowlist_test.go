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

//go:build gormconformance

package tests_test

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// dumpAllowlistEnvVar, when set to "1", makes TestMain marshal allowlist to
// stdout as JSON and exit before doing anything else — including before
// validating GORM_QRDB_TARGET, since the allowlist itself does not depend
// on a target. runner.go (a separate process; see its doc comment) invokes
// the test binary this way to read the ratchet table without duplicating it
// as hand-maintained JSON: allowlist below is ordinary, type-checked Go
// source, so it cannot rot into a value the compiler would have rejected.
const dumpAllowlistEnvVar = "GORM_QRDB_DUMP_ALLOWLIST"

// target identifies which qrdb surface an expectedFailure applies to. Reuses
// the type declared in qrdb_tests_test.go.
//
// expectedFailure is one entry in the ratchet: a specific upstream Test (or
// "Test/Subtest") this harness currently expects to fail on one or more
// targets, why, and the deadline by which that must stop being true.
// runner.go enforces four invariants against this table — see its doc
// comment — the second of which, "an allowlisted test that PASSES fails the
// run," is what makes this a ratchet instead of a skip list: nothing in
// this table can silently keep excusing a test that starts passing.
type expectedFailure struct {
	// Test is the exact name go test -json reports: "TestMigrate" for a
	// whole-test failure, or "TestMigrate/create_table" for a specific
	// subtest. Must match verbatim — runner.go does no fuzzy matching.
	Test string

	// Targets lists which of node/mysql/pgsql this entry applies to.
	// A test failing on a target NOT listed here is an unexpected failure.
	Targets []target

	// Reason is a short, greppable category, not a paragraph. Established
	// values so far: "qrdb:relational-gap" (a gap in pkg/sql/provider's
	// relational surface — DDL, foreign keys, association loading) and
	// "upstream:dialector-type-assertion" (an upstream test that is
	// structurally unpassable for any third-party dialector, so no qrdb
	// change can retire it).
	Reason string

	// Issue is a tracking reference (issue number, memory-file slug, or
	// similar) for where the fix lives.
	Issue string

	// Retire is a hard deadline. runner.go fails the run if today is on or
	// after Retire for any entry, regardless of whether that entry's test
	// still fails — an allowlist entry is a promise to come back, not a
	// permanent exemption.
	Retire time.Time

	// AbortsRun marks a test that does not merely fail but prevents the
	// `go test` process from finishing, taking every test after it with it.
	//
	// Two distinct upstream behaviours land here. Most panic: they
	// dereference the result of a call they did not expect to fail —
	// `AutoMigrate` followed by an unchecked `ColumnTypes`, say — so when
	// qrdb returns an error where PostgreSQL would not, the test nil-derefs
	// instead of failing cleanly. One (TestPreparedStmtDeadlock) hangs
	// instead, which ends the run at the -timeout rather than immediately.
	// The consequence is identical either way: the run reports a prefix of
	// the suite, and everything after the offending test is never measured.
	//
	// runner.go excludes these with `go test -skip` and reports them in
	// their own bucket. That is the difference between a suite that measures
	// 139 of 397 tests and calls it a result, and one that measures all 397.
	// They are exempt from the stale-pass and matched-nothing checks for the
	// obvious reason that a test which never runs can be neither: to retire
	// one, drop AbortsRun and see whether the run still completes.
	AbortsRun bool

	// Unstable marks a test whose outcome is not a property of the test at
	// all: it passes or fails depending on what an EARLIER test left behind.
	//
	// The upstream suite shares one set of models across every file, and its
	// migration tests drop and re-create them. When a re-creation partially
	// fails -- which several do, on gaps tracked elsewhere in this table --
	// the shared tables stay dropped, and every later test touching them
	// fails with "table not found: accounts" on a table TestMain created.
	// Which tests land on the wrong side of that varies between otherwise
	// identical runs.
	//
	// Such an entry is exempt from the stale-pass check, because a passing
	// run says nothing about it either: it means the collateral fell
	// elsewhere that time. It is NOT a way to park a genuinely flaky qrdb
	// behaviour -- the diagnosis above is the bar, and the fix is to repair
	// the migration gap upstream of it, at which point these become ordinary
	// entries or disappear.
	Unstable bool
}

// allowlist is the ratchet's typed source of truth. It is seeded from actual
// `make conformance-test-gorm-node` runs (see README.md for the observed
// counts) rather than guessed, and MUST be edited down as qrdb's relational
// surface fills in — runner.go's stale-pass check makes leaving a
// now-passing entry in place a hard failure, not a silent no-op.
//
// The entries were originally seeded when this target ran over embedded://,
// which has no service.Registry and therefore no relational table catalog at
// all: CREATE TABLE failed outright, RunMigrations created zero tables, and
// every upstream test that needs one failed the same way. The target then
// moved to node://, which does have the catalog, and 34 of those entries were
// deleted because their tests passed.
//
// FOREIGN KEY support in pkg/sql/provider then deleted 117 more, taking this
// table from 138 entries to 21. That was the single largest remaining
// blocker: AutoMigrate emits a FOREIGN KEY constraint on the shared User
// model, and until the provider implemented sql.ForeignKeyTable it aborted
// there, so most of the suite never got its tables at all.
//
// Fixing openNode's connection handling (see qrdb_tests_test.go's
// nodeConnector: every OpenTestConnection used to get its own brand-new EMPTY
// node) then deleted 12 more, taking the table from 21 to 9. Each of those 12
// was verified passing in three consecutive whole-suite runs before removal.
//
// Neither of the two remaining generics entries is a qrdb gap: both upstream
// tests type-assert DB.Dialector.(*mysql.Dialector) while guarding only on
// Name() == "mysql", so they are unpassable for any third-party dialector.
// They are scoped to node alone — under the mysql WIRE target the dialector
// genuinely is gorm.io/driver/mysql's, and both tests pass there.
//
// The seven that are real gaps have NOT been individually triaged, and their
// Reason still says so. The recurring themes are many2many behaviour, ALTER
// TABLE column-type migration over existing rows, and column-metadata
// reporting. Retire is set close specifically so that triage is forced rather
// than left to rot.
//
// Neither wire target has any entry at all: both were first measured on
// 2026-08-13 and every failure on them is deliberately reported as unexpected
// rather than pre-excused. See GORM_SUITE.lock's budgets_comment.
var allowlist = []expectedFailure{
	{
		Test:    "TestConcurrentMany2ManyAssociation",
		Targets: []target{targetNode},
		Reason:  "qrdb:relational-gap",
		Issue:   "not yet individually triaged: seeded when this target had no relational catalog at all, and still failing after the node:// target gave it one and after FOREIGN KEY support landed in pkg/sql/provider (which retired 117 of the original 138 entries). What remains is no longer one shared blocker; the recurring themes are many2many/nested-preload behaviour, ALTER TABLE column-type migration, and unique-constraint reporting; see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestCreateFromMapWithTable",
		Targets: []target{targetNode},
		Reason:  "qrdb:relational-gap",
		Issue:   "not yet individually triaged: seeded when this target had no relational catalog at all, and still failing after the node:// target gave it one and after FOREIGN KEY support landed in pkg/sql/provider (which retired 117 of the original 138 entries). What remains is no longer one shared blocker; the recurring themes are many2many/nested-preload behaviour, ALTER TABLE column-type migration, and unique-constraint reporting; see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestDifferentTypeWithoutDeclaredLength",
		Targets: []target{targetNode},
		Reason:  "qrdb:relational-gap",
		Issue:   "not yet individually triaged: seeded when this target had no relational catalog at all, and still failing after the node:// target gave it one and after FOREIGN KEY support landed in pkg/sql/provider (which retired 117 of the original 138 entries). What remains is no longer one shared blocker; the recurring themes are many2many/nested-preload behaviour, ALTER TABLE column-type migration, and unique-constraint reporting; see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:      "TestGenericsNestedPreloads",
		Targets:   []target{targetNode},
		Reason:    "upstream:dialector-type-assertion",
		Issue:     "NOT a qrdb gap and not fixable from qrdb: the upstream test does DB.Dialector.(*mysql.Dialector).ServerVersion guarded only by DB.Dialector.Name() == \"mysql\", so it panics for ANY third-party dialector that reports the mysql name -- which sdk/go/gorm deliberately does (see sdk/go/gorm/doc.go). Scoped to node alone: under the mysql WIRE target the dialector genuinely IS gorm.io/driver/mysql's and this test passes. Retiring it needs a decision not taken here -- substitute generics_test.go through GORM_SUITE.lock's substitutes[], or stop reporting \"mysql\" (an SDK-wide behaviour change); see conformance/README.md",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:      "TestGenericsPreloads",
		Targets:   []target{targetNode},
		Reason:    "upstream:dialector-type-assertion",
		Issue:     "NOT a qrdb gap and not fixable from qrdb: the upstream test does DB.Dialector.(*mysql.Dialector).ServerVersion guarded only by DB.Dialector.Name() == \"mysql\", so it panics for ANY third-party dialector that reports the mysql name -- which sdk/go/gorm deliberately does (see sdk/go/gorm/doc.go). Scoped to node alone: under the mysql WIRE target the dialector genuinely IS gorm.io/driver/mysql's and this test passes. Retiring it needs a decision not taken here -- substitute generics_test.go through GORM_SUITE.lock's substitutes[], or stop reporting \"mysql\" (an SDK-wide behaviour change); see conformance/README.md",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:    "TestMigrateColumns",
		Targets: []target{targetNode},
		Reason:  "qrdb:relational-gap",
		Issue:   "not yet individually triaged: seeded when this target had no relational catalog at all, and still failing after the node:// target gave it one and after FOREIGN KEY support landed in pkg/sql/provider (which retired 117 of the original 138 entries). What remains is no longer one shared blocker; the recurring themes are many2many/nested-preload behaviour, ALTER TABLE column-type migration, and unique-constraint reporting; see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateConstraint",
		Targets: []target{targetNode},
		Reason:  "qrdb:relational-gap",
		Issue:   "not yet individually triaged: seeded when this target had no relational catalog at all, and still failing after the node:// target gave it one and after FOREIGN KEY support landed in pkg/sql/provider (which retired 117 of the original 138 entries). What remains is no longer one shared blocker; the recurring themes are many2many/nested-preload behaviour, ALTER TABLE column-type migration, and unique-constraint reporting; see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},

	// -----------------------------------------------------------------
	// mysql wire — measured 2026-08-14: 373 pass / 6 fail / 18 skip of 397,
	// the whole suite run to completion.
	// -----------------------------------------------------------------
	{
		Test:    "TestConcurrentMany2ManyAssociation",
		Targets: []target{targetMySQL},
		Reason:  "qrdb:optimistic-conflict",
		Issue:   "the upstream test appends to a many2many association from several goroutines at once and expects every append to succeed. qrdb's transactions are optimistic and first-committer-wins, so a concurrent writer of the same key is aborted with 1213 rather than blocked and applied as InnoDB would. This is the documented isolation model (see docs/sql/README.md), not a defect, but it is a real behavioural difference from MySQL",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateColumns",
		Targets: []target{targetMySQL},
		Reason:  "qrdb:alter-column-rewrite",
		Issue:   "ALTER TABLE ... MODIFY COLUMN over existing rows needs the rewrite path (RewriteInserter); the provider currently refuses rather than silently reinterpreting stored rows under the new type",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateView",
		Targets: []target{targetMySQL},
		Reason:  "qrdb:create-view-privilege",
		Issue:   "CREATE VIEW is refused by the SQL privilege plane -- views are not yet represented in the privilege model, so the statement is denied rather than authorized against a view-shaped resource",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateWithUniqueIndexAndUnique",
		Targets: []target{targetMySQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSubQueryWithHaving",
		Targets: []target{targetMySQL},
		Reason:  "upstream:gms-having-subquery",
		Issue:   "a scalar subquery in HAVING is evaluated against the pre-aggregation row set, so an ungrouped aggregate that returns exactly one row is reported as returning many. Reproduced on a stock in-memory go-mysql-server engine with no qrdb code in the path -- see docs/sql/upstream-gaps.md §5",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestToSQL",
		Targets: []target{targetMySQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestSelectWithUpdate",
		Targets:  []target{targetMySQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \"table not found\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestSubQueryWithHaving",
		Targets: []target{targetNode},
		Reason:  "upstream:gms-having-subquery",
		Issue:   "a scalar subquery in HAVING is evaluated against the pre-aggregation row set, so an ungrouped aggregate that returns exactly one row is reported as returning many. Reproduced on a stock in-memory go-mysql-server engine with no qrdb code in the path -- see docs/sql/upstream-gaps.md §5",
		Retire:  mustParseRetire("2026-09-30"),
	},

	{
		// Hangs rather than fails, on every target, and reproduces on an
		// unmodified tree -- so it is not a qrdb regression but it does stop
		// any run that includes it from finishing. Excluded for that reason
		// alone; nothing about qrdb's behaviour is being excused here.
		Test:      "TestPreparedStmtDeadlock",
		Targets:   []target{targetNode, targetMySQL, targetPgSQL},
		Reason:    "upstream:hangs",
		Issue:     "the upstream test deadlocks against qrdb and never returns; it predates this harness and is not diagnosed. Excluding it is what lets every other test be measured -- see the AbortsRun field",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},

	// -----------------------------------------------------------------
	// pgsql — measured 2026-08-14 from a COMPLETED single-process run
	// (255 pass / 119 fail / 17 skip), which is how runner.go runs the
	// suite. A chunked run is not a substitute: upstream tests share
	// database state, so chunk boundaries change outcomes.
	//
	// Over half the failures are two defects in go-mysql-server's RETURNING
	// support, documented in docs/sql/upstream-gaps.md and not fixable from
	// qrdb. Six entries carry AbortsRun (they end the test process rather
	// than failing) and eight carry Unstable (their outcome tracks what an
	// earlier migration test left behind).
	// -----------------------------------------------------------------
	{
		Test:    "TestAlterColumnDataType",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestAutoMigrateDecimal",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestAutoMigrateNullable",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestBelongsToAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestBelongsToAssociationForSlice",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestBelongsToAssociationUnscoped",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestBulkCreatePtrDataWithAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestBulkCreateWithAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestClauseAssociationSetUpdateBelongsToCreateValues",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestConcurrentMany2ManyAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:optimistic-conflict",
		Issue:   "qrdb's transactions are optimistic and first-committer-wins, so a concurrent writer of the same key is aborted with 1213 rather than blocked and applied as InnoDB would. The documented isolation model (docs/sql/README.md), not a defect, but a real behavioural difference",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestCreateInBatches",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestCreateInBatchesWithDefaultSize",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestCreateOnConflictWithDefaultNull",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestCreateWithAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestDeleteAssociationsWithUnscoped",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestDeleteReturning",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestDeleteSliceWithAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestDeleteWithAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestDifferentTypeWithoutDeclaredLength",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestEmbedPreload",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestFindInBatches",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestFindOrCreate",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestForeignKeyConstraints",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-drop-table-cascade",
		Issue:   "DROP TABLE ... CASCADE is parsed and ignored (MySQL treats CASCADE as a no-op), so a table another table references cannot be dropped. PostgreSQL's CASCADE drops the dependent constraints first; implementing it needs the intent to survive analysis, and plan.DropTable carries no cascade flag -- see docs/sql/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestForeignKeyConstraintsBelongsTo",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestFullSaveAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestGenericsJoins",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestGenericsNestedJoins",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestGenericsNestedPreloads",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestGenericsPreloads",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestGroupBy",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestHasManyAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestHasManyAssociationForSlice",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestIdempotentSave",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestInnerJoins",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestInvalidCachedPlanSimpleProtocol",
		Targets: []target{targetPgSQL},
		Reason:  "harness:upstream-dsn-stub",
		Issue:   "the upstream test opens its own connection to the hard-coded postgresDSN (localhost:9920), which this harness defines only so the file compiles; it does not point at the qrdb server -- see qrdb_tests_test.go",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestJoins",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestJoinsForSlice",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMany2ManyDuplicateBelongsToAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMany2ManyOmitAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMany2ManyWithDefaultValueUUID",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:      "TestManyToManyPreloadForNestedPointer",
		Targets:   []target{targetPgSQL},
		Reason:    "upstream:aborts-run",
		Issue:     "ends the go test process rather than failing cleanly, so it is excluded from the run; see the AbortsRun field",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:    "TestManyToManyPreloadForPointer",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestManyToManyPreloadWithMultiPrimaryKeys",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestManyToManyWithMultiPrimaryKeys",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMergeNestedPreloadWithNestedJoin",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrate",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-drop-table-cascade",
		Issue:   "DROP TABLE ... CASCADE is parsed and ignored (MySQL treats CASCADE as a no-op), so a table another table references cannot be dropped. PostgreSQL's CASCADE drops the dependent constraints first; implementing it needs the intent to survive analysis, and plan.DropTable carries no cascade flag -- see docs/sql/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:      "TestMigrateArrayTypeModel",
		Targets:   []target{targetPgSQL},
		Reason:    "upstream:aborts-run",
		Issue:     "ends the go test process rather than failing cleanly, so it is excluded from the run; see the AbortsRun field",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:    "TestMigrateColumns",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-comment-on",
		Issue:   "COMMENT ON COLUMN has no MySQL statement form; MySQL carries a column comment inside the column definition, so supporting it means reconstructing a full MODIFY COLUMN from the catalog",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateConstraint",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateExistingBoolColumnPGAndGaussDB",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateIndexes",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-drop-index-qualified",
		Issue:   "PostgreSQL indexes are schema-scoped and dropped as DROP INDEX schema.name; MySQL's are table-scoped (DROP INDEX name ON table), so the owning table has to be resolved from the catalog first",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateIndexesWithDynamicTableName",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateSameEmbeddedFieldName",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateSerialColumn",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-multiple-serial-columns",
		Issue:   "PostgreSQL allows several serial columns per table; MySQL allows one AUTO_INCREMENT column",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateView",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:create-view-privilege",
		Issue:   "CREATE VIEW is refused by the SQL privilege plane -- views are not yet represented in the privilege model",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateWithColumnComment",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-comment-on",
		Issue:   "COMMENT ON COLUMN has no MySQL statement form; MySQL carries a column comment inside the column definition, so supporting it means reconstructing a full MODIFY COLUMN from the catalog",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:      "TestMigrateWithDefaultValue",
		Targets:   []target{targetPgSQL},
		Reason:    "upstream:aborts-run",
		Issue:     "ends the go test process rather than failing cleanly, so it is excluded from the run; see the AbortsRun field",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:    "TestMigrateWithUniqueIndex",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestMigrateWithUniqueIndexAndUnique",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestNamedPolymorphic",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestNestedJoins",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedManyToManyPreload",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedManyToManyPreload2",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedManyToManyPreload3",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedManyToManyPreload3ForStruct",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedManyToManyPreload4",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedPreload",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedPreload10",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedPreload12",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestNestedPreloadForSlice",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestNestedPreloadWithConds",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestNestedPreloadWithPointerJoin",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-drop-table-cascade",
		Issue:   "DROP TABLE ... CASCADE is parsed and ignored (MySQL treats CASCADE as a no-op), so a table another table references cannot be dropped. PostgreSQL's CASCADE drops the dependent constraints first; implementing it needs the intent to survive analysis, and plan.DropTable carries no cascade flag -- see docs/sql/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNestedPreloadWithUnscoped",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestNilPointerSlice",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestOmitWithCreate",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestOmitWithUpdate",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestOmitWithUpdateColumn",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestOmitWithUpdateWithMap",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestOverrideJoinTable",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestPolymorphicHasManyAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestPolymorphicHasManyAssociationForSlice",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestPolymorphicHasOneAssociation",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestPostgres",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-array-types",
		Issue:   "PostgreSQL array column types (text[], integer[3][3]) have no MySQL equivalent and are rejected with a typed UnsupportedSyntaxError rather than mistranslated",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestPostgresOnConstraint",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestPostgresReturningIDWhichHasStringType",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:      "TestPrefixedPreloadDuplication",
		Targets:   []target{targetPgSQL},
		Reason:    "upstream:aborts-run",
		Issue:     "ends the go test process rather than failing cleanly, so it is excluded from the run; see the AbortsRun field",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:    "TestPreloadEmptyData",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestPreloadManyToManyCallbacks",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestPreloadWithAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestPreloadWithConds",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestPreloadWithDiffModel",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestPrimarykeyID",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-extension-functions",
		Issue:   "uuid_generate_v4 and friends come from PostgreSQL extensions (uuid-ossp) that qrdb does not implement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestQueryWithAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestReturningWithNullToZeroValues",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSave",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSaveHasManyCircularReference",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:      "TestSaveWithHooks",
		Targets:   []target{targetPgSQL},
		Reason:    "upstream:aborts-run",
		Issue:     "ends the go test process rather than failing cleanly, so it is excluded from the run; see the AbortsRun field",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:    "TestScannerValuer",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestScannerValuerWithFirstOrCreate",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSearchWithMap",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSelectWithCreate",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSelectWithUpdate",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSelectWithUpdateColumn",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSelectWithUpdateWithMap",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSerializer",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:gob-serializer",
		Issue:   "a value written through GORM's gob serializer does not survive the round trip. Binary round-tripping itself is intact (see TestSQL_PgSQL_BinaryRoundTripsExactly), so the fault is elsewhere in the serializer path and is not yet isolated",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSerializerAssignFirstOrCreate",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:gob-serializer",
		Issue:   "a value written through GORM's gob serializer does not survive the round trip. Binary round-tripping itself is intact (see TestSQL_PgSQL_BinaryRoundTripsExactly), so the fault is elsewhere in the serializer path and is not yet isolated",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSerializerZeroValue",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:gob-serializer",
		Issue:   "a value written through GORM's gob serializer does not survive the round trip. Binary round-tripping itself is intact (see TestSQL_PgSQL_BinaryRoundTripsExactly), so the fault is elsewhere in the serializer path and is not yet isolated",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSingleTableHasManyAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSingleTableHasManyAssociationForSlice",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSingleTableMany2ManyAssociation",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSingleTableMany2ManyAssociationForSlice",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSmartMigrateColumn",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSmartMigrateColumnGaussDB",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:pg-alter-column-form",
		Issue:   "PostgreSQL's ALTER COLUMN ... TYPE has no string-level translation: MySQL's MODIFY restates the column's whole definition. A translation was written and reverted -- it reached a provider rewrite path that regressed 41 other tests, so the syntax gap is the safer state until that path is understood (see docs/sql/upstream-gaps.md and the MODIFY COLUMN invariant test in test/integration)",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSoftDeleteReturning",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSubModel",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSubQueryWithHaving",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-having-subquery",
		Issue:   "a scalar subquery in HAVING is evaluated against the pre-aggregation row set, so an ungrouped aggregate that returns exactly one row is reported as returning many. Reproduced on a stock go-mysql-server engine with no qrdb code in the path -- docs/sql/upstream-gaps.md §5",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSupportedDialectorWithErrDuplicatedKey",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestSupportedDialectorWithErrForeignKeyViolated",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestTransactionWithHooks",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestUpdate",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestUpdateBelongsTo",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestUpdateColumnsSkipsAssociations",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:collateral-of-failed-migration",
		Issue:    "observed both passing and failing across otherwise identical runs. Its failures are \\\"table not found\\\" on a shared model an earlier migration test dropped and failed to re-create, so the outcome tracks that collateral rather than anything about this test -- see the Unstable field",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:    "TestUpdateFrom",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestUpdateFromSubQuery",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-insert-ignore-returning",
		Issue:   "go-mysql-server drops the RETURNING row for INSERT IGNORE, which is how ON CONFLICT DO NOTHING translates; qrdb refuses the statement with SQLSTATE 0A000 rather than serving the OkResult as data. Fix belongs upstream -- see docs/sql/upstream-gaps.md §1",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestUpdateHasManyAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestUpdateHasOne",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestUpdateMany2ManyAssociations",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:untriaged",
		Issue:   "measured failing on 2026-08-14 in a completed single-process run and not yet individually triaged; the run reports an assertion mismatch rather than a server error, so the cause is behavioural rather than a rejected statement",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:      "TestUpdateReturning",
		Targets:   []target{targetPgSQL},
		Reason:    "upstream:aborts-run",
		Issue:     "ends the go test process rather than failing cleanly, so it is excluded from the run; see the AbortsRun field",
		Retire:    mustParseRetire("2026-09-30"),
		AbortsRun: true,
	},
	{
		Test:    "TestUpsert",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestUpsertSliceWithReturning",
		Targets: []target{targetPgSQL},
		Reason:  "upstream:gms-ondup-returning-width",
		Issue:   "go-mysql-server emits a row wider than its declared schema for INSERT ... ON DUPLICATE KEY UPDATE ... RETURNING. Fix belongs upstream -- see docs/sql/upstream-gaps.md §2",
		Retire:  mustParseRetire("2026-09-30"),
	},
}

// mustParseRetire parses a YYYY-MM-DD date into the UTC instant of the start
// of that day. Panics on a malformed literal — every call site here is a
// compile-time-constant-shaped string literal, so a parse failure can only
// be an authoring mistake in this file, caught the moment the test binary
// is built.
func mustParseRetire(date string) time.Time {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		panic(fmt.Sprintf("qrdb_allowlist_test.go: invalid Retire date %q: %v", date, err))
	}
	return t
}

// dumpAllowlistAndExit, when GORM_QRDB_DUMP_ALLOWLIST=1, marshals allowlist
// to stdout as JSON and terminates the process, returning true. TestMain
// calls it first, before validating GORM_QRDB_TARGET, so runner.go can read
// the ratchet table without ever needing a target or starting a wire
// server. Returns false (a no-op) otherwise.
func dumpAllowlistAndExit() bool {
	if os.Getenv(dumpAllowlistEnvVar) != "1" {
		return false
	}
	type wireFailure struct {
		Test      string    `json:"test"`
		Targets   []target  `json:"targets"`
		Reason    string    `json:"reason"`
		Issue     string    `json:"issue"`
		Retire    time.Time `json:"retire"`
		AbortsRun bool      `json:"aborts_run"`
		Unstable  bool      `json:"unstable"`
	}
	out := make([]wireFailure, len(allowlist))
	for i, e := range allowlist {
		out[i] = wireFailure{Test: e.Test, Targets: e.Targets, Reason: e.Reason, Issue: e.Issue, Retire: e.Retire, AbortsRun: e.AbortsRun, Unstable: e.Unstable}
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: dumpAllowlistAndExit: encode: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
