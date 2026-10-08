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
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/analysis"
)

// CopyOutAnalyzer is securecopy, the LIFETIME half of the secure rules
// (go-qrdb#459).
//
// securecheck's rules are about declarations: is a long-lived holder of key
// material the wipeable type? None of them asks what happens to the bytes
// once they leave protected memory, and that is where #429 added an
// unrecoverable plaintext copy while every rule stayed green:
//
//	config.Barrier.Passphrase.WithBytes(func(b []byte) error {
//		softwareSecret = append([]byte(nil), b...)   // a COPY nobody wipes
//		return nil
//	})
//
// securecopy follows every such copy to the end of its owner's
// responsibility.
//
// # Sources: a copy of protected bytes into ordinary memory
//
// A VIEW is protected memory seen as a []byte: the parameter of a function
// literal passed to a WithBytes(func([]byte) error) error method (any type —
// secure.Buffer, secret.SecretValue, a go-enclave enclave, a Borrower), or a
// re-slice of one. A COPY-OUT is:
//
//   - append(x, view...)
//   - copy(dst, view) where dst is not itself a view
//   - bytes.Clone(view) or slices.Clone(view)
//   - string(view) — which is reported unconditionally, because a string can
//     never be wiped
//   - a call to a function known to RETURN a copy-out (below)
//
// # Sinks: where the obligation ends
//
// The copy is held by a local variable, and the obligation is discharged in
// the function that declares it when that variable is:
//
//   - passed to a CONSUMER — a function that wipes or takes ownership of its
//     []byte argument. The roots are go-enclave's Wipe and NewSealed and the
//     builtin clear. Every function in this repository that passes a
//     parameter to a consumer is inferred to be one (secure.Wipe,
//     secure.NewBufferFromBytes, the sdk/go re-exports, ...). A function whose
//     contract consumes its argument through a path inference cannot see —
//     an interface method, a function variable — declares it with a
//     //secure:consumes line in its doc comment.
//   - returned. The function is then inferred to RETURN a copy-out, and the
//     obligation moves to every caller, across packages, as an analysis fact.
//
// Anything else is reported at the copy: stored in a field or a global, sent
// on a channel, passed to an ordinary function, or simply dropped. A copy
// that genuinely has to outlive every wipe carries //secure:unwiped <reason>
// trailing it, or standing alone on the line above it, which is per site and
// visible in review. A directive trailing a different statement on the line
// above covers nothing.
//
// # What this cannot see, stated rather than implied away
//
//   - Path sensitivity: a wipe anywhere after the copy in the declaring
//     function discharges it, even one that only some paths reach.
//     `defer secure.Wipe(x)` immediately after the copy is the shape that
//     makes this exact. Written BEFORE the copy, that same defer wipes the
//     value x held when the defer ran, and is not counted.
//   - Aliases: the analysis follows the variable, not its backing array. A
//     wipe through a re-slice (secure.Wipe(x[:n])) counts; a wipe through a
//     second variable (y := x; secure.Wipe(y)) does not, and is reported — a
//     false positive in the safe direction. An append that reallocates x
//     after the copy leaves the old array unwiped and is NOT seen.
//   - Callees that receive a view: passing a view itself to a function is
//     not a copy-out, and what the callee does with it — SealRootKey copies,
//     SealAndDistribute consumes — is not followed. Inside the callee the
//     parameter is an ordinary []byte, not a view.
//   - Interface dispatch: facts attach to concrete functions, so a copy
//     returned through an interface method is not traced to its callers
//     unless the interface method declares //secure:consumes on the way in.
//   - Callbacks passed by variable to WithBytes (not as a literal) are not
//     analysed as views.
//   - Copies the compiler or runtime makes (stack growth, spills, GC moves)
//     and plaintext that never lived in protected memory at all.
//
// Those remain a review obligation, recorded in docs/barrier-seal.
var CopyOutAnalyzer = &analysis.Analyzer{
	Name:      "securecopy",
	Doc:       "reports plaintext copied out of protected memory that is never wiped, returned or consumed",
	Run:       runCopyOut,
	FactTypes: []analysis.Fact{new(consumesFact), new(returnsCopyFact)},
}

// consumesFact marks a function that wipes or takes ownership of the []byte
// arguments at the listed parameter indices.
type consumesFact struct{ Params []int }

// AFact implements analysis.Fact.
func (*consumesFact) AFact() {}

// String implements fmt.Stringer for analysistest's fact expectations.
func (f *consumesFact) String() string { return "consumes" + intList(f.Params) }

// returnsCopyFact marks a function whose listed results carry a copy of
// protected bytes that the CALLER now owns.
type returnsCopyFact struct{ Results []int }

// AFact implements analysis.Fact.
func (*returnsCopyFact) AFact() {}

// String implements fmt.Stringer.
func (f *returnsCopyFact) String() string { return "returnsCopy" + intList(f.Results) }

func intList(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return "(" + strings.Join(parts, ",") + ")"
}

// consumesDirective and unwipedDirective are the two per-site declarations
// securecopy reads.
const (
	consumesDirective = "//secure:consumes"
	unwipedDirective  = "//secure:unwiped"
)

// rootConsumers are the consumers inference starts from. They sit outside
// this repository — go-enclave is the protected-memory library — so no
// directive can be written on them; each is documented there as wiping (Wipe)
// or taking ownership of and wiping (NewSealed) its argument.
var rootConsumers = map[string]struct{}{
	"github.com/jeremyhahn/go-enclave.Wipe":      {},
	"github.com/jeremyhahn/go-enclave.NewSealed": {},
}

// cloneFuncs are the standard-library functions that return a fresh copy of
// their []byte argument.
var cloneFuncs = map[string]struct{}{
	"bytes.Clone":  {},
	"slices.Clone": {},
}

// copyState is one package's analysis: the facts it has inferred so far and
// the findings of the final pass.
type copyState struct {
	pass     *analysis.Pass
	consumes map[types.Object][]int
	returns  map[types.Object][]int
	report   bool
}

func runCopyOut(pass *analysis.Pass) (any, error) {
	st := &copyState{
		pass:     pass,
		consumes: make(map[types.Object][]int),
		returns:  make(map[types.Object][]int),
	}
	decls := st.funcDecls()
	st.declaredConsumers()

	// Inference runs to a fixed point: a function becomes a consumer or a
	// copy-returner because of a callee in the same package, which may be
	// declared after it. Each round only ever ADDS facts, so it terminates.
	for changed := true; changed; {
		changed = st.funcVarAliases()
		for _, d := range decls {
			if st.inferConsumes(d.fd) {
				changed = true
			}
			if st.analyse(d) {
				changed = true
			}
		}
	}

	st.report = true
	for _, d := range decls {
		if !testFileScope(pass.Fset.Position(d.file.Pos()).Filename) {
			st.analyse(d)
		}
	}

	for fn, params := range st.consumes {
		pass.ExportObjectFact(fn, &consumesFact{Params: params})
	}
	for fn, results := range st.returns {
		pass.ExportObjectFact(fn, &returnsCopyFact{Results: results})
	}
	return nil, nil
}

// fileDecl is a function declaration with a body, and the file it is in.
type fileDecl struct {
	file *ast.File
	fd   *ast.FuncDecl
	// unwiped holds the //secure:unwiped comments of file, by line.
	unwiped map[int]lineDirective
}

// funcDecls returns every function declaration with a body.
func (st *copyState) funcDecls() []fileDecl {
	var out []fileDecl
	for _, file := range st.pass.Files {
		unwiped := directiveComments(st.pass, file, unwipedDirective)
		for _, d := range file.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil {
				out = append(out, fileDecl{file: file, fd: fd, unwiped: unwiped})
			}
		}
	}
	return out
}

// declaredConsumers records every function and interface method whose doc
// comment carries //secure:consumes. It consumes every []byte parameter.
func (st *copyState) declaredConsumers() {
	for _, file := range st.pass.Files {
		ast.Inspect(file, func(n ast.Node) bool {
			switch d := n.(type) {
			case *ast.FuncDecl:
				if hasDirective(d.Doc, consumesDirective) {
					if fn, ok := st.pass.TypesInfo.Defs[d.Name].(*types.Func); ok {
						st.addConsumes(fn, byteSliceParams(fn.Signature())...)
					}
				}
			case *ast.InterfaceType:
				for _, m := range d.Methods.List {
					if !hasDirective(m.Doc, consumesDirective) {
						continue
					}
					for _, name := range m.Names {
						if fn, ok := st.pass.TypesInfo.Defs[name].(*types.Func); ok {
							st.addConsumes(fn, byteSliceParams(fn.Signature())...)
						}
					}
				}
			}
			return true
		})
	}
}

// inferConsumes marks fd a consumer of each parameter it passes, unchanged,
// to a consumer. Reports whether anything new was learned.
func (st *copyState) inferConsumes(fd *ast.FuncDecl) bool {
	fn, ok := st.pass.TypesInfo.Defs[fd.Name].(*types.Func)
	if !ok {
		return false
	}
	params := make(map[*types.Var]int)
	sig := fn.Signature()
	for i := range sig.Params().Len() {
		params[sig.Params().At(i)] = i
	}
	var found []int
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		for _, argIdx := range st.consumedArgs(call) {
			if v := rootVar(st.pass.TypesInfo, call.Args[argIdx]); v != nil {
				if i, isParam := params[v]; isParam {
					found = append(found, i)
				}
			}
		}
		return true
	})
	return st.addConsumes(fn, found...)
}

// consumedArgs returns the indices of call's arguments its callee consumes.
func (st *copyState) consumedArgs(call *ast.CallExpr) []int {
	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if b, isBuiltin := st.pass.TypesInfo.Uses[id].(*types.Builtin); isBuiltin && b.Name() == "clear" {
			return []int{0}
		}
	}
	return st.consumesOf(callee(st.pass.TypesInfo, call.Fun))
}

// consumesOf returns the parameter indices obj consumes: a root consumer, one
// inferred in this package, or one imported as a fact.
func (st *copyState) consumesOf(obj types.Object) []int {
	if obj == nil {
		return nil
	}
	if obj.Pkg() != nil {
		if _, root := rootConsumers[obj.Pkg().Path()+"."+obj.Name()]; root {
			if sig, ok := obj.Type().(*types.Signature); ok {
				return byteSliceParams(sig)
			}
		}
	}
	if params, ok := st.consumes[obj]; ok {
		return params
	}
	var fact consumesFact
	if st.pass.ImportObjectFact(obj, &fact) {
		return fact.Params
	}
	return nil
}

// returnedCopies returns the result indices of call's callee that carry a
// copy the caller owns.
func (st *copyState) returnedCopies(call *ast.CallExpr) []int {
	return st.returnsOf(callee(st.pass.TypesInfo, call.Fun))
}

// returnsOf returns the result indices of obj that carry a caller-owned copy.
func (st *copyState) returnsOf(obj types.Object) []int {
	if obj == nil {
		return nil
	}
	if results, ok := st.returns[obj]; ok {
		return results
	}
	var fact returnsCopyFact
	if st.pass.ImportObjectFact(obj, &fact) {
		return fact.Results
	}
	return nil
}

// funcVarAliases carries facts onto a package-level function variable
// initialised from a function. pkg/seal's `var NewCredentials =
// quicraftseal.NewCredentials` is how this repository re-exports, and a call
// through it must mean what a call to its target means. The initialiser is
// what the analysis follows: a test that swaps the variable does not change
// the production contract.
func (st *copyState) funcVarAliases() bool {
	grew := false
	for _, file := range st.pass.Files {
		for _, d := range file.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Values) != len(vs.Names) {
					continue
				}
				for i, name := range vs.Names {
					v, ok := st.pass.TypesInfo.Defs[name].(*types.Var)
					if !ok {
						continue
					}
					target := callee(st.pass.TypesInfo, vs.Values[i])
					if st.addConsumes(v, st.consumesOf(target)...) {
						grew = true
					}
					if st.addReturns(v, st.returnsOf(target)...) {
						grew = true
					}
				}
			}
		}
	}
	return grew
}

func (st *copyState) addConsumes(obj types.Object, params ...int) bool {
	return addIndices(st.consumes, obj, params)
}

func (st *copyState) addReturns(obj types.Object, results ...int) bool {
	return addIndices(st.returns, obj, results)
}

// addIndices merges idx into m[fn], keeping the list sorted and unique, and
// reports whether it grew.
func addIndices(m map[types.Object][]int, fn types.Object, idx []int) bool {
	grew := false
	for _, i := range idx {
		present := false
		for _, have := range m[fn] {
			if have == i {
				present = true
				break
			}
		}
		if !present {
			m[fn] = append(m[fn], i)
			grew = true
		}
	}
	if grew {
		sort.Ints(m[fn])
	}
	return grew
}

// copySite is one copy-out found in a function body.
type copySite struct {
	pos    token.Pos
	holder *types.Var // nil when the copy is not held by a local variable
	// returnIdx is the result index when the copy is returned directly
	// (return append(...)); -1 otherwise.
	returnIdx int
	// consumed is set when the copy is passed straight to a consumer.
	consumed bool
	// unwipeable is set for a string conversion.
	unwipeable bool
	what       string
}

// analyse finds every copy-out in fd and checks each is discharged. On an
// inference round it only records new returnsCopy facts and reports whether
// any were added; on the reporting round it reports.
func (st *copyState) analyse(d fileDecl) bool {
	fd := d.fd
	fn, ok := st.pass.TypesInfo.Defs[fd.Name].(*types.Func)
	if !ok {
		return false
	}
	views := st.views(fd.Body)
	sites := st.copySites(fd, views)
	grew := false
	for _, s := range sites {
		excused := unwipedExcused(d.unwiped, st.pass.Fset.Position(s.pos).Line)
		if s.unwipeable {
			if !excused {
				st.reportf(s.pos, "%s converts protected bytes to a string, which can never be wiped; "+
					"keep them as []byte and wipe them, or justify it with %s <reason>", s.what, unwipedDirective)
			}
			continue
		}
		if s.consumed {
			continue
		}
		if s.returnIdx >= 0 {
			if st.addReturns(fn, s.returnIdx) {
				grew = true
			}
			continue
		}
		if s.holder != nil {
			wiped, returned := st.discharge(fd, s.holder, s.pos)
			if wiped {
				continue
			}
			if len(returned) > 0 {
				if st.addReturns(fn, returned...) {
					grew = true
				}
				continue
			}
		}
		if excused {
			continue
		}
		st.reportf(s.pos, "%s copies protected bytes into ordinary memory that is never wiped, returned or "+
			"consumed; defer secure.Wipe on the copy, or justify it with %s <reason>", s.what, unwipedDirective)
	}
	return grew
}

func (st *copyState) reportf(pos token.Pos, format string, args ...any) {
	if st.report {
		st.pass.Reportf(pos, format, args...)
	}
}

// views returns the variables in body that hold a view of protected memory:
// the []byte parameter of a literal callback passed to WithBytes.
func (st *copyState) views(body *ast.BlockStmt) map[*types.Var]struct{} {
	out := make(map[*types.Var]struct{})
	info := st.pass.TypesInfo
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if lit := withBytesCallback(info, call); lit != nil {
			for _, field := range lit.Type.Params.List {
				for _, name := range field.Names {
					if v, ok := info.Defs[name].(*types.Var); ok {
						out[v] = struct{}{}
					}
				}
			}
		}
		return true
	})
	return out
}

// withBytesCallback returns the literal callback when call is a
// WithBytes(func([]byte) error) error call whose argument is a function
// literal.
func withBytesCallback(info *types.Info, call *ast.CallExpr) *ast.FuncLit {
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "WithBytes" || len(call.Args) != 1 {
		return nil
	}
	lit, ok := ast.Unparen(call.Args[0]).(*ast.FuncLit)
	if !ok {
		return nil
	}
	sig, ok := info.TypeOf(lit).(*types.Signature)
	if !ok || sig.Params().Len() != 1 || !isByteSlice(sig.Params().At(0).Type()) {
		return nil
	}
	return lit
}

// isView reports whether expr is a view or a re-slice of one.
func isView(info *types.Info, views map[*types.Var]struct{}, expr ast.Expr) bool {
	if v := rootVar(info, expr); v != nil {
		_, ok := views[v]
		return ok
	}
	return false
}

// copySites finds every copy-out in fd's body and classifies where it goes.
func (st *copyState) copySites(fd *ast.FuncDecl, views map[*types.Var]struct{}) []copySite {
	var sites []copySite
	var stack []ast.Node
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		parent := ast.Node(nil)
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)

		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		site, isCopy := st.classifyCopy(call, views)
		if !isCopy {
			return true
		}
		switch {
		case site.unwipeable, site.holder != nil:
		default:
			st.placeCopy(fd, &site, call, parent)
		}
		sites = append(sites, site)
		return true
	})
	return sites
}

// classifyCopy reports whether call makes a copy-out, and fills in what it
// can from the call alone.
func (st *copyState) classifyCopy(call *ast.CallExpr, views map[*types.Var]struct{}) (copySite, bool) {
	info := st.pass.TypesInfo
	site := copySite{pos: call.Pos(), returnIdx: -1}

	// string(view)
	if tv, ok := info.Types[call.Fun]; ok && tv.IsType() {
		if basic, isBasic := tv.Type.Underlying().(*types.Basic); isBasic && basic.Info()&types.IsString != 0 &&
			len(call.Args) == 1 && isView(info, views, call.Args[0]) {
			site.unwipeable = true
			site.what = "string(...)"
			return site, true
		}
		return site, false
	}

	if id, ok := ast.Unparen(call.Fun).(*ast.Ident); ok {
		if b, isBuiltin := info.Uses[id].(*types.Builtin); isBuiltin {
			switch b.Name() {
			case "append":
				if call.Ellipsis.IsValid() && len(call.Args) == 2 && isView(info, views, call.Args[1]) {
					site.what = "append(..., view...)"
					return site, true
				}
			case "copy":
				if len(call.Args) == 2 && isView(info, views, call.Args[1]) && !isView(info, views, call.Args[0]) {
					site.what = "copy(dst, view)"
					site.holder = rootVar(info, call.Args[0])
					return site, true
				}
			}
			return site, false
		}
	}

	fn := callee(info, call.Fun)
	if fn == nil {
		return site, false
	}
	if fn.Pkg() != nil {
		if _, clone := cloneFuncs[fn.Pkg().Path()+"."+fn.Name()]; clone && len(call.Args) == 1 && isView(info, views, call.Args[0]) {
			site.what = fn.Pkg().Path() + "." + fn.Name() + "(view)"
			return site, true
		}
	}
	if results := st.returnedCopies(call); len(results) > 0 {
		site.what = fn.Name() + "(...)"
		return site, true
	}
	return site, false
}

// placeCopy decides where a copy made by call goes, from its parent node:
// into a local variable, straight back to fd's caller, or straight into a
// consumer. Anything else leaves the site with no holder, which is reported.
func (st *copyState) placeCopy(fd *ast.FuncDecl, site *copySite, call *ast.CallExpr, parent ast.Node) {
	info := st.pass.TypesInfo
	resultIdx := 0
	if results := st.returnedCopies(call); len(results) > 0 {
		resultIdx = results[0]
	}
	switch p := parent.(type) {
	case *ast.AssignStmt:
		if len(p.Rhs) == 1 && len(p.Lhs) > resultIdx {
			site.holder = rootVar(info, p.Lhs[resultIdx])
			return
		}
		// Several values: the left side lines up one to one.
		for i, rhs := range p.Rhs {
			if rhs == call {
				site.holder = rootVar(info, p.Lhs[i])
			}
		}
	case *ast.ValueSpec:
		if len(p.Values) == 1 && len(p.Names) > resultIdx {
			site.holder, _ = info.Defs[p.Names[resultIdx]].(*types.Var)
			return
		}
		for i, v := range p.Values {
			if v == call {
				site.holder, _ = info.Defs[p.Names[i]].(*types.Var)
			}
		}
	case *ast.ReturnStmt:
		if !returnsFrom(fd, p) {
			return
		}
		if len(p.Results) == 1 {
			site.returnIdx = resultIdx
			return
		}
		for i, r := range p.Results {
			if r == call {
				site.returnIdx = i
			}
		}
	case *ast.CallExpr:
		for _, idx := range st.consumedArgs(p) {
			if idx < len(p.Args) && p.Args[idx] == call {
				site.consumed = true
			}
		}
	}
}

// discharge looks for the end of holder's obligation in fd: a consumer call
// on it, or a return of it from fd itself. It returns whether it was consumed
// and, if not, the result indices it is returned through.
//
// Only a consumer call or return positioned AFTER the copy counts, with one
// exception. `defer secure.Wipe(x)` evaluates x when the defer statement
// runs, so written before the copy it wipes the slice x held THEN, typically
// nil, and the copy assigned to x afterwards is never touched. A deferred
// closure, `defer func() { secure.Wipe(x) }()`, reads x at exit and counts
// wherever it is written.
func (st *copyState) discharge(fd *ast.FuncDecl, holder *types.Var, site token.Pos) (bool, []int) {
	info := st.pass.TypesInfo
	deferred := deferredClosures(fd.Body)
	consumed := false
	var returned []int
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			if x.Pos() < site && !within(deferred, x.Pos()) {
				return true
			}
			for _, idx := range st.consumedArgs(x) {
				if idx < len(x.Args) && rootVar(info, x.Args[idx]) == holder {
					consumed = true
				}
			}
		case *ast.ReturnStmt:
			if x.Pos() < site || !returnsFrom(fd, x) {
				return true
			}
			if len(x.Results) == 0 {
				// A bare return of a named result.
				results := fd.Type.Results
				if results == nil {
					return true
				}
				i := 0
				for _, field := range results.List {
					for _, name := range field.Names {
						if info.Defs[name] == holder {
							returned = append(returned, i)
						}
						i++
					}
				}
				return true
			}
			for i, r := range x.Results {
				if rootVar(info, r) == holder {
					returned = append(returned, i)
				}
			}
		}
		return true
	})
	return consumed, returned
}

// deferredClosures returns the function literals body defers directly —
// `defer func() { ... }()` — which run at exit whatever their position.
func deferredClosures(body *ast.BlockStmt) []*ast.FuncLit {
	var out []*ast.FuncLit
	ast.Inspect(body, func(n ast.Node) bool {
		if d, ok := n.(*ast.DeferStmt); ok {
			if lit, isLit := ast.Unparen(d.Call.Fun).(*ast.FuncLit); isLit {
				out = append(out, lit)
			}
		}
		return true
	})
	return out
}

// within reports whether pos lies inside any of lits.
func within(lits []*ast.FuncLit, pos token.Pos) bool {
	for _, lit := range lits {
		if lit.Pos() <= pos && pos < lit.End() {
			return true
		}
	}
	return false
}

// returnsFrom reports whether ret returns from fd itself rather than from a
// function literal nested inside it.
func returnsFrom(fd *ast.FuncDecl, ret *ast.ReturnStmt) bool {
	inside := true
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.FuncLit)
		if !ok {
			return true
		}
		if lit.Pos() <= ret.Pos() && ret.End() <= lit.End() {
			inside = false
		}
		return true
	})
	return inside
}

// rootVar returns the variable expr names, looking through parentheses and
// re-slicing, or nil when expr is not a plain variable (a field, an index, a
// call, ...).
func rootVar(info *types.Info, expr ast.Expr) *types.Var {
	for {
		switch x := ast.Unparen(expr).(type) {
		case *ast.SliceExpr:
			expr = x.X
			continue
		case *ast.Ident:
			if v, ok := info.Uses[x].(*types.Var); ok && !v.IsField() {
				return v
			}
			if v, ok := info.Defs[x].(*types.Var); ok && !v.IsField() {
				return v
			}
		}
		return nil
	}
}

// callee returns the statically known function expr names — a function, a
// method, or a package-level function variable — or nil.
func callee(info *types.Info, expr ast.Expr) types.Object {
	var id *ast.Ident
	switch f := ast.Unparen(expr).(type) {
	case *ast.Ident:
		id = f
	case *ast.SelectorExpr:
		id = f.Sel
	default:
		return nil
	}
	switch obj := info.Uses[id].(type) {
	case *types.Func:
		return obj
	case *types.Var:
		if _, isFunc := obj.Type().Underlying().(*types.Signature); isFunc && obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope() {
			return obj
		}
	}
	return nil
}

// byteSliceParams returns the indices of sig's []byte parameters.
func byteSliceParams(sig *types.Signature) []int {
	var out []int
	params := sig.Params()
	for i := range params.Len() {
		if isByteSlice(params.At(i).Type()) {
			out = append(out, i)
		}
	}
	return out
}

// isByteSlice reports whether t is []byte.
func isByteSlice(t types.Type) bool {
	s, ok := t.Underlying().(*types.Slice)
	if !ok {
		return false
	}
	b, ok := s.Elem().Underlying().(*types.Basic)
	return ok && b.Kind() == types.Byte
}

// hasDirective reports whether a doc comment carries directive on a line of
// its own.
func hasDirective(doc *ast.CommentGroup, directive string) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		if c.Text == directive || strings.HasPrefix(c.Text, directive+" ") {
			return true
		}
	}
	return false
}

// unwipedExcused reports whether a copy on line is justified by a
// well-formed //secure:unwiped directive — the prefix, a space and a
// non-empty reason — trailing it, or standing alone on the line above it.
func unwipedExcused(m map[int]lineDirective, line int) bool {
	d, ok := directiveAt(m, line)
	if !ok {
		return false
	}
	rest, hasReason := strings.CutPrefix(d.text, unwipedDirective+" ")
	return hasReason && strings.TrimSpace(rest) != ""
}
