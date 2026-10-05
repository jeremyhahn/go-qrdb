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

package main

import (
	"bytes"
	"errors"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// committedForward is the generated forwarding-method file, relative to this
// package.
const committedForward = "../../replica_forward_methods.go"

// The committed forwarding file is exactly what the generator produces from
// today's SDK client interfaces.
func TestCommittedForwardFileIsCurrent(t *testing.T) {
	if err := runAll("", "", "", committedForward); err != nil {
		t.Fatalf("%v", err)
	}
}

// Every forwarded family's unary methods are forwarded, and a stream is not.
func TestGeneratedForward_UnaryForwardedStreamsNot(t *testing.T) {
	src, err := generateForward()
	if err != nil {
		t.Fatalf("generateForward: %v", err)
	}
	for _, m := range []string{"Put", "Get", "CompareAndSwap", "ExecuteTransaction", "ExecuteBatch", "LeaseGrant", "LockAcquire", "IndexQuery", "TSWrite", "UpsertVector", "InsertDocument"} {
		if !strings.Contains(string(src), "func (s *forwardingScope) "+m+"(") {
			t.Errorf("%s is not forwarded", m)
		}
	}
	if strings.Contains(string(src), "func (s *forwardingScope) ImportFile(") {
		t.Error("ImportFile, a stream, is forwarded")
	}
	if !strings.Contains(string(src), "Not forwarded -- a stream, which a non-replica refuses: ImportFile.") {
		t.Error("the generated file does not name the streams it leaves refused")
	}
}

func TestRunAll_WritesAndChecksBothFiles(t *testing.T) {
	dir := t.TempDir()
	scope, fwd := filepath.Join(dir, "scope.go"), filepath.Join(dir, "forward.go")
	if err := runAll(scope, "", fwd, ""); err != nil {
		t.Fatalf("runAll(write both): %v", err)
	}
	for path, want := range map[string]string{scope: committed, fwd: committedForward} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if w, _ := os.ReadFile(want); !bytes.Equal(got, w) {
			t.Errorf("%s differs from the committed %s", path, want)
		}
	}
	if err := runAll("", scope, "", fwd); err != nil {
		t.Fatalf("runAll(check both): %v", err)
	}
	// A stale forwarding file fails even when the scoped one is current.
	if err := os.WriteFile(fwd, []byte("package embedded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var drift *DriftError
	if err := runAll("", scope, "", fwd); !errors.As(err, &drift) || drift.Path != fwd {
		t.Fatalf("runAll(stale forward) = %v; want *DriftError naming it", err)
	}
	// A stale scoped file fails before the forwarding one is generated.
	if err := os.WriteFile(scope, []byte("package embedded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runAll("", scope, "", committedForward); !errors.As(err, &drift) || drift.Path != scope {
		t.Fatalf("runAll(stale scope) = %v; want *DriftError naming it", err)
	}
}

func TestForwardable(t *testing.T) {
	ctxPkg := types.NewPackage("context", "context")
	ctxType := types.NewNamed(types.NewTypeName(token.NoPos, ctxPkg, "Context", nil), types.NewInterfaceType(nil, nil), nil)
	ioPkg := types.NewPackage("io", "io")
	reader := types.NewNamed(types.NewTypeName(token.NoPos, ioPkg, "Reader", nil), types.NewInterfaceType(nil, nil), nil)
	errType := types.Universe.Lookup("error").Type()
	v := func(t types.Type) *types.Var { return types.NewParam(token.NoPos, nil, "", t) }
	sig := func(params, results []*types.Var) *types.Signature {
		return types.NewSignatureType(nil, nil, nil, types.NewTuple(params...), types.NewTuple(results...), false)
	}
	qual := func(p *types.Package) string { return p.Name() }
	for name, tc := range map[string]struct {
		sig  *types.Signature
		want bool
	}{
		"unary":            {sig([]*types.Var{v(ctxType), v(types.Typ[types.String])}, []*types.Var{v(errType)}), true},
		"no context":       {sig([]*types.Var{v(types.Typ[types.String])}, []*types.Var{v(errType)}), false},
		"no parameters":    {sig(nil, []*types.Var{v(errType)}), false},
		"no error":         {sig([]*types.Var{v(ctxType)}, []*types.Var{v(types.Typ[types.Int])}), false},
		"no results":       {sig([]*types.Var{v(ctxType)}, nil), false},
		"a reader":         {sig([]*types.Var{v(ctxType), v(reader)}, []*types.Var{v(errType)}), false},
		"a channel":        {sig([]*types.Var{v(ctxType)}, []*types.Var{v(types.NewChan(types.RecvOnly, types.Typ[types.Int])), v(errType)}), false},
		"a callback param": {sig([]*types.Var{v(ctxType), v(sig(nil, nil))}, []*types.Var{v(errType)}), false},
	} {
		if got := forwardable(tc.sig, qual); got != tc.want {
			t.Errorf("%s: forwardable = %v, want %v", name, got, tc.want)
		}
	}
}

// loadScope returns the type scope of the package at path.
func loadScope(t *testing.T, path string) *types.Scope {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{Mode: packages.NeedTypes | packages.NeedName | packages.NeedImports | packages.NeedDeps}, path)
	if err != nil || len(pkgs) != 1 || pkgs[0].Types == nil {
		t.Fatalf("loading %s: %v", path, err)
	}
	return pkgs[0].Types.Scope()
}

func TestRenderForward_RefusesWhatItCannotForward(t *testing.T) {
	var genErr *GenerateError
	if _, err := renderForward(types.NewScope(nil, token.NoPos, token.NoPos, "empty"), types.NewMethodSet(types.Typ[types.Int])); !errors.As(err, &genErr) || !strings.Contains(genErr.Error(), "KVClient") {
		t.Fatalf("renderForward(empty scope) = %v, want *GenerateError naming KVClient", err)
	}

	pkg := types.NewPackage(transportPkg, "transport")
	scope := types.NewScope(nil, token.NoPos, token.NoPos, "transport")
	scope.Insert(types.NewTypeName(token.NoPos, pkg, "KVClient", types.Typ[types.Int]))
	if _, err := renderForward(scope, types.NewMethodSet(types.Typ[types.Int])); !errors.As(err, &genErr) || !strings.Contains(genErr.Error(), "not an interface") {
		t.Fatalf("renderForward(non-interface) = %v, want *GenerateError", err)
	}

	// The real interfaces against a transport with no methods: the first
	// forwardable method is named as the one the remote transport lacks.
	sdkScope := loadScope(t, transportPkg)
	if _, err := renderForward(sdkScope, types.NewMethodSet(types.Typ[types.Int])); !errors.As(err, &genErr) || !strings.Contains(genErr.Error(), "quic.Transport has no") {
		t.Fatalf("renderForward(no remote methods) = %v, want *GenerateError naming the missing method", err)
	}
}
