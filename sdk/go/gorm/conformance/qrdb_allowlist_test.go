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

	// AbortsRun marks a test that terminates the go test PROCESS rather than
	// failing — a panic outside the test goroutine, a fatal runtime error, or
	// a hang the timeout kills. Every test after it in the run then produces
	// no result at all, so the whole suite reports a PREFIX of itself.
	//
	// runner.go excludes these with `go test -skip` so the remaining suite
	// can be measured. Without the exclusion the run TRUNCATES: dropping this
	// field once took the node target from 395 tests to 139 while still
	// reporting "0 unexpected failures", which is the most dangerous shape a
	// green-looking gate can have.
	AbortsRun bool

	// Unstable marks a test whose outcome tracks earlier collateral in the
	// same run rather than its own behaviour — it passes or fails depending
	// on what ran before it. runner.go counts these separately (as
	// unstable-passed) instead of treating a pass as a stale allowlist entry,
	// so an entry here does not have to be deleted the first time it happens
	// to succeed.
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
		Test:     "TestConcurrentMany2ManyAssociation",
		Targets:  []target{targetNode},
		Reason:   "qrdb:concurrent-association-write",
		Issue:    "concurrent Append to a many2many association races: measured as a serialization failure losing one of three writes on the mysql target, and ORDER-DEPENDENT on all three -- it failed in the seeding run and passes in others. Marked Unstable so neither outcome breaks the gate. Same underlying join-table serialization gap as the mysql and pgsql entries; transparent retry is not the fix (see memory tunable-conflict-detection)",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
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
	// ---------------------------------------------------------------------
	// Wire-target entries, triaged individually from a 2026-08-16 run.
	//
	// Each names the exact engine error it fails on, so retiring it is a
	// matter of re-running that one test rather than re-deriving why it was
	// ever listed. None is a blanket "relational gap".
	// ---------------------------------------------------------------------
	{
		Test:    "TestConcurrentMany2ManyAssociation",
		Targets: []target{targetMySQL},
		Reason:  "qrdb:concurrent-association-write",
		Issue:   "concurrent Append to a many2many association loses a write: `Error 1213 (40001): serialization failure`, then 2 of 3 languages present. The join-table inserts collide under the session's conflict detection and one is aborted rather than serialized. Transparent retry is NOT the fix -- the write-set holds ops, not statements (see memory tunable-conflict-detection); this needs pessimistic locking on the join table",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:     "TestSelectWithUpdate",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:order-dependent",
		Issue:    "the same order-dependent flake the mysql entry below documents, observed on the pgsql target on 2026-08-16 in one run out of four otherwise-identical full-suite runs (it passed in the three preceding). Marked Unstable for the same reason as the mysql entry",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestSelectWithUpdate",
		Targets:  []target{targetMySQL},
		Reason:   "qrdb:order-dependent",
		Issue:    "order-dependent, not a stable failure: GORM_SUITE.lock's budgets_comment records it appearing in ONE of three consecutive whole-suite runs, which is why mysql's ceiling was set to the union (12) rather than the stable count (11). Marked Unstable so a run where it passes is reported as unstable-passed instead of failing the ratchet as a stale entry -- deleting it on the first green run would just reinstate the flake later",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	// ---------------------------------------------------------------------
	// pgsql, re-derived 2026-08-16 against the live target.
	//
	// The 7 AbortsRun entries are the CONVERGED set, discovered by running the
	// suite repeatedly and adding whichever test the process died in until a run
	// reached process exit. GORM_SUITE.lock's 13-entry list is STALE: the tests
	// that no longer abort stopped aborting because the tables they needed now
	// get created, which is exactly the re-derivation its budgets_comment asks
	// for. Carrying the stale members would -skip tests that now run, hiding
	// their real outcome and spending budget on nothing.
	//
	// 7 aborters + 133 failures = 140 entries. 63 of the failures are the two
	// go-mysql-server RETURNING defects in docs/sql/upstream-gaps.md and can
	// only retire upstream.
	// ---------------------------------------------------------------------

	// Measured failures. Listed as a block and NOT individually triaged -- the
	// Reason says so, so they are not mistaken for the entries above, each of
	// which names its exact engine error.
	{
		Test:     "TestFindOrCreate",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestNamedPolymorphic",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestNestedPreloadForSlice",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestNestedPreloadWithConds",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestPreloadWithDiffModel",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestSaveWithPrimaryValue",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestTransactionWithHooks",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestUpdateColumnsSkipsAssociations",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestUpsertSlice",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestUpsertWithSave",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:pgsql-wire-gap",
		Issue:    "measured failing on the pgsql wire target in the converged 2026-08-16 whole-suite run. Not individually triaged; the known large cluster is the two go-mysql-server RETURNING defects covering 63 of these, see docs/sql/upstream-gaps.md",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	},
	{
		Test:     "TestConcurrentMany2ManyAssociation",
		Targets:  []target{targetPgSQL},
		Reason:   "qrdb:concurrent-association-write",
		Issue:    "same concurrent many2many Append defect listed for mysql, and order-dependent on this target: it failed in the converged discovery run and passed in the ratchet run. Marked Unstable so neither outcome breaks the gate while the underlying join-table serialization gap is open",
		Retire:   mustParseRetire("2026-09-30"),
		Unstable: true,
	}, {
		Test:    "TestMigrateArrayTypeModel",
		Targets: []target{targetPgSQL},
		Reason:  "qrdb:array-introspection-spelling",
		Issue:   "array VALUES round-trip (text[] columns store the PostgreSQL array literal in text storage and lib/pq's array types parse it back; TestPostgres exercises that and passes), but INTROSPECTION reports the storage type: udt_name/format_type answer text where PostgreSQL answers text[]/_text, and this test asserts the spelling. Serving the array spelling needs the declared PG type persisted per column in the relational catalog (a ColumnDef field captured on the pgsql wire, mirrored into pgcatalog's columns/format_type), which is metadata fidelity work no functional path depends on",
		Retire:  mustParseRetire("2026-12-31"),
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

// wireFailure is the JSON shape runner.go decodes. Its field names are the
// contract between the two processes: runner.go re-declares this struct and
// nothing makes the two agree, so a field dropped here decodes to its zero
// value there in SILENCE. That is how aborts_run and unstable were lost on
// 2026-08-15, taking the run-abort exclusion with them and truncating the
// node suite from 396 tests to 139 while still reporting zero unexpected
// failures. TestAllowlistWireFormatCarriesEveryField guards it.
type wireFailure struct {
	Test      string    `json:"test"`
	Targets   []target  `json:"targets"`
	Reason    string    `json:"reason"`
	Issue     string    `json:"issue"`
	Retire    time.Time `json:"retire"`
	AbortsRun bool      `json:"aborts_run"`
	Unstable  bool      `json:"unstable"`
}

// allowlistWireForm converts entries into the form runner.go decodes.
func allowlistWireForm(entries []expectedFailure) []wireFailure {
	out := make([]wireFailure, len(entries))
	for i, e := range entries {
		out[i] = wireFailure{
			Test:      e.Test,
			Targets:   e.Targets,
			Reason:    e.Reason,
			Issue:     e.Issue,
			Retire:    e.Retire,
			AbortsRun: e.AbortsRun,
			Unstable:  e.Unstable,
		}
	}
	return out
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
	if err := json.NewEncoder(os.Stdout).Encode(allowlistWireForm(allowlist)); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: dumpAllowlistAndExit: encode: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
