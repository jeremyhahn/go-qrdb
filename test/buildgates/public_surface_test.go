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

// public_surface_test.go pins the exact pre-authentication (public) surface
// this build serves on every wire transport, and asserts it against a
// curated, reviewed allowlist. "Invitation redemption is the one write any
// caller may make with no credential at all"
// (docs/identity-rbac/architecture.md#trust-boundaries) is a claim about the
// WHOLE tree, not one file, so the gate here is a scanner over the
// registration SITES rather than a constructed server: pkg/api/rest's
// default-deny startup assertion and its route-mapping guard
// (route_mapping_guard_test.go) already prove every REST route is covered by
// SOME marker; this file is the cross-transport check that the SET of
// markers naming "public" is exactly what a reviewer signed off on, on every
// transport that has the concept at all.
//
// Deliberately NARROW, like check-publication-bound.sh: it matches the
// literal registration call shapes below, not "every way a transport could
// expose something unauthenticated." A new public registration that does not
// match one of these call shapes slips past this gate -- the floor it sets
// is on the markers themselves (RegisterPublic / s.public(If) /
// PublicGRPCMethods / the enrolment server's own tool list), the same
// construction-time markers each transport's own authorizer already
// fails closed without. Growing the set of recognized call shapes, or the
// allowlist itself, is a reviewed, deliberate edit to this file.
//
//   - REST: every `s.public(` / `s.publicIf(` call in pkg/api/rest/server.go.
//   - Native QUIC: every `s.handlePublicIf(` call anywhere under
//     pkg/api/quic (there is exactly one transport-wide registry,
//     QUICMethodRegistry, and RegisterPublic is its only way in).
//   - MCP: the SEPARATE, public /mcp/enroll server
//     (pkg/api/mcp.EnrollPath) and the tool(s) it serves -- found by
//     scanning pkg/api/mcp/tools_invitations.go's newEnrollServer for
//     `srv.AddTool(` calls and resolving each tool constructor to the
//     mcp.NewTool name argument.
//   - gRPC and the Unix socket share one mechanism
//     (AuthMiddleware.RegisterPublicGRPCMethods): a capability module's
//     PublicGRPCMethods() []string (no production implementation ships
//     today, so this is asserted empty and the gate documents exactly what
//     would make it non-empty) and the built-in literal registration both
//     pkg/api/grpc/server.go and pkg/api/unix/server.go make for
//     RedeemInvitation -- the one PUBLIC built-in RPC, scanned the same
//     curated way as REST/QUIC/MCP above.
//
// Embedded carries no authentication boundary to begin with (the caller is
// already in-process), so it has no "public surface" to pin here; its own
// contract -- RedeemInvitation still runs the sealed-barrier gate, nothing
// else -- is covered by sdk/go/transport/embedded's own tests.

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// restPublicCallPattern matches `s.public(http.MethodX, "pattern", ...)` and
// `s.publicIf(cond, http.MethodX, "pattern", ...)` registration call sites in
// pkg/api/rest/server.go's registerRoutes. It deliberately excludes the
// `public`/`publicIf` METHOD DEFINITIONS themselves (which call
// s.publicIf(true, method, pattern, h) with variables, not literals) by
// requiring a literal http.MethodX token in the match.
var restPublicCallPattern = regexp.MustCompile(`s\.public(?:If\([^,]+,\s*)?\(?http\.Method(\w+),\s*"([^"]+)"`)

// restPublicPathIdentifiers resolves a non-literal path argument a REST
// public-route registration passes (an imported constant) to the literal
// path it names. clusterenroll.JoinHTTPPath is the only one today:
// pkg/clusterenroll/join_http.go documents it as "the SAME admin HTTPS
// listener ... alongside /v1/ca/bootstrap" -- i.e. it is a public route on
// this exact listener, just spelled as a shared constant rather than a
// literal so the client package (pkg/clusterenroll) and the server
// (pkg/api/rest) can never drift on the path string.
var restPublicPathIdentifiers = map[string]string{
	`clusterenroll.JoinHTTPPath`: "/v1/enroll/join",
}

// restPublicCallPatternWithIdent additionally matches a call whose path
// argument is a bare identifier/selector rather than a string literal, e.g.
// `s.public(http.MethodPost, clusterenroll.JoinHTTPPath, ...)`.
var restPublicCallPatternWithIdent = regexp.MustCompile(`s\.public(?:If\([^,]+,\s*)?\(?http\.Method(\w+),\s*([A-Za-z][A-Za-z0-9_.]*)\s*,`)

// minRESTPublicRoutes is the discovery floor: fewer than this and the scan
// itself is broken (a regex that stopped matching looks identical to a
// clean tree with no public routes).
const minRESTPublicRoutes = 5

// restRegisterRoutesFile is relative to the repository root.
const restRegisterRoutesFile = "pkg/api/rest/server.go"

// restPublicAllowlist is the curated, reviewed set of REST routes reachable
// without authentication. Adding to it is a deliberate, reviewed edit --
// see the file doc comment.
var restPublicAllowlist = map[string]struct{}{
	"GET /v1/config/initialized":   {}, // first-boot setup wizard checks provisioning state pre-credential.
	"GET /health":                  {}, // load balancer / liveness probes reach it without a client cert.
	"POST /invitations/redeem":     {}, // the one write any caller may make with no credential at all.
	"GET /v1/ca/bootstrap":         {}, // joining nodes fetch the trust anchor before they hold credentials.
	"POST /v1/enroll/join":         {}, // SPKI/Direct cluster join: the joiner has no credentials yet.
	"POST /v1/cluster/keydelivery": {}, // cluster peer plane: self-authorized by node cert + placement table.
	"GET /v1/cluster/nodekey":      {}, // cluster peer plane: self-authorized by node cert + placement table.
}

// TestPublicSurface_REST_ExactAllowlist scans pkg/api/rest/server.go for
// every s.public/s.publicIf registration call and asserts the resulting set
// is exactly restPublicAllowlist.
func TestPublicSurface_REST_ExactAllowlist(t *testing.T) {
	root := repoRoot(t)
	src := readSourceFile(t, filepath.Join(root, restRegisterRoutesFile))

	found := map[string]struct{}{}
	for _, m := range restPublicCallPattern.FindAllStringSubmatch(src, -1) {
		found[strings.ToUpper(m[1])+" "+m[2]] = struct{}{}
	}
	for _, m := range restPublicCallPatternWithIdent.FindAllStringSubmatch(src, -1) {
		path, ok := restPublicPathIdentifiers[m[2]]
		if !ok {
			t.Fatalf("REST public-route registration passes unresolved identifier %q as its path "+
				"(method %s) -- add it to restPublicPathIdentifiers with the literal path it names, "+
				"reviewed the same way a new allowlist entry is", m[2], m[1])
		}
		found[strings.ToUpper(m[1])+" "+path] = struct{}{}
	}

	if len(found) < minRESTPublicRoutes {
		t.Fatalf("found only %d public REST route registration(s) in %s, floor is %d; "+
			"the scan likely stopped matching s.public/s.publicIf call sites", len(found), restRegisterRoutesFile, minRESTPublicRoutes)
	}

	assertExactSet(t, "REST public route", found, restPublicAllowlist)
}

// quicHandlePublicPattern matches `s.handlePublicIf(cond, method, handler)`
// registration call sites anywhere under pkg/api/quic. method is a bare
// identifier (a package-level `methodXxx = "..."` constant), resolved via
// quicMethodConstants.
var quicHandlePublicPattern = regexp.MustCompile(`s\.handlePublicIf\([^,]+,\s*([A-Za-z_][A-Za-z0-9_]*)\s*,`)

// quicMethodConstPattern finds `methodXxx = "literal"` declarations so a
// handlePublicIf call's bare identifier argument resolves to its wire name
// without this file hand-copying it a second time.
var quicMethodConstPattern = regexp.MustCompile(`(method[A-Za-z0-9_]*)\s*=\s*"([^"]+)"`)

// quicPublicAllowlist is the curated, reviewed set of native QUIC frame
// methods served without authentication or authorization.
var quicPublicAllowlist = map[string]struct{}{
	"invitation.Redeem": {}, // the one frame method with no registered permission at all.
}

// TestPublicSurface_QUIC_ExactAllowlist scans every file under pkg/api/quic
// for handlePublicIf registrations and asserts the resolved method-name set
// is exactly quicPublicAllowlist.
func TestPublicSurface_QUIC_ExactAllowlist(t *testing.T) {
	root := repoRoot(t)
	quicDir := filepath.Join(root, "pkg", "api", "quic")

	constants := map[string]string{}
	var calls []string
	walkGoFilesText(t, quicDir, func(path, src string) {
		for _, m := range quicMethodConstPattern.FindAllStringSubmatch(src, -1) {
			constants[m[1]] = m[2]
		}
		for _, m := range quicHandlePublicPattern.FindAllStringSubmatch(src, -1) {
			calls = append(calls, m[1])
		}
	})

	if len(calls) == 0 {
		t.Fatal("found no s.handlePublicIf(...) call under pkg/api/quic; the scan likely stopped matching")
	}

	found := map[string]struct{}{}
	for _, ident := range calls {
		lit, ok := constants[ident]
		if !ok {
			t.Fatalf("handlePublicIf registers %s, which no methodXxx = \"...\" constant under pkg/api/quic resolves; "+
				"the scan cannot name the public method", ident)
		}
		found[lit] = struct{}{}
	}

	assertExactSet(t, "QUIC public method", found, quicPublicAllowlist)
}

// mcpEnrollServerFile is where the PUBLIC enrolment MCP server is
// constructed (pkg/api/mcp.newEnrollServer) and its tool(s) registered.
const mcpEnrollServerFile = "pkg/api/mcp/tools_invitations.go"

// mcpAddToolPattern matches `srv.AddTool(toolXxx(), ...)` call sites: the
// enrolment server's own tool registrations.
var mcpAddToolPattern = regexp.MustCompile(`srv\.AddTool\((tool[A-Za-z0-9_]*)\(\)`)

// mcpToolNameConstPattern finds `toolNameXxx = "literal"` declarations so an
// AddTool call's constructor function resolves to the tool's wire name.
var mcpToolNameConstPattern = regexp.MustCompile(`(toolName[A-Za-z0-9_]*)\s*=\s*"([^"]+)"`)

// mcpToolConstructorPattern links a `func toolXxx() mcp.Tool` definition to
// the toolNameYyy constant its mcp.NewTool call passes as the tool's name,
// so mcpAddToolPattern's constructor-function match resolves all the way to
// a literal tool name.
var mcpToolConstructorPattern = regexp.MustCompile(`func (tool[A-Za-z0-9_]*)\(\) mcp\.Tool \{\s*return mcp\.NewTool\((toolName[A-Za-z0-9_]*)`)

// mcpEnrollAllowlist is the curated, reviewed set of tools the SEPARATE,
// public /mcp/enroll server carries.
var mcpEnrollAllowlist = map[string]struct{}{
	"qrdb_invitation_redeem": {}, // the only tool the public enrolment server carries.
}

// TestPublicSurface_MCPEnroll_ExactAllowlist scans
// pkg/api/mcp/tools_invitations.go's public enrolment server construction
// for its AddTool calls and asserts the resolved tool-name set is exactly
// mcpEnrollAllowlist.
func TestPublicSurface_MCPEnroll_ExactAllowlist(t *testing.T) {
	root := repoRoot(t)
	src := readSourceFile(t, filepath.Join(root, mcpEnrollServerFile))

	nameConsts := map[string]string{}
	for _, m := range mcpToolNameConstPattern.FindAllStringSubmatch(src, -1) {
		nameConsts[m[1]] = m[2]
	}
	constructors := map[string]string{}
	for _, m := range mcpToolConstructorPattern.FindAllStringSubmatch(src, -1) {
		constructors[m[1]] = m[2]
	}

	enrollSrvIdx := strings.Index(src, "func (s *Server) newEnrollServer()")
	if enrollSrvIdx < 0 {
		t.Fatalf("%s does not define newEnrollServer; the public enrolment server construction moved", mcpEnrollServerFile)
	}
	body := src[enrollSrvIdx:]

	calls := mcpAddToolPattern.FindAllStringSubmatch(body, -1)
	if len(calls) == 0 {
		t.Fatal("newEnrollServer registers no srv.AddTool(...) calls; the scan likely stopped matching")
	}

	found := map[string]struct{}{}
	for _, m := range calls {
		ctor := m[1]
		nameConst, ok := constructors[ctor]
		if !ok {
			t.Fatalf("newEnrollServer calls %s(), which no `func %s() mcp.Tool { return mcp.NewTool(toolNameXxx, ...` "+
				"pattern resolves to a toolName constant", ctor, ctor)
		}
		lit, ok := nameConsts[nameConst]
		if !ok {
			t.Fatalf("tool constructor %s names %s, which no %s = \"...\" constant resolves", ctor, nameConst, nameConst)
		}
		found[lit] = struct{}{}
	}

	assertExactSet(t, "MCP enrolment tool", found, mcpEnrollAllowlist)
}

// publicGRPCMethodsImplPattern matches a production (non-test)
// implementation of the apimodule capability-module hook
// `PublicGRPCMethods() []string` that RegisterPublicGRPCMethods consumes.
var publicGRPCMethodsImplPattern = regexp.MustCompile(`func\s*\([^)]*\)\s*PublicGRPCMethods\(\)\s*\[\]string`)

// grpcUnixBuiltinPublicPattern matches the built-in (not module-sourced)
// `s.authMW.RegisterPublicGRPCMethods([]string{pb.QRDBService_Xxx_FullMethodName})`
// call shape both pkg/api/grpc/server.go and pkg/api/unix/server.go use to
// register RedeemInvitation as public.
var grpcUnixBuiltinPublicPattern = regexp.MustCompile(`RegisterPublicGRPCMethods\(\[\]string\{pb\.QRDBService_(\w+)_FullMethodName\}\)`)

// grpcServerFile and unixServerFile are relative to the repository root.
const (
	grpcServerFile = "pkg/api/grpc/server.go"
	unixServerFile = "pkg/api/unix/server.go"
)

// grpcUnixPublicAllowlist is the curated, reviewed set of gRPC/Unix RPCs
// registered public through the built-in literal call shape above. Adding to
// it is a deliberate, reviewed edit -- see the file doc comment.
var grpcUnixPublicAllowlist = map[string]struct{}{
	"RedeemInvitation": {}, // the one PUBLIC built-in RPC: an invited user holds no credential yet.
}

// TestPublicSurface_GRPCAndUnix_ExactAllowlist asserts two things about gRPC
// and the Unix socket, which share AuthMiddleware.RegisterPublicGRPCMethods:
//
//  1. No non-test file anywhere in this module implements the capability
//     module hook PublicGRPCMethods() []string. The day one does, this test
//     names exactly what changed, and recording what it exposes in
//     grpcUnixPublicAllowlist is the reviewed next step.
//  2. The built-in literal registration both pkg/api/grpc/server.go and
//     pkg/api/unix/server.go make is exactly grpcUnixPublicAllowlist, on
//     EACH file independently -- Unix registers its own copy rather than
//     relying on a shared instance, so both must carry it.
func TestPublicSurface_GRPCAndUnix_ExactAllowlist(t *testing.T) {
	root := repoRoot(t)

	var implementers []string
	walkGoFilesText(t, root, func(path, src string) {
		if strings.HasSuffix(path, "_test.go") {
			return
		}
		if publicGRPCMethodsImplPattern.MatchString(src) {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			implementers = append(implementers, filepath.ToSlash(rel))
		}
	})

	if len(implementers) != 0 {
		sort.Strings(implementers)
		t.Fatalf("gRPC/unix public surface grew a capability-module source: %s now implement PublicGRPCMethods() []string. "+
			"This is a deliberate capability addition -- record what it exposes in this file's allowlist "+
			"(mirroring restPublicAllowlist/quicPublicAllowlist/mcpEnrollAllowlist) rather than leaving this "+
			"gate to merely fail.", strings.Join(implementers, ", "))
	}

	for _, file := range []string{grpcServerFile, unixServerFile} {
		src := readSourceFile(t, filepath.Join(root, file))
		found := map[string]struct{}{}
		for _, m := range grpcUnixBuiltinPublicPattern.FindAllStringSubmatch(src, -1) {
			found[m[1]] = struct{}{}
		}
		if len(found) == 0 {
			t.Fatalf("found no built-in RegisterPublicGRPCMethods([]string{pb.QRDBService_Xxx_FullMethodName}) "+
				"call in %s; the scan likely stopped matching", file)
		}
		assertExactSet(t, file+" built-in public RPC", found, grpcUnixPublicAllowlist)
	}
}

// readSourceFile reads path or fails the test. Tests in this package read
// only this repository's own checkout, which nothing else writes while the
// gate runs.
func readSourceFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // path is built from repoRoot + a literal relative path in this file
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}

// walkGoFiles calls fn(path, source) for every non-generated .go file under
// dir.
func walkGoFilesText(t *testing.T, dir string, fn func(path, src string)) {
	t.Helper()
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		fn(path, readSourceFile(t, path))
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", dir, err)
	}
}

// assertExactSet fails the test with the specific additions/omissions when
// got != want, rather than a single pass/fail with no detail.
func assertExactSet(t *testing.T, label string, got, want map[string]struct{}) {
	t.Helper()

	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("%s %q is public but NOT in the reviewed allowlist -- add it deliberately or refuse it", label, k)
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s %q is in the reviewed allowlist but is no longer public -- remove it from the allowlist", label, k)
		}
	}
}
