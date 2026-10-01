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
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// This gate answers "what stops the NEXT compiled-out subsystem repeating
// go-qrdb#520".
//
// The classifier is now open — any error implementing
// protocol.CapabilityError is a refusal — but open is not the same as
// automatic. An error that declares nothing still halts Apply, deliberately,
// because a replica that has diverged must stop rather than serve wrong data.
// So the residual risk is a refusal that forgets to declare itself, and there
// is exactly one place in this repository where "refusal" is the WHOLE
// semantics of the code and forgetting is therefore likeliest: the stub a
// subsystem ships for the build that compiles it out. That set is enumerable
// from the build constraints, so it is checked rather than trusted.
//
// The rule: a not-compiled refusal is a TYPE, never a bare errors.New. That is
// CLAUDE.md's typed-errors rule as well, which an untyped sentinel quietly
// sidesteps — and sidestepping it here is what cost a shard every subsequent
// linearizable read for the length of the caller's deadline.
//
// Deliberately NARROW. It matches not-compiled sentinels only, not every error
// in a stub file: the other sentinels those files declare (ErrNotFound,
// ErrEmptyIndex) mirror the real build's vocabulary and are ordinary
// application errors, correctly fatal-by-default at the Apply seam if they
// ever reached one. Narrow does not make the RULE narrow; the rule is the
// paragraph above, and this is a floor under it.

// subsystemTagPrefixes identify a build constraint that compiles a subsystem
// OUT. A file whose constraint negates one of these is a stub.
var subsystemTagPrefixes = []string{"qrdb_", "gpu"}

// notCompiledMarkers appear in the name or message of a refusal that means
// "this build does not have that subsystem".
var notCompiledMarkers = []string{"NotCompiled", "not compiled"}

// Discovery floors. Every assertion below is inside a loop, and an empty loop
// reports success.
const (
	// minStubFiles is the number of build-tag stub files under pkg/. Measured
	// at 8 on the tree that fixed go-qrdb#520. A deliberate deletion lowers
	// this in the same commit.
	minStubFiles = 8

	// minNotCompiledSentinels is the number of not-compiled refusal
	// declarations found across them. Measured at 5 — timeseries, hnsw, ivf,
	// flat, gpu. If this reaches zero the scan has stopped matching, which
	// looks identical to a clean tree without this floor.
	minNotCompiledSentinels = 5
)

// notCompiledSentinel is one package-level refusal declaration in a stub file.
type notCompiledSentinel struct {
	file  string
	line  int
	name  string
	typed bool
}

// TestNoStubDeclaresABareNotCompiledSentinel is the gate.
func TestNoStubDeclaresABareNotCompiledSentinel(t *testing.T) {
	files, sentinels := collectStubSentinels(t)

	if files < minStubFiles {
		t.Fatalf("found %d build-tag stub files under pkg/, floor is %d; either the "+
			"scan is broken or stubs were deleted — if deleted, lower minStubFiles "+
			"in the same commit", files, minStubFiles)
	}
	if len(sentinels) < minNotCompiledSentinels {
		t.Fatalf("found %d not-compiled refusals across %d stub files, floor is %d; "+
			"a scan that matches nothing looks exactly like a clean tree",
			len(sentinels), files, minNotCompiledSentinels)
	}

	for _, s := range sentinels {
		if s.typed {
			continue
		}
		t.Errorf("%s:%d: %s is a BARE error sentinel. A compiled-out subsystem's "+
			"refusal must be a type implementing protocol.CapabilityError, or the "+
			"Apply classifier cannot tell it from a storage fault and halts the "+
			"state machine — after which every linearizable read on that shard, on "+
			"any engine sharing it, burns the caller's deadline (go-qrdb#520). "+
			"Declare a NotCompiledError type with a Capability method and point "+
			"this var at it.", s.file, s.line, s.name)
	}
}

// TestTheStubFloorsRefuseAVacuousScan asserts the floors FIRE. A floor nobody
// has watched go red is a floor nobody has verified.
func TestTheStubFloorsRefuseAVacuousScan(t *testing.T) {
	for _, tc := range []struct {
		name              string
		files, sentinels  int
		wantFiles, wantSt bool
	}{
		{"nothing found at all", 0, 0, true, true},
		{"files found, sentinels vanished", minStubFiles, 0, false, true},
		{"a stub file went missing", minStubFiles - 1, minNotCompiledSentinels, true, false},
		{"the measured tree", minStubFiles, minNotCompiledSentinels, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.files < minStubFiles; got != tc.wantFiles {
				t.Errorf("file floor fired = %v, want %v for %d files", got, tc.wantFiles, tc.files)
			}
			if got := tc.sentinels < minNotCompiledSentinels; got != tc.wantSt {
				t.Errorf("sentinel floor fired = %v, want %v for %d sentinels",
					got, tc.wantSt, tc.sentinels)
			}
		})
	}
}

// collectStubSentinels walks pkg/ for files whose build constraint compiles a
// subsystem out, and returns the stub-file count with every not-compiled
// refusal declared in them.
func collectStubSentinels(t *testing.T) (int, []notCompiledSentinel) {
	t.Helper()
	root := repoRoot(t)
	pkgDir := filepath.Join(root, "pkg")

	files := 0
	var sentinels []notCompiledSentinel
	fset := token.NewFileSet()
	err := filepath.WalkDir(pkgDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// #nosec G122 -- a test reading this repository's own checkout; nothing else writes the tree while it walks.
		src, readErr := os.ReadFile(path) //nolint:gosec // path comes from the walk of this repository
		if readErr != nil {
			return readErr
		}
		file, parseErr := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		if !negatesASubsystemTag(file) {
			return nil
		}
		files++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		sentinels = append(sentinels, notCompiledVars(fset, file, filepath.ToSlash(rel))...)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", pkgDir, err)
	}
	sort.Slice(sentinels, func(i, j int) bool {
		if sentinels[i].file != sentinels[j].file {
			return sentinels[i].file < sentinels[j].file
		}
		return sentinels[i].line < sentinels[j].line
	})
	return files, sentinels
}

// negatesASubsystemTag reports whether the file's build constraint excludes it
// from a subsystem build — the shape `//go:build !qrdb_ts`.
func negatesASubsystemTag(file *ast.File) bool {
	for _, group := range file.Comments {
		for _, c := range group.List {
			constraint, ok := strings.CutPrefix(c.Text, "//go:build ")
			if !ok {
				continue
			}
			for _, prefix := range subsystemTagPrefixes {
				if strings.Contains(constraint, "!"+prefix) {
					return true
				}
			}
		}
	}
	return false
}

// notCompiledVars returns the package-level vars in file whose name or
// initialiser says "not compiled", recording whether each is typed.
//
// Typed means: initialised from a composite literal or a call that is not
// errors.New / fmt.Errorf. That is the whole distinction the classifier turns
// on, so it is the whole distinction checked here — this gate does not try to
// prove the type implements the interface, because the `var _
// protocol.CapabilityError = ...` assertion beside each declaration already
// makes the compiler do that, and a compiler is a better checker than a
// parser.
func notCompiledVars(fset *token.FileSet, file *ast.File, rel string) []notCompiledSentinel {
	var out []notCompiledSentinel
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				value := vs.Values[i]
				if !saysNotCompiled(name.Name, value) {
					continue
				}
				out = append(out, notCompiledSentinel{
					file:  rel,
					line:  fset.Position(name.Pos()).Line,
					name:  name.Name,
					typed: !isBareErrorConstructor(value),
				})
			}
		}
	}
	return out
}

// saysNotCompiled reports whether the declaration names, or its initialiser
// spells, an absent subsystem.
func saysNotCompiled(name string, value ast.Expr) bool {
	haystack := name
	ast.Inspect(value, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			haystack += " " + lit.Value
		}
		return true
	})
	for _, marker := range notCompiledMarkers {
		if strings.Contains(haystack, marker) {
			return true
		}
	}
	return false
}

// isBareErrorConstructor reports whether value is errors.New(...) or
// fmt.Errorf(...) — an error carrying a string and nothing a classifier can
// read.
func isBareErrorConstructor(value ast.Expr) bool {
	call, ok := value.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return (pkg.Name == "errors" && sel.Sel.Name == "New") ||
		(pkg.Name == "fmt" && sel.Sel.Name == "Errorf")
}
