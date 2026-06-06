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
// memory rules for go-qrdb and downstream consumers (go-xkms,
// go-quicbaas). The analyzer reports — it does not auto-fix.
//
// Rules enforced:
//
//  1. No `import "github.com/awnumar/memguard"` outside pkg/secure/.
//  2. No calls to `ssh.ParsePrivateKey`. Callers must use
//     `ssh.ParsePrivateKeyWithPassphrase` on Buffer-backed bytes.
//  3. Struct fields named Passphrase, Password, PIN, Secret, PrivateKey,
//     KeyMaterial, or RecoveryShare with Go type `string` or `[]byte`
//     are flagged. The fix is to switch to `*secure.Secret` or
//     `*secure.Buffer`.
package analyzer

import (
	"go/ast"
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

// bannedImport is the raw memguard import path. Only pkg/secure is
// permitted to reference it directly.
const bannedImport = "github.com/awnumar/memguard"

// allowedImportPrefix is the exact pkg/secure import path for the qrdb
// module. Source files whose package path contains this substring are
// exempt from the memguard ban.
var allowedImportSubstrings = []string{
	"go-qrdb/pkg/secure",
	"go-qrdb/sdk/go/secure",
}

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
	pkgPath := pass.Pkg.Path()
	inPkgSecure := false
	for _, sub := range allowedImportSubstrings {
		if strings.Contains(pkgPath, sub) {
			inPkgSecure = true
			break
		}
	}

	// Rule 1: ban raw memguard imports outside pkg/secure.
	for _, file := range pass.Files {
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == bannedImport && !inPkgSecure {
				pass.Reportf(imp.Pos(),
					"import of %q is forbidden outside pkg/secure; use github.com/jeremyhahn/go-qrdb/pkg/secure instead",
					bannedImport)
			}
		}
	}

	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

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
	insp.Preorder([]ast.Node{(*ast.StructType)(nil)}, func(n ast.Node) {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			if !isSensitiveType(field.Type) {
				continue
			}
			for _, name := range field.Names {
				if _, hit := sensitiveFieldNames[name.Name]; !hit {
					continue
				}
				pass.Reportf(name.Pos(),
					"field %q holds sensitive material as %s; switch to *secure.Secret or *secure.Buffer",
					name.Name, typeDescription(field.Type))
			}
		}
	})

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
