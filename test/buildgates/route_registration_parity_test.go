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
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Node parity (owner ruling 2026-10-06): "all the nodes are equal and should
// have all the endpoints so they can serve requests just like the leader or
// first node that joined the cluster." A route, RPC, tool, frame method or
// listener may be conditioned on whether a SERVICE is wired into this node
// (ceremonySvc != nil, barrierSvc != nil, ...) -- every node in a cluster
// gets the same service graph -- never on which node is asking: its
// identity (is it the leader? the first node to join? does it hold some
// per-node file?) is exactly what must NOT decide whether an endpoint
// exists or is served.
//
// This file gates all six transports from one shared detector
// (roleConditionReason/roleConditionPatterns below): it parses each
// transport's own registration idiom's source -- never compiled behaviour,
// which cannot tell "this node happens not to be leader right now" apart
// from "this node can never serve this route" -- and fails if a condition's
// source text names leadership, founding-node or bootstrap-file status.
//
// Each transport's registration idiom differs, so each gets its own
// EXTRACTION (which conditions to look at), while every extraction feeds
// the SAME detector:
//   - REST (pkg/api/rest/server.go, registerRoutes): one choke point,
//     (*Server).routeIf(enabled, method, pattern, perm, handler); the
//     condition is the call's first argument.
//   - QUIC (pkg/api/quic/dispatch.go, buildHandlers): the same shape,
//     (*Server).handleIf(enabled, method, perm, handler) and its public
//     sibling handlePublicIf.
//   - MCP (pkg/api/mcp/server.go, NewServer) and the Unix socket
//     (pkg/api/unix/server.go, Start) have no single named gate call --
//     a tool or a regOpt is wrapped in a plain `if COND { ... }` -- so these
//     two scan every if-condition in that one function instead.
//   - gRPC and the embedded transport have no registration call at all:
//     gRPC's qrdbServiceServer always implements the full generated
//     interface, and embedded's *Transport always implements the full SDK
//     client interface. What stands in for "is this served" is the guard
//     clause at the top of each handler/method (if s.xSvc == nil { return
//     ...Unimplemented }); these two scan every if-condition in every
//     function of the package's (or, for embedded, the transport's) own
//     non-test source files.
//
// It does not and cannot flag the CA bootstrap/anchor-advertiser route
// family (gated on bootServers != nil in cmd/qrdbd/start.go, not through any
// of the idioms above) -- that topology choice is documented at
// docs/clustering/architecture.md#enrollment-role-kept and is a different
// lane's (caanchor) concern, deliberately untouched here.

// roleConditionPatterns are source fragments that mean "this exists, or is
// served, depending on which node is asking" rather than "depending on what
// this node is composed with." Matched against the rendered source of a
// condition expression. This is the ONE detector every transport's scan
// below shares.
var roleConditionPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)isleader`),
	regexp.MustCompile(`(?i)firstnode`),
	regexp.MustCompile(`(?i)genesis`),
	regexp.MustCompile(`(?i)isfirst\b`),
	regexp.MustCompile(`replicaID\s*==\s*1\b`),
	regexp.MustCompile(`ReplicaID\s*==\s*1\b`),
	regexp.MustCompile(`nodeID\s*==\s*1\b`),
	regexp.MustCompile(`NodeID\s*==\s*1\b`),
}

// roleConditionReason renders cond's source and reports why it names node
// role rather than node composition, or "" when it does not. This is the
// shared detector: every scan in this file, for every transport, calls
// exactly this function and no other.
func roleConditionReason(fset *token.FileSet, cond ast.Expr) string {
	var buf []byte
	w := byteSliceWriter{&buf}
	if err := format.Node(w, fset, cond); err != nil {
		// Unrenderable is not this gate's failure mode to report; let the
		// condition through rather than fail the build on a formatter gap.
		return ""
	}
	src := string(buf)
	for _, pat := range roleConditionPatterns {
		if pat.MatchString(src) {
			return "condition " + src + " names node role (matches " + pat.String() + ")"
		}
	}
	return ""
}

// byteSliceWriter adapts a *[]byte to io.Writer for format.Node, which wants
// a writer, without pulling in bytes.Buffer for one call site.
type byteSliceWriter struct {
	buf *[]byte
}

func (w byteSliceWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// ---------------------------------------------------------------------------
// Shared extraction #1: a named gate call's first argument (REST, QUIC).
// ---------------------------------------------------------------------------

// callGateTarget names one transport's registration choke point: a function
// in a file, every call within it to one of callNames, whose argument at
// condArg is the enabled/condition expression.
type callGateTarget struct {
	transport  string
	file       string
	targetFunc string
	callNames  map[string]bool
	condArg    int
}

// scanCallGate finds target.targetFunc in target.file and applies the shared
// detector to the condArg'th argument of every call to one of callNames
// within it.
func scanCallGate(t *testing.T, target callGateTarget) (seen int, violations []string) {
	t.Helper()
	root := repoRoot(t)
	path := filepath.Join(root, target.file)
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("[%s] parse %s: %v", target.transport, target.file, err)
	}

	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == target.targetFunc {
			fn = f
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatalf("[%s] %s: function %s not found; this gate's target moved", target.transport, target.file, target.targetFunc)
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !target.callNames[sel.Sel.Name] {
			return true
		}
		if _, ok := sel.X.(*ast.Ident); !ok || len(call.Args) <= target.condArg {
			return true
		}
		seen++
		if reason := roleConditionReason(fset, call.Args[target.condArg]); reason != "" {
			violations = append(violations, fset.Position(call.Pos()).String()+" ("+target.transport+"): "+reason)
		}
		return true
	})
	return seen, violations
}

// ---------------------------------------------------------------------------
// Shared extraction #2: every if-condition in a function, a file, or a
// whole directory's non-test files (MCP, Unix, gRPC, embedded).
// ---------------------------------------------------------------------------

// ifGateTarget names one or more files to scan: every if-statement's
// condition in them, optionally restricted to the named top-level
// functions/methods (nil scans every function in the file).
type ifGateTarget struct {
	transport string
	files     []string
	funcNames map[string]bool
}

// scanIfGate applies the shared detector to every if-condition target names.
func scanIfGate(t *testing.T, target ifGateTarget) (seen int, violations []string) {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range target.files {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("[%s] parse %s: %v", target.transport, path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if target.funcNames != nil && !target.funcNames[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ifs, ok := n.(*ast.IfStmt)
				if !ok {
					return true
				}
				seen++
				if reason := roleConditionReason(fset, ifs.Cond); reason != "" {
					violations = append(violations, fset.Position(ifs.Pos()).String()+" ("+target.transport+"): "+reason)
				}
				return true
			})
		}
	}
	return seen, violations
}

// goFilesIn lists dir's own non-test .go files (not subdirectories), for a
// gate that scans a whole package's or transport's source.
func goFilesIn(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || filepath.Ext(name) != ".go" || len(name) > 8 && name[len(name)-8:] == "_test.go" {
			continue
		}
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// ---------------------------------------------------------------------------
// REST: routeIf(enabled, method, pattern, perm, handler) in registerRoutes.
// ---------------------------------------------------------------------------

const minimumRESTGateCalls = 50

func TestRouteRegistrationNeverConditionalOnNodeRole_REST(t *testing.T) {
	seen, violations := scanCallGate(t, callGateTarget{
		transport:  "rest",
		file:       "pkg/api/rest/server.go",
		targetFunc: "registerRoutes",
		callNames:  map[string]bool{"routeIf": true},
		condArg:    0,
	})
	if seen < minimumRESTGateCalls {
		t.Fatalf("found %d routeIf calls, under the floor of %d: the scan is not seeing the route table", seen, minimumRESTGateCalls)
	}
	reportRoleViolations(t, violations)
}

func TestRouteRegistrationGate_Detects_REST(t *testing.T) {
	assertDetects(t, map[string]bool{
		"s.IsLeader()":               true,
		"isLeader":                   true,
		"s.isFirstNode":              true,
		"cfg.FirstNode":              true,
		"nodeIsGenesis":              true,
		"f.replicaID == 1":           true,
		"host.ReplicaID == 1":        true,
		"s.ceremonySvc != nil":       false,
		"s.opts.barrierSvc != nil":   false,
		"s.opts.rebalanceSvc != nil": false,
	})
}

// ---------------------------------------------------------------------------
// QUIC: handleIf(enabled, method, perm, handler) / handlePublicIf(enabled,
// method, handler) in buildHandlers.
// ---------------------------------------------------------------------------

const minimumQUICGateCalls = 50

func TestRouteRegistrationNeverConditionalOnNodeRole_QUIC(t *testing.T) {
	seen, violations := scanCallGate(t, callGateTarget{
		transport:  "quic",
		file:       "pkg/api/quic/dispatch.go",
		targetFunc: "buildHandlers",
		callNames:  map[string]bool{"handleIf": true, "handlePublicIf": true},
		condArg:    0,
	})
	if seen < minimumQUICGateCalls {
		t.Fatalf("found %d handleIf/handlePublicIf calls, under the floor of %d: the scan is not seeing the dispatch table", seen, minimumQUICGateCalls)
	}
	reportRoleViolations(t, violations)
}

func TestRouteRegistrationGate_Detects_QUIC(t *testing.T) {
	assertDetects(t, map[string]bool{
		"s.isLeader()":         true,
		"nodeIsFirstNode":      true,
		"s.ceremonySvc != nil": false,
		"s.opts.tsSvc != nil":  false,
	})
}

// ---------------------------------------------------------------------------
// MCP: every if-condition in NewServer, which wraps each addTools call in
// `if s.xSvc != nil { s.addTools(...) }` -- there is no single named gate
// call to match on, unlike REST/QUIC.
// ---------------------------------------------------------------------------

const minimumMCPGateIfs = 20

func TestRouteRegistrationNeverConditionalOnNodeRole_MCP(t *testing.T) {
	root := repoRoot(t)
	seen, violations := scanIfGate(t, ifGateTarget{
		transport: "mcp",
		files:     []string{filepath.Join(root, "pkg/api/mcp/server.go")},
		funcNames: map[string]bool{"NewServer": true},
	})
	if seen < minimumMCPGateIfs {
		t.Fatalf("found %d if-conditions in NewServer, under the floor of %d: the scan is not seeing the tool table", seen, minimumMCPGateIfs)
	}
	reportRoleViolations(t, violations)
}

func TestRouteRegistrationGate_Detects_MCP(t *testing.T) {
	assertDetects(t, map[string]bool{
		"s.isLeader":           true,
		"s.isFirstNode":        true,
		"s.ceremonySvc != nil": false,
		"s.dekSvc != nil":      false,
	})
}

// ---------------------------------------------------------------------------
// Unix: every if-condition in Start, which wraps each regOpts append and
// each interceptor addition the same way MCP wraps a tool.
// ---------------------------------------------------------------------------

const minimumUnixGateIfs = 20

func TestRouteRegistrationNeverConditionalOnNodeRole_Unix(t *testing.T) {
	root := repoRoot(t)
	seen, violations := scanIfGate(t, ifGateTarget{
		transport: "unix",
		files:     []string{filepath.Join(root, "pkg/api/unix/server.go")},
		funcNames: map[string]bool{"Start": true},
	})
	if seen < minimumUnixGateIfs {
		t.Fatalf("found %d if-conditions in Start, under the floor of %d: the scan is not seeing the registration options", seen, minimumUnixGateIfs)
	}
	reportRoleViolations(t, violations)
}

func TestRouteRegistrationGate_Detects_Unix(t *testing.T) {
	assertDetects(t, map[string]bool{
		"s.isLeader":                 true,
		"genesisNode":                true,
		"s.ceremonySvc != nil":       false,
		"s.replicaForwarding != nil": false,
	})
}

// ---------------------------------------------------------------------------
// gRPC: every if-condition in every function of the package's own non-test
// source files. gRPC has no registration call at all -- qrdbServiceServer
// always implements the full generated interface -- so what stands in for
// "is this served" is each handler's own guard clause
// (if s.xSvc == nil { return ...Unimplemented }) and the interceptor chain
// Start assembles; scanning every if in the package catches both without
// having to name every handler file.
// ---------------------------------------------------------------------------

const minimumGRPCGateIfs = 200

func TestRouteRegistrationNeverConditionalOnNodeRole_GRPC(t *testing.T) {
	root := repoRoot(t)
	seen, violations := scanIfGate(t, ifGateTarget{
		transport: "grpc",
		files:     goFilesIn(t, filepath.Join(root, "pkg/api/grpc")),
	})
	if seen < minimumGRPCGateIfs {
		t.Fatalf("found %d if-conditions in pkg/api/grpc, under the floor of %d: the scan is not seeing the handlers", seen, minimumGRPCGateIfs)
	}
	reportRoleViolations(t, violations)
}

func TestRouteRegistrationGate_Detects_GRPC(t *testing.T) {
	assertDetects(t, map[string]bool{
		"s.isLeader()":         true,
		"f.replicaID == 1":     true,
		"s.ceremonySvc == nil": false,
		"s.barrierSvc != nil":  false,
	})
}

// ---------------------------------------------------------------------------
// Embedded: every if-condition in every function of the transport's own
// non-test source files, for the same reason as gRPC -- *Transport always
// implements the full SDK client interface, and each method's own guard
// clause is what stands in for "is this served."
// ---------------------------------------------------------------------------

const minimumEmbeddedGateIfs = 200

func TestRouteRegistrationNeverConditionalOnNodeRole_Embedded(t *testing.T) {
	root := repoRoot(t)
	seen, violations := scanIfGate(t, ifGateTarget{
		transport: "embedded",
		files:     goFilesIn(t, filepath.Join(root, "sdk/go/transport/embedded")),
	})
	if seen < minimumEmbeddedGateIfs {
		t.Fatalf("found %d if-conditions in sdk/go/transport/embedded, under the floor of %d: the scan is not seeing the transport", seen, minimumEmbeddedGateIfs)
	}
	reportRoleViolations(t, violations)
}

func TestRouteRegistrationGate_Detects_Embedded(t *testing.T) {
	assertDetects(t, map[string]bool{
		"t.isLeader()":         true,
		"t.isFirstNode":        true,
		"t.ceremonySvc == nil": false,
		"t.forwarding != nil":  false,
	})
}

// ---------------------------------------------------------------------------
// Shared test plumbing.
// ---------------------------------------------------------------------------

// reportRoleViolations fails the test with every violation found, or does
// nothing when there are none.
func reportRoleViolations(t *testing.T, violations []string) {
	t.Helper()
	for _, v := range violations {
		t.Errorf("%s — an endpoint must never be registered or served conditionally on node role "+
			"(leader/first-node/genesis/a per-node file); every node registers and serves it, "+
			"forwarding to the leader for leader-held state instead", v)
	}
}

// assertDetects is every transport's _Detects control: it must flag every
// role-shaped condition and pass every legitimate capability condition, or a
// green run for that transport's gate proves nothing. It drives the SAME
// shared detector (roleConditionReason) the gates above call, so this is a
// control on the detector as exercised, not a second implementation of it.
func assertDetects(t *testing.T, cases map[string]bool) {
	t.Helper()
	fset := token.NewFileSet()
	for src, want := range cases {
		expr, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		if got := roleConditionReason(fset, expr) != ""; got != want {
			t.Errorf("%q flagged=%v, want %v", src, got, want)
		}
	}
}
