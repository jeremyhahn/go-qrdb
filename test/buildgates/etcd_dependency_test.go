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

// This gate answers "does WITH_QRDB_ETCD actually remove the etcd wire
// protocol -- and go.etcd.io itself -- from the binaries". It shares
// fullBuildTags, withoutTags, goListDeps and the discovery-floor helpers
// with mysql_dependency_test.go; see its header comment for why the floor
// matters.
//
// Unlike MySQL and PostgreSQL, etcd is a SINGLE family with no shared engine
// underneath it: pkg/api/etcd is qrdb's entire etcd wire server, every file
// in it tagged qrdb_etcd directly (no import-site gating, matching the
// pkg/sql treatment), and go.etcd.io/* is reached from nowhere else in this
// module. So removing qrdb_etcd is expected to remove BOTH the package and
// the upstream dependency entirely -- there is no "etcd core stays, only the
// listener goes" middle rung the way NOMYSQL has one for vitess.

const (
	// etcdPkgPrefix is every package go.etcd.io exports, reached in this
	// module only through pkg/api/etcd (the server-side wire protocol) --
	// test/performance/etcd-benchmark pins its own go.etcd.io/etcd/client/v3
	// in a SEPARATE go.mod, deliberately against a released version, and is
	// not part of this module's dependency graph.
	etcdPkgPrefix = "go.etcd.io/etcd/"
	// qrdbEtcdPkg is qrdb's own etcd wire server.
	qrdbEtcdPkg = "github.com/jeremyhahn/go-qrdb/pkg/api/etcd"
	// minEtcdPackages is the floor for how many go.etcd.io packages the full
	// tag set must reach through cmd/qrdbd. Measured at 1 (go.mod pins
	// go.etcd.io/etcd/api/v3 v3.7.2); a deliberate etcd client bump that adds
	// more belongs in the same commit as the new number.
	minEtcdPackages = 1
)

// TestFullBuildDiscoversEtcdAndGoEtcdIO is the floor every absence assertion
// in this file depends on.
func TestFullBuildDiscoversEtcdAndGoEtcdIO(t *testing.T) {
	full := fullBuildTags(t)
	if !contains(full, "qrdb_etcd") {
		t.Fatalf("default build tags %v do not include qrdb_etcd; this gate's floor "+
			"assumes the Makefile default enables it", full)
	}
	for _, target := range mysqlGateTargets {
		deps := goListDeps(t, full, target)
		if n := countPrefix(deps, etcdPkgPrefix); n < minEtcdPackages {
			t.Errorf("%s under the default tags reaches only %d go.etcd.io packages, floor is %d",
				target, n, minEtcdPackages)
		}
		if !contains(deps, qrdbEtcdPkg) {
			t.Errorf("%s under the default tags does not reach %s", target, qrdbEtcdPkg)
		}
	}
}

// TestNoEtcdBuildHasNoEtcdPackageOrDependency is the hard requirement for
// this family: with qrdb_etcd off, qrdb's own etcd wire package AND
// go.etcd.io itself must both be fully absent from the dependency graph of
// both binaries.
func TestNoEtcdBuildHasNoEtcdPackageOrDependency(t *testing.T) {
	tags := withoutTags(fullBuildTags(t), "qrdb_etcd")
	if contains(tags, "qrdb_etcd") {
		t.Fatalf("withoutTags did not remove qrdb_etcd from %v", tags)
	}
	for _, target := range mysqlGateTargets {
		deps := goListDeps(t, tags, target)
		if contains(deps, qrdbEtcdPkg) {
			t.Errorf("%s without qrdb_etcd still reaches %s", target, qrdbEtcdPkg)
		}
		if containsPrefix(deps, etcdPkgPrefix) {
			t.Errorf("%s with qrdb_etcd off still reaches a go.etcd.io package, which "+
				"must be fully absent from this build", target)
		}
	}
}
