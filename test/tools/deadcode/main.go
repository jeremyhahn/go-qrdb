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

// Command deadcode reports functions no production path can reach, and error
// types no reachable code can construct (go-qrdb#337).
//
// WHY A SECOND INSTRUMENT. #243 swept for declared error types that no code
// path returns and found 103. That detector measures CONSTRUCTION: a type built
// anywhere outside a test counts as produced. A type constructed only inside a
// function that nothing calls therefore scores "produced" and never enters the
// set. pkg/seal.ShareTimeoutError is the proven instance -- constructed in
// CollectSharesWithTimeout, which has no caller outside tests (#336). So 103 is
// a LOWER BOUND, and every instrument error recorded on #243 has biased the
// finding smaller, five times out of five.
//
// This tool asks the larger question: not "is it constructed" but "can anything
// reach the code that constructs it".
//
// #340 is why the distinction earns a tool rather than a note. Its
// AuthFactorHardwareUnavailableError looked merely unproduced, and the actual
// defect was that no out-of-repo factor COULD produce it -- both transports
// mapped the condition to 503, so a handler arm was unreachable in a deployed
// system. A detector that reads "unproduced" as "delete it" gets that case
// exactly backwards. Reachability is the question that separates the two.
//
// HOW. Rapid Type Analysis over SSA, from roots chosen per module:
//
//	root module   the two binaries' main and init
//	sdk/go        every exported function and method, because the SDK's
//	              callers are downstream repositories this analysis cannot see
//
// RTA rather than CHA: RTA resolves an interface call only to methods of types
// the reachable program actually instantiates, so it is the tighter of the two
// standard answers and reports fewer functions that a human would have to
// dismiss. It is still SOUND for this purpose in the direction that matters --
// it over-approximates reachability, so anything it calls unreachable is
// genuinely unreachable through these roots.
//
// WHAT IT DELIBERATELY DOES NOT CLAIM. "Unreachable from these roots" is not
// "dead". Four honest reasons a listed symbol may have to stay are named in the
// report itself and in baseline.txt's header. This tool produces CANDIDATES; a
// human decides, and the deciding is filed as issues rather than done here
// (#337 scope: "Do not fix findings in the same PR that introduces the
// instrument").
//
// Usage:
//
//	deadcode [-mod dir] [-tags list] [-baseline file] [-write-baseline]
//
// Exit status: 0 when every finding is in the baseline, 1 when a finding is not
// (a NEW unreachable symbol), 2 on an analysis error. A baseline entry that no
// longer appears is reported and is NOT a failure -- deleting dead code is the
// point, and a gate that failed when the tree improved would be reverted within
// a week.
package main

import (
	"flag"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/rta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// finding is one unreachable symbol, in the form the baseline stores.
type finding struct {
	Module string // "." or "sdk/go"
	Scope  string // "internal" or "exported" -- see scopeOf
	Kind   string // "func" or "errortype"
	Symbol string // package-qualified name
	Reason string
}

func (f finding) key() string {
	return f.Module + "\t" + f.Scope + "\t" + f.Kind + "\t" + f.Symbol
}

// scopeOf says whether anything OUTSIDE the declaring package could call this
// symbol, which is what decides whether an unreachable verdict is evidence.
//
//   - internal: some component of the name is unexported, so no other package
//     can name it. Unreachable from the roots plus uncallable from outside
//     means it is reached from tests or from nothing. This tier GATES.
//   - exported: externally callable, so a repository this analysis cannot see
//     may be the caller -- go-xkms and go-quicbaas both import
//     go-qrdb/pkg/... directly. Unreachable here is a QUESTION, not a verdict,
//     so this tier is recorded and reported but does not fail the build.
//
// The distinction is the difference between a gate people keep and one they
// turn off: 2283 of the first run's 2803 findings were exported, and failing on
// those would have made every downstream-facing API addition a build error.
func scopeOf(symbol string) string {
	tail := symbol[strings.LastIndex(symbol, "/")+1:]
	parts := strings.Split(tail, ".")
	for _, p := range parts[1:] { // parts[0] is the package name
		if p == "" || !unicode.IsUpper(rune(p[0])) {
			return "internal"
		}
	}
	return "exported"
}

func main() {
	var (
		modDir   = flag.String("mod", ".", "module directory to analyse")
		modName  = flag.String("name", "", "module label used in the report (defaults to -mod)")
		tags     = flag.String("tags", "", "comma-separated build tags")
		extra    = flag.String("reflect-roots", "", "regexp of qualified symbols invoked by reflection, added as roots")
		baseline = flag.String("baseline", "", "baseline file of accepted findings")
		write    = flag.Bool("write-baseline", false, "rewrite the baseline from this run")
		library  = flag.Bool("library", false, "enter the module through its exported surface even when it contains main packages")
	)
	flag.Parse()

	label := *modName
	if label == "" {
		label = *modDir
	}

	findings, err := analyse(*modDir, label, *tags, *extra, *library)
	if err != nil {
		fmt.Fprintf(os.Stderr, "deadcode: %v\n", err)
		os.Exit(2)
	}

	if *baseline == "" {
		report(findings)
		return
	}
	os.Exit(compare(findings, *baseline, *write, label))
}

// analyse loads one module, builds SSA, runs RTA from that module's roots and
// returns what the roots cannot reach.
func analyse(dir, label, tags, reflectRoots string, library bool) ([]finding, error) {
	var reflectRE *regexp.Regexp
	if reflectRoots != "" {
		var err error
		if reflectRE, err = regexp.Compile(reflectRoots); err != nil {
			return nil, fmt.Errorf("bad -reflect-roots pattern: %w", err)
		}
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedModule,
		Dir: dir,
		// Tests are excluded on purpose: a function reachable only from a test
		// is exactly what this tool is looking for. Including them would make
		// every finding disappear behind its own unit test, which is the
		// failure mode #243's detector already had.
		Tests: false,
	}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags", tags}
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", dir, err)
	}
	// A typecheck error makes the callgraph a fiction -- some packages would be
	// missing and everything they call would look unreachable. Refuse rather
	// than report.
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("%s did not typecheck (%d errors), first: %s",
			dir, len(loadErrs), loadErrs[0])
	}
	// DISCOVERY FLOOR. Zero packages is a broken invocation, not a clean tree.
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s matched no packages -- this analysis would be vacuous", dir)
	}

	prog, ssaPkgs := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	roots, err := rootsFor(ssaPkgs, library)
	if err != nil {
		return nil, err
	}
	roots = append(roots, reflectionRoots(prog, ssaPkgs, reflectRE)...)
	reachable, runtimeTypes, algo := reachableSet(prog, roots)
	fmt.Fprintf(os.Stderr, "deadcode: %s analysed with %s from %d root(s)\n", label, algo, len(roots))

	// RUNTIME TYPES ARE REACHABLE THROUGH THEIR WHOLE METHOD SET. The analysis
	// resolves a call it can see; it cannot see a call made by reflection.
	// Treating every method of a dynamically-accessible type as reachable is the
	// conservative model of "something outside the callgraph may call this", and
	// it is what #337 asks for when it says an interface's method set counts as
	// reachable once the interface is.
	//
	// The cost is under-reporting: a genuinely dead method on a type used
	// dynamically elsewhere will not appear. That is the right direction for a
	// gate -- a false finding costs a person an investigation and their trust in
	// the tool, while a missed one costs a line of code that was already there.
	for _, t := range runtimeTypes {
		for _, fn := range methodsOf(prog, t) {
			if fn != nil {
				reachable[fn] = true
			}
		}
	}

	// Own packages only: a dependency's unused code is not this repository's
	// finding, and reporting it would bury the ones that are.
	own := make(map[*ssa.Package]bool)
	for _, p := range ssaPkgs {
		if p != nil && isOwn(p.Pkg.Path()) && !isTestSupport(p.Pkg.Path()) {
			own[p] = true
		}
	}

	var findings []finding
	unreachableFns := make(map[*ssa.Function]bool)

	for p := range own {
		for _, m := range p.Members {
			fn, ok := m.(*ssa.Function)
			if !ok || skipFunc(fn) {
				continue
			}
			if !reachable[fn] {
				unreachableFns[fn] = true
				sym := qualify(fn)
				findings = append(findings, finding{
					Module: label, Scope: scopeOf(sym), Kind: "func", Symbol: sym,
					Reason: "no path from " + rootDescription(label),
				})
			}
			// Methods live on the type, not in Members.
		}
		for _, t := range p.Members {
			tt, ok := t.(*ssa.Type)
			if !ok {
				continue
			}
			for _, fn := range methodsOf(prog, tt.Type()) {
				if fn == nil || skipFunc(fn) {
					continue
				}
				if !reachable[fn] {
					unreachableFns[fn] = true
					sym := qualify(fn)
					findings = append(findings, finding{
						Module: label, Scope: scopeOf(sym), Kind: "func", Symbol: sym,
						Reason: "no path from " + rootDescription(label),
					})
				}
			}
		}
	}

	findings = append(findings, unconstructableErrors(prog, own, reachable, unreachableFns, label)...)

	sort.Slice(findings, func(i, j int) bool { return findings[i].key() < findings[j].key() })
	return findings, nil
}

// reachableSet computes what the roots can reach, preferring RTA and falling
// back to CHA.
//
// RTA is the better answer: it resolves an interface call only to methods of
// types the reachable program actually instantiates, so it reports fewer
// symbols a human then has to dismiss. But x/tools' RTA panics on some generic
// code -- "ForEachElement called on type containing *types.TypeParam" -- and
// sdk/go trips it. A tool that simply omitted the module it cannot analyse
// would report a clean sdk/go forever, which is worse than a coarser answer.
//
// CHA assumes every method of every type whose signature matches is a possible
// callee, so it over-approximates reachability by more than RTA does. Every
// symbol it still calls unreachable is unreachable under RTA too: the fallback
// loses findings, never invents them. Which algorithm ran is printed, because a
// change in the count between two runs of DIFFERENT algorithms would otherwise
// look like a change in the code.
func reachableSet(prog *ssa.Program, roots []*ssa.Function) (map[*ssa.Function]bool, []types.Type, string) {
	if reachable, rts, ok := tryRTA(roots); ok {
		return reachable, rts, "RTA"
	}

	cg := cha.CallGraph(prog)
	reachable := make(map[*ssa.Function]bool)
	var walk func(fn *ssa.Function)
	walk = func(fn *ssa.Function) {
		if fn == nil || reachable[fn] {
			return
		}
		reachable[fn] = true
		node := cg.Nodes[fn]
		if node == nil {
			return
		}
		for _, e := range node.Out {
			walk(e.Callee.Func)
		}
	}
	for _, fn := range roots {
		walk(fn)
	}
	// Under CHA every type is treated as dynamically accessible, so the
	// runtime-type pass has nothing to add that the traversal has not.
	return reachable, nil, "CHA (RTA unavailable: generics)"
}

// tryRTA runs RTA, converting its panic on generic code into a false result.
// Recovering from a library panic is not something to do casually; it is done
// here because the alternative is a whole module silently outside the gate, and
// because the failure is a known, reported x/tools limitation rather than a
// signal that this program is wrong.
func tryRTA(roots []*ssa.Function) (reachable map[*ssa.Function]bool, rts []types.Type, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "deadcode: RTA unavailable (%v); falling back to CHA\n", r)
			reachable, rts, ok = nil, nil, false
		}
	}()
	res := rta.Analyze(roots, true)
	reachable = make(map[*ssa.Function]bool, len(res.Reachable))
	for fn := range res.Reachable {
		reachable[fn] = true
	}
	res.RuntimeTypes.Iterate(func(t types.Type, _ any) {
		rts = append(rts, t)
	})
	return reachable, rts, true
}

// rootsFor picks the roots RTA starts from. A module with main packages is
// entered through them; a library module is entered through its exported
// surface, because its callers are repositories this analysis cannot see.
//
// A library module may still contain main packages -- sdk/go has a code
// generator -- and those must not turn it into a binary: with library set the
// mains' main and init functions are roots beside the exported surface, rather
// than instead of it. Without that, a generator's presence collapses the root
// set to two functions and every exported SDK symbol reads as unreachable.
func rootsFor(ssaPkgs []*ssa.Package, library bool) ([]*ssa.Function, error) {
	var roots []*ssa.Function
	mains := ssautil.MainPackages(ssaPkgs)
	for _, m := range mains {
		if fn := m.Func("main"); fn != nil {
			roots = append(roots, fn)
		}
		if fn := m.Func("init"); fn != nil {
			roots = append(roots, fn)
		}
	}
	if len(mains) > 0 && !library {
		if len(roots) == 0 {
			return nil, fmt.Errorf("found %d main packages but no main function -- roots are empty", len(mains))
		}
		return roots, nil
	}

	for _, p := range ssaPkgs {
		if p == nil || !isOwn(p.Pkg.Path()) {
			continue
		}
		if fn := p.Func("init"); fn != nil {
			roots = append(roots, fn)
		}
		for _, m := range p.Members {
			switch v := m.(type) {
			case *ssa.Function:
				if v.Object() != nil && v.Object().Exported() {
					roots = append(roots, v)
				}
			case *ssa.Type:
				for _, fn := range methodsOf(p.Prog, v.Type()) {
					if fn != nil && fn.Object() != nil && fn.Object().Exported() {
						roots = append(roots, fn)
					}
				}
			}
		}
	}
	// DISCOVERY FLOOR again: an empty root set makes everything unreachable,
	// which would read as a spectacular finding and be an empty analysis.
	if len(roots) == 0 {
		return nil, fmt.Errorf("no roots found -- every symbol would be reported and none of it would mean anything")
	}
	return roots, nil
}

// reflectionRoots adds symbols that are called, but not by any call site this
// analysis can see, because a framework invokes them by name at runtime.
//
// cmd/qrdb is the case that requires it: it is a Wails desktop application, and
// its *Binding methods are invoked from JavaScript. `main` reaches the binding
// STRUCT and never names a method on it, so RTA -- correctly, given what it can
// see -- reports the entire GUI backend as unreachable. Without this the first
// run's report was 3296 symbols and mostly that.
//
// This is a DECLARATION, not an inference: the pattern is passed in from the
// Makefile, where it sits next to a comment saying which framework does the
// calling. A tool that guessed at reflection entry points would be wrong in
// both directions and silent about it.
func reflectionRoots(prog *ssa.Program, ssaPkgs []*ssa.Package, re *regexp.Regexp) []*ssa.Function {
	if re == nil {
		return nil
	}
	var roots []*ssa.Function
	matched := 0
	for _, p := range ssaPkgs {
		if p == nil || !isOwn(p.Pkg.Path()) {
			continue
		}
		for _, m := range p.Members {
			switch v := m.(type) {
			case *ssa.Function:
				if v.Object() != nil && re.MatchString(qualify(v)) {
					roots = append(roots, v)
					matched++
				}
			case *ssa.Type:
				for _, fn := range methodsOf(prog, v.Type()) {
					if fn != nil && fn.Object() != nil && re.MatchString(qualify(fn)) {
						roots = append(roots, fn)
						matched++
					}
				}
			}
		}
	}
	// A pattern that matches nothing is a stale declaration -- the binding type
	// was renamed and the exemption now silently covers nothing. Say so on
	// stderr rather than letting the report quietly grow by a few hundred.
	if matched == 0 {
		fmt.Fprintf(os.Stderr,
			"deadcode: warning: -reflect-roots %q matched no symbols; the declaration is stale\n", re)
	}
	return roots
}

// unconstructableErrors reports error types whose every constructor sits in an
// unreachable function. This is the class #243's detector cannot see: the type
// IS constructed, so that sweep scores it produced, but nothing can reach the
// construction.
func unconstructableErrors(prog *ssa.Program, own map[*ssa.Package]bool,
	reachable, unreachableFns map[*ssa.Function]bool, label string) []finding {
	// Every named error type declared in our own packages.
	declared := make(map[*types.Named]bool)
	for p := range own {
		for _, m := range p.Members {
			t, ok := m.(*ssa.Type)
			if !ok {
				continue
			}
			named, ok := t.Type().(*types.Named)
			if !ok || !implementsError(named) {
				continue
			}
			declared[named] = true
		}
	}

	// Where each is constructed, and whether any of those sites is reachable.
	constructedIn := make(map[*types.Named][]*ssa.Function)
	for fn := range allFuncs(prog) {
		if fn.Blocks == nil {
			continue
		}
		for named := range typesConstructedBy(fn) {
			if declared[named] {
				constructedIn[named] = append(constructedIn[named], fn)
			}
		}
	}

	var out []finding
	for named := range declared {
		sites := constructedIn[named]
		if len(sites) == 0 {
			// Never constructed at all -- that is #243's finding, already
			// swept, and repeating it here would double-report the same type
			// under two instruments.
			continue
		}
		constructible := false
		for _, fn := range sites {
			if reachable[fn] {
				constructible = true
				break
			}
		}
		if constructible {
			continue
		}
		var where []string
		for _, fn := range sites {
			where = append(where, qualify(fn))
		}
		sort.Strings(where)
		sym := named.Obj().Pkg().Path() + "." + named.Obj().Name()
		out = append(out, finding{
			Module: label, Scope: scopeOf(sym), Kind: "errortype", Symbol: sym,
			Reason: "constructed only in unreachable " + strings.Join(where, ", "),
		})
	}
	return out
}

// typesConstructedBy returns the named types a function allocates or composes.
func typesConstructedBy(fn *ssa.Function) map[*types.Named]bool {
	out := make(map[*types.Named]bool)
	record := func(t types.Type) {
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			out[named] = true
		}
	}
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			switch v := instr.(type) {
			case *ssa.Alloc:
				record(v.Type())
			case *ssa.MakeInterface:
				record(v.X.Type())
			}
		}
	}
	return out
}

func allFuncs(prog *ssa.Program) map[*ssa.Function]bool {
	return ssautil.AllFunctions(prog)
}

func methodsOf(prog *ssa.Program, t types.Type) []*ssa.Function {
	var out []*ssa.Function
	for _, tt := range []types.Type{t, types.NewPointer(t)} {
		ms := prog.MethodSets.MethodSet(tt)
		for i := 0; i < ms.Len(); i++ {
			out = append(out, prog.MethodValue(ms.At(i)))
		}
	}
	return out
}

func implementsError(named *types.Named) bool {
	// error's underlying type is an interface by definition of the universe
	// scope, but assert with comma-ok anyway: a panic here would take down a
	// whole analysis run over a compile-time truth, and the honest answer to
	// "the universe scope is not what I think it is" is to report nothing
	// rather than to crash.
	errIface, ok := types.Universe.Lookup("error").Type().Underlying().(*types.Interface)
	if !ok {
		return false
	}
	return types.Implements(named, errIface) || types.Implements(types.NewPointer(named), errIface)
}

func isOwn(path string) bool {
	return strings.HasPrefix(path, "github.com/jeremyhahn/go-qrdb")
}

// testSupport is a package that exists to serve tests, in non-test files.
// Nothing in it is meant to be reachable from a binary, so reporting it is
// reporting the design. These packages are still LOADED -- production code that
// only a harness reaches must still be found -- they are just not reported
// against.
//
// Named by path rather than detected, because "is this package test support"
// is a judgement about intent that a pattern cannot make. A new one has to be
// added here deliberately, which is the point.
func isTestSupport(path string) bool {
	switch {
	case strings.Contains(path, "/test/"),
		strings.HasSuffix(path, "/testutil"),
		strings.Contains(path, "/testutil/"),
		strings.HasSuffix(path, "/sqltest"),
		strings.Contains(path, "/internal/sqltest/"):
		return true
	}
	return testSupportPackages[strings.TrimPrefix(path, "github.com/jeremyhahn/go-qrdb/")]
}

// testSupportPackages are the <pkg>test packages: in-memory doubles and
// fixtures a package ships beside itself for its own tests and for downstream
// ones, after the standard library's httptest. Each is listed by hand, per the
// rule above.
var testSupportPackages = map[string]bool{
	"pkg/api/dbwire/dbwiretest":             true,
	"pkg/api/grpc/grpctest":                 true,
	"pkg/auth/authtest":                     true,
	"pkg/authfactors/authfactorstest":       true,
	"pkg/authfactors/factortest":            true,
	"pkg/ceremony/ceremonytest":             true,
	"pkg/cert/certtest":                     true,
	"pkg/clock/clocktest":                   true,
	"pkg/clusterseal/clustersealtest":       true,
	"pkg/dek/dektest":                       true,
	"pkg/index/indextest":                   true,
	"pkg/initproof/initprooftest":           true,
	"pkg/kvstore/kvstoretest":               true,
	"pkg/pin/pintest":                       true,
	"pkg/replicaforward/replicaforwardtest": true,
	"pkg/seal/sealtest":                     true,
	"pkg/service/servicetest":               true,
	"pkg/store/storetest":                   true,
	"pkg/syncwatch/syncwatchtest":           true,
	"pkg/volume/volumetest":                 true,
}

// skipFunc drops the symbols that are noise in every report of this kind:
// synthetic wrappers with no source, generated init work, and the blank
// function. Anonymous closures are dropped because they are reported through
// their parent, which is the symbol a human can act on.
//
// It also drops anything DECLARED in another module. A method set includes
// PROMOTED methods, so walking the method set of one of our types that embeds a
// go-quicraft one reports go-quicraft's methods as ours -- 3323 findings on the
// first run, the great majority of them somebody else's code. The declaring
// package is the test, not the package we found it through.
func skipFunc(fn *ssa.Function) bool {
	if fn.Object() == nil || fn.Name() == "_" || fn.Name() == "init" {
		return true
	}
	if pkg := fn.Object().Pkg(); pkg == nil || !isOwn(pkg.Path()) || isTestSupport(pkg.Path()) {
		return true
	}
	if fn.Parent() != nil {
		return true
	}
	if fn.Synthetic != "" {
		return true
	}
	if strings.HasSuffix(fn.Prog.Fset.Position(fn.Pos()).Filename, "_test.go") {
		return true
	}
	return false
}

func qualify(fn *ssa.Function) string {
	name := fn.Name()
	if recv := fn.Signature.Recv(); recv != nil {
		t := recv.Type()
		if ptr, ok := t.(*types.Pointer); ok {
			t = ptr.Elem()
		}
		if named, ok := t.(*types.Named); ok {
			name = named.Obj().Name() + "." + name
		}
	}
	if fn.Pkg != nil && fn.Pkg.Pkg != nil {
		return fn.Pkg.Pkg.Path() + "." + name
	}
	return name
}

func rootDescription(label string) string {
	if label == "sdk/go" {
		return "the SDK's exported surface"
	}
	return "either binary's main"
}

func report(findings []finding) {
	for _, f := range findings {
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%s\n", f.Module, f.Scope, f.Kind, f.Symbol, f.Reason)
	}
	fmt.Fprintf(os.Stderr, "%d finding(s)\n", len(findings))
}

// compare holds the run against the baseline. New findings fail; findings that
// have disappeared are reported and do not.
func compare(findings []finding, path string, write bool, label string) int {
	if write {
		if err := writeBaseline(findings, path, label); err != nil {
			fmt.Fprintf(os.Stderr, "deadcode: %v\n", err)
			return 2
		}
		fmt.Fprintf(os.Stdout, "wrote %d finding(s) for %s to %s\n", len(findings), label, path)
		return 0
	}

	accepted, err := readBaseline(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "deadcode: %v\n", err)
		return 2
	}

	// ONLY THE INTERNAL TIER GATES. An exported symbol may be called by a
	// repository this analysis cannot see -- go-xkms and go-quicbaas import
	// go-qrdb/pkg/... directly -- so a new exported symbol that main does not
	// reach is the normal shape of adding a downstream-facing API, not a defect.
	// Failing on those would make every such addition a build error and the gate
	// would be switched off. They are still counted and named, just not fatal.
	seen := make(map[string]bool, len(findings))
	var added, informational []finding
	for _, f := range findings {
		seen[f.key()] = true
		if accepted[f.key()] {
			continue
		}
		if f.Scope == "internal" {
			added = append(added, f)
			continue
		}
		informational = append(informational, f)
	}

	var removed []string
	for key := range accepted {
		if strings.HasPrefix(key, label+"\t") && !seen[key] {
			removed = append(removed, key)
		}
	}
	sort.Strings(removed)

	for _, key := range removed {
		parts := strings.SplitN(key, "\t", 3)
		fmt.Fprintf(os.Stdout, "gone: %s %s (baseline entry is now reachable or deleted)\n", parts[1], parts[2])
	}
	if len(removed) > 0 {
		fmt.Fprintf(os.Stdout, "\n%d baseline entr(ies) no longer apply. Refresh with:\n", len(removed))
		fmt.Fprintf(os.Stdout, "    make dead-code-baseline\n\n")
	}

	if len(informational) > 0 {
		fmt.Fprintf(os.Stdout, "note: %d new EXPORTED symbol(s) unreachable from %s.\n",
			len(informational), rootDescription(label))
		fmt.Fprintf(os.Stdout, "      Not a failure -- a downstream repository may be the caller.\n")
		for _, f := range informational {
			fmt.Fprintf(os.Stdout, "      %s\n", f.Symbol)
		}
		fmt.Fprintf(os.Stdout, "      Record them with `make dead-code-baseline` when the API is deliberate.\n\n")
	}

	if len(added) == 0 {
		fmt.Fprintf(os.Stdout, "dead-code: %s clean against baseline (%d known finding(s))\n", label, len(findings))
		return 0
	}

	fmt.Fprintf(os.Stderr, "FAIL: %d symbol(s) no other package can call, unreachable and not in the baseline:\n\n", len(added))
	for _, f := range added {
		fmt.Fprintf(os.Stderr, "  %-9s %s\n            %s\n", f.Kind, f.Symbol, f.Reason)
	}
	fmt.Fprintf(os.Stderr, `
Unreachable is a CANDIDATE, not a verdict. Four reasons a symbol legitimately
stays, and each must be stated rather than assumed:

  * it is an SDK surface a downstream repository calls (xkms, quicbaas) --
    then it belongs to sdk/go, whose exported surface is already a root here;
  * it satisfies an interface whose implementations are chosen at runtime;
  * it is reached only through reflection, a struct tag, or a plugin;
  * it is a boundary the composer injects, like a Factor or a SealStrategy.

If none of those hold, the symbol is dead: delete it, or wire it to the path
that should have been calling it -- go-qrdb#340 is the case where WIRING was
the answer and deleting would have been the defect.

Accept a finding deliberately with `+"`make dead-code-baseline`"+`, which records it
and requires the reason in the commit message.
`)
	return 1
}

func writeBaseline(findings []finding, path, label string) error {
	existing, err := readBaselineFile(path)
	if err != nil {
		return err
	}
	current := make(map[string]bool, len(findings))
	for _, f := range findings {
		current[fmt.Sprintf("%s\t%s\t%s\t%s", f.Module, f.Scope, f.Kind, f.Symbol)] = true
	}
	refreshed := existing.refresh(label, current)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(refreshed.render()), 0o600)
}

func readBaseline(path string) (map[string]bool, error) {
	lines, err := readBaselineLines(path)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(lines))
	for _, l := range lines {
		out[l] = true
	}
	return out, nil
}

func readBaselineLines(path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- path is a developer-supplied flag in a build tool, not a request input
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

// reasonPrefix opens a kept-entry group: the entry lines after it, up to the
// next comment that is not a bare "#" separator, are kept for that reason.
const reasonPrefix = "# KEEP: "

// reasonGroup is the entries kept for one stated reason.
type reasonGroup struct {
	reason  string
	entries []string
}

// baselineFile is the baseline as written: the entries no reason has been
// stated for yet, then the reasoned groups in file order.
type baselineFile struct {
	plain  []string
	groups []reasonGroup
}

func readBaselineFile(path string) (baselineFile, error) {
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304 -- path is a developer-supplied flag in a build tool, not a request input
	if os.IsNotExist(err) {
		return baselineFile{}, nil
	}
	if err != nil {
		return baselineFile{}, err
	}
	return parseBaseline(string(data)), nil
}

func parseBaseline(data string) baselineFile {
	var b baselineFile
	group := -1
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case line == "" || line == "#":
		case strings.HasPrefix(line, reasonPrefix):
			b.groups = append(b.groups, reasonGroup{reason: strings.TrimPrefix(line, reasonPrefix)})
			group = len(b.groups) - 1
		case strings.HasPrefix(line, "#"):
			group = -1
		case group >= 0:
			b.groups[group].entries = append(b.groups[group].entries, line)
		default:
			b.plain = append(b.plain, line)
		}
	}
	return b
}

// refresh replaces label's entries with current. An entry that stays keeps
// the reason it was filed under; a reason whose entries all left is dropped;
// a new entry lands among the plain ones until a reason is written for it.
func (b baselineFile) refresh(label string, current map[string]bool) baselineFile {
	ours := func(line string) bool { return strings.HasPrefix(line, label+"\t") }
	reasoned := make(map[string]bool)
	var out baselineFile
	for _, g := range b.groups {
		var kept []string
		for _, line := range g.entries {
			if !ours(line) || current[line] {
				kept = append(kept, line)
				reasoned[line] = true
			}
		}
		if len(kept) > 0 {
			out.groups = append(out.groups, reasonGroup{reason: g.reason, entries: kept})
		}
	}
	for _, line := range b.plain {
		if !ours(line) {
			out.plain = append(out.plain, line)
		}
	}
	for line := range current {
		if !reasoned[line] {
			out.plain = append(out.plain, line)
		}
	}
	return out
}

func (b baselineFile) render() string {
	var s strings.Builder
	s.WriteString(baselineHeader)
	plain := append([]string(nil), b.plain...)
	sort.Strings(plain)
	for _, line := range plain {
		s.WriteString(line + "\n")
	}
	if len(b.groups) > 0 {
		s.WriteString(reasonsPreamble)
	}
	for _, g := range b.groups {
		s.WriteString("#\n" + reasonPrefix + g.reason + "\n")
		entries := append([]string(nil), g.entries...)
		sort.Strings(entries)
		for _, line := range entries {
			s.WriteString(line + "\n")
		}
	}
	return s.String()
}

const reasonsPreamble = `#
# go-qrdb#350 -- every entry below was checked by reading its call sites, and is
# kept for the reason on the KEEP line above its group. A group that names a
# design call sits under an exported API no production path wires; wiring or
# deleting it is the owner's decision, not this baseline's. A refresh keeps each
# entry under its reason and drops a reason once all its entries have left; a new
# entry lands above, among the unreasoned ones, until a reason is written for it.
`

const baselineHeader = `# dead-code baseline (go-qrdb#337) -- symbols unreachable from production roots.
#
# Generated by: make dead-code-baseline
# Format: <module>\t<scope>\t<kind>\t<symbol>
#
# AN ENTRY HERE IS AN ACCEPTED FINDING, NOT AN ABSOLVED ONE. Every line is a
# symbol no production path reaches; it is recorded so the gate can fail on NEW
# ones rather than on the existing backlog. Working the backlog down is the
# point -- an entry that disappears is reported and is not a failure.
#
# Before accepting a new entry, state which of these applies:
#   * an SDK surface a downstream repository calls;
#   * an interface implementation chosen at runtime;
#   * reached by reflection, a struct tag, or a plugin;
#   * a composer-injected boundary (Factor, SealStrategy).
# If none does, the symbol is dead -- delete it, or WIRE it. go-qrdb#340 is the
# case where wiring was the answer and deleting would have been the defect.
`
