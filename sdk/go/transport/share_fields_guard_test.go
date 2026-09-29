// Copyright (c) 2026 Jeremy Hahn
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package transport

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shareCarryingRequests are the only request types that may carry share
// material: an authenticated holder's quorum submission. Any other request
// with a share field is an unauthenticated share path, which the one quorum
// accumulator exists to make impossible.
var shareCarryingRequests = map[string]bool{
	"QuorumSubmitRequest":       true,
	"TenantQuorumSubmitRequest": true,
}

// minRequestTypes is the discovery floor: every assertion below runs inside
// a loop over the request types found, and an empty loop reports success.
const minRequestTypes = 100

// isCount reports whether a field type is an integer: a share COUNT (such as
// the N of a Shamir initialization), not share material.
func isCount(expr ast.Expr) bool {
	id, ok := expr.(*ast.Ident)
	return ok && (id.Name == "int" || id.Name == "int32" || id.Name == "int64" || id.Name == "uint32")
}

// TestRequests_OnlyQuorumSubmissionsCarryShares walks every exported
// *Request struct in this package and refuses a non-count field named Share
// or Shares outside the quorum submissions.
func TestRequests_OnlyQuorumSubmissionsCarryShares(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok || !spec.Name.IsExported() || !strings.HasSuffix(spec.Name.Name, "Request") {
				return true
			}
			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}
			found++
			for _, field := range st.Fields.List {
				if isCount(field.Type) {
					continue
				}
				for _, id := range field.Names {
					if (id.Name == "Share" || id.Name == "Shares") && !shareCarryingRequests[spec.Name.Name] {
						t.Errorf("%s.%s carries share material outside an authenticated quorum submission", spec.Name.Name, id.Name)
					}
				}
			}
			return true
		})
	}
	if found < minRequestTypes {
		t.Fatalf("found %d request types, want at least %d: the walk is not seeing the package", found, minRequestTypes)
	}
}
