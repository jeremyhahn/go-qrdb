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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This gate keeps go-qrdb#557's property from eroding one handler at a time.
//
// Every typed error a gRPC or QUIC handler returns is classified by ONE table,
// pkg/api/errclass, and rendered through the transport's mapper
// (mapServiceError). A handler that instead writes
//
//	status.Error(codes.InvalidArgument, err.Error())
//
// has classified the error ITSELF, at the call site, where no parity test can
// see it: that is exactly how a missing client certificate answered
// InvalidArgument from four gRPC barrier handlers while every other path
// answered its declared class. The review that found it found it by reading.
//
// So the gate is structural: in the gRPC and QUIC transport packages, a
// status built from an error's own text — status.Error(code, X.Error()) or
// status.Errorf(code, ..., err) — fails. A status built from a literal the
// handler authored ("tenant_id is required") is the handler's own refusal and
// is left alone.
//
// It parses every file regardless of build tags, because a handler behind a
// tag is as reachable in the build that has the tag.

// errorMappingPackages are the transport packages whose handlers must render
// service errors through their mapper.
var errorMappingPackages = []string{"pkg/api/grpc", "pkg/api/quic", "pkg/api/unix"}

// mapperFiles hold the mappers themselves: the one place a status IS built
// from a classified error, which is the point of routing through them.
var mapperFiles = map[string]string{
	"pkg/api/grpc/error_status.go": "mapServiceError: renders errclass's class for the error",
}

// minimumStatusCalls is the discovery floor: every assertion below is inside a
// loop over status calls, and an empty loop reports success.
const minimumStatusCalls = 20

func TestTransportHandlersRouteServiceErrorsThroughTheMapper(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	seen := 0
	var violations []string

	for _, dir := range errorMappingPackages {
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			if _, mapper := mapperFiles[dir+"/"+name]; mapper {
				continue
			}
			path := filepath.Join(root, dir, name)
			file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isStatusCall(call) {
					return true
				}
				seen++
				if reason := builtFromAnError(call); reason != "" {
					violations = append(violations, fset.Position(call.Pos()).String()+": "+reason)
				}
				return true
			})
		}
	}

	if seen < minimumStatusCalls {
		t.Fatalf("found %d status.Error/Errorf calls, under the floor of %d: the scan is not "+
			"seeing the handlers", seen, minimumStatusCalls)
	}
	for _, v := range violations {
		t.Errorf("%s — render it with mapServiceError so pkg/api/errclass classifies it", v)
	}
}

// isStatusCall reports whether call is status.Error or status.Errorf.
func isStatusCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "status" && (sel.Sel.Name == "Error" || sel.Sel.Name == "Errorf")
}

// builtFromAnError reports why a status call renders an error's own text, or
// "" when it renders a message the handler authored.
func builtFromAnError(call *ast.CallExpr) string {
	for _, arg := range call.Args[1:] {
		if inner, ok := arg.(*ast.CallExpr); ok {
			if sel, ok := inner.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" && len(inner.Args) == 0 {
				return "status built from an error's Error() text"
			}
		}
		if id, ok := arg.(*ast.Ident); ok && (id.Name == "err" || strings.HasSuffix(id.Name, "Err")) {
			return "status formatted from error " + id.Name
		}
	}
	return ""
}

// TestErrorMappingGate_Detects is the gate's control: it must flag both shapes
// and pass a handler-authored literal, or a green run proves nothing.
func TestErrorMappingGate_Detects(t *testing.T) {
	for src, want := range map[string]bool{
		`status.Error(codes.InvalidArgument, err.Error())`:             true,
		`status.Error(codes.Unimplemented, (&X{}).Error())`:            true,
		`status.Errorf(codes.Internal, "audit query: %v", err)`:        true,
		`status.Error(codes.InvalidArgument, "tenant_id is required")`: false,
		`status.Errorf(codes.Internal, "internal server error")`:       false,
	} {
		expr, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		call := expr.(*ast.CallExpr)
		if !isStatusCall(call) {
			t.Fatalf("%q not recognised as a status call", src)
		}
		if got := builtFromAnError(call) != ""; got != want {
			t.Errorf("%q flagged=%v, want %v", src, got, want)
		}
	}
}

// The REST transport renders through writeErrorJSON(w, status, err), so its
// equivalent of a hand-built status is a literal http.StatusX paired with an
// error VALUE the handler received rather than authored. Before this gate,
// ten admin and rebalance handlers answered every service error 500 — a
// caller's own deadline, a lost leader — because the status was written at
// the call site, where pkg/api/errclass never saw the error.
//
// The gate is syntactic, so it keys on the shape: a literal status with a bare
// identifier as the error. A literal status with a composite literal the
// handler built (&ReadBodyError{...}, &MissingParameterError{...}) is the
// handler's own refusal and is left alone.

// restPackage is the transport whose handlers write errors via writeErrorJSON.
const restPackage = "pkg/api/rest"

// restFixedStatusExemptions are the handlers whose fixed status is the
// endpoint's contract for ANY error, keyed file:function. Each carries why.
var restFixedStatusExemptions = map[string]string{
	"handlers.go:handleHealth": "a health probe answers 503 for any failure; " +
		"load balancers read the status, not the class",
}

// minimumWriteErrorCalls is the REST discovery floor.
const minimumWriteErrorCalls = 300

func TestRESTHandlersRouteServiceErrorsThroughMapHTTPError(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	dir := filepath.Join(root, restPackage)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", restPackage, err)
	}

	seen := 0
	usedExemptions := map[string]bool{}
	var violations []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := name + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || !isWriteErrorJSONCall(call) {
					return true
				}
				seen++
				reason := fixedStatusForReceivedError(call)
				if reason == "" {
					return true
				}
				if _, exempt := restFixedStatusExemptions[key]; exempt {
					usedExemptions[key] = true
					return true
				}
				violations = append(violations, fset.Position(call.Pos()).String()+" ("+key+"): "+reason)
				return true
			})
		}
	}

	if seen < minimumWriteErrorCalls {
		t.Fatalf("found %d writeErrorJSON calls, under the floor of %d: the scan is not "+
			"seeing the handlers", seen, minimumWriteErrorCalls)
	}
	for _, v := range violations {
		t.Errorf("%s — pass mapHTTPError(err) so pkg/api/errclass classifies it", v)
	}
	// An exemption no call site needs is a standing licence for the next one.
	for key := range restFixedStatusExemptions {
		if !usedExemptions[key] {
			t.Errorf("exemption %s matches no fixed-status call; delete it", key)
		}
	}
}

// isWriteErrorJSONCall reports whether call is writeErrorJSON(w, status, err).
func isWriteErrorJSONCall(call *ast.CallExpr) bool {
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "writeErrorJSON" && len(call.Args) == 3
}

// fixedStatusForReceivedError reports why a writeErrorJSON call pairs a literal
// status with an error the handler received, or "" when it does not.
func fixedStatusForReceivedError(call *ast.CallExpr) string {
	sel, ok := call.Args[1].(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "http" || !strings.HasPrefix(sel.Sel.Name, "Status") {
		return ""
	}
	if id, ok := call.Args[2].(*ast.Ident); ok && id.Name != "nil" {
		return "http." + sel.Sel.Name + " fixed for received error " + id.Name
	}
	return ""
}

// TestRESTErrorMappingGate_Detects is the REST gate's control: it must flag a
// fixed status on a received error and pass both the mapper and a
// handler-authored refusal, or a green run proves nothing.
func TestRESTErrorMappingGate_Detects(t *testing.T) {
	for src, want := range map[string]bool{
		`writeErrorJSON(w, http.StatusInternalServerError, err)`:                   true,
		`writeErrorJSON(w, http.StatusServiceUnavailable, probeErr)`:               true,
		`writeErrorJSON(w, http.StatusNotFound, notFound)`:                         true,
		`writeErrorJSON(w, mapHTTPError(err), err)`:                                false,
		`writeErrorJSON(w, http.StatusBadRequest, &ReadBodyError{Err: err})`:       false,
		`writeErrorJSON(w, http.StatusBadRequest, &MissingParameterError{P: "x"})`: false,
	} {
		expr, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		call := expr.(*ast.CallExpr)
		if !isWriteErrorJSONCall(call) {
			t.Fatalf("%q not recognised as a writeErrorJSON call", src)
		}
		if got := fixedStatusForReceivedError(call) != ""; got != want {
			t.Errorf("%q flagged=%v, want %v", src, got, want)
		}
	}
}
