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

package buildgates

import (
	"bytes"
	"os/exec"
	"sort"
	"strings"
	"testing"
)

// This gate answers "does WITH_QRDB_MYSQL / WITH_QRDB_SQL actually remove
// vitess and go-mysql-server from the binaries, or only from one package".
//
// An audit of this tree found `pkg/api/errclass` importing
// `github.com/dolthub/vitess/go/mysql` with no build constraint at all --
// which linked vitess into EVERY build, including one with every SQL flag
// off, because errclass sits in every transport's import graph. A `go vet`
// of errclass alone could not see that: the file typechecked fine, same as
// it does today under the tag that caused the leak. The property that
// mattered was never "does this package compile", it was "what is in the
// binary's dependency graph", and that is a property of `go list -deps`
// over the ENTRY POINT, not of any one package in the middle.
//
// The three build-tag combinations below are DATA this file does not author
// twice: fullTags is read from `make print-build-tags`, the Makefile's own
// single declaration of the default set (go-qrdb#263), and the two reduced
// sets are computed by filtering it here with the same removals the
// Makefile's NOMYSQL_TAGS / NOSQLFAMILY_TAGS apply (mysql_dependency_test.go
// and the Makefile are both downstream of BUILD_TAGS; recomputing the
// filter here rather than re-reading NOMYSQL_TAGS keeps this gate able to
// run as a plain `go test`, with no Make invocation on the hot path other
// than the one call that resolves fullTags itself).
//
// WHY A DISCOVERY FLOOR. Every assertion below either requires a package to
// be ABSENT or requires one to be PRESENT. An absence assertion that passes
// because the whole scan matched nothing is the vacuous-pass failure named
// throughout this package's other gates (#263, #271) -- so before any
// absence is trusted, the full-tags rung must be shown to contain vitess,
// go-mysql-server AND pkg/sql/mysql. If that floor ever reads empty, the
// scan broke, not the tree.
const (
	// vitessPkgPrefix is every package vitess exports, the module path
	// go-mysql-server's go.mod pins as `github.com/dolthub/vitess`.
	vitessPkgPrefix = "github.com/dolthub/vitess/"
	// gmsPkgPrefix is every package go-mysql-server exports.
	gmsPkgPrefix = "github.com/dolthub/go-mysql-server"
	// gmsServerPkg is the ONE go-mysql-server package that constructs the
	// vitess listener (`mysql.NewListenerWithConfig`, confirmed by reading
	// its source at the pinned version): if this package is unreachable,
	// nothing in the binary can start a MySQL listener, whatever vitess
	// packages GMS's SQL core still pulls in for parsing or its error type.
	gmsServerPkg = "github.com/dolthub/go-mysql-server/server"
	// qrdbMySQLPkg is qrdb's own MySQL wire server.
	qrdbMySQLPkg = "github.com/jeremyhahn/go-qrdb/pkg/sql/mysql"

	// minVitessPackages is the floor for how many vitess packages the full
	// tag set must reach through cmd/qrdbd. Measured at 20 (go.mod pins
	// v0.0.0-20260728212736-0542037326d7); a deliberate vitess bump that
	// moves this materially belongs in the same commit as the new number.
	minVitessPackages = 15
)

// mysqlGateTargets are the two binaries the hard limit names explicitly.
var mysqlGateTargets = []string{"./cmd/qrdbd", "./cmd/qrdb"}

// fullBuildTags asks the Makefile for its own declaration of BUILD_TAGS
// (`make print-build-tags`) rather than restating the WITH_* defaults here,
// so a flag the Makefile adds or removes changes this gate's floor without
// anyone having to remember a second place that lists them.
func fullBuildTags(t *testing.T) []string {
	t.Helper()
	root := repoRoot(t)
	cmd := exec.Command("make", "-s", "print-build-tags")
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("make -s print-build-tags: %v\n%s", err, errOut.String())
	}
	tags := strings.Fields(out.String())
	if len(tags) == 0 {
		t.Fatalf("make -s print-build-tags printed nothing; cannot derive the gate's tag sets")
	}
	return tags
}

// withoutTags returns tags with every entry in drop removed, preserving
// order, mirroring the Makefile's $(filter-out ...) construction of
// NOMYSQL_TAGS / NOSQLFAMILY_TAGS.
func withoutTags(tags []string, drop ...string) []string {
	dropSet := make(map[string]bool, len(drop))
	for _, d := range drop {
		dropSet[d] = true
	}
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !dropSet[tag] {
			out = append(out, tag)
		}
	}
	return out
}

// goListDeps runs `go list -deps -tags <tags> <target>` from the repository
// root and returns the import paths it printed, one per line, exactly as
// `go list` ordered them.
func goListDeps(t *testing.T, tags []string, target string) []string {
	t.Helper()
	root := repoRoot(t)
	args := []string{"list", "-deps"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, target)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, errOut.String())
	}
	var deps []string
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			deps = append(deps, line)
		}
	}
	return deps
}

// containsPrefix reports whether any entry in deps has the given prefix.
func containsPrefix(deps []string, prefix string) bool {
	for _, d := range deps {
		if strings.HasPrefix(d, prefix) {
			return true
		}
	}
	return false
}

// countPrefix counts entries in deps with the given prefix.
func countPrefix(deps []string, prefix string) int {
	n := 0
	for _, d := range deps {
		if strings.HasPrefix(d, prefix) {
			n++
		}
	}
	return n
}

// contains reports whether deps has the exact import path pkg.
func contains(deps []string, pkg string) bool {
	for _, d := range deps {
		if d == pkg {
			return true
		}
	}
	return false
}

// TestFullBuildDiscoversVitessAndMySQL is the floor every absence assertion
// in this file depends on: under the DEFAULT tag set, both binaries must
// actually reach vitess, go-mysql-server and qrdb's own MySQL package. If
// this test fails, every other test in this file is checking an absence
// that would have been "found" whatever the tree looked like.
func TestFullBuildDiscoversVitessAndMySQL(t *testing.T) {
	full := fullBuildTags(t)
	if !contains(full, "qrdb_mysql") || !contains(full, "qrdb_sql") || !contains(full, "qrdb_pgsql") {
		t.Fatalf("default build tags %v do not include qrdb_sql, qrdb_mysql and qrdb_pgsql; "+
			"this gate's floor assumes the Makefile defaults enable all three", full)
	}
	for _, target := range mysqlGateTargets {
		deps := goListDeps(t, full, target)
		if n := countPrefix(deps, vitessPkgPrefix); n < minVitessPackages {
			t.Errorf("%s under the default tags reaches only %d vitess packages, floor is %d; "+
				"a scan finding fewer than this cannot be trusted to detect a real absence",
				target, n, minVitessPackages)
		}
		if !containsPrefix(deps, gmsPkgPrefix) {
			t.Errorf("%s under the default tags does not reach go-mysql-server at all", target)
		}
		if !contains(deps, gmsServerPkg) {
			t.Errorf("%s under the default tags does not reach %s", target, gmsServerPkg)
		}
		if !contains(deps, qrdbMySQLPkg) {
			t.Errorf("%s under the default tags does not reach %s", target, qrdbMySQLPkg)
		}
	}
}

// TestNoMySQLBuildHasNoMySQLWireOrListener is requirement 2 of the owner's
// ruling: with qrdb_mysql off but qrdb_sql and qrdb_pgsql still on, qrdb's
// own MySQL wire package must be gone, and so must the one go-mysql-server
// package that can construct a vitess MySQL listener -- vitess's SQLError
// type and parser legitimately remain, reached through go-mysql-server's SQL
// core, which this test does not assert away (see the hard-limit comment on
// TestNoSQLBuildHasNoVitessOrMySQLServer for why that remainder is accepted,
// not a leak).
func TestNoMySQLBuildHasNoMySQLWireOrListener(t *testing.T) {
	tags := withoutTags(fullBuildTags(t), "qrdb_mysql")
	if contains(tags, "qrdb_mysql") {
		t.Fatalf("withoutTags did not remove qrdb_mysql from %v", tags)
	}
	if !contains(tags, "qrdb_sql") || !contains(tags, "qrdb_pgsql") {
		t.Fatalf("tags %v lost qrdb_sql/qrdb_pgsql as a side effect of removing qrdb_mysql; "+
			"the NOMYSQL rung is supposed to keep SQL and PG on", tags)
	}
	for _, target := range mysqlGateTargets {
		deps := goListDeps(t, tags, target)
		if contains(deps, qrdbMySQLPkg) {
			t.Errorf("%s without qrdb_mysql still reaches %s", target, qrdbMySQLPkg)
		}
		if contains(deps, gmsServerPkg) {
			t.Errorf("%s without qrdb_mysql still reaches %s, which is how a vitess MySQL "+
				"listener gets constructed (mysql.NewListenerWithConfig) -- the MySQL wire "+
				"protocol would still be reachable with the tag that is supposed to remove it",
				target, gmsServerPkg)
		}
	}
}

// TestNoSQLBuildHasNoVitessOrMySQLServer is requirement 1: with qrdb_sql,
// qrdb_mysql and qrdb_pgsql ALL off, neither vitess nor go-mysql-server may
// appear anywhere in the dependency graph of either binary. This is the one
// combination where the hard limit on this layering says absence is fully
// achievable -- go-mysql-server (and therefore vitess) is only ever reached
// through the shared pkg/sql core, and that core compiles only under
// qrdb_sql.
func TestNoSQLBuildHasNoVitessOrMySQLServer(t *testing.T) {
	tags := withoutTags(fullBuildTags(t), "qrdb_mysql", "qrdb_pgsql", "qrdb_sql")
	for _, dropped := range []string{"qrdb_mysql", "qrdb_pgsql", "qrdb_sql"} {
		if contains(tags, dropped) {
			t.Fatalf("withoutTags did not remove %s from %v", dropped, tags)
		}
	}
	for _, target := range mysqlGateTargets {
		deps := goListDeps(t, tags, target)
		var leaked []string
		for _, d := range deps {
			if strings.HasPrefix(d, vitessPkgPrefix) || strings.HasPrefix(d, gmsPkgPrefix) {
				leaked = append(leaked, d)
			}
		}
		if len(leaked) > 0 {
			sort.Strings(leaked)
			t.Errorf("%s with qrdb_sql, qrdb_mysql and qrdb_pgsql all off still reaches "+
				"vitess/go-mysql-server packages, which must be fully absent from this build:\n  %s",
				target, strings.Join(leaked, "\n  "))
		}
		if contains(deps, qrdbMySQLPkg) {
			t.Errorf("%s with SQL off still reaches %s", target, qrdbMySQLPkg)
		}
	}
}
