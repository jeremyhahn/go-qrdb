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

import "testing"

// This gate is PostgreSQL's mirror of mysql_dependency_test.go: does
// WITH_QRDB_PGSQL actually remove the PostgreSQL wire protocol from the
// binaries, or only from one package. It shares fullBuildTags, withoutTags,
// goListDeps and the discovery-floor helpers with that file; see its header
// comment for why the floor matters and why the tag sets are computed here
// rather than re-read from the Makefile's NOPGSQL_TAGS.

const (
	// psqlWirePkgPrefix is every package the PostgreSQL wire framing
	// library exports. Unlike MySQL, which is served by go-mysql-server's
	// OWN server package calling into vitess, PostgreSQL framing is a
	// separate upstream dependency with no go-mysql-server intermediary:
	// pkg/sql/pgsql/server.go calls it directly.
	psqlWirePkgPrefix = "github.com/jeroenrinzema/psql-wire"
	// qrdbPgSQLPkg is qrdb's own PostgreSQL wire server.
	qrdbPgSQLPkg = "github.com/jeremyhahn/go-qrdb/pkg/sql/pgsql"
)

// TestFullBuildDiscoversPsqlWireAndPgSQL is the floor TestNoPgSQLBuildHasNoPsqlWireOrListener
// depends on: under the default tags, both binaries must actually reach
// psql-wire and qrdb's own PostgreSQL package.
func TestFullBuildDiscoversPsqlWireAndPgSQL(t *testing.T) {
	full := fullBuildTags(t)
	if !contains(full, "qrdb_pgsql") {
		t.Fatalf("default build tags %v do not include qrdb_pgsql; this gate's floor "+
			"assumes the Makefile default enables it", full)
	}
	for _, target := range mysqlGateTargets {
		deps := goListDeps(t, full, target)
		if !containsPrefix(deps, psqlWirePkgPrefix) {
			t.Errorf("%s under the default tags does not reach psql-wire (%s) at all", target, psqlWirePkgPrefix)
		}
		if !contains(deps, qrdbPgSQLPkg) {
			t.Errorf("%s under the default tags does not reach %s", target, qrdbPgSQLPkg)
		}
	}
}

// TestNoPgSQLBuildHasNoPsqlWireOrListener is PostgreSQL's requirement 2: with
// qrdb_pgsql off but qrdb_sql and qrdb_mysql still on, qrdb's own PostgreSQL
// wire package must be gone, and so must psql-wire itself -- unlike vitess
// under NOMYSQL, psql-wire is not shared by go-mysql-server's MySQL path, so
// nothing legitimately keeps it once qrdb_pgsql is off.
func TestNoPgSQLBuildHasNoPsqlWireOrListener(t *testing.T) {
	tags := withoutTags(fullBuildTags(t), "qrdb_pgsql")
	if contains(tags, "qrdb_pgsql") {
		t.Fatalf("withoutTags did not remove qrdb_pgsql from %v", tags)
	}
	if !contains(tags, "qrdb_sql") || !contains(tags, "qrdb_mysql") {
		t.Fatalf("tags %v lost qrdb_sql/qrdb_mysql as a side effect of removing qrdb_pgsql; "+
			"the NOPGSQL rung is supposed to keep SQL and MySQL on", tags)
	}
	for _, target := range mysqlGateTargets {
		deps := goListDeps(t, tags, target)
		if contains(deps, qrdbPgSQLPkg) {
			t.Errorf("%s without qrdb_pgsql still reaches %s", target, qrdbPgSQLPkg)
		}
		if containsPrefix(deps, psqlWirePkgPrefix) {
			t.Errorf("%s without qrdb_pgsql still reaches psql-wire (%s) -- the PostgreSQL "+
				"wire protocol would still be reachable with the tag that is supposed to "+
				"remove it", target, psqlWirePkgPrefix)
		}
	}
}
