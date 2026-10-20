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
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The platform-init signer, which signs the init statement every Raft
// auto-unseal tenant create requires, is installed in ONE function, reached
// from ONE composition, and both binaries that run a Raft node -- qrdbd and
// the embedded sdk/go/node -- derive the node identity it signs with and
// compose it through that same path. A second installation site, or a
// composer that wires its own, is how one of the two came to refuse every
// such create while the other accepted it.
var (
	// signerInstallers is every non-test function that may install the
	// signer, by "<file>:<function>".
	signerInstallers = []string{"pkg/server/lifecycle/barrier_init.go:wirePlatformInit"}

	// signerWiringCallers is every non-test function that may call the
	// installer.
	signerWiringCallers = []string{"pkg/server/lifecycle/raft_tenants.go:ComposeRaftTenantCluster"}

	// signerComposers is every package that runs a Raft node, each of which
	// must reach the signer through the shared composition and identity.
	signerComposers = []string{"cmd/qrdbd", "sdk/go/node"}

	// sharedSignerPath is the lifecycle functions each composer must call.
	sharedSignerPath = []string{"lifecycle.ComposeRaftTenantCluster", "lifecycle.NodeIdentity"}
)

// callSite is one call in non-test code: the file and the top-level function
// it sits in, and the callee as written ("x.Name" for a selector).
type callSite struct {
	rel, fn, callee string
}

// site is the "<file>:<function>" a call sits in.
func (c callSite) site() string { return c.rel + ":" + c.fn }

// name is the callee without its qualifier.
func (c callSite) name() string {
	if i := strings.LastIndexByte(c.callee, '.'); i >= 0 {
		return c.callee[i+1:]
	}
	return c.callee
}

// nonTestCalls lists every call in the non-test files of parsed, attributed
// to the top-level function declaring it (a call in a closure belongs to the
// function the closure is in).
func nonTestCalls(parsed []parsedFile) []callSite {
	var calls []callSite
	for _, pf := range parsed {
		if strings.HasSuffix(pf.rel, "_test.go") {
			continue
		}
		for _, decl := range pf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch f := call.Fun.(type) {
				case *ast.Ident:
					callee = f.Name
				case *ast.SelectorExpr:
					callee = f.Sel.Name
					if x, ok := f.X.(*ast.Ident); ok {
						callee = x.Name + "." + f.Sel.Name
					}
				default:
					return true
				}
				calls = append(calls, callSite{rel: pf.rel, fn: fn.Name.Name, callee: callee})
				return true
			})
		}
	}
	return calls
}

// signerPathViolations is what calls says against the rules above: an
// installation or wiring call from anywhere but its one site, and a composer
// missing a call on the shared path.
func signerPathViolations(calls []callSite) []string {
	var violations []string
	composed := map[string]map[string]bool{}
	for _, c := range calls {
		switch c.name() {
		case "SetPlatformInitSigner":
			if !slices.Contains(signerInstallers, c.site()) {
				violations = append(violations, c.site()+" installs the platform-init signer; only "+
					strings.Join(signerInstallers, ", ")+" may")
			}
		case "wirePlatformInit":
			if !slices.Contains(signerWiringCallers, c.site()) {
				violations = append(violations, c.site()+" wires the platform-init signer; only "+
					strings.Join(signerWiringCallers, ", ")+" may")
			}
		}
		if dir := path.Dir(c.rel); slices.Contains(signerComposers, dir) && slices.Contains(sharedSignerPath, c.callee) {
			if composed[dir] == nil {
				composed[dir] = map[string]bool{}
			}
			composed[dir][c.callee] = true
		}
	}
	for _, dir := range signerComposers {
		for _, want := range sharedSignerPath {
			if !composed[dir][want] {
				violations = append(violations, dir+" runs a Raft node but never calls "+want+
					"; its platform-init signer must come from the shared composition")
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func TestPlatformInitSigner_OneInstallationSharedByEveryComposer(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	walked, parsed := walkGoFiles(t, root, fset, ".")
	if walked < minGoFilesScanned {
		t.Fatalf("scanned %d non-generated .go files, floor is %d; either the scan is "+
			"broken or the tree shrank -- if deliberate, lower minGoFilesScanned in the "+
			"same commit", walked, minGoFilesScanned)
	}

	calls := nonTestCalls(parsed)
	// The installation site itself is a discovery floor: a scan that sees
	// no call to the installer at all proves nothing about who else calls it.
	installs := 0
	for _, c := range calls {
		if c.name() == "SetPlatformInitSigner" {
			installs++
		}
	}
	if installs == 0 {
		t.Fatal("saw no call to SetPlatformInitSigner; the scan is broken or the installer was renamed")
	}
	for _, v := range signerPathViolations(calls) {
		t.Error(v)
	}
}

func TestPlatformInitSigner_DetectsASecondPath(t *testing.T) {
	const src = `package x
func wirePlatformInit() { wiring.SetPlatformInitSigner(s) }
func ComposeRaftTenantCluster() { wirePlatformInit() }
func own() { wiring.SetPlatformInitSigner(s); wirePlatformInit() }
func boot() { lifecycle.ComposeRaftTenantCluster(c); lifecycle.NodeIdentity(id) }
`
	parse := func(rel string) parsedFile {
		file, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		return parsedFile{rel: rel, file: file}
	}

	got := signerPathViolations(nonTestCalls([]parsedFile{
		parse("pkg/server/lifecycle/barrier_init.go"),
		parse("cmd/qrdbd/start.go"),
		// A composer's own tests are not production wiring.
		parse("sdk/go/node/node_test.go"),
	}))
	want := []string{
		"cmd/qrdbd/start.go:ComposeRaftTenantCluster wires the platform-init signer; only pkg/server/lifecycle/raft_tenants.go:ComposeRaftTenantCluster may",
		"cmd/qrdbd/start.go:own installs the platform-init signer; only pkg/server/lifecycle/barrier_init.go:wirePlatformInit may",
		"cmd/qrdbd/start.go:own wires the platform-init signer; only pkg/server/lifecycle/raft_tenants.go:ComposeRaftTenantCluster may",
		"cmd/qrdbd/start.go:wirePlatformInit installs the platform-init signer; only pkg/server/lifecycle/barrier_init.go:wirePlatformInit may",
		"pkg/server/lifecycle/barrier_init.go:ComposeRaftTenantCluster wires the platform-init signer; only pkg/server/lifecycle/raft_tenants.go:ComposeRaftTenantCluster may",
		"pkg/server/lifecycle/barrier_init.go:own installs the platform-init signer; only pkg/server/lifecycle/barrier_init.go:wirePlatformInit may",
		"pkg/server/lifecycle/barrier_init.go:own wires the platform-init signer; only pkg/server/lifecycle/raft_tenants.go:ComposeRaftTenantCluster may",
		"sdk/go/node runs a Raft node but never calls lifecycle.ComposeRaftTenantCluster; its platform-init signer must come from the shared composition",
		"sdk/go/node runs a Raft node but never calls lifecycle.NodeIdentity; its platform-init signer must come from the shared composition",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
