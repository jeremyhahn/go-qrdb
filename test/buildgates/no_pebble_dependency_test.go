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
	"os/exec"
	"strings"
	"testing"
)

// This gate answers the Lane 1A acceptance criterion (docs/databases/
// engineering.md, "Target: phased implementation plan", Lane 1A): pkg/engine
// and pkg/timeseries must not import Pebble directly, under ANY build tag
// combination -- not a tag-conditional claim the way the MySQL/etcd gates in
// this package are, because the storage-generic engine and its time-series
// support are defined to work identically over memory, file, pebble and
// objstore storage.Storage backends. A direct Pebble import in either
// package would silently reintroduce the pebble-only coupling this lane
// removed (MVCCEngine's former *pebble.DB field, and pkg/timeseries.Storage's
// former *pebble.DB-backed constructor).
//
// Only the ROOT Pebble import path is checked, not every subpackage under
// it: pkg/engine's vector checkpoint support (Lane 1B territory, a separate,
// explicitly-allowed on-disk ANN index) imports cockroachdb/pebble/v2/vfs --
// a filesystem abstraction type, not the storage engine itself -- and is not
// what this gate exists to catch.
const pebbleRootImport = "github.com/cockroachdb/pebble/v2"

// pebbleGateTargets are the two packages Lane 1A requires to be
// storage-generic.
var pebbleGateTargets = []string{
	"github.com/jeremyhahn/go-qrdb/pkg/engine",
	"github.com/jeremyhahn/go-qrdb/pkg/timeseries",
}

// goListPackageImports runs `go list -json -tags <tags> <pkg>` from the
// repository root and returns every import path the package's own non-test,
// test and external-test files name directly (not the transitive closure:
// this gate cares what THIS package imports, not what it reaches through
// something else).
func goListPackageImports(t *testing.T, tags []string, pkg string) []string {
	t.Helper()
	root := repoRoot(t)
	args := []string{"list", "-json"}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	args = append(args, pkg)
	cmd := exec.Command("go", args...) // #nosec G204 -- fixed subcommand, tags and pkg are this file's own constants/helpers
	cmd.Dir = root
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, errOut.String())
	}

	var pkgInfo struct {
		Imports      []string
		TestImports  []string
		XTestImports []string
	}
	if err := json.Unmarshal(out.Bytes(), &pkgInfo); err != nil {
		t.Fatalf("go %s: decoding JSON output: %v\n%s", strings.Join(args, " "), err, out.String())
	}

	all := make([]string, 0, len(pkgInfo.Imports)+len(pkgInfo.TestImports)+len(pkgInfo.XTestImports))
	all = append(all, pkgInfo.Imports...)
	all = append(all, pkgInfo.TestImports...)
	all = append(all, pkgInfo.XTestImports...)
	return all
}

// TestEngineAndTimeSeriesDoNotImportPebble is the Lane 1A build gate.
func TestEngineAndTimeSeriesDoNotImportPebble(t *testing.T) {
	tags := fullBuildTags(t)
	for _, pkg := range pebbleGateTargets {
		imports := goListPackageImports(t, tags, pkg)
		if contains(imports, pebbleRootImport) {
			t.Errorf("%s imports %s directly; the storage-generic engine and its "+
				"time-series support must work identically over memory, file, pebble "+
				"and objstore storage.Storage, not depend on Pebble specifically "+
				"(docs/databases/engineering.md, Lane 1A)", pkg, pebbleRootImport)
		}
	}
}

// TestEngineAndTimeSeriesDoNotImportPebble_DefaultTags repeats the gate with
// no build tags at all, so the claim holds for the minimal build too, not
// only the full tag set qrdb ships with by default.
func TestEngineAndTimeSeriesDoNotImportPebble_DefaultTags(t *testing.T) {
	for _, pkg := range pebbleGateTargets {
		imports := goListPackageImports(t, nil, pkg)
		if contains(imports, pebbleRootImport) {
			t.Errorf("%s imports %s directly under the default (untagged) build; "+
				"the storage-generic engine and its time-series support must not depend "+
				"on Pebble specifically (docs/databases/engineering.md, Lane 1A)", pkg, pebbleRootImport)
		}
	}
}
