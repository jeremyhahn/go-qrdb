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
	"go/build/constraint"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This gate answers the question mysql_dependency_test.go and its siblings
// cannot: not "does the dependency graph change under a reduced build" but
// "does every FILE under pkg/sql/ and pkg/api/etcd/ carry its own build
// constraint at all". A file with none compiles into every binary,
// unconditionally, regardless of what anything that imports it is tagged
// with -- which is exactly how `pkg/sql/internal/sessionguard/sessionguard.go`
// and `pkg/sql/pgcatalog/sleep.go` arrived untagged through an upstream
// merge: new files in an already-tagged directory, added by a change that
// never touched the tagging convention and so never tripped it.
// `make vet-tags` could not catch either file: both typecheck fine under
// every declared combination, tagged or not, because nothing in this tree
// imports them from a context that would fail to compile without `qrdb_sql`.
//
// SQL FAMILY, NOT JUST qrdb_sql. pkg/sql/mysql and pkg/sql/pgsql carry
// `qrdb_mysql` / `qrdb_pgsql` alone, not a compound
// `qrdb_mysql && qrdb_sql` -- the Makefile's WITH_QRDB_MYSQL=1 forcing
// WITH_QRDB_SQL=1 is a build-time invariant over the TAG SET, not a
// property of any one file's own constraint, and every declared
// combination in SUPPORTED_TAG_SETS already upholds it (there is no rung
// with qrdb_mysql on and qrdb_sql off). So a file under pkg/sql/ passes
// here if its constraint implies qrdb_sql, qrdb_mysql OR qrdb_pgsql --
// implying membership in the SQL family, not the one tag alone -- which
// still refuses the real defect class (no constraint at all implies
// nothing) without flagging every file mysql_dependency_test.go and
// pgsql_dependency_test.go already hold accountable on their own terms.
//
// IMPLICATION, NOT SUBSTRING MATCHING. "Carries a constraint implying
// qrdb_sql" means: for every assignment of every OTHER tag the expression
// mentions, the expression evaluates false whenever qrdb_sql (or, for the
// family check, all of qrdb_sql/qrdb_mysql/qrdb_pgsql) is false. A file
// tagged `qrdb_sql || foo` would NOT imply qrdb_sql (foo alone could select
// it) and must fail here even though the literal tag appears in the file.
// go/build/constraint, the same stdlib package `go build` itself uses to
// parse `//go:build` lines, both parses the expression and evaluates it;
// implication here is a brute-force check over the free variables the
// expression actually mentions (there are at most a handful per file, so
// 2^n is cheap).
//
// DISCOVERY FLOOR. A WalkDir that silently matched zero files would make
// every assertion below vacuously true -- the vacuous-pass hazard named
// throughout this package's other gates (#263, #271). sqlFileFloor and
// etcdFileFloor are measured counts (`find pkg/sql -name '*.go' | wc -l`,
// `find pkg/api/etcd -name '*.go' | wc -l` at the time this test was
// written); a count below either means the walk broke, not that the tree
// shrank enough to matter.
const (
	sqlDir  = "../../pkg/sql"
	etcdDir = "../../pkg/api/etcd"

	// sqlFileFloor is pkg/sql's total .go file count (every subpackage,
	// mysql/pgsql included) at the time this test was written. A bump in
	// this number updates the floor; a drop should not happen on its own.
	sqlFileFloor = 250
	// etcdFileFloor is pkg/api/etcd's total .go file count at the time
	// this test was written.
	etcdFileFloor = 24
)

// buildExprFor returns the parsed //go:build expression at the top of path,
// and whether one was found at all. A //go:build line must appear before
// the package clause (the language spec requires it), so scanning stops
// there.
func buildExprFor(t *testing.T, path string) (constraint.Expr, bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("buildgates: reading %s: %v", path, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			return nil, false
		}
		if !constraint.IsGoBuild(trimmed) {
			continue
		}
		expr, err := constraint.Parse(trimmed)
		if err != nil {
			t.Fatalf("buildgates: parsing build constraint in %s: %v", path, err)
		}
		return expr, true
	}
	return nil, false
}

// collectTagNames walks expr and adds every tag identifier it mentions to
// out.
func collectTagNames(expr constraint.Expr, out map[string]bool) {
	switch v := expr.(type) {
	case *constraint.TagExpr:
		out[v.Tag] = true
	case *constraint.NotExpr:
		collectTagNames(v.X, out)
	case *constraint.AndExpr:
		collectTagNames(v.X, out)
		collectTagNames(v.Y, out)
	case *constraint.OrExpr:
		collectTagNames(v.X, out)
		collectTagNames(v.Y, out)
	}
}

// exprImplies reports whether expr evaluates false for every assignment of
// its free tags (every tag it mentions other than those named in forced)
// whenever every tag in forced is false -- i.e. whether the expression can
// be satisfied ONLY when at least one forced tag is true.
func exprImplies(expr constraint.Expr, forced ...string) bool {
	forcedSet := make(map[string]bool, len(forced))
	for _, f := range forced {
		forcedSet[f] = true
	}

	mentioned := map[string]bool{}
	collectTagNames(expr, mentioned)

	var free []string
	for tag := range mentioned {
		if !forcedSet[tag] {
			free = append(free, tag)
		}
	}
	sort.Strings(free) // deterministic iteration; not load-bearing otherwise

	combos := 1 << len(free)
	for mask := 0; mask < combos; mask++ {
		assignment := map[string]bool{}
		for f := range forcedSet {
			assignment[f] = false
		}
		for i, tag := range free {
			assignment[tag] = mask&(1<<i) != 0
		}
		if expr.Eval(func(tag string) bool { return assignment[tag] }) {
			return false
		}
	}
	return true
}

// goFilesUnder returns every .go file under dir, relative to dir, sorted.
func goFilesUnder(t *testing.T, dir string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("buildgates: walking %s: %v", dir, err)
	}
	sort.Strings(files)
	return files
}

// TestEverySQLFileImpliesTheSQLFamilyTag asserts that every .go file under
// pkg/sql/ carries a build constraint satisfiable only when qrdb_sql,
// qrdb_mysql or qrdb_pgsql is on -- so none of it is ever compiled into a
// binary with the whole SQL family off, and an untagged file added to any
// pkg/sql subpackage is caught here rather than discovered the way
// sessionguard.go and sleep.go were.
func TestEverySQLFileImpliesTheSQLFamilyTag(t *testing.T) {
	files := goFilesUnder(t, sqlDir)
	if len(files) < sqlFileFloor {
		t.Fatalf("buildgates: found only %d .go files under %s, want at least %d -- "+
			"the walk likely broke rather than the tree shrinking", len(files), sqlDir, sqlFileFloor)
	}

	var untagged, notImplying []string
	for _, f := range files {
		expr, ok := buildExprFor(t, f)
		if !ok {
			untagged = append(untagged, f)
			continue
		}
		if !exprImplies(expr, "qrdb_sql", "qrdb_mysql", "qrdb_pgsql") {
			notImplying = append(notImplying, f)
		}
	}

	if len(untagged) > 0 {
		t.Errorf("buildgates: %d file(s) under %s carry NO build constraint at all "+
			"(compiled into every binary, SQL family on or off):\n  %s",
			len(untagged), sqlDir, strings.Join(untagged, "\n  "))
	}
	if len(notImplying) > 0 {
		t.Errorf("buildgates: %d file(s) under %s carry a build constraint that does NOT "+
			"imply qrdb_sql, qrdb_mysql or qrdb_pgsql (satisfiable with the whole SQL "+
			"family off):\n  %s", len(notImplying), sqlDir, strings.Join(notImplying, "\n  "))
	}
}

// TestEveryEtcdFileImpliesQrdbEtcd asserts that every .go file under
// pkg/api/etcd/ carries a build constraint satisfiable only when qrdb_etcd
// is on. Unlike pkg/sql, etcd has no sub-protocol family -- qrdb_etcd is
// the one tag the whole package is gated on (docs/etcd-compatibility/
// architecture.md), so the check is implication of that single tag, not a
// family of three.
func TestEveryEtcdFileImpliesQrdbEtcd(t *testing.T) {
	files := goFilesUnder(t, etcdDir)
	if len(files) < etcdFileFloor {
		t.Fatalf("buildgates: found only %d .go files under %s, want at least %d -- "+
			"the walk likely broke rather than the tree shrinking", len(files), etcdDir, etcdFileFloor)
	}

	var untagged, notImplying []string
	for _, f := range files {
		expr, ok := buildExprFor(t, f)
		if !ok {
			untagged = append(untagged, f)
			continue
		}
		if !exprImplies(expr, "qrdb_etcd") {
			notImplying = append(notImplying, f)
		}
	}

	if len(untagged) > 0 {
		t.Errorf("buildgates: %d file(s) under %s carry NO build constraint at all "+
			"(compiled into every binary, qrdb_etcd on or off):\n  %s",
			len(untagged), etcdDir, strings.Join(untagged, "\n  "))
	}
	if len(notImplying) > 0 {
		t.Errorf("buildgates: %d file(s) under %s carry a build constraint that does NOT "+
			"imply qrdb_etcd (satisfiable with qrdb_etcd off):\n  %s",
			len(notImplying), etcdDir, strings.Join(notImplying, "\n  "))
	}
}
