// Copyright 2026 Jeremy Hahn
// SPDX-License-Identifier: Apache-2.0
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

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestSysMigratorWiring_RunStartUsesBuildSysMigrator is the go-qrdb#550
// call-site wiring-pin test. TestBuildSysMigrator_PinsSealConfigEnabled
// proves buildSysMigrator itself reads f.sealConfig.Enabled correctly, but
// that alone does not prove runStart actually calls buildSysMigrator: a
// caller could bypass the seam entirely by calling newDaemonSystemMigrator
// directly with a hardcoded sealEnabled, and a unit test of buildSysMigrator
// in isolation would stay green while a live seal-disabled daemon silently
// went back to accepting enable-encryption requests it cannot service.
//
// Exercising this end-to-end would mean dialing the gRPC/Unix transport of a
// fully booted raft-mode daemon (REST's route is gated on barrier presence
// and never reaches this code at all, so it cannot be used to observe the
// regression) -- substantial live-process infrastructure this package does
// not otherwise have, to check a single call expression. Parsing the
// production source is the same trade repo's own
// test/buildgates/action_pins_test.go makes for workflow pins: cheap,
// deterministic, and it fails exactly when the wiring it protects is
// touched, which a behavioral test exercising a much larger surface would
// not do any more reliably.
//
// It parses every non-test .go file in this package (not just start.go) so
// attribution of each call site to its enclosing function is correct
// regardless of which file declares runStart or buildSysMigrator, and
// fails if:
//   - runStart never assigns sysMigrator := buildSysMigrator(f, migrateCh)
//     with exactly those two identifier arguments in that order (a dead
//     buildSysMigrator call whose result is discarded, or a call wired to
//     the wrong variables, would otherwise still satisfy a looser "is
//     buildSysMigrator called somewhere in runStart" check), or
//   - anything outside buildSysMigrator's own body calls
//     newDaemonSystemMigrator directly (test files are exempt: they
//     legitimately construct migrators directly to unit-test them).
func TestSysMigratorWiring_RunStartUsesBuildSysMigrator(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	pkgDir := filepath.Dir(thisFile)

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, pkgDir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parser.ParseDir(%s): %v", pkgDir, err)
	}

	// identName returns the identifier name for a simple *ast.Ident
	// expression, or "" for anything else (a literal, a selector, a nested
	// call, ...), which never matches the exact identifiers this test looks
	// for.
	identName := func(e ast.Expr) string {
		id, ok := e.(*ast.Ident)
		if !ok {
			return ""
		}
		return id.Name
	}

	var (
		runStartWiresSysMigrator bool
		directNewCallSites       []string
	)

	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				isRunStart := fn.Name.Name == "runStart"
				isBuildSysMigrator := fn.Name.Name == "buildSysMigrator"

				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if isRunStart {
						if assign, ok := n.(*ast.AssignStmt); ok &&
							len(assign.Lhs) == 1 && len(assign.Rhs) == 1 &&
							identName(assign.Lhs[0]) == "sysMigrator" {
							if call, ok := assign.Rhs[0].(*ast.CallExpr); ok &&
								identName(call.Fun) == "buildSysMigrator" &&
								len(call.Args) == 2 &&
								identName(call.Args[0]) == "f" &&
								identName(call.Args[1]) == "migrateCh" {
								runStartWiresSysMigrator = true
							}
						}
					}

					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					if identName(call.Fun) == "newDaemonSystemMigrator" && !isBuildSysMigrator {
						pos := fset.Position(call.Pos())
						directNewCallSites = append(directNewCallSites,
							pos.String()+" (in "+fn.Name.Name+")")
					}
					return true
				})
			}
		}
	}

	if !runStartWiresSysMigrator {
		t.Error("runStart does not assign sysMigrator := buildSysMigrator(f, migrateCh): " +
			"the seal.enabled wiring seam is bypassed, dead, or mis-wired")
	}
	if len(directNewCallSites) > 0 {
		t.Errorf("newDaemonSystemMigrator called directly outside buildSysMigrator: %v", directNewCallSites)
	}
}
