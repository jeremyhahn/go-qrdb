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

// This gate is the structural half of go-qrdb#520's answer, and the reason the
// fix is not "add one more type to the list".
//
// The runtime rule inverted: a handler error halts Apply unless the ERROR
// declares itself an absent capability (protocol.CapabilityError). That keeps
// the property that matters — a replica which has actually diverged stops
// rather than serving wrong data — while removing the allowlist a new
// subsystem had to be added to and never was.
//
// The rule only holds where it is applied, though, and the seam it was missing
// from was not the allowlist at all: pkg/engine's MVCC engine had NO
// classification, so EVERY refusal it produced was fatal, under every build
// configuration. That is a gap a reviewer reading one diff cannot see, because
// the defect is the absence of a call.
//
// So this gate is DEFAULT-DENY at the seam: a new Update implementation is red
// until it either classifies or is listed below with a reason. That is the
// opposite failure mode from the allowlist it replaces. The allowlist was a
// list of error TYPES consulted at run time, which a new subsystem fell
// outside of silently; this is a list of SEAMS checked statically, which a new
// seam cannot fall outside of at all.
//
// It parses every file regardless of build tags — go/parser, not go/packages —
// because the seams most likely to be forgotten are the ones behind a tag that
// the default `go vet` never compiles.

// applyEntrySignature is the parameter type that identifies a Raft apply seam.
// A method taking a slice of committed entries is applying them; nothing else
// in this repository takes this type.
const applyEntrySignature = "[]sm.Entry"

// classifier is the function every apply seam must route handler errors
// through. Named as a string because this gate reads source, not symbols.
const classifier = "ResolveApplyError"

// applySeamMethods are the method names that carry the apply loop.
var applySeamMethods = map[string]bool{
	"Update":        true,
	"UpdateEntries": true,
}

// exemptSeams are the apply seams that legitimately do not classify, each with
// the reason. Every entry is a DELEGATOR: it forwards the entries to another
// seam in this list's scope and returns whatever that seam returned, so the
// classification happens once, at the bottom, rather than twice.
//
// Adding an entry here is a design decision, not a formality. If a new seam
// applies entries itself, it classifies; "it is only used in tests" and "it
// cannot fail" are not reasons, because the second one has been wrong twice in
// this repository (go-qrdb#129, go-qrdb#520).
var exemptSeams = map[string]string{
	"pkg/statemachine.KVStateMachine.Update": "delegates to KVStateMachine.applyEntries, which classifies " +
		"(go-qrdb#584 B1: DiskAdapter.Update calls applyEntries directly too, under its own per-entry batch " +
		"lock, instead of calling this Update once per entry)",
	"pkg/engine.MVCCEngine.Update":            "delegates to MVCCEngine.UpdateEntries, which classifies",
	"pkg/store.MVCCStateMachine.Update":       "delegates to MVCCEngine.UpdateEntries, which classifies",
	"pkg/statemachine.HighPerfAdapter.Update": "delegates to the embedded FastStateMachine, which classifies",
	"pkg/statemachine.StandardAdapter.Update": "delegates to the embedded KVStateMachine, which classifies",
	"pkg/statemachine.ConcurrentAdapter.Update": "delegates to the embedded ConcurrentStateMachine, which " +
		"classifies",
	"pkg/statemachine.DiskAdapter.Update": "delegates to the embedded state machine, which classifies",
	"pkg/statemachine.unopenedReplica.Update": "applies nothing: it stands in for a replica whose storage " +
		"failed to open, its Open returns that failure so the shard never starts, and Update only " +
		"repeats the same open error",
}

// minApplySeams is the discovery floor. Every other assertion in this gate is
// inside a loop, and an empty loop reports success — the same reasoning that
// put floors in action_pins_test.go. Measured at 15 on the tree that fixed
// go-qrdb#520; a legitimate deletion lowers this in the same commit.
const minApplySeams = 15

// applySeam is one Update implementation found in the tree.
type applySeam struct {
	id         string // "pkg/engine.MVCCEngine.UpdateEntries"
	file       string
	line       int
	classifies bool
}

// TestEveryApplySeamClassifiesItsHandlerErrors is the gate.
func TestEveryApplySeamClassifiesItsHandlerErrors(t *testing.T) {
	seams := collectApplySeams(t)

	if len(seams) < minApplySeams {
		t.Fatalf("found %d apply seams, floor is %d; either the scan is broken or "+
			"seams were deleted — if deleted, lower minApplySeams in the same commit",
			len(seams), minApplySeams)
	}

	for _, seam := range seams {
		reason, exempt := exemptSeams[seam.id]
		switch {
		case seam.classifies && exempt:
			t.Errorf("%s classifies AND is listed exempt (%q); remove the exemption — "+
				"a stale exemption is how the next seam gets one for free",
				seam.id, reason)
		case !seam.classifies && !exempt:
			t.Errorf("%s:%d: %s applies committed entries without calling %s. "+
				"A handler error returned straight to Raft halts the state machine, "+
				"and every later linearizable read on that shard burns the caller's "+
				"deadline (go-qrdb#520). Route the error through "+
				"statemachine.%s, or add this seam to exemptSeams with the reason "+
				"it delegates.",
				seam.file, seam.line, seam.id, classifier, classifier)
		}
	}
}

// TestEveryExemptSeamStillExists keeps the exemption list from outliving the
// code it describes. An exemption naming a deleted type is an exemption
// waiting to be read as precedent by whoever adds a type of that name next.
func TestEveryExemptSeamStillExists(t *testing.T) {
	found := make(map[string]bool)
	for _, seam := range collectApplySeams(t) {
		found[seam.id] = true
	}
	ids := make([]string, 0, len(exemptSeams))
	for id := range exemptSeams {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !found[id] {
			t.Errorf("exemptSeams names %q, which no longer exists; delete the entry", id)
		}
	}
}

// TestTheApplySeamFloorRefusesAVacuousScan asserts the floor FIRES, rather
// than merely existing. A floor nobody has watched go red is a floor nobody
// has verified (action_pins_test.go makes the same argument).
func TestTheApplySeamFloorRefusesAVacuousScan(t *testing.T) {
	for _, tc := range []struct {
		name     string
		found    int
		wantFail bool
	}{
		{"nothing found", 0, true},
		{"one seam short", minApplySeams - 1, true},
		{"the measured tree", minApplySeams, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.found < minApplySeams; got != tc.wantFail {
				t.Errorf("floor fired = %v, want %v for %d seams", got, tc.wantFail, tc.found)
			}
		})
	}
}

// collectApplySeams parses every non-test Go file under pkg/ — ignoring build
// constraints, deliberately — and returns each method that applies committed
// entries.
func collectApplySeams(t *testing.T) []applySeam {
	t.Helper()
	root := repoRoot(t)
	pkgDir := filepath.Join(root, "pkg")

	var seams []applySeam
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
		file, parseErr := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || fn.Body == nil || !applySeamMethods[fn.Name.Name] {
				continue
			}
			if !takesCommittedEntries(fn) {
				continue
			}
			seams = append(seams, applySeam{
				id:         filepath.ToSlash(filepath.Dir(rel)) + "." + receiverTypeName(fn) + "." + fn.Name.Name,
				file:       filepath.ToSlash(rel),
				line:       fset.Position(fn.Pos()).Line,
				classifies: bodyCalls(fn.Body, classifier),
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", pkgDir, err)
	}
	sort.Slice(seams, func(i, j int) bool { return seams[i].id < seams[j].id })
	return seams
}

// takesCommittedEntries reports whether fn takes a []sm.Entry parameter, which
// is what makes a method an apply seam rather than an ordinary Update.
func takesCommittedEntries(fn *ast.FuncDecl) bool {
	for _, param := range fn.Type.Params.List {
		if exprString(param.Type) == applyEntrySignature {
			return true
		}
	}
	return false
}

// receiverTypeName returns the receiver's type name without its pointer star.
func receiverTypeName(fn *ast.FuncDecl) string {
	expr := fn.Recv.List[0].Type
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return "?"
}

// bodyCalls reports whether body contains a call whose function name is name,
// qualified or not. Matching the NAME rather than resolving the symbol is
// deliberate: this gate parses files individually, without type information,
// so that files behind a build tag are scanned too. The cost is that a local
// identifier of the same name would satisfy it, which is a trade this gate
// takes knowingly — the alternative is not scanning the tag-gated seams, and
// those are exactly the ones that get forgotten.
func bodyCalls(body *ast.BlockStmt, name string) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			if fn.Name == name {
				found = true
			}
		case *ast.SelectorExpr:
			if fn.Sel.Name == name {
				found = true
			}
		}
		return !found
	})
	return found
}

// exprString renders a type expression back to the source form it would be
// written in, so it can be compared against applyEntrySignature.
//
// Hand-rolled rather than go/printer because the shapes involved are two --
// a selector and a slice of one -- and go/printer is behind this repository's
// forbidigo `^print.*$` rule. A type this does not recognise renders as the
// empty string and therefore does not match, which makes an unrecognised shape
// a MISSED seam rather than a false one; the discovery floor is what keeps
// that from passing silently.
func exprString(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.ArrayType:
		if t.Len != nil {
			return ""
		}
		return "[]" + exprString(t.Elt)
	default:
		return ""
	}
}
