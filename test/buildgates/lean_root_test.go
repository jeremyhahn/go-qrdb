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
	"strings"
	"testing"
)

// The lean-root gate (#627 slice B) guards that a caller of the sdk/go root
// alone -- a client naming *sdk.Client, the closed-store error and the MCP
// error codes -- does not compile the protocol servers. The server, sealed-
// window and gRPC-registration re-exports that used to link them moved to
// sdk/go/server, and the storage, engine and discovery re-exports to their own
// subpackages; this gate keeps them from coming back into the root.
//
// What still links the root to the cluster engine is client API, not a
// re-export: pkg/service, pkg/config -> pkg/cluster and pkg/seal ->
// pkg/storage/pebble, under the methods in cluster.go, featuregate.go and
// membership.go. Those are not this gate's subject.

// leanRootFixture is the program that imports the sdk/go root and nothing else
// from QRDB.
const leanRootFixture = localModule + "/sdk/go/examples/leanroot"

// minLeanRootDeps is a discovery floor: the root links several hundred
// packages, so a dependency list this short means go list read the wrong
// package or none, and an empty list must not pass as "no server linked".
const minLeanRootDeps = 200

// leanRootForbidden is every package a root-only caller must not link: the
// server orchestrator and its lifecycle, the API-module registry, and every
// package beneath pkg/api except the contract types in leanRootAPIContracts.
var leanRootForbidden = []leanRule{
	{path: localModule + "/pkg/server", self: true, subpackages: true},
	{path: localModule + "/pkg/apimodule", self: true, subpackages: true},
	{path: localModule + "/pkg/api", subpackages: true},
}

// leanRootAPIContracts are the pkg/api subpackages the root may link: wire and
// contract types every transport shares, with no server in them. A new entry
// here is a reviewed decision that the package holds no listener or handler.
var leanRootAPIContracts = map[string]bool{
	localModule + "/pkg/api/errclass":     true,
	localModule + "/pkg/api/mcp/wire":     true,
	localModule + "/pkg/api/sealedwindow": true,
}

// leanRootViolations returns every forbidden package in pkgs, sorted.
func leanRootViolations(pkgs []listedPackage) []string {
	var bad []string
	for _, p := range pkgs {
		if !p.Standard && !leanRootAPIContracts[p.ImportPath] && isLeanStandaloneForbidden(p.ImportPath, leanRootForbidden) {
			bad = append(bad, p.ImportPath)
		}
	}
	return bad
}

func TestLeanStandaloneRootViolations(t *testing.T) {
	pkgs := []listedPackage{
		{ImportPath: "fmt", Standard: true},
		{ImportPath: localModule + "/sdk/go"},
		{ImportPath: localModule + "/pkg/api"},
		{ImportPath: localModule + "/pkg/api/errclass"},
		{ImportPath: localModule + "/pkg/api/mcp/wire"},
		{ImportPath: localModule + "/pkg/api/grpc"},
		{ImportPath: localModule + "/pkg/api/rest/middleware"},
		{ImportPath: localModule + "/pkg/server"},
		{ImportPath: localModule + "/pkg/server/lifecycle"},
		{ImportPath: localModule + "/pkg/apimodule"},
		{ImportPath: localModule + "/pkg/serverless"},
	}
	got := strings.Join(leanRootViolations(pkgs), " ")
	want := strings.Join([]string{
		localModule + "/pkg/api/grpc",
		localModule + "/pkg/api/rest/middleware",
		localModule + "/pkg/server",
		localModule + "/pkg/server/lifecycle",
		localModule + "/pkg/apimodule",
	}, " ")
	if got != want {
		t.Fatalf("leanRootViolations:\n got  %s\n want %s", got, want)
	}
}

func TestLeanStandaloneRootViolationsNone(t *testing.T) {
	pkgs := []listedPackage{
		{ImportPath: localModule + "/sdk/go"},
		{ImportPath: localModule + "/pkg/api/sealedwindow"},
		{ImportPath: localModule + "/pkg/service"},
	}
	if bad := leanRootViolations(pkgs); len(bad) != 0 {
		t.Fatalf("leanRootViolations flagged allowed packages: %v", bad)
	}
}

// TestLeanStandaloneRootLinksNoServer reads the fixture's import graph under
// the lean tag set and under BUILD_TAGS, the default feature set: a root file
// behind a feature tag must not pull a server in either.
func TestLeanStandaloneRootLinksNoServer(t *testing.T) {
	for _, tags := range []string{leanStandaloneTags, strings.Join(fullBuildTags(t), " ")} {
		t.Run(tags, func(t *testing.T) {
			pkgs := sdkModuleDeps(t, tags, leanRootFixture)
			if len(pkgs) < minLeanRootDeps {
				t.Fatalf("go list reported %d packages for %s, below the floor of %d", len(pkgs), leanRootFixture, minLeanRootDeps)
			}
			listed := map[string]bool{}
			for _, p := range pkgs {
				listed[p.ImportPath] = true
			}
			for _, need := range []string{leanRootFixture, localModule + "/sdk/go", localModule + "/pkg/service"} {
				if !listed[need] {
					t.Fatalf("%s is not in the fixture's graph; the fixture no longer exercises what the gate guards", need)
				}
			}
			if bad := leanRootViolations(pkgs); len(bad) > 0 {
				t.Errorf("a caller of the sdk/go root alone links %d server or API package(s); move the re-export that pulls it into a subpackage (sdk/go/server), do not allow it:\n  %s",
					len(bad), strings.Join(bad, "\n  "))
			}
		})
	}
}
