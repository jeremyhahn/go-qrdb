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

package analyzer

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// Rule 4 — an UNBORROWED read of protected memory must say why (go-qrdb#428).
//
// secure.Buffer.Bytes returns a slice into the buffer's locked pages with no
// lock held once it returns, so a concurrent Destroy — a shutdown
// enclave.WipeAll above all — can unmap the pages while the caller still reads
// them, and the read faults the process. Buffer.WithBytes, and secure.CopyOut
// built on it, hold the borrow across the whole read and are the only shape a
// lock can cover. go-qrdb#74 and #428 converted every reader that could hold
// one.
//
// What remains cannot, and Buffer.Bytes' own documentation names the three
// reasons a read may still be unborrowed:
//
//   - injected-interface: the bytes cross into an interface a consumer
//     implements (seal.EncryptionBarrier, a SealingStrategy, a share
//     distributor). A borrow held across it would put an arbitrary
//     implementation inside a WithBytes callback, which pkg/secure's rule 6
//     forbids and its borrow guard reports;
//   - allocates-protected: the read spans a call in this repository that
//     allocates protected memory of its own (a strategy constructor copying
//     the key into its own enclave, for example) — rule 6 again, with a callee
//     that can be named;
//   - nested-enclave: the read happens inside a go-quicraft enclave borrow,
//     whose own contract forbids nesting a second protected-memory borrow.
//
// That documentation used to be the whole enforcement: "each surviving one
// carries a comment saying which kind it is; a new one that does not is a
// defect". A free-text comment is not checkable, so the convention is now a
// directive the analyzer reads, trailing the read on its own line or standing
// alone on the line directly above it:
//
//	//secure:unborrowed injected-interface m.barrier is a seal.EncryptionBarrier (go-qrdb#145)
//
// A directive trailing a DIFFERENT statement on the line above belongs to that
// statement and covers nothing. The kind must be one of the three above and a
// reason must follow it. The directive is per site and visible in review — it
// is not an allowlist (go-qrdb#429 argued against one): nothing outside the
// line itself can silence a read, and adding one means writing, at the site,
// which named hazard justifies it.
//
// Any reference to the method counts, not only a call: a method value
// (f := buf.Bytes) is the same unborrowed read, deferred.
//
// Test files are out of scope, for the reason rule 3's S4 gives
// (scope.go): a test asserting a buffer's contents reads it on the goroutine
// that owns it, after the code under test has returned, with nothing able to
// destroy it concurrently. The hazard is a production process's shutdown sweep
// landing mid-read, and a test binary has no such sweep running.

// securePkgPath is the package whose Buffer.Bytes rule 4 governs. The SDK's
// sdk/go/secure.Buffer is an alias of this type, so it is covered too.
const securePkgPath = "github.com/jeremyhahn/go-qrdb/pkg/secure"

// unborrowedDirective is the directive prefix rule 4 accepts.
const unborrowedDirective = "//secure:unborrowed"

// unborrowedKinds is the closed set of reasons a read may be unborrowed. Each
// is documented on secure.Buffer.Bytes; a new kind is a change to that
// documentation first.
var unborrowedKinds = map[string]struct{}{
	"injected-interface":  {},
	"allocates-protected": {},
	"nested-enclave":      {},
}

// unborrowedKindList names the kinds in diagnostics.
const unborrowedKindList = "injected-interface|allocates-protected|nested-enclave"

// directiveState classifies the directive found on one line.
type directiveState int

const (
	directiveValid directiveState = iota + 1
	directiveMalformed
)

// checkUnborrowedBytes reports every reference to secure.Buffer.Bytes in a
// non-test file that is not justified by a well-formed //secure:unborrowed
// directive on its line or the line above.
func checkUnborrowedBytes(pass *analysis.Pass) {
	for _, file := range pass.Files {
		if testFileScope(pass.Fset.Position(file.Pos()).Filename) {
			continue
		}
		directives := directiveComments(pass, file, unborrowedDirective)
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || !isSecureBufferBytes(pass.TypesInfo, sel) {
				return true
			}
			d, found := directiveAt(directives, pass.Fset.Position(sel.Sel.Pos()).Line)
			switch {
			case !found:
				pass.Reportf(sel.Sel.Pos(),
					"secure.Buffer.Bytes holds no borrow across the read; use WithBytes or secure.CopyOut, "+
						"or justify it with %s <%s> <reason> on this line or on a line of its own directly above",
					unborrowedDirective, unborrowedKindList)
			case parseUnborrowedDirective(d.text) == directiveMalformed:
				pass.Reportf(sel.Sel.Pos(),
					"malformed %s directive: it must name a kind (%s) and then a reason",
					unborrowedDirective, unborrowedKindList)
			}
			return true
		})
	}
}

// isSecureBufferBytes reports whether sel refers to the Bytes method of
// pkg/secure's Buffer, whether as a call, a method value or a method
// expression.
func isSecureBufferBytes(info *types.Info, sel *ast.SelectorExpr) bool {
	if sel.Sel.Name != "Bytes" {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types.Func)
	if !ok {
		return false
	}
	recv := fn.Signature().Recv()
	if recv == nil {
		return false
	}
	t := recv.Type()
	if p, isPtr := t.(*types.Pointer); isPtr {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj.Name() == "Buffer" && obj.Pkg() != nil && obj.Pkg().Path() == securePkgPath
}

// lineDirective is one directive comment, and whether it stands on a line of
// its own.
type lineDirective struct {
	text    string
	ownLine bool
}

// directiveComments maps each line of file carrying a comment that starts
// with prefix to that comment.
//
// ownLine is decided from the source text: only whitespace may precede the
// comment on its line. A file the driver cannot read yields ownLine false,
// which fails closed — the directive then covers only its own line.
func directiveComments(pass *analysis.Pass, file *ast.File, prefix string) map[int]lineDirective {
	out := make(map[int]lineDirective)
	var src []byte
	tf := pass.Fset.File(file.Pos())
	if pass.ReadFile != nil && tf != nil {
		src, _ = pass.ReadFile(tf.Name())
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if !strings.HasPrefix(c.Text, prefix) {
				continue
			}
			line := pass.Fset.Position(c.Slash).Line
			out[line] = lineDirective{text: c.Text, ownLine: standsAlone(src, tf, line, c.Slash)}
		}
	}
	return out
}

// standsAlone reports whether only whitespace precedes pos on its line.
func standsAlone(src []byte, tf *token.File, line int, pos token.Pos) bool {
	if src == nil || tf == nil {
		return false
	}
	start, end := tf.Offset(tf.LineStart(line)), tf.Offset(pos)
	if start < 0 || end > len(src) || start > end {
		return false
	}
	return strings.TrimLeft(string(src[start:end]), " \t") == ""
}

// directiveAt returns the directive covering a reference on line: one on that
// same line, or one on the line directly above that stands on a line of its
// own. A comment trailing a different statement on the line above belongs to
// that statement and covers nothing below it.
func directiveAt(m map[int]lineDirective, line int) (lineDirective, bool) {
	if d, ok := m[line]; ok {
		return d, true
	}
	if d, ok := m[line-1]; ok && d.ownLine {
		return d, true
	}
	return lineDirective{}, false
}

// parseUnborrowedDirective classifies one comment that starts with the
// directive prefix. Well formed is the prefix, a space, a known kind, and at
// least one further word of reason.
func parseUnborrowedDirective(text string) directiveState {
	rest, ok := strings.CutPrefix(text, unborrowedDirective+" ")
	if !ok {
		return directiveMalformed
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 {
		return directiveMalformed
	}
	if _, known := unborrowedKinds[fields[0]]; !known {
		return directiveMalformed
	}
	return directiveValid
}
