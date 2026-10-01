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

// Package analyzer provides a go vet plugin that enforces the secure-
// memory rules for go-qrdb. The analyzer reports — it does not auto-fix.
//
// Rules enforced:
//
//  1. No `import "github.com/awnumar/memguard"` ANYWHERE, including
//     pkg/secure itself. Locked memory comes only from pkg/secure or, for the
//     small set of packages composing go-enclave primitives directly
//     (pkg/seal, pkg/clusterkeys, pkg/keydelivery), go-enclave itself
//     (go-qrdb#114, go-qrdb#553). memguard is retired from this repository;
//     there is no longer an exempt package for it.
//  2. No calls to `ssh.ParsePrivateKey`. Callers must use
//     `ssh.ParsePrivateKeyWithPassphrase` on Buffer-backed bytes.
//  3. Struct fields named Passphrase, Password, PIN, Secret, PrivateKey,
//     KeyMaterial, or RecoveryShare with Go type `string` or `[]byte`
//     are flagged. The fix is to switch to `*secure.Secret` or
//     `*secure.Buffer`.
//
// # Where it runs
//
// `make secure-analyzer` builds the singlechecker under
// cmd/secure-analyzer and sweeps it over EVERY Go module in the tree under
// EVERY combination in the Makefile's SUPPORTED_TAG_SETS. It runs in CI's
// Lint job beside check-publication-bound, and a green result is a
// precondition for merge.
//
// TWO THINGS THAT LOOK LIKE COVERAGE AND ARE NOT, both of which cost a
// measurement each before they were understood (go-qrdb#429):
//
//   - `go vet ./...` DOES NOT CROSS A MODULE BOUNDARY. sdk/go is its own
//     module and held 14 findings of its own; two separate root-module runs
//     saw none of them.
//   - A single tag combination answers about the files that combination
//     compiles and says nothing about the others. One finding on main was
//     visible only under the full tag set — the same blindness `make
//     vet-tags` exists to remove.
//
// Both are why this is a make target driving scripts/vet-modules.sh rather
// than a command line anyone is expected to remember.
//
// # Scope
//
// Rule 3 is scoped; rules 1 and 2 are not. The clauses, the argument for
// each and the counts they silenced are in scope.go, which is the document
// of record for that decision.
//
// # Downstream consumers
//
// This analyzer enforces the rules for THIS REPOSITORY. go-xkms and
// go-quicbaas import pkg/secure and are bound by the same rules, but
// nothing here reaches into their trees: an analyzer runs where its make
// target runs, and those repositories must wire up their own. Until they
// do, their compliance is unmeasured — stated here rather than implied
// away, because the sentence this replaces claimed a reach the tooling did
// not have, and a false claim of enforcement is worse than none.
//
// Tracked as go-xkms#549 and go-quicbaas#827 rather than asserted here.
package analyzer

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer is the entry point used by go vet and analysistest.
var Analyzer = &analysis.Analyzer{
	Name:     "securecheck",
	Doc:      "enforces go-qrdb/pkg/secure rules on sensitive material",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

// bannedImport is the raw memguard import path. go-enclave's adoption
// (go-qrdb#114, go-qrdb#553) retired memguard from this repository
// entirely, so unlike the SSH and field rules below, this one has no
// exempt package — not even pkg/secure, which used to be the one
// permitted to reference it directly and no longer does.
const bannedImport = "github.com/awnumar/memguard"

// bannedSSHFunc is the unsafe SSH private-key parse function. Callers
// must use ParsePrivateKeyWithPassphrase, which accepts Buffer-backed
// bytes for the passphrase.
const bannedSSHFunc = "ParsePrivateKey"

// sshImportPath is the expected import path of the SSH helper. We only
// flag calls to ParsePrivateKey on identifiers bound to this import.
const sshImportPath = "golang.org/x/crypto/ssh"

// sensitiveFieldNames is the set of struct field names that trigger a
// lint when declared as string or []byte. The name check is
// case-sensitive; struct conventions in this codebase use PascalCase for
// exported and camelCase for unexported.
var sensitiveFieldNames = map[string]struct{}{
	"Passphrase":    {},
	"Password":      {},
	"PIN":           {},
	"Secret":        {},
	"PrivateKey":    {},
	"KeyMaterial":   {},
	"RecoveryShare": {},
	"passphrase":    {},
	"password":      {},
	"pin":           {},
	"privateKey":    {},
	"keyMaterial":   {},
	"recoveryShare": {},
}

func run(pass *analysis.Pass) (any, error) {
	// Rule 1: ban raw memguard imports everywhere, no exempt package.
	for _, file := range pass.Files {
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == bannedImport {
				pass.Reportf(imp.Pos(),
					"import of %q is forbidden; use github.com/jeremyhahn/go-qrdb/pkg/secure or github.com/jeremyhahn/go-enclave instead",
					bannedImport)
			}
		}
	}

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector) //nolint:errcheck // type assertion on a value this container only ever holds

	// Rule 2: flag ssh.ParsePrivateKey calls.
	insp.Preorder([]ast.Node{(*ast.CallExpr)(nil)}, func(n ast.Node) {
		ce, ok := n.(*ast.CallExpr)
		if !ok {
			return
		}
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		if sel.Sel.Name != bannedSSHFunc {
			return
		}
		xIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		// Determine the import path bound to the identifier.
		for _, file := range pass.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if path != sshImportPath {
					continue
				}
				name := "ssh"
				if imp.Name != nil {
					name = imp.Name.Name
				}
				if name == xIdent.Name {
					pass.Reportf(ce.Pos(),
						"call to %s.%s is forbidden; use %s.ParsePrivateKeyWithPassphrase on *secure.Buffer-backed bytes",
						xIdent.Name, bannedSSHFunc, xIdent.Name)
					return
				}
			}
		}
	})

	// Rule 3: flag sensitive-named struct fields typed string or []byte.
	//
	// Walked per FILE rather than through the shared inspector, because two of
	// the four scope clauses are properties of the file — whether it is
	// generated, and whether it is a test — and the inspector's Preorder hands
	// out nodes with no file attached. Deriving the file from a position for
	// every struct in the package would be the same walk with a lookup in it.
	//
	// The flag-target clause is the one that cannot be decided per file: a
	// field declared in one file is bound to a flag in another, so the binding
	// set is collected across the whole package first.
	flagBound := flagBoundFields(pass, insp)
	for _, file := range pass.Files {
		if generatedFileScope(file) {
			continue
		}
		if testFileScope(pass.Fset.Position(file.Pos()).Filename) {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, field := range st.Fields.List {
				if !isSensitiveType(field.Type) {
					continue
				}
				if codecTaggedScope(field.Tag) {
					continue
				}
				for _, name := range field.Names {
					if _, hit := sensitiveFieldNames[name.Name]; !hit {
						continue
					}
					if obj, isVar := pass.TypesInfo.Defs[name].(*types.Var); isVar {
						if _, isFlag := flagBound[obj]; isFlag {
							continue
						}
					}
					pass.Reportf(name.Pos(),
						"field %q holds sensitive material as %s; switch to *secure.Secret or *secure.Buffer",
						name.Name, typeDescription(field.Type))
				}
			}
			return true
		})
	}

	return nil, nil
}

// isSensitiveType reports whether expr is `string` or `[]byte`, the two
// unsafe types we want to ban for sensitive fields.
func isSensitiveType(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "string"
	case *ast.ArrayType:
		if t.Len != nil {
			return false
		}
		ident, ok := t.Elt.(*ast.Ident)
		return ok && ident.Name == "byte"
	}
	return false
}

// typeDescription returns a short human-readable description of expr
// suitable for inclusion in a diagnostic message.
func typeDescription(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.ArrayType:
		if ident, ok := t.Elt.(*ast.Ident); ok {
			return "[]" + ident.Name
		}
	}
	return "unknown"
}
