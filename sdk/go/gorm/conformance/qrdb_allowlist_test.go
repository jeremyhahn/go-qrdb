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
		Test:    "TestGenericsNestedPreloads",
		Targets: []target{targetNode},
		Reason:  "upstream:dialector-type-assertion",
		Issue:   "NOT a qrdb gap and not fixable from qrdb: the upstream test does DB.Dialector.(*mysql.Dialector).ServerVersion guarded only by DB.Dialector.Name() == \"mysql\", so it panics for ANY third-party dialector that reports the mysql name -- which sdk/go/gorm deliberately does (see sdk/go/gorm/doc.go). Scoped to node alone: under the mysql WIRE target the dialector genuinely IS gorm.io/driver/mysql's and this test passes. Retiring it needs a decision not taken here -- substitute generics_test.go through GORM_SUITE.lock's substitutes[], or stop reporting \"mysql\" (an SDK-wide behaviour change); see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestGenericsPreloads",
		Targets: []target{targetNode},
		Reason:  "upstream:dialector-type-assertion",
		Issue:   "NOT a qrdb gap and not fixable from qrdb: the upstream test does DB.Dialector.(*mysql.Dialector).ServerVersion guarded only by DB.Dialector.Name() == \"mysql\", so it panics for ANY third-party dialector that reports the mysql name -- which sdk/go/gorm deliberately does (see sdk/go/gorm/doc.go). Scoped to node alone: under the mysql WIRE target the dialector genuinely IS gorm.io/driver/mysql's and this test passes. Retiring it needs a decision not taken here -- substitute generics_test.go through GORM_SUITE.lock's substitutes[], or stop reporting \"mysql\" (an SDK-wide behaviour change); see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
	},
	{
		Test:    "TestHasManyAssociation",
		Targets: []target{targetNode},
		Reason:  "qrdb:relational-gap",
		Issue:   "not yet individually triaged: seeded when this target had no relational catalog at all, and still failing after the node:// target gave it one and after FOREIGN KEY support landed in pkg/sql/provider (which retired 117 of the original 138 entries). What remains is no longer one shared blocker; the recurring themes are many2many/nested-preload behaviour, ALTER TABLE column-type migration, and unique-constraint reporting; see conformance/README.md",
		Retire:  mustParseRetire("2026-09-30"),
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
	{
		Test:    "TestSingleTableMany2ManyAssociation",
		Targets: []target{targetNode},
		Reason:  "qrdb:relational-gap",
		Issue:   "not yet individually triaged: seeded when this target had no relational catalog at all, and still failing after the node:// target gave it one and after FOREIGN KEY support landed in pkg/sql/provider (which retired 117 of the original 138 entries). What remains is no longer one shared blocker; the recurring themes are many2many/nested-preload behaviour, ALTER TABLE column-type migration, and unique-constraint reporting; see conformance/README.md",
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
		Test    string    `json:"test"`
		Targets []target  `json:"targets"`
		Reason  string    `json:"reason"`
		Issue   string    `json:"issue"`
		Retire  time.Time `json:"retire"`
	}
	out := make([]wireFailure, len(allowlist))
	for i, e := range allowlist {
		out[i] = wireFailure{Test: e.Test, Targets: e.Targets, Reason: e.Reason, Issue: e.Issue, Retire: e.Retire}
	}
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: dumpAllowlistAndExit: encode: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
	return true
}
