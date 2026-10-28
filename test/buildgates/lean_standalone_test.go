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
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// leanStandaloneFixture is the smallest useful embedding of QRDB: one
// standalone node on the file backend, the embedded transport and an entity
// DAO. Whatever it compiles, every application embedding QRDB that way
// compiles.
const leanStandaloneFixture = localModule + "/sdk/go/examples/leanstandalone"

// leanStandaloneTags is the build-tag set the fixture is measured under: the
// one tag the DAO needs at run time (its JSON codec), and nothing a plain
// `go build` by an embedder would not have.
const leanStandaloneTags = "codec_json"

// leanRule forbids the package at path, the packages beneath it at a
// path-segment boundary, or both: {path: pkg/api, subpackages: true} forbids
// pkg/api/rest and neither pkg/api nor pkg/apimodule.
type leanRule struct {
	path        string
	self        bool
	subpackages bool
}

// matches reports whether importPath falls under r.
func (r leanRule) matches(importPath string) bool {
	if importPath == r.path {
		return r.self
	}
	return r.subpackages && strings.HasPrefix(importPath, r.path+"/")
}

// leanStandaloneForbidden lists what a lean standalone embedder must not
// compile: features it does not use. Ruling 193 narrowed it to the Raft
// engine and the pkg/api subpackages on the go-quicraft and pkg/api sides.
var leanStandaloneForbidden = []leanRule{
	// Only go-quicraft's Raft engine: the NodeHost package and the internal
	// packages beneath it. Its seal and Shamir primitives, and the other public
	// leaf packages, cost 1-13 dependencies and pull no engine (ruling 193 D).
	{path: "github.com/jeremyhahn/go-quicraft/pkg", self: true},
	{path: "github.com/jeremyhahn/go-quicraft/pkg/internal", self: true, subpackages: true},
	{path: "github.com/cockroachdb/pebble", self: true, subpackages: true},
	// The protocol servers beneath pkg/api, not the contract types in its root
	// that pkg/mvcc needs (ruling 193 E).
	{path: localModule + "/pkg/api", subpackages: true},
	{path: localModule + "/pkg/server", self: true, subpackages: true},
	{path: localModule + "/pkg/cluster", self: true, subpackages: true},
	{path: localModule + "/pkg/vector", self: true, subpackages: true},
	{path: localModule + "/pkg/timeseries", self: true, subpackages: true},
	{path: "github.com/mark3labs/mcp-go", self: true, subpackages: true},
	{path: "github.com/prometheus/client_golang", self: true, subpackages: true},
	{path: "github.com/gin-gonic/gin", self: true, subpackages: true},
	{path: "github.com/go-chi/chi", self: true, subpackages: true},
	{path: "github.com/jeremyhahn/go-objstore", self: true, subpackages: true},
}

// leanStandaloneEdges is the ratchet: every import edge by which the fixture
// still reaches a forbidden package, written "importer -> imported" with this
// module's prefix trimmed. The target is an empty table. A new edge fails the
// gate, and so does a listed edge the fixture no longer has -- delete it in the
// change that removes it, so the table only shrinks.
var leanStandaloneEdges = map[string]bool{
	// The API-module registry types the chi router and the MCP server.
	"pkg/apimodule -> github.com/go-chi/chi/v5":           true,
	"pkg/apimodule -> github.com/mark3labs/mcp-go/server": true,

	// Sealed-window contract types.
	"pkg/auth -> pkg/api/sealedwindow": true,

	// The gRPC proto and chi types in the shared auth chain.
	"pkg/auth/middleware -> github.com/go-chi/chi/v5": true,

	// The raftadmin.Host interface LocalBackend implements is typed by the
	// quicraft NodeHost.
	"pkg/backend -> github.com/jeremyhahn/go-quicraft/pkg": true,

	// The Raft backend, reached through pkg/service.
	"pkg/backend/raft -> github.com/jeremyhahn/go-quicraft/pkg": true,
	"pkg/backend/raft -> pkg/cluster/featuregate":               true,

	// Sealed-window contract types.
	"pkg/ceremony -> pkg/api/sealedwindow": true,

	// Cluster feature gates and membership.
	"pkg/clusterenroll -> pkg/cluster/featuregate": true,
	"pkg/clusterseal -> pkg/cluster/featuregate":   true,
	"pkg/joinadvertise -> pkg/cluster":             true,
	"pkg/joinadvertise -> pkg/cluster/featuregate": true,
	"pkg/replicaforward -> pkg/cluster":            true,

	// The per-database barrier WAL is quicraft logdb on pebble, opened through
	// the NodeHost package.
	"pkg/database -> github.com/cockroachdb/pebble/v2":      true,
	"pkg/database -> github.com/jeremyhahn/go-quicraft/pkg": true,

	// Vector and time-series engines.
	"pkg/engine -> pkg/timeseries": true,
	"pkg/engine -> pkg/vector":     true,

	// The pebble.DB index the Raft state machine in pkg/store builds; reached
	// only through pkg/store.
	"pkg/index/pebbleindex -> github.com/cockroachdb/pebble/v2": true,

	// The Raft-admin host (#627): membership, snapshots and shard start and
	// stop are typed by the quicraft NodeHost. pkg/service still composes it
	// into AdminService, so a lean embedder compiles it until the admin
	// services leave pkg/service.
	"pkg/raftadmin -> github.com/jeremyhahn/go-quicraft/pkg": true,

	// Every feature service is compiled into one package.
	"pkg/service -> github.com/jeremyhahn/go-quicraft/pkg":          true,
	"pkg/service -> github.com/prometheus/client_golang/prometheus": true,
	"pkg/service -> pkg/cluster":                                    true,
	"pkg/service -> pkg/cluster/featuregate":                        true,
	"pkg/service -> pkg/timeseries":                                 true,
	"pkg/service -> pkg/vector":                                     true,
	"pkg/service -> pkg/vector/hnsw":                                true,

	// Vector and time-series engines.
	"pkg/statemachine -> pkg/timeseries":  true,
	"pkg/statemachine -> pkg/vector/hnsw": true,

	// The object-store backend, compiled by server/lifecycle.
	"pkg/storage/objstore -> github.com/jeremyhahn/go-objstore/pkg/common":  true,
	"pkg/storage/objstore -> github.com/jeremyhahn/go-objstore/pkg/factory": true,

	// The pebble storage backend, compiled by every node.
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2":         true,
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2/bloom":   true,
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2/sstable": true,
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2/vfs":     true,

	// Vector storage, reached through pkg/service.
	"pkg/storage/vector -> github.com/cockroachdb/pebble/v2":         true,
	"pkg/storage/vector -> github.com/cockroachdb/pebble/v2/bloom":   true,
	"pkg/storage/vector -> github.com/cockroachdb/pebble/v2/sstable": true,

	// The pebble store; time series.
	"pkg/store -> github.com/cockroachdb/pebble/v2": true,
	"pkg/store -> pkg/timeseries":                   true,

	// Prometheus metrics, reached through pkg/service.
	"pkg/util/metrics -> github.com/prometheus/client_golang/prometheus":            true,
	"pkg/util/metrics -> github.com/prometheus/client_golang/prometheus/collectors": true,
	"pkg/util/metrics -> github.com/prometheus/client_golang/prometheus/promauto":   true,

	// The SDK root's client API: the MCP error-envelope wire types, and the
	// cluster and feature-gate methods on *sdk.Client (#627 slice B moved
	// every server, storage and engine re-export out of the root).
	"sdk/go -> pkg/api/mcp/wire":        true,
	"sdk/go -> pkg/cluster":             true,
	"sdk/go -> pkg/cluster/featuregate": true,

	// The node composer builds every mode and backend in one package.
	"sdk/go/node -> github.com/cockroachdb/pebble/v2":      true,
	"sdk/go/node -> github.com/cockroachdb/pebble/v2/vfs":  true,
	"sdk/go/node -> github.com/jeremyhahn/go-quicraft/pkg": true,
	"sdk/go/node -> pkg/cluster":                           true,
	"sdk/go/node -> pkg/cluster/featuregate":               true,
	"sdk/go/node -> pkg/server":                            true,
	"sdk/go/node -> pkg/server/lifecycle":                  true,

	// The object-store pool manager seam; time series; API contract types.
	"sdk/go/transport/embedded -> github.com/jeremyhahn/go-objstore/pkg/pool": true,
	"sdk/go/transport/embedded -> pkg/api/dbwire":                             true,
	"sdk/go/transport/embedded -> pkg/api/dekwire":                            true,
	"sdk/go/transport/embedded -> pkg/api/errclass":                           true,
	"sdk/go/transport/embedded -> pkg/api/holderwire":                         true,
	"sdk/go/transport/embedded -> pkg/api/sealedwindow":                       true,
	"sdk/go/transport/embedded -> pkg/timeseries":                             true,

	// The transport proto converters and the QUIC transport, reached through
	// the SDK root. policywire is public because the gRPC and QUIC servers
	// convert through it too.
	"sdk/go/transport/internal/collectionconv -> pkg/api/grpc/proto/qrdbv1": true,
	"sdk/go/transport/internal/compactionconv -> pkg/api/grpc/proto/qrdbv1": true,
	"sdk/go/transport/internal/dekconv -> pkg/api/grpc/proto/qrdbv1":        true,
	"sdk/go/transport/internal/enrollconv -> pkg/api/grpc/proto/qrdbv1":     true,
	"sdk/go/transport/internal/holderconv -> pkg/api/grpc/proto/qrdbv1":     true,
	"sdk/go/transport/internal/rebalanceconv -> pkg/api/grpc/proto/qrdbv1":  true,
	"sdk/go/transport/internal/sealconv -> pkg/api/grpc/proto/qrdbv1":       true,
	"sdk/go/transport/internal/statehashconv -> pkg/api/grpc/proto/qrdbv1":  true,
	"sdk/go/transport/internal/tenantconv -> pkg/api/grpc/proto/qrdbv1":     true,
	"sdk/go/transport/policywire -> pkg/api/grpc/proto/qrdbv1":              true,
	"sdk/go/transport/quic -> pkg/api/grpc/proto/qrdbv1":                    true,
	"sdk/go/transport/quic -> pkg/api/quic/wire":                            true,
}

// minLeanStandaloneDeps is a discovery floor: the fixture composes a whole
// node, and a graph smaller than this means `go list` read something else.
const minLeanStandaloneDeps = 200

// listedPackage is the part of a `go list -json` record the gate reads.
type listedPackage struct {
	ImportPath string
	Imports    []string
	Standard   bool
}

// isLeanStandaloneForbidden reports whether path is, or lies beneath, one of
// the forbidden entries.
func isLeanStandaloneForbidden(path string, forbidden []leanRule) bool {
	for _, r := range forbidden {
		if r.matches(path) {
			return true
		}
	}
	return false
}

// forbiddenEdges returns every edge from a package that is not forbidden to
// one that is. An edge between two forbidden packages is not reported: it
// disappears with the edge that reaches the first of them.
func forbiddenEdges(pkgs []listedPackage, forbidden []leanRule) map[string]bool {
	edges := map[string]bool{}
	for _, p := range pkgs {
		if p.Standard || isLeanStandaloneForbidden(p.ImportPath, forbidden) {
			continue
		}
		for _, imp := range p.Imports {
			if isLeanStandaloneForbidden(imp, forbidden) {
				edges[trimLocalModule(p.ImportPath)+" -> "+trimLocalModule(imp)] = true
			}
		}
	}
	return edges
}

// trimLocalModule shortens a path in this module to its module-relative form.
func trimLocalModule(path string) string {
	return strings.TrimPrefix(path, localModule+"/")
}

// leanStandaloneDeps returns the fixture's dependency graph. The fixture lives
// in the sdk/go module, so `go list` runs there.
func leanStandaloneDeps(t *testing.T) []listedPackage {
	t.Helper()
	return sdkModuleDeps(t, leanStandaloneTags, leanStandaloneFixture)
}

// sdkModuleDeps runs go list -deps over pkg, a package of the sdk/go module,
// under tags.
func sdkModuleDeps(t *testing.T, tags, pkg string) []listedPackage {
	t.Helper()
	args := []string{"list", "-deps", "-json=ImportPath,Imports,Standard", "-tags", tags, pkg}
	cmd := exec.Command("go", args...) // #nosec G204 -- fixed subcommand; tags are this package's constant or the Makefile's BUILD_TAGS, pkg a fixture constant
	cmd.Dir = filepath.Join(repoRoot(t), "sdk", "go")
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, errOut.String())
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(&out)
	for {
		var p listedPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

// sortedKeys returns the members of set in order.
func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestLeanStandaloneEdgeDetection proves the edge finder on a graph whose
// answer is known, so an empty result from the real graph means something.
func TestLeanStandaloneEdgeDetection(t *testing.T) {
	forbidden := []leanRule{{path: localModule + "/pkg/api", self: true, subpackages: true}, {path: "example.com/heavy", self: true, subpackages: true}}
	graph := []listedPackage{
		{ImportPath: "fmt", Standard: true, Imports: []string{"example.com/heavy"}},
		{ImportPath: localModule + "/sdk/go", Imports: []string{localModule + "/pkg/api/rest", localModule + "/pkg/apimodule"}},
		{ImportPath: localModule + "/pkg/apimodule", Imports: []string{"example.com/heavy/sub", "example.com/heavyweight"}},
		{ImportPath: localModule + "/pkg/api/rest", Imports: []string{"example.com/heavy"}},
		{ImportPath: "example.com/third", Imports: []string{localModule + "/pkg/api"}},
	}
	want := []string{
		"example.com/third -> pkg/api",
		"pkg/apimodule -> example.com/heavy/sub",
		"sdk/go -> pkg/api/rest",
	}
	got := sortedKeys(forbiddenEdges(graph, forbidden))
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("forbiddenEdges:\ngot  %q\nwant %q", got, want)
	}
}

// TestLeanStandaloneRuleMatches pins each combination of self and subpackages against
// the package itself, one beneath it and a sibling sharing its prefix.
func TestLeanStandaloneRuleMatches(t *testing.T) {
	const root = "example.com/m/pkg"
	cases := []struct {
		rule leanRule
		path string
		want bool
	}{
		{leanRule{path: root, self: true}, root, true},
		{leanRule{path: root, self: true}, root + "/sub", false},
		{leanRule{path: root, subpackages: true}, root, false},
		{leanRule{path: root, subpackages: true}, root + "/sub/deep", true},
		{leanRule{path: root, self: true, subpackages: true}, root, true},
		{leanRule{path: root, self: true, subpackages: true}, root + "/sub", true},
		{leanRule{path: root, self: true, subpackages: true}, root + "module", false},
		{leanRule{path: root}, root, false},
	}
	for _, c := range cases {
		if got := c.rule.matches(c.path); got != c.want {
			t.Errorf("%+v.matches(%q) = %v, want %v", c.rule, c.path, got, c.want)
		}
	}
}

// TestLeanStandaloneForbiddenRaftEngine proves the narrowed go-quicraft rule
// (ruling 193 D) on a synthetic graph: an edge into the NodeHost package or
// an engine internal still fails, and the seal and Shamir primitives and the
// other public leaf packages do not.
func TestLeanStandaloneForbiddenRaftEngine(t *testing.T) {
	const qr = "github.com/jeremyhahn/go-quicraft/pkg"
	for path, want := range map[string]bool{
		qr:                           true,
		qr + "/internal/engine":      true,
		qr + "/internal/raft":        true,
		qr + "/seal":                 false,
		qr + "/crypto/shamir":        false,
		qr + "/sm":                   false,
		qr + "/writemode":            false,
		"github.com/jeremyhahn/go-x": false,
	} {
		if got := isLeanStandaloneForbidden(path, leanStandaloneForbidden); got != want {
			t.Errorf("isLeanStandaloneForbidden(%q) = %v, want %v", path, got, want)
		}
	}
	graph := []listedPackage{
		{ImportPath: localModule + "/pkg/seal", Imports: []string{qr + "/seal", qr + "/crypto/shamir"}},
		{ImportPath: localModule + "/pkg/backend", Imports: []string{qr, qr + "/sm"}},
		{ImportPath: qr, Imports: []string{qr + "/internal/engine", qr + "/seal"}},
	}
	want := []string{"pkg/backend -> " + qr}
	if got := sortedKeys(forbiddenEdges(graph, leanStandaloneForbidden)); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("forbiddenEdges:\ngot  %q\nwant %q", got, want)
	}
}

// TestLeanStandaloneForbiddenAPISubpackages proves the narrowed pkg/api rule
// (ruling 193 E) on a synthetic graph: an edge into a pkg/api subpackage
// still fails, and one into the pkg/api root or into pkg/apimodule does not.
func TestLeanStandaloneForbiddenAPISubpackages(t *testing.T) {
	const api = localModule + "/pkg/api"
	for path, want := range map[string]bool{
		api:                            false,
		api + "/rest":                  true,
		api + "/grpc/proto/qrdbv1":     true,
		localModule + "/pkg/apimodule": false,
	} {
		if got := isLeanStandaloneForbidden(path, leanStandaloneForbidden); got != want {
			t.Errorf("isLeanStandaloneForbidden(%q) = %v, want %v", path, got, want)
		}
	}
	graph := []listedPackage{
		{ImportPath: localModule + "/pkg/mvcc", Imports: []string{api}},
		{ImportPath: localModule + "/sdk/go/transport/embedded", Imports: []string{api, api + "/dbwire"}},
		{ImportPath: api, Imports: []string{api + "/errclass"}},
	}
	want := []string{
		"pkg/api -> pkg/api/errclass",
		"sdk/go/transport/embedded -> pkg/api/dbwire",
	}
	if got := sortedKeys(forbiddenEdges(graph, leanStandaloneForbidden)); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("forbiddenEdges:\ngot  %q\nwant %q", got, want)
	}
}

// TestLeanStandaloneImportGraph is the gate: the lean standalone fixture
// reaches a forbidden package by no edge outside the ratchet table, and by
// every edge inside it.
func TestLeanStandaloneImportGraph(t *testing.T) {
	pkgs := leanStandaloneDeps(t)
	if len(pkgs) < minLeanStandaloneDeps {
		t.Fatalf("go list reported %d packages for %s, below the floor of %d", len(pkgs), leanStandaloneFixture, minLeanStandaloneDeps)
	}
	listed := map[string]bool{}
	for _, p := range pkgs {
		listed[p.ImportPath] = true
	}
	for _, need := range []string{leanStandaloneFixture, localModule + "/sdk/go/node", localModule + "/sdk/go/transport/embedded", localModule + "/pkg/dao"} {
		if !listed[need] {
			t.Fatalf("%s is not in the fixture's graph; the fixture no longer composes what the gate guards", need)
		}
	}

	got := forbiddenEdges(pkgs, leanStandaloneForbidden)
	var unexpected, stale []string
	for _, e := range sortedKeys(got) {
		if !leanStandaloneEdges[e] {
			unexpected = append(unexpected, e)
		}
	}
	for _, e := range sortedKeys(leanStandaloneEdges) {
		if !got[e] {
			stale = append(stale, e)
		}
	}
	if len(unexpected) > 0 {
		t.Errorf("a lean standalone embedder compiles %d forbidden import edge(s) not in leanStandaloneEdges; remove the dependency, do not list it:\n  %s",
			len(unexpected), strings.Join(unexpected, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("%d leanStandaloneEdges entr(ies) no longer occur; delete them so the ratchet holds:\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}
