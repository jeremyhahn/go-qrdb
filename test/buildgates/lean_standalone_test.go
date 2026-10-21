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

// leanStandaloneForbidden lists what a lean standalone embedder must not
// compile: features it does not use. A path is forbidden when it equals an
// entry or lies beneath one at a path-segment boundary, so pkg/api forbids
// pkg/api/rest and not pkg/apimodule.
var leanStandaloneForbidden = []string{
	"github.com/jeremyhahn/go-quicraft",
	"github.com/cockroachdb/pebble",
	localModule + "/pkg/api",
	localModule + "/pkg/server",
	localModule + "/pkg/cluster",
	localModule + "/pkg/vector",
	localModule + "/pkg/timeseries",
	"github.com/mark3labs/mcp-go",
	"github.com/prometheus/client_golang",
	"github.com/gin-gonic/gin",
	"github.com/go-chi/chi",
	"github.com/jeremyhahn/go-objstore",
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

	// At-rest encryption is a pebble vfs.FS wrapper.
	"pkg/atrest -> github.com/cockroachdb/pebble/v2":     true,
	"pkg/atrest -> github.com/cockroachdb/pebble/v2/vfs": true,

	// Seal state from the L1 seal home; sealed-window contract types.
	"pkg/auth -> github.com/jeremyhahn/go-quicraft/pkg/seal": true,
	"pkg/auth -> pkg/api/sealedwindow":                       true,

	// The gRPC proto and chi types in the shared auth chain.
	"pkg/auth/middleware -> github.com/go-chi/chi/v5": true,

	// The service.Host interface LocalBackend implements is typed by the
	// quicraft NodeHost.
	"pkg/backend -> github.com/jeremyhahn/go-quicraft/pkg":        true,
	"pkg/backend -> github.com/jeremyhahn/go-quicraft/pkg/config": true,
	"pkg/backend -> github.com/jeremyhahn/go-quicraft/pkg/sm":     true,
	"pkg/backend -> github.com/jeremyhahn/go-quicraft/pkg/sm/kv":  true,

	// The Raft backend, reached through pkg/service.
	"pkg/backend/raft -> github.com/jeremyhahn/go-quicraft/pkg":    true,
	"pkg/backend/raft -> github.com/jeremyhahn/go-quicraft/pkg/sm": true,
	"pkg/backend/raft -> pkg/cluster/featuregate":                  true,

	// Shamir from quicraft; sealed-window contract types.
	"pkg/ceremony -> github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir": true,
	"pkg/ceremony -> pkg/api/sealedwindow":                                true,

	// Cluster feature gates and membership.
	"pkg/clusterenroll -> pkg/cluster/featuregate": true,
	"pkg/clusterseal -> pkg/cluster/featuregate":   true,
	"pkg/joinadvertise -> pkg/cluster":             true,
	"pkg/joinadvertise -> pkg/cluster/featuregate": true,
	"pkg/replicaforward -> pkg/cluster":            true,
	"pkg/shard -> pkg/cluster/featuregate":         true,

	// Cluster topology types in the config schema.
	"pkg/config -> pkg/cluster": true,

	// The per-database barrier WAL is quicraft logdb on pebble.
	"pkg/database -> github.com/cockroachdb/pebble/v2":                  true,
	"pkg/database -> github.com/cockroachdb/pebble/v2/vfs":              true,
	"pkg/database -> github.com/jeremyhahn/go-quicraft/pkg":             true,
	"pkg/database -> github.com/jeremyhahn/go-quicraft/pkg/logdb":       true,
	"pkg/database -> github.com/jeremyhahn/go-quicraft/pkg/logdb/waldb": true,

	// Raft state-machine interfaces; pebble vfs; vector and time-series
	// engines.
	"pkg/engine -> github.com/cockroachdb/pebble/v2/vfs":        true,
	"pkg/engine -> github.com/jeremyhahn/go-quicraft/pkg/sm":    true,
	"pkg/engine -> github.com/jeremyhahn/go-quicraft/pkg/sm/kv": true,
	"pkg/engine -> pkg/timeseries":                              true,
	"pkg/engine -> pkg/vector":                                  true,

	// The pebble.DB index the Raft state machine in pkg/store builds; reached
	// only through pkg/store.
	"pkg/index/pebbleindex -> github.com/cockroachdb/pebble/v2": true,

	// The L1 seal home: pkg/seal re-exports go-quicraft/pkg/seal and Shamir
	// (Layer Ownership), and its users follow.
	"pkg/keydelivery -> github.com/jeremyhahn/go-quicraft/pkg/seal":   true,
	"pkg/pin -> github.com/jeremyhahn/go-quicraft/pkg/seal":           true,
	"pkg/seal -> github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir": true,
	"pkg/seal -> github.com/jeremyhahn/go-quicraft/pkg/seal":          true,
	"pkg/secure -> github.com/jeremyhahn/go-quicraft/pkg/seal":        true,

	// The encrypted file system is a pebble vfs.FS.
	"pkg/seal/encfs -> github.com/cockroachdb/pebble/v2/vfs": true,

	// Every feature service is compiled into one package.
	"pkg/service -> github.com/jeremyhahn/go-quicraft/pkg":          true,
	"pkg/service -> github.com/jeremyhahn/go-quicraft/pkg/config":   true,
	"pkg/service -> github.com/jeremyhahn/go-quicraft/pkg/logdb":    true,
	"pkg/service -> github.com/jeremyhahn/go-quicraft/pkg/sm":       true,
	"pkg/service -> github.com/jeremyhahn/go-quicraft/pkg/sm/kv":    true,
	"pkg/service -> github.com/prometheus/client_golang/prometheus": true,
	"pkg/service -> pkg/api":                                        true,
	"pkg/service -> pkg/cluster":                                    true,
	"pkg/service -> pkg/cluster/featuregate":                        true,
	"pkg/service -> pkg/timeseries":                                 true,
	"pkg/service -> pkg/vector":                                     true,
	"pkg/service -> pkg/vector/hnsw":                                true,

	// Raft state-machine interfaces; vector and time-series engines.
	"pkg/statemachine -> github.com/jeremyhahn/go-quicraft/pkg/sm":    true,
	"pkg/statemachine -> github.com/jeremyhahn/go-quicraft/pkg/sm/kv": true,
	"pkg/statemachine -> pkg/api":                                     true,
	"pkg/statemachine -> pkg/timeseries":                              true,
	"pkg/statemachine -> pkg/vector/hnsw":                             true,

	// The object-store backend, compiled by server/lifecycle.
	"pkg/storage/objstore -> github.com/jeremyhahn/go-objstore/pkg/common":  true,
	"pkg/storage/objstore -> github.com/jeremyhahn/go-objstore/pkg/factory": true,

	// At-rest encryption of the file backend goes through a pebble vfs.FS.
	"pkg/storage/file -> github.com/cockroachdb/pebble/v2/vfs": true,

	// The pebble storage backend, compiled by every node.
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2":         true,
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2/bloom":   true,
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2/sstable": true,
	"pkg/storage/pebble -> github.com/cockroachdb/pebble/v2/vfs":     true,

	// Vector storage, reached through pkg/service.
	"pkg/storage/vector -> github.com/cockroachdb/pebble/v2":         true,
	"pkg/storage/vector -> github.com/cockroachdb/pebble/v2/bloom":   true,
	"pkg/storage/vector -> github.com/cockroachdb/pebble/v2/sstable": true,
	"pkg/storage/vector/schema -> pkg/api":                           true,

	// The pebble store; Raft state-machine interfaces; time series.
	"pkg/store -> github.com/cockroachdb/pebble/v2":            true,
	"pkg/store -> github.com/jeremyhahn/go-quicraft/pkg/sm":    true,
	"pkg/store -> github.com/jeremyhahn/go-quicraft/pkg/sm/kv": true,
	"pkg/store -> pkg/timeseries":                              true,

	// Prometheus metrics, reached through pkg/service.
	"pkg/util/metrics -> github.com/prometheus/client_golang/prometheus":            true,
	"pkg/util/metrics -> github.com/prometheus/client_golang/prometheus/collectors": true,
	"pkg/util/metrics -> github.com/prometheus/client_golang/prometheus/promauto":   true,

	// Write modes re-export quicraft's.
	"pkg/writemode -> github.com/jeremyhahn/go-quicraft/pkg/writemode": true,

	// The SDK root re-exports every feature and transport.
	"sdk/go -> github.com/cockroachdb/pebble/v2/vfs":                true,
	"sdk/go -> github.com/jeremyhahn/go-quicraft/pkg/crypto/shamir": true,
	"sdk/go -> github.com/jeremyhahn/go-quicraft/pkg/seal":          true,
	"sdk/go -> github.com/mark3labs/mcp-go/server":                  true,
	"sdk/go -> pkg/api/grpc":                                        true,
	"sdk/go -> pkg/api/mcp":                                         true,
	"sdk/go -> pkg/api/mcp/wire":                                    true,
	"sdk/go -> pkg/api/quic":                                        true,
	"sdk/go -> pkg/api/rest":                                        true,
	"sdk/go -> pkg/api/sealedwindow":                                true,
	"sdk/go -> pkg/cluster":                                         true,
	"sdk/go -> pkg/cluster/featuregate":                             true,
	"sdk/go -> pkg/server":                                          true,
	"sdk/go -> pkg/server/lifecycle":                                true,
	"sdk/go -> pkg/timeseries":                                      true,
	"sdk/go -> pkg/vector/flat":                                     true,
	"sdk/go -> pkg/vector/gpu":                                      true,
	"sdk/go -> pkg/vector/hnsw":                                     true,
	"sdk/go -> pkg/vector/ivf":                                      true,

	// The node composer builds every mode and backend in one package.
	"sdk/go/node -> github.com/cockroachdb/pebble/v2":             true,
	"sdk/go/node -> github.com/cockroachdb/pebble/v2/vfs":         true,
	"sdk/go/node -> github.com/jeremyhahn/go-quicraft/pkg":        true,
	"sdk/go/node -> github.com/jeremyhahn/go-quicraft/pkg/config": true,
	"sdk/go/node -> github.com/jeremyhahn/go-quicraft/pkg/sm":     true,
	"sdk/go/node -> pkg/cluster":                                  true,
	"sdk/go/node -> pkg/cluster/featuregate":                      true,
	"sdk/go/node -> pkg/server":                                   true,
	"sdk/go/node -> pkg/server/lifecycle":                         true,

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
func isLeanStandaloneForbidden(path string, forbidden []string) bool {
	for _, f := range forbidden {
		if path == f || strings.HasPrefix(path, f+"/") {
			return true
		}
	}
	return false
}

// forbiddenEdges returns every edge from a package that is not forbidden to
// one that is. An edge between two forbidden packages is not reported: it
// disappears with the edge that reaches the first of them.
func forbiddenEdges(pkgs []listedPackage, forbidden []string) map[string]bool {
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
	args := []string{"list", "-deps", "-json=ImportPath,Imports,Standard", "-tags", leanStandaloneTags, leanStandaloneFixture}
	cmd := exec.Command("go", args...)
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
	forbidden := []string{localModule + "/pkg/api", "example.com/heavy"}
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
