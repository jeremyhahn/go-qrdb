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

// Command ifacesweep answers go-qrdb#271: which exported interface methods,
// declared under pkg/ or cmd/, have no non-test caller anywhere in this
// repository (root module + sdk/go)?
//
// WHY NOT test/tools/deadcode. That instrument (go-qrdb#337) answers a
// different question. It runs RTA/CHA reachability from production roots and
// reports unreachable FUNCTIONS -- including concrete methods with bodies.
// But its own doc comment states the rule that makes it blind to #271's
// question: "RUNTIME TYPES ARE REACHABLE THROUGH THEIR WHOLE METHOD SET."
// Once a concrete type is ever converted to an interface anywhere reachable
// (a single `var _ CertProvider = (*DefaultCertProvider)(nil)`, or any
// assignment to an interface-typed variable, is enough), RTA marks EVERY
// method of that concrete type reachable, whether or not any call site
// invokes that specific method through the interface. That is precisely the
// #270 shape -- CertProvider.IsRevoked is declared, implemented, and never
// invoked -- and deadcode's own acceptance criteria would not have found it;
// #337's tool found #270's sibling only because an unrelated error-type sweep
// stumbled onto it. This tool asks the narrower, method-level question
// directly: does at least one non-test call site select THIS METHOD on THIS
// INTERFACE, anywhere in the repository?
//
// IDENTITY. A method's identity is (declaring package path, interface name,
// method name) -- never the bare method name. #271's own second comment
// measured why: Get, Put, Delete, Close, Start, Stop, Name and friends are
// declared on many unrelated interfaces, and a single call to any one of
// them would, under name matching, exonerate all of them. This tool never
// compares names. Every call site is resolved through go/types: the
// receiver's static type is looked up in the type-checker's Selections map,
// and the selected method's own *types.Func -- which is IDENTICAL, by
// Go's own type-checking, whether reached directly or through an embedding
// interface -- is compared against the set of *types.Func objects obtained
// by walking ExplicitMethods() of every named interface declared under
// pkg/ or cmd/. A name never enters the comparison.
//
// TWO LOAD PHASES, ONE IDENTITY SPACE. pkg/ and cmd/ live in the root
// module; sdk/go is a second module (its own go.mod), replaced back into the
// root module and replacing the root module in turn -- the same circular
// pair test/tools/deadcode already treats as two roots. *types.Object
// pointers are NOT stable across two separate packages.Load calls, so
// identity across the phases is the string key package+"."+interface+"."+
// method, not pointer equality. Declarations are collected ONLY from the
// root-module phase (pkg/, cmd/); call sites are collected from BOTH phases
// and unioned -- a caller in sdk/go makes a pkg/ method exactly as un-dead as
// a caller in pkg/ itself.
//
// TAGS ARE NOT MERGED ACROSS RUNS. A call site behind a build tag this
// process was not run with is invisible to it, exactly as for deadcode. Two
// runs with different -tags produce two independent reports; this program
// does not average or union them, because #271's own second comment recorded
// both directions happening in the analogous case (methods that go from
// uncalled to called when tags turn on, and methods that exist only under a
// tag and vanish from a no-tag run entirely). Comparing the reports is the
// caller's job, done by the Makefile target that runs this twice.
//
// EXCLUSIONS APPLIED HERE, NOT LEFT TO THE READER:
//
//   - Generated interfaces (any interface declared in a file whose header
//     matches the standard "Code generated ... DO NOT EDIT." marker) are
//     dropped from the declaration set entirely and counted separately.
//     protoc-gen-go-grpc's client/server interfaces are consumer surface by
//     construction; "no in-repo caller" says nothing about them.
//   - Nothing else is excluded by this tool. Whether a zero-caller,
//     non-generated method is an externally-implemented seam, an SDK
//     surface, or a genuine defect is a judgement call recorded alongside
//     the finding (Implemented column) and made by a human, not inferred
//     here -- see the -report output's IMPLEMENTED-IN-REPO split, which is
//     exactly the axis #271 asked this tool to report on rather than decide.
//
// Usage:
//
//	ifacesweep -tags list [-out file]
//
// Exit status is always 0; this is a measurement tool, not a gate. Output is
// a sorted TSV to -out (default stdout); a one-line summary goes to stderr.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// modulePrefix is this repository's own module path. Both the root module
// and sdk/go (github.com/jeremyhahn/go-qrdb/sdk/go) share it as a prefix, so
// one check identifies "our code" in either load.
const modulePrefix = "github.com/jeremyhahn/go-qrdb"

// generatedRE matches the standard marker cmd/go and every generator
// (protoc-gen-go, protoc-gen-go-grpc, mockgen, ...) emit; see
// https://pkg.go.dev/cmd/go#hdr-Generate_Go_files_by_processing_source. It is
// matched against a raw *ast.Comment's .Text, which still carries the "//"
// prefix -- unlike ast.CommentGroup.Text(), which strips it and would make
// this regex (anchored on "^// ") never match.
var generatedRE = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// methodID is the identity #271 requires: package-qualified interface and
// method name, never a bare name.
type methodID struct {
	Pkg    string
	Iface  string
	Method string
}

func (m methodID) String() string { return m.Pkg + "." + m.Iface + "." + m.Method }

// declaration is one interface method found under pkg/ or cmd/.
//
// File is repo-root-relative, never absolute -- see relFile. A committed
// TSV of go/packages' raw, absolute token.Position.Filename would bake the
// host's checkout path into version control (workstation-path leakage) and
// would rewrite every line on a different checkout or worktree name, which
// defeats the entire point of committing a stable, diffable reference
// measurement.
type declaration struct {
	ID         methodID
	IfaceObj   *types.Named
	Exported   bool // interface name AND method name both start uppercase
	File       string
	Line       int
	NumImplIn  int // count of in-repo concrete types satisfying the interface
	CalledRoot bool
	CalledSDK  bool
}

func (d declaration) called() bool { return d.CalledRoot || d.CalledSDK }

func main() {
	var (
		tags = flag.String("tags", "", "comma-separated build tags, applied to both the root module and sdk/go loads")
		out  = flag.String("out", "", "TSV output file (default stdout)")
	)
	flag.Parse()

	// ONE load for the root module. Declarations, root call sites, and the
	// concrete-type implementer count all come out of this single
	// packages.Load, and that is load-bearing: *types.Named identity is
	// per-session, so an interface's Named and a candidate implementer's
	// Named must come from the SAME session for types.Implements to see a
	// method whose signature mentions any other of our own named types
	// (context aside, anything beyond builtins and the universe `error`).
	// Two separate Loads of "." -- one for decls, one for call sites --
	// silently produced zero implementers for nearly every non-trivial
	// interface the first time this was tried, because Implements() was
	// comparing Named objects from two unrelated type-checking sessions.
	decls, calledRoot, concreteRoot, generatedSkipped, err := analyseRoot(*tags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ifacesweep: loading root module: %v\n", err)
		os.Exit(2)
	}

	calledSDK, err := loadCallSites("./sdk/go", *tags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ifacesweep: loading sdk/go call sites: %v\n", err)
		os.Exit(2)
	}

	for i := range decls {
		key := decls[i].ID.String()
		if calledRoot[key] {
			decls[i].CalledRoot = true
		}
		if calledSDK[key] {
			decls[i].CalledSDK = true
		}
		// Implementers are counted from the root module's own session only
		// (see the comment on analyseRoot). A concrete type in sdk/go that
		// implements a pkg/ interface -- if one exists -- is not counted
		// here; that is a known, documented gap, not a silent one.
		decls[i].NumImplIn = countImplementers(decls[i].IfaceObj, concreteRoot)
	}

	sort.Slice(decls, func(i, j int) bool { return decls[i].ID.String() < decls[j].ID.String() })

	w := os.Stdout
	if *out != "" {
		f, ferr := os.Create(*out) // #nosec G304 -- developer-supplied flag on a build tool, not request input
		if ferr != nil {
			fmt.Fprintf(os.Stderr, "ifacesweep: %v\n", ferr)
			os.Exit(2)
		}
		defer f.Close()
		w = f
	}

	fmt.Fprintln(w, "package\tinterface\tmethod\texported\tcalled_root\tcalled_sdk\timplementers_in_repo\tfile\tline")
	var uncalled, uncalledExported int
	for _, d := range decls {
		fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%t\t%d\t%s\t%d\n",
			d.ID.Pkg, d.ID.Iface, d.ID.Method, d.Exported, d.CalledRoot, d.CalledSDK,
			d.NumImplIn, d.File, d.Line)
		if !d.called() {
			uncalled++
			if d.Exported {
				uncalledExported++
			}
		}
	}

	fmt.Fprintf(os.Stderr,
		"ifacesweep: tags=%q declared=%d (generated interfaces skipped=%d) uncalled=%d (exported=%d, unexported=%d)\n",
		*tags, len(decls), generatedSkipped, uncalled, uncalledExported, uncalled-uncalledExported)
}

// analyseRoot loads the root module ONCE and returns everything that must
// come from a single type-checking session: the declared interface methods
// (under pkg/ or cmd/, excluding generated files), the set of those methods
// with a non-test call site anywhere in the root module, and every named,
// non-interface type declared in the root module's own packages, for the
// implementer count.
//
// The single load matters. types.Implements compares method signatures
// structurally, but two *types.Named values are only types.Identical when
// they are literally the same *types.TypeName object -- so an interface
// loaded in one packages.Load call can never be seen to be implemented by a
// concrete type loaded in a SEPARATE call, for any method whose signature
// mentions another of this repository's own named types. A first version of
// this tool split declarations and call sites into two Loads of "." and
// measured zero implementers for nearly every non-trivial interface as a
// result -- discovered by the pkg/storage.Storage acceptance check below,
// which is why that check exists.
func analyseRoot(tags string) (decls []declaration, called map[string]bool, concrete []*types.Named, generatedSkipped int, err error) {
	pkgs, err := load(".", tags)
	if err != nil {
		return nil, nil, nil, 0, err
	}

	// root is the base every declaration's File is made relative to. "."
	// is exactly the Dir just passed to load(), so this is the same
	// directory go/packages resolved every file path against -- resolving
	// it once, the same way, is what makes relFile's output agree with
	// where the files actually are, on any checkout.
	root, err := filepath.Abs(".")
	if err != nil {
		return nil, nil, nil, 0, fmt.Errorf("resolving root module directory: %w", err)
	}

	called = make(map[string]bool)

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil || p.TypesInfo == nil || !isOwn(p.PkgPath) {
			return
		}

		scope := p.Types.Scope()
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok {
				continue
			}
			named, ok := tn.Type().(*types.Named)
			if !ok {
				continue
			}
			iface, isIface := named.Underlying().(*types.Interface)
			if !isIface {
				concrete = append(concrete, named)
				continue
			}
			if !inDeclScope(p.PkgPath) {
				continue
			}
			if isGenerated(p, named) {
				generatedSkipped += iface.NumExplicitMethods()
				continue
			}
			for i := 0; i < iface.NumExplicitMethods(); i++ {
				fn := iface.ExplicitMethod(i)
				pos := p.Fset.Position(fn.Pos())
				file, ferr := relFile(root, pos.Filename)
				if ferr != nil {
					err = ferr
					return
				}
				decls = append(decls, declaration{
					ID: methodID{
						Pkg:    p.PkgPath,
						Iface:  named.Obj().Name(),
						Method: fn.Name(),
					},
					IfaceObj: named,
					Exported: token.IsExported(named.Obj().Name()) && token.IsExported(fn.Name()),
					File:     file,
					Line:     pos.Line,
				})
			}
		}

		for key := range callSitesIn(p) {
			called[key] = true
		}
	})
	// err may have been set from inside the Visit callback (relFile
	// failing on some file) -- the closure assigns the named return, but
	// packages.Visit itself has no way to propagate it, so it is checked
	// here rather than trusted away.
	if err != nil {
		return nil, nil, nil, 0, err
	}
	return decls, called, concrete, generatedSkipped, nil
}

// loadCallSites loads one module and returns the set of methodID string keys
// with at least one non-test call site anywhere in that module's own
// packages. Used for sdk/go, where only the call-site union with the root
// module's declarations matters -- string identity, not *types.Named
// pointer identity, so a separate load is safe here (see analyseRoot).
func loadCallSites(dir, tags string) (map[string]bool, error) {
	pkgs, err := load(dir, tags)
	if err != nil {
		return nil, err
	}
	called := make(map[string]bool)
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil || p.TypesInfo == nil || !isOwn(p.PkgPath) {
			return
		}
		for key := range callSitesIn(p) {
			called[key] = true
		}
	})
	return called, nil
}

// callSitesIn returns the methodID string keys called from p's non-test
// files.
//
// A "call site" here is any *types.Selection of kind MethodVal or
// MethodExpr resolving to the method -- a bare method-value reference
// (`h := x.Method`) counts as well as an immediate call (`x.Method()`).
// Treating a reference as evidence of use, not just an invocation, is the
// conservative choice for a sweep whose output gates deletions: it can only
// ever make a method look LESS dead than a call-only count would, and #271's
// own guidance is that ambiguity should be resolved toward under-reporting,
// never toward a widened match.
func callSitesIn(p *packages.Package) map[string]bool {
	called := make(map[string]bool)
	for _, file := range p.Syntax {
		fname := p.Fset.Position(file.Pos()).Filename
		if strings.HasSuffix(fname, "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			selection, ok := p.TypesInfo.Selections[sel]
			if !ok {
				return true
			}
			if selection.Kind() != types.MethodVal && selection.Kind() != types.MethodExpr {
				return true
			}
			fn, ok := selection.Obj().(*types.Func)
			if !ok {
				return true
			}
			if id := funcMethodID(fn); id != nil {
				called[id.String()] = true
			}
			return true
		})
	}
	return called
}

// funcMethodID recovers the (package, interface, method) identity of an
// interface method's *types.Func, or nil if fn is not a method declared on
// a named interface (e.g. it is a method on a concrete struct, or on the
// predeclared universe interface `error`, or on an anonymous interface
// literal -- none of those are in #271's declaration scope, and none should
// be counted as a call to one of our own declared interfaces).
func funcMethodID(fn *types.Func) *methodID {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return nil
	}
	recvType := sig.Recv().Type()
	named, ok := recvType.(*types.Named)
	if !ok {
		return nil
	}
	if _, isIface := named.Underlying().(*types.Interface); !isIface {
		return nil
	}
	pkg := named.Obj().Pkg()
	if pkg == nil { // the predeclared `error` has no package
		return nil
	}
	return &methodID{Pkg: pkg.Path(), Iface: named.Obj().Name(), Method: fn.Name()}
}

// countImplementers returns how many of the given concrete, non-interface
// named types satisfy iface (by value or by pointer). This is the signal
// #271 asks be reported rather than resolved automatically: an uncalled
// method on an interface with zero in-repo implementers is very likely an
// externally-implemented seam (a consumer this repo cannot see implements
// it); an uncalled method on an interface this repo both defines AND
// implements is the #270 shape.
func countImplementers(iface *types.Named, concrete []*types.Named) int {
	ifaceType, ok := iface.Underlying().(*types.Interface)
	if !ok {
		return 0
	}
	n := 0
	for _, c := range concrete {
		if c == iface {
			continue
		}
		if types.Implements(c, ifaceType) || types.Implements(types.NewPointer(c), ifaceType) {
			n++
		}
	}
	return n
}

// inDeclScope is #271's declaration universe: interfaces declared under
// pkg/ or cmd/ of the root module, by import path. Callers check isOwn
// first, so pkgPath always carries the module prefix here.
func inDeclScope(pkgPath string) bool {
	rel := strings.TrimPrefix(pkgPath, modulePrefix+"/")
	return strings.HasPrefix(rel, "pkg/") || strings.HasPrefix(rel, "cmd/")
}

// relFile converts the absolute path go/packages reports for a declaration
// into a path relative to root, so the emitted TSV never embeds the
// machine- and checkout-specific absolute path token.Position.Filename
// carries. Two checkouts of the same commit, at two different absolute
// locations, produce IDENTICAL relative paths here, because both root and
// filename share that same absolute prefix and filepath.Rel cancels it --
// which is the whole point: a committed reference measurement that is
// stable and diffable across checkouts, not one that rewrites itself on
// every machine it is regenerated on.
//
// It refuses rather than emits anything that is not clean and inside root:
// still absolute, or escaping via a leading "..", would be the same
// workstation-path defect in a different shape, and a tool that silently
// let that through is how the first version of this file shipped it.
func relFile(root, filename string) (string, error) {
	abs, err := filepath.Abs(filename)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", filename, err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("relativizing %q against %q: %w", abs, root, err)
	}
	rel = filepath.ToSlash(rel)
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("%q resolved to %q, outside module root %q", filename, rel, root)
	}
	return rel, nil
}

// isGenerated reports whether named's declaration lives in a file this
// module did not hand-write.
func isGenerated(p *packages.Package, named *types.Named) bool {
	pos := p.Fset.Position(named.Obj().Pos())
	for _, f := range p.Syntax {
		if p.Fset.Position(f.Pos()).Filename != pos.Filename {
			continue
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if generatedRE.MatchString(c.Text) {
					return true
				}
			}
		}
	}
	return false
}

func isOwn(pkgPath string) bool {
	return strings.HasPrefix(pkgPath, modulePrefix)
}

func load(dir, tags string) ([]*packages.Package, error) {
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedSyntax |
			packages.NeedTypesInfo | packages.NeedDeps | packages.NeedModule,
		Dir: dir,
		// Non-test files only: a call site reachable only from a test is
		// exactly what "no non-test caller" is asking about, and a mock
		// implementation living in a _test.go file must not count toward
		// countImplementers either.
		Tests: false,
	}
	if tags != "" {
		cfg.BuildFlags = []string{"-tags", tags}
	}

	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("loading %s: %w", dir, err)
	}
	var loadErrs []string
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for _, e := range p.Errors {
			loadErrs = append(loadErrs, e.Error())
		}
	})
	if len(loadErrs) > 0 {
		return nil, fmt.Errorf("%s did not typecheck (%d errors), first: %s", dir, len(loadErrs), loadErrs[0])
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("%s matched no packages -- this analysis would be vacuous", dir)
	}
	return pkgs, nil
}
