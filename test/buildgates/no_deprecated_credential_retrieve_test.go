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
	"sort"
	"testing"
)

// This gate keeps the deleted []byte credential retrieval methods from
// returning (#22). A retrieved credential travels as a protected-memory
// *secure.Buffer; the former []byte Go forms were removed in the coordinated
// protected-memory signature wave.
//
// The deprecated names are:
//   - RetrieveCredential, RetrieveCredentialVersion (credentials.CredentialService)
//   - CredentialRetrieve, CredentialRetrieveVersion (service.CredentialServicer,
//     transport.CredentialClient and every transport that implements it)
//
// The scan walks declarations and interface methods over every non-generated Go
// file in the tree, tests and the sdk/go module included. It allows the
// protobuf wire RPC methods of the same names because they return protobuf
// response messages, not []byte.

// deprecatedRetrieveNames are the selector names no in-repo code may use.
var deprecatedRetrieveNames = map[string]bool{
	"RetrieveCredential":        true,
	"RetrieveCredentialVersion": true,
	"CredentialRetrieve":        true,
	"CredentialRetrieveVersion": true,
}

var removedCredentialStoreSelectors = map[string]bool{
	"RetrieveCredential":        true,
	"RetrieveCredentialVersion": true,
}

// bufferRetrieveNames are the replacements. Their call count is the discovery
// floor: a scan that walks files but sees none of them is not looking at the
// credential call sites at all.
var bufferRetrieveNames = map[string]bool{
	"RetrieveCredentialBuffer":        true,
	"RetrieveCredentialVersionBuffer": true,
	"CredentialRetrieveBuffer":        true,
	"CredentialRetrieveVersionBuffer": true,
}

// minBufferRetrieveSelectors is a floor below the number of selectors naming a
// Buffer retrieval method (measured near 150).
const minBufferRetrieveSelectors = 60

func resultIncludesByteSlice(results *ast.FieldList) bool {
	if results == nil {
		return false
	}
	for _, result := range results.List {
		array, ok := result.Type.(*ast.ArrayType)
		if !ok || array.Len != nil {
			continue
		}
		if ident, ok := array.Elt.(*ast.Ident); ok && ident.Name == "byte" {
			return true
		}
	}
	return false
}

type deprecatedDeclaration struct {
	name string
	pos  token.Pos
}

// retrieveDeclarations returns deleted []byte declarations in file, removed
// store-level selectors, and the number of Buffer-method selectors it saw.
func retrieveDeclarations(file *ast.File) (deprecated []deprecatedDeclaration, removedStoreSelectors []*ast.SelectorExpr, buffer int) {
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if deprecatedRetrieveNames[node.Name.Name] && resultIncludesByteSlice(node.Type.Results) {
				deprecated = append(deprecated, deprecatedDeclaration{name: node.Name.Name, pos: node.Name.Pos()})
			}
			return true
		case *ast.InterfaceType:
			for _, method := range node.Methods.List {
				ft, ok := method.Type.(*ast.FuncType)
				if !ok {
					continue
				}
				for _, name := range method.Names {
					if deprecatedRetrieveNames[name.Name] && resultIncludesByteSlice(ft.Results) {
						deprecated = append(deprecated, deprecatedDeclaration{name: name.Name, pos: name.Pos()})
					}
				}
			}
			return false
		}
		return true
	})
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch {
		case bufferRetrieveNames[sel.Sel.Name]:
			buffer++
		case removedCredentialStoreSelectors[sel.Sel.Name]:
			removedStoreSelectors = append(removedStoreSelectors, sel)
		}
		return true
	})
	return deprecated, removedStoreSelectors, buffer
}

// TestNoDeprecatedCredentialRetrieveReturns fails when any Go file in the
// repository reintroduces one of the deleted []byte credential retrieval
// methods.
func TestNoInRepoCallerOfDeprecatedCredentialRetrieve(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	walked, parsed := walkGoFiles(t, root, fset, ".")

	if walked < minGoFilesScanned {
		t.Fatalf("scanned %d non-generated .go files, floor is %d; either the scan is "+
			"broken or the tree shrank -- if deliberate, lower minGoFilesScanned in the "+
			"same commit", walked, minGoFilesScanned)
	}

	type hit struct {
		rel, name string
		line      int
	}
	var hits []hit
	var selectors []hit
	bufferSelectors := 0
	for _, pf := range parsed {
		deprecated, removedStoreSelectors, buffer := retrieveDeclarations(pf.file)
		bufferSelectors += buffer
		for _, decl := range deprecated {
			hits = append(hits, hit{rel: pf.rel, name: decl.name, line: fset.Position(decl.pos).Line})
		}
		for _, sel := range removedStoreSelectors {
			selectors = append(selectors, hit{rel: pf.rel, name: sel.Sel.Name, line: fset.Position(sel.Sel.Pos()).Line})
		}
	}

	if bufferSelectors < minBufferRetrieveSelectors {
		t.Fatalf("saw %d selectors naming a Buffer retrieval method, floor is %d; either "+
			"the scan is broken or the credential call sites moved -- if deliberate, lower "+
			"minBufferRetrieveSelectors in the same commit", bufferSelectors, minBufferRetrieveSelectors)
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].rel != hits[j].rel {
			return hits[i].rel < hits[j].rel
		}
		return hits[i].line < hits[j].line
	})
	for _, h := range hits {
		t.Errorf("%s:%d: %s reintroduces a deleted []byte credential retrieval method; declare only the Buffer form", h.rel, h.line, h.name)
	}
	sort.Slice(selectors, func(i, j int) bool {
		if selectors[i].rel != selectors[j].rel {
			return selectors[i].rel < selectors[j].rel
		}
		return selectors[i].line < selectors[j].line
	})
	for _, h := range selectors {
		t.Errorf("%s:%d: %s was deleted; call the Buffer form and Destroy the result", h.rel, h.line, h.name)
	}
}

// TestRetrieveDeclarations_RecognizesDeletedByteForms proves the detector
// reports what it is written to report: a green gate that cannot go red is
// worth nothing.
func TestRetrieveSelectors_RecognizesDeprecatedAndExemptsGRPC(t *testing.T) {
	const src = `package p

type S interface {
	RetrieveCredential(ctx context.Context, name string) ([]byte, error)
	CredentialRetrieveBuffer(ctx context.Context, name string) (*secure.Buffer, error)
	CredentialRetrieve(ctx context.Context, req *pb.CredentialRetrieveRequest) (*pb.CredentialRetrieveResponse, error)
}

func (s *svc) CredentialRetrieve(ctx context.Context, name string) ([]byte, error) { return nil, nil }
func (s *svc) CredentialRetrieveVersion(ctx context.Context, name string, version uint64) ([]byte, error) {
	return nil, nil
}
func (s *svc) CredentialRetrieve(ctx context.Context, req *pb.CredentialRetrieveRequest) (*pb.CredentialRetrieveResponse, error) {
	return nil, nil
}

func f() {
	_, _ = svc.RetrieveCredential(ctx, "k")
	_, _ = svc.RetrieveCredentialVersion(ctx, "k", 1)
	_, _ = svc.CredentialRetrieve(ctx, "k")
	_, _ = tr.CredentialRetrieveVersion(ctx, req)
	hook := svc.CredentialRetrieve
	_ = hook
	_, _ = client.CredentialRetrieve(ctx, &pb.CredentialRetrieveRequest{Name: "k"})
	_, _ = svc.CredentialRetrieveBuffer(ctx, "k")
	_, _ = client.CredentialRetrieveVersion(ctx, &other.Request{})
}
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	if err != nil {
		t.Fatalf("parsing fixture: %v", err)
	}
	deprecated, removedStoreSelectors, buffer := retrieveDeclarations(file)

	if got, want := len(deprecated), 3; got != want {
		t.Errorf("found %d deprecated declarations, want %d", got, want)
	}
	if got, want := len(removedStoreSelectors), 2; got != want {
		t.Errorf("found %d removed store selectors, want %d", got, want)
	}
	if buffer != 1 {
		t.Errorf("counted %d Buffer selectors, want 1", buffer)
	}
}
